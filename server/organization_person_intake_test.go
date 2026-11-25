package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOrganizationPersonIntakeIsAtomicAndRepeatable(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, directorFixtureSQL); err != nil {
		t.Fatal(err)
	}
	account := func(phone string) dispatchAccount {
		return dispatchAccount{Status: "FOUND", UserID: phone, State: "ACTIVE", CompanyID: "601", MemberID: "m" + phone, Role: "MEMBER", Name: "验证账号"}
	}
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]string
		if r.URL.Path != organizationLookupPath || json.NewDecoder(r.Body).Decode(&v) != nil {
			http.Error(w, "lookup", 400)
			return
		}
		_ = json.NewEncoder(w).Encode(account(v["phone"]))
	}))
	defer stub.Close()
	old := http.DefaultTransport
	http.DefaultTransport = stub.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, target := range []string{"DCZD", "DCJK"} {
		prefix := "ORG_SYNC_" + target + "_"
		t.Setenv(prefix+"ENABLED", "true")
		t.Setenv(prefix+"BASE_URL", stub.URL)
		t.Setenv(prefix+"KEY_ID", "local-test")
		t.Setenv(prefix+"SECRET", "01234567890123456789012345678901")
	}
	t.Setenv("ORG_SYNC_PHONE_HASH_SECRET", "01234567890123456789012345678901")
	row := func(target, num string) intakeBinding {
		return intakeBinding{dispatchBindInput: dispatchBindInput{dispatchLookupInput: dispatchLookupInput{Target: target, Phone: num, CountryCode: "+86"}, UserID: num}, Confirmed: account(num)}
	}
	req := personIntakeInput{Name: "新建样本", MembershipCompanyID: "42000000-0000-4000-8000-000000000001", MemberRole: "MEMBER", RequestID: uuid.NewString(), Bindings: []intakeBinding{row("DCJK", "10000111"), row("DCZD", "10000112")}}
	if _, err := s.db.Exec(ctx, `INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(1,'DCZD','10000112',now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.savePersonIntake(ctx, req); err == nil {
		t.Fatal("conflicting account accepted")
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM people WHERE name='新建样本')+(SELECT count(*) FROM organization_dispatch_links WHERE entity_type='MEMBER')+(SELECT count(*) FROM organization_person_intakes)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial intake: %d %v", n, err)
	}
	req.Bindings[1] = row("DCZD", "10000113")
	saved, err := s.savePersonIntake(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.savePersonIntake(ctx, req)
	if err != nil || saved["personId"] != again["personId"] {
		t.Fatal(again, err)
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM organization_dispatch_links WHERE person_id=$1`, saved["personId"]).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM organization_assignments WHERE person_id=$1 AND unit_id=$2`, saved["personId"], req.MembershipCompanyID).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	req.Name = "不同请求"
	if _, err = s.savePersonIntake(ctx, req); !errors.Is(err, conflict) {
		t.Fatal("request replay conflict", err)
	}
	req.RequestID = uuid.NewString()
	req.Name = "过时核验"
	req.Bindings[0].Confirmed.CompanyID = "changed"
	if _, err = s.savePersonIntake(ctx, req); err == nil {
		t.Fatal("stale identity accepted")
	}
	// An existing member and a second owner cannot overlap the current appointment.
	assign := personIntakeInput{PersonID: saved["personId"], RequestID: uuid.NewString(), MembershipCompanyID: "42000000-0000-4000-8000-000000000002", MemberRole: "OWNER"}
	if _, err = s.savePersonIntake(ctx, assign); err == nil {
		t.Fatal("overlapping company accepted")
	}
	var free int64
	if err = s.db.QueryRow(ctx, "INSERT INTO people(name) VALUES('负责人样本') RETURNING id").Scan(&free); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(time.Hour)
	assign.StartsAt = &at
	assign.PersonID = free
	if _, err = s.savePersonIntake(ctx, assign); err != nil {
		t.Fatal(err)
	}
	assign.RequestID = uuid.NewString()
	if err = s.db.QueryRow(ctx, "INSERT INTO people(name) VALUES('另一负责人') RETURNING id").Scan(&assign.PersonID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.savePersonIntake(ctx, assign); err == nil {
		t.Fatal("duplicate owner accepted")
	}
}
