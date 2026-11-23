package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	errDraftRule  = errors.New("请先发布此范围的规则")
	errDraftLimit = errors.New("当前范围订单过多，无法完成试算")
)

type calculateDraftInput struct{ PeriodID int64 }

type draftSummary struct {
	Catalog               bool      `json:"catalog,omitempty"`
	PracticeRefundMonth   bool      `json:"practiceRefundMonth,omitempty"`
	PracticeRosterID      *int64    `json:"-"`
	PracticeStartsAt      time.Time `json:"-"`
	AggregatePackage      *string   `json:"aggregatePackageId,omitempty"`
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

const draftScopeSQL = `SELECT pro.id,pro.period_mode, p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai',
 (p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai',coalesce(p.source_period_key,''),p.ordinal,
 EXISTS(SELECT 1 FROM product_catalog_settings l WHERE l.product_id=pro.id),` + catalogDraftAvailableSQL + `,
 (pro.period_mode='class' AND p.calendar_month IS NOT NULL),p.aggregate_package_id::text
 FROM periods p JOIN products pro ON pro.id=p.product_id WHERE p.id=$1`

const draftRulesSQL = `SELECT rv.id,rv.version,rv.effective_from,stopped.at,rv.components FROM periods p
 JOIN rule_versions rv ON ` + matchingRuleScope + `
 CROSS JOIN LATERAL (SELECT ` + componentRuleRetiredAtSQL + ` AS at) stopped
 WHERE p.id=$1 AND (rv.effective_from IS NULL OR rv.effective_from<$2)
 AND (stopped.at IS NULL OR greatest($3::timestamptz,rv.effective_from)<stopped.at)
 ORDER BY ` + matchingRuleOrder

const draftPaymentsSQL = `SELECT v.id,v.fact,coalesce(company.company_id,'')
 FROM source_mappings m
 JOIN source_order_versions v ON v.source=m.source AND v.fact->>'domain'=m.domain AND v.fact->>'sourceKey'=m.source_key
 LEFT JOIN source_payment_companies company ON company.source_version_id=v.id
 WHERE m.product_id=(SELECT product_id FROM periods WHERE id=$1)
   AND v.kind='ORDER_PAID' AND v.occurred_at >= $2 AND v.occurred_at < $3
   AND NOT ` + catalogSourceReservedSQL + `
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
	return s.calculateCurrentDraft(ctx, input.PeriodID, 0)
}

func (s workspace) calculateCurrentDraft(ctx context.Context, periodID, wantedGeneration int64) (draftSummary, error) {
	summary := draftSummary{PeriodID: periodID}
	err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		lock := `SELECT id FROM periods WHERE id=$1 FOR NO KEY UPDATE`
		if wantedGeneration != 0 {
			lock += ` NOWAIT`
		}
		var locked int64
		if err := tx.QueryRow(ctx, lock, periodID).Scan(&locked); err != nil {
			return err
		}
		var generation int64
		err := tx.QueryRow(ctx, `SELECT generation FROM draft_refresh_queue WHERE period_id=$1`, periodID).Scan(&generation)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if wantedGeneration != 0 && generation != wantedGeneration {
			return nil
		}
		clear := func() error {
			if generation == 0 {
				return nil
			}
			_, err := tx.Exec(ctx, `DELETE FROM draft_refresh_queue WHERE period_id=$1 AND generation=$2`, periodID, generation)
			return err
		}
		_, err = readSettlement(ctx, tx, summary.PeriodID)
		if err == nil {
			if wantedGeneration != 0 {
				return clear()
			}
			return errSettlementClosed
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		window, rules, err := readDraftScope(ctx, tx, &summary)
		if err != nil {
			return err
		}
		result, digest, err := computeDraft(ctx, tx, summary, window, rules, s.vipDraftQuotes)
		if err != nil {
			return err
		}
		if err := lockDraftInputs(ctx, tx, periodID); err != nil {
			return err
		}
		if err := storeDraft(ctx, tx, &summary, digest, result); err != nil {
			return err
		}
		return clear()
	})
	return summary, err
}

func computeDraft(ctx context.Context, tx pgx.Tx, summary draftSummary, window periodWindow, rules []paymentRule, quotes *vipDraftQuoteCache) (paymentDraft, []byte, error) {
	if summary.AggregatePackage != nil {
		return computeCatalogAggregateDraft(ctx, tx, summary, window)
	}
	if summary.PracticeRefundMonth {
		return computePracticeRefundMonth(ctx, tx, summary.PeriodID, window)
	}
	if summary.Catalog {
		return computeVIPDraft(ctx, tx, summary.PeriodID, window, quotes, nil)
	}
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
	if err = addRefunds(ctx, tx, summary.PeriodID, window, rules, facts, &result, nil); err != nil {
		return paymentDraft{}, nil, err
	}
	if err = addRecommendationCorrections(ctx, tx, summary.PeriodID, window, &result); err != nil {
		return paymentDraft{}, nil, err
	}
	if err = attachReportOrganizations(ctx, tx, &result, facts); err != nil {
		return paymentDraft{}, nil, err
	}
	digest, err := digestDraft(summary.RuleID, result, facts)
	return result, digest, err
}

func readDraftScope(ctx context.Context, tx pgx.Tx, summary *draftSummary) (periodWindow, []paymentRule, error) {
	var window periodWindow
	var mode, sourceKey string
	var productID int64
	var scenarioRules bool
	err := tx.QueryRow(ctx, draftScopeSQL, summary.PeriodID).Scan(&productID, &mode, &window.Start, &window.Through, &sourceKey, &summary.PeriodOrdinal, &scenarioRules, &summary.Catalog, &summary.PracticeRefundMonth, &summary.AggregatePackage)
	if err != nil {
		return window, nil, err
	}
	if summary.PracticeRefundMonth {
		summary.Catalog = false
		return window, nil, nil
	}
	if summary.AggregatePackage != nil {
		summary.Catalog = true
		return window, nil, nil
	}
	if summary.Catalog {
		return window, nil, nil
	}
	if scenarioRules && mode != "class" {
		return window, nil, errProductRuleUnavailable
	}
	var classStart time.Time
	if mode == "class" {
		if sourceKey != "" {
			if err := checkPracticeRosterProduct(ctx, tx, productID, sourceKey); err != nil {
				return window, nil, err
			}
		}
		classStart, err = readPracticeHeader(ctx, tx, sourceKey, summary)
		if err != nil {
			return window, nil, err
		}
		summary.PracticeStartsAt = classStart
		if classStart.Before(window.Start) || !classStart.Before(window.Through) {
			return window, nil, errPracticeStart
		}
	}
	rows, err := tx.Query(ctx, draftRulesSQL, summary.PeriodID, window.Through, window.Start)
	if err != nil {
		return window, nil, err
	}
	rules, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (paymentRule, error) {
		var rule paymentRule
		if err := row.Scan(&rule.ID, &rule.Version, &rule.From, &rule.Through, &rule.Components); err != nil {
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
		err := row.Scan(&input.ID, &input.Fact, &input.Fact.SalesCompanyID)
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
		WHERE i.valid_from<$3 AND (i.valid_to IS NULL OR i.valid_to>$4)
		ORDER BY i.source,i.external_id,i.valid_from,i.id`, sources, ids, window.Through, window.Start)
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
		SalesCompanyID                       string `json:",omitempty"`
	}
	facts := make(map[int64]stablePayment, len(inputs))
	for _, input := range inputs {
		f := input.Fact
		facts[input.ID] = stablePayment{Source: f.Source, Order: f.OrderID, Payment: f.PaymentID, Line: f.LineID,
			Record: f.RecordKey, OccurredAt: f.OccurredAt.UTC(), SalesCompanyID: f.SalesCompanyID}
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
			Payment      stablePayment
			Line         draftLine
			Organization *reportOrganization `json:",omitempty"`
		}{fact, line, line.organization.present()}); err != nil {
			return nil, err
		}
	}
	for _, posting := range result.Performance {
		fact := facts[posting.SourceVersionID]
		original, current := facts[posting.PaymentVersionID]
		posting.SourceVersionID = 0
		if current {
			posting.PaymentVersionID = 0
		}
		if err := encode.Encode(struct {
			Fact, Original stablePayment
			Posting        draftPerformance
		}{fact, original, posting}); err != nil {
			return nil, err
		}
	}
	for _, issue := range result.Issues {
		if issue.Advisory && issue.Code != "PRACTICE_CORRECTION_LATER_TIERS" {
			continue
		}
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
		var unit *pgtype.UUID
		if correction.Unit.Valid {
			unit = &correction.Unit
		}
		if err := encode.Encode(struct {
			correctionLine
			Organization    *reportOrganization `json:",omitempty"`
			BeneficiaryUnit *pgtype.UUID        `json:",omitempty"`
		}{correction, correction.organization.present(), unit}); err != nil {
			return nil, err
		}
	}
	for _, change := range result.practiceChanges {
		if err := encode.Encode(struct {
			Class, Payment int64
			Key            string
			Identities     map[string][]identityAssignment
		}{change.ClassID, change.OriginalID, change.Key, change.Identities}); err != nil {
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
	if result.practiceReplay != nil {
		if err := encode.Encode(result.practiceReplay); err != nil {
			return nil, err
		}
	}
	for _, group := range result.aggregateGroups {
		if err := encode.Encode(group); err != nil {
			return nil, err
		}
	}
	if len(result.aggregateEvents) != 0 {
		if err := encode.Encode(result.aggregateEvents); err != nil {
			return nil, err
		}
	}
	if result.aggregatePredecessor != 0 {
		if err := encode.Encode(result.aggregatePredecessor); err != nil {
			return nil, err
		}
	}
	return hash.Sum(nil), nil
}

func storeDraft(ctx context.Context, tx pgx.Tx, summary *draftSummary, digest []byte, result paymentDraft) error {
	rosterID := summary.PracticeRosterID
	lineCount := 0
	for _, line := range result.Lines {
		if !line.PracticeTierReference {
			lineCount++
		}
	}
	err := tx.QueryRow(ctx, `INSERT INTO calculation_drafts(period_id,rule_id,version,input_digest,total_minor,line_count,issue_count,calculated_at,refund_minor,refund_count,correction_minor,correction_count,practice_roster_id,performance_ready,catalog_refunds_ready,practice_replay_inputs,aggregate_predecessor_id)
	  VALUES($1,nullif($2::bigint,0),1,$3,$4,$5,$6,now(),$7,$8,$9,$10,$11,true,$12,$13,$14)
	  ON CONFLICT(period_id) DO UPDATE SET rule_id=excluded.rule_id,version=calculation_drafts.version+1,
	  input_digest=excluded.input_digest,total_minor=excluded.total_minor,line_count=excluded.line_count,issue_count=excluded.issue_count,
	  performance_ready=true,calculated_at=excluded.calculated_at,refund_minor=excluded.refund_minor,refund_count=excluded.refund_count,
	  correction_minor=excluded.correction_minor,correction_count=excluded.correction_count,practice_roster_id=excluded.practice_roster_id,
	  catalog_refunds_ready=excluded.catalog_refunds_ready,practice_replay_inputs=excluded.practice_replay_inputs,
	  aggregate_predecessor_id=excluded.aggregate_predecessor_id
	  WHERE NOT calculation_drafts.performance_ready OR calculation_drafts.input_digest<>excluded.input_digest
	   OR calculation_drafts.catalog_refunds_ready<>excluded.catalog_refunds_ready
	  RETURNING version,total_minor,line_count,issue_count,calculated_at,refund_minor,refund_count,correction_minor,correction_count,practice_roster_id`,
		summary.PeriodID, summary.RuleID, digest, result.Total, lineCount, result.blockingIssues(), result.RefundTotal, len(result.Refunds), result.CorrectionTotal, len(result.Corrections),
		summary.PracticeRosterID, summary.Catalog, result.practiceReplay, optionalDraftID(result.aggregatePredecessor)).
		Scan(&summary.Version, &summary.Accrued, &summary.LineCount, &summary.IssueCount, &summary.CalculatedAt, &summary.RefundTotal, &summary.RefundCount, &summary.CorrectionTotal, &summary.CorrectionCount, &summary.PracticeRosterID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Keep monetary lines and allocations stable when a newer roster
		// has the same result, or a refund is routed to a separate month.
		if rosterID != nil {
			if err := syncPracticeDraftMetadata(ctx, tx, summary.PeriodID, *rosterID, result.Issues); err != nil {
				return err
			}
		}
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
	if err = storeDraftPerformance(ctx, tx, summary.PeriodID, result.Performance); err != nil {
		return err
	}
	if summary.AggregatePackage != nil {
		if err := storeCatalogAggregateDraft(ctx, tx, summary.PeriodID, result.aggregateGroups, result.aggregateEvents); err != nil {
			return err
		}
	}
	if summary.PracticeRefundMonth {
		if err := storePracticeCorrectionRevisions(ctx, tx, summary.PeriodID, result.practiceChanges); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM calculation_draft_corrections WHERE period_id=$1", summary.PeriodID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM calculation_draft_refunds WHERE period_id=$1", summary.PeriodID); err != nil {
		return err
	}
	if err = storeDraftLines(ctx, tx, summary, result.Lines); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM calculation_draft_issues WHERE period_id=$1", summary.PeriodID); err != nil {
		return err
	}
	if len(result.Issues) != 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"calculation_draft_issues"},
			[]string{"period_id", "source_version_id", "component_type", "code", "practice_enrollment_id", "advisory",
				"correction_line_id", "recovery_person_id", "recovery_minor"},
			pgx.CopyFromSlice(len(result.Issues), func(i int) ([]any, error) {
				issue := result.Issues[i]
				var recovery pendingCorrectionRecovery
				if issue.Recovery != nil {
					recovery = *issue.Recovery
				}
				return []any{summary.PeriodID, optionalDraftID(issue.FactID), issue.Component, issue.Code, optionalDraftID(issue.EnrollmentID), issue.Advisory,
					optionalDraftID(issue.CorrectionLineID), optionalDraftID(recovery.PersonID), optionalDraftID(recovery.Amount)}, nil
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
			[]string{"period_id", "original_line_id", "source_version_id", "person_id", "amount_minor", "company_id", "department_id", "beneficiary_unit_id", "group_id"},
			pgx.CopyFromSlice(len(result.Corrections), func(i int) ([]any, error) {
				c := result.Corrections[i]
				return []any{summary.PeriodID, c.OriginalLineID, c.SourceVersionID, optionalDraftID(c.PersonID),
					c.Amount, c.organization.Company, c.organization.Department, c.Unit, c.organization.groupID()}, nil
			}))
	}
	return err
}

