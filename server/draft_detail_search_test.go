package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestDraftDetailSearchIncludesRefundsAndBothCorrectionRecipients(t *testing.T) {
	service := workspace{db: integrationDB(t)}
	const seed = `
	 INSERT INTO products(code,name,period_mode) VALUES('DETAIL_SEARCH','明细查找','calendar');
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month)
	  VALUES(1,'六月',202606,'2026-06-01','2026-06-30','2026-06-01');
	 INSERT INTO people(name) VALUES('原受益人'),('更正受益人');
	 INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(1,'DCAI','seller','2026-01-01');
	 INSERT INTO organization_units(id,kind,name,status) VALUES
	  ('10000000-0000-0000-0000-000000000001','DEPARTMENT','甲部门','ACTIVE'),
	  ('10000000-0000-0000-0000-000000000002','DEPARTMENT','乙部门','ACTIVE');
	 INSERT INTO rule_versions(product_id,version,label,components) VALUES(1,1,'销售分润','[]');
	 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact) VALUES
	  ('DCAI','ORDER-1','','paid-1','v1','ORDER_PAID',now(),'2026-06-05T12:00:00+08:00','{"orderNo":"显示订单一"}'),
	  ('DCAI','ORDER-1','','paid-1','v2','ORDER_PAID',now(),'2026-06-05T12:00:00+08:00','{"orderNo":"更新后订单一"}'),
	  ('DCAI','ORDER-1','','refund-1','v1','REFUND_SUCCEEDED',now(),'2026-06-10T12:00:00+08:00','{"refundId":"refund-1"}');
	 INSERT INTO calculation_drafts(period_id,rule_id,version,input_digest,total_minor,line_count,issue_count,calculated_at,
	  refund_minor,refund_count,correction_minor,correction_count)
	 VALUES(1,1,2,'\x01',1000,1,0,now(),-200,1,100,1);
	 INSERT INTO calculation_draft_lines(period_id,source_version_id,component_type,identity_id,person_id,base_minor,amount_minor,rule_id,department_id)
	 VALUES(1,1,'sales',1,1,10000,1000,1,'10000000-0000-0000-0000-000000000001');
	 INSERT INTO calculation_draft_refunds(period_id,original_line_id,refund_period_id,refund_source_version_id,amount_minor)
	 VALUES(1,1,1,3,-200);
	 INSERT INTO calculation_draft_corrections(period_id,original_line_id,source_version_id,person_id,amount_minor,department_id)
	 VALUES(1,1,2,2,100,'10000000-0000-0000-0000-000000000002');`
	if _, err := service.db.Exec(context.Background(), seed); err != nil {
		t.Fatal(err)
	}

	type details struct {
		TotalMinor    int
		LineCount     int
		Lines         []json.RawMessage
		Refunds       []json.RawMessage
		Corrections   []json.RawMessage
		DetailHasMore map[string]bool
	}
	get := func(order, beneficiary, unit, version string, status int) details {
		t.Helper()
		query := url.Values{"periodId": {"1"}, "orderQuery": {order}, "beneficiaryQuery": {beneficiary}, "unitId": {unit}, "version": {version}}
		response := httptest.NewRecorder()
		service.getDraft(response, httptest.NewRequest(http.MethodGet, "/api/v1/drafts?"+query.Encode(), nil))
		if response.Code != status {
			t.Fatalf("HTTP %d want %d: %s", response.Code, status, response.Body)
		}
		var result details
		if status == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	initial := get("", "", "", "", http.StatusOK)
	if initial.TotalMinor != 900 || initial.LineCount != 1 || initial.DetailHasMore != nil {
		t.Fatalf("unexpected unfiltered draft: %+v", initial)
	}
	original := get("order-1", "原受益人", "", "2", http.StatusOK)
	if original.TotalMinor != initial.TotalMinor || len(original.Lines) != 1 || len(original.Refunds) != 1 || len(original.Corrections) != 1 || len(original.DetailHasMore) != 3 {
		t.Fatalf("original recipient search: %+v", original)
	}
	updated := get("更新后订单一", "更正受益人", "10000000-0000-0000-0000-000000000002", "2", http.StatusOK)
	if len(updated.Lines) != 0 || len(updated.Refunds) != 0 || len(updated.Corrections) != 1 {
		t.Fatalf("correction and organization search: %+v", updated)
	}
	excluded := get("order-1", "原受益人", "10000000-0000-0000-0000-000000000001", "2", http.StatusOK)
	if len(excluded.Lines) != 1 || len(excluded.Refunds) != 1 || len(excluded.Corrections) != 0 {
		t.Fatalf("organization leaked a correction: %+v", excluded)
	}
	literal := get("%_", "", "", "2", http.StatusOK)
	if len(literal.Lines)+len(literal.Refunds)+len(literal.Corrections) != 0 {
		t.Fatalf("wildcards interpreted in literal search: %+v", literal)
	}
	get("order-1", "", "", "1", http.StatusConflict)
}

func TestConfirmedDraftLinePageUsesOrganizationBeforeLimit(t *testing.T) {
	service := workspace{db: integrationDB(t)}
	const department = "20000000-0000-0000-0000-000000000002"
	const seed = `
	 INSERT INTO products(code,name,period_mode) VALUES('DETAIL_PAGE','明细分页','calendar');
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month)
	  VALUES(1,'六月',202606,'2026-06-01','2026-06-30','2026-06-01');
	 INSERT INTO people(name) VALUES('受益人');
	 INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(1,'DCAI','seller','2026-01-01');
	 INSERT INTO organization_units(id,kind,name,status) VALUES
	  ('20000000-0000-0000-0000-000000000001','DEPARTMENT','甲部门','ACTIVE'),
	  ('20000000-0000-0000-0000-000000000002','DEPARTMENT','乙部门','ACTIVE');
	 INSERT INTO rule_versions(product_id,version,label,components) VALUES(1,1,'销售分润','[]');
	 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
	  SELECT 'DCAI','PAY-'||n,'','paid-'||n,'v1','ORDER_PAID','2026-06-05','2026-06-05',
	         jsonb_build_object('orderNo','显示订单 '||n)
	  FROM generate_series(1,120) n ORDER BY n;
	 INSERT INTO calculation_drafts(period_id,rule_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	  VALUES(1,1,2,'\x01',120000,120,0,'2026-07-01');
	 INSERT INTO calculation_draft_lines(period_id,source_version_id,component_type,identity_id,person_id,base_minor,amount_minor,rule_id,department_id)
	  SELECT 1,id,'sales',1,1,10000,1000,1,CASE WHEN id<=60
	   THEN '20000000-0000-0000-0000-000000000001'::uuid ELSE '20000000-0000-0000-0000-000000000002'::uuid END
	  FROM source_order_versions ORDER BY id;
	 INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(1,2,'财务');
	 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
	  VALUES('DCAI','PAY-120','','paid-120','v2','ORDER_PAID','2026-07-02','2026-06-05','{"orderNo":"变更后订单"}');`
	if _, err := service.db.Exec(context.Background(), seed); err != nil {
		t.Fatal(err)
	}

	get := func(after, order string) []string {
		t.Helper()
		query := url.Values{"periodId": {"1"}, "version": {"2"}, "unitId": {department}, "afterLineId": {after}, "orderQuery": {order}}
		response := httptest.NewRecorder()
		service.getDraft(response, httptest.NewRequest(http.MethodGet, "/api/v1/drafts?"+query.Encode(), nil))
		if response.Code != http.StatusOK {
			t.Fatalf("HTTP %d: %s", response.Code, response.Body)
		}
		var page struct {
			ScopeLineCount int
			Settlement     struct{ DraftVersion int }
			Lines          []struct{ OrderNo, SourceVersion string }
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.ScopeLineCount != 60 || page.Settlement.DraftVersion != 2 {
			t.Fatalf("organization count %d or confirmed version %d changed", page.ScopeLineCount, page.Settlement.DraftVersion)
		}
		orders := make([]string, 0, len(page.Lines))
		for _, row := range page.Lines {
			if row.SourceVersion != "v1" {
				t.Fatalf("confirmed row picked source version %s", row.SourceVersion)
			}
			orders = append(orders, row.OrderNo)
		}
		return orders
	}
	if page := get("", ""); len(page) != 50 || page[0] != "显示订单 61" || page[49] != "显示订单 110" {
		t.Fatalf("wrong first organization page: %v", page)
	}
	if page := get("110", ""); len(page) != 10 || page[0] != "显示订单 111" || page[9] != "显示订单 120" {
		t.Fatalf("wrong second organization page: %v", page)
	}
	if page := get("", "PAY-120"); len(page) != 1 || page[0] != "显示订单 120" {
		t.Fatalf("search did not reach past the first page: %v", page)
	}
}
