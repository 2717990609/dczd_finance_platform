package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	errDraftRule  = errors.New("请先发布此范围的规则")
	errDraftLimit = errors.New("当前范围订单过多，无法完成试算")
)

type calculateDraftInput struct{ PeriodID int64 }

type draftSummary struct {
	PracticeRosterID      *int64    `json:"-"`
	PracticeStartsAt      time.Time `json:"-"`
	PeriodOrdinal         int       `json:"-"`
	PracticeRosterVersion int64     `json:"practiceRosterVersion,omitempty"`
	PeriodID              int64     `json:"periodId"`
	RuleID                int64     `json:"ruleId"`
	RuleVersion           int       `json:"ruleVersion"`
	Version               int64     `json:"version"`
	Total                 int64     `json:"totalMinor"`
	Accrued               int64     `json:"accruedMinor"`
	RefundTotal           int64     `json:"refundMinor"`
	RefundCount           int       `json:"refundCount"`
	CorrectionTotal       int64     `json:"correctionMinor"`
	CorrectionCount       int       `json:"correctionCount"`
	LineCount             int       `json:"lineCount"`
	IssueCount            int       `json:"issueCount"`
	CalculatedAt          time.Time `json:"calculatedAt"`
}

const maxDraftPayments = 100000

const draftScopeSQL = `SELECT pro.period_mode, p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai',
 (p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai',coalesce(p.source_period_key,''),p.ordinal
 FROM periods p JOIN products pro ON pro.id=p.product_id WHERE p.id=$1 FOR UPDATE OF p`

const draftRulesSQL = `SELECT rv.id,rv.version,rv.effective_from,rv.components FROM periods p
 JOIN rule_versions rv ON ` + matchingRuleScope + `
 WHERE p.id=$1 AND (rv.effective_from IS NULL OR rv.effective_from<$2)
 ORDER BY ` + matchingRuleOrder

const draftPaymentsSQL = `SELECT v.id,v.fact
 FROM source_mappings m
 JOIN source_order_versions v ON v.source=m.source AND v.fact->>'domain'=m.domain AND v.fact->>'sourceKey'=m.source_key
 WHERE m.product_id=(SELECT product_id FROM periods WHERE id=$1)
   AND v.kind='ORDER_PAID' AND v.occurred_at >= $2 AND v.occurred_at < $3
   AND NOT EXISTS (
    SELECT 1 FROM source_order_versions newer
    WHERE (newer.source,newer.order_id,newer.line_id,newer.record_key,newer.kind)=(v.source,v.order_id,v.line_id,v.record_key,v.kind)
      AND (newer.updated_at,newer.id)>(v.updated_at,v.id)
   )
 ORDER BY v.source,v.order_id,v.line_id,v.record_key LIMIT $4`

func (s workspace) recalculateDraft(ctx context.Context, input calculateDraftInput) (any, error) {
	if input.PeriodID < 1 {
		return nil, invalid
	}
	summary := draftSummary{PeriodID: input.PeriodID}
	err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		window, rules, err := readDraftScope(ctx, tx, &summary)
		if err != nil {
			return err
		}
		_, err = readSettlement(ctx, tx, summary.PeriodID)
		if err == nil {
			return errSettlementClosed
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result, digest, err := computeDraft(ctx, tx, summary, window, rules)
		if err != nil {
			return err
		}
		return storeDraft(ctx, tx, &summary, digest, result)
	})
	return summary, err
}

func computeDraft(ctx context.Context, tx pgx.Tx, summary draftSummary, window periodWindow, rules []paymentRule) (paymentDraft, []byte, error) {
	if summary.PracticeRosterID != nil {
		return computePracticeDraft(ctx, tx, summary, rules[0])
	}
	facts, identities, err := readDraftInputs(ctx, tx, summary.PeriodID, window, rules)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	result, err := calculatePaymentDraft(window, rules, facts, identities)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	if err = addRefunds(ctx, tx, summary.PeriodID, window, rules, facts, &result); err != nil {
		return paymentDraft{}, nil, err
	}
	if err = addRecommendationCorrections(ctx, tx, summary.PeriodID, window, &result); err != nil {
		return paymentDraft{}, nil, err
	}
	digest, err := digestDraft(summary.RuleID, result, facts)
	return result, digest, err
}

