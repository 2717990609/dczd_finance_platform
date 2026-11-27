package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestFinanceReportsFilterUsingOrganizationTree(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	const company, dept, team, sibling = "51000000-0000-4000-8000-000000000001", "51000000-0000-4000-8000-000000000002", "51000000-0000-4000-8000-000000000003", "51000000-0000-4000-8000-000000000004"
	_, err := s.db.Exec(ctx, `
	 INSERT INTO organization_units(id,parent_id,kind,name,status) VALUES
	 ('51000000-0000-4000-8000-000000000001',NULL,'COMPANY','公司','ACTIVE'),
	 ('51000000-0000-4000-8000-000000000002','51000000-0000-4000-8000-000000000001','DEPARTMENT','部门','ACTIVE'),
	 ('51000000-0000-4000-8000-000000000003','51000000-0000-4000-8000-000000000002','TEAM','一组','ACTIVE'),
	 ('51000000-0000-4000-8000-000000000004','51000000-0000-4000-8000-000000000002','TEAM','二组','INACTIVE');
	 INSERT INTO people(name) VALUES('第一组销售'),('第二组销售');
	 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from) VALUES
	 ('51000000-0000-4000-8000-000000000005',1,'51000000-0000-4000-8000-000000000003','PRIMARY','2025-01-01'),
	 ('51000000-0000-4000-8000-000000000006',2,'51000000-0000-4000-8000-000000000004','PRIMARY','2025-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	mustID := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	day := 1
	product := mustID(s.createProduct(ctx, createProductInput{Code: "TREE_FIXTURE", Name: "组织筛选样本", PeriodMode: "calendar", CycleStartDay: &day}))
	period := mustID(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月", SettlementMonth: "2026-08"}))
	mustID(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCJK", Domain: "TEST", SourceKey: "test"}))
	for i, key := range []string{"one", "two"} {
		mustID(s.assignIdentity(ctx, assignIdentityInput{PersonID: int64(i + 1), Source: "DCJK", ExternalID: key, ValidFrom: "2025-01-01T00:00:00Z"}))
	}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "试算分润", Components: []component{
		{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "SELLER", RefundPolicy: "proportional"},
	}}); err != nil {
		t.Fatal(err)
	}
	fixture := scopeOrderFixtures()
	if err := storeScopeOrderFixture(ctx, s, fixture); err != nil {
		t.Fatal(err)
	}
	orderURL := "/api/v1/orders?unitId=" + team
	pending := httptest.NewRecorder()
	s.orders(pending, httptest.NewRequest(http.MethodGet, orderURL, nil))
	var pendingRows []struct{ OrderID string }
	if err := json.Unmarshal(pending.Body.Bytes(), &pendingRows); err != nil || pending.Code != 200 || len(pendingRows) != 1 || pendingRows[0].OrderID != "one" {
		t.Fatalf("uncalculated team order: %d %v %s", pending.Code, err, pending.Body)
	}
	result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	draft := result.(draftSummary)
	if draft.Total != 300 {
		t.Fatalf("unexpected fixture: %+v", draft)
	}
	if _, err := s.db.Exec(ctx, "INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')", period, draft.Version); err != nil {
		t.Fatal(err)
	}
	call := func(handler http.HandlerFunc, path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	draftURL := fmt.Sprintf("/api/v1/draft-report?periodId=%d&version=%d&dimension=person", period, draft.Version)
	monthURL := "/api/v1/monthly-report?month=2026-08&view=settled&dimension=person"
	for _, test := range []struct {
		unit           string
		net, principal int64
		count          int
	}{
		{"", 300, 3000, 2}, {company, 300, 3000, 2}, {dept, 300, 3000, 2}, {team, 100, 1000, 1}, {sibling, 200, 2000, 1},
	} {
		for _, endpoint := range []struct {
			handler http.HandlerFunc
			path    string
		}{{s.draftReport, draftURL}, {s.monthlyReport, monthURL}} {
			w := call(endpoint.handler, endpoint.path+"&unitId="+test.unit)
			var view struct {
				Total            int
				PerformanceMinor int64
				Items            []struct{ TotalMinor int64 }
				Periods          []struct{ CommissionMinor int64 }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 {
				t.Fatalf("%s %s: %d %v %s", endpoint.path, test.unit, w.Code, err, w.Body)
			}
			var net int64
			for _, row := range view.Items {
				net += row.TotalMinor
			}
			if net != test.net || view.Total != test.count || view.PerformanceMinor != test.principal ||
				len(view.Periods) != 0 && view.Periods[0].CommissionMinor != test.net {
				t.Fatalf("%s %s: net %d view %+v", endpoint.path, test.unit, net, view)
			}
		}
	}
	details := call(s.getDraft, fmt.Sprintf("/api/v1/drafts?periodId=%d&unitId=%s", period, team))
	var detail struct {
		LineCount, ScopeLineCount, TotalMinor int
		Lines                                 []struct{ OrderID string }
	}
	if err := json.Unmarshal(details.Body.Bytes(), &detail); err != nil || details.Code != 200 ||
		detail.LineCount != 2 || detail.TotalMinor != 300 || detail.ScopeLineCount != 1 || len(detail.Lines) != 1 || detail.Lines[0].OrderID != "one" {
		t.Fatalf("scoped settlement detail and overall total: %d %v %s", details.Code, err, details.Body)
	}
	t.Run("both reports use the same organization type", func(t *testing.T) {
		if _, err := s.db.Exec(ctx, "UPDATE organization_units SET sales_company_type='EXTERNAL' WHERE id=$1", company); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := s.db.Exec(ctx, "UPDATE organization_units SET sales_company_type=NULL WHERE id=$1", company); err != nil {
				t.Error(err)
			}
		}()
		for _, endpoint := range []struct {
			handler http.HandlerFunc
			path    string
		}{{s.draftReport, draftURL}, {s.monthlyReport, monthURL}} {
			for _, test := range []struct {
				query  string
				rows   int
				amount int64
			}{{"&scope=INTERNAL", 0, 0}, {"&scope=EXTERNAL", 2, 3000}, {"&scope=EXTERNAL&unitId=" + team, 1, 1000}} {
				w := call(endpoint.handler, endpoint.path+test.query)
				var got struct {
					Total            int
					PerformanceMinor int64
				}
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || got.Total != test.rows || got.PerformanceMinor != test.amount {
					t.Fatalf("%s%s: %d %v %s", endpoint.path, test.query, w.Code, err, w.Body)
				}
			}
		}
	})
	// A newer source version can change its seller while the booked attribution
	// still governs the organization view. Put it beyond the recent page scan.
	revised := fixture[0]
	revised.Version, revised.UpdatedAt = "2", revised.UpdatedAt.Add(time.Hour)
	revised.Participants = []sourceParticipant{{Role: "SELLER", UserID: "two"}}
	if err := storeScopeOrderFixture(ctx, s, []sourceFact{revised}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,kind,version,updated_at,occurred_at,fact)
	 SELECT 'DCJK','unmapped:'||n,'','pending:'||n,'ORDER_PAID','1',now(),now(),'{"domain":"UNMAPPED","sourceKey":"unmapped"}'::jsonb
	 FROM generate_series(1,800) n`); err != nil {
		t.Fatal(err)
	}
	orders := call(s.orders, "/api/v1/orders?unitId="+team)
	var rows []struct{ OrderID, Version string }
	if err := json.Unmarshal(orders.Body.Bytes(), &rows); err != nil || orders.Code != 200 || len(rows) != 1 || rows[0].OrderID != "one" || rows[0].Version != "2" {
		t.Fatalf("team orders: %d %v %s", orders.Code, err, orders.Body)
	}
	t.Run("export respects filters, cursor and frozen organization", func(t *testing.T) {
		for _, test := range []struct {
			query string
			want  int
		}{
			{"&orderId=one", 2}, {"&orderId=two", 1}, {"&orderId=one&beforeId=1", 1}, {"&source=DCAI", 1},
		} {
			w := call(s.orders, orderURL+"&format=csv"+test.query)
			records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\uFEFF"))).ReadAll()
			if w.Code != http.StatusOK || err != nil || len(records) != test.want {
				t.Fatalf("CSV scope %s: HTTP %d, %d rows, %v", test.query, w.Code, len(records), err)
			}
			if len(records) > 1 && (records[1][1] != "one" || records[1][2] != "DCJK") {
				t.Fatal("expected the one source order belonging to the selected team")
			}
		}
	})
	if w := call(s.monthlyReport, monthURL+"&unitId=bogus"); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope must fail: %d", w.Code)
	}
	const otherCompany, otherDept = "51000000-0000-4000-8000-000000000009", "51000000-0000-4000-8000-000000000010"
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,kind,name,status) VALUES
	 ('51000000-0000-4000-8000-000000000009',NULL,'COMPANY','另一公司','ACTIVE'),
	 ('51000000-0000-4000-8000-000000000010','51000000-0000-4000-8000-000000000009','DEPARTMENT','另一部门','ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE organization_units SET parent_id=$2 WHERE id=$1", team, otherDept); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{otherCompany, otherDept} {
		w := call(s.monthlyReport, monthURL+"&unitId="+scope)
		var view struct{ Total int }
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 || view.Total != 0 {
			t.Fatalf("moving a team cannot move confirmed amounts to a different company or department: %d %v %s", w.Code, err, w.Body)
		}
		w = call(s.orders, "/api/v1/orders?unitId="+scope)
		if w.Code != 200 || w.Body.String() != "[]" {
			t.Fatalf("moving a team cannot move the historical order: %d %s", w.Code, w.Body)
		}
	}
}