func optionalDraftID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
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
		rows, err := tx.Query(ctx, `SELECT id,source_version_id,component_type FROM calculation_draft_lines
		 WHERE period_id=$1 AND aggregate_group_key IS NULL`, periodID)
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
		[]string{"period_id", "original_line_id", "refund_period_id", "refund_source_version_id", "amount_minor", "tier_credit"},
		pgx.CopyFromSlice(len(refunds), func(i int) ([]any, error) {
			refund := refunds[i]
			lineID := refund.OriginalLineID
			if refund.CurrentPaymentVersion != 0 {
				lineID = current[lineKey{refund.CurrentPaymentVersion, refund.ComponentType}]
			}
			if lineID == 0 {
				return nil, errRefundAmount
			}
			return []any{periodID, lineID, refund.RefundPeriodID, refund.RefundSourceVersionID, refund.Amount, refund.TierCredit}, nil
		}))
	return err
}

var draftLinePageScope = `l.period_id=d.period_id AND l.id>$2 AND NOT l.practice_tier_reference
 AND ({{unit}} IS NULL OR EXISTS(SELECT 1 FROM organization_scope org WHERE ` + organizationPostingInScope("l.") + `))`

var draftDetailTemplateSQL = `WITH RECURSIVE ` + organizationScopeCTE + `
 SELECT jsonb_build_object(
 'periodId',d.period_id,'ruleId',d.rule_id,'ruleVersion',r.version,'version',d.version,'calculatedAt',d.calculated_at,
 'catalog',d.rule_id IS NULL AND NOT (` + practiceRefundMonthScope + `),
 'aggregatePackageId',period.aggregate_package_id,
 'aggregatePredecessorId',d.aggregate_predecessor_id,
 'aggregateGroups',CASE WHEN period.aggregate_package_id IS NOT NULL AND {{unit}} IS NULL THEN (
  SELECT coalesce(jsonb_agg(jsonb_build_object('key',g.group_key,'ruleVersion',v.version_no,'receiptsMinor',g.receipts_minor,
   'refundsMinor',g.refunds_minor,'carryInMinor',g.carry_in_minor,'carryOutMinor',g.carry_out_minor,'fixedOnly',g.fixed_only)
  ORDER BY v.version_no,g.group_key),'[]')
  FROM catalog_aggregate_draft_groups g JOIN product_rule_versions v ON v.source_id=g.rule_id WHERE g.period_id=d.period_id
 ) END,
 'practiceRefundMonth',(` + practiceRefundMonthScope + `),
 'refundsReady',d.rule_id IS NOT NULL OR d.catalog_refunds_ready OR (` + practiceRefundMonthScope + `),
 'settlementReviewAvailable',d.rule_id IS NULL AND d.catalog_refunds_ready OR d.practice_roster_id IS NOT NULL OR (` + practiceRefundMonthScope + `),
 'poolAllocationAvailable',(d.rule_id IS NULL AND d.catalog_refunds_ready OR (d.practice_roster_id IS NOT NULL OR period.aggregate_package_id IS NOT NULL OR (` + practiceRefundMonthScope + `)) AND (EXISTS(
  SELECT 1 FROM pool_account_activity movement JOIN pool_period_order origin ON origin.id=movement.period_id
  JOIN pool_period_order target ON target.id=d.period_id
  WHERE origin.product_id=target.product_id AND origin.aggregate_package_id IS NOT DISTINCT FROM target.aggregate_package_id AND (origin.id=target.id OR
   (origin.book_date,origin.book_stage,origin.ordinal)<(target.book_date,target.book_stage,target.ordinal)
   AND EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=origin.id AND (st.id IS NULL OR s.id<st.id))))
  OR EXISTS(SELECT 1 FROM monthly_pool_allocations a JOIN monthly_pool_allocation_shares sh ON sh.allocation_id=a.id
   WHERE a.period_id=d.period_id))),
 'totalMinor',d.total_minor+d.refund_minor+d.correction_minor,'accruedMinor',d.total_minor,
 'refundMinor',d.refund_minor,'refundCount',d.refund_count,'correctionMinor',d.correction_minor,'correctionCount',d.correction_count,
 'scopeLineCount',CASE WHEN {{unit}} IS NOT NULL THEN (SELECT count(*) FROM calculation_draft_lines l
  WHERE l.period_id=d.period_id AND NOT l.practice_tier_reference AND EXISTS(SELECT 1 FROM organization_scope org WHERE ` + organizationPostingInScope("l.") + `)) END,
 'scopeRefundCount',CASE WHEN {{unit}} IS NOT NULL THEN (SELECT count(*) FROM calculation_draft_refunds a
  JOIN calculation_draft_lines l ON l.id=a.original_line_id WHERE a.period_id=d.period_id
   AND EXISTS(SELECT 1 FROM organization_scope org WHERE ` + organizationPostingInScope("l.") + `)) END,
 'scopeCorrectionCount',CASE WHEN {{unit}} IS NOT NULL THEN (SELECT count(*) FROM calculation_draft_corrections c
  WHERE c.period_id=d.period_id AND EXISTS(SELECT 1 FROM organization_scope org WHERE ` + organizationPostingInScope("c.") + `)) END,
 'lineCount',d.line_count,'issueCount',d.issue_count,'practiceRosterVersion',roster.source_version,
 'advisoryCount',(SELECT count(*) FROM calculation_draft_issues i WHERE i.period_id=d.period_id AND i.advisory),
 'attendanceCount',roster.attendance_count,
 'settlement',CASE WHEN st.id IS NOT NULL THEN jsonb_build_object('id',st.id,'confirmedAt',st.confirmed_at,'draftVersion',st.draft_version) END,
 'lines',coalesce((SELECT jsonb_agg(to_jsonb(l) ORDER BY l.id) FROM (
  SELECT l.id,coalesce(p.name,practice_pool.name,action.definition->>'pool_name',beneficiary.name) AS "personName",
   CASE WHEN l.allocation_owner_id IS NOT NULL THEN 'pool' WHEN l.beneficiary_unit_id IS NOT NULL THEN 'company' ELSE 'person' END AS "beneficiaryKind",
   l.component_type AS "component", beneficiary.kind AS "beneficiaryUnitKind",
   CASE WHEN rv.id IS NOT NULL AND starts_with(l.component_type,'period:')
     THEN CASE split_part(l.component_type,':',2)
       WHEN 'company_sales' THEN rv.components->(split_part(l.component_type,':',3)::int)->'companySales'->>'label'
       WHEN 'retrain_service' THEN (
        SELECT split->>'serviceLabel'
        FROM jsonb_array_elements(rv.components->(split_part(l.component_type,':',3)::int)->'retrainSplits') split
        WHERE split->>'companyId'=company.company_id)
       ELSE rv.components->(split_part(l.component_type,':',3)::int)->'periodBenefit'->>'label' END
     WHEN e.id IS NOT NULL AND rv.id IS NOT NULL AND l.component_type IN ('sales','referral') THEN (
       SELECT coalesce(part->>'label',part->>'salesLabel') FROM jsonb_array_elements(rv.components) part
       WHERE part->>'type'=l.component_type AND (
         coalesce(part->>'classMember','')='' OR part->>'classMember'=CASE e.membership_type WHEN 3 THEN 'new' WHEN 2 THEN 'retrain' END
         OR (e.membership_type=2 AND part#>>'{tiers,includeRetrain}'='true')) LIMIT 1)
     ELSE action.definition->>'label' END AS "componentLabel",nullif(split_part(l.component_type,':',2),'') AS "componentRole",
   l.base_minor AS "baseMinor",l.amount_minor AS "amountMinor",l.aggregate_group_key AS "aggregateGroupKey",o.source,o.order_id AS "orderId",
   o.fact->>'orderNo' AS "orderNo",o.version AS "sourceVersion",o.id AS "sourceVersionId",coalesce(rv.version,cv.version_no) AS "ruleVersion",
   completed.occurred_at AS "completedAt",
   (completed.occurred_at AT TIME ZONE 'Asia/Shanghai')::date<period.starts_on AS "completionCarried",
   coalesce(l.catalog_rule_id::text,l.rule_id::text) AS "ruleId",coalesce(package.name,rv.label) AS "ruleLabel",
   e.source_enrollment_id AS "enrollmentId",e.membership_type AS "membershipType",coalesce(company_label.label,company.company_id) AS "salesCompany",
   CASE WHEN e.id IS NOT NULL THEN (o.fact->>'amountMinor')::bigint-l.base_minor END AS "classRefundMinor"
  FROM {{lineSource}} l LEFT JOIN source_order_versions o ON o.id=l.source_version_id
  LEFT JOIN calculation_draft_performance recognition ON recognition.period_id=l.period_id AND recognition.source_version_id=l.source_version_id
   AND recognition.completion_version_id IS NOT NULL
  LEFT JOIN source_order_versions completed ON completed.id=recognition.completion_version_id
  LEFT JOIN people p ON p.id=l.person_id LEFT JOIN organization_units beneficiary ON beneficiary.id=l.beneficiary_unit_id
  LEFT JOIN practice_pools practice_pool ON practice_pool.id=l.practice_pool_id
  LEFT JOIN rule_versions rv ON rv.id=l.rule_id LEFT JOIN product_rule_versions cv ON cv.source_id=l.catalog_rule_id
  LEFT JOIN product_rule_packages package ON package.source_id=cv.package_id
  LEFT JOIN LATERAL (
   SELECT b AS definition FROM jsonb_array_elements(cv.definition->'benefit_actions') b
   WHERE b->>'item_key'=l.component_type OR
    (b->>'item_key'=cv.definition->'internal_sales_direct_split'->>'sales_item_key' AND starts_with(l.component_type,(b->>'item_key')||':'))
   ORDER BY (b->>'item_key'=l.component_type) DESC LIMIT 1
  ) action ON true
  LEFT JOIN practice_enrollments e ON e.id=l.practice_enrollment_id
  LEFT JOIN source_payment_companies company ON company.source_version_id=l.source_version_id AND e.id IS NOT NULL
  LEFT JOIN configuration_options company_label ON company_label.group_key='practiceSalesCompany' AND company_label.option_key=company.company_id
  {{lineSearch}} ORDER BY l.id LIMIT {{detailLimit}}
 ) l),'[]'),
 'refunds',coalesce((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM (
  SELECT a.id,a.amount_minor AS "amountMinor",a.tier_credit AS "tierCredit",rv.fact->>'refundId' AS "refundId",rv.version AS "refundVersion",
   rv.id AS "refundSourceVersionId",
   coalesce(p.name,practice_pool.name,pool.name,beneficiary.name) AS "personName",
   CASE WHEN l.allocation_owner_id IS NOT NULL THEN 'pool' WHEN l.beneficiary_unit_id IS NOT NULL THEN 'company' ELSE 'person' END AS "beneficiaryKind",
   o.order_id AS "originalOrderId",o.fact->>'orderNo' AS "originalOrderNo",o.id AS "sourceVersionId",coalesce(r.version,cv.version_no) AS "ruleVersion",
   CASE WHEN l.practice_enrollment_id IS NOT NULL AND rv.order_id<>o.order_id THEN rv.order_id END AS "replayTriggerOrderId",
   coalesce(package.name,r.label) AS "ruleLabel",
   original_period.name AS "originalPeriod",refund_period.name AS "refundPeriod",posting_period.name AS "postingPeriod"
  FROM calculation_draft_refunds a
  JOIN source_order_versions rv ON rv.id=a.refund_source_version_id
  JOIN calculation_draft_lines l ON l.id=a.original_line_id
  LEFT JOIN catalog_aggregate_draft_events aggregate_refund ON l.aggregate_group_key IS NOT NULL
   AND aggregate_refund.period_id=a.period_id AND aggregate_refund.source_version_id=a.refund_source_version_id
  JOIN source_order_versions o ON o.id=coalesce(l.source_version_id,aggregate_refund.payment_version_id)
  LEFT JOIN people p ON p.id=l.person_id LEFT JOIN organization_units beneficiary ON beneficiary.id=l.beneficiary_unit_id
  LEFT JOIN practice_pools practice_pool ON practice_pool.id=l.practice_pool_id
  LEFT JOIN rule_versions r ON r.id=l.rule_id LEFT JOIN product_rule_versions cv ON cv.source_id=l.catalog_rule_id
  LEFT JOIN product_rule_packages package ON package.source_id=cv.package_id
  LEFT JOIN LATERAL (
   SELECT b->>'pool_name' AS name FROM jsonb_array_elements(cv.definition->'benefit_actions') b
   WHERE l.allocation_owner_id IS NOT NULL AND b->>'item_key'=l.component_type LIMIT 1
  ) pool ON true
  JOIN periods original_period ON original_period.id=l.period_id JOIN periods refund_period ON refund_period.id=a.refund_period_id
  JOIN periods posting_period ON posting_period.id=a.period_id
  WHERE a.period_id=d.period_id AND a.id>$5
  AND ({{unit}} IS NULL OR EXISTS(SELECT 1 FROM organization_scope org WHERE ` + organizationPostingInScope("l.") + `))
  {{refundSearch}} ORDER BY a.id LIMIT {{detailLimit}}
 ) a),'[]'),
 'corrections',coalesce((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM (
  SELECT c.id,c.amount_minor AS "amountMinor",paid.order_id AS "orderId",
   CASE WHEN paid.order_id=v.order_id AND v.kind='ORDER_PAID' THEN v.fact->>'orderNo' ELSE paid.fact->>'orderNo' END AS "orderNo",
   paid.fact->>'orderNo' AS "originalOrderNo",v.id AS "correctionSourceVersionId",v.version AS "sourceVersion",
   paid.id AS "sourceVersionId",coalesce(p.name,pool.name,recipient.name) AS "personName",
   coalesce(old_person.name,pool.name,old_recipient.name) AS "originalPersonName",
   CASE WHEN l.allocation_owner_id IS NOT NULL THEN 'pool' WHEN c.beneficiary_unit_id IS NOT NULL THEN 'company' ELSE 'person' END AS "beneficiaryKind",
   CASE WHEN paid.order_id<>v.order_id THEN v.order_id END AS "triggerOrderId",v.fact->>'refundId' AS "triggerRefundId",
   original_period.name AS "originalPeriod",coalesce(r.version,cv.version_no) AS "ruleVersion",
   coalesce(package.name,r.label) AS "ruleLabel"
  FROM calculation_draft_corrections c JOIN source_order_versions v ON v.id=c.source_version_id
  JOIN calculation_draft_lines l ON l.id=c.original_line_id
  LEFT JOIN people p ON p.id=c.person_id LEFT JOIN organization_units recipient ON recipient.id=c.beneficiary_unit_id
  LEFT JOIN practice_pools pool ON pool.id=l.practice_pool_id
  JOIN source_order_versions paid ON paid.id=l.source_version_id
  LEFT JOIN people old_person ON old_person.id=l.person_id LEFT JOIN organization_units old_recipient ON old_recipient.id=l.beneficiary_unit_id
  JOIN periods original_period ON original_period.id=l.period_id
  LEFT JOIN rule_versions r ON r.id=l.rule_id LEFT JOIN product_rule_versions cv ON cv.source_id=l.catalog_rule_id
  LEFT JOIN product_rule_packages package ON package.source_id=cv.package_id
  WHERE c.period_id=d.period_id AND c.id>$6
  AND ({{unit}} IS NULL OR EXISTS(SELECT 1 FROM organization_scope org WHERE ` + organizationPostingInScope("c.") + `))
  {{correctionSearch}} ORDER BY c.id LIMIT {{detailLimit}}
 ) c),'[]'),
 'issues',coalesce((SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM (
  SELECT i.id,i.component_type AS "component",i.code,i.advisory,o.source,o.order_id AS "orderId",o.fact->>'orderNo' AS "orderNo",
   o.id AS "sourceVersionId",o.version AS "sourceVersion",e.source_enrollment_id AS "enrollmentId",
   CASE WHEN o.id IS NULL THEN e.paid_order_ids END AS "enrollmentOrderIds",
   CASE WHEN r.id IS NOT NULL AND starts_with(i.component_type,'period:')
     THEN r.components->(split_part(i.component_type,':',3)::int)->'periodBenefit'->>'label' END AS "componentLabel",
   CASE WHEN starts_with(i.component_type,'period:') THEN r.version END AS "ruleVersion",
   CASE WHEN i.recovery_person_id IS NOT NULL THEN jsonb_build_object(
     'personName',recipient.name,'personId',recipient.id,'amountMinor',i.recovery_minor,'originalPeriod',original_period.name,
     'ruleVersion',coalesce(original_rule.version,original_catalog_rule.version_no))
   END AS recovery
  FROM calculation_draft_issues i LEFT JOIN source_order_versions o ON o.id=i.source_version_id LEFT JOIN practice_enrollments e ON e.id=i.practice_enrollment_id
  LEFT JOIN people recipient ON recipient.id=i.recovery_person_id
  LEFT JOIN calculation_draft_lines original ON original.id=i.correction_line_id
  LEFT JOIN periods original_period ON original_period.id=original.period_id
  LEFT JOIN rule_versions original_rule ON original_rule.id=original.rule_id
  LEFT JOIN product_rule_versions original_catalog_rule ON original_catalog_rule.source_id=original.catalog_rule_id
  WHERE i.period_id=d.period_id AND i.id>$3 ORDER BY i.id LIMIT 50
 ) i),'[]')
) FROM calculation_drafts d JOIN periods period ON period.id=d.period_id JOIN products product ON product.id=period.product_id
 LEFT JOIN rule_versions r ON r.id=d.rule_id LEFT JOIN settlements st ON st.period_id=d.period_id
 LEFT JOIN practice_rosters roster ON roster.id=d.practice_roster_id WHERE d.period_id=$1 AND ($4::bigint=0 OR d.version=$4)`

