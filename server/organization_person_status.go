package main

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

type dispatchPersonStatusInput struct {
	PersonID        int64
	Status          string
	ExpectedVersion *int64
}

func lockActiveDirectoryPerson(ctx context.Context, tx pgx.Tx, personID int64) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT directory_visible AND NOT directory_disabled AND coalesce(a2_status,'ACTIVE')='ACTIVE' AND a2_canonical_party IS NULL
 FROM people WHERE id=$1 FOR NO KEY UPDATE`, personID).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return dispatchProblem("请选择在用的财务人员")
	}
	return nil
}

func (s workspace) setDispatchPersonStatus(ctx context.Context, in dispatchPersonStatusInput) (map[string]string, error) {
	if in.PersonID < 1 || in.ExpectedVersion == nil || *in.ExpectedVersion < 0 || (in.Status != "ACTIVE" && in.Status != "INACTIVE") {
		return nil, invalid
	}
	disable := in.Status == "INACTIVE"
	err := s.writeAppointments(ctx, func(tx pgx.Tx, _ *string) error {
		var localDisabled, importedInactive, merged bool
		var version int64
		if err := tx.QueryRow(ctx, `SELECT directory_disabled,coalesce(a2_status,'ACTIVE')<>'ACTIVE',
   a2_canonical_party IS NOT NULL,directory_status_version FROM people WHERE id=$1 AND directory_visible FOR NO KEY UPDATE`,
			in.PersonID).Scan(&localDisabled, &importedInactive, &merged, &version); err != nil {
			return err
		}
		if importedInactive || merged {
			return dispatchProblem("原系统已停用或已合并的人员，不能在此变更状态")
		}
		if localDisabled == disable {
			return nil
		}
		if *in.ExpectedVersion != version {
			return dispatchProblem("人员状态已变化，请刷新后重试")
		}
		if disable {
			if err := checkPersonBeforeDisable(ctx, tx, in.PersonID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE people SET directory_disabled=$2,directory_status_version=directory_status_version+1 WHERE id=$1`, in.PersonID, disable)
		return err
	})
	return map[string]string{"status": in.Status}, err
}

func checkPersonBeforeDisable(ctx context.Context, tx pgx.Tx, personID int64) error {
	var assignments, duties, positions, identities, pending bool
	err := tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT FROM organization_assignments WHERE person_id=$1 AND tstzrange(valid_from,valid_to,'[)') @> statement_timestamp()),
		EXISTS(SELECT FROM organization_business_duties WHERE person_id=$1 AND tstzrange(valid_from,valid_to,'[)') @> statement_timestamp()),
		EXISTS(SELECT FROM catalog_current_sales_appointments WHERE person_id=$1 AND tstzrange(valid_from,valid_to,'[)') @> statement_timestamp()),
  EXISTS(SELECT FROM organization_dispatch_links WHERE person_id=$1 AND entity_type='MEMBER' AND identity_state IN ('ACTIVE','PENDING')),
  EXISTS(SELECT FROM organization_dispatch_links l JOIN LATERAL (
   SELECT state FROM organization_dispatch_outbox q WHERE q.target=l.target AND q.entity_type='MEMBER' AND q.entity_id=l.entity_id
   ORDER BY q.version DESC LIMIT 1
  ) last ON true WHERE l.person_id=$1 AND l.entity_type='MEMBER' AND last.state NOT IN ('APPLIED','STOPPED'))`,
		personID).Scan(&assignments, &duties, &positions, &identities, &pending)
	if err != nil {
		return err
	}
	var blockers []string
	if assignments {
		blockers = append(blockers, "组织任职")
	}
	if duties {
		blockers = append(blockers, "业务职责")
	}
	if positions {
		blockers = append(blockers, "分润岗位")
	}
	if identities {
		blockers = append(blockers, "子系统销售身份")
	}
	if pending {
		blockers = append(blockers, "待处理的身份下发")
	}
	if len(blockers) > 0 {
		return dispatchProblem("停用前请先处理：" + strings.Join(blockers, "、"))
	}
	return nil
}