func readDraftScope(ctx context.Context, tx pgx.Tx, summary *draftSummary) (periodWindow, []paymentRule, error) {
	var window periodWindow
	var mode, sourceKey string
	err := tx.QueryRow(ctx, draftScopeSQL, summary.PeriodID).Scan(&mode, &window.Start, &window.Through, &sourceKey, &summary.PeriodOrdinal)
	if err != nil {
		return window, nil, err
	}
	var classStart time.Time
	if mode == "class" {
		classStart, err = readPracticeHeader(ctx, tx, sourceKey, summary)
		if err != nil {
			return window, nil, err
		}
		summary.PracticeStartsAt = classStart
		if classStart.Before(window.Start) || !classStart.Before(window.Through) {
			return window, nil, errPracticeStart
		}
	}
	rows, err := tx.Query(ctx, draftRulesSQL, summary.PeriodID, window.Through)
	if err != nil {
		return window, nil, err
	}
	rules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (paymentRule, error) {
		var rule paymentRule
		if err := row.Scan(&rule.ID, &rule.Version, &rule.From, &rule.Components); err != nil {
			return rule, err
		}
		return rule, validateComponents(rule.Components)
	})
	if err != nil {
		return window, nil, err
	}
	if len(rules) == 0 {
		return window, nil, errDraftRule
	}
	if mode == "class" {
		rule := selectPaymentRule(rules, classStart)
		if rule == nil {
			return window, nil, errDraftRule
		}
		rules = []paymentRule{*rule}
	}
	summary.RuleID, summary.RuleVersion = rules[0].ID, rules[0].Version
	return window, rules, nil
}

func readDraftInputs(ctx context.Context, tx pgx.Tx, periodID int64, window periodWindow, rules []paymentRule) ([]paymentInput, map[identityKey][]identityAssignment, error) {
	rows, err := tx.Query(ctx, draftPaymentsSQL, periodID, window.Start, window.Through, maxDraftPayments+1)
	if err != nil {
		return nil, nil, err
	}
	facts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (paymentInput, error) {
		var input paymentInput
		err := row.Scan(&input.ID, &input.Fact)
		return input, err
	})
	if err != nil {
		return nil, nil, err
	}
	if len(facts) > maxDraftPayments {
		return nil, nil, errDraftLimit
	}

	selectedRoles := make(map[string]bool)
	for _, rule := range rules {
		for _, c := range rule.Components {
			selectedRoles[c.SourceRole] = true
		}
	}
	keys := make(map[identityKey]bool)
	for _, input := range facts {
		source := input.Fact.Source
		for _, role := range input.Fact.Participants {
			if selectedRoles[role.Role] {
				keys[identityKey{source, role.UserID}] = true
			}
		}
		if selectedRoles["DIRECT_REFERRER"] && input.Fact.ReferrerID != "" {
			keys[identityKey{source, input.Fact.ReferrerID}] = true
		}
	}
	identities, err := readIdentityAssignments(ctx, tx, window, keys)
	return facts, identities, err
}