const draftDetailPageSize = 50

// A normal page has no joined-field filters. Bound its rows before loading
// rule definitions and other display fields. Searches still filter before
// limiting because their matches can depend on those fields.
var standardDraftDetailsSQL = strings.NewReplacer(
	"{{lineSource}}", "(SELECT l.* FROM calculation_draft_lines l WHERE "+draftLinePageScope+
		" ORDER BY l.id LIMIT "+strconv.Itoa(draftDetailPageSize)+")",
	"{{lineSearch}}", "", "{{refundSearch}}", "",
	"{{correctionSearch}}", "", "{{detailLimit}}", strconv.Itoa(draftDetailPageSize)).Replace(draftDetailTemplateSQL)
var draftDetailSQL = organizationScopeQuery(standardDraftDetailsSQL, "NULL")
var scopedDraftDetailSQL = organizationScopeQuery(standardDraftDetailsSQL, "$7")

const draftAggregateOrderMatch = `(l.aggregate_group_key IS NOT NULL AND EXISTS(
 SELECT 1 FROM catalog_aggregate_event_groups membership JOIN catalog_aggregate_draft_events e USING(period_id,source_version_id)
 JOIN source_order_versions paid ON paid.id=e.payment_version_id
 WHERE membership.period_id=l.period_id AND membership.group_key=l.aggregate_group_key
  AND (strpos(lower(paid.order_id),lower($8))>0 OR strpos(lower(paid.fact->>'orderNo'),lower($8))>0)))`

