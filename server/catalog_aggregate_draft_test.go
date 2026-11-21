package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCatalogAggregateDraftUsesAnIndependentPeriodAndLatestOrderReferences(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	s := workspace{db: db, sources: map[string]sourceConfig{"DCAI": {BaseURL: "https://fixture.invalid", Token: "fixture"}}}
	const productID = 805
	const versionID = "10000000-0000-4000-8000-000000000805"
	statements := []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE
		 VALUES(805,'AGGREGATE_DRAFT','整期样本','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		 VALUES(805,gen_random_uuid(),'test','DCAI','DCAI','fixture','2026-10-01',sha256('fixture'::bytea))`,
		`INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
		 VALUES('10000000-0000-4000-8000-000000000820',805,'period-test','整期奖','DCAI','COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE','period-test',false)`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key, classifier,
		 sku_code,sku_label,mapping_version,effective_from,observed_at,source_product_code) VALUES
		 (gen_random_uuid(),805,'DCAI','VIP','ORDER','vip','first','VIP','FIRST','会员',1,'2026-01-01','2026-10-01','fixture'),
		 (gen_random_uuid(),805,'DCAI','VIP','ORDER','vip','second','VIP','SECOND','商品',1,'2026-01-01','2026-10-01','fixture')`,
		`INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest,benefit_ready)
		 VALUES('DCAI','2026-10-01',sha256('fixture'::bytea),true)`,
		`INSERT INTO organization_units(id,name,kind,status) VALUES('10000000-0000-4000-8000-000000000821','收款公司','COMPANY','ACTIVE')`,
		`INSERT INTO catalog_benefit_organizations(source,party_uuid,unit_id,observed_at)
		 VALUES('DCAI','10000000-0000-4000-8000-000000000822','10000000-0000-4000-8000-000000000821','2026-10-01')`,
		`INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(5801,'整期收款人')`,
	}
	for _, sql := range statements {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	definition := json.RawMessage(`{
	 "amount_basis":"ACTUAL_RECEIVED","recognition_trigger":"PAID","refund_policy":"PROPORTIONAL","stacking_policy":"SUM",
	 "rounding_policy":{"mode":"HALF_UP","scale":2},
	 "policy_scope":{"sales_systems":["DCAI"],"business_sku_codes":["FIRST","SECOND"],"financial_event_types":["order.paid"]},
	 "settlement_policy":{"cycle":"MONTHLY","negative_balance_policy":"CARRY_FORWARD"},
	 "benefit_actions":[{"item_key":"ops","label":"综合运营","type":"TIER_RATE","metric_code":"MONTHLY_NET_RECEIPTS","metric_unit":"AMOUNT_MINOR",
	  "beneficiary_mode":"PARTY","company_beneficiary_party_id":"10000000-0000-4000-8000-000000000822",
	  "tier_table":[{"rate_bps":1500}]},
	  {"item_key":"individual","label":"个人分润","type":"TIER_RATE","metric_code":"MONTHLY_NET_RECEIPTS","metric_unit":"AMOUNT_MINOR",
	   "beneficiary_mode":"PARTY","designated_party_id":"person:5801","role":"DIRECT_BENEFIT","tier_table":[{"rate_bps":500}]}]}`)
	if _, err := db.Exec(ctx, `INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at)
	 VALUES($1,'10000000-0000-4000-8000-000000000820',1,'PUBLISHED','2026-07-31 16:00Z','fixture',$2,'2026-10-01')`, versionID, definition); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: "会员逐单", SettlementMonth: "2026-08"}); err != nil {
		t.Fatal(err)
	}
	var sourceID int64
	if err := db.QueryRow(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
	  VALUES('DCAI','101','','vip:101','1','ORDER_PAID','2026-08-05', '{"domain":"VIP","sourceKey":"fixture","paymentId":"101","amountMinor":20000}',
	  '2026-08-05') RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
	 VALUES($1,'ORDER','first','VIP')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `WITH paid AS (
	  INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
	  VALUES('DCAI','104','','vip:104','1','ORDER_PAID','2026-08-06',
	   '{"domain":"VIP","sourceKey":"fixture","paymentId":"104","orderNo":"第二笔订单","amountMinor":5000}','2026-08-06')
	  RETURNING id
	 ) INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
	 SELECT id,'ORDER','first','VIP' FROM paid`); err != nil {
		t.Fatal(err)
	}
	actor := financeRuleActor{subject: "fixture", permits: func(product int64, _ bool) bool { return product == productID }}
	var periodID int64
	input := catalogAggregatePeriodInput{RuleID: versionID, Month: "2026-08"}
	open := func() int64 {
		t.Helper()
		var id int64
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			var err error
			id, err = createCatalogAggregatePeriod(ctx, tx, input, actor)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	activity := func(month string) *catalogPeriodGroupReport {
		t.Helper()
		reply := httptest.NewRecorder()
		s.getCatalogPeriodActivity(reply, httptest.NewRequest(http.MethodGet,
			"/api/v1/catalog-period-activity?ruleId="+versionID+"&month="+month, nil))
		var report catalogPeriodActivity
		if err := json.Unmarshal(reply.Body.Bytes(), &report); err != nil || reply.Code != http.StatusOK || report.Grouping == nil {
			t.Fatalf("expected grouped activity: %d %s %v", reply.Code, reply.Body.String(), err)
		}
		return report.Grouping
	}
	periodID = open()
	if open() != periodID {
		t.Fatal("reopening the same rule package and month should reuse its period")
	}
	draft, err := s.calculateCurrentDraft(ctx, periodID, 0)
	if err != nil || draft.Total != 5000 || draft.IssueCount != 0 || draft.LineCount != 2 {
		t.Fatalf("one rule should calculate both company and personal awards: %+v %v", draft, err)
	}
	var individualAmount int64
	if err := db.QueryRow(ctx, `SELECT amount_minor FROM calculation_draft_lines
	 WHERE period_id=$1 AND person_id=5801 AND beneficiary_unit_id IS NULL AND allocation_owner_id IS NULL`, periodID).
		Scan(&individualAmount); err != nil || individualAmount != 1250 {
		t.Fatalf("individual award %d should be booked against the person: %v", individualAmount, err)
	}
	var grouped int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM calculation_draft_lines l
	 JOIN catalog_aggregate_draft_groups g ON (g.period_id,g.group_key)=(l.period_id,l.aggregate_group_key)
	 WHERE l.period_id=$1 AND l.source_version_id IS NULL AND g.anchor_payment_id IS NULL`, periodID).Scan(&grouped); err != nil || grouped != 2 {
		t.Fatalf("period awards should reference the group directly: %d %v", grouped, err)
	}
	for order, count := range map[string]int{"101": 2, "第二笔订单": 2, "不存在的订单": 0} {
		var detail struct {
			Lines []struct{ AggregateGroupKey string }
		}
		err := db.QueryRow(ctx, filteredDraftDetailSQL, periodID, 0, 0, draft.Version, 0, 0, nil, order, "").Scan(&detail)
		if err != nil || len(detail.Lines) != count || count > 0 && detail.Lines[0].AggregateGroupKey == "" {
			t.Fatalf("search by order %q should find its group awards: %+v %v", order, detail, err)
		}
	}
	var facts, performance, ordinaryPeriods int
	if err := db.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM catalog_aggregate_draft_events WHERE period_id=$1 AND source_version_id=$2),
	 (SELECT count(*) FROM calculation_draft_performance WHERE period_id=$1),
	 (SELECT count(*) FROM periods WHERE product_id=$3 AND aggregate_package_id IS NULL)`, periodID, sourceID, productID).
		Scan(&facts, &performance, &ordinaryPeriods); err != nil || facts != 1 || performance != 0 || ordinaryPeriods != 1 {
		t.Fatalf("source references %d, duplicate performance %d, ordinary periods %d: %v", facts, performance, ordinaryPeriods, err)
	}
	if next, err := s.calculateCurrentDraft(ctx, periodID, 0); err != nil || next.Version != draft.Version {
		t.Fatalf("unchanged source must retain latest draft: %+v %v", next, err)
	}

	input.Month = "2026-09"
	september := open()
	if _, err := db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
	 VALUES('DCAI','101','','vip:101','1','REFUND_SUCCEEDED','2026-09-05',
	  '{"domain":"VIP","sourceKey":"fixture","refundId":"refund-101","amountMinor":5000,"refundsPayment":[{"orderId":"101","recordId":"101"}]}','2026-09-05')`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.calculateCurrentDraft(ctx, september, 0); err != nil || result.IssueCount != 1 {
		t.Fatalf("an unconfirmed preceding month must be visible for review: %+v %v", result, err)
	}
	if report := activity("2026-09"); !report.PriorUnsettled {
		t.Fatalf("preview must flag an unconfirmed preceding month: %+v", report)
	}
	if _, err := db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", productID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "UPDATE source_write_state SET provider_key=$1 WHERE source='DCAI'", s.dcaiSourceKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,completed_through,last_success_at,next_updated_at,next_id)
	 VALUES('VIP_ORDERS',$1,'2026-07-01',now()-interval '1 minute',now(),now(),'0')`, s.dcaiSourceKey()); err != nil {
		t.Fatal(err)
	}
	freeze := func(period, version int64) {
		t.Helper()
		reply := httptest.NewRecorder()
		s.getSettlementReview(reply, httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", period, version), nil))
		var review settlementReview
		if err := json.Unmarshal(reply.Body.Bytes(), &review); err != nil || reply.Code != http.StatusOK || review.Token == "" {
			t.Fatalf("expected a settlement review token: %d %s %v", reply.Code, reply.Body.String(), err)
		}
		if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: period, ExpectedVersion: version, ReviewToken: review.Token}, "fixture"); err != nil {
			t.Fatalf("confirm aggregate period: %v", err)
		}
	}
	freeze(periodID, draft.Version)
	if _, err := db.Exec(ctx, "UPDATE source_order_versions SET updated_at=updated_at+interval '1 second' WHERE id=$1", sourceID); err == nil {
		t.Fatal("source versions referenced by a frozen aggregate group must remain protected")
	}
	if _, err := db.Exec(ctx, `INSERT INTO organization_units(id,name,kind,status)
	 VALUES('10000000-0000-4000-8000-000000000823','后续组织','COMPANY','ACTIVE');
	 INSERT INTO catalog_benefit_organizations(source,party_uuid,unit_id,observed_at)
	 VALUES('DCAI','10000000-0000-4000-8000-000000000822','10000000-0000-4000-8000-000000000823','2026-10-01')`); err != nil {
		t.Fatal(err)
	}
	refunds, err := s.calculateCurrentDraft(ctx, september, 0)
	if err != nil || refunds.Total != 0 || refunds.IssueCount != 0 {
		t.Fatalf("refund-only month must keep the original group despite a later organization conflict: %+v %v", refunds, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM catalog_benefit_organizations WHERE unit_id='10000000-0000-4000-8000-000000000823'`); err != nil {
		t.Fatal(err)
	}
	freeze(september, refunds.Version)
	trialWorkspace := s
	trialWorkspace.development = true
	trialRequest := httptest.NewRequest(http.MethodPost, "/api/v1/catalog-period-activity", strings.NewReader(`{
	 "baseId":"`+versionID+`","effectiveFrom":"2026-09-01T00:00:00+08:00",
	 "edits":[{"itemKey":"ops","tiers":[{"rate_bps":2000}]}]}`))
	trialRequest.Header.Set("Content-Type", "application/json")
	trialResponse := httptest.NewRecorder()
	trialWorkspace.postCatalogPeriodActivity(trialResponse, trialRequest)
	if trialResponse.Code != http.StatusConflict || !strings.Contains(trialResponse.Body.String(), errSettlementClosed.Error()) {
		t.Fatalf("trial must respect the confirmed period: %d %s", trialResponse.Code, trialResponse.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM settlements WHERE period_id=$1`, periodID); err == nil {
		t.Fatal("a preceding settlement referenced by a subsequent settlement cannot be removed")
	}
	input.Month = "2026-10"
	if report := activity(input.Month); report.PriorUnsettled || len(report.Groups) != 1 ||
		report.Groups[0].Version != 1 || len(report.Groups[0].Targets) != 2 ||
		report.Groups[0].Targets[0].Name != "收款公司" || report.Groups[0].CarryIn != -5000 ||
		report.Groups[0].AfterCarry == nil || report.Groups[0].AfterCarry.CarryForward != -5000 {
		t.Fatalf("preview must show the original payees and carry in an empty month: %+v", report)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM periods
	 WHERE aggregate_package_id='10000000-0000-4000-8000-000000000820' AND report_month='2026-10-01'`).Scan(&ordinaryPeriods); err != nil || ordinaryPeriods != 0 {
		t.Fatalf("preview should leave an unopened period untouched, found %d: %v", ordinaryPeriods, err)
	}
	october := open()
	carry, err := s.calculateCurrentDraft(ctx, october, 0)
	if err != nil || carry.Total != 0 || carry.IssueCount != 0 {
		t.Fatalf("empty month should retain the original group carry: %+v %v", carry, err)
	}
	var carried, remaining, predecessor int64
	if err := db.QueryRow(ctx, `SELECT carry_in_minor,carry_out_minor,carry_from_period_id
	 FROM catalog_aggregate_draft_groups WHERE period_id=$1`, october).Scan(&carried, &remaining, &predecessor); err != nil ||
		carried != -5000 || remaining != -5000 || predecessor != september {
		t.Fatalf("empty month carry in %d out %d from %d: %v", carried, remaining, predecessor, err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
	  VALUES('DCAI','102','','vip:102','1','ORDER_PAID','2026-10-05', '{"domain":"VIP","sourceKey":"fixture","paymentId":"102","amountMinor":10000}',
	  '2026-10-05') RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
	 VALUES($1,'ORDER','second','VIP')`, sourceID); err != nil {
		t.Fatal(err)
	}
	preview := activity(input.Month)
	if preview.PriorUnsettled || len(preview.Groups) != 1 || preview.Groups[0].CarryIn != -5000 ||
		preview.Groups[0].BeforeCarry.Total != 2000 || preview.Groups[0].AfterCarry == nil ||
		preview.Groups[0].AfterCarry.Total != 1000 || preview.Groups[0].AfterCarry.Basis != 5000 {
		t.Fatalf("preview must offset the prior refund against the original group: %+v", preview)
	}
	if result, err := s.calculateCurrentDraft(ctx, october, 0); err != nil || result.Total != 1000 || result.IssueCount != 0 || result.Version <= carry.Version {
		t.Fatalf("same group offsets the earlier refund before its rate is applied: %+v %v", result, err)
	} else {
		// Simulate an earlier release's settled group and payment references.
		if _, err := db.Exec(ctx, `UPDATE catalog_aggregate_draft_groups SET anchor_payment_id=$2 WHERE period_id=$1`,
			october, sourceID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE calculation_draft_lines SET source_version_id=$2 WHERE period_id=$1`,
			october, sourceID); err != nil {
			t.Fatal(err)
		}
		freeze(october, result.Version)
		if err := migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
		var legacy struct {
			Lines []struct {
				SourceVersionID int64
				AmountMinor     int64
			}
		}
		if err := db.QueryRow(ctx, draftDetailSQL, october, 0, 0, result.Version, 0, 0).Scan(&legacy); err != nil ||
			len(legacy.Lines) != 2 || legacy.Lines[0].SourceVersionID != sourceID ||
			legacy.Lines[0].AmountMinor+legacy.Lines[1].AmountMinor != result.Total {
			t.Fatalf("migration must preserve and display the frozen legacy references: %+v %v", legacy, err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO catalog_rule_retirements(package_id,effective_from)
	 VALUES('10000000-0000-4000-8000-000000000820','2026-11-01 00:00+08')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
	 VALUES('DCAI','103','','vip:103','1','ORDER_PAID','2026-11-05',
	  '{"domain":"VIP","sourceKey":"fixture","paymentId":"103","amountMinor":90000}','2026-11-05') RETURNING id`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
	 VALUES($1,'ORDER','first','VIP')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
	 VALUES('DCAI','late-101','','vip:101','1','REFUND_SUCCEEDED','2026-11-05',
	   '{"domain":"VIP","sourceKey":"fixture","refundId":"late-refund-101","amountMinor":1000,"refundsPayment":[{"orderId":"101","recordId":"101"}]}','2026-10-15')`); err != nil {
		t.Fatal(err)
	}
	input.Month = time.Now().In(shanghai).Format("2006-01")
	if preview := activity(input.Month); len(preview.Groups) != 1 || preview.Groups[0].Refunds != 1000 {
		t.Fatalf("activity must show the late refund assigned to the current open month before drafting: %+v", preview)
	}
	current := open()
	result, err := s.calculateCurrentDraft(ctx, current, 0)
	wantIssues := 0
	if input.Month != "2026-11" {
		wantIssues = 1 // The immediately preceding aggregate month is not in this fixture.
	}
	if err != nil || result.Total != 0 || result.IssueCount != wantIssues {
		t.Fatalf("late refund stays available after retirement and the new payment is ignored: %+v %v", result, err)
	}
	if err := db.QueryRow(ctx, `SELECT refunds_minor,carry_out_minor FROM catalog_aggregate_draft_groups WHERE period_id=$1`, current).
		Scan(&carried, &remaining); err != nil || carried != 1000 || remaining != -1000 {
		t.Fatalf("late refund %d, carry %d: %v", carried, remaining, err)
	}
	if preview := activity(input.Month); len(preview.Groups) != 1 || preview.Groups[0].Refunds != carried ||
		preview.Groups[0].quote().CarryForward != remaining {
		t.Fatalf("activity after saving the draft must show the same late refund and carry: %+v", preview)
	}
	if _, err := db.Exec(ctx, `UPDATE catalog_business_routes SET effective_to='2026-11-01 00:00+08' WHERE product_id=$1`, productID); err != nil {
		t.Fatal(err)
	}
	if after, err := s.calculateCurrentDraft(ctx, current, 0); err != nil || after.Version != result.Version {
		t.Fatalf("closed sales mappings must not change the retired package's refund-only draft: %+v %v", after, err)
	}
}
