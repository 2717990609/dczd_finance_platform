package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestRecommendationRefundUsesTheCorrectionBookedBeforeItsPeriod(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t), development: true}
	at := func(month time.Month) time.Time { return time.Date(2000, month, 5, 12, 0, 0, 0, shanghai) }
	mustID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	day := 1
	product := mustID(service.createProduct(ctx, createProductInput{Code: "REFERRAL_REFUNDS", Name: "推荐退款", PeriodMode: "calendar", CycleStartDay: &day}))
	createPeriod := func(month time.Month) int64 {
		label := at(month).Format("2006-01")
		return mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: label, SettlementMonth: label}))
	}
	june, july, august, september, october := createPeriod(time.June), createPeriod(time.July),
		createPeriod(time.August), createPeriod(time.September), createPeriod(time.October)
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "DEMO", SourceKey: "REFERRAL"}))
	people := make(map[string]int64)
	for _, name := range []string{"old", "replacement", "next"} {
		person := mustID(service.createPerson(ctx, createPersonInput{Name: name}))
		people[name] = person
		mustID(service.assignIdentity(ctx, assignIdentityInput{Source: "DCZD", ExternalID: name, PersonID: person, ValidFrom: "1999-01-01T00:00:00+08:00"}))
	}
	_, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "推荐提成", Components: []component{
		{Type: "referral", Mode: "rate", Value: 1000, SourceRole: "DIRECT_REFERRER", RefundPolicy: "proportional"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	amount := int64(1000)
	payment := sourceFact{Source: "DCZD", Domain: "DEMO", SourceKey: "REFERRAL", Kind: "ORDER_PAID",
		OrderID: "order", PaymentID: "payment", RecordKey: "orders:1", AmountMinor: &amount, OccurredAt: at(time.June)}
	save := func(fact sourceFact, version string, received time.Time) {
		t.Helper()
		fact.Version, fact.UpdatedAt = version, received
		if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			if _, err := storeSourceFacts(ctx, tx, []sourceFact{fact}); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "UPDATE source_order_versions SET received_at=$2 WHERE version=$1", version, received)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	refund := func(id string, month time.Month, value int64) {
		save(sourceFact{Source: payment.Source, Domain: payment.Domain, SourceKey: payment.SourceKey,
			Kind: "REFUND_SUCCEEDED", OrderID: payment.OrderID, RecordKey: "refunds:" + id, RefundID: id,
			AmountMinor: &value, OccurredAt: at(month), RefundsPayment: []sourcePaymentReference{{OrderID: payment.OrderID, RecordID: "1"}}}, id, at(month))
	}
	draftAt := func(period, net int64, corrections, issues int) draftSummary {
		t.Helper()
		actual, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		got := actual.(draftSummary)
		if got.Total != net || got.CorrectionCount != corrections || got.IssueCount != issues {
			t.Fatalf("period %d: %+v, want net %d, %d corrections and %d issues", period, got, net, corrections, issues)
		}
		return got
	}
	close := func(draft draftSummary) {
		t.Helper()
		if _, err := service.confirmSettlement(ctx, confirmSettlementInput{PeriodID: draft.PeriodID, ExpectedVersion: draft.Version}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	payment.ReferrerID = "old"
	save(payment, "pay", at(time.June))
	close(draftAt(june, 100, 0, 0))
	payment.ReferrerID = "replacement"
	save(payment, "first-change", at(time.August))
	refund("july-refund", time.July, 200)
	close(draftAt(july, -20, 0, 0))
	refund("september-refund", time.September, 300)
	waiting := draftAt(september, -30, 0, 1)
	if _, err := service.confirmSettlement(ctx, confirmSettlementInput{PeriodID: september, ExpectedVersion: waiting.Version}, "fixture"); !errors.Is(err, errSettlementPending) {
		t.Fatalf("a refund must wait for the earlier correction before confirmation: %v", err)
	}
	var originalLine int64
	if err := service.db.QueryRow(ctx, "SELECT id FROM calculation_draft_lines WHERE period_id=$1", june).Scan(&originalLine); err != nil {
		t.Fatal(err)
	}
	for _, person := range []string{"old", "replacement"} {
		if _, err := service.db.Exec(ctx, storeCorrectionRecoverySQL, originalLine, people[person], "recover", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	close(draftAt(august, 0, 2, 0))
	var queued bool
	if err := service.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)", september).Scan(&queued); err != nil || !queued {
		t.Fatalf("confirming the correction should queue the pending refund again: %t, error=%v", queued, err)
	}
	payment.ReferrerID = "next"
	save(payment, "next-change", at(time.October))
	refunded := draftAt(september, -30, 2, 0)
	var credited, charged int64
	if err := service.db.QueryRow(ctx, `SELECT sum(amount_minor) FILTER(WHERE person_id=$2),
	 sum(amount_minor) FILTER(WHERE person_id=$3) FROM calculation_draft_corrections WHERE period_id=$1`,
		september, people["old"], people["replacement"]).Scan(&credited, &charged); err != nil || credited != 30 || charged != -30 {
		t.Fatalf("refund after the confirmed correction: restored to old=%d, debited replacement=%d, error=%v", credited, charged, err)
	}
	close(refunded)
	draftAt(october, 0, 2, 0)
	if err := service.db.QueryRow(ctx, `SELECT amount_minor FROM calculation_draft_corrections WHERE period_id=$1 AND person_id=$2`,
		october, people["next"]).Scan(&credited); err != nil || credited != 50 {
		t.Fatalf("the new recipient must receive the remaining commission: %d, error=%v", credited, err)
	}
}

func TestRecommendationCorrectionKeepsOriginalSettlementAndPostsOnlyNewDeltas(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t), development: true}
	at := func(month time.Month, day int) time.Time { return time.Date(2000, month, day, 12, 0, 0, 0, shanghai) }
	mustID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	day := 1
	product := mustID(service.createProduct(ctx, createProductInput{Code: "REFERRAL", Name: "Referral Sample", PeriodMode: "calendar", CycleStartDay: &day}))
	createPeriod := func(month time.Month) int64 {
		name := at(month, 1).Format("2006-01")
		return mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: name, SettlementMonth: name}))
	}
	original, july, august, september := createPeriod(time.June), createPeriod(time.July), createPeriod(time.August), createPeriod(time.September)
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "VIP", SourceKey: "VIP"}))
	person := func(name string) int64 {
		id := mustID(service.createPerson(ctx, createPersonInput{Name: name}))
		mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: id, Source: "DCAI", ExternalID: name, ValidFrom: "1999-01-01T00:00:00+08:00"}))
		return id
	}
	a, b, c := person("Original recipient"), person("Replacement"), person("Second replacement")
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Original 10%",
		Components: []component{{Type: "referral", Mode: "rate", Value: 1000, SourceRole: "DIRECT_REFERRER", RefundPolicy: "proportional"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	paidAt := at(time.June, 5)
	save := func(version, recipient, display string, amount int64, observed time.Time) {
		t.Helper()
		err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCAI", Kind: "ORDER_PAID", OrderID: "original-order", OrderNo: display, PaymentID: "p1", RecordKey: "orders:1",
				Version: version, Domain: "VIP", SourceKey: "VIP", AmountMinor: &amount, OccurredAt: paidAt, UpdatedAt: observed, ReferrerID: recipient}})
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "UPDATE source_order_versions SET received_at=$2 WHERE version=$1", version, observed)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	draftAt := func(period, net int64, rows, issues int) draftSummary {
		t.Helper()
		value, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		draft := value.(draftSummary)
		if draft.Total != net || draft.CorrectionCount != rows || draft.IssueCount != issues {
			t.Fatalf("unexpected draft: %+v", draft)
		}
		return draft
	}
	confirm := func(draft draftSummary) settlement {
		t.Helper()
		confirm, err := service.confirmSettlement(ctx, confirmSettlementInput{PeriodID: draft.PeriodID, ExpectedVersion: draft.Version}, "integration")
		if err != nil {
			t.Fatal(err)
		}
		return confirm
	}
	save("paid", "Original recipient", "order", 1000, paidAt)
	originalSettlement := confirm(draftAt(original, 100, 0, 0))
	var originalLine int64
	if err := service.db.QueryRow(ctx, "SELECT id FROM calculation_draft_lines WHERE period_id=$1", original).Scan(&originalLine); err != nil {
		t.Fatal(err)
	}
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Current 50%",
		Components: []component{{Type: "referral", Mode: "rate", Value: 5000, SourceRole: "DIRECT_REFERRER", RefundPolicy: "proportional"}},
	}); err != nil {
		t.Fatal(err)
	}
	review := func(personID int64, outcome string) {
		t.Helper()
		_, err := service.db.Exec(ctx, storeCorrectionRecoverySQL, originalLine, personID, outcome, "integration")
		if err != nil {
			t.Fatal(err)
		}
	}
	save("v2", "Replacement", "order", 1200, at(time.July, 5))
	save("v2-boundary-display", "Replacement", "same correction after month boundary", 1200, at(time.August, 1))
	needsCheck := draftAt(july, 120, 1, 1)
	const dept = "10000000-0000-0000-0000-000000000001"
	if _, err := service.db.Exec(ctx, `INSERT INTO organization_units(id,kind,name,status) VALUES($1::uuid,'DEPARTMENT','接收部门','ACTIVE')`, dept); err != nil {
		t.Fatal(err)
	}
	// The July correction uses the original June payment time, even though
	// June is closed and the updated appointment does not reach July.
	endOfJune := at(time.June, 29)
	if _, err := service.addOrganizationAssignment(ctx, addAppointmentInput{PersonID: b, StartsAt: at(time.June, 1), ThroughAt: &endOfJune,
		appointmentTarget: appointmentTarget{UnitID: dept, Kind: "PRIMARY"}}); err != nil {
		t.Fatal(err)
	}
	if err := service.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var reassigned string
	if err := service.db.QueryRow(ctx, `SELECT department_id::text FROM calculation_draft_corrections
	 WHERE period_id=$1 AND person_id=$2`, july, b).Scan(&reassigned); err != nil || reassigned != dept {
		t.Fatalf("historical correction department %q, want %q: %v", reassigned, dept, err)
	}
	needsCheck = draftAt(july, 120, 1, 1)
	save("v2-review-display", "Replacement", "updated order display", 1200, at(time.July, 5).Add(time.Hour))
	if repeat := draftAt(july, 120, 1, 1); repeat.Version != needsCheck.Version {
		t.Fatalf("display-only update changed a pending draft: %d -> %d", needsCheck.Version, repeat.Version)
	}
	mux := func(server workspace) *http.ServeMux {
		api := http.NewServeMux()
		for _, route := range server.routes() {
			api.HandleFunc(route.Method+" "+route.Path, route.Handler)
		}
		return api
	}
	managed := service
	managed.development = false
	api := mux(managed)
	operator := mustID(service.createPerson(ctx, createPersonInput{Name: "Finance operator"}))
	if err := service.saveFinanceAccount(ctx, financeAccountConfig{PersonID: operator, Login: "recovery.operator", Enabled: true,
		Workspace: true, Settlements: []int64{product}}, "isolated-recovery-password"); err != nil {
		t.Fatal(err)
	}
	key, err := service.loginFinanceAccount(ctx, financeLoginInput{Login: "recovery.operator", Password: "isolated-recovery-password"})
	if err != nil {
		t.Fatal(err)
	}
	var issueID int64
	if err := service.db.QueryRow(ctx, `SELECT id FROM calculation_draft_issues WHERE period_id=$1 AND code='CORRECTION_RECOVERY_PENDING'`, july).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	callDecision := func(session string, version, issue int64, outcome string, status int) {
		t.Helper()
		body := fmt.Sprintf(`{"periodId":%d,"version":%d,"issueId":%d,"outcome":%q}`, july, version, issue, outcome)
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/correction-recovery", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1")
		if session != "" {
			r.AddCookie(&http.Cookie{Name: financeAccountCookie, Value: session})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("recovery decision HTTP %d, want %d: %s", w.Code, status, w.Body.String())
		}
	}
	callDecision("", needsCheck.Version, issueID, "recover", http.StatusUnauthorized)
	viewerPerson := mustID(service.createPerson(ctx, createPersonInput{Name: "Finance viewer"}))
	if err := service.saveFinanceAccount(ctx, financeAccountConfig{PersonID: viewerPerson, Login: "recovery.viewer", Enabled: true,
		Workspace: true}, "isolated-viewer-password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := service.loginFinanceAccount(ctx, financeLoginInput{Login: "recovery.viewer", Password: "isolated-viewer-password"})
	if err != nil {
		t.Fatal(err)
	}
	callDecision(viewer, needsCheck.Version, issueID, "recover", http.StatusForbidden)
	callDecision(key, needsCheck.Version+1, issueID, "recover", http.StatusConflict)
	callDecision(key, needsCheck.Version, issueID+1, "recover", http.StatusConflict)
	if _, err := service.db.Exec(ctx, `UPDATE configuration_options SET enabled=false WHERE group_key='correctionRecovery' AND option_key='recover'`); err != nil {
		t.Fatal(err)
	}
	callDecision(key, needsCheck.Version, issueID, "recover", http.StatusBadRequest)
	if _, err := service.db.Exec(ctx, `UPDATE configuration_options SET enabled=true WHERE group_key='correctionRecovery' AND option_key='recover'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(ctx, `WITH former AS (INSERT INTO organization_units(id,kind,name,status)
	 VALUES('10000000-0000-0000-0000-000000000002','DEPARTMENT','Former department','ACTIVE') RETURNING id)
	 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,valid_to)
	 SELECT gen_random_uuid(),$1::bigint,id,'PRIMARY',now()-interval '2 months',now()-interval '1 month' FROM former
	 UNION ALL SELECT gen_random_uuid(),$1::bigint,$2::uuid,'PRIMARY',now()-interval '1 month',NULL`, a, dept); err != nil {
		t.Fatal(err)
	}
	waiverAvailable := func(want bool) {
		t.Helper()
		w := httptest.NewRecorder()
		service.getDraft(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d", july), nil))
		var body struct {
			Issues []struct {
				ID       int64
				Recovery *struct{ MayWaive *bool }
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || len(body.Issues) != 1 ||
			body.Issues[0].ID != issueID || body.Issues[0].Recovery == nil ||
			body.Issues[0].Recovery.MayWaive == nil || *body.Issues[0].Recovery.MayWaive != want {
			t.Fatalf("draft waiver availability must be %v: %d %s %v", want, w.Code, w.Body, err)
		}
	}
	callDecision(key, needsCheck.Version, issueID, "departed_unreachable", http.StatusConflict)
	waiverAvailable(false)
	if _, err := service.db.Exec(ctx, `UPDATE organization_assignments SET valid_to=now()
	 WHERE person_id=$1 AND valid_to IS NULL`, a); err != nil {
		t.Fatal(err)
	}
	waiverAvailable(true)
	// A salesperson may still have a product or project appointment while
	// switching departments. Only the current appointment version is relevant.
	if _, err := service.db.Exec(ctx, `INSERT INTO catalog_sales_context(source,captured_at,appointments_at,snapshot_digest)
	 VALUES('DCAI',now()-interval '3 days',now()-interval '2 days',decode('ff','hex'));`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(ctx, `INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
	 SELECT gen_random_uuid(),source,$1,'SALES','PROJECT','AI',now()-interval '1 month',snapshot.at
	 FROM catalog_sales_context CROSS JOIN LATERAL unnest(ARRAY[captured_at,appointments_at]) AS snapshot(at)
	 WHERE source='DCAI'`, a); err != nil {
		t.Fatal(err)
	}
	waiverAvailable(false)
	callDecision(key, needsCheck.Version, issueID, "departed_unreachable", http.StatusConflict)
	if _, err := service.db.Exec(ctx, `UPDATE catalog_sales_appointments a SET valid_to=now()
	 FROM catalog_sales_context c WHERE a.source=c.source AND a.person_id=$1 AND a.observed_at=c.appointments_at`, a); err != nil {
		t.Fatal(err)
	}
	waiverAvailable(true)
	// The stored issue must not authorize a write when its deduction no longer exists.
	review(a, "departed_unreachable")
	callDecision(key, needsCheck.Version, issueID, "recover", http.StatusConflict)
	if _, err := service.db.Exec(ctx, `DELETE FROM correction_recovery_decisions WHERE original_line_id=$1 AND person_id=$2`, originalLine, a); err != nil {
		t.Fatal(err)
	}
	callDecision(key, needsCheck.Version, issueID, "departed_unreachable", http.StatusOK)
	draftAt(july, 120, 1, 0)
	if _, err := service.db.Exec(ctx, `DELETE FROM correction_recovery_decisions WHERE original_line_id=$1 AND person_id=$2`, originalLine, a); err != nil {
		t.Fatal(err)
	}
	needsCheck = draftAt(july, 120, 1, 1)
	if err := service.db.QueryRow(ctx, `SELECT id FROM calculation_draft_issues WHERE period_id=$1 AND code='CORRECTION_RECOVERY_PENDING'`, july).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	local := service
	local.development = true
	api = mux(local)
	callDecision("", needsCheck.Version, issueID, "recover", http.StatusOK)
	api = mux(managed)
	var reviewer string
	if err := service.db.QueryRow(ctx, `SELECT reviewed_by FROM correction_recovery_decisions
	 WHERE original_line_id=$1 AND person_id=$2`, originalLine, a).Scan(&reviewer); err != nil || reviewer != "本地财务" {
		t.Fatalf("local recovery should record its origin: %q %v", reviewer, err)
	}
	var queued bool
	if err := service.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, july).Scan(&queued); err != nil || !queued {
		t.Fatalf("saving a decision must queue the open draft: %v %v", queued, err)
	}
	draftAt(july, 20, 2, 0)
	review(a, "departed_unreachable")
	pending := draftAt(july, 120, 1, 0)
	save("v2-display", "Replacement", "changed display only", 1200, at(time.July, 6))
	if repeat := draftAt(july, 120, 1, 0); repeat.Version != pending.Version {
		t.Fatalf("display-only change produced a new draft version: %d -> %d", pending.Version, repeat.Version)
	}
	confirm(pending)
	callDecision(key, pending.Version, issueID, "recover", http.StatusConflict)
	save("v3", "Second replacement", "changed display only", 900, at(time.August, 5))
	review(b, "recover")
	newDraft := draftAt(august, -30, 2, 0)
	confirmed := confirm(newDraft)
	if retried := confirm(newDraft); retried.ID != confirmed.ID {
		t.Fatal("repeated confirmation was not idempotent")
	}
	response := httptest.NewRecorder()
	service.getDraft(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d", august), nil))
	var view struct {
		Corrections []struct {
			PersonName, OriginalPersonName, SourceVersion string
			RuleVersion                                   int
			AmountMinor                                   int64
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil || response.Code != 200 || len(view.Corrections) != 2 {
		t.Fatalf("correction detail HTTP %d, error %v", response.Code, err)
	}
	if view.Corrections[0].PersonName != "Replacement" || view.Corrections[0].AmountMinor != -120 ||
		view.Corrections[1].PersonName != "Second replacement" || view.Corrections[1].AmountMinor != 90 ||
		view.Corrections[0].OriginalPersonName != "Original recipient" || view.Corrections[0].RuleVersion != 1 || view.Corrections[0].SourceVersion != "v3" {
		t.Fatalf("correction detail invariant failed: %+v", view.Corrections)
	}
	type personTotals struct {
		PersonID, AccruedMinor, RefundMinor, CorrectionMinor, TotalMinor int64
	}
	readPersonnel := func(period, version int64, status int) []personTotals {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/draft-personnel?periodId=%d&version=%d", period, version), nil)
		for _, route := range service.routes() {
			if route.Path != req.URL.Path || route.Method != req.Method {
				continue
			}
			route.Handler(w, req)
			if w.Code != status {
				t.Fatalf("personnel HTTP %d expected %d", w.Code, status)
			}
			if status != http.StatusOK {
				return nil
			}
			var result struct {
				Items []personTotals
				Total int
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Total != len(result.Items) {
				t.Fatalf("personnel result was invalid: %v", err)
			}
			return result.Items
		}
		t.Fatal("missing personnel endpoint")
		return nil
	}
	if rows := readPersonnel(original, originalSettlement.DraftVersion, http.StatusOK); len(rows) != 1 ||
		rows[0] != (personTotals{PersonID: a, AccruedMinor: 100, TotalMinor: 100}) {
		t.Fatalf("original personnel amounts = %+v", rows)
	}
	if rows := readPersonnel(august, newDraft.Version, http.StatusOK); len(rows) != 2 ||
		rows[0] != (personTotals{PersonID: c, CorrectionMinor: 90, TotalMinor: 90}) ||
		rows[1] != (personTotals{PersonID: b, CorrectionMinor: -120, TotalMinor: -120}) {
		t.Fatalf("corrected personnel amounts = %+v", rows)
	}
	readPersonnel(august, newDraft.Version+1, http.StatusConflict)
	review(c, "recover")
	refund := int64(200)
	err = pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCAI", Kind: "REFUND_SUCCEEDED", OrderID: "original-order", RefundID: "refund", RecordKey: "refunds:1",
			Version: "refund-1", Domain: "VIP", SourceKey: "VIP", AmountMinor: &refund, OccurredAt: at(time.September, 5), UpdatedAt: at(time.September, 5),
			RefundsPayment: []sourcePaymentReference{{OrderID: "original-order", RecordID: "1"}}}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Refund originally references A. The correction restores the difference for A
	// and charges the part of commission still held by the corrected beneficiary.
	refunded := draftAt(september, -20, 2, 0)
	if refunded.RefundTotal != -20 || refunded.CorrectionTotal != 0 {
		t.Fatalf("refund after recommendation changes=%+v", refunded)
	}
	var sum int64
	for _, row := range readPersonnel(september, refunded.Version, http.StatusOK) {
		if row.PersonID == a && row.RefundMinor != -20 || row.PersonID != a && row.RefundMinor != 0 {
			t.Fatalf("refund allocated to the wrong person: %+v", row)
		}
		sum += row.TotalMinor
	}
	if sum != refunded.Total {
		t.Fatalf("personnel total=%d, draft total=%d", sum, refunded.Total)
	}
	if _, err = service.db.Exec(ctx, "DELETE FROM settlements WHERE id=$1", originalSettlement.ID); err == nil {
		t.Fatal("deleted the original settlement despite booked corrections")
	}
}
