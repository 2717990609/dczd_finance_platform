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
	errDraftScope = errors.New("当前试算支持已配置规则的固定账期")
	errDraftRule  = errors.New("请先发布此范围的规则")
	errDraftLimit = errors.New("当前范围订单过多，无法完成试算")
)

type calculateDraftInput struct{ PeriodID int64 }

type draftSummary struct {
	PeriodID     int64     `json:"periodId"`
	RuleID       int64     `json:"ruleId"`
	RuleVersion  int       `json:"ruleVersion"`
	Version      int64     `json:"version"`
	Total        int64     `json:"totalMinor"`
	LineCount    int       `json:"lineCount"`
	IssueCount   int       `json:"issueCount"`
	CalculatedAt time.Time `json:"calculatedAt"`
}

const maxDraftPayments = 100000

const draftScopeSQL = `SELECT pro.period_mode, p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai',
 (p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'
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
	facts, identities, err := readDraftInputs(ctx, tx, summary.PeriodID, window, rules)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	result, err := calculatePaymentDraft(window, rules, facts, identities)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	digest, err := digestDraft(summary.RuleID, result, facts)
	return result, digest, err
}

func readDraftScope(ctx context.Context, tx pgx.Tx, summary *draftSummary) (periodWindow, []paymentRule, error) {
	var window periodWindow
	var mode string
	err := tx.QueryRow(ctx, draftScopeSQL, summary.PeriodID).Scan(&mode, &window.Start, &window.Through)
	if err != nil {
		return window, nil, err
	}
	if mode != "calendar" {
		return window, nil, errDraftScope
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
	sources := make([]string, 0, len(keys))
	ids := make([]string, 0, len(keys))
	for key := range keys {
		sources, ids = append(sources, key.Source), append(ids, key.ExternalID)
	}
	rows, err = tx.Query(ctx, `SELECT i.source,i.external_id,i.id,i.person_id,i.valid_from,i.valid_to
		FROM unnest($1::text[],$2::text[]) AS wanted(source,external_id)
		JOIN identity_mappings i USING(source,external_id)
		WHERE i.valid_from<$3 AND (i.valid_to IS NULL OR i.valid_to>$4)`, sources, ids, window.Through, window.Start)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	identities := make(map[identityKey][]identityAssignment, len(keys))
	for rows.Next() {
		var key identityKey
		var value identityAssignment
		if err = rows.Scan(&key.Source, &key.ExternalID, &value.ID, &value.PersonID, &value.ValidFrom, &value.ValidTo); err != nil {
			return nil, nil, err
		}
		identities[key] = append(identities[key], value)
	}
	return facts, identities, rows.Err()
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
		issue.FactID = 0
		if err := encode.Encode(struct {
			Payment stablePayment
			Issue   draftIssue
		}{fact, issue}); err != nil {
			return nil, err
		}
	}
	return hash.Sum(nil), nil
}

func storeDraft(ctx context.Context, tx pgx.Tx, summary *draftSummary, digest []byte, result paymentDraft) error {
	err := tx.QueryRow(ctx, `INSERT INTO calculation_drafts(period_id,rule_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	  VALUES($1,$2,1,$3,$4,$5,$6,now())
	  ON CONFLICT(period_id) DO UPDATE SET rule_id=excluded.rule_id,version=calculation_drafts.version+1,
	  input_digest=excluded.input_digest,total_minor=excluded.total_minor,line_count=excluded.line_count,issue_count=excluded.issue_count,
	  calculated_at=excluded.calculated_at WHERE calculation_drafts.input_digest<>excluded.input_digest
	  RETURNING version,total_minor,line_count,issue_count,calculated_at`, summary.PeriodID, summary.RuleID, digest,
		result.Total, len(result.Lines), len(result.Issues)).Scan(&summary.Version, &summary.Total, &summary.LineCount, &summary.IssueCount, &summary.CalculatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.QueryRow(ctx, `SELECT version,total_minor,line_count,issue_count,calculated_at FROM calculation_drafts WHERE period_id=$1`,
			summary.PeriodID).Scan(&summary.Version, &summary.Total, &summary.LineCount, &summary.IssueCount, &summary.CalculatedAt)
	}
	if err != nil {
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
			[]string{"period_id", "source_version_id", "component_type", "rule_id", "identity_id", "person_id", "base_minor", "amount_minor"},
			pgx.CopyFromSlice(len(result.Lines), func(i int) ([]any, error) {
				l := result.Lines[i]
				return []any{summary.PeriodID, l.FactID, l.Component, l.RuleID, l.IdentityID, l.PersonID, l.Base, l.Amount}, nil
			}))
		if err != nil {
			return err
		}
	}
	if len(result.Issues) != 0 {
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"calculation_draft_issues"},
			[]string{"period_id", "source_version_id", "component_type", "code"},
			pgx.CopyFromSlice(len(result.Issues), func(i int) ([]any, error) {
				issue := result.Issues[i]
				var factID *int64
				if issue.FactID != 0 {
					factID = &issue.FactID
				}
				return []any{summary.PeriodID, factID, issue.Component, issue.Code}, nil
			}))
	}
	return err
}

const draftDetailSQL = `SELECT jsonb_build_object(
 'periodId',d.period_id,'ruleId',d.rule_id,'ruleVersion',r.version,'version',d.version,'calculatedAt',d.calculated_at,
 'totalMinor',d.total_minor,'lineCount',d.line_count,'issueCount',d.issue_count,
 'settlement',CASE WHEN st.id IS NOT NULL THEN jsonb_build_object('id',st.id,'confirmedAt',st.confirmed_at,'draftVersion',st.draft_version) END,
 'lines',coalesce((SELECT jsonb_agg(to_jsonb(l) ORDER BY l.id) FROM (
  SELECT l.id,p.name AS "personName",l.component_type AS "component",l.base_minor AS "baseMinor",l.amount_minor AS "amountMinor",
   o.source,o.order_id AS "orderId",o.fact->>'orderNo' AS "orderNo",o.version AS "sourceVersion",rv.version AS "ruleVersion"
  FROM calculation_draft_lines l JOIN people p ON p.id=l.person_id JOIN source_order_versions o ON o.id=l.source_version_id
  JOIN rule_versions rv ON rv.id=l.rule_id
  WHERE l.period_id=d.period_id AND l.id>$2 ORDER BY l.id LIMIT 50
 ) l),'[]'),
 'issues',coalesce((SELECT jsonb_agg(to_jsonb(i) ORDER BY i.id) FROM (
  SELECT i.id,i.component_type AS "component",i.code,o.source,o.order_id AS "orderId"
  FROM calculation_draft_issues i LEFT JOIN source_order_versions o ON o.id=i.source_version_id
  WHERE i.period_id=d.period_id AND i.id>$3 ORDER BY i.id LIMIT 50
 ) i),'[]')
) FROM calculation_drafts d JOIN rule_versions r ON r.id=d.rule_id LEFT JOIN settlements st ON st.period_id=d.period_id WHERE d.period_id=$1 AND ($4::bigint=0 OR d.version=$4)`

func (s workspace) getDraft(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	periodID, err := strconv.ParseInt(query.Get("periodId"), 10, 64)
	if err != nil || periodID < 1 {
		respond(w, nil, invalid)
		return
	}
	cursors := [2]int64{}
	for i, name := range []string{"afterLineId", "afterIssueId"} {
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
	err = s.db.QueryRow(r.Context(), draftDetailSQL, periodID, cursors[0], cursors[1], version).Scan(&result)
	if version > 0 && errors.Is(err, pgx.ErrNoRows) {
		err = conflict
	}
	respond(w, result, err)
}
