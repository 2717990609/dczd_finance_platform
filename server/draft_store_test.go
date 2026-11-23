package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func integrationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := emptyIntegrationDB(t)
	if err := migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func emptyIntegrationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("FINANCE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("FINANCE_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasSuffix(cfg.ConnConfig.Database, "_dev") {
		t.Fatal("integration tests must point to a dedicated development database")
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schemaName := pgx.Identifier{fmt.Sprintf("finance_test_%d", time.Now().UnixNano())}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schemaName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+schemaName+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	cfg = cfg.Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schemaName
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestDraftStoreReusesUnchangedVersionAndReplacesOnlyChangedOrdinaryLines(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t)}
	mustID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	day := 25
	product := mustID(service.createProduct(ctx, createProductInput{Code: "test", Name: "Test", PeriodMode: "calendar", CycleStartDay: &day}))
	period := mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "August", SettlementMonth: "2026-08"}))
	if _, err := service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "Duplicate", SettlementMonth: "2026-08"}); !errors.Is(err, conflict) {
		t.Fatalf("duplicate calendar period: %v", err)
	}
	person := mustID(service.createPerson(ctx, createPersonInput{Name: "Alice"}))
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "VIP", SourceKey: "VIP499"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCAI", ExternalID: "test-a", ValidFrom: "2026-01-01T00:00:00+08:00"}))
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "2026", Components: []component{{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "LOCK_SALES_USER"}}}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	fact := sourceFact{Source: "DCAI", Kind: "ORDER_PAID", OrderID: "O1", PaymentID: "P1", RecordKey: "orders:1", Domain: "VIP", SourceKey: "VIP499",
		OccurredAt: at, Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "test-a"}}}
	insert := func(version string, amount int64, updated time.Time) {
		t.Helper()
		fact.Version, fact.AmountMinor, fact.UpdatedAt = version, &amount, updated
		raw, err := json.Marshal(fact)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
			VALUES($1,$2,'',$3,$4,$5,$6,$7,$8)`, fact.Source, fact.OrderID, fact.RecordKey, version, fact.Kind, updated, raw, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(version, total int64) {
		t.Helper()
		data, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		head := data.(draftSummary)
		if head.Version != version || head.Total != total || head.IssueCount != 0 || head.LineCount != 2 {
			t.Fatalf("unexpected draft: %+v", head)
		}
	}
	insert("1", 990, at)
	fact.OrderID, fact.PaymentID, fact.RecordKey = "O2", "P2", "orders:2"
	insert("1", 2000, at)
	fact.OrderID, fact.PaymentID, fact.RecordKey = "O1", "P1", "orders:1"
	idFor := func(order string) int64 {
		t.Helper()
		var id int64
		if err := service.db.QueryRow(ctx, `SELECT l.id FROM calculation_draft_lines l JOIN source_order_versions v ON v.id=l.source_version_id
		 WHERE l.period_id=$1 AND v.order_id=$2`, period, order).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	performanceRowVersion := func(order string) string {
		t.Helper()
		var version string
		if err := service.db.QueryRow(ctx, `SELECT a.xmin::text FROM calculation_draft_performance a
		 JOIN source_order_versions v ON v.id=a.source_version_id WHERE a.period_id=$1 AND v.order_id=$2`, period, order).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	check(1, 299)
	first, second := idFor("O1"), idFor("O2")
	firstPerformance, secondPerformance := performanceRowVersion("O1"), performanceRowVersion("O2")
	fact.OrderNo = "display-only change"
	insert("2", 990, at.Add(time.Minute))
	check(1, 299)
	insert("3", 105, at.Add(2*time.Minute))
	check(2, 211)
	updated := idFor("O1")
	if updated == first || idFor("O2") != second {
		t.Fatal("an order revision should replace its line and keep the unrelated line")
	}
	if performanceRowVersion("O1") == firstPerformance || performanceRowVersion("O2") != secondPerformance {
		t.Fatal("an order revision should replace its performance posting and keep the unrelated posting")
	}
	var count int
	var amount int64
	err := service.db.QueryRow(ctx, "SELECT count(*), sum(amount_minor) FROM calculation_draft_lines WHERE period_id=$1", period).Scan(&count, &amount)
	if err != nil || count != 2 || amount != 211 {
		t.Fatalf("stored lines: count=%d amount=%d error=%v", count, amount, err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d&version=1", period), nil)
	service.getDraft(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale draft pagination returned HTTP %d", response.Code)
	}
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "新版规则", Components: []component{
		{Type: "sales", Mode: "rate", Value: 1250, SourceRole: "LOCK_SALES_USER"},
	}}); err != nil {
		t.Fatal(err)
	}
	appliedRequest := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts/rule?periodId=%d&section=lines&rowId=%d", period, updated), nil)
	appliedResponse := httptest.NewRecorder()
	service.getDraftRule(appliedResponse, appliedRequest)
	var applied struct {
		Rule struct {
			Version    int         `json:"version"`
			Components []component `json:"components"`
		} `json:"rule"`
	}
	if err := json.Unmarshal(appliedResponse.Body.Bytes(), &applied); err != nil ||
		appliedResponse.Code != http.StatusOK || applied.Rule.Version != 1 ||
		len(applied.Rule.Components) != 1 || applied.Rule.Components[0].Value != 1000 {
		t.Fatalf("expected the applied version rather than the latest publication: %d %s", appliedResponse.Code, appliedResponse.Body)
	}
	check(3, 263)
	if idFor("O1") == updated || idFor("O2") == second {
		t.Fatal("both orders should use the new rule and receive new detail rows")
	}
	if performanceRowVersion("O2") != secondPerformance {
		t.Fatal("a rate-only rule change should not rewrite identical performance")
	}
	appliedResponse = httptest.NewRecorder()
	service.getDraftRule(appliedResponse, appliedRequest)
	if appliedResponse.Code != http.StatusNotFound {
		t.Fatalf("a replaced draft row must not open a different rule: %d", appliedResponse.Code)
	}
}

func TestDraftStorePreservesUnchangedPoolFundingAndRebuildsChangedOrder(t *testing.T) {
	ctx := context.Background()
	db, err := integrationDB(t).Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Release()
	const unitID = "10000000-0000-4000-8000-000000000001"
	if _, err := db.Exec(ctx, `INSERT INTO products(code,name,period_mode) VALUES('POOL_STORAGE','班次池','class');
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on) VALUES(1,'班次',1,'2026-08-01','2026-08-02');
	 INSERT INTO rule_versions(product_id,version,label,components) VALUES(1,1,'样本','[]');
	 INSERT INTO people(name) VALUES('销售及池负责人');
	 INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(1,'DCZD','seller','2026-01-01');
	 INSERT INTO organization_units(id,kind,name,status) VALUES('`+unitID+`','COMPANY','样本公司','ACTIVE');
	 INSERT INTO practice_pools(product_id,unit_id,name) VALUES(1,'`+unitID+`','样本池');
	 INSERT INTO practice_enrollments(period_key,source_enrollment_id,participant_id,membership_type,commission_eligible,signed_at,paid_order_ids,hash)
	 VALUES('PRACTICE:1:1:1','1','1',3,true,'2026-08-01',ARRAY['1','2'],decode('00','hex'));
	 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
	 SELECT 'DCZD',n::text,'','orders:'||n,'1','ORDER_PAID','2026-08-01','{}','2026-08-01' FROM generate_series(1,2) n ORDER BY n;`); err != nil {
		t.Fatal(err)
	}
	var unit pgtype.UUID
	if err := unit.Scan(unitID); err != nil {
		t.Fatal(err)
	}
	first := draftLine{FactID: 1, RuleID: 1, EnrollmentID: 1, Component: "period:pool:1", Amount: 100, Base: 1000,
		BeneficiaryUnit: &unit, PracticePool: &practicePoolTarget{PoolID: 1, OwnerID: 1}}
	second := first
	second.FactID = 2
	sales := draftLine{FactID: 1, RuleID: 1, PersonID: 1, IdentityID: 1, Component: "sales", Amount: 120, Base: 1000}
	save := func(version byte, lines []draftLine) {
		t.Helper()
		var total int64
		for _, line := range lines {
			total += line.Amount
		}
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			return storeDraft(ctx, tx, &draftSummary{PeriodID: 1, RuleID: 1}, []byte{version}, paymentDraft{Lines: lines, Total: total})
		}); err != nil {
			t.Fatal(err)
		}
	}
	poolLines := func() ([]int64, []int64) {
		t.Helper()
		var ids, sources []int64
		if err := db.QueryRow(ctx, `SELECT array_agg(id ORDER BY id),array_agg(source_version_id ORDER BY id)
		 FROM calculation_draft_lines WHERE allocation_owner_id IS NOT NULL`).Scan(&ids, &sources); err != nil {
			t.Fatal(err)
		}
		return ids, sources
	}
	save(1, []draftLine{first, second, sales})
	before, _ := poolLines()
	if _, err := db.Exec(ctx, `INSERT INTO monthly_pool_allocations(period_id,unit_id,item_key,scenario_key,draft_version,basis_token,revision)
	 VALUES(1,'`+unitID+`','pool','practice',1,'fixture',1);
	 INSERT INTO monthly_pool_allocation_shares(allocation_id,person_id,amount_minor) VALUES(1,NULL,100);
	 INSERT INTO monthly_pool_allocation_funding(share_id,source_line_id,amount_minor)
	 SELECT 1,id,100 FROM calculation_draft_lines WHERE source_version_id=1 AND allocation_owner_id IS NOT NULL;`); err != nil {
		t.Fatal(err)
	}
	sales.Amount = 125
	save(2, []draftLine{first, sales, second})
	stable, order := poolLines()
	if !slices.Equal(before, stable) || !slices.Equal(order, []int64{1, 2}) {
		t.Fatalf("an ordinary sale should keep the pool postings: %v %v", stable, order)
	}
	var funding int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM monthly_pool_allocation_funding`).Scan(&funding); err != nil || funding != 1 {
		t.Fatalf("unchanged funding was lost: %d %v", funding, err)
	}
	save(3, []draftLine{second, sales, first})
	replaced, order := poolLines()
	if !slices.Equal(order, []int64{2, 1}) || slices.Contains(before, replaced[0]) || slices.Contains(before, replaced[1]) {
		t.Fatalf("pool credits should follow the fresh calculation order: %v %v", replaced, order)
	}
}

