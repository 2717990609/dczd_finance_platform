package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOrganizationDirectoryUsesLocalAssignmentIntervals(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	raw := json.RawMessage(`{
		"capturedAt":"2026-10-02T00:00:00Z",
		"people":[{"id":"00000000-0000-0000-0000-000000000001","name":"测试人员"}],
		"units":[
			{"id":"00000000-0000-0000-0000-000000000002","name":"测试公司","kind":"COMPANY","status":"ACTIVE"},
			{"id":"00000000-0000-0000-0000-000000000003","parentId":"00000000-0000-0000-0000-000000000002","name":"测试小组","kind":"TEAM","status":"ACTIVE"}
		],
		"assignments":[
			{"id":"00000000-0000-0000-0000-000000000004","personId":"00000000-0000-0000-0000-000000000001","unitId":"00000000-0000-0000-0000-000000000002","kind":"PRIMARY","from":"2026-09-30T16:00:00Z","to":"2026-10-01T16:00:00Z"},
			{"id":"00000000-0000-0000-0000-000000000005","personId":"00000000-0000-0000-0000-000000000001","unitId":"00000000-0000-0000-0000-000000000003","kind":"CONCURRENT","from":"2026-10-01T16:00:00Z"}
		]
	}`)
	if _, err := s.db.Exec(ctx, `INSERT INTO people(name,legacy_party_id)
	 VALUES ('期初已关联人员','00000000-0000-0000-0000-000000000001')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := storeOrganizationSnapshot(ctx, s.db, raw); err != nil {
			t.Fatal(err)
		}
	}
	query := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, route := range s.routes() {
			if route.Method == req.Method && route.Path == req.URL.Path {
				route.Handler(w, req)
				if w.Code != status {
					t.Fatalf("%s = %d: %s", path, w.Code, w.Body)
				}
				return w.Body.String()
			}
		}
		t.Fatalf("no route for %s", path)
		return ""
	}
	if got := query("/api/v1/organization-units", 200); !strings.Contains(got, "测试小组") {
		t.Fatal(got)
	}
	if got := query("/api/v1/organization-assignments?on=2026-10-01", 200); !strings.Contains(got, "测试公司") || strings.Contains(got, "测试小组") {
		t.Fatal(got)
	}
	if got := query("/api/v1/organization-assignments?on=2026-10-02", 200); !strings.Contains(got, "测试小组") || strings.Contains(got, "测试公司") {
		t.Fatal(got)
	}
	if got := query("/api/v1/organization-assignments?unitId=00000000-0000-0000-0000-000000000002", 200); !strings.Contains(got, `"items":[]`) {
		t.Fatal(got)
	}
	if got := query("/api/v1/organization-assignments?unitId=00000000-0000-0000-0000-000000000002&history=true", 200); !strings.Contains(got, "测试公司") {
		t.Fatal(got)
	}
	const parentOnChildDay = "/api/v1/organization-assignments?unitId=00000000-0000-0000-0000-000000000002&on=2026-10-02"
	if got := query(parentOnChildDay, 200); !strings.Contains(got, `"total":0`) {
		t.Fatal(got)
	}
	if got := query(parentOnChildDay+"&includeChildren=true", 200); !strings.Contains(got, "测试小组") ||
		!strings.Contains(got, `"unitKind":"TEAM"`) || !strings.Contains(got, `"total":1`) {
		t.Fatal(got)
	}
	query(parentOnChildDay+"&includeChildren=invalid", 400)
	if got := query("/api/v1/organization-assignments?name=1&history=true", 200); !strings.Contains(got, `"total":2`) {
		t.Fatal(got)
	}
	query("/api/v1/organization-assignments?on=2026-02-30", 400)
	if got := query("/api/v1/organization-assignments?history=true&name=测试人员&page=2", 200); !strings.Contains(got, `"items":[],"total":2`) {
		t.Fatal(got)
	}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM people").Scan(&count); err != nil || count != 1 {
		t.Fatalf("person key was not reused: count=%d, err=%v", count, err)
	}
	older := strings.Replace(string(raw), "2026-10-02T00:00:00Z", "2026-10-01T00:00:00Z", 1)
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(older)); err == nil {
		t.Fatal("stale snapshot was accepted")
	}
	newer := strings.Replace(string(raw), "2026-10-02T00:00:00Z", "2026-10-03T00:00:00Z", 1)
	withoutPersonID := strings.Replace(newer, `{"id":"00000000-0000-0000-0000-000000000001","name":"测试人员"}`,
		`{"name":"缺少主键的人员"}`, 1)
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(withoutPersonID)); err == nil {
		t.Fatal("snapshot without a person primary key was accepted")
	}
	cyclic := strings.Replace(newer, `"name":"测试公司"`,
		`"name":"修改后的公司","parentId":"00000000-0000-0000-0000-000000000003"`, 1)
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(cyclic)); err == nil {
		t.Fatal("snapshot with a parent cycle was accepted")
	}
	if got := query("/api/v1/organization-units", 200); strings.Contains(got, "修改后的公司") {
		t.Fatalf("failed import committed part of the hierarchy: %s", got)
	}
}

func TestOrganizationDirectoryFiltersAssignmentsByInheritedScope(t *testing.T) {
	s := workspace{db: integrationDB(t), development: true}
	ctx := context.Background()
	if _, err := s.db.Exec(ctx, `
	 INSERT INTO organization_units(id,parent_id,name,kind,status,sales_company_type) VALUES
	  ('00000000-0000-4000-8000-000000000001',NULL,'总部','COMPANY','ACTIVE',NULL),
	  ('00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000001','内部部门','DEPARTMENT','ACTIVE',NULL),
	  ('00000000-0000-4000-8000-000000000003','00000000-0000-4000-8000-000000000001','外部公司','COMPANY','ACTIVE','EXTERNAL'),
	  ('00000000-0000-4000-8000-000000000004','00000000-0000-4000-8000-000000000003','外部小组','TEAM','ACTIVE',NULL),
	  ('00000000-0000-4000-8000-000000000005','00000000-0000-4000-8000-000000000001','待确认公司','COMPANY','ACTIVE','UNKNOWN'),
	  ('00000000-0000-4000-8000-000000000006','00000000-0000-4000-8000-000000000005','待确认小组','TEAM','ACTIVE',NULL);
	 WITH fixture AS (SELECT generate_series(1,26) AS i)
	 INSERT INTO people(name) SELECT 'External '||lpad(i::text,2,'0') FROM fixture;
	 INSERT INTO people(name) VALUES ('Former external'),('Internal'),('Pending');
	 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,valid_to)
	 SELECT gen_random_uuid(),p.id,u.id,'PRIMARY','2026-10-01 00:00:00+08',
	  CASE WHEN p.name='Former external' THEN '2026-10-02 00:00:00+08'::timestamptz END
	 FROM people p JOIN organization_units u ON u.name=CASE p.name
	  WHEN 'Internal' THEN '内部部门' WHEN 'Pending' THEN '待确认小组' ELSE '外部小组' END;`); err != nil {
		t.Fatal(err)
	}
	request := func(handler http.HandlerFunc, path string, status int) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status {
			t.Fatalf("%s = %d, want %d: %s", path, w.Code, status, w.Body)
		}
		return w.Body.Bytes()
	}
	const assignments = "/api/v1/organization-assignments?"
	for _, tc := range []struct {
		query       string
		total, size int
		unitName    string
	}{
		{"history=true", 29, 25, ""},
		{"scope=INTERNAL&history=true", 1, 1, "内部部门"},
		{"scope=EXTERNAL&on=2026-10-02", 26, 25, "外部小组"},
		{"scope=EXTERNAL&on=2026-10-02&page=2", 26, 1, "外部小组"},
		{"scope=EXTERNAL&name=Former&on=2026-10-01", 1, 1, "外部小组"},
		{"scope=EXTERNAL&name=Former&on=2026-10-02", 0, 0, ""},
		{"scope=INTERNAL&unitId=00000000-0000-4000-8000-000000000001&includeChildren=true&history=true", 1, 1, "内部部门"},
		{"scope=INTERNAL&unitId=00000000-0000-4000-8000-000000000003&includeChildren=true&history=true", 0, 0, ""},
	} {
		var got struct {
			Total int
			Items []struct{ UnitName string }
		}
		if err := json.Unmarshal(request(s.organizationAssignments, assignments+tc.query, 200), &got); err != nil {
			t.Fatal(err)
		}
		if got.Total != tc.total || len(got.Items) != tc.size {
			t.Errorf("%s: total=%d size=%d, want %d/%d", tc.query, got.Total, len(got.Items), tc.total, tc.size)
		}
		for _, item := range got.Items {
			if tc.unitName != "" && item.UnitName != tc.unitName {
				t.Errorf("%s: assignment for %q, want %q", tc.query, item.UnitName, tc.unitName)
			}
		}
	}
	request(s.organizationAssignments, assignments+"scope=UNKNOWN", 400)

	var units []struct{ Name, DirectoryScope string }
	if err := json.Unmarshal(request(s.list(organizationUnitsSQL), "/api/v1/organization-units", 200), &units); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"总部": "INTERNAL", "内部部门": "INTERNAL", "外部公司": "EXTERNAL",
		"外部小组": "EXTERNAL", "待确认公司": "UNKNOWN", "待确认小组": "UNKNOWN"}
	if len(units) != len(want) {
		t.Fatalf("units = %v, want %v", units, want)
	}
	for _, unit := range units {
		if unit.DirectoryScope != want[unit.Name] {
			t.Errorf("%s scope = %q, want %q", unit.Name, unit.DirectoryScope, want[unit.Name])
		}
	}
}
