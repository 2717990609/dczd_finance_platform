package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/zeromicro/go-zero/rest"
)

func developmentAccess(addr, database string) (bool, error) {
	switch mode := strings.TrimSpace(os.Getenv("FINANCE_ACCESS_MODE")); mode {
	case "", "managed":
		return false, nil
	case "development":
		host, _, err := net.SplitHostPort(addr)
		if err == nil && isLoopbackHost(host) && strings.HasSuffix(database, "_dev") {
			return true, nil
		}
		return false, errors.New("development access requires a loopback address and a database ending in _dev")
	default:
		return false, errors.New("FINANCE_ACCESS_MODE must be managed or development")
	}
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

func (s workspace) financeRoute(route rest.Route) rest.Route {
	switch route.Path {
	case "/api/v1/session", "/api/v1/catalog-rule-rates", "/api/v1/catalog-rule-people", "/api/v1/catalog-rule-impact",
		"/api/v1/catalog-period-rules", "/api/v1/catalog-period-rules/preview":
		return route
	}
	rulesAccess := false
	switch route.Path {
	case "/api/v1/configuration-options", "/api/v1/rules", "/api/v1/rule-catalog", "/api/v1/practice-count-openings", "/api/v1/practice-rule-recipients", "/api/v1/practice-pools", "/api/v1/rule-impact":
		rulesAccess = true
	case "/api/v1/products", "/api/v1/periods":
		rulesAccess = route.Method == http.MethodGet
	}
	handler := route.Handler
	route.Handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		principal, err := s.accountSession(r)
		if err != nil {
			respond(w, nil, err)
			return
		}
		if !s.development || principal != nil {
			if principal == nil {
				respond(w, nil, errFinanceLogin)
				return
			}
			if r.Method != http.MethodGet && !catalogCookieWriteAllowed(r) {
				respond(w, nil, errFinanceForbidden)
				return
			}
			if !principal.Workspace && (!rulesAccess || len(principal.Rules) == 0) {
				respond(w, nil, errFinanceForbidden)
				return
			}
		}
		if principal != nil {
			r = r.WithContext(context.WithValue(r.Context(), financePrincipalKey{}, principal))
		}
		handler(w, r)
	}
	return route
}

func (s workspace) listForAuthorizedProducts(query string, allowWorkspaceScope bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var scope []int64
		if actor := financeUser(r.Context()); actor != nil && (!actor.Workspace || !allowWorkspaceScope) {
			scope = make([]int64, 0, len(actor.Rules))
			for _, grant := range actor.Rules {
				scope = append(scope, grant.ProductID)
			}
		}
		var result json.RawMessage
		err := s.db.QueryRow(r.Context(), query, scope).Scan(&result)
		respond(w, result, err)
	}
}

func (s workspace) publishAuthorizedRule(ctx context.Context, input publishRuleInput) (any, error) {
	if actor := financeUser(ctx); actor != nil {
		if !actor.permitsRule(input.ProductID, true) {
			return nil, errFinanceForbidden
		}
	}
	return s.publishRule(ctx, input)
}

type financeRuleActor struct {
	subject string
	permits func(int64, bool) bool
	account *financePrincipal
}

func (s workspace) financeRateActor(r *http.Request) (financeRuleActor, error) {
	if r.Header.Get("Authorization") == "" {
		p, err := s.accountSession(r)
		if err != nil {
			return financeRuleActor{}, err
		}
		if p != nil {
			if r.Method != http.MethodGet && !catalogCookieWriteAllowed(r) {
				return financeRuleActor{}, errRulePermission
			}
			return financeRuleActor{subject: p.ID, permits: p.permitsRule, account: p}, nil
		}
		if !s.development {
			return financeRuleActor{}, errRuleLogin
		}
	}
	p := s.ruleAuth.session(r)
	if p == nil {
		return financeRuleActor{}, errRuleLogin
	}
	return financeRuleActor{subject: p.Subject, permits: p.permits}, nil
}