func readIdentityAssignments(ctx context.Context, tx pgx.Tx, window periodWindow, keys map[identityKey]bool) (map[identityKey][]identityAssignment, error) {
	sources := make([]string, 0, len(keys))
	ids := make([]string, 0, len(keys))
	for key := range keys {
		sources, ids = append(sources, key.Source), append(ids, key.ExternalID)
	}
	rows, err := tx.Query(ctx, `SELECT i.source,i.external_id,i.id,i.person_id,i.valid_from,i.valid_to
		FROM unnest($1::text[],$2::text[]) AS wanted(source,external_id)
		JOIN identity_mappings i USING(source,external_id)
		WHERE i.valid_from<$3 AND (i.valid_to IS NULL OR i.valid_to>$4)`, sources, ids, window.Through, window.Start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	identities := make(map[identityKey][]identityAssignment, len(keys))
	for rows.Next() {
		var key identityKey
		var value identityAssignment
		if err = rows.Scan(&key.Source, &key.ExternalID, &value.ID, &value.PersonID, &value.ValidFrom, &value.ValidTo); err != nil {
			return nil, err
		}
		identities[key] = append(identities[key], value)
	}
	return identities, rows.Err()
}

func digestDraft(ruleID int64, result paymentDraft, inputs []paymentInput) ([]byte, error) {
	type stablePayment struct {
		Source, Order, Payment, Line, Record string
		OccurredAt                           time.Time
	}
	facts := make(map[int64]stablePayment, len(inputs))
	for _, input := range inputs {
		f := input.Fact
		facts[input.ID] = stablePayment{f.Source, f.OrderID, f.PaymentID, f.LineID, f.RecordKey, f.OccurredAt.UTC()}
	}
	hash := sha256.New()
	encode := json.NewEncoder(hash)
	if err := encode.Encode(ruleID); err != nil {
		return nil, err
	}
	// Display-only revisions retain the exact input references from the last equivalent calculation.
	for _, line := range result.Lines {
		fact := facts[line.FactID]
		line.FactID = 0
		if err := encode.Encode(struct {
			Payment stablePayment
			Line    draftLine
		}{fact, line}); err != nil {
			return nil, err
		}
	}
	for _, issue := range result.Issues {
		fact := facts[issue.FactID]
		if fact.Order != "" || issue.stableKey != "" {
			issue.FactID = 0
		}
		if err := encode.Encode(struct {
			Payment stablePayment
			Issue   draftIssue
			Key     string `json:",omitempty"`
		}{fact, issue, issue.stableKey}); err != nil {
			return nil, err
		}
	}
	for _, refund := range result.Refunds {
		payment := facts[refund.CurrentPaymentVersion]
		refund.CurrentPaymentVersion, refund.RefundSourceVersionID = 0, 0
		if err := encode.Encode(struct {
			Payment stablePayment
			Refund  refundLine
		}{payment, refund}); err != nil {
			return nil, err
		}
	}
	for _, correction := range result.Corrections {
		correction.SourceVersionID = 0
		if err := encode.Encode(correction); err != nil {
			return nil, err
		}
	}
	if !result.PracticeStartsAt.IsZero() {
		if err := encode.Encode(struct {
			StartsAt      time.Time
			EnrollmentIDs []int64
		}{result.PracticeStartsAt.UTC(), result.PracticeEnrollments}); err != nil {
			return nil, err
		}
	}
	return hash.Sum(nil), nil
}

func storeDraft(ctx context.Context, tx pgx.Tx, summary *draftSummary, digest []byte, result paymentDraft) error {
	err := tx.QueryRow(ctx, `INSERT INTO calculation_drafts(period_id,rule_id,version,input_digest,total_minor,line_count,issue_count,calculated_at,refund_minor,refund_count,correction_minor,correction_count,practice_roster_id)
	  VALUES($1,$2,1,$3,$4,$5,$6,now(),$7,$8,$9,$10,$11)
	  ON CONFLICT(period_id) DO UPDATE SET rule_id=excluded.rule_id,version=calculation_drafts.version+1,
	  input_digest=excluded.input_digest,total_minor=excluded.total_minor,line_count=excluded.line_count,issue_count=excluded.issue_count,
	  calculated_at=excluded.calculated_at,refund_minor=excluded.refund_minor,refund_count=excluded.refund_count,
	  correction_minor=excluded.correction_minor,correction_count=excluded.correction_count,practice_roster_id=excluded.practice_roster_id
	  WHERE calculation_drafts.input_digest<>excluded.input_digest RETURNING version,total_minor,line_count,issue_count,calculated_at,refund_minor,refund_count,correction_minor,correction_count,practice_roster_id`,
		summary.PeriodID, summary.RuleID, digest, result.Total, len(result.Lines), len(result.Issues), result.RefundTotal, len(result.Refunds), result.CorrectionTotal, len(result.Corrections), summary.PracticeRosterID).
		Scan(&summary.Version, &summary.Accrued, &summary.LineCount, &summary.IssueCount, &summary.CalculatedAt, &summary.RefundTotal, &summary.RefundCount, &summary.CorrectionTotal, &summary.CorrectionCount, &summary.PracticeRosterID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT d.version,d.total_minor,d.line_count,d.issue_count,d.calculated_at,d.refund_minor,d.refund_count,d.correction_minor,d.correction_count,
		 d.practice_roster_id,coalesce(r.source_version,0) FROM calculation_drafts d
		 LEFT JOIN practice_rosters r ON r.id=d.practice_roster_id WHERE d.period_id=$1`, summary.PeriodID).
			Scan(&summary.Version, &summary.Accrued, &summary.LineCount, &summary.IssueCount, &summary.CalculatedAt, &summary.RefundTotal, &summary.RefundCount, &summary.CorrectionTotal, &summary.CorrectionCount, &summary.PracticeRosterID, &summary.PracticeRosterVersion)
		if err != nil {
			return err
		}
		summary.Total, err = addMinor(summary.Accrued+summary.RefundTotal, summary.CorrectionTotal)
		return err
	}
	if err != nil {
		return err
	}
	if summary.Total, err = addMinor(summary.Accrued+summary.RefundTotal, summary.CorrectionTotal); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM calculation_draft_corrections WHERE period_id=$1", summary.PeriodID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM calculation_draft_refunds WHERE period_id=$1", summary.PeriodID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM calculation_draft_lines WHERE period_id=$1", summary.PeriodID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM calculation_draft_issues WHERE period_id=$1", summary.PeriodID); err != nil {
		return err
	}
	if len(result.Lines) != 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"calculation_draft_lines"},
			[]string{"period_id", "source_version_id", "component_type", "rule_id", "identity_id", "person_id", "base_minor", "amount_minor", "practice_enrollment_id"},
			pgx.CopyFromSlice(len(result.Lines), func(i int) ([]any, error) {
				l := result.Lines[i]
				var enrollment *int64
				if l.EnrollmentID != 0 {
					enrollment = &l.EnrollmentID
				}
				return []any{summary.PeriodID, l.FactID, l.Component, l.RuleID, l.IdentityID, l.PersonID, l.Base, l.Amount, enrollment}, nil
			}))
		if err != nil {
			return err
		}
	}
	if len(result.Issues) != 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"calculation_draft_issues"},
			[]string{"period_id", "source_version_id", "component_type", "code", "practice_enrollment_id"},
			pgx.CopyFromSlice(len(result.Issues), func(i int) ([]any, error) {
				issue := result.Issues[i]
				var factID *int64
				if issue.FactID != 0 {
					factID = &issue.FactID
				}
				var enrollment *int64
				if issue.EnrollmentID != 0 {
					enrollment = &issue.EnrollmentID
				}
				return []any{summary.PeriodID, factID, issue.Component, issue.Code, enrollment}, nil
			}))
	}
	if err != nil {
		return err
	}
	if len(result.Refunds) != 0 {
		if err = storeDraftRefunds(ctx, tx, summary.PeriodID, result.Refunds); err != nil {
			return err
		}
	}
	if len(result.Corrections) != 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"calculation_draft_corrections"},
			[]string{"period_id", "original_line_id", "source_version_id", "person_id", "amount_minor"},
			pgx.CopyFromSlice(len(result.Corrections), func(i int) ([]any, error) {
				c := result.Corrections[i]
				return []any{summary.PeriodID, c.OriginalLineID, c.SourceVersionID, c.PersonID, c.Amount}, nil
			}))
	}
	return err
}

func storeDraftRefunds(ctx context.Context, tx pgx.Tx, periodID int64, refunds []refundLine) error {
	type lineKey struct {
		version   int64
		component string
	}
	current := make(map[lineKey]int64)
	for _, refund := range refunds {
		if refund.CurrentPaymentVersion == 0 || len(current) != 0 {
			continue
		}
		rows, err := tx.Query(ctx, `SELECT id,source_version_id,component_type FROM calculation_draft_lines WHERE period_id=$1`, periodID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key lineKey
			var id int64
			if err = rows.Scan(&id, &key.version, &key.component); err != nil {
				rows.Close()
				return err
			}
			current[key] = id
		}
		if err = rows.Err(); err != nil {
			return err
		}
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"calculation_draft_refunds"},
		[]string{"period_id", "original_line_id", "refund_period_id", "refund_source_version_id", "amount_minor"},
		pgx.CopyFromSlice(len(refunds), func(i int) ([]any, error) {
			refund := refunds[i]
			lineID := refund.OriginalLineID
			if refund.CurrentPaymentVersion != 0 {
				lineID = current[lineKey{refund.CurrentPaymentVersion, refund.ComponentType}]
			}
			if lineID == 0 {
				return nil, errRefundAmount
			}
			return []any{periodID, lineID, refund.RefundPeriodID, refund.RefundSourceVersionID, refund.Amount}, nil
		}))
	return err
}

const draftDetailSQL = `SELECT jsonb_build_object(
 'periodId',d.period_id,'ruleId',d.rule_id,'ruleVersion',r.version,'version',d.version,'calculatedAt',d.calculated_at,
 'totalMinor',d.total_minor+d.refund_minor+d.correction_minor,'accruedMinor',d.total_minor,
 'refundMinor',d.refund_minor,'refundCount',d.refund_count,'correctionMinor',d.correction_minor,'correctionCount',d.correction_count,
 'lineCount',d.line_count,'issueCount',d.issue_count,'practiceRosterVersion',roster.source_version,
 'attendanceCount',roster.attendance_count,
 'settlement',CASE WHEN st.id IS NOT NULL THEN jsonb_build_object('id',st.id,'confirmedAt',st.confirmed_at,'draftVersion',st.draft_version) END,
 'lines',coalesce((SELECT jsonb_agg(to_jsonb(l) ORDER BY l.id) FROM (
  SELECT l.id,p.name AS "personName",l.component_type AS "component",l.base_minor AS "baseMinor",l.amount_minor AS "amountMinor",
   o.source,o.order_id AS "orderId",o.fact->>'orderNo' AS "orderNo",o.version AS "sourceVersion",rv.version AS "ruleVersion",e.source_enrollment_id AS "enrollmentId",e.membership_type AS "membershipType"
  FROM calculation_draft_lines l JOIN people p ON p.id=l.person_id JOIN source_order_versions o ON o.id=l.source_version_id
  JOIN rule_versions rv ON rv.id=l.rule_id LEFT JOIN practice_enrollments e ON e.id=l.practice_enrollment_id
  WHERE l.period_id=d.period_id AND l.id>$2 ORDER BY l.id LIMIT 50
 ) l),'[]'),
 'refunds',coalesce((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM (
  SELECT a.id,a.amount_minor AS "amountMinor",rv.fact->>'refundId' AS "refundId",rv.version AS "refundVersion",
   p.name AS "personName",o.order_id AS "originalOrderId",o.fact->>'orderNo' AS "originalOrderNo",r.version AS "ruleVersion",
   original_period.name AS "originalPeriod",refund_period.name AS "refundPeriod",posting_period.name AS "postingPeriod"
  FROM calculation_draft_refunds a
  JOIN source_order_versions rv ON rv.id=a.refund_source_version_id
  JOIN calculation_draft_lines l ON l.id=a.original_line_id JOIN people p ON p.id=l.person_id
  JOIN rule_versions r ON r.id=l.rule_id JOIN source_order_versions o ON o.id=l.source_version_id
  JOIN periods original_period ON original_period.id=l.period_id JOIN periods refund_period ON refund_period.id=a.refund_period_id
  JOIN periods posting_period ON posting_period.id=a.period_id
  WHERE a.period_id=d.period_id AND a.id>$5 ORDER BY a.id LIMIT 50
 ) a),'[]'),
 'corrections',coalesce((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM (
  SELECT c.id,c.amount_minor AS "amountMinor",v.order_id AS "orderId",v.fact->>'orderNo' AS "orderNo",
   v.version AS "sourceVersion",p.name AS "personName",old_person.name AS "originalPersonName",
   original_period.name AS "originalPeriod",r.version AS "ruleVersion"
  FROM calculation_draft_corrections c JOIN source_order_versions v ON v.id=c.source_version_id
  JOIN people p ON p.id=c.person_id JOIN calculation_draft_lines l ON l.id=c.original_line_id
  JOIN people old_person ON old_person.id=l.person_id JOIN periods original_period ON original_period.id=l.period_id
  JOIN rule_versions r ON r.id=l.rule_id
  WHERE c.period_id=d.period_id AND c.id>$6 ORDER BY c.id LIMIT 50
 ) c),'[]'),
 'issues',coalesce((SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM (
  SELECT i.id,i.component_type AS "component",i.code,o.source,o.order_id AS "orderId",e.source_enrollment_id AS "enrollmentId"
  FROM calculation_draft_issues i LEFT JOIN source_order_versions o ON o.id=i.source_version_id LEFT JOIN practice_enrollments e ON e.id=i.practice_enrollment_id
  WHERE i.period_id=d.period_id AND i.id>$3 ORDER BY i.id LIMIT 50
 ) i),'[]')
) FROM calculation_drafts d JOIN rule_versions r ON r.id=d.rule_id LEFT JOIN settlements st ON st.period_id=d.period_id LEFT JOIN practice_rosters roster ON roster.id=d.practice_roster_id WHERE d.period_id=$1 AND ($4::bigint=0 OR d.version=$4)`

func (s workspace) getDraft(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	periodID, err := strconv.ParseInt(query.Get("periodId"), 10, 64)
	if err != nil || periodID < 1 {
		respond(w, nil, invalid)
		return
	}
	cursors := [4]int64{}
	for i, name := range []string{"afterLineId", "afterIssueId", "afterRefundId", "afterCorrectionId"} {
		if value := query.Get(name); value != "" {
			cursors[i], err = strconv.ParseInt(value, 10, 64)
			if err != nil || cursors[i] < 1 {
				respond(w, nil, invalid)
				return
			}
		}
	}
	var version int64
	if value := query.Get("version"); value != "" {
		version, err = strconv.ParseInt(value, 10, 64)
		if err != nil || version < 1 {
			respond(w, nil, invalid)
			return
		}
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), draftDetailSQL, periodID, cursors[0], cursors[1], version, cursors[2], cursors[3]).Scan(&result)
	if version > 0 && errors.Is(err, pgx.ErrNoRows) {
		err = conflict
	}
	respond(w, result, err)
}