func scopeOrderFixtures() []sourceFact {
	at := time.Date(2026, 8, 5, 8, 0, 0, 0, shanghai)
	one, two := int64(1000), int64(2000)
	return []sourceFact{
		{Source: "DCJK", Domain: "TEST", SourceKey: "test", Kind: "ORDER_PAID", OrderID: "one", RecordKey: "payment:one",
			PaymentID: "one", Version: "1", AmountMinor: &one, OccurredAt: at, UpdatedAt: at,
			Participants: []sourceParticipant{{Role: "SELLER", UserID: "one"}, {Role: "DIRECT_REFERRER", UserID: "two"}}},
		{Source: "DCJK", Domain: "TEST", SourceKey: "test", Kind: "ORDER_PAID", OrderID: "two", RecordKey: "payment:two",
			PaymentID: "two", Version: "1", AmountMinor: &two, OccurredAt: at, UpdatedAt: at, Participants: []sourceParticipant{{Role: "SELLER", UserID: "two"}}},
	}
}

func storeScopeOrderFixture(ctx context.Context, s workspace, facts []sourceFact) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, facts)
		return err
	})
}

func TestFinanceReportsGroupByActualOrganizationTree(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	const company = "51000000-0000-4000-8000-000000000001"
	const department = "51000000-0000-4000-8000-000000000002"
	const directTeam = "51000000-0000-4000-8000-000000000003"
	const seed = `
 INSERT INTO products(code,name,period_mode,cycle_start_day) VALUES('TREE_REPORT','组织汇总样本','calendar',1);
 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month)
 VALUES(1,'八月账期',202608,'2026-08-01','2026-08-31','2026-08-01');
 INSERT INTO organization_units(id,parent_id,kind,name,status) VALUES
 ('51000000-0000-4000-8000-000000000001',NULL,'COMPANY','公司','ACTIVE'),
 ('51000000-0000-4000-8000-000000000002','51000000-0000-4000-8000-000000000001','DEPARTMENT','部门','ACTIVE'),
 ('51000000-0000-4000-8000-000000000003','51000000-0000-4000-8000-000000000001','TEAM','直属组','ACTIVE'),
 ('51000000-0000-4000-8000-000000000004','51000000-0000-4000-8000-000000000002','TEAM','部门组','ACTIVE');
 INSERT INTO people(name) VALUES('销售');
 INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(1,'DCAI','seller','2026-01-01');
 INSERT INTO rule_versions(product_id,version,label,components) VALUES(1,1,'销售规则','[]');
 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
 SELECT 'DCAI',i::text,'','paid:'||i,'1','ORDER_PAID','2026-08-04','2026-08-04',
   jsonb_build_object('domain','VIP','paymentId',i::text,'amountMinor',i*1000) FROM generate_series(1,4) AS i;
 INSERT INTO calculation_drafts(period_id,rule_id,version,input_digest,total_minor,line_count,issue_count,calculated_at,performance_ready)
 VALUES(1,1,1,'\x01',1000,4,0,now(),true);
 INSERT INTO calculation_draft_lines(period_id,source_version_id,rule_id,component_type,identity_id,person_id,base_minor,amount_minor,company_id,group_id)
 VALUES (1,1,1,'sales',1,1,1000,100,'51000000-0000-4000-8000-000000000001','51000000-0000-4000-8000-000000000003'),
 (1,2,1,'sales',1,1,2000,200,NULL,'51000000-0000-4000-8000-000000000004'),
 (1,3,1,'sales',1,1,3000,300,'51000000-0000-4000-8000-000000000001',NULL),
 (1,4,1,'sales',1,1,4000,400,NULL,NULL);
 INSERT INTO calculation_draft_performance(period_id,source_version_id,person_id,company_id,group_id)
 SELECT period_id,source_version_id,person_id,company_id,group_id FROM calculation_draft_lines;
 `
	if _, err := s.db.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}

	type amount struct{ PerformanceMinor, TotalMinor int64 }
	check := func(dimension, view, unit string, expected map[string]amount) {
		t.Helper()
		endpoints := []struct {
			handler http.HandlerFunc
			path    string
		}{
			{s.draftReport, "/api/v1/draft-report?periodId=1&version=1"},
			{s.monthlyReport, "/api/v1/monthly-report?month=2026-08&view=" + view},
		}
		for _, endpoint := range endpoints {
			w := httptest.NewRecorder()
			endpoint.handler(w, httptest.NewRequest(http.MethodGet, endpoint.path+"&dimension="+dimension+"&unitId="+unit, nil))
			var report struct {
				Items []struct {
					Name string
					amount
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != http.StatusOK {
				t.Fatalf("%s: %d %v %s", endpoint.path, w.Code, err, w.Body)
			}
			for _, item := range report.Items {
				if got, ok := expected[item.Name]; !ok || got != item.amount {
					t.Errorf("%s %s: unexpected row %+v", endpoint.path, dimension, item)
				}
			}
			if len(report.Items) != len(expected) {
				t.Errorf("%s %s: expected %d rows, got %+v", endpoint.path, dimension, len(expected), report.Items)
			}
		}
	}
	check("department", "current", "", map[string]amount{"公司 · 直属": {4000, 400}, "部门": {2000, 200}, "归属待核": {4000, 400}})
	check("department", "current", department, map[string]amount{"部门": {2000, 200}})
	check("company", "current", company, map[string]amount{"公司": {6000, 600}})

	// Freeze the resolved department before moving the other team. Historical
	// company-direct rows retain their saved path when the current tree changes.
	for _, statement := range []string{
		"UPDATE calculation_draft_lines SET company_id=$1,department_id=$2 WHERE source_version_id=2",
		"UPDATE calculation_draft_performance SET company_id=$1,department_id=$2 WHERE source_version_id=2",
	} {
		if _, err := s.db.Exec(ctx, statement, company, department); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(ctx, "INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(1,1,'fixture')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE organization_units SET parent_id=$1 WHERE id=$2", department, directTeam); err != nil {
		t.Fatal(err)
	}
	check("department", "settled", "", map[string]amount{"公司 · 直属": {4000, 400}, "部门": {2000, 200}, "归属待核": {4000, 400}})
	check("department", "settled", department, map[string]amount{"部门": {2000, 200}})
}
