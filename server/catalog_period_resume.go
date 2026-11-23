package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	errCatalogPeriodResumeDate   = errors.New("请选择今天或之后的完整账期起点")
	errCatalogPeriodResumeState  = errors.New("规则当前未暂停或已有其他恢复日期，请重新读取")
	errCatalogPeriodResumeTarget = errors.New("收款安排尚未就绪")
)

type catalogPeriodResumeInput struct {
	RuleID        string    `json:"ruleId"`
	EffectiveFrom time.Time `json:"effectiveFrom"`
}

func (s workspace) postCatalogPeriodResume(w http.ResponseWriter, r *http.Request) {
	actor, err := s.financeRateActor(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	submitJSON(16<<10, func(ctx context.Context, input catalogPeriodResumeInput) (map[string]time.Time, error) {
		at := input.EffectiveFrom.UTC().Truncate(time.Microsecond)
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			return resumeCatalogPeriodRule(ctx, tx, input.RuleID, at, actor)
		})
		return map[string]time.Time{"resumeFrom": at}, err
	})(w, r)
}

func resumeCatalogPeriodRule(ctx context.Context, tx pgx.Tx, id string, at time.Time, actor financeRuleActor) error {
	id, valid := canonicalSourceUUID(id)
	if !valid {
		return invalid
	}
	base, err := readCatalogPeriodVersion(ctx, tx, id)
	if err != nil {
		return err
	}
	if !actor.permits(base.ProductID, true) {
		return errRulePermission
	}
	if actor.account != nil {
		if err := lockCatalogPublisher(ctx, tx, actor.account, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT source_id FROM product_rule_packages WHERE source_id=$1::uuid FOR UPDATE`, base.packageID); err != nil {
		return err
	}
	base, err = readCatalogPeriodVersion(ctx, tx, id)
	if err != nil {
		return err
	}
	if !base.Paused && base.ResumeFrom != nil && base.ResumeFrom.Equal(at) {
		return nil
	}
	if !base.Paused {
		return errCatalogPeriodResumeState
	}
	period, err := parseCatalogPeriodRule(base.raw)
	if err != nil {
		return err
	}
	local := at.In(shanghai)
	if !validCatalogPeriodStart(local, period.Cycle) || local.Format(time.DateOnly) < time.Now().In(shanghai).Format(time.DateOnly) {
		return errCatalogPeriodResumeDate
	}
	if !base.MayPublish || at.Before(base.From) || base.Through != nil && !at.Before(*base.Through) {
		return errCatalogRateStale
	}
	active, err := catalogPeriodVersionAt(ctx, tx, base.packageID, at)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && active != id {
		return errCatalogRateStale
	}
	if err != nil {
		return err
	}
	if err := checkCatalogPeriodResumeTargets(ctx, tx, period, at); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE product_rule_packages SET paused=false,resume_from=$2,resume_recorded_by=$3 WHERE source_id=$1::uuid`,
		base.packageID, at, actor.subject); err != nil {
		return err
	}
	return enqueueDraftConfiguration(ctx, tx, `SELECT id FROM periods WHERE aggregate_package_id=$1::uuid
	 AND $2<(ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'`, base.packageID, at)
}

func checkCatalogPeriodResumeTargets(ctx context.Context, tx pgx.Tx, period catalogPeriodDefinition, at time.Time) error {
	directory, err := readCatalogAggregateDirectory(ctx, tx, []catalogAggregateRule{{Definition: period}}, at, at.Add(time.Second))
	if err != nil {
		return err
	}
	for _, item := range period.Items {
		if item.AppointmentLevel != "" {
			continue
		}
		_, target, issue := directory.groupTarget(item, at)
		if issue == "" && target.Unit != "" {
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM organization_units WHERE id=$1::uuid FOR SHARE`, target.Unit).Scan(&status); err != nil {
				return err
			}
			if status != "ACTIVE" {
				issue = "BENEFIT_ORG_INACTIVE"
			}
		}
		if issue == "" {
			continue
		}
		reason := map[string]string{
			"BENEFIT_PERSON_MISSING":       "请选择有效的收款人",
			"BENEFIT_ORG_INACTIVE":         "收款组织已停用",
			"BENEFIT_ORG_MISSING":          "缺少有效的收款组织",
			"BENEFIT_ORG_CONFLICT":         "收款组织无法唯一确定",
			"BENEFIT_POOL_OWNER_MISSING":   "恢复日缺少分配负责人",
			"BENEFIT_POOL_OWNER_CONFLICT":  "恢复日有多位分配负责人",
			"BENEFIT_POOL_MEMBERS_MISSING": "恢复日没有可分配成员",
		}[issue]
		return fmt.Errorf("%w，%s：%s", errCatalogPeriodResumeTarget, item.Label, reason)
	}
	return nil
}