var filteredDraftDetailSQL = organizationScopeQuery(strings.NewReplacer(
	"{{lineSource}}", "calculation_draft_lines",
	"{{lineSearch}}", "WHERE "+draftLinePageScope+
		draftDetailFilter(draftDetailOrderMatch("o", "o.fact->>'orderNo'")+" OR "+draftAggregateOrderMatch,
			"coalesce(p.name,practice_pool.name,action.definition->>'pool_name',beneficiary.name)"),
	"{{refundSearch}}", draftDetailFilter(draftDetailOrderMatch("o", "o.fact->>'orderNo'"),
		"coalesce(p.name,practice_pool.name,pool.name,beneficiary.name)"),
	"{{correctionSearch}}", draftDetailFilter(draftDetailOrderMatch("paid",
		"CASE WHEN paid.order_id=v.order_id AND v.kind='ORDER_PAID' THEN v.fact->>'orderNo' ELSE paid.fact->>'orderNo' END"),
		"coalesce(p.name,pool.name,recipient.name)", "coalesce(old_person.name,pool.name,old_recipient.name)"),
	"{{detailLimit}}", strconv.Itoa(draftDetailPageSize+1),
).Replace(draftDetailTemplateSQL), "$7")

func draftDetailOrderMatch(orderAlias, orderName string) string {
	return "strpos(lower(" + orderAlias + ".order_id),lower($8))>0 OR strpos(lower(" + orderName + "),lower($8))>0"
}

