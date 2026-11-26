package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeRosterPullAndSettlementUseCurrentSourceVersion(t *testing.T) {
	const contract = "dczd.finance-practice-roster-read/1.0"
	const periodKey = "PRACTICE:1:1:101"
	const serviceToken = "isolated-practice-roster-fixture"
	start := time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC)
	order := "31"
	first := practiceRosterWire{ContractVersion: "dczd.practice-roster/1.0", SourceSystem: "DCZD", PeriodKey: periodKey,
		ActivityID: 101, ActivityType: 1, StrategyID: 1, RosterVersion: 1, PeriodStartAt: start.Format(time.RFC3339Nano),
		ConfirmedAt: start.Add(time.Hour + 42*time.Nanosecond).Format(time.RFC3339Nano), ConfirmedBy: "1",
		Enrollments: []practiceEnrollmentWire{{SourceEnrollmentID: "10", SourceOrderID: &order, ParticipantID: "20",
			MembershipType: 3, CommissionEligible: true, SignedAt: start.Format(time.RFC3339Nano)}}}
	first.RosterHash = practiceRosterHash(first.Enrollments)
	latest := first
	latest.RosterVersion, latest.ConfirmedAt = 2, start.Add(2*time.Hour).Format(time.RFC3339Nano)
	var upstreamVersion atomic.Int32
	var upstreamHTTPStatus atomic.Int32
	upstreamVersion.Store(1)
	var changed, tampered, overlap atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/finance/source-orders/practice-rosters" ||
			r.URL.Query().Get("period_key") != periodKey || r.Header.Get("Authorization") != "Bearer "+serviceToken ||
			r.Header.Get("X-DCZD-Finance-Contract") != contract {
			http.Error(w, "unexpected authenticated endpoint", http.StatusBadRequest)
			return
		}
		if status := upstreamHTTPStatus.Load(); status != 0 {
			w.WriteHeader(int(status))
			return
		}
		selected := first
		if upstreamVersion.Load() == 2 {
			selected = latest
		}
		response := map[string]any{"contract_version": contract, "period_key": periodKey, "current": !changed.Load(),
			"has_payment_conflict": overlap.Load(),
			"roster_version":       selected.RosterVersion, "roster_hash": selected.RosterHash, "period_start_at": selected.PeriodStartAt,
			"confirmed_at": selected.ConfirmedAt, "first_confirmed_at": first.ConfirmedAt}
		if r.URL.Query().Get("include_versions") == "1" {
			if tampered.Load() {
				selected.Enrollments = nil
			}
			response["first_roster"], response["latest_roster"] = first, selected
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer provider.Close()
	ctx := context.Background()
	s := workspace{db: integrationDB(t), sources: map[string]sourceConfig{"DCZD": {BaseURL: provider.URL, Token: serviceToken}}}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "ROSTER_PULL", Name: "实战班", PeriodMode: "class"}))
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月实战班", Ordinal: 1,
		StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: periodKey}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "财务"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "sales", ValidFrom: "2026-07-01T00:00:00+08:00"}))
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "销售规则", Components: []component{
		{Type: "sales", Mode: "rate", Value: 1000, ClassMember: "new", SourceRole: "DIRECT_REFERRER"},
	}}); err != nil {
		t.Fatal(err)
	}
	providerKey := s.orderSourceKey("DCZD")
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		amount := int64(10000)
		_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: "practice",
			OrderID: order, PaymentID: order, RecordKey: "payment:" + order, Version: "1", ReferrerID: "sales", AmountMinor: &amount,
			OccurredAt: start.Add(-time.Hour), UpdatedAt: start.Add(-time.Hour)}}, providerKey)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,completed_through,last_success_at,next_updated_at,next_id)
	 VALUES($1,$2,'2026-07-01',now(),now(),now(),'0')`, dczdOrderStream, providerKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	if err := s.saveFinanceAccount(ctx, financeAccountConfig{PersonID: person, Login: "roster.operator", Enabled: true, Workspace: true,
		Settlements: []int64{product}}, "isolated-roster-password"); err != nil {
		t.Fatal(err)
	}
	key, err := s.loginFinanceAccount(ctx, financeLoginInput{Login: "roster.operator", Password: "isolated-roster-password"})
	if err != nil {
		t.Fatal(err)
	}
	api := http.NewServeMux()
	for _, route := range s.routes() {
		api.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	call := func(method, path string, body any, status int) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(string(raw)))
		r.Header.Set("Origin", "http://127.0.0.1")
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: financeAccountCookie, Value: key})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: want %d, got %d %s", method, path, status, w.Code, w.Body)
		}
		return w
	}
	pull := func(status int) {
		call(http.MethodPost, "/api/v1/practice-rosters/pull", map[string]int64{"periodId": period}, status)
	}
	tampered.Store(true)
	pull(http.StatusConflict)
	var rosterCount int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM practice_rosters`).Scan(&rosterCount); err != nil || rosterCount != 0 {
		t.Fatalf("inconsistent contents were written: %d %v", rosterCount, err)
	}
	tampered.Store(false)
	pull(http.StatusOK)
	calculate := func() draftSummary {
		t.Helper()
		w := call(http.MethodPost, "/api/v1/drafts", calculateDraftInput{PeriodID: period}, http.StatusOK)
		var d draftSummary
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil || d.Total != 1000 || d.IssueCount != 0 {
			t.Fatalf("unexpected trial %s: %v", w.Body, err)
		}
		return d
	}
	draft := calculate()
	view := func() settlementReview {
		t.Helper()
		w := call(http.MethodGet, fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", period, draft.Version), nil, http.StatusOK)
		var r settlementReview
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || r.PracticeRoster == nil || r.Total != 1000 {
			t.Fatalf("unexpected review %s: %v", w.Body, err)
		}
		return r
	}
	approved := view()
	if !approved.CanConfirm || approved.Token == "" || approved.PracticeRoster.Version != 1 {
		t.Fatalf("matching upstream roster should enable confirmation: %+v", approved)
	}
	post := func(token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		return call(http.MethodPost, "/api/v1/settlements", confirmSettlementInput{PeriodID: period, ExpectedVersion: draft.Version, ReviewToken: token}, status)
	}
	for _, failure := range []struct {
		status int
		state  string
	}{
		{http.StatusNotFound, "ROUTE_MISSING"}, {http.StatusMethodNotAllowed, "ROUTE_MISSING"}, {http.StatusNotImplemented, "ROUTE_MISSING"},
		{http.StatusUnauthorized, "ACCESS_DENIED"}, {http.StatusForbidden, "ACCESS_DENIED"}, {http.StatusBadGateway, "UNAVAILABLE"},
	} {
		upstreamHTTPStatus.Store(int32(failure.status))
		if r := view(); r.CanConfirm || r.Token != "" || r.PracticeRoster.SourceStatus != failure.state ||
			len(r.Sources) != 1 || r.Sources[0].Status != "READY" {
			t.Fatalf("order feed can be ready while roster HTTP %d blocks confirmation: %+v", failure.status, r)
		}
		post(approved.Token, http.StatusConflict)
		pull(http.StatusConflict)
	}
	upstreamHTTPStatus.Store(0)
	changed.Store(true)
	post(approved.Token, http.StatusConflict)
	if r := view(); r.CanConfirm || r.Token != "" {
		t.Fatalf("unconfirmed live changes must block close: %+v", r)
	}
	changed.Store(false)
	overlap.Store(true)
	post(approved.Token, http.StatusConflict)
	if r := view(); r.CanConfirm || r.Token != "" {
		t.Fatalf("a conflicting payment from another source class must block close: %+v", r)
	}
	overlap.Store(false)
	upstreamVersion.Store(2)
	post(approved.Token, http.StatusConflict)
	pull(http.StatusOK)
	two := calculate()
	if two.Version != draft.Version || two.PracticeRosterVersion != 2 {
		t.Fatalf("equivalent roster must update the reference and reuse the financial draft: %+v", two)
	}
	ready := view()
	if !ready.CanConfirm || ready.Token == approved.Token || ready.PracticeRoster.Version != 2 ||
		!ready.PracticeRoster.RefundCutoff.Equal(start.Add(time.Hour)) {
		t.Fatalf("new roster needs a fresh approval and must preserve the first cutoff: %+v", ready)
	}
	post(approved.Token, http.StatusConflict)
	confirmed := post(ready.Token, http.StatusOK)
	changed.Store(true)
	retry := post(ready.Token, http.StatusOK)
	if confirmed.Body.String() != retry.Body.String() {
		t.Fatal("repeating confirmation must return the existing final result")
	}
	pull(http.StatusConflict)
}

