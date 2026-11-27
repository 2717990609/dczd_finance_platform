package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestMonthlyPoolRefundUsesOriginalAwardAcrossRuleVersions(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	service := workspace{db: db, development: true}
	const ruleID = "10000000-0000-4000-8000-000000000811"
	const otherRuleID = "10000000-0000-4000-8000-000000000812"
	const packageID = "10000000-0000-4000-8000-000000000810"
	const unit = "10000000-0000-4000-8000-000000000813"
	_, err := db.Exec(ctx, `
 INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE VALUES(805,'DCAI_VIP','会员','calendar',25);
 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
 VALUES(805,gen_random_uuid(),'test','DCAI','DCAI','fixture','2020-01-01',sha256('fixture'::bytea));
 INSERT INTO organization_units(id,name,kind,status) VALUES('`+unit+`','交付','COMPANY','ACTIVE');
 INSERT INTO practice_pools(product_id,unit_id,name) VALUES(805,'`+unit+`','交付池');
 INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
 VALUES('`+packageID+`',805,'period-test','交付','DCAI','COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE','period-test',false);
 INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,
 sku_code,sku_label,mapping_version,effective_from,observed_at,source_product_code)
 VALUES(gen_random_uuid(),805,'DCAI','VIP','ORDER','vip','first','VIP','FIRST','会员',1,'2020-01-01','2020-01-01','fixture');`)
	if err != nil {
		t.Fatal(err)
	}
	const rule = `{
 "amount_basis":"ACTUAL_RECEIVED","recognition_trigger":"PAID","refund_policy":"PROPORTIONAL","stacking_policy":"SUM",
 "rounding_policy":{"mode":"TRUNCATE","scale":2},"mixed_arrangements":true,
 "policy_scope":{"sales_systems":["DCAI"],"business_sku_codes":["FIRST"],"financial_event_types":["order.paid"]},
 "settlement_policy":{"cycle":"MONTHLY_25_TO_24","negative_balance_policy":"CARRY_FORWARD"},
 "benefit_actions":[{"item_key":"delivery","label":"交付提成","type":"TIER_RATE","metric_code":"MONTHLY_NET_RECEIPTS",
 "metric_unit":"AMOUNT_MINOR","beneficiary_mode":"TEAM_POOL","role":"TEAM_POOL","pool_code":"team-pool:1",
 "pool_name":"交付池","pool_party_id":"unit:` + unit + `","pool_organization_unit_id":"` + unit + `","tier_table":[{"rate_bps":%d}]}]}`
	_, err = db.Exec(ctx, `INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,effective_to,content_hash,definition,observed_at)
 VALUES($1,$3,1,'PUBLISHED','2026-05-25 00:00+08','2026-06-25 00:00+08','v1',$4,'2020-01-01'),
       ($2,$3,2,'PUBLISHED','2026-06-25 00:00+08',NULL,'v2',$5,'2020-01-01')`,
		ruleID, otherRuleID, packageID, fmt.Sprintf(rule, 2500), fmt.Sprintf(rule, 3000))
	if err != nil {
		t.Fatal(err)
	}
	factTime := func(month, day int) time.Time {
		return time.Date(2026, time.Month(month), day, 12, 0, 0, 0, shanghai)
	}
	save := func(facts ...sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, facts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	pay := func(id string, at time.Time) {
		amount := int64(49900)
		save(sourceFact{Source: "DCAI", Domain: "VIP", SourceKey: "fixture", Kind: "ORDER_PAID", OrderID: id,
			PaymentID: id, RecordKey: "vip:" + id, Version: "1", AmountMinor: &amount, OccurredAt: at, UpdatedAt: at,
			Classification: sourceClassification{Entity: "ORDER", Key: "first", Classifier: "VIP"}})
	}
	refund := func(id string, amount int64, at time.Time) {
		save(sourceFact{Source: "DCAI", Domain: "VIP", SourceKey: "fixture", Kind: "REFUND_SUCCEEDED", OrderID: "101",
			RefundID: id, RecordKey: "refund:" + id, Version: "1", AmountMinor: &amount, OccurredAt: at, UpdatedAt: at,
			RefundsPayment: []sourcePaymentReference{{OrderID: "101", RecordID: "101"}}})
	}
	open := func(month string) draftSummary {
		t.Helper()
		value, err := service.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: ruleID, Month: month})
		if err != nil {
			t.Fatal(err)
		}
		return value.(draftSummary)
	}
	freeze := func(draft draftSummary) {
		t.Helper()
		if _, err := db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`,
			draft.PeriodID, draft.Version); err != nil {
			t.Fatal(err)
		}
	}
	assertDraft := func(draft draftSummary, accrued, refunded int64, count int) {
		t.Helper()
		if draft.Total != accrued+refunded || draft.RefundTotal != refunded || draft.RefundCount != count || draft.IssueCount != 0 {
			t.Fatalf("unexpected draft: %+v, want gross %d, refund %d (%d rows)", draft, accrued, refunded, count)
		}
	}
	pay("101", factTime(6, 7))
	june := open("2026-06")
	assertDraft(june, 12475, 0, 0)
	freeze(june)
	pay("102", factTime(6, 29))
	refund("first", 24950, factTime(7, 1))
	july := open("2026-07")
	assertDraft(july, 14970, -6237, 1)
	retry := open("2026-07")
	if retry.Version != july.Version {
		t.Fatalf("unchanged July retry wrote version %d over %d", retry.Version, july.Version)
	}
	freeze(july)
	pay("103", factTime(7, 27))
	refund("last", 24950, factTime(8, 1))
	august := open("2026-08")
	assertDraft(august, 14970, -6238, 1)
	var carried int64
	if err := db.QueryRow(ctx, `SELECT coalesce(sum(carry_in_minor),0) FROM catalog_aggregate_draft_groups WHERE period_id=$1`, august.PeriodID).Scan(&carried); err != nil || carried != 0 {
		t.Fatalf("July's amount must not reappear as August principal carry: %d (%v)", carried, err)
	}
}

func TestMonthlyPoolRefundDoesNotConsumeAnotherArrangement(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	_, err := db.Exec(ctx, `
 INSERT INTO products(id,code,name,period_mode) OVERRIDING SYSTEM VALUE VALUES(805,'DCAI_VIP','会员','calendar');
 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
 VALUES(805,gen_random_uuid(),'test','DCAI','DCAI','fixture','2020-01-01',sha256('fixture'::bytea));
 INSERT INTO organization_units(id,kind,name,status) VALUES('10000000-0000-4000-8000-000000000813','COMPANY','交付','ACTIVE');
 INSERT INTO practice_pools(id,product_id,unit_id,name) OVERRIDING SYSTEM VALUE
 VALUES(1,805,'10000000-0000-4000-8000-000000000813','团队池');
 INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
 VALUES('10000000-0000-4000-8000-000000000810',805,'period-test','交付','DCAI','COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE','fixture',false);
 INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at)
 VALUES('10000000-0000-4000-8000-000000000811','10000000-0000-4000-8000-000000000810',1,'PUBLISHED',
 '2026-05-25 00:00+08','fixture','{"benefit_actions":[]}','2026-05-25 00:00+08');
 INSERT INTO periods(id,product_id,name,ordinal,starts_on,ends_on,aggregate_package_id,report_month) OVERRIDING SYSTEM VALUE
 SELECT n,805,'测试',n,starts_on,ends_on,'10000000-0000-4000-8000-000000000810',date_trunc('month',ends_on)::date
 FROM (VALUES(1,'2026-05-25'::date,'2026-06-24'::date),(2,'2026-06-25','2026-07-24'),
 (3,'2026-07-25','2026-08-24')) v(n,starts_on,ends_on);
 INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at,catalog_refunds_ready)
 VALUES(1,1,sha256('1'::bytea),100,1,0,now(),true),(2,1,sha256('2'::bytea),100,2,0,now(),true),
 (3,1,sha256('3'::bytea),70,1,0,now(),true);
 INSERT INTO source_order_versions(id,source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact) OVERRIDING SYSTEM VALUE
 SELECT n,'DCAI',n::text,'','record:'||n,'1',kind,at,at,'{}'
 FROM (VALUES(1,'ORDER_PAID','2026-06-01'::timestamptz),(2,'ORDER_PAID','2026-07-01'),
 (3,'ORDER_PAID','2026-07-02'),(4,'REFUND_SUCCEEDED','2026-07-03'),(5,'ORDER_PAID','2026-08-01')) v(n,kind,at);
 INSERT INTO calculation_draft_lines(id,period_id,source_version_id,component_type,base_minor,amount_minor,catalog_rule_id,beneficiary_unit_id,catalog_pool_id) OVERRIDING SYSTEM VALUE
 SELECT line,period,fact,item,amount,amount,'10000000-0000-4000-8000-000000000811','10000000-0000-4000-8000-000000000813',1
 FROM (VALUES(1,1,1,'delivery',100),(2,2,2,'delivery',40),(3,2,3,'marketing',60),(4,3,5,'delivery',70)) v(line,period,fact,item,amount);
 INSERT INTO calculation_draft_refunds(period_id,original_line_id,refund_period_id,refund_source_version_id,amount_minor)
 VALUES(2,1,2,4,-100);
 INSERT INTO monthly_pool_allocations(id,period_id,unit_id,item_key,scenario_key,draft_version,basis_token,revision) OVERRIDING SYSTEM VALUE
 VALUES(1,1,'10000000-0000-4000-8000-000000000813','1','team-pool',1,'fixture',1);
 INSERT INTO monthly_pool_allocation_shares(id,allocation_id,amount_minor) OVERRIDING SYSTEM VALUE VALUES(1,1,100);
 INSERT INTO monthly_pool_allocation_funding(share_id,source_line_id,amount_minor) VALUES(1,1,100);
 INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(1,1,'fixture');`)
	if err != nil {
		t.Fatal(err)
	}
	assertLots := func(period int64, want map[int64]int64) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			stages, err := readPoolStages(ctx, tx, period)
			if err != nil {
				return err
			}
			for _, stage := range stages {
				for _, lot := range stage.lots {
					if lot.Remaining != want[lot.LineID] {
						t.Errorf("period %d line %d has %d available, want %d", period, lot.LineID, lot.Remaining, want[lot.LineID])
					}
					delete(want, lot.LineID)
				}
			}
			if len(want) != 0 {
				t.Errorf("period %d missing funding lines: %v", period, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertLots(2, map[int64]int64{1: 0, 2: 0, 3: 60})
	if _, err := db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(2,1,'fixture')`); err != nil {
		t.Fatal(err)
	}
	assertLots(3, map[int64]int64{1: 0, 2: 0, 3: 60, 4: 10})
}
