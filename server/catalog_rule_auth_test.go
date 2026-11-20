package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func TestCatalogRuleAccessRequiresSignedScopedSession(t *testing.T) {
	auth := catalogRuleAuth{issuer: "finance-test", key: []byte("test-key-for-finance-rule-access-only")}
	valid := catalogRuleClaims{
		RegisteredClaims: jwt.RegisteredClaims{Issuer: "finance-test", Subject: "fixture",
			Audience: jwt.ClaimStrings{"finance-platform"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))},
		Permissions: []string{catalogRulePublishPermission}, ProductIDs: []int64{13},
	}
	sign := func(claims catalogRuleClaims, key []byte) string {
		t.Helper()
		value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	token := sign(valid, auth.key)
	browser := httptest.NewRequest(http.MethodGet, "https://finance.test/api/v1/catalog-rule-rates", nil)
	browser.AddCookie(&http.Cookie{Name: "finance_session", Value: token})
	if _, err := (workspace{ruleAuth: auth}).financeRateActor(browser); err != errRuleLogin {
		t.Fatalf("managed browser restored access from a separate JWT cookie: %v", err)
	}
	browser.Header.Set("Authorization", "Bearer "+token)
	if actor, err := (workspace{ruleAuth: auth}).financeRateActor(browser); err != nil || actor.subject != "fixture" || !actor.permits(13, true) {
		t.Fatalf("configured API credential no longer works: %q %v", actor.subject, err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://finance.test/api/v1/catalog-rule-rates", nil)
	r.AddCookie(&http.Cookie{Name: "finance_session", Value: token})
	r.Header.Set("Origin", "https://other.finance.test")
	if auth.session(r) != nil {
		t.Fatal("a cookie cannot authorize a cross-origin write")
	}
	r.Header.Set("Origin", "https://finance.test")
	good := auth.session(r)
	if good == nil || !good.permits(13, true) || good.permits(14, true) || good.Subject != "fixture" {
		t.Fatalf("unexpected permissions: %+v", good)
	}
	if (catalogRuleAuth{}).session(r) != nil {
		t.Fatal("an unconfigured verifier accepted a session")
	}
	r.Host = "127.0.0.1:8097"
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if auth.session(r) == nil {
		t.Fatal("same-origin browser write failed behind a reverse proxy")
	}
	r.Header.Set("Sec-Fetch-Site", "same-site")
	if auth.session(r) != nil {
		t.Fatal("a sibling site authorized a cookie write")
	}

	r.Header.Set("Authorization", "Bearer "+sign(valid, []byte("incorrect-secret")))
	if auth.session(r) != nil {
		t.Fatal("accepted a forged bearer token")
	}
	withoutExp := valid
	withoutExp.ExpiresAt = nil
	r.Header.Set("Authorization", "Bearer "+sign(withoutExp, auth.key))
	if auth.session(r) != nil {
		t.Fatal("accepted a token without expiry")
	}
	expired := valid
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	r.Header.Set("Authorization", "Bearer "+sign(expired, auth.key))
	if auth.session(r) != nil {
		t.Fatal("accepted an expired token")
	}
	wrongAudience := valid
	wrongAudience.Audience = jwt.ClaimStrings{"other-app"}
	r.Header.Set("Authorization", "Bearer "+sign(wrongAudience, auth.key))
	if auth.session(r) != nil {
		t.Fatal("accepted a token for another app")
	}
	readonly := valid
	readonly.Permissions = []string{catalogRuleReadPermission}
	r.Header.Set("Authorization", "Bearer "+sign(readonly, auth.key))
	view := auth.session(r)
	if view == nil || !view.permits(13, false) || view.permits(13, true) {
		t.Fatalf("readonly permission did not remain readonly: %+v", view)
	}
}

func TestCatalogRuleDevelopmentAccessWithoutLogin(t *testing.T) {
	for _, tc := range []struct {
		name, method, site string
		configure          func(*http.Request)
		want               error
	}{
		{name: "read", method: http.MethodGet},
		{name: "same-origin write", method: http.MethodPost, site: "same-origin"},
		{name: "cross-origin write", method: http.MethodPost, site: "cross-site", want: errRulePermission},
		{name: "invalid API credential", method: http.MethodGet, want: errRuleLogin, configure: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer invalid")
		}},
		{name: "invalid browser session", method: http.MethodGet, want: errRuleLogin, configure: func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: financeAccountCookie, Value: "invalid"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://127.0.0.1:5194/api/v1/catalog-period-rules", nil)
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			if tc.configure != nil {
				tc.configure(r)
			}
			actor, err := (workspace{development: true}).financeRateActor(r)
			if err != tc.want {
				t.Fatalf("development access returned %v, want %v", err, tc.want)
			}
			if err == nil && (!actor.permits(13, false) || !actor.permits(13, true) || actor.subject == "") {
				t.Fatal("local finance preview should allow rule maintenance and identify its actor")
			}
			if _, err := (workspace{}).financeRateActor(r); err != errRuleLogin {
				t.Fatalf("managed access returned %v without a valid session", err)
			}
		})
	}
}