func TestPracticeRegistrationSyncUsesBusinessStateWithoutConfirmation(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	const key = "PRACTICE:2:4:20"
	start := time.Date(2026, 8, 8, 1, 0, 0, 0, time.UTC).Format(time.RFC3339)
	product, err := s.createProduct(ctx, createProductInput{Code: "REGISTRATION_SYNC", Name: "家庭教育", PeriodMode: "class"})
	if err != nil {
		t.Fatal(err)
	}
	productID := product.(map[string]int64)["id"]
	period, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: "第20期", Ordinal: 20,
		StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: key})
	if err != nil {
		t.Fatal(err)
	}
	periodID := period.(map[string]int64)["id"]
	if _, err = s.createMapping(ctx, createMappingInput{ProductID: productID, Source: "DCZD", Domain: "PRACTICE", SourceKey: "4"}); err != nil {
		t.Fatal(err)
	}
	registration := map[string]any{"source_enrollment_id": "19", "source_participant_id": "3", "membership_type": 1,
		"enabled": true, "state": 1, "referrer_id": "40", "referrer_company_id": "50"}
	signed := []practiceEnrollmentWire{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/finance/source-orders/practice-registrations" ||
			r.URL.Query().Get("period_key") != key || r.Header.Get("X-DCZD-Finance-Contract") != practiceRegistrationsContract {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"contract_version": practiceRegistrationsContract,
			"source_system": "DCZD", "period_key": key, "activity_type": 2, "strategy_id": 4, "activity_id": 20,
			"period_start_at": start, "enrollments": signed, "roster_hash": practiceRosterHash(signed),
			"valid_payment_links": true, "registrations": []any{registration}})
	}))
	defer provider.Close()
	client, err := newOrderFeedClient("DCZD", sourceConfig{BaseURL: provider.URL, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	check := func(version int64, attendance int, referrer string) {
		t.Helper()
		if err := syncPracticeRegistrations(ctx, conn, client); err != nil {
			t.Fatal(err)
		}
		var gotVersion int64
		var gotAttendance int
		var gotReferrer string
		err := conn.QueryRow(ctx, `SELECT roster.source_version,roster.attendance_count,s.registrations->0->>'referrer_id'
		 FROM practice_registrations s JOIN practice_rosters roster ON roster.period_key=s.period_key
		 WHERE s.period_key=$1 ORDER BY roster.source_version DESC LIMIT 1`, key).Scan(&gotVersion, &gotAttendance, &gotReferrer)
		if err != nil || gotVersion != version || gotAttendance != attendance || gotReferrer != referrer {
			t.Fatalf("version %d attendance %d referrer %s: %v", gotVersion, gotAttendance, gotReferrer, err)
		}
	}
	check(1, 0, "40")
	registration["state"], registration["signed_at"] = 2, start
	signed = []practiceEnrollmentWire{{SourceEnrollmentID: "19", ParticipantID: "3", MembershipType: 1, SignedAt: start}}
	check(2, 1, "40")
	registration["referrer_id"] = "41"
	check(3, 1, "41")
	registration["state"], registration["signed_at"], signed = 4, nil, []practiceEnrollmentWire{}
	check(4, 0, "41")
	var oldAttendance int
	if err := conn.QueryRow(ctx, "SELECT attendance_count FROM practice_rosters WHERE period_key=$1 AND source_version=2", key).Scan(&oldAttendance); err != nil || oldAttendance != 1 {
		t.Fatalf("old signed version was changed: %d %v", oldAttendance, err)
	}
	if _, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: periodID}); !errors.Is(err, errPracticeNotReady) {
		t.Fatalf("empty attendance draft: %v", err)
	}
}
