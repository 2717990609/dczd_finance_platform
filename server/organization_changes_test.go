package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestOrganizationAppointmentMaintenance(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	_, err := s.db.Exec(ctx, `
	 INSERT INTO people(name) VALUES('林主管'),('只读人员'),('维护人员');
	 INSERT INTO organization_units(id,kind,name,status) VALUES
	 ('10000000-0000-0000-0000-000000000001','DEPARTMENT','销售一部','ACTIVE'),
	 ('10000000-0000-0000-0000-000000000002','DEPARTMENT','销售二部','ACTIVE'),
	 ('10000000-0000-0000-0000-000000000003','DEPARTMENT','停用部门','INACTIVE');
	 INSERT INTO configuration_options(group_key,option_key,label,display_order) VALUES('appointmentKind','COACH','带教',90);
	 INSERT INTO products(code,name,period_mode,cycle_start_day) VALUES('AI','AI','calendar',25),('PRACTICE','实战班','class',25);
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month) VALUES
	 (1,'July',202607,'2026-06-25','2026-07-24','2026-07-01'),
	 (1,'August',202608,'2026-07-25','2026-08-24','2026-08-01'),
	 (1,'September',202609,'2026-08-25','2026-09-24','2026-09-01'),
	 (2,'Practice',10,'2026-09-10','2026-09-11',NULL);
	 INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	 VALUES(2,1,'\x01',0,0,0,now());
	 INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(2,1,'fixture');`)
	if err != nil {
		t.Fatal(err)
	}
	const password = "appointment-fixture-only"
	for i, login := range []string{"reader", "operator"} {
		if err := s.saveFinanceAccount(ctx, financeAccountConfig{Login: login, PersonID: int64(i + 2), Enabled: true, Workspace: true, OrganizationWrite: login == "operator"}, password); err != nil {
			t.Fatal(err)
		}
	}
	router := http.NewServeMux()
	for _, route := range s.routes() {
		router.HandleFunc(route.Method+" "+route.Path, route.Handler)
	}
	call := func(method, path, body string, cookie *http.Cookie, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, status, w.Body)
		}
		return w
	}
	const path = "/api/v1/organization-assignments"
	const create = `{"personId":1,"unitId":"10000000-0000-0000-0000-000000000001","kind":"COACH","position":"带教主管","startsAt":"2026-08-15T00:00:00+08:00"}`
	const loginPath = "/api/v1/session"
	login := func(name string) *http.Cookie {
		return call("POST", loginPath, `{"login":"`+name+`","password":"`+password+`"}`, nil, 200).Result().Cookies()[0]
	}
	readonly := login("reader")
	call("POST", path, create, nil, 401)
	call("POST", path, create, readonly, 403)
	call("GET", path+"?history=true", "", readonly, 200)
	call("GET", "/api/v1/organization-people?name=林", "", readonly, 403)
	operator := login("operator")
	if got := call("GET", "/api/v1/organization-people?name=林", "", operator, 200).Body.String(); !strings.Contains(got, "林主管") || strings.Contains(got, "只读人员") {
		t.Fatal(got)
	}
	getID := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var result map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["id"] == "" {
			t.Fatalf("missing assignment ID: %s", w.Body)
		}
		return result["id"]
	}
	id := getID(call("POST", path, create, operator, 200))
	if got := getID(call("POST", path, create, operator, 200)); got != id {
		t.Fatal("retry created another assignment")
	}
	call("POST", path, strings.Replace(create, "08-15", "08-16", 1), operator, 409)
	call("POST", path, strings.Replace(create, "000000000001", "000000000003", 1), operator, 409)
	call("POST", path, strings.Replace(create, "COACH", "UNKNOWN", 1), operator, 400)
	transfer := `{"assignmentId":"` + id + `","at":"2026-09-01T00:00:00+08:00","unitId":"10000000-0000-0000-0000-000000000002","kind":"COACH","position":"负责人"}`
	call("POST", path+"/transfer", strings.Replace(transfer, "000000000002", "000000000003", 1), operator, 409)
	if got := call("GET", path+"?on=2026-09-01", "", readonly, 200).Body.String(); !strings.Contains(got, "销售一部") {
		t.Fatalf("failed transfer closed the original appointment: %s", got)
	}
	toID := getID(call("POST", path+"/transfer", transfer, operator, 200))
	if got := getID(call("POST", path+"/transfer", transfer, operator, 200)); got != toID {
		t.Fatal("retry changed the transferred assignment")
	}
	call("POST", path+"/transfer", strings.Replace(transfer, "09-01", "08-31", 1), operator, 409)
	for _, day := range []struct{ date, want, other string }{{"2026-08-31", "销售一部", "销售二部"}, {"2026-09-01", "销售二部", "销售一部"}} {
		got := call("GET", path+"?on="+day.date, "", readonly, 200).Body.String()
		if !strings.Contains(got, day.want) || strings.Contains(got, day.other) {
			t.Fatal(got)
		}
	}
	end := `{"assignmentId":"` + toID + `","at":"2026-09-15T00:00:00+08:00"}`
	call("POST", path+"/end", end, operator, 200)
	call("POST", path+"/end", end, operator, 200)
	if got := call("GET", path+"?on=2026-09-15", "", readonly, 200).Body.String(); !strings.Contains(got, `"total":0`) {
		t.Fatal(got)
	}
	var pending []int64
	if err := s.db.QueryRow(ctx, `SELECT array_agg(period_id ORDER BY period_id) FROM draft_refresh_queue`).Scan(&pending); err != nil ||
		len(pending) != 2 || pending[0] != 3 || pending[1] != 4 {
		t.Fatalf("affected scopes = %v, %v", pending, err)
	}
	var attributed int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM organization_assignments a JOIN finance_accounts p ON p.id=a.changed_by
	 WHERE p.login='operator' AND a.changed_at IS NOT NULL`).Scan(&attributed); err != nil || attributed != 2 {
		t.Fatalf("operator references = %d, %v", attributed, err)
	}
}

func TestAppointmentTransferRefreshesOpenDraftAndPreservesSettlement(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	const unitA = "10000000-0000-0000-0000-000000000002"
	const unitB = "10000000-0000-0000-0000-000000000003"
	if _, err := s.db.Exec(ctx, `
	 INSERT INTO people(name) VALUES('张主管');
	 INSERT INTO organization_units(id,kind,name,status,parent_id) VALUES
	 ('10000000-0000-0000-0000-000000000001','COMPANY','甲公司','ACTIVE',NULL),
	 ('10000000-0000-0000-0000-000000000002','DEPARTMENT','甲一部','ACTIVE','10000000-0000-0000-0000-000000000001'),
	 ('10000000-0000-0000-0000-000000000003','DEPARTMENT','甲二部','ACTIVE','10000000-0000-0000-0000-000000000001');
	 INSERT INTO products(code,name,period_mode,cycle_start_day,settlement_enabled) VALUES('REPORT','报表','calendar',1,true);
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month) VALUES
	 (1,'August',202608,'2026-08-01','2026-08-31','2026-08-01'),
	 (1,'September',202609,'2026-09-01','2026-09-30','2026-09-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.assignIdentity(ctx, assignIdentityInput{PersonID: 1, Source: "DCAI", ExternalID: "seller", ValidFrom: "2026-01-01T00:00:00+08:00"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.createMapping(ctx, createMappingInput{ProductID: 1, Source: "DCAI", Domain: "VIP", SourceKey: "report"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: 1, Label: "分润", Components: []component{
		{Type: "sales", SourceRole: "PID_USER", Mode: "rate", Value: 1000},
	}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, shanghai)
	appointment, err := s.addOrganizationAssignment(ctx, addAppointmentInput{PersonID: 1, StartsAt: start,
		appointmentTarget: appointmentTarget{UnitID: unitA, Kind: "PRIMARY"}})
	if err != nil {
		t.Fatal(err)
	}
	amount := int64(10000)
	makePayment := func(month time.Month, id string) sourceFact {
		at := time.Date(2026, month, 10, 12, 0, 0, 0, shanghai)
		return sourceFact{Source: "DCAI", Domain: "VIP", SourceKey: "report", Kind: "ORDER_PAID", OrderID: id,
			RecordKey: "payment:" + id, PaymentID: id, Version: "1", AmountMinor: &amount, OccurredAt: at, UpdatedAt: at,
			Participants: []sourceParticipant{{Role: "PID_USER", UserID: "seller"}}}
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{makePayment(time.August, "1"), makePayment(time.September, "2")})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inspect := func(period, version int64, unit string) {
		t.Helper()
		var gotVersion, total int64
		var salesUnit, recipientUnit string
		err := s.db.QueryRow(ctx, `SELECT d.version,d.total_minor,p.department_id::text,l.department_id::text
		 FROM calculation_drafts d JOIN calculation_draft_lines l USING(period_id) JOIN calculation_draft_performance p USING(period_id)
		 WHERE d.period_id=$1`, period).Scan(&gotVersion, &total, &salesUnit, &recipientUnit)
		if err != nil || gotVersion != version || total != 1000 || salesUnit != unit || recipientUnit != unit {
			t.Fatalf("period %d version=%d total=%d sales=%s recipient=%s err=%v", period, gotVersion, total, salesUnit, recipientUnit, err)
		}
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	inspect(1, 1, unitA)
	inspect(2, 1, unitA)
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: 1, ExpectedVersion: 1}, "fixture"); err != nil {
		t.Fatal(err)
	}
	start = time.Date(2026, 8, 5, 0, 0, 0, 0, shanghai)
	if _, err := s.transferOrganizationAssignment(ctx, transferAppointmentInput{
		endAppointmentInput: endAppointmentInput{AssignmentID: appointment.ID, At: start},
		appointmentTarget:   appointmentTarget{UnitID: unitB, Kind: "PRIMARY"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	inspect(1, 1, unitA)
	inspect(2, 2, unitB)

	// Two overlapping requests from independent transactions must not both
	// create appointments. Keep the existing primary appointment throughout.
	candidates := make(chan error, 2)
	for i := range 2 {
		go func() {
			_, err := s.addOrganizationAssignment(ctx, addAppointmentInput{PersonID: 1, StartsAt: start.AddDate(0, 0, i),
				appointmentTarget: appointmentTarget{UnitID: unitB, Kind: "CONCURRENT"}})
			candidates <- err
		}()
	}
	successes, overlaps := 0, 0
	for range 2 {
		switch err := <-candidates; {
		case err == nil:
			successes++
		case errors.Is(err, errAppointmentOverlap):
			overlaps++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || overlaps != 1 {
		t.Fatalf("concurrent appointment writes: %d accepted, %d blocked", successes, overlaps)
	}
}
