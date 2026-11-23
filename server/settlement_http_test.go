package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func verifySettlementHTTP(t *testing.T, ctx context.Context, s workspace, personID, productID int64, draft draftSummary, token string) settlement {
	t.Helper()
	mux := func(server workspace) *http.ServeMux {
		router := http.NewServeMux()
		for _, route := range server.routes() {
			router.HandleFunc(route.Method+" "+route.Path, route.Handler)
		}
		return router
	}
	router := mux(s)
	origin := "http://127.0.0.1"
	call := func(method, path, body, session string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		if session != "" {
			r.AddCookie(&http.Cookie{Name: financeAccountCookie, Value: session})
		}
		router.ServeHTTP(w, r)
		return w
	}
	grant := financeAccountConfig{PersonID: personID, Login: "settlement.fixture", Enabled: true, Workspace: true}
	credentials := financeLoginInput{Login: grant.Login, Password: "isolated-close-fixture"}
	if err := s.saveFinanceAccount(ctx, grant, credentials.Password); err != nil {
		t.Fatal(err)
	}
	login := func() string {
		t.Helper()
		key, err := s.loginFinanceAccount(ctx, credentials)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	view := func(key string) settlementReview {
		t.Helper()
		w := call(http.MethodGet, fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", draft.PeriodID, draft.Version), "", key)
		var review settlementReview
		if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil || w.Code != 200 {
			t.Fatalf("read review: %d %s %v", w.Code, w.Body, err)
		}
		return review
	}
	submit := func(key, reviewed string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(confirmSettlementInput{PeriodID: draft.PeriodID, ExpectedVersion: draft.Version, ReviewToken: reviewed})
		return call(http.MethodPost, "/api/v1/settlements", string(body), key)
	}
	if w := submit("", token); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous confirmation: %d %s", w.Code, w.Body)
	}
	viewer := login()
	if r := view(viewer); r.MayConfirm || r.CanConfirm || r.Token != "" {
		t.Fatalf("viewer received confirmation controls: %+v", r)
	}
	if w := submit(viewer, token); w.Code != http.StatusForbidden {
		t.Fatalf("workbench permission authorized confirmation: %d %s", w.Code, w.Body)
	}
	grant.Settlements = []int64{productID}
	if err := s.saveFinanceAccount(ctx, grant, ""); err != nil {
		t.Fatal(err)
	}
	if w := submit(viewer, token); w.Code != http.StatusUnauthorized {
		t.Fatalf("old session survived grant change: %d %s", w.Code, w.Body)
	}
	operator := login()
	approved := view(operator)
	if !approved.MayConfirm || !approved.CanConfirm || approved.Token != token || approved.Total != draft.Total {
		t.Fatalf("expected an actionable review: %+v", approved)
	}
	w := submit(operator, strings.Repeat("0", 64))
	if w.Code != http.StatusConflict {
		t.Fatalf("stale review: %d %s", w.Code, w.Body)
	}
	w = submit(operator, approved.Token)
	var result settlement
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.ID == 0 {
		t.Fatalf("confirmation: %d %s %v", w.Code, w.Body, err)
	}
	var stamped bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM finance_accounts WHERE login=$1 AND id::text=$2)`, grant.Login, result.ConfirmedBy).Scan(&stamped); err != nil || !stamped {
		t.Fatalf("missing confirming finance account: %t %v", stamped, err)
	}
	w = submit(operator, approved.Token)
	var repeated settlement
	if err := json.Unmarshal(w.Body.Bytes(), &repeated); err != nil || w.Code != 200 || repeated.ID != result.ID {
		t.Fatalf("retry created a different result: %d %s %v", w.Code, w.Body, err)
	}
	if r := view(operator); r.Settlement == nil || r.Settlement.ID != result.ID || r.CanConfirm || r.Token != "" {
		t.Fatalf("closed result was not stable: %+v", r)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM settlements WHERE id=$1`, result.ID); err != nil {
		t.Fatal(err)
	}
	if w := submit(operator, approved.Token); w.Code != http.StatusConflict {
		t.Fatalf("an old review could reclose a technically removed result: %d %s", w.Code, w.Body)
	}
	reopened := view(operator)
	if !reopened.CanConfirm || reopened.Token == approved.Token {
		t.Fatalf("expected a fresh review after technical removal: %+v", reopened)
	}
	local := s
	local.development = true
	router = mux(local)
	localReview := view("")
	if !localReview.MayConfirm || !localReview.CanConfirm || localReview.Token != reopened.Token {
		t.Fatalf("local workspace should offer confirmation after the same review: %+v", localReview)
	}
	origin = "http://unrelated.example"
	if w := submit("", localReview.Token); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin local confirmation should be rejected: %d %s", w.Code, w.Body)
	}
	origin = "http://127.0.0.1"
	if w := submit("", ""); w.Code != http.StatusConflict {
		t.Fatalf("local confirmation must require a review: %d %s", w.Code, w.Body)
	}
	if w := submit("", approved.Token); w.Code != http.StatusConflict {
		t.Fatalf("local confirmation must also reject an obsolete review: %d %s", w.Code, w.Body)
	}
	w = submit("", localReview.Token)
	if err := json.Unmarshal(w.Body.Bytes(), &repeated); err != nil || w.Code != 200 || repeated.ID == result.ID {
		t.Fatalf("confirmation after fresh review: %d %s %v", w.Code, w.Body, err)
	}
	if repeated.ConfirmedBy != "本地财务" {
		t.Fatalf("local development confirmation should record its origin: %+v", repeated)
	}
	return repeated
}
