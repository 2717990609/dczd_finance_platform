package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestDraftReportSeparatesOrderPerformanceAndRecipientOrganization(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t), development: true}
	mustID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	day := 25
	product := mustID(service.createProduct(ctx, createProductInput{Code: "REPORT", Name: "汇总测试", PeriodMode: "calendar", CycleStartDay: &day}))
	period := func(month string) int64 {
		return mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: month, SettlementMonth: month}))
	}
	june, july := period("2025-06"), period("2025-07")
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "VIP", SourceKey: "test"}))
	seller := mustID(service.createPerson(ctx, createPersonInput{Name: "同名人员"}))
	referrer := mustID(service.createPerson(ctx, createPersonInput{Name: "同名人员"}))
	for i, person := range []int64{seller, referrer} {
		mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCAI", ExternalID: fmt.Sprintf("p%d", i), ValidFrom: "2025-01-01T00:00:00+08:00"}))
	}
	if _, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "测试规则", Components: []component{
		{Type: "sales", SourceRole: "PID_USER", Mode: "rate", Value: 1000, RefundPolicy: "non_refundable"},
		{Type: "referral", SourceRole: "DIRECT_REFERRER", Mode: "rate", Value: 500, RefundPolicy: "non_refundable"},
	}}); err != nil {
		t.Fatal(err)
	}
	const org = `
	 INSERT INTO organization_units(id,kind,name,status,parent_id) VALUES
	 ('00000000-0000-0000-0000-000000000001','COMPANY','甲公司','ACTIVE',NULL),
	 ('00000000-0000-0000-0000-000000000002','DEPARTMENT','甲一部','ACTIVE','00000000-0000-0000-0000-000000000001'),
	 ('00000000-0000-0000-0000-000000000003','COMPANY','乙公司','ACTIVE',NULL),
	 ('00000000-0000-0000-0000-000000000004','DEPARTMENT','乙一部','ACTIVE','00000000-0000-0000-0000-000000000003');
	 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,valid_to) VALUES
	 ('00000000-0000-0000-0000-000000000005',%d,'00000000-0000-0000-0000-000000000002','PRIMARY','2025-01-01','2025-06-15'),
	 ('00000000-0000-0000-0000-000000000006',%d,'00000000-0000-0000-0000-000000000004','PRIMARY','2025-06-15',NULL),
	 ('00000000-0000-0000-0000-000000000007',%d,'00000000-0000-0000-0000-000000000004', 'PRIMARY','2025-01-01',NULL);
	 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from,valid_to) VALUES
	 ('DCAI','company-7','00000000-0000-0000-0000-000000000001','2025-01-01','2025-06-15'),
	 ('DCAI','company-7','00000000-0000-0000-0000-000000000003','2025-06-15',NULL); `
	if _, err := service.db.Exec(ctx, fmt.Sprintf(org, seller, seller, referrer)); err != nil {
		t.Fatal(err)
	}
	payTime := time.Date(2025, 6, 5, 12, 0, 0, 0, shanghai)
	refundTime := payTime.AddDate(0, 1, 0)
	save := func(fact sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	gross, debit := int64(10000), int64(2500)
	save(sourceFact{Source: "DCAI", Kind: "ORDER_PAID", Domain: "VIP", SourceKey: "test", OrderID: "order", RecordKey: "order:1",
		PaymentID: "pay-1", Version: "one", AmountMinor: &gross, OccurredAt: payTime, UpdatedAt: payTime,
		ReferrerID: "p1", SalesCompanyID: "company-7", Participants: []sourceParticipant{{Role: "PID_USER", UserID: "p0"}}})
	draftAt := func(periodID int64) draftSummary {
		t.Helper()
		result, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: periodID})
		if err != nil {
			t.Fatal(err)
		}
		return result.(draftSummary)
	}
	type reportRow struct {
		Name                         string
		PerformanceMinor, TotalMinor int64
	}
	get := func(draft draftSummary, dimension string) []reportRow {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/draft-report?periodId=%d&version=%d&dimension=%s", draft.PeriodID, draft.Version, dimension), nil)
		w := httptest.NewRecorder()
		for _, route := range service.routes() {
			if route.Method == req.Method && route.Path == req.URL.Path {
				route.Handler(w, req)
				var report struct{ Items []reportRow }
				if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil || w.Code != 200 {
					t.Fatalf("draft report HTTP %d: %s", w.Code, w.Body)
				}
				return report.Items
			}
		}
		t.Fatal("missing draft report endpoint")
		return nil
	}
	initial := draftAt(june)
	if previous := draftAt(june); previous.Version != initial.Version {
		t.Fatal("unchanged report changed the draft version")
	}
	for _, dimension := range []string{"company", "department", "person"} {
		rows := get(initial, dimension)
		var performance, commission int64
		for _, row := range rows {
			performance += row.PerformanceMinor
			commission += row.TotalMinor
		}
		if len(rows) != 2 || performance != gross || commission != 1500 {
			t.Fatalf("%s did not reconcile: %+v", dimension, rows)
		}
	}
	if rows := get(initial, "department"); rows[0] != (reportRow{"甲一部", gross, 1000}) ||
		rows[1] != (reportRow{"乙一部", 0, 500}) {
		t.Fatalf("department split = %+v", rows)
	}
	if _, err := service.db.Exec(ctx, "UPDATE products SET settlement_enabled=true WHERE id=$1", product); err != nil {
		t.Fatal(err)
	}
	if _, err := service.confirmSettlement(ctx, confirmSettlementInput{PeriodID: june, ExpectedVersion: initial.Version}, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(ctx, "UPDATE organization_units SET parent_id='00000000-0000-0000-0000-000000000003' WHERE name='甲一部'"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.db.Exec(ctx, "UPDATE source_order_versions SET fact=jsonb_set(fact,'{amountMinor}','1') WHERE order_id='order'"); err == nil {
		t.Fatal("confirmed report order amount was writable")
	}
	if rows := get(initial, "company"); rows[0].Name != "甲公司" || rows[0].PerformanceMinor != gross {
		t.Fatalf("confirmed report moved after organization change: %+v", rows)
	}
	refundFact := sourceFact{Source: "DCAI", Kind: "REFUND_SUCCEEDED", Domain: "VIP", SourceKey: "test", OrderID: "order", RecordKey: "refund:1",
		RefundID: "refund-1", Version: "one", AmountMinor: &debit, OccurredAt: refundTime, UpdatedAt: refundTime,
		RefundsPayment: []sourcePaymentReference{{OrderID: "order", RecordID: "1"}}}
	save(refundFact)
	refund := draftAt(july)
	if rows := get(refund, "company"); len(rows) != 1 || rows[0] != (reportRow{"甲公司", -debit, 0}) {
		t.Fatalf("refund should follow original order company without commission debit: %+v", rows)
	}
	refundFact.Version, refundFact.OrderNo = "two", "展示更新"
	refundFact.UpdatedAt = refundTime.Add(time.Minute)
	save(refundFact)
	if repeated := draftAt(july); repeated.Version != refund.Version {
		t.Fatalf("display-only refund change created a new draft: %d -> %d", refund.Version, repeated.Version)
	}
}
