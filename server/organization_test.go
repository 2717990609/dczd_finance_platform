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
	s := workspace{db: integrationDB(t)}
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
