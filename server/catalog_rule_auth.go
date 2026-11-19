package main

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/golang-jwt/jwt/v4"
)

const (
	catalogRuleReadPermission    = "finance.rules.read"
	catalogRulePublishPermission = "finance.rules.publish"
)

var (
	errRuleLogin      = errors.New("请登录后查看或发布产品规则")
	errRulePermission = errors.New("当前账号没有该产品的规则权限")
)

type catalogRuleAuth struct {
	issuer string
	key    []byte
}

type catalogRuleClaims struct {
	jwt.RegisteredClaims
	Permissions []string `json:"permission_codes"`
	ProductIDs  []int64  `json:"product_ids"`
}

func readCatalogRuleAuth() (catalogRuleAuth, error) {
	issuer := strings.TrimSpace(os.Getenv("FINANCE_SESSION_ISSUER"))
	key := os.Getenv("FINANCE_SESSION_SECRET")
	if issuer == "" && key == "" {
		return catalogRuleAuth{}, nil
	}
	if issuer == "" || len(key) < 32 {
		return catalogRuleAuth{}, errors.New("configure FINANCE_SESSION_ISSUER and a FINANCE_SESSION_SECRET of at least 32 bytes")
	}
	return catalogRuleAuth{issuer: issuer, key: []byte(key)}, nil
}

func (a catalogRuleAuth) session(r *http.Request) *catalogRuleClaims {
	if len(a.key) == 0 {
		return nil
	}
	var credential string
	if authorization := r.Header.Get("Authorization"); authorization != "" {
		kind, value, ok := strings.Cut(authorization, " ")
		if !ok || !strings.EqualFold(kind, "Bearer") {
			return nil
		}
		credential = value
	} else if cookie, err := r.Cookie("finance_session"); err == nil {
		if r.Method != http.MethodGet && !catalogCookieWriteAllowed(r) {
			return nil
		}
		credential = cookie.Value
	}
	if credential == "" {
		return nil
	}
	var claims catalogRuleClaims
	checked, err := jwt.ParseWithClaims(credential, &claims, func(*jwt.Token) (any, error) {
		return a.key, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !checked.Valid || claims.ExpiresAt == nil || !textOK(claims.Subject, 128) ||
		!claims.VerifyIssuer(a.issuer, true) || !claims.VerifyAudience("finance-platform", true) {
		return nil
	}
	return &claims
}

func catalogCookieWriteAllowed(r *http.Request) bool {
	// Browser-generated metadata describes the public origin even if a proxy rewrites Host.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && strings.EqualFold(origin.Host, r.Host) &&
		(origin.Scheme == "https" || origin.Scheme == "http")
}

func (a *catalogRuleClaims) permits(product int64, publish bool) bool {
	return a != nil && slices.Contains(a.ProductIDs, product) &&
		(slices.Contains(a.Permissions, catalogRulePublishPermission) ||
			!publish && slices.Contains(a.Permissions, catalogRuleReadPermission))
}
