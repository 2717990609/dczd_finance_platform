package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMonthlyReportCombinesVersionsAndPreservesBookedScope(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	// A fixed-period product closes in the named cycle. Two classes and an
	// independently booked refund month belong to the same report.
	const seed = `
INSERT INTO products(code,name,period_mode,cycle_start_day) VALUES('AI','AI 会员','calendar',25),('CLASS','实战班','class',25);
INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month) VALUES
 (1,'八月账期',202608,'2026-07-25','2026-08-24','2026-08-01'),
 (2,'前一班',1,'2026-07-30','2026-07-31',NULL),
 (2,'跨月班',2,'2026-07-31','2026-08-01',NULL),
 (2,'八月退款',202608,'2026-07-25','2026-08-24','2026-08-01'),
 (2,'待试算班',3,'2026-08-22','2026-08-23',NULL);
INSERT INTO organization_units(id,kind,name,status,parent_id) VALUES
 ('00000000-0000-0000-0000-000000000001','COMPANY','甲公司','ACTIVE',NULL),
 ('00000000-0000-0000-0000-000000000002','DEPARTMENT','甲一部','ACTIVE','00000000-0000-0000-0000-000000000001'),
 ('00000000-0000-0000-0000-000000000003','COMPANY','乙公司','ACTIVE',NULL),
 ('00000000-0000-0000-0000-000000000004','DEPARTMENT','乙一部','ACTIVE','00000000-0000-0000-0000-000000000003');
INSERT INTO people(name) VALUES('同名人员'),('同名人员');
INSERT INTO identity_mappings(person_id,source,external_id,valid_from)
 VALUES(1,'DCAI','seller','2026-01-01'),(2,'DCZD','seller-a','2026-01-01'),(1,'DCZD','seller-b','2026-01-01');
INSERT INTO rule_versions(product_id,version,label,components) VALUES(1,1,'会员规则','[]'),(2,1,'实战班规则','[]');
INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
 VALUES('DCAI','paid-ai','','paid:1','1','ORDER_PAID','2026-07-27','2026-07-27','{"domain":"VIP","paymentId":"pay-ai","amountMinor":10000}'),
 ('DCZD','paid-a','','paid:1','1','ORDER_PAID','2026-07-15','2026-07-15','{"domain":"PRACTICE","paymentId":"pay-a","amountMinor":20000}'),
 ('DCZD','paid-b','','paid:2','1','ORDER_PAID','2026-07-29','2026-07-29','{"domain":"PRACTICE","paymentId":"pay-b","amountMinor":30000}'),
 ('DCZD','paid-a','','refund:1','1','REFUND_SUCCEEDED','2026-08-07','2026-08-07','{"domain":"PRACTICE","refundId":"refund-a","amountMinor":5000}');
INSERT INTO calculation_drafts(period_id,rule_id,version,input_digest,total_minor,line_count,issue_count,calculated_at,performance_ready,refund_minor,refund_count)
 VALUES (1,1,1,'\x01',1000,1,0,now(),true,0,0),(2,2,1,'\x02',2000,1,0,now(),true,0,0),
 (3,2,2,'\x03',3000,1,0,now(),true,0,0),(4,NULL,1,'\x04',0,0,0,now(),true,-500,1);
INSERT INTO calculation_draft_lines(period_id,source_version_id,rule_id,component_type,identity_id,person_id,base_minor,amount_minor,company_id,department_id)
 VALUES(1,1,1,'sales',1,1,10000,1000,'00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002'),
 (2,2,2,'sales',2,2,20000,2000,'00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000004'),
 (3,3,2,'sales',3,1,30000,3000,'00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002');
INSERT INTO calculation_draft_performance(period_id,source_version_id,payment_version_id,person_id,company_id,department_id)
 VALUES(1,1,NULL,1,'00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002'),
 (2,2,NULL,2,'00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000004'),
 (3,3,NULL,1,'00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002'),
 (4,4,2,2,'00000000-0000-0000-0000-000000000003','00000000-0000-0000-0000-000000000004');
INSERT INTO calculation_draft_refunds(period_id,original_line_id,refund_period_id,refund_source_version_id,amount_minor) VALUES(4,2,4,4,-500);
INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(1,1,'fixture'),(2,1,'fixture');`
	if _, err := s.db.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	api := http.NewServeMux()
	for _, route := range s.routes() {
		api.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	type monthlyRow struct {
		ID, Name                     string
		PerformanceMinor, TotalMinor int64
		AllocationMinor              int64
	}
	type report struct {
		Snapshot                 string
		PerformanceReady         bool
		PerformanceMinor         *int64
		CommissionMinor          int64
		DuplicateFacts           int
		ConflictingPerformance   bool
		PersonPerformancePending bool
		Items                    []monthlyRow
		Periods                  []struct {
			ID         int64
			Status     string
			IssueCount int
		}
	}
	get := func(params string, status int) report {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/monthly-report?month=2026-08&"+params, nil)
		api.ServeHTTP(w, r)
		var got report
		if w.Code != status || status == http.StatusOK && json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("monthly report HTTP %d, expected %d: %s", w.Code, status, w.Body)
		}
		return got
	}
	for _, dimension := range []string{"company", "department", "person"} {
		got := get("view=current&dimension="+dimension, http.StatusOK)
		if len(got.Snapshot) != 32 || !got.PerformanceReady || got.PerformanceMinor == nil || *got.PerformanceMinor != 35000 ||
			got.CommissionMinor != 3500 || len(got.Items) != 2 || len(got.Periods) != 4 {
			t.Fatalf("%s report: %+v", dimension, got)
		}
		if got.Items[0].PerformanceMinor != 40000 || got.Items[1].PerformanceMinor != -5000 {
			t.Fatalf("%s did not retain the refund's original organization: %+v", dimension, got.Items)
		}
	}
	personal := get("view=current&dimension=person&personId=1", http.StatusOK)
	if personal.CommissionMinor != 4000 || len(personal.Items) != 1 || personal.Items[0].ID != "1" ||
		personal.PerformanceMinor == nil || *personal.PerformanceMinor != 40000 || len(personal.Periods) != 2 {
		t.Fatalf("personal report mixed same-name participants or period totals: %+v", personal)
	}
	get("view=current&dimension=person&personId=2&snapshot="+personal.Snapshot, http.StatusConflict)
	get("view=current&dimension=company&personId=1", http.StatusBadRequest)
	if _, err := s.db.Exec(ctx, "INSERT INTO people(name) VALUES('仅有业绩')"); err != nil {
		t.Fatal(err)
	}
	updateCoverage := func(personID int, ready bool) {
		t.Helper()
		if _, err := s.db.Exec(ctx, "UPDATE calculation_draft_performance SET person_id=$1 WHERE source_version_id=3", personID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, "UPDATE calculation_drafts SET performance_ready=$1 WHERE period_id=4", ready); err != nil {
			t.Fatal(err)
		}
	}
	updateCoverage(3, false)
	pending := get("view=current&dimension=person&personId=3", http.StatusOK)
	if !pending.PersonPerformancePending || pending.PerformanceMinor != nil || len(pending.Periods) != 0 || len(pending.Items) != 0 {
		t.Fatalf("withheld personal performance should still surface incomplete coverage: %+v", pending)
	}
	updateCoverage(1, true)
	confirmed := get("view=settled&dimension=company", http.StatusOK)
	if len(confirmed.Periods) != 1 || confirmed.Periods[0].ID != 1 || confirmed.Periods[0].Status != "SETTLED" || confirmed.CommissionMinor != 1000 {
		t.Fatalf("settled scope: %+v", confirmed)
	}
	class := get("view=current&dimension=person&productId=2", http.StatusOK)
	if class.CommissionMinor != 2500 || len(class.Periods) != 3 {
		t.Fatalf("product scope: %+v", class)
	}
	setMonth := func(id int, month string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		input := fmt.Sprintf(`{"periodId":%d,"month":%q}`, id, month)
		r := httptest.NewRequest(http.MethodPost, "/api/v1/period-report-month", strings.NewReader(input))
		r.Header.Set("Content-Type", "application/json")
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("set month HTTP %d, expected %d: %s", w.Code, status, w.Body)
		}
	}
	setMonth(2, "2026-08", http.StatusConflict)
	setMonth(3, "2026-09", http.StatusOK)
	get("view=current&dimension=person&productId=2&snapshot="+class.Snapshot, http.StatusConflict)
	if moved := get("view=current&dimension=person&productId=2", http.StatusOK); moved.CommissionMinor != -500 || len(moved.Periods) != 2 {
		t.Fatalf("class moved without its own postings: %+v", moved)
	}
	setMonth(3, "", http.StatusOK)
	get("view=current&dimension=company&page=2", http.StatusBadRequest)
	if stable := get("view=current&dimension=company&snapshot="+class.Snapshot+"&productId=2", http.StatusOK); stable.CommissionMinor != 2500 {
		t.Fatalf("the same snapshot could not be used for another dimension: %+v", stable)
	}
	allocation := `INSERT INTO practice_enrollments(period_key,source_enrollment_id,participant_id,membership_type,commission_eligible,signed_at,paid_order_ids,hash)
	 VALUES('PRACTICE:1:1:3','3','3',3,true,'2026-08-01',ARRAY['paid-b'],'\x01');
	 INSERT INTO practice_pools(product_id,unit_id,name) VALUES(2,'00000000-0000-0000-0000-000000000002','甲池');
	 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from) VALUES
	 ('00000000-0000-0000-0000-000000000005',2,'00000000-0000-0000-0000-000000000002','MEMBER','2026-01-01');
	 UPDATE calculation_draft_lines SET practice_enrollment_id=1,beneficiary_unit_id='00000000-0000-0000-0000-000000000002',
	  practice_pool_id=1,allocation_owner_id=1,identity_id=NULL,person_id=NULL,component_type='period:delivery:0' WHERE period_id=3;
	 INSERT INTO monthly_pool_allocations(period_id,unit_id,item_key,scenario_key,draft_version,basis_token,revision)
	 VALUES(3,'00000000-0000-0000-0000-000000000002','test','',2,(` + poolAllocationBasisSQL + `3),1);
	 INSERT INTO monthly_pool_allocation_shares(allocation_id,person_id,amount_minor) VALUES(1,2,200);
	 INSERT INTO monthly_pool_allocation_funding(share_id,source_line_id,amount_minor) VALUES(1,3,200);`
	if _, err := s.db.Exec(ctx, allocation); err != nil {
		t.Fatal(err)
	}
	withAllocation := get("view=current&dimension=person", http.StatusOK)
	if len(withAllocation.Items) != 3 || withAllocation.CommissionMinor != 3500 ||
		withAllocation.Items[1].ID != "pool" || withAllocation.Items[1].TotalMinor != 2800 ||
		withAllocation.Items[2].ID != "2" || withAllocation.Items[2].AllocationMinor != 200 || withAllocation.Items[2].TotalMinor != -300 {
		t.Fatalf("valid allocation did not move earlier money to the recipient: %+v", withAllocation.Items)
	}
	if _, err := s.db.Exec(ctx, `UPDATE monthly_pool_allocations SET revision=2 WHERE period_id=3`); err != nil {
		t.Fatal(err)
	}
	get("view=current&dimension=person&page=2&snapshot="+withAllocation.Snapshot, http.StatusConflict)
	if _, err := s.db.Exec(ctx, `UPDATE monthly_pool_allocations SET basis_token='stale' WHERE period_id=3`); err != nil {
		t.Fatal(err)
	}
	if stale := get("view=current&dimension=person", http.StatusOK); stale.Items[1].TotalMinor != 3000 || stale.Items[2].AllocationMinor != 0 {
		t.Fatalf("stale allocation still counted: %+v", stale.Items)
	}
	if _, err := s.db.Exec(ctx, `UPDATE monthly_pool_allocations SET basis_token=(`+poolAllocationBasisSQL+`3) WHERE period_id=3;
	 UPDATE organization_assignments SET valid_to='2026-07-20' WHERE id='00000000-0000-0000-0000-000000000005'`); err != nil {
		t.Fatal(err)
	}
	if stale := get("view=current&dimension=person", http.StatusOK); stale.Items[1].TotalMinor != 3000 || stale.Items[2].AllocationMinor != 0 {
		t.Fatalf("open plan still counted after membership ended before its payment: %+v", stale.Items)
	}
	if _, err := s.db.Exec(ctx, `WITH revised AS (
	 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
	 SELECT source,order_id,'replacement-line',record_key,'2',kind,updated_at,occurred_at,fact FROM source_order_versions WHERE id=1 RETURNING id
	 ) INSERT INTO calculation_draft_performance(period_id,source_version_id,person_id,company_id,department_id)
	 SELECT 3,revised.id,a.person_id,a.company_id,a.department_id FROM calculation_draft_performance a CROSS JOIN revised WHERE a.period_id=1`); err != nil {
		t.Fatal(err)
	}
	if duplicates := get("view=current&dimension=company", http.StatusOK); duplicates.DuplicateFacts != 1 || !duplicates.PerformanceReady ||
		duplicates.PerformanceMinor == nil || *duplicates.PerformanceMinor != 35000 || duplicates.CommissionMinor != 3500 {
		t.Fatalf("identical payment should count once with both commission scopes: %+v", duplicates)
	}
	if _, err := s.db.Exec(ctx, `UPDATE calculation_draft_performance SET person_id=2 WHERE period_id=3 AND source_version_id=5`); err != nil {
		t.Fatal(err)
	}
	if ambiguous := get("view=current&dimension=company", http.StatusOK); ambiguous.PerformanceReady || ambiguous.PerformanceMinor != nil ||
		!ambiguous.ConflictingPerformance || ambiguous.CommissionMinor != 3500 {
		t.Fatalf("conflicting payment assignment must not silently report performance: %+v", ambiguous)
	}
	if _, err := s.db.Exec(ctx, `UPDATE calculation_draft_performance SET person_id=1 WHERE period_id=3 AND source_version_id=5;
	 UPDATE source_order_versions SET line_id='',fact=jsonb_set(fact,'{paymentId}','"other-id"') WHERE id=5;
	 UPDATE calculation_drafts SET issue_count=2 WHERE period_id=3`); err != nil {
		t.Fatal(err)
	}
	beforeExtra := get("view=current&dimension=person", http.StatusOK)
	if !beforeExtra.ConflictingPerformance || beforeExtra.PerformanceMinor != nil || beforeExtra.Periods[1].Status != "NEEDS_REVIEW" || beforeExtra.Periods[1].IssueCount != 2 {
		t.Fatalf("same source record with another payment identity or blocked scope was hidden: %+v", beforeExtra)
	}
	newProduct, err := s.createProduct(ctx, createProductInput{Code: "EXTRA", Name: "Other", PeriodMode: "calendar"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.createPeriod(ctx, createPeriodInput{ProductID: newProduct.(map[string]int64)["id"], Name: "补登记", Ordinal: 1,
		StartsOn: "2026-08-05", EndsOn: "2026-08-10"}); err != nil {
		t.Fatal(err)
	}
	get("view=current&dimension=person&page=2&snapshot="+beforeExtra.Snapshot, http.StatusConflict)
	get("view=settled&dimension=department&snapshot="+confirmed.Snapshot, http.StatusOK)
	if _, err := s.db.Exec(ctx, `UPDATE organization_assignments SET valid_to=NULL WHERE id='00000000-0000-0000-0000-000000000005';
	 INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(3,2,'fixture');
	 UPDATE organization_assignments SET valid_to='2026-07-20' WHERE id='00000000-0000-0000-0000-000000000005'`); err != nil {
		t.Fatal(err)
	}
	frozenPool := get("view=settled&dimension=person", http.StatusOK)
	for _, item := range frozenPool.Items {
		if item.ID == "2" && item.AllocationMinor == 200 {
			return
		}
	}
	t.Fatalf("confirmed pool allocation changed after membership ended: %+v", frozenPool.Items)
}
