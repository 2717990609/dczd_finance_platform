package main

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
)

var errCorrectionRecoveryStillAppointed = errors.New("原收款人当前仍有有效任职，请登记扣回")

func correctionRecipientHasActiveAssignmentSQL(personID string) string {
	return `(EXISTS(SELECT 1 FROM organization_assignments a JOIN organization_units u ON u.id=a.unit_id
 WHERE a.person_id=` + personID + ` AND u.status='ACTIVE'
  AND a.valid_from<=CURRENT_TIMESTAMP AND (a.valid_to IS NULL OR CURRENT_TIMESTAMP<a.valid_to))
 OR EXISTS(SELECT 1 FROM catalog_current_sales_appointments a
 WHERE a.person_id=` + personID + ` AND a.valid_from<=CURRENT_TIMESTAMP
  AND (a.valid_to IS NULL OR CURRENT_TIMESTAMP<a.valid_to)
  AND (a.scope<>'ORGANIZATION_UNIT' OR EXISTS(SELECT 1 FROM organization_units u
   WHERE u.id::text=a.scope_key AND u.status='ACTIVE'))))`
}

type reviewCorrectionRecoveryInput struct {
	PeriodID int64  `json:"periodId"`
	Version  int64  `json:"version"`
	IssueID  int64  `json:"issueId"`
	Outcome  string `json:"outcome"`
}

const storeCorrectionRecoverySQL = `INSERT INTO correction_recovery_decisions(original_line_id,person_id,outcome,reviewed_by)
 VALUES($1,$2,$3,$4) ON CONFLICT(original_line_id,person_id) DO UPDATE SET
 outcome=excluded.outcome,reviewed_by=excluded.reviewed_by,reviewed_at=now()
 WHERE correction_recovery_decisions.outcome<>excluded.outcome`

const pendingCorrectionRecoverySQL = `SELECT i.correction_line_id,i.recovery_person_id,i.recovery_minor
 FROM calculation_draft_issues i JOIN calculation_draft_lines line ON line.id=i.correction_line_id
 JOIN settlements booked ON booked.period_id=line.period_id JOIN periods p ON p.id=booked.period_id
 WHERE i.period_id=$1 AND i.id=$2 AND i.code='CORRECTION_RECOVERY_PENDING' AND i.recovery_minor<0
  AND (line.component_type='referral' OR line.catalog_rule_id IS NOT NULL OR line.practice_enrollment_id IS NOT NULL)
  AND line.person_id IS NOT NULL AND p.product_id=$3 AND i.recovery_person_id IS NOT NULL`

const draftRefreshCorrectionRecovery = `SELECT $1::bigint
 UNION SELECT period_id FROM calculation_draft_issues WHERE correction_line_id=$2
 UNION SELECT period_id FROM calculation_draft_corrections WHERE original_line_id=$2`

func (s workspace) reviewCorrectionRecovery(ctx context.Context, input reviewCorrectionRecoveryInput) (any, error) {
	operator := financeUser(ctx)
	if operator == nil && !s.development {
		return nil, errFinanceLogin
	}
	actor := localFinanceActor
	if operator != nil {
		actor = operator.ID
	}
	if input.PeriodID < 1 || input.Version < 1 || input.IssueID < 1 ||
		input.Outcome != "recover" && input.Outcome != "departed_unreachable" {
		return nil, invalid
	}
	err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		scope, err := readSettlementScope(ctx, tx, input.PeriodID, true)
		if err != nil {
			return err
		}
		if operator != nil {
			if err := lockSettlementOperator(ctx, tx, operator, scope.product); err != nil {
				return err
			}
		}
		if _, err := readSettlement(ctx, tx, input.PeriodID); err == nil {
			return errSettlementClosed
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if scope.Version != input.Version {
			return errSettlementStale
		}
		if err := touchSourceWrites(ctx, tx, []string{"CONFIG"}); err != nil {
			return err
		}
		if err := lockDraftInputs(ctx, tx, input.PeriodID); err != nil {
			return err
		}
		if pending, err := draftRefreshPending(ctx, tx, input.PeriodID); err != nil {
			return err
		} else if pending {
			return errDraftChanged
		}
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM configuration_options
		 WHERE group_key='correctionRecovery' AND option_key=$1 AND enabled)`, input.Outcome).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			return invalid
		}
		var lineID int64
		var pending pendingCorrectionRecovery
		if err := tx.QueryRow(ctx, pendingCorrectionRecoverySQL, input.PeriodID, input.IssueID, scope.product).Scan(&lineID, &pending.PersonID, &pending.Amount); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errSettlementStale
			}
			return err
		}
		current, err := readUnchangedDraft(ctx, tx, input.PeriodID, scope)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(current.Issues, func(issue draftIssue) bool {
			return issue.CorrectionLineID == lineID && issue.Recovery != nil && *issue.Recovery == pending
		}) {
			return errSettlementStale
		}
		if input.Outcome == "departed_unreachable" {
			var appointed bool
			if err := tx.QueryRow(ctx, `SELECT `+correctionRecipientHasActiveAssignmentSQL("$1"), pending.PersonID).
				Scan(&appointed); err != nil {
				return err
			}
			if appointed {
				return errCorrectionRecoveryStillAppointed
			}
		}
		if _, err := tx.Exec(ctx, storeCorrectionRecoverySQL, lineID, pending.PersonID, input.Outcome, actor); err != nil {
			return err
		}
		return enqueueDraftPeriods(ctx, tx, draftRefreshCorrectionRecovery, input.PeriodID, lineID)
	})
	return map[string]string{"outcome": input.Outcome}, err
}
