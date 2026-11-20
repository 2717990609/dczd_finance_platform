package main

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

const draftLineTable = "calculation_draft_lines"
const draftLineInput = "draft_lines_input"

var draftLineFields = strings.Fields(`period_id source_version_id component_type rule_id identity_id person_id
 base_minor amount_minor practice_enrollment_id company_id department_id catalog_rule_id beneficiary_unit_id
 allocation_owner_id applied_rate_bps practice_pool_id group_id aggregate_group_key practice_tier_reference`)

const sameDraftLineKey = `stored.period_id=incoming.period_id AND stored.source_version_id=incoming.source_version_id
 AND stored.component_type=incoming.component_type`

func storeDraftLines(ctx context.Context, tx pgx.Tx, summary *draftSummary, lines []draftLine) error {
	if summary.Version == 1 && len(lines) != 0 {
		return copyDraftLines(ctx, tx, summary.PeriodID, lines, false)
	}
	if len(lines) == 0 {
		_, err := tx.Exec(ctx, "DELETE FROM calculation_draft_lines WHERE period_id=$1", summary.PeriodID)
		return err
	}

	columns := strings.Join(draftLineFields, ",")
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE `+draftLineInput+` ON COMMIT DROP AS
	 SELECT `+columns+`,0::integer AS input_position FROM calculation_draft_lines WITH NO DATA`); err != nil {
		return err
	}
	if err := copyDraftLines(ctx, tx, summary.PeriodID, lines, true); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE UNIQUE INDEX ON pg_temp.`+draftLineInput+`(period_id,source_version_id,component_type)`); err != nil {
		return err
	}
	stored := "stored." + strings.Join(draftLineFields, ",stored.")
	incoming := "incoming." + strings.Join(draftLineFields, ",incoming.")
	same := sameDraftLineKey + ` AND ROW(` + stored + `) IS NOT DISTINCT FROM ROW(` + incoming + `)`

	// Pool credits use line IDs to break ties at the same payment instant.
	// Recreate that ordered set when a pool credit changes or moves in it.
	var replacePools bool
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT array_agg(id ORDER BY id)
	 FROM calculation_draft_lines WHERE period_id=$1 AND allocation_owner_id IS NOT NULL),'{}'::bigint[])
	 IS DISTINCT FROM coalesce((SELECT array_agg(stored.id ORDER BY incoming.input_position)
	 FROM pg_temp.`+draftLineInput+` incoming LEFT JOIN calculation_draft_lines stored ON `+same+`
	 WHERE incoming.allocation_owner_id IS NOT NULL),'{}'::bigint[])`, summary.PeriodID).Scan(&replacePools); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM calculation_draft_lines stored WHERE stored.period_id=$1
	 AND ($2 AND stored.allocation_owner_id IS NOT NULL OR NOT EXISTS (
	 SELECT 1 FROM pg_temp.`+draftLineInput+` incoming WHERE `+same+`))`, summary.PeriodID, replacePools); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO calculation_draft_lines (`+columns+`) SELECT `+incoming+`
	 FROM pg_temp.`+draftLineInput+` incoming WHERE NOT EXISTS (
	 SELECT 1 FROM calculation_draft_lines stored WHERE `+sameDraftLineKey+`)
	 ORDER BY incoming.input_position`)
	return err
}

func copyDraftLines(ctx context.Context, tx pgx.Tx, periodID int64, lines []draftLine, staging bool) error {
	table, columns := pgx.Identifier{draftLineTable}, draftLineFields
	if staging {
		table = pgx.Identifier{"pg_temp", draftLineInput}
		columns = append(append([]string(nil), draftLineFields...), "input_position")
	}
	_, err := tx.CopyFrom(ctx, table, columns, pgx.CopyFromSlice(len(lines), func(i int) ([]any, error) {
		l := lines[i]
		var benefit catalogDraftBenefit
		var appliedRate *int64
		var practicePoolID int64
		if l.Catalog != nil {
			benefit = *l.Catalog
			appliedRate = &benefit.Rate
		}
		if l.BeneficiaryUnit != nil {
			benefit.Unit = *l.BeneficiaryUnit
		}
		if l.PracticePool != nil {
			practicePoolID, benefit.AllocationOwnerID = l.PracticePool.PoolID, l.PracticePool.OwnerID
		}
		var aggregateGroup *string
		if l.AggregateGroup != "" {
			aggregateGroup = &l.AggregateGroup
		}
		values := []any{periodID, l.FactID, l.Component, optionalDraftID(l.RuleID), optionalDraftID(l.IdentityID),
			optionalDraftID(l.PersonID), l.Base, l.Amount, optionalDraftID(l.EnrollmentID), l.organization.Company, l.organization.Department,
			benefit.Rule, benefit.Unit, optionalDraftID(benefit.AllocationOwnerID), appliedRate, optionalDraftID(practicePoolID), l.organization.groupID(),
			aggregateGroup, l.PracticeTierReference}
		if staging {
			values = append(values, i)
		}
		return values, nil
	}))
	return err
}
