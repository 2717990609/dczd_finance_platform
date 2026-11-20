package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBusinessDutyHandoverPreservesHolderAndProduct(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	raw := json.RawMessage(`{"capturedAt":"2025-10-01T00:00:00Z","people":[],"units":[
 {"id":"00000000-0000-4000-8000-000000000001","name":"样本小组","kind":"TEAM","status":"ACTIVE"}],"assignments":[],
 "dutyScopes":[{"id":"00000000-0000-4000-8000-000000000010","kind":"PRODUCT","code":"DCZD_PRACTICE","name":"健康观念实战班","active":true},
 {"id":"00000000-0000-4000-8000-000000000011","kind":"PRODUCT","code":"RULESET_R16","name":"家庭教育实战班","active":true}],
 "businessDuties":[]}`)
	if err := storeOrganizationSnapshot(ctx, s.db, raw); err != nil {
		t.Fatal(err)
	}
	var person int64
	if err := s.db.QueryRow(ctx, "INSERT INTO people(name) VALUES('样本人员') RETURNING id").Scan(&person); err != nil {
		t.Fatal(err)
	}
	date := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	scope := "00000000-0000-4000-8000-000000000011"
	unit := "00000000-0000-4000-8000-000000000001"
	input := createBusinessDutyInput{dutySubject: dutySubject{UnitID: unit}, Role: "PRACTICE_TEAM_DIRECTOR", ScopeID: scope, StartsAt: date}
	restricted := context.WithValue(ctx, financePrincipalKey{}, &financePrincipal{Workspace: true})
	if _, err := s.addBusinessDuty(restricted, input); !errors.Is(err, errFinanceForbidden) {
		t.Fatalf("write without permission: %v", err)
	}
	original, err := s.addBusinessDuty(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.addBusinessDuty(ctx, input)
	if err != nil || original != again {
		t.Fatalf("retry did not return same appointment: %v %v", again, err)
	}
	input.StartsAt = date.Add(time.Hour)
	if _, err := s.addBusinessDuty(ctx, input); !errors.Is(err, errBusinessDutyOverlap) {
		t.Fatalf("overlapping appointment: %v", err)
	}
	at := date.AddDate(0, 0, 1)
	transfer := transferBusinessDutyInput{endAppointmentInput: endAppointmentInput{AssignmentID: original.ID, At: at}, dutySubject: dutySubject{PersonID: person}}
	replacement, err := s.transferBusinessDuty(ctx, transfer)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.transferBusinessDuty(ctx, transfer)
	if err != nil || retry != replacement {
		t.Fatalf("handover retry: %v %v", retry, err)
	}
	assertTotal := func(query string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.organizationBusinessDuties(w, httptest.NewRequest(http.MethodGet, "/api/v1/organization-business-duties?"+query, nil))
		var list struct{ Total int }
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 || list.Total != want {
			t.Fatalf("bad duty results: %d %s", w.Code, w.Body.String())
		}
	}
	assertTotal("on=2025-10-02&scopeId="+scope, 1)
	assertTotal("on=2025-10-03&scopeId="+scope+"&personId="+strconv.FormatInt(person, 10), 1)
	assertTotal("history=true&scopeId="+scope, 2)
	assertTotal("history=true&scopeId=00000000-0000-4000-8000-000000000010", 0)
}

func TestBusinessDutyImportPreservesLocalEndAndScope(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	const id = "00000000-0000-4000-8000-000000000021"
	const snapshot = `{"capturedAt":"2025-10-01T00:00:00Z",
	"people":[{"id":"00000000-0000-4000-8000-000000000022","name":"样本人员"}],"units":[],"assignments":[],
	"dutyScopes":[{"id":"00000000-0000-4000-8000-000000000023","kind":"PRODUCT","code":"DCZD_PRACTICE","name":"健康观念实战班","active":true}],
	"businessDuties":[{"id":"` + id + `","role":"PRACTICE_AGENT_RECOMMENDATION","scopeId":"00000000-0000-4000-8000-000000000023",
	 "personId":"00000000-0000-4000-8000-000000000022","from":"2025-09-01T00:00:00Z"}]}`
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(snapshot)); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2025, 10, 2, 0, 0, 0, 0, time.UTC)
	if _, err := s.endBusinessDuty(ctx, endAppointmentInput{AssignmentID: id, At: at}); err != nil {
		t.Fatal(err)
	}
	newer := strings.Replace(snapshot, "2025-10-01T00:00:00Z", "2025-10-03T00:00:00Z", 1)
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(newer)); err != nil {
		t.Fatal(err)
	}
	var through time.Time
	if err := s.db.QueryRow(ctx, "SELECT valid_to FROM organization_business_duties WHERE id=$1", id).Scan(&through); err != nil || !through.Equal(at) {
		t.Fatalf("reimport changed the local end: %v %v", through, err)
	}
	remapped := strings.ReplaceAll(snapshot, "DCZD_PRACTICE", "RULESET_R16")
	remapped = strings.Replace(remapped, "2025-10-01T00:00:00Z", "2025-10-04T00:00:00Z", 1)
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(remapped)); !errors.Is(err, invalid) {
		t.Fatalf("scope identity may not change on import: %v", err)
	}
}
