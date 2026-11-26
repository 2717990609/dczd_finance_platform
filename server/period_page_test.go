package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPeriodPageFiltersBeforePaging(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, `INSERT INTO products(id,code,name,period_mode) OVERRIDING SYSTEM VALUE
	  VALUES (11,'OLDER','家庭教育','class'),(12,'RECENT','健康观念','class');
	  INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on)
	  SELECT 11,'历史班次 '||n,n,'2020-01-01','2020-01-01' FROM generate_series(1,3)n;
	  INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on)
	  SELECT 12,'新班次 '||n,n,'2026-10-01','2026-10-01' FROM generate_series(1,505)n`); err != nil {
		t.Fatal(err)
	}
	var handler, workflow http.HandlerFunc
	for _, route := range s.routes() {
		if route.Method == http.MethodGet && route.Path == "/api/v1/periods/page" {
			handler = route.Handler
		}
		if route.Method == http.MethodGet && route.Path == "/api/v1/periods" {
			workflow = route.Handler
		}
	}
	if handler == nil {
		t.Fatal("period page endpoint is missing")
	}
	type item struct {
		ID, ProductID int64
		Name          string
	}
	type result struct {
		Items        []item
		NextBeforeID int64
	}
	page := func(query string) result {
		t.Helper()
		reply := httptest.NewRecorder()
		handler(reply, httptest.NewRequest(http.MethodGet, "/api/v1/periods/page?"+query, nil))
		var got result
		if err := json.Unmarshal(reply.Body.Bytes(), &got); err != nil || reply.Code != http.StatusOK {
			t.Fatalf("page response: %d %s: %v", reply.Code, reply.Body, err)
		}
		return got
	}
	first := page("productId=11&limit=2")
	if len(first.Items) != 2 || first.Items[0].Name != "历史班次 3" || first.NextBeforeID != first.Items[1].ID {
		t.Fatalf("first page must include older periods before applying limit: %+v", first)
	}
	second := page(fmt.Sprintf("productId=11&limit=2&beforeId=%d", first.NextBeforeID))
	if len(second.Items) != 1 || second.Items[0].Name != "历史班次 1" || second.NextBeforeID != 0 {
		t.Fatalf("unexpected second page: %+v", second)
	}
	found := page("productId=11&limit=1&q=2020-01-01%20")
	if len(found.Items) != 1 || found.Items[0].ProductID != 11 {
		t.Fatalf("server-side search lost older rows: %+v", found)
	}

	for _, example := range []struct {
		query string
		want  int
	}{
		{"month=2020-01", 3},
		{fmt.Sprintf("periodId=%d", second.Items[0].ID), 1},
	} {
		reply := httptest.NewRecorder()
		workflow(reply, httptest.NewRequest(http.MethodGet, "/api/v1/periods?"+example.query, nil))
		var items []item
		if err := json.Unmarshal(reply.Body.Bytes(), &items); err != nil || reply.Code != http.StatusOK ||
			len(items) != example.want || items[0].ProductID != 11 {
			t.Fatalf("workflow scope %s: status %d, rows %d, wanted %d (%v)", example.query, reply.Code, len(items), example.want, err)
		}
	}
}
