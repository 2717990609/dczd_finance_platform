package main

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type performanceRefundKey struct {
	Source, Domain, ID string
	OccurredAt         time.Time
}

type draftPerformance struct {
	SourceVersionID, PaymentVersionID, PersonID, Principal int64
	Organization                                           reportOrganization
	Refund                                                 *performanceRefundKey `json:",omitempty"`
	CompletionVersionID                                    int64                 `json:",omitempty"`
	PriorRefundVersionID                                   int64                 `json:",omitempty"`
}

const performancePostingMinorSQL = `CASE WHEN a.payment_version_id IS NULL
 THEN (v.fact->>'amountMinor')::bigint-coalesce((prior.fact->>'amountMinor')::bigint,0)
 ELSE -(v.fact->>'amountMinor')::bigint END`

func performanceOwner(fact sourceFact, rule *paymentRule, member string, identities map[identityKey][]identityAssignment) int64 {
	if rule == nil {
		return 0
	}
	for _, part := range rule.Components {
		if part.Type == "sales" && part.SourceRole != "" && part.matchesPracticeMember(member) {
			owner, _ := paymentBeneficiary(fact, part.SourceRole, identities)
			return owner.PersonID
		}
	}
	return 0
}

type performanceRefundOrigin struct {
	draftPerformance
	PreviouslyRefunded int64
	PeriodID           int64
	PracticeReplay     bool
}

const performanceRefundOriginsSQL = `SELECT q.id,a.source_version_id,coalesce(a.person_id,0),
 (p.fact->>'amountMinor')::bigint-coalesce((previous_refund.fact->>'amountMinor')::bigint,0),a.company_id,a.department_id,
 coalesce((SELECT sum((rv.fact->>'amountMinor')::bigint) FROM calculation_draft_performance prior
 JOIN settlements closed ON closed.period_id=prior.period_id
 JOIN source_order_versions rv ON rv.id=prior.source_version_id
 WHERE prior.payment_version_id=a.source_version_id),0)::bigint,
 a.period_id,` + practiceRefundReplaySQL + `,a.group_id
 ` + refundReferencePaymentsSQL + `
 JOIN calculation_draft_performance a ON a.source_version_id=p.id AND a.payment_version_id IS NULL
 LEFT JOIN source_order_versions previous_refund ON previous_refund.id=a.prior_refund_version_id
 JOIN settlements closed ON closed.period_id=a.period_id
 JOIN calculation_drafts d ON d.period_id=a.period_id
 JOIN periods original ON original.id=a.period_id
 WHERE original.product_id=(SELECT product_id FROM periods WHERE id=$2)`

func loadPerformanceRefundOrigins(ctx context.Context, tx pgx.Tx, periodID int64, refs []refundReference,
	payments []paymentInput, draft []draftPerformance) (map[int64][]performanceRefundOrigin, error) {
	out := make(map[int64][]performanceRefundOrigin, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, performanceRefundOriginsSQL, refs, periodID)
	if err != nil {
		return nil, err
	}
	var refundID int64
	var origin performanceRefundOrigin
	_, err = pgx.ForEachRow(rows, []any{&refundID, &origin.SourceVersionID, &origin.PersonID, &origin.Principal,
		&origin.Organization.Company, &origin.Organization.Department, &origin.PreviouslyRefunded, &origin.PeriodID, &origin.PracticeReplay, &origin.Organization.Group}, func() error {
		out[refundID] = append(out[refundID], origin)
		return nil
	})
	if err != nil {
		return nil, err
	}

	performance := make(map[int64]draftPerformance, len(draft))
	for _, row := range draft {
		performance[row.SourceVersionID] = row
	}
	type orderKey struct{ source, order string }
	currentByOrder := make(map[orderKey][]paymentInput)
	for _, p := range payments {
		if _, ok := performance[p.ID]; ok {
			key := orderKey{p.Fact.Source, p.Fact.OrderID}
			currentByOrder[key] = append(currentByOrder[key], p)
		}
	}
	for _, ref := range refs {
		for _, p := range currentByOrder[orderKey{ref.Source, ref.OrderID}] {
			if !ref.matchesPayment(p.Fact) {
				continue
			}
			out[ref.ID] = append(out[ref.ID], performanceRefundOrigin{draftPerformance: performance[p.ID]})
		}
	}
	return out, nil
}