func draftDetailFilter(orderMatch string, beneficiaryNames ...string) string {
	beneficiaryMatches := []string{"$9::text=''"}
	for _, name := range beneficiaryNames {
		beneficiaryMatches = append(beneficiaryMatches, "strpos(lower("+name+"),lower($9))>0")
	}
	return " AND ($8::text='' OR " + orderMatch + ") AND (" + strings.Join(beneficiaryMatches, " OR ") + ")"
}

func withDraftDetailPagination(raw json.RawMessage) (json.RawMessage, error) {
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	more := make(map[string]bool, 3)
	for _, section := range []string{"lines", "refunds", "corrections"} {
		var rows []json.RawMessage
		if err := json.Unmarshal(result[section], &rows); err != nil {
			return nil, err
		}
		more[section] = len(rows) > draftDetailPageSize
		if more[section] {
			items, err := json.Marshal(rows[:draftDetailPageSize])
			if err != nil {
				return nil, err
			}
			result[section] = items
		}
	}
	next, err := json.Marshal(more)
	if err != nil {
		return nil, err
	}
	result["detailHasMore"] = next
	return json.Marshal(result)
}

const draftRuleReferenceSQL = `SELECT rule_id,catalog_rule_id::text,component_type,aggregate_group_key IS NOT NULL
 FROM calculation_draft_lines WHERE $3::text='lines' AND period_id=$1 AND id=$2
 UNION ALL
 SELECT l.rule_id,l.catalog_rule_id::text,l.component_type,l.aggregate_group_key IS NOT NULL FROM calculation_draft_refunds r
 JOIN calculation_draft_lines l ON l.id=r.original_line_id
 WHERE $3::text='refunds' AND r.period_id=$1 AND r.id=$2
 UNION ALL
 SELECT l.rule_id,l.catalog_rule_id::text,l.component_type,false FROM calculation_draft_corrections c
 JOIN calculation_draft_lines l ON l.id=c.original_line_id
 WHERE $3::text='corrections' AND c.period_id=$1 AND c.id=$2
 UNION ALL
 SELECT d.rule_id,NULL::text,i.component_type,false FROM calculation_draft_issues i
 JOIN calculation_drafts d ON d.period_id=i.period_id
 WHERE $3::text='issues' AND i.period_id=$1 AND i.id=$2 AND d.rule_id IS NOT NULL
  AND starts_with(i.component_type,'period:')`

