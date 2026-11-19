package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/httpx"
)

//go:embed schema.sql
var schema string

type component struct {
	Type  string `json:"type"`
	Mode  string `json:"mode"`
	Value int64  `json:"value"`
}

var invalid = errors.New("请核对名称、范围与必填配置")
var conflict = errors.New("记录与现有配置冲突，请重新读取")

func validateComponents(items []component) error {
	if len(items) == 0 || len(items) > 4 {
		return invalid
	}
	seen := make(map[string]bool)
	for _, c := range items {
		switch c.Type {
		case "sales", "referral", "manager", "delivery":
		default:
			return invalid
		}
		if seen[c.Type] || c.Value < 0 {
			return invalid
		}
		seen[c.Type] = true
		switch c.Mode {
		case "rate":
			if c.Value > 10000 {
				return invalid
			}
		case "fixed":
			if c.Value > 100000000000 {
				return invalid
			}
		default:
			return invalid
		}
	}
	return nil
}

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(637619238)"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schema)
		return err
	})
}

type workspace struct {
	db      *pgxpool.Pool
	sources map[string]sourceConfig
}

func (s workspace) routes() []rest.Route {
	return []rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/products", Handler: s.list(productsSQL)},
		{Method: http.MethodPost, Path: "/api/v1/products", Handler: submit(s.createProduct)},
		{Method: http.MethodGet, Path: "/api/v1/mappings", Handler: s.list(mappingsSQL)},
		{Method: http.MethodPost, Path: "/api/v1/mappings", Handler: submit(s.createMapping)},
		{Method: http.MethodGet, Path: "/api/v1/periods", Handler: s.list(periodsSQL)},
		{Method: http.MethodPost, Path: "/api/v1/periods", Handler: submit(s.createPeriod)},
		{Method: http.MethodGet, Path: "/api/v1/rules", Handler: s.list(rulesSQL)},
		{Method: http.MethodPost, Path: "/api/v1/rules", Handler: submit(s.publishRule)},
		{Method: http.MethodGet, Path: "/api/v1/people", Handler: s.list(peopleSQL)},
		{Method: http.MethodPost, Path: "/api/v1/people", Handler: submit(s.createPerson)},
		{Method: http.MethodGet, Path: "/api/v1/identities", Handler: s.list(identitiesSQL)},
		{Method: http.MethodPost, Path: "/api/v1/identities", Handler: submit(s.assignIdentity)},
		{Method: http.MethodGet, Path: "/api/v1/orders", Handler: s.orders},
		{Method: http.MethodPost, Path: "/api/v1/orders/pull", Handler: submit(s.pullOrder)},
		{Method: http.MethodGet, Path: "/api/v1/sources", Handler: s.sourceStatuses},
	}
}

func submit[T any](apply func(context.Context, T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input T
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			respond(w, nil, invalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			respond(w, nil, invalid)
			return
		}
		result, err := apply(r.Context(), input)
		respond(w, result, err)
	}
}

func respond(w http.ResponseWriter, result any, err error) {
	if err == nil {
		httpx.OkJson(w, result)
		return
	}
	code, message := http.StatusInternalServerError, "处理失败，请稍后重试"
	var dbErr *pgconn.PgError
	switch {
	case errors.Is(err, invalid):
		code, message = 400, err.Error()
	case errors.Is(err, conflict):
		code, message = 409, err.Error()
	case errors.Is(err, pgx.ErrNoRows):
		code, message = 404, "找不到对应记录"
	case errors.Is(err, errSourceDisabled):
		code, message = 409, "来源接口尚未配置"
	case errors.Is(err, errSourceRead):
		code, message = 502, "来源读取失败，请核对接口或订单号"
	case errors.As(err, &dbErr) && dbErr.Code == "23505":
		code, message = 409, conflict.Error()
	case errors.As(err, &dbErr) && (dbErr.Code == "23503" || dbErr.Code == "23514"):
		code, message = 400, invalid.Error()
	}
	httpx.WriteJson(w, code, map[string]string{"message": message})
}

func (s workspace) list(query string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var result json.RawMessage
		err := s.db.QueryRow(r.Context(), query).Scan(&result)
		respond(w, result, err)
	}
}

const productsSQL = `SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id DESC),'[]') FROM
 (SELECT id,code,name,period_mode AS "periodMode" FROM products ORDER BY id DESC LIMIT 500) p`
