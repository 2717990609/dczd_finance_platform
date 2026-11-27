package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDepartmentRetentionReadsConfirmedAllocationsAcrossProducts(t *testing.T) {
	s := workspace{db: integrationDB(t), development: true}
	const company = "52000000-0000-4000-8000-000000000001"
	const department = "52000000-0000-4000-8000-000000000002"
	const otherCompany = "52000000-0000-4000-8000-000000000003"
	ctx := context.Background()
	if _, err := s.db.Exec(ctx, `
INSERT INTO products(code,name,period_mode,cycle_start_day)
 VALUES('RET_AI','大春 AI','calendar',25),('RET_CLASS','实战班','class',25);
INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month,report_month) VALUES
 (1,'七月账期',202607,'2026-06-25','2026-07-24','2026-07-01',NULL),
 (2,'八月班',56,'2026-07-31','2026-07-31',NULL,'2026-08-01'),
 (1,'八月未结算',202608,'2026-07-25','2026-08-24','2026-08-01',NULL),
 (1,'九月账期',202609,'2026-08-25','2026-09-24','2026-09-01',NULL);
INSERT INTO organization_units(id,kind,name,status,parent_id) VALUES
 ('`+company+`','COMPANY','甲公司','ACTIVE',NULL),
 ('`+department+`','DEPARTMENT','共有业务部','ACTIVE','`+company+`'),
 ('`+otherCompany+`','COMPANY','乙公司','ACTIVE',NULL);
UPDATE organization_units SET sales_company_type='EXTERNAL' WHERE id='`+company+`';
INSERT INTO people(name) VALUES('分配成员');
INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
 VALUES (1,1,'\x01',20000,0,0,now()),(2,1,'\x02',20000,0,0,now()),
 (3,1,'\x03',20000,0,0,now()),(4,1,'\x04',20000,0,0,now());
INSERT INTO monthly_pool_allocations(period_id,unit_id,item_key,scenario_key,draft_version,basis_token,revision) VALUES
 (1,'`+department+`','pool1','ai',1,'fixture',1),
 (2,'`+department+`','pool2','class',1,'fixture',1),
 (3,'`+department+`','pool1','ai',1,'fixture',1),
 (4,'`+department+`','pool1','ai',1,'fixture',1);
INSERT INTO monthly_pool_allocation_shares(allocation_id,person_id,amount_minor) VALUES
 (1,NULL,5000),(2,NULL,12000),(2,1,8000),(3,NULL,15000),(4,NULL,600);
INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(1,1,'fixture'),(2,1,'fixture'),(4,1,'fixture');`); err != nil {
		t.Fatal(err)
	}
	api := http.NewServeMux()
	for _, route := range s.routes() {
		api.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	type account struct {
		UnitID, Name                                           string
		OpeningMinor, IncomeMinor, BalanceMinor                int64
		CurrentBalanceMinor, TotalIncomeMinor, TotalSpentMinor int64
	}
	type entry struct {
		PeriodID, AmountMinor                         int64
		ProductName, Month, UnitID, ItemKey, Scenario string
	}
	read := func(query string) struct {
		Items   []account
		Entries []entry
	} {
		t.Helper()
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/department-retention?"+query, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("department retention returned %d: %s", w.Code, w.Body.String())
		}
		var report struct {
			Items   []account
			Entries []entry
		}
		if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}
	report := read("month=2026-08&unitId=" + company + "&accountId=" + department)
	if len(report.Items) != 1 || report.Items[0] != (account{department, "共有业务部", 5000, 12000, 17000, 17600, 17600, 0}) {
		t.Fatalf("expected one cross-product account for the selected company: %+v", report.Items)
	}
	if len(report.Entries) != 2 || report.Entries[0] != (entry{2, 12000, "实战班", "2026-08", department, "pool2", "class"}) ||
		report.Entries[1] != (entry{1, 5000, "大春 AI", "2026-07", department, "pool1", "ai"}) {
		t.Fatalf("expected the actual confirmed retained entries and business months: %+v", report.Entries)
	}
	if other := read("month=2026-08&unitId=" + otherCompany + "&accountId=" + department); len(other.Items) != 0 || len(other.Entries) != 0 {
		t.Fatalf("a different organization scope must exclude the account: %+v", other)
	}
	if internal := read("month=2026-08&scope=INTERNAL&accountId=" + department); len(internal.Items) != 0 || len(internal.Entries) != 0 {
		t.Fatalf("internal scope must exclude external balances and entries: %+v", internal)
	}
	if external := read("month=2026-08&scope=EXTERNAL&unitId=" + company + "&accountId=" + department); len(external.Items) != 1 || len(external.Entries) != 2 {
		t.Fatalf("the external scope and its parent should include the department: %+v", external)
	}
	if july := read("month=2026-07"); len(july.Items) != 1 || july.Items[0].BalanceMinor != 5000 || july.Items[0].OpeningMinor != 0 {
		t.Fatalf("the report must exclude postings in future business months: %+v", july)
	}

	post := func(input retentionEntryInput, status int) int64 {
		t.Helper()
		input.UnitID = department
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/department-retention", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("record %s returned %d: %s", input.Kind, w.Code, w.Body.String())
		}
		var result struct{ ID int64 }
		if status == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ID < 1 {
				t.Fatalf("saved retention entry: %s %v", w.Body.String(), err)
			}
		}
		return result.ID
	}
	august := "month=2026-08&accountId=" + department
	opening := retentionEntryInput{Kind: "opening", Month: "2026-07", AmountMinor: 20000,
		RequestID: "52100000-0000-4000-8000-000000000001"}
	openingID := post(opening, http.StatusOK)
	if repeated := post(opening, http.StatusOK); repeated != openingID {
		t.Fatal("retry must reuse the same opening entry")
	}
	opening.RequestID = "52100000-0000-4000-8000-000000000009"
	post(opening, http.StatusConflict)
	if got := read(august); got.Items[0].BalanceMinor != 32000 || got.Items[0].OpeningMinor != 20000 ||
		len(got.Entries) != 2 {
		t.Fatalf("the imported opening must replace covered retention credits: %+v", got)
	}
	expense := retentionEntryInput{Kind: "expense", Month: "2026-08", AmountMinor: 35000, ReimbursementNo: "WX-0001",
		RequestID: "52100000-0000-4000-8000-000000000002"}
	expenseID := post(expense, http.StatusOK)
	expense.RequestID = "52100000-0000-4000-8000-000000000008"
	post(expense, http.StatusConflict)
	if got := read(august); got.Items[0].BalanceMinor != -3000 || got.Items[0].CurrentBalanceMinor != -2400 || got.Items[0].TotalSpentMinor != 35000 {
		t.Fatalf("recorded expense can result in a negative balance: %+v", got.Items)
	}
	if got := read(august + "&productId=2"); len(got.Items) != 1 || got.Items[0].CurrentBalanceMinor != -2400 ||
		got.Items[0].TotalSpentMinor != 35000 || len(got.Entries) != 1 || got.Entries[0].PeriodID != 2 {
		t.Fatalf("product selection should filter entries while preserving department amounts: %+v", got)
	}
	if got := read(august + "&productId=1"); len(got.Items) != 0 || len(got.Entries) != 0 {
		t.Fatalf("a credit covered by the opening or outside this statement must not match: %+v", got)
	}
	post(retentionEntryInput{Kind: "correction", Month: "2026-08", AmountMinor: 300, Note: "月度更正",
		RequestID: "52100000-0000-4000-8000-000000000003"}, http.StatusOK)
	reversal := retentionEntryInput{Kind: "reversal", Month: "2026-08", OriginalID: expenseID,
		RequestID: "52100000-0000-4000-8000-000000000004"}
	reversalID := post(reversal, http.StatusOK)
	if repeated := post(reversal, http.StatusOK); repeated != reversalID {
		t.Fatal("retry must reuse the same reversal")
	}
	reversal.RequestID = "52100000-0000-4000-8000-000000000007"
	post(reversal, http.StatusConflict)
	if got := read(august); got.Items[0].BalanceMinor != 32300 || got.Items[0].TotalSpentMinor != 0 || len(got.Entries) != 5 {
		t.Fatalf("the original expense and its reversal must both remain visible: %+v", got)
	}
	if _, err := s.db.Exec(ctx, "UPDATE department_retention_entries SET amount_minor=0 WHERE id=$1", expenseID); err == nil {
		t.Fatal("the ledger must protect recorded entries")
	}
	operator := financeAccountConfig{Login: "retention.fixture", PersonID: 1, Workspace: true, Enabled: true, Settlements: []int64{1}}
	const password = "isolated-retention-fixture"
	if err := s.saveFinanceAccount(ctx, operator, password); err != nil {
		t.Fatal(err)
	}
	var operatorID string
	if err := s.db.QueryRow(ctx, "SELECT id::text FROM finance_accounts WHERE login=$1", operator.Login).Scan(&operatorID); err != nil {
		t.Fatal(err)
	}
	operatorCtx := context.WithValue(ctx, financePrincipalKey{}, &financePrincipal{ID: operatorID, Workspace: true})
	if allowed, err := s.canWriteRetention(operatorCtx, s.db); err != nil || allowed {
		t.Fatalf("an account scoped to one product cannot update cross-product retention: %t %v", allowed, err)
	}
	operator.Settlements = []int64{1, 2}
	if err := s.saveFinanceAccount(ctx, operator, password); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.canWriteRetention(operatorCtx, s.db); err != nil || !allowed {
		t.Fatalf("the finance operator with both settlement grants must be able to record entries: %t %v", allowed, err)
	}
}
