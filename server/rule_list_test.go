package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestComponentRuleListReadsSummaryAndLoadsAuthorizedVersion(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, `INSERT INTO products(id,code,name,period_mode) OVERRIDING SYSTEM VALUE VALUES
	 (1,'class-a','Class A','class'),(2,'class-b','Class B','class');
	 INSERT INTO rule_versions(id,product_id,version,label,components) OVERRIDING SYSTEM VALUE VALUES
	 (11,1,1,'Earlier','[{"type":"sales","mode":"rate","classMember":"new","value":1200}]'),
	 (12,1,2,'Later','[{"type":"sales","mode":"tiered","classMember":"new","tiers":{"includeRetrain":true}}]'),
	 (13,2,1,'Other company','[{"type":"sales","mode":"rate","value":2300}]');`); err != nil {
		t.Fatal(err)
	}
	operator := &financePrincipal{Rules: []financeRuleGrant{{ProductID: 1}}}
	call := func(query string, want int) json.RawMessage {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/rules"+query, nil)
		r = r.WithContext(context.WithValue(r.Context(), financePrincipalKey{}, operator))
		for _, route := range s.routes() {
			if route.Path == r.URL.Path && route.Method == r.Method {
				route.Handler(w, r)
				break
			}
		}
		if w.Code != want {
			t.Fatalf("%s: status=%d, want %d, response=%s", query, w.Code, want, w.Body)
		}
		return w.Body.Bytes()
	}
	var list []struct {
		ID             int64           `json:"id"`
		ClassMembers   []string        `json:"classMembers"`
		ComponentCount int             `json:"componentCount"`
		Components     json.RawMessage `json:"components"`
	}
	if err := json.Unmarshal(call("?view=summary", http.StatusOK), &list); err != nil ||
		len(list) != 2 || list[0].ID != 12 || list[1].ID != 11 ||
		len(list[0].Components) != 0 || len(list[1].Components) != 0 ||
		len(list[0].ClassMembers) != 2 || len(list[1].ClassMembers) != 1 || list[1].ClassMembers[0] != "new" || list[0].ComponentCount != 1 {
		t.Fatalf("summary must preserve authorized history and shared retraining eligibility: %+v %v", list, err)
	}
	var detail struct {
		ID         int64       `json:"id"`
		Components []component `json:"components"`
	}
	if err := json.Unmarshal(call("?view=detail&id=11", http.StatusOK), &detail); err != nil ||
		detail.ID != 11 || len(detail.Components) != 1 || detail.Components[0].Value != 1200 {
		t.Fatalf("historical detail: %+v %v", detail, err)
	}
	call("?view=detail&id=13", http.StatusNotFound)
	call("?view=detail&id=bad", http.StatusBadRequest)
	if err := json.Unmarshal(call("", http.StatusOK), &list); err != nil || len(list) != 2 || len(list[0].Components) == 0 {
		t.Fatalf("existing rules consumer must still receive full components: %+v %v", list, err)
	}
}
