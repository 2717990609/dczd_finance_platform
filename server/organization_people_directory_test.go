package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOrganizationPeopleDirectorySearchAndUnassigned(t *testing.T) {
	db := integrationDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, directorFixtureSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO people(name) VALUES('未归属样本');
 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,valid_to)
 VALUES(gen_random_uuid(),5,'42000000-0000-4000-8000-000000000003','MEMBER','2020-01-01','2020-02-01');
 INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(5,'DCJK','99881','2020-01-01'),(5,'DCZD','77882','2020-01-01');
 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
 VALUES(gen_random_uuid(),2,'42000000-0000-4000-8000-000000000003','MEMBER','2020-01-01')`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		term  string
		scope string
		want  int64
	}{{"甲公司", "", 1}, {"201", "", 1}, {"99881", "", 5}, {"", "unassigned", 5}, {"乙公司", "unassigned", 0}, {"", "conflicts", 2}, {"", "multiple-accounts", 5}} {
		var out struct {
			Total int
			Items []struct{ ID int64 }
		}
		if err := db.QueryRow(ctx, organizationPeopleDirectorySQL, test.term, test.scope, 0).Scan(&out); err != nil {
			t.Fatal(err)
		}
		if test.want == 0 && out.Total != 0 || test.want != 0 && (out.Total != 1 || len(out.Items) != 1 || out.Items[0].ID != test.want) {
			t.Fatalf("%+v: %+v", test, out)
		}
	}
}

func TestOrganizationDirectorySeparatesLoginOnlyAndKeepsSourceOrder(t *testing.T) {
	db := integrationDB(t)
	ctx := context.Background()
	_, err := db.Exec(ctx, `INSERT INTO people(name,directory_visible) VALUES('Login only',false),('B',true);
	 INSERT INTO people(name,legacy_party_id,a2_status,a2_name_status) VALUES
	 ('A',gen_random_uuid(),'ACTIVE','SOURCE_CONFIRMED'),('C',gen_random_uuid(),'ACTIVE','MANUAL'),
	 ('D',gen_random_uuid(),'INACTIVE','MANUAL');
	 INSERT INTO finance_accounts(login,person_id,password_hash,enabled,workspace_access)
	 SELECT 'test-'||id,id,'unused',true,true FROM people WHERE name IN ('Login only','C')`)
	if err != nil {
		t.Fatal(err)
	}
	for scope, want := range map[string]string{"": "BCA", "unassigned": "BCA", "archived": "D"} {
		var out struct{ Items []struct{ Name string } }
		if err := db.QueryRow(ctx, organizationPeopleDirectorySQL, "", scope, 0).Scan(&out); err != nil {
			t.Fatal(err)
		}
		var names string
		for _, row := range out.Items {
			names += row.Name
		}
		if names != want {
			t.Fatalf("scope %q returned %q, want %q", scope, names, want)
		}
	}
}

func TestCompanyMemberSearchByPersonAndAccount(t *testing.T) {
	db := integrationDB(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, directorFixtureSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO identity_mappings(person_id,source,external_id,valid_from,valid_to)
	 VALUES(1,'DCJK','111822','2020-01-01','2025-01-01')`); err != nil {
		t.Fatal(err)
	}
	s := workspace{db: db, development: true}
	for _, test := range []struct {
		term, on string
		total    int
	}{{"样本甲", "", 1}, {"1", "", 1}, {"201", "", 1}, {"202", "", 0},
		{"111822", "", 0}, {"111822", "2024-01-01", 1}} {
		req := httptest.NewRequest(http.MethodGet, "/company-members?companyId=42000000-0000-4000-8000-000000000001", nil)
		q := req.URL.Query()
		q.Set("name", test.term)
		q.Set("on", test.on)
		req.URL.RawQuery = q.Encode()
		w := httptest.NewRecorder()
		s.companyMemberDirectory(w, req)
		var results struct {
			Total int
			Items []struct{ PersonID int64 }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &results); w.Code != 200 || err != nil ||
			results.Total != test.total || (test.total == 1 && (len(results.Items) != 1 || results.Items[0].PersonID != 1)) {
			t.Fatalf("%+v: status %d %s", test, w.Code, w.Body)
		}
	}
	for _, term := range []string{"1", "201"} {
		people, err := s.searchFinancePeople(ctx, term)
		var results []struct{ ID int64 }
		if err != nil || json.Unmarshal(people, &results) != nil || len(results) != 1 || results[0].ID != 1 {
			t.Fatalf("directory picker %q: %s (%v)", term, people, err)
		}
	}
}
