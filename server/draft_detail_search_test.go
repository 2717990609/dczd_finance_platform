package main

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestDraftBeneficiarySearchUsesLabelFromEachCatalogRuleVersion(t *testing.T) {
	service := workspace{db: integrationDB(t)}
	const seed = `
	 INSERT INTO products(code,name,period_mode) VALUES('CATALOG_DETAIL_SEARCH','目录明细查找','calendar');
	 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
	 VALUES(1,'11000000-0000-0000-0000-000000000001','TEST','AI','DCAI','sku',now(),sha256('fixture'::bytea));
	 INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
	 VALUES('11000000-0000-0000-0000-000000000002',1,'FIXTURE','目录规则','AI','COMPONENT_RULE','SKU','sku',false);
	 INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at)
	 VALUES
	 ('11000000-0000-0000-0000-000000000003','11000000-0000-0000-0000-000000000002',1,'PUBLISHED','2026-06-01','v1',
	  '{"benefit_actions":[{"item_key":"delivery","label":"原交付","pool_name":"甲资金池"}]}',now()),
	 ('11000000-0000-0000-0000-000000000004','11000000-0000-0000-0000-000000000002',2,'PUBLISHED','2026-06-02','v2',
	  '{"benefit_actions":[{"item_key":"delivery","label":"后续交付","pool_name":"乙资金池"}]}',now());
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month)
	 VALUES(1,'六月',202606,'2026-06-01','2026-06-30','2026-06-01');
	 INSERT INTO people(name) VALUES('分配负责人');
	 INSERT INTO organization_units(id,kind,name,status)
	 VALUES('11000000-0000-0000-0000-000000000005','DEPARTMENT','交付部门','ACTIVE');
	 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
	 VALUES('DCAI','OLD-ORDER','','paid-1','v1','ORDER_PAID',now(),'2026-06-01','{}'),
	       ('DCAI','NEW-ORDER','','paid-2','v1','ORDER_PAID',now(),'2026-06-02','{}');
	 INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	 VALUES(1,2,'\x01',200,2,0,now());
	 INSERT INTO calculation_draft_lines(period_id,source_version_id,component_type,base_minor,amount_minor,catalog_rule_id,beneficiary_unit_id,allocation_owner_id)
	 VALUES(1,1,'delivery',1000,100,'11000000-0000-0000-0000-000000000003','11000000-0000-0000-0000-000000000005',1),
	       (1,2,'delivery',1000,100,'11000000-0000-0000-0000-000000000004','11000000-0000-0000-0000-000000000005',1);`
	if _, err := service.db.Exec(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, order, label string
		version            int
	}{{"甲资金池", "OLD-ORDER", "原交付", 1}, {"乙资金池", "NEW-ORDER", "后续交付", 2}} {
		page := httptest.NewRecorder()
		params := url.Values{"periodId": {"1"}, "version": {"2"}, "beneficiaryQuery": {tc.name}}
		service.getDraft(page, httptest.NewRequest(http.MethodGet, "/api/v1/drafts?"+params.Encode(), nil))
		var result struct {
			Lines []struct {
				OrderID, ComponentLabel, PersonName string
				RuleVersion                         int
			}
		}
		if err := json.Unmarshal(page.Body.Bytes(), &result); page.Code != http.StatusOK || err != nil || len(result.Lines) != 1 {
			t.Fatalf("search for %s failed: %d %s %v", tc.name, page.Code, page.Body, err)
		}
		line := result.Lines[0]
		if line.OrderID != tc.order || line.PersonName != tc.name || line.ComponentLabel != tc.label || line.RuleVersion != tc.version {
			t.Fatalf("pool name and arrangement must use the line's catalog rule version: %+v", line)
		}
	}
}

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
	var refundSource struct {
		SourceVersionID       int64
		RefundSourceVersionID int64
	}
	if len(initial.Refunds) != 1 || json.Unmarshal(initial.Refunds[0], &refundSource) != nil ||
		refundSource.SourceVersionID != 1 || refundSource.RefundSourceVersionID != 3 {
		t.Fatalf("refund should link to both the original payment and the recorded refund version: %+v", refundSource)
	}
	var correctionSource struct {
		SourceVersionID           int64
		CorrectionSourceVersionID int64
		OriginalOrderNo           string
		SourceVersion             string
	}
	if len(initial.Corrections) != 1 || json.Unmarshal(initial.Corrections[0], &correctionSource) != nil ||
		correctionSource.SourceVersionID != 1 || correctionSource.CorrectionSourceVersionID != 2 ||
		correctionSource.OriginalOrderNo != "显示订单一" || correctionSource.SourceVersion != "v2" {
		t.Fatalf("correction should show the original order and open its own recorded source version: %+v", correctionSource)
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
	for _, section := range []string{"lines", "refunds", "corrections", "issues"} {
		t.Run(section, func(t *testing.T) {
			params := url.Values{"periodId": {"1"}, "version": {"2"}, "orderQuery": {"order-1"}, "section": {section}}
			response := httptest.NewRecorder()
			service.getDraft(response, httptest.NewRequest(http.MethodGet, "/api/v1/drafts?"+params.Encode(), nil))
			var result map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &result); response.Code != http.StatusOK || err != nil {
				t.Fatalf("detail page failed: %d %s %v", response.Code, response.Body, err)
			}
			var rows []json.RawMessage
			want := 1
			if section == "issues" {
				want = 0
			}
			if err := json.Unmarshal(result[section], &rows); err != nil || len(rows) != want {
				t.Fatalf("%s page should return %d rows: %s %v", section, want, result[section], err)
			}
			for key := range result {
				if key != section && key != "version" && key != "calculatedAt" && key != "detailHasMore" {
					t.Fatalf("%s page returned an unrelated field %s", section, key)
				}
			}
			if string(result["version"]) != "2" || len(result["calculatedAt"]) < 3 {
				t.Fatalf("detail page is missing its draft version: %s", response.Body)
			}
			if section != "issues" {
				var more map[string]bool
				if err := json.Unmarshal(result["detailHasMore"], &more); err != nil || !maps.Equal(more, map[string]bool{section: false}) {
					t.Fatalf("wrong pagination flags for %s: %v %v", section, more, err)
				}
			}
		})
	}
	for _, tc := range []struct {
		query, cursor string
		status        int
	}{{"unknown", "", http.StatusBadRequest}, {"refunds", "1", http.StatusConflict}} {
		params := url.Values{"periodId": {"1"}, "version": {"1"}, "section": {tc.query}, "afterRefundId": {tc.cursor}}
		response := httptest.NewRecorder()
		service.getDraft(response, httptest.NewRequest(http.MethodGet, "/api/v1/drafts?"+params.Encode(), nil))
		if response.Code != tc.status {
			t.Fatalf("section %s returned %d, want %d", tc.query, response.Code, tc.status)
		}
	}
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
	 INSERT INTO organization_units(id,kind,name,status,sales_company_type)
	  VALUES('20000000-0000-0000-0000-000000000003','COMPANY','乙公司','ACTIVE','EXTERNAL');
	 UPDATE organization_units SET parent_id='20000000-0000-0000-0000-000000000003'
	  WHERE id='20000000-0000-0000-0000-000000000002';
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
		query := url.Values{"periodId": {"1"}, "version": {"2"}, "afterLineId": {after}, "orderQuery": {order}, "scope": {"EXTERNAL"}}
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
	query := url.Values{"periodId": {"1"}, "version": {"2"}, "unitId": {department}, "afterLineId": {"110"}, "section": {"lines"}, "scope": {"EXTERNAL"}}
	response := httptest.NewRecorder()
	service.getDraft(response, httptest.NewRequest(http.MethodGet, "/api/v1/drafts?"+query.Encode(), nil))
	var page struct {
		Lines []struct{ OrderNo, SourceVersion string }
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); response.Code != http.StatusOK || err != nil || len(page.Lines) != 10 ||
		page.Lines[0].OrderNo != "显示订单 111" || page.Lines[9].OrderNo != "显示订单 120" || page.Lines[9].SourceVersion != "v1" {
		t.Fatalf("line-only page must use the original draft version, cursor and organization scope: %d %s %v", response.Code, response.Body, err)
	}

	for _, tc := range []struct {
		name, page, order, beneficiary, version, cursor string
		status, total, rows                             int
		first                                           string
	}{
		{"second page", "2", "", "", "2", "", http.StatusOK, 60, 10, "显示订单 111"},
		{"filtered second page", "2", "PAY-", "受益人", "2", "", http.StatusOK, 60, 10, "显示订单 111"},
		{"single matching order", "1", "PAY-120", "", "2", "", http.StatusOK, 1, 1, "显示订单 120"},
		{"past last page", "3", "PAY-", "受益人", "2", "", http.StatusOK, 60, 0, ""},
		{"changed version", "2", "", "", "1", "", http.StatusConflict, 0, 0, ""},
		{"page zero", "0", "", "", "2", "", http.StatusBadRequest, 0, 0, ""},
		{"page and cursor", "2", "", "", "2", "110", http.StatusBadRequest, 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := url.Values{"periodId": {"1"}, "section": {"lines"}, "unitId": {department}, "scope": {"EXTERNAL"},
				"page": {tc.page}, "version": {tc.version}, "orderQuery": {tc.order}, "beneficiaryQuery": {tc.beneficiary}, "afterLineId": {tc.cursor}}
			response := httptest.NewRecorder()
			service.getDraft(response, httptest.NewRequest(http.MethodGet, "/api/v1/drafts?"+params.Encode(), nil))
			if response.Code != tc.status {
				t.Fatalf("HTTP %d want %d: %s", response.Code, tc.status, response.Body)
			}
			if tc.status != http.StatusOK {
				return
			}
			var result struct {
				LineTotal int
				Lines     []struct{ OrderNo, SourceVersion string }
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.LineTotal != tc.total || len(result.Lines) != tc.rows {
				t.Fatalf("line count and total: got %d rows / %d total, want %d / %d (%v)", len(result.Lines), result.LineTotal, tc.rows, tc.total, err)
			}
			if tc.rows > 0 && (result.Lines[0].OrderNo != tc.first || result.Lines[0].SourceVersion != "v1") {
				t.Fatalf("wrong first row: %+v", result.Lines[0])
			}
		})
	}
}
