package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCatalogPaymentQueuesOnlyAggregatePackagesThatIncludeItsSKU(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	const anchor = 805
	const sourceProduct = 806
	const member = "10000000-0000-4000-8000-000000000981"
	const health = "10000000-0000-4000-8000-000000000982"
	fixtures := []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE VALUES
		 (805,'FINANCE_ANCHOR','整期规则产品','calendar',1),(806,'FINANCE_MEMBER','会员 SKU 产品','calendar',1)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		 VALUES(805,gen_random_uuid(),'test','DCAI','DCAI','combined','2026-10-01',sha256('one'::bytea)),
		 (806,gen_random_uuid(),'test','DCAI','DCAI','membership','2026-10-01',sha256('two'::bytea))`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,
		 sku_code,sku_label,mapping_version,effective_from,observed_at,source_product_code) VALUES
		 (gen_random_uuid(),806,'DCAI','VIP','VIP_ORDER','ai_vip_order','first','fixture','MEMBER','会员',1,'2026-01-01','2026-10-01','membership')`,
		`INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused) VALUES
		 ('` + member + `',805,'membership-aggregate','会员整期','DCAI','COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE','membership',false),
		 ('` + health + `',805,'health-aggregate','大使整期','DCAI','COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE','health',false)`,
		`INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at) VALUES
		 (gen_random_uuid(),'` + member + `',1,'PUBLISHED','2026-09-01','member','{"policy_scope":{"business_sku_codes":["MEMBER"]}}','2026-10-01'),
		 (gen_random_uuid(),'` + health + `',1,'PUBLISHED','2026-09-01','health','{"policy_scope":{"business_sku_codes":["HEALTH"]}}','2026-10-01')`,
	}
	for _, sql := range fixtures {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	var memberPeriod, healthPeriod int64
	for _, fixture := range []struct {
		packageID string
		periodID  *int64
	}{{member, &memberPeriod}, {health, &healthPeriod}} {
		if err := db.QueryRow(ctx, `INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,report_month,aggregate_package_id)
		 VALUES($1,'十月整期',202610,'2026-10-01','2026-10-31','2026-10-01',$2) RETURNING id`, anchor, fixture.packageID).
			Scan(fixture.periodID); err != nil {
			t.Fatal(err)
		}
	}
	var ordinary int64
	if err := db.QueryRow(ctx, `INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month)
	 VALUES($1,'十月会员',202610,'2026-10-01','2026-10-31','2026-10-01') RETURNING id`, sourceProduct).Scan(&ordinary); err != nil {
		t.Fatal(err)
	}
	paid := int64(49900)
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, shanghai)
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCAI", Domain: "VIP", SourceKey: "membership", Kind: "ORDER_PAID",
			RecordKey: "ai_vip_order:1", OrderID: "1", PaymentID: "1", AmountMinor: &paid, Version: "1", OccurredAt: at, UpdatedAt: at}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ctx, "SELECT period_id FROM draft_refresh_queue ORDER BY period_id")
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || !slices.Equal(got, []int64{memberPeriod, ordinary}) {
		t.Fatalf("membership should refresh only its ordinary period and participating package, not period %d: %v / %v", healthPeriod, got, err)
	}
}

