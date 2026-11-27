package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTeamPoolDirectoryAndLedgerUseRegisteredPools(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, `
 INSERT INTO products(code,name,period_mode,cycle_start_day) VALUES('POOL_LEDGER','团队池产品','calendar',25);
 INSERT INTO organization_units(id,kind,name,status,sales_company_type) VALUES
 ('54000000-0000-4000-8000-000000000005','COMPANY','外部公司','ACTIVE','EXTERNAL');
 INSERT INTO organization_units(id,kind,name,status,parent_id) VALUES
 ('54000000-0000-4000-8000-000000000001','DEPARTMENT','交付组织','ACTIVE','54000000-0000-4000-8000-000000000005');
 INSERT INTO people(name) VALUES('池负责人'),('分配成员');`); err != nil {
		t.Fatal(err)
	}
	api := http.NewServeMux()
	for _, route := range s.routes() {
		api.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	call := func(method, url, body string, result any) {
		t.Helper()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		api.ServeHTTP(res, req)
		if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), result) != nil {
			t.Fatalf("%s %s: %d %s", method, url, res.Code, res.Body.String())
		}
	}
	const path = "/api/v1/team-pools"
	var created struct{ ID, OwnerPersonID int64 }
	call(http.MethodPost, path, `{"productId":1,"unitId":"54000000-0000-4000-8000-000000000001","name":"固定交付池","ownerPersonId":1}`, &created)
	if created.ID != 1 || created.OwnerPersonID != 1 {
		t.Fatalf("missing configured pool owner: %+v", created)
	}
	var updated struct{ ID int64 }
	call(http.MethodPost, path+"/owner", `{"productId":1,"poolId":1,"ownerPersonId":2}`, &updated)
	var listing struct {
		Items []struct {
			ID, BalanceMinor, TotalIncomeMinor, TotalSpentMinor int64
			OwnerName                                           string
		}
	}
	call(http.MethodGet, "/api/v1/team-pool-ledger", "", &listing)
	if len(listing.Items) != 1 || listing.Items[0].ID != 1 || listing.Items[0].BalanceMinor != 0 || listing.Items[0].OwnerName != "分配成员" {
		t.Fatalf("registered empty pool missing: %+v", listing)
	}
	if _, err := s.db.Exec(ctx, `
 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month) VALUES
	(1,'首期',202607,'2026-06-25','2026-07-24','2026-07-01'),(1,'次期',202608,'2026-07-25','2026-08-24','2026-08-01');
 INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at,catalog_refunds_ready)
	VALUES(1,1,'\x01',0,0,0,now(),true),(2,1,'\x02',0,0,0,now(),true);
 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
 VALUES ('DCAI','ORDER-1','','payment:1','1','ORDER_PAID',now(),now(),'{}'),
 ('DCAI','ORDER-2','','payment:2','1','ORDER_PAID',now(),now(),'{}'),
 ('DCAI','ORDER-1','','refund:1','1','REFUND_SUCCEEDED',now(),now(),'{}');
 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
 VALUES(1,'54000000-0000-4000-8000-000000000002','test','AI','DCAI','pool.ledger',now(),sha256('fixture'::bytea));
 INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
 VALUES('54000000-0000-4000-8000-000000000003',1,'pool-rule','团队池规则','AI','COMPONENT_RULE','BUSINESS_SCENARIO','NEW',false);
 INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at)
 VALUES('54000000-0000-4000-8000-000000000004','54000000-0000-4000-8000-000000000003',1,'PUBLISHED','2026-06-25','hash','{}',now());
 INSERT INTO calculation_draft_lines(period_id,source_version_id,component_type,base_minor,amount_minor,catalog_rule_id,beneficiary_unit_id,catalog_pool_id)
 VALUES(1,1,'delivery',5000,5000,'54000000-0000-4000-8000-000000000004','54000000-0000-4000-8000-000000000001',1),
 (1,2,'delivery',1000,1000,'54000000-0000-4000-8000-000000000004','54000000-0000-4000-8000-000000000001',1);
 INSERT INTO calculation_draft_refunds(period_id,original_line_id,refund_period_id,refund_source_version_id,amount_minor) VALUES(2,1,2,3,-300);
 INSERT INTO monthly_pool_allocations(period_id,unit_id,item_key,scenario_key,draft_version,basis_token,revision)
	VALUES (1,'54000000-0000-4000-8000-000000000001','1','team-pool',1,'fixture',1),
	       (2,'54000000-0000-4000-8000-000000000001','1','team-pool',1,'fixture',1);
 INSERT INTO monthly_pool_allocation_shares(allocation_id,person_id,amount_minor)
	VALUES (1,NULL,1500),(1,2,500),(2,NULL,2500);
 INSERT INTO monthly_pool_allocation_funding(share_id,source_line_id,amount_minor) VALUES(1,1,500),(1,2,1000),(2,1,500),(3,1,2500);
 INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(1,1,'fixture');`); err != nil {
		t.Fatal(err)
	}
	call(http.MethodGet, "/api/v1/team-pool-ledger", "", &listing)
	if len(listing.Items) != 1 || listing.Items[0].TotalSpentMinor != 2000 || listing.Items[0].TotalIncomeMinor != 6000 || listing.Items[0].BalanceMinor != 4000 {
		t.Fatalf("unconfirmed allocations or duplicate spending: %+v", listing)
	}
	var details struct {
		Entries []struct {
			Kind        string
			AmountMinor int64
		}
		Total int
	}
	call(http.MethodGet, "/api/v1/team-pool-ledger?key=pool%3A1&periodId=1", "", &details)
	if len(details.Entries) != 4 || details.Total != 4 {
		t.Fatalf("should return two credits and two allocations, without multiplying shares by funding lines: %+v", details)
	}
	var periods struct {
		Periods []struct{ PeriodID, TotalSpentMinor int64 }
		Total   int
	}
	call(http.MethodGet, "/api/v1/team-pool-ledger?key=pool%3A1", "", &periods)
	if len(periods.Periods) != 1 || periods.Periods[0].PeriodID != 1 || periods.Periods[0].TotalSpentMinor != 2000 {
		t.Fatalf("should include only the booked period summary: %+v", periods)
	}
	call(http.MethodGet, "/api/v1/team-pool-ledger?productId=1&scope=EXTERNAL&unitId=54000000-0000-4000-8000-000000000005", "", &listing)
	if len(listing.Items) != 1 || listing.Items[0].BalanceMinor != 4000 {
		t.Fatalf("the parent and inherited external scope should include the pool: %+v", listing)
	}
	call(http.MethodGet, "/api/v1/team-pool-ledger?scope=INTERNAL", "", &listing)
	if len(listing.Items) != 0 {
		t.Fatalf("internal scope included an external pool: %+v", listing)
	}
	call(http.MethodGet, "/api/v1/team-pool-ledger?key=pool%3A1&periodId=1&scope=INTERNAL", "", &details)
	if len(details.Entries) != 0 || details.Total != 0 {
		t.Fatalf("the drawer should use the same organization scope as the list: %+v", details)
	}
	var draft poolDraftReport
	call(http.MethodGet, "/api/v1/draft-pools?periodId=1&version=1", "", &draft)
	if len(draft.Items) != 1 {
		t.Fatalf("expected the pool in the unfiltered allocation report: %+v", draft)
	}
	call(http.MethodGet, "/api/v1/draft-pools?periodId=1&version=1&scope=INTERNAL", "", &draft)
	if len(draft.Items) != 0 {
		t.Fatalf("allocation report included an external pool: %+v", draft)
	}

	var retention struct {
		Items []struct{ CurrentBalanceMinor int64 }
	}
	call(http.MethodGet, "/api/v1/department-retention?month=2026-08", "", &retention)
	if len(retention.Items) != 1 || retention.Items[0].CurrentBalanceMinor != 1500 {
		t.Fatalf("a saved distribution draft must not credit retention: %+v", retention)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(2,1,'fixture')`); err != nil {
		t.Fatal(err)
	}
	call(http.MethodGet, "/api/v1/team-pool-ledger", "", &listing)
	call(http.MethodGet, "/api/v1/department-retention?month=2026-08", "", &retention)
	if len(listing.Items) != 1 || listing.Items[0].TotalSpentMinor != 4800 || listing.Items[0].BalanceMinor != 1200 ||
		len(retention.Items) != 1 || retention.Items[0].CurrentBalanceMinor != 4000 {
		t.Fatalf("settlement must debit refund and allocations once and credit retention once: %+v %+v", listing, retention)
	}
}
