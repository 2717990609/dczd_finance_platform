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
}

func performanceOwner(fact sourceFact, rule *paymentRule, member string, identities map[identityKey][]identityAssignment) int64 {
	if rule == nil {
		return 0
	}
	for _, part := range rule.Components {
		if part.Type == "sales" && part.SourceRole != "" && (part.ClassMember == "" || part.ClassMember == member) {
			owner, _ := paymentBeneficiary(fact, part.SourceRole, identities)
			return owner.PersonID
		}
	}
	return 0
}

type performanceRefundOrigin struct {
	draftPerformance
	PreviouslyRefunded int64
}

const performanceRefundOriginsSQL = `SELECT q.id,a.source_version_id,coalesce(a.person_id,0),
 (p.fact->>'amountMinor')::bigint,a.company_id,a.department_id,
 coalesce((SELECT sum((rv.fact->>'amountMinor')::bigint) FROM calculation_draft_performance prior
 JOIN settlements closed ON closed.period_id=prior.period_id
 JOIN source_order_versions rv ON rv.id=prior.source_version_id
 WHERE prior.payment_version_id=a.source_version_id),0)::bigint
 FROM jsonb_to_recordset($1::jsonb) AS q(id bigint,source text,order_id text,line_id text,record_id text)
 JOIN source_order_versions p ON p.source=q.source AND p.order_id=q.order_id AND p.kind='ORDER_PAID'
  AND (q.line_id='' OR p.line_id=q.line_id)
  AND (q.record_id='' OR substr(p.record_key,strpos(p.record_key,':')+1)=q.record_id AND strpos(p.record_key,':')>0)
 JOIN calculation_draft_performance a ON a.source_version_id=p.id AND a.payment_version_id IS NULL
 JOIN settlements closed ON closed.period_id=a.period_id
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
		&origin.Organization.Company, &origin.Organization.Department, &origin.PreviouslyRefunded}, func() error {
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
			if !refundRefMatches(ref, p.Fact) {
				continue
			}
			out[ref.ID] = append(out[ref.ID], performanceRefundOrigin{draftPerformance: performance[p.ID]})
		}
	}
	return out, nil
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

func storeDraftPerformance(ctx context.Context, tx pgx.Tx, periodID int64, postings []draftPerformance) error {
	if _, err := tx.Exec(ctx, "DELETE FROM calculation_draft_performance WHERE period_id=$1", periodID); err != nil {
		return err
	}
	if len(postings) == 0 {
		return nil
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"calculation_draft_performance"},
		[]string{"period_id", "source_version_id", "payment_version_id", "person_id", "company_id", "department_id"},
		pgx.CopyFromSlice(len(postings), func(i int) ([]any, error) {
			p := postings[i]
			var payment, person *int64
			if p.PaymentVersionID != 0 {
				payment = &p.PaymentVersionID
			}
			if p.PersonID != 0 {
				person = &p.PersonID
			}
			return []any{periodID, p.SourceVersionID, payment, person, p.Organization.Company, p.Organization.Department}, nil
		}))
	return err
}