type draftRuleDetails struct {
	Rule      json.RawMessage       `json:"rule,omitempty"`
	Catalog   *catalogRateSheet     `json:"catalog,omitempty"`
	Aggregate *catalogPeriodVersion `json:"aggregate,omitempty"`
	Directory json.RawMessage       `json:"directory,omitempty"`
	Component string                `json:"component"`
}

func (s workspace) getDraftRule(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	periodID, periodErr := strconv.ParseInt(query.Get("periodId"), 10, 64)
	rowID, rowErr := strconv.ParseInt(query.Get("rowId"), 10, 64)
	section := query.Get("section")
	if periodErr != nil || periodID < 1 || rowErr != nil || rowID < 1 ||
		section != "lines" && section != "refunds" && section != "corrections" && section != "issues" {
		respond(w, nil, invalid)
		return
	}
	ctx := r.Context()
	var result draftRuleDetails
	var ruleID *int64
	var catalogID *string
	var aggregate bool
	err := s.db.QueryRow(ctx, draftRuleReferenceSQL, periodID, rowID, section).Scan(&ruleID, &catalogID, &result.Component, &aggregate)
	if err != nil {
		respond(w, nil, err)
		return
	}
	switch {
	case catalogID != nil && aggregate:
		var sheet catalogPeriodVersion
		sheet, err = readCatalogPeriodVersion(ctx, s.db, *catalogID)
		if err == nil {
			var policy catalogPeriodDefinition
			policy, err = parseCatalogPeriodRule(sheet.raw)
			sheet.MayPublish = false
			sheet.Cycle, sheet.Items = policy.Cycle, policy.Items
			if err == nil {
				err = nameCatalogPeriodTargets(ctx, s.db, sheet.Items)
			}
			for _, code := range policy.Scenarios {
				sheet.Scenarios = append(sheet.Scenarios, catalogPeriodSKU{Code: code, Label: code, Mapped: true})
			}
			result.Aggregate = &sheet
		}
	case catalogID != nil:
		sheet, sheetErr := s.catalogRuleRateSheet(ctx, *catalogID)
		sheet.MayPublish = false
		result.Catalog, err = &sheet, sheetErr
	case ruleID != nil:
		err = s.db.QueryRow(ctx, `SELECT to_jsonb(rv_row) FROM (`+ruleVersionRowsSQL+` WHERE rv.id=$1) rv_row`, *ruleID).Scan(&result.Rule)
		if err == nil {
			result.Directory, err = s.appliedRuleRecipients(ctx, result.Rule)
		}
	default:
		err = pgx.ErrNoRows
	}
	respond(w, result, err)
}

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
	unit, err := parseOrganizationScope(query.Get("unitId"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	orderQuery := strings.TrimSpace(query.Get("orderQuery"))
	beneficiaryQuery := strings.TrimSpace(query.Get("beneficiaryQuery"))
	if orderQuery != "" && !textOK(orderQuery, 128) || beneficiaryQuery != "" && !textOK(beneficiaryQuery, 128) {
		respond(w, nil, invalid)
		return
	}
	detailSQL := scopedDraftDetailSQL
	args := []any{periodID, cursors[0], cursors[1], version, cursors[2], cursors[3], unit}
	filtered := orderQuery != "" || beneficiaryQuery != ""
	if filtered {
		detailSQL = filteredDraftDetailSQL
		args = append(args, orderQuery, beneficiaryQuery)
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), detailSQL, args...).Scan(&result)
	if version > 0 && errors.Is(err, pgx.ErrNoRows) {
		err = conflict
	}
	if err == nil && filtered {
		result, err = withDraftDetailPagination(result)
	}
	respond(w, result, err)
}