func (ref refundReference) matchesPayment(fact sourceFact) bool {
	return ref.Domain == fact.Domain && ref.SourceKey == fact.SourceKey &&
		!fact.OccurredAt.After(ref.OccurredAt) && refundRefMatches(ref, fact)
}

func refundRefMatches(ref refundReference, fact sourceFact) bool {
	_, record, _ := strings.Cut(fact.RecordKey, ":")
	return (ref.RecordID == "" || ref.RecordID == record) && (ref.LineID == "" || ref.LineID == fact.LineID)
}

func recordPerformanceRefund(draft *paymentDraft, event refundInput, origins []performanceRefundOrigin, credited map[int64]int64) string {
	if len(origins) == 0 {
		return "REFUND_PERFORMANCE_PAYMENT_MISSING"
	}
	if len(origins) > 1 {
		return "REFUND_PAYMENT_AMBIGUOUS"
	}
	origin := origins[0]
	remaining := origin.Principal - origin.PreviouslyRefunded - credited[origin.SourceVersionID]
	principal := *event.Fact.AmountMinor
	if principal > remaining {
		return "REFUND_AMOUNT_EXCEEDS_PAYMENT"
	}
	credited[origin.SourceVersionID] += principal
	draft.Performance = append(draft.Performance, draftPerformance{
		SourceVersionID: event.ID, PaymentVersionID: origin.SourceVersionID, PersonID: origin.PersonID,
		Principal: principal, Organization: origin.Organization,
		Refund: &performanceRefundKey{event.Fact.Source, event.Fact.Domain, event.Fact.RefundID, event.Fact.OccurredAt.UTC()},
	})
	return ""
}

const draftPerformanceTable = "calculation_draft_performance"
const draftPerformanceInput = "draft_performance_input"

var draftPerformanceFields = strings.Fields(`period_id source_version_id payment_version_id person_id company_id
 department_id completion_version_id prior_refund_version_id group_id`)

func storeDraftPerformance(ctx context.Context, tx pgx.Tx, summary *draftSummary, postings []draftPerformance) error {
	if len(postings) == 0 {
		_, err := tx.Exec(ctx, "DELETE FROM "+draftPerformanceTable+" WHERE period_id=$1", summary.PeriodID)
		return err
	}
	if summary.Version == 1 {
		return copyDraftPerformance(ctx, tx, pgx.Identifier{draftPerformanceTable}, summary.PeriodID, postings)
	}

	columns := strings.Join(draftPerformanceFields, ",")
	stage := "pg_temp." + draftPerformanceInput
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE `+draftPerformanceInput+` ON COMMIT DROP AS
	 SELECT `+columns+` FROM `+draftPerformanceTable+` WITH NO DATA`); err != nil {
		return err
	}
	if err := copyDraftPerformance(ctx, tx, pgx.Identifier{"pg_temp", draftPerformanceInput}, summary.PeriodID, postings); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE UNIQUE INDEX ON `+stage+`(source_version_id)`); err != nil {
		return err
	}
	key := `stored.period_id=incoming.period_id AND stored.source_version_id=incoming.source_version_id`
	stored := "stored." + strings.Join(draftPerformanceFields, ",stored.")
	incoming := "incoming." + strings.Join(draftPerformanceFields, ",incoming.")
	if _, err := tx.Exec(ctx, `DELETE FROM `+draftPerformanceTable+` stored WHERE stored.period_id=$1
	 AND NOT EXISTS(SELECT 1 FROM `+stage+` incoming WHERE `+key+`
	  AND ROW(`+stored+`) IS NOT DISTINCT FROM ROW(`+incoming+`))`, summary.PeriodID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO `+draftPerformanceTable+` (`+columns+`)
	 SELECT `+incoming+` FROM `+stage+` incoming
	 WHERE NOT EXISTS(SELECT 1 FROM `+draftPerformanceTable+` stored WHERE `+key+`)`)
	return err
}

func copyDraftPerformance(ctx context.Context, tx pgx.Tx, table pgx.Identifier, periodID int64, postings []draftPerformance) error {
	_, err := tx.CopyFrom(ctx, table, draftPerformanceFields,
		pgx.CopyFromSlice(len(postings), func(i int) ([]any, error) {
			p := postings[i]
			return []any{periodID, p.SourceVersionID, optionalDraftID(p.PaymentVersionID), optionalDraftID(p.PersonID),
				p.Organization.Company, p.Organization.Department, optionalDraftID(p.CompletionVersionID),
				optionalDraftID(p.PriorRefundVersionID), p.Organization.groupID()}, nil
		}))
	return err
}
