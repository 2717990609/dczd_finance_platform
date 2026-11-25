package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"slices"
	"time"
)

//go:embed organization_dispatch.sql
var organizationDispatchSchema string
var errDispatchPersonName = errors.New("人员姓名须为 1 至 25 个字符")
var errDispatchCategoryName = errors.New("销售类别名称须为 1 至 10 个字符")
var errDispatchCompanyName = errors.New("公司或组织名称须为 1 至 100 个字符")
var errOrganizationDispatch = errors.New("组织同步未完成")

func dispatchProblem(message string) error {
	return fmt.Errorf("%w：%s", errOrganizationDispatch, message)
}
func lockDispatchEntity(ctx context.Context, tx pgx.Tx, target, kind, id string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "org-sync:"+target+":"+kind+":"+id)
	return err
}
func enqueueDispatch(ctx context.Context, tx pgx.Tx, target, kind, id, typ string, payload map[string]any, at *time.Time, by *string) (string, error) {
	if err := lockDispatchEntity(ctx, tx, target, kind, id); err != nil {
		return "", err
	}
	if _, ok := payload["ai_base_revision"]; !ok && target == "DCAI" && kind != "CATEGORY" {
		member, _ := payload["external_member_id"].(string)
		revision, err := organizationHealthDispatchBase(ctx, tx, kind, id, member)
		if err != nil {
			return "", err
		}
		payload["ai_base_revision"] = revision
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if kind == "COMPANY" || kind == "CATEGORY" {
		if _, err := tx.Exec(ctx, `UPDATE organization_dispatch_outbox SET state='STOPPED',reason='SUPERSEDED'
           WHERE target=$1 AND entity_type=$2 AND entity_id=$3 AND state IN ('PENDING','RETRYABLE','WAIT_CATEGORY','WAIT_PARENT_COMPANY')`, target, kind, id); err != nil {
			return "", err
		}
	}
	var result string
	err = tx.QueryRow(ctx, `INSERT INTO organization_dispatch_outbox(target,entity_type,entity_id,message_type,payload,effective_at,next_attempt_at,actor)
 VALUES($1,$2,$3,$4,$5,coalesce($6,now()),greatest(now(),$6),$7) RETURNING id::text`, target, kind, id, typ, raw, at, by).Scan(&result)
	return result, err
}
func protectedDispatchCategory(name string) bool {
	return name == "健康大使" || name == "健康创客"
}
func enqueueDispatchCategory(ctx context.Context, tx pgx.Tx, cat organizationCategory, by *string) error {
	if protectedDispatchCategory(cat.Name) {
		return dispatchProblem("健康大使和健康创客类别由大春 AI 维护")
	}
	status := "INACTIVE"
	if cat.Active {
		status = "ACTIVE"
	}
	for _, target := range organizationDispatchSystems {
		if _, err := enqueueDispatch(ctx, tx, target, "CATEGORY", cat.ID, "sale_category_upsert", map[string]any{"name": cat.Name, "status": status}, nil, by); err != nil {
			return err
		}
	}
	return nil
}
func companyTargets(ctx context.Context, tx pgx.Tx, id string) ([]string, error) {
	var targets []string
	err := tx.QueryRow(ctx, `SELECT array(SELECT target FROM organization_dispatch_targets WHERE company_id=$1 ORDER BY target)`, id).Scan(&targets)
	return targets, err
}
func enqueueDispatchCompany(ctx context.Context, tx pgx.Tx, id string, by *string) error {
	targets, err := companyTargets(ctx, tx, id)
	if err != nil || len(targets) == 0 {
		return err
	}
	return enqueueDispatchCompanyTargets(ctx, tx, id, targets, by)
}
func enqueueDispatchCompanyTargets(ctx context.Context, tx pgx.Tx, id string, targets []string, by *string) error {
	var name, status, kind string
	var wire, parentWire, category, categoryName *string
	var active *bool
	err := tx.QueryRow(ctx, `SELECT u.name,u.status,u.sales_company_type,k.wire_id::text,k.parent_wire_id::text,c.id::text,c.name,c.active
 FROM organization_units u LEFT JOIN organization_dispatch_company_keys k ON k.unit_id=u.id

 LEFT JOIN LATERAL(SELECT category_id FROM organization_sales_category_history h WHERE h.unit_id=u.id
  AND tstzrange(h.valid_from,h.valid_to,'[)') @> clock_timestamp() ORDER BY h.valid_from DESC LIMIT 1)h ON true
 LEFT JOIN organization_sales_categories c ON c.id=h.category_id WHERE u.id=$1 AND u.sales_company_type IS NOT NULL`, id).Scan(&name, &status, &kind, &wire, &parentWire, &category, &categoryName, &active)
	if err != nil {
		return err
	}
	if wire == nil {
		return dispatchProblem("请先导入已核对的销售公司主体编号和上级公司编号")
	}
	if !textOK(name, 100) {
		return errDispatchCompanyName
	}
	if categoryName != nil && !textOK(*categoryName, 10) {
		return errDispatchCategoryName
	}
	if category == nil || kind == "UNKNOWN" || status == "ACTIVE" && !*active {
		return dispatchProblem("请选择有效销售类别并确认公司的内外部类型")
	}
	for _, target := range targets {
		if !protectedDispatchCategory(*categoryName) {
			if err := lockDispatchEntity(ctx, tx, target, "CATEGORY", *category); err != nil {
				return err
			}
			var exists bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_dispatch_outbox WHERE target=$1 AND entity_type='CATEGORY' AND entity_id=$2 AND state!='STOPPED'
    UNION ALL SELECT 1 FROM organization_dispatch_links WHERE target=$1 AND entity_type='CATEGORY' AND entity_id=$2)`, target, *category).Scan(&exists)
			if err != nil {
				return err
			}
			if !exists {
				status := "INACTIVE"
				if *active {
					status = "ACTIVE"
				}
				if _, err := enqueueDispatch(ctx, tx, target, "CATEGORY", *category, "sale_category_upsert", map[string]any{"name": *categoryName, "status": status}, nil, by); err != nil {
					return err
				}
			}
		}
		payload := map[string]any{"name": name, "status": status, "category_id": *category, "parent_company_id": parentWire, "company_type": kind}
		if _, err := enqueueDispatch(ctx, tx, target, "COMPANY", id, "sale_company_upsert", payload, nil, by); err != nil {
			return err
		}
	}
	return nil
}

type dispatchTargetInput struct {
	CompanyID                string
	Targets, ExpectedTargets []string
}

func (s workspace) saveDispatchTargets(ctx context.Context, input dispatchTargetInput) ([]string, error) {
	id, ok := canonicalSourceUUID(input.CompanyID)
	if !ok || input.Targets == nil || input.ExpectedTargets == nil {
		return nil, invalid
	}
	for _, list := range [][]string{input.Targets, input.ExpectedTargets} {
		slices.Sort(list)
		for i, target := range list {
			if !validDispatchTarget(target) || i > 0 && list[i-1] == target {
				return nil, invalid
			}
		}
	}
	err := s.writeOrganizationCategory(ctx, func(tx pgx.Tx, by *string) error {
		var unit string
		if err := tx.QueryRow(ctx, "SELECT id::text FROM organization_units WHERE id=$1 AND sales_company_type IS NOT NULL FOR UPDATE", id).Scan(&unit); err != nil {
			return err
		}
		current, err := companyTargets(ctx, tx, id)
		if err != nil {
			return err
		}
		if slices.Equal(current, input.Targets) {
			return nil
		}
		if !slices.Equal(current, input.ExpectedTargets) {
			return conflict
		}
		if _, err := tx.Exec(ctx, "UPDATE organization_dispatch_company_keys SET targets_managed=true WHERE unit_id=$1", id); err != nil {
			return err
		}
		for _, target := range current {
			if slices.Contains(input.Targets, target) {
				continue
			}
			if err := lockDispatchEntity(ctx, tx, target, "COMPANY", id); err != nil {
				return err
			}
			if target == "DCAI" {
				n, e := organizationHealthDispatchBase(ctx, tx, "COMPANY", id, "")
				if e != nil {
					return e
				}
				if n > 0 {
					return dispatchProblem("大春 AI 原生公司的同步目标须保留")
				}
			}
			var processing bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_dispatch_outbox WHERE target=$1 AND entity_type='COMPANY' AND entity_id=$2 AND state='PROCESSING')`, target, id).Scan(&processing); err != nil {
				return err
			}
			if processing {
				return dispatchProblem("当前消息投递中，请稍后调整")
			}
			if _, err := tx.Exec(ctx, "DELETE FROM organization_dispatch_targets WHERE company_id=$1 AND target=$2", id, target); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE organization_dispatch_outbox SET state='STOPPED',reason='TARGET_REMOVED' WHERE target=$1 AND entity_type='COMPANY' AND entity_id=$2 AND state NOT IN ('APPLIED','STOPPED','CONFLICT')`, target, id); err != nil {
				return err
			}
		}
		var added []string
		for _, target := range input.Targets {
			if !slices.Contains(current, target) {
				if _, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_targets(company_id,target) VALUES($1,$2)`, id, target); err != nil {
					return err
				}
				added = append(added, target)
			}
		}
		if len(added) == 0 {
			return nil
		}
		return enqueueDispatchCompanyTargets(ctx, tx, id, added, by)
	})
	return input.Targets, err
}