const mappingsSQL = `SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id DESC),'[]') FROM
 (SELECT m.id,m.product_id AS "productId",p.name AS "productName",m.source,m.domain,m.source_key AS "sourceKey"
 FROM source_mappings m JOIN products p ON p.id=m.product_id ORDER BY m.id DESC LIMIT 500) p`
const periodsSQL = `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id DESC),'[]') FROM
 (SELECT p.id,p.product_id AS "productId",pro.name AS "productName",p.name,p.ordinal,
 p.starts_on AS "startsOn",p.ends_on AS "endsOn",rule.version AS "ruleVersion",rule.label AS "ruleLabel"
 FROM periods p JOIN products pro ON pro.id=p.product_id LEFT JOIN LATERAL (
  SELECT rv.version,rv.label FROM rule_versions rv WHERE rv.product_id=p.product_id AND
  (rv.period_id=p.id OR (rv.period_id IS NULL AND (rv.from_ordinal IS NULL OR rv.from_ordinal<=p.ordinal)))
  ORDER BY (rv.period_id IS NOT NULL) DESC,rv.from_ordinal DESC NULLS LAST,rv.version DESC LIMIT 1
 ) rule ON true ORDER BY p.id DESC LIMIT 500) a`
const rulesSQL = `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id DESC),'[]') FROM
 (SELECT rv.id,rv.product_id AS "productId",p.name AS "productName",rv.version,rv.label,
 rv.from_ordinal AS "fromOrdinal",rv.period_id AS "periodId",pr.name AS "periodName",rv.components
 FROM rule_versions rv JOIN products p ON p.id=rv.product_id LEFT JOIN periods pr ON pr.id=rv.period_id ORDER BY rv.id DESC LIMIT 500) a`
const peopleSQL = `SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id DESC),'[]') FROM
 (SELECT id,name FROM people ORDER BY id DESC LIMIT 500) p`
const identitiesSQL = `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id DESC),'[]') FROM
 (SELECT i.id,i.source,i.external_id AS "externalId",p.name AS "personName",
 i.valid_from AS "validFrom",i.valid_to AS "validTo"
 FROM identity_mappings i JOIN people p ON p.id=i.person_id ORDER BY i.id DESC LIMIT 500) a`

func textOK(s string, max int) bool { return strings.TrimSpace(s) != "" && len([]rune(s)) <= max }
func validSource(s string) bool     { return s == "DCAI" || s == "DCZD" || s == "DCJK" }

type createProductInput struct{ Code, Name, PeriodMode string }
type createMappingInput struct {
	ProductID                 int64
	Source, Domain, SourceKey string
}
type createPeriodInput struct {
	ProductID              int64
	Name, StartsOn, EndsOn string
	Ordinal                int
}
type publishRuleInput struct {
	ProductID   int64
	Label       string
	PeriodID    *int64
	FromOrdinal *int
	Components  []component
}
type createPersonInput struct{ Name string }
type assignIdentityInput struct {
	PersonID                      int64
	Source, ExternalID, ValidFrom string
}

func (s workspace) insert(ctx context.Context, stmt string, args ...any) (any, error) {
	var id int64
	err := s.db.QueryRow(ctx, stmt, args...).Scan(&id)
	return map[string]int64{"id": id}, err
}