func TestFinancialInputChangesRefreshOnlyAffectedDrafts(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	day := 1
	product := id(s.createProduct(ctx, createProductInput{Code: "AUTO", Name: "自动试算样本", PeriodMode: "calendar", CycleStartDay: &day}))
	other := id(s.createProduct(ctx, createProductInput{Code: "OTHER", Name: "无关产品", PeriodMode: "calendar", CycleStartDay: &day}))
	august := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月", SettlementMonth: "2026-08"}))
	september := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月", SettlementMonth: "2026-09"}))
	unrelated := id(s.createPeriod(ctx, createPeriodInput{ProductID: other, Name: "八月其他", SettlementMonth: "2026-08"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "DEMO", SourceKey: "PRODUCT"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "财务样本"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-01-01T00:00:00+08:00"}))
	publish := func(rate int64, only *int64) {
		t.Helper()
		if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "销售分润", PeriodID: only, Components: []component{{Type: "sales", Mode: "rate", Value: rate, SourceRole: "LOCK_SALES_USER"}}}); err != nil {
			t.Fatal(err)
		}
	}
	publish(1000, nil)
	if _, err := s.db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 15, 12, 0, 0, 0, shanghai)
	store := func(rev, display string, amount int64, occurs time.Time, updated time.Time) {
		t.Helper()
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Domain: "DEMO", SourceKey: "PRODUCT", Kind: "ORDER_PAID", RecordKey: "demo:1", OrderID: "DEMO-1", OrderNo: display, PaymentID: "PAY-1", Version: rev, AmountMinor: &amount, OccurredAt: occurs, UpdatedAt: updated, Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "seller"}}}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	queued := func(ids ...int64) {
		t.Helper()
		rows, err := s.db.Query(ctx, "SELECT period_id FROM draft_refresh_queue ORDER BY period_id")
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil || len(got) != len(ids) {
			t.Fatalf("queued periods %v, want %v, error %v", got, ids, err)
		}
		for i, id := range ids {
			if got[i] != id {
				t.Fatalf("queued periods %v, want %v", got, ids)
			}
		}
	}
	value := func(period, version, total int64) {
		t.Helper()
		var v, n int64
		if err := s.db.QueryRow(ctx, `SELECT version,total_minor FROM calculation_drafts WHERE period_id=$1`, period).Scan(&v, &n); err != nil || v != version || n != total {
			t.Fatalf("draft %d version %d total %d, want %d %d: %v", period, v, n, version, total, err)
		}
	}
	store("1", "单据", 10000, at, at)
	queued(august)
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	value(august, 1, 1000)
	queued()
	store("2", "仅显示号码更正", 10000, at, at.Add(time.Hour))
	queued()
	store("3", "调整支付日期", 20000, at.AddDate(0, 1, 0), at.Add(2*time.Hour))
	queued(august, september)
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	value(august, 2, 0)
	value(september, 1, 2000)
	queued()
	publish(2000, &september)
	queued(september)
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	value(september, 2, 4000)
	queued()
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM calculation_drafts WHERE period_id=$1)`, unrelated).Scan(&exists); err != nil || exists {
		t.Fatalf("unrelated product was calculated: %v %v", exists, err)
	}
}

func TestDraftWorkerRetriesLockedPeriodAfterMissingRuleIsPublished(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	day := 1
	product := id(s.createProduct(ctx, createProductInput{Code: "RETRY", Name: "定时试算", PeriodMode: "calendar", CycleStartDay: &day}))
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月", SettlementMonth: "2026-08"}))
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var waitingGeneration int64
	var retry bool
	var message string
	if err := s.db.QueryRow(ctx, `SELECT generation,retry_on_scan,message FROM draft_refresh_queue WHERE period_id=$1`, period).
		Scan(&waitingGeneration, &retry, &message); err != nil || retry || message != errDraftRule.Error() {
		t.Fatalf("missing rules should pause that generation: %t %q %v", retry, message, err)
	}
	lock, err := s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `SELECT id FROM periods WHERE id=$1 FOR NO KEY UPDATE`, period); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "首版规则", Components: []component{
		{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "LOCK_SALES_USER"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := s.db.QueryRow(ctx, `SELECT generation,retry_on_scan,message FROM draft_refresh_queue WHERE period_id=$1`, period).
		Scan(&generation, &retry, &message); err != nil || generation <= waitingGeneration || !retry || message != "" {
		t.Fatalf("the refreshed generation should survive a locked period: %d %t %q %v", generation, retry, message, err)
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	stop := s.startDraftRefreshWorker(ctx, 10*time.Millisecond)
	defer stop()
	deadline, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var version int64
		var pending bool
		if err := s.db.QueryRow(deadline, `SELECT coalesce((SELECT version FROM calculation_drafts WHERE period_id=$1),0),
		 EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)`, period).Scan(&version, &pending); err != nil {
			t.Fatal(err)
		}
		if version == 1 && !pending {
			return
		}
		select {
		case <-deadline.Done():
			t.Fatal("the background worker did not process the retained generation")
		case <-ticker.C:
		}
	}
}
