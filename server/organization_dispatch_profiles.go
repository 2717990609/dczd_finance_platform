package main

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"strings"
)

type dispatchProfileInput struct {
	ID, Name, CompanyType, CategoryID, ParentID, Status string
	ExpectedVersion                                     int64
	Targets                                             []string
}

func (s workspace) saveDispatchProfile(ctx context.Context, input dispatchProfileInput) (map[string]string, error) {
	id := input.ID
	create := id == ""
	input.Name = strings.TrimSpace(input.Name)
	if !textOK(input.Name, 100) {
		return nil, errDispatchCompanyName
	}
	if input.ExpectedVersion < 0 || input.Status != "ACTIVE" && input.Status != "INACTIVE" {
		return nil, invalid
	}
	if input.ParentID != "" {
		v, ok := canonicalSourceUUID(input.ParentID)
		if !ok {
			return nil, invalid
		}
		input.ParentID = v
	}
	if create {
		id = uuid.NewString()
		if _, ok := canonicalSourceUUID(input.CategoryID); !ok || input.Status != "ACTIVE" || input.CompanyType != "INTERNAL" && input.CompanyType != "EXTERNAL" {
			return nil, invalid
		}
	} else {
		v, ok := canonicalSourceUUID(id)
		if !ok || input.ParentID == v {
			return nil, invalid
		}
		id = v
	}
	err := s.writeOrganizationCategory(ctx, func(tx pgx.Tx, by *string) error {
		var parentWire *string
		if input.ParentID != "" {
			var cycle bool
			err := tx.QueryRow(ctx, `WITH RECURSIVE ancestors(id,parent) AS(SELECT k.unit_id,k.parent_wire_id FROM organization_dispatch_company_keys k WHERE k.unit_id=$1
    UNION SELECT k.unit_id,k.parent_wire_id FROM ancestors a JOIN organization_dispatch_company_keys k ON k.wire_id=a.parent)
    SELECT k.wire_id::text,EXISTS(SELECT FROM ancestors WHERE id=$2) FROM organization_units u JOIN organization_dispatch_company_keys k ON k.unit_id=u.id
    WHERE u.id=$1 AND u.status='ACTIVE' AND u.sales_company_type IS NOT NULL FOR SHARE OF u,k`, input.ParentID, id).Scan(&parentWire, &cycle)
			if err != nil {
				return err
			}
			if cycle {
				return dispatchProblem("上级公司不能位于本公司的下级")
			}
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM organization_units WHERE sales_company_type IS NOT NULL AND id<>$1 AND lower(btrim(name))=lower($2))`, id, input.Name).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return dispatchProblem("已有同名销售公司")
		}
		if create {
			var category bool
			if err := tx.QueryRow(ctx, "SELECT active FROM organization_sales_categories WHERE id=$1 FOR SHARE", input.CategoryID).Scan(&category); err != nil {
				return err
			}
			if !category {
				return errOrganizationCategoryTarget
			}
			_, err := tx.Exec(ctx, `INSERT INTO organization_units(id,parent_id,name,kind,status,sales_company_type,dispatch_profile_version)
    VALUES($1,nullif($2,'')::uuid,$3,'COMPANY','ACTIVE',$4,1)`, id, input.ParentID, input.Name, input.CompanyType)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_company_keys(unit_id,wire_id,parent_wire_id,targets_managed) VALUES($1,gen_random_uuid(),$2,true)`, id, parentWire); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from,recorded_at,recorded_by) VALUES($1,$2,clock_timestamp(),clock_timestamp(),$3)`, id, input.CategoryID, by); err != nil {
				return err
			}
			for _, target := range input.Targets {
				if !validDispatchTarget(target) {
					return invalid
				}
				if _, err := tx.Exec(ctx, "INSERT INTO organization_dispatch_targets VALUES($1,$2)", id, target); err != nil {
					return err
				}
			}
		} else {
			var name, status string
			var currentParent *string
			var version int64
			err := tx.QueryRow(ctx, `SELECT u.name,u.status,k.parent_wire_id::text,u.dispatch_profile_version FROM organization_units u JOIN organization_dispatch_company_keys k ON k.unit_id=u.id WHERE u.id=$1 FOR UPDATE OF u,k`, id).Scan(&name, &status, &currentParent, &version)
			if err != nil {
				return err
			}
			if input.Status == status && input.Name == name && ((parentWire == nil && currentParent == nil) || parentWire != nil && currentParent != nil && *parentWire == *currentParent) {
				return nil
			}
			if version != input.ExpectedVersion {
				return conflict
			}
			if status == "INACTIVE" && input.Status == "ACTIVE" {
				var eligible bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM organization_sales_category_history h
 JOIN organization_sales_categories c ON c.id=h.category_id AND c.active
 WHERE h.unit_id=$1 AND tstzrange(h.valid_from,h.valid_to,'[)') @> statement_timestamp())`, id).Scan(&eligible); err != nil {
					return err
				}
				if !eligible {
					return dispatchProblem("启用公司前请选择有效销售类别")
				}
			}
			if status == "ACTIVE" && input.Status == "INACTIVE" {
				var inUse bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM organization_assignments WHERE unit_id=$1 AND (valid_to IS NULL OR valid_to>now())
     UNION ALL SELECT FROM organization_assignments a JOIN organization_dispatch_units link ON link.unit_id=a.unit_id
     WHERE link.company_id=$1 AND (a.valid_to IS NULL OR a.valid_to>now())
     UNION ALL SELECT FROM organization_dispatch_company_keys k JOIN organization_units u ON u.id=k.unit_id AND u.status='ACTIVE' WHERE k.parent_wire_id=(SELECT wire_id FROM organization_dispatch_company_keys WHERE unit_id=$1)
     UNION ALL SELECT FROM organization_dispatch_links c JOIN organization_dispatch_links m ON m.target=c.target AND m.entity_type='MEMBER' AND m.external_company_id=c.external_id AND m.identity_state IN ('ACTIVE','PENDING') WHERE c.entity_type='COMPANY' AND c.entity_id=$1)`, id).Scan(&inUse); err != nil {
					return err
				}
				if inUse {
					return dispatchProblem("请先交接该公司仍有效的任职、下级公司和销售身份")
				}
				var unmanaged bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM (`+companyBusinessUsesSQL+`) u WHERE u.active
 AND NOT EXISTS(SELECT FROM organization_dispatch_links l WHERE l.entity_id=$1 AND l.entity_type='COMPANY'
  AND l.target=u.source AND l.external_id=u."externalId")
 AND NOT EXISTS(SELECT FROM organization_a2_delivery_history h JOIN organization_dispatch_company_keys k ON k.wire_id=h.entity_id
  WHERE k.unit_id=$1 AND h.kind='COMPANY' AND h.state='APPLIED' AND h.target=u.source AND h.external_id=u."externalId")
 AND NOT EXISTS(SELECT FROM organization_health_inbox a WHERE u.source='DCAI' AND a.entity_id=$1 AND a.kind='COMPANY'
  AND a.external_id=u."externalId" AND a.acknowledged_at IS NOT NULL))`, id).Scan(&unmanaged); err != nil {
					return err
				}
				if unmanaged {
					return dispatchProblem("停用前，请在公司资料里结束尚未接管的业务系统使用关系")
				}
			}
			if _, err := tx.Exec(ctx, "UPDATE organization_dispatch_company_keys SET parent_wire_id=$2 WHERE unit_id=$1", id, parentWire); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE organization_units SET name=$2,status=$3,parent_id=CASE WHEN $5 THEN nullif($4,'')::uuid ELSE parent_id END,dispatch_profile_version=dispatch_profile_version+1 WHERE id=$1`, id, input.Name, input.Status, input.ParentID, parentWire != nil || currentParent != nil); err != nil {
				return err
			}
		}
		if err := enqueueDispatchCompany(ctx, tx, id, by); err != nil {
			return err
		}
		return enqueueDraftConfiguration(ctx, tx, `SELECT id FROM periods`)
	})
	return map[string]string{"id": id}, err
}