func (s workspace) createProduct(ctx context.Context, p createProductInput) (any, error) {
	if !textOK(p.Code, 48) || !textOK(p.Name, 80) || (p.PeriodMode != "class" && p.PeriodMode != "calendar") {
		return nil, invalid
	}
	return s.insert(ctx, "INSERT INTO products(code,name,period_mode) VALUES($1,$2,$3) RETURNING id", strings.ToUpper(strings.TrimSpace(p.Code)), strings.TrimSpace(p.Name), p.PeriodMode)
}
func (s workspace) createMapping(ctx context.Context, p createMappingInput) (any, error) {
	if p.ProductID < 1 || !validSource(p.Source) || !textOK(p.Domain, 80) || !textOK(p.SourceKey, 128) {
		return nil, invalid
	}
	return s.insert(ctx, "INSERT INTO source_mappings(product_id,source,domain,source_key) VALUES($1,$2,$3,$4) RETURNING id", p.ProductID, p.Source, strings.TrimSpace(p.Domain), strings.TrimSpace(p.SourceKey))
}
func (s workspace) createPeriod(ctx context.Context, p createPeriodInput) (any, error) {
	from, e1 := time.Parse(time.DateOnly, p.StartsOn)
	to, e2 := time.Parse(time.DateOnly, p.EndsOn)
	if p.ProductID < 1 || !textOK(p.Name, 80) || p.Ordinal < 1 || e1 != nil || e2 != nil || to.Before(from) {
		return nil, invalid
	}
	return s.insert(ctx, "INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on) VALUES($1,$2,$3,$4,$5) RETURNING id", p.ProductID, strings.TrimSpace(p.Name), p.Ordinal, from, to)
}
func (s workspace) publishRule(ctx context.Context, p publishRuleInput) (any, error) {
	if p.ProductID < 1 || !textOK(p.Label, 80) || (p.PeriodID != nil && *p.PeriodID < 1) || (p.FromOrdinal != nil && *p.FromOrdinal < 1) || (p.PeriodID != nil && p.FromOrdinal != nil) || validateComponents(p.Components) != nil {
		return nil, invalid
	}
	raw, err := json.Marshal(p.Components)
	if err != nil {
		return nil, err
	}
	var version int
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var product int64
		if e := tx.QueryRow(ctx, "SELECT id FROM products WHERE id=$1 FOR UPDATE", p.ProductID).Scan(&product); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `INSERT INTO rule_versions(product_id,version,label,period_id,from_ordinal,components)
   SELECT $1,coalesce(max(version),0)+1,$2,$3,$4,$5 FROM rule_versions WHERE product_id=$1 RETURNING version`,
			p.ProductID, strings.TrimSpace(p.Label), p.PeriodID, p.FromOrdinal, raw).Scan(&version)
	})
	return map[string]int{"version": version}, err
}
func (s workspace) createPerson(ctx context.Context, p createPersonInput) (any, error) {
	if !textOK(p.Name, 80) {
		return nil, invalid
	}
	return s.insert(ctx, "INSERT INTO people(name) VALUES($1) RETURNING id", strings.TrimSpace(p.Name))
}
func (s workspace) assignIdentity(ctx context.Context, p assignIdentityInput) (any, error) {
	start, err := time.Parse(time.RFC3339, p.ValidFrom)
	if err != nil || p.PersonID < 1 || !validSource(p.Source) || !textOK(p.ExternalID, 128) {
		return nil, invalid
	}
	external := strings.TrimSpace(p.ExternalID)
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", p.Source+":"+external); e != nil {
			return e
		}
		var previousAt time.Time
		var previousID int64
		var currentPerson int64
		e := tx.QueryRow(ctx, `SELECT id,person_id,valid_from FROM identity_mappings WHERE source=$1 AND external_id=$2 AND valid_to IS NULL`, p.Source, external).Scan(&previousID, &currentPerson, &previousAt)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if e == nil {
			if currentPerson == p.PersonID && previousAt.Equal(start) {
				id = previousID
				return nil
			}
			if !start.After(previousAt) {
				return conflict
			}
			if _, e = tx.Exec(ctx, "UPDATE identity_mappings SET valid_to=$1 WHERE id=$2", start, previousID); e != nil {
				return e
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES($1,$2,$3,$4) RETURNING id`, p.PersonID, p.Source, external, start).Scan(&id)
	})
	return map[string]int64{"id": id}, err
}

func startHTTP(ctx context.Context, cfg config, db *pgxpool.Pool) error {
	host, portText, err := net.SplitHostPort(cfg.addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		return errors.New("development HTTP address must use loopback")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid port")
	}
	if !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return errors.New("development database name must end with _dev")
	}
	if err = migrate(ctx, db); err != nil {
		return err
	}
	server, err := rest.NewServer(rest.RestConf{Host: host, Port: port, MaxConns: 64, MaxBytes: 1 << 20, Timeout: 15000, Middlewares: rest.MiddlewaresConf{Recover: true, Timeout: true, MaxBytes: true}})
	if err != nil {
		return err
	}
	defer server.Stop()
	health := newHandler(db.Ping)
	api := workspace{db: db, sources: readSources()}
	server.AddRoutes(append([]rest.Route{{Method: http.MethodGet, Path: "/healthz", Handler: health.ServeHTTP}, {Method: http.MethodGet, Path: "/readyz", Handler: health.ServeHTTP}}, api.routes()...))
	server.Start()
	return nil
}