func TestCalendarDraftDigestRemainsCompatible(t *testing.T) {
	result := paymentDraft{Lines: []draftLine{{FactID: 42, PersonID: 3, IdentityID: 4, RuleID: 17, Component: "sales", Base: 990, Amount: 99}}}
	facts := []paymentInput{{ID: 42, Fact: sourceFact{Source: "DCAI", OrderID: "O1", PaymentID: "P1", RecordKey: "orders:1",
		OccurredAt: time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)}}}
	digest, err := digestDraft(17, result, facts)
	const previous = "4d531cd1e84d9fb81bdb5789bb4d9599d98c7732f3190db8fd1322a1249162a1"
	if err != nil || fmt.Sprintf("%x", digest) != previous {
		t.Fatalf("calendar digest = %x, error = %v", digest, err)
	}
}

func TestClassDraftKeepsNewCompanyVersionEvenWhenAmountIsSame(t *testing.T) {
	fact := paymentInput{ID: 1, Fact: sourceFact{Source: "DCZD", OrderID: "1", PaymentID: "1", SalesCompanyID: "first"}}
	draft := paymentDraft{Lines: []draftLine{{FactID: 1, RuleID: 1, EnrollmentID: 1, PersonID: 1, Amount: 99}}}
	first, err := digestDraft(1, draft, []paymentInput{fact})
	if err != nil {
		t.Fatal(err)
	}
	fact.Fact.SalesCompanyID = "second"
	next, err := digestDraft(1, draft, []paymentInput{fact})
	if err != nil || string(first) == string(next) {
		t.Fatalf("company change did not invalidate the draft, error=%v", err)
	}
}
