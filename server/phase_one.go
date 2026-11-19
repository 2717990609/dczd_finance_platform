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
	Type         string              `json:"type"`
	Mode         string              `json:"mode"`
	Value        int64               `json:"value"`
	SourceRole   string              `json:"sourceRole,omitempty"`
	RefundPolicy string              `json:"refundPolicy,omitempty"`
	ClassMember  string              `json:"classMember,omitempty"`
	Tiers        *practiceSalesTiers `json:"tiers,omitempty"`
}

var errPracticeTierCompany = errors.New("公司阶梯引用的销售公司未配置或已停用")

var errPracticeCountPerson = errors.New("个人累计起点引用的人员不存在")

var invalid = errors.New("请核对名称、范围与必填配置")
var conflict = errors.New("记录与现有配置冲突，请重新读取")

func validateComponents(items []component) error {
	if len(items) == 0 || len(items) > 8 {
		return invalid
	}
	seen := make(map[string]uint8)
	for _, c := range items {
		switch c.Type {
		case "sales", "referral", "manager", "delivery":
		default:
			return invalid
		}
		var members uint8
		switch c.ClassMember {
		case "":
			members = 3
		case "new":
			members = 1
		case "retrain":
			members = 2
		default:
			return invalid
		}
		if seen[c.Type]&members != 0 || c.Value < 0 || (c.SourceRole != "" && !validSourceRole(c.SourceRole)) || !validRefundPolicy(c.RefundPolicy) {
			return invalid
		}
		seen[c.Type] |= members
		if c.Mode != "tiered" && c.Tiers != nil {
			return invalid
		}
		switch c.Mode {
		case "tiered":
			if !validPracticeSalesTiers(c) {
				return invalid
			}
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
	db       *pgxpool.Pool
	sources  map[string]sourceConfig
	ruleAuth catalogRuleAuth
}

func (s workspace) routes() []rest.Route {
	return []rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/configuration-options", Handler: s.list(configurationOptionsSQL)},
		{Method: http.MethodGet, Path: "/api/v1/products", Handler: s.list(productsSQL)},
		{Method: http.MethodPost, Path: "/api/v1/products", Handler: submit(s.createProduct)},
		{Method: http.MethodGet, Path: "/api/v1/mappings", Handler: s.list(mappingsSQL)},
		{Method: http.MethodPost, Path: "/api/v1/mappings", Handler: submit(s.createMapping)},
		{Method: http.MethodGet, Path: "/api/v1/periods", Handler: s.list(periodsSQL)},
		{Method: http.MethodPost, Path: "/api/v1/periods", Handler: submit(s.createPeriod)},
		{Method: http.MethodGet, Path: "/api/v1/rules", Handler: s.list(rulesSQL)},
		{Method: http.MethodGet, Path: "/api/v1/rule-catalog", Handler: s.list(catalogRulesSQL)},
		{Method: http.MethodGet, Path: "/api/v1/catalog-rule-rates", Handler: s.getCatalogRuleRates},
		{Method: http.MethodPost, Path: "/api/v1/catalog-rule-rates", Handler: s.postCatalogRuleRates},
		{Method: http.MethodGet, Path: "/api/v1/practice-count-openings", Handler: s.list(practiceCountOpeningsSQL)},
		{Method: http.MethodPost, Path: "/api/v1/rules", Handler: submit(s.publishRule)},
		{Method: http.MethodGet, Path: "/api/v1/organization-units", Handler: s.list(organizationUnitsSQL)},
		{Method: http.MethodGet, Path: "/api/v1/organization-assignments", Handler: s.organizationAssignments},
		{Method: http.MethodGet, Path: "/api/v1/people", Handler: s.list(peopleSQL)},
		{Method: http.MethodPost, Path: "/api/v1/people", Handler: submit(s.createPerson)},
		{Method: http.MethodGet, Path: "/api/v1/identities", Handler: s.list(identitiesSQL)},
		{Method: http.MethodPost, Path: "/api/v1/identities", Handler: submit(s.assignIdentity)},
		{Method: http.MethodGet, Path: "/api/v1/orders", Handler: s.orders},
		{Method: http.MethodPost, Path: "/api/v1/orders/pull", Handler: submit(s.pullOrder)},
		{Method: http.MethodGet, Path: "/api/v1/orders/sync", Handler: s.getDCAISyncJobs},
		{Method: http.MethodPost, Path: "/api/v1/orders/sync", Handler: submit(s.requestDCAISync)},
		{Method: http.MethodGet, Path: "/api/v1/sources", Handler: s.sourceStatuses},
		{Method: http.MethodGet, Path: "/api/v1/drafts", Handler: s.getDraft},
		{Method: http.MethodGet, Path: "/api/v1/drafts/status", Handler: s.draftRefreshStatus},
		{Method: http.MethodGet, Path: "/api/v1/draft-personnel", Handler: s.draftPersonnel},
		{Method: http.MethodGet, Path: "/api/v1/draft-report", Handler: s.draftReport},
		{Method: http.MethodGet, Path: "/api/v1/period-rule-matches", Handler: s.catalogMatches},
		{Method: http.MethodGet, Path: "/api/v1/period-pool-contributions", Handler: s.periodPoolContributions},
		{Method: http.MethodGet, Path: "/api/v1/draft-pools", Handler: s.draftPools},
		{Method: http.MethodGet, Path: "/api/v1/settlement-review", Handler: s.getSettlementReview},
		{Method: http.MethodPost, Path: "/api/v1/draft-pool-allocations", Handler: submit(s.savePoolAllocation)},
		{Method: http.MethodPost, Path: "/api/v1/drafts", Handler: submit(s.recalculateDraft)},
	}
}

func submit[T any](apply func(context.Context, T) (any, error)) http.HandlerFunc {
	return submitJSON(32<<10, apply)
}

func submitJSON[T, R any](size int64, apply func(context.Context, T) (R, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input T
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			respond(w, nil, invalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, size)
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
	case errors.Is(err, errRuleLogin):
		code, message = http.StatusUnauthorized, err.Error()
	case errors.Is(err, errRulePermission):
		code, message = http.StatusForbidden, err.Error()
	case errors.Is(err, invalid), errors.Is(err, errPracticeCountPerson), errors.Is(err, errPracticeOpeningScope), errors.Is(err, errPracticeTierCompany):
		code, message = 400, err.Error()
	case errors.Is(err, errLegacyRuleNotAdapted), errors.Is(err, errDraftRule), errors.Is(err, errDraftLimit), errors.Is(err, errPracticeNotReady), errors.Is(err, errPracticeStart), errors.Is(err, errPracticeSettlementUnavailable),
		errors.Is(err, errCatalogRateStale), errors.Is(err, errCatalogRateUnavailable), errors.Is(err, errCatalogRateDates), errors.Is(err, errCatalogTeamRate),
		errors.Is(err, errSettlementClosed), errors.Is(err, errSettlementStale), errors.Is(err, errSettlementPending),
		errors.Is(err, errDraftChanged),
		errors.Is(err, errSettlementPools), errors.Is(err, errSettlementPoolHistory),
		errors.Is(err, errSettlementSourceCoverage), errors.Is(err, errSettlementSourceRefund),
		errors.Is(err, errSyncOrigin), errors.Is(err, errDCAIUnverified), errors.Is(err, errDCAIFileImport),
		errors.Is(err, errSyncInitialFrom), errors.Is(err, errSyncLaterStart), errors.Is(err, errSyncInProgress),
		errors.Is(err, errSettlementOpen), errors.Is(err, errSettlementDisabled), errors.Is(err, errPoolStale),
		errors.Is(err, errPoolAmount), errors.Is(err, errPoolEligibility):
		code, message = http.StatusConflict, err.Error()
	case errors.Is(err, errCalculationOverflow):
		code, message = http.StatusConflict, "试算金额超出范围，请核对原单和规则"
	case errors.Is(err, conflict):
		code, message = 409, err.Error()
	case errors.Is(err, pgx.ErrNoRows):
		code, message = 404, "找不到对应记录"
	case errors.Is(err, errSourceDisabled):
		code, message = 409, "来源接口尚未配置"
	case errors.Is(err, errSourceRead):
		code, message = 502, "来源读取失败，请核对接口或订单号"
	case errors.As(err, &dbErr) && (dbErr.Code == "23505" || dbErr.Code == "40001"):
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
 (SELECT p.id,p.code,p.name,p.period_mode AS "periodMode",p.cycle_start_day AS "cycleStartDay",
 (l.product_id IS NOT NULL) AS "catalogPending"
 FROM products p LEFT JOIN legacy_catalog_products l ON l.product_id=p.id ORDER BY p.id DESC LIMIT 500) p`
const mappingsSQL = `SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id DESC),'[]') FROM
 (SELECT m.id,m.product_id AS "productId",p.name AS "productName",m.source,m.domain,m.source_key AS "sourceKey"
 FROM source_mappings m JOIN products p ON p.id=m.product_id ORDER BY m.id DESC LIMIT 500) p`
const periodsSQL = `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id DESC),'[]') FROM
 (SELECT p.id,p.product_id AS "productId",pro.name AS "productName",pro.period_mode AS "periodMode",p.name,p.ordinal,
 EXISTS(SELECT 1 FROM catalog_business_routes m JOIN legacy_catalog_products c USING(product_id)
  WHERE m.product_id=p.product_id AND m.observed_at=c.captured_at) AS "ruleMatchingAvailable",
 ` + catalogDraftAvailableSQL + ` AS "catalogDraftAvailable",
 p.starts_on AS "startsOn",p.ends_on AS "endsOn",to_char(p.calendar_month,'YYYY-MM') AS "settlementMonth",p.source_period_key AS "sourcePeriodKey",roster.starts_at AS "classStartsAt",rule.version AS "ruleVersion",rule.label AS "ruleLabel"
 FROM periods p JOIN products pro ON pro.id=p.product_id
 LEFT JOIN LATERAL (
  SELECT r.starts_at FROM practice_rosters r WHERE r.period_key=p.source_period_key ORDER BY r.source_version DESC LIMIT 1
 ) roster ON pro.period_mode='class'
 LEFT JOIN LATERAL (
  SELECT rv.version,rv.label FROM rule_versions rv WHERE ` + matchingRuleScope + `
  AND CASE WHEN pro.period_mode='class'
    THEN roster.starts_at IS NOT NULL AND (rv.effective_from IS NULL OR rv.effective_from<=roster.starts_at)
    ELSE rv.effective_from IS NULL OR rv.effective_from < ((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai') END
  ORDER BY ` + matchingRuleOrder + ` LIMIT 1
 ) rule ON true ORDER BY p.id DESC LIMIT 500) a`
const matchingRuleScope = `rv.product_id=p.product_id AND
  (rv.period_id=p.id OR (rv.period_id IS NULL AND (rv.from_ordinal IS NULL OR rv.from_ordinal<=p.ordinal)))`
const matchingRuleOrder = `(rv.period_id IS NOT NULL) DESC,rv.from_ordinal DESC NULLS LAST,
  rv.effective_from DESC NULLS LAST,rv.version DESC`
const rulesSQL = `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id DESC),'[]') FROM
 (SELECT rv.id,rv.product_id AS "productId",p.name AS "productName",rv.version,rv.label,
 rv.from_ordinal AS "fromOrdinal",rv.period_id AS "periodId",pr.name AS "periodName",rv.effective_from AS "effectiveFrom",rv.components
 FROM rule_versions rv JOIN products p ON p.id=rv.product_id LEFT JOIN periods pr ON pr.id=rv.period_id ORDER BY rv.id DESC LIMIT 500) a`
const peopleSQL = `SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id DESC),'[]') FROM
 (SELECT id,name FROM people) p`
const identitiesSQL = `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id DESC),'[]') FROM
 (SELECT i.id,i.source,i.external_id AS "externalId",p.name AS "personName",
 i.valid_from AS "validFrom",i.valid_to AS "validTo"
 FROM identity_mappings i JOIN people p ON p.id=i.person_id ORDER BY i.id DESC LIMIT 500) a`

func textOK(s string, max int) bool { return strings.TrimSpace(s) != "" && len([]rune(s)) <= max }
func validSourceRole(role string) bool {
	if len(role) == 0 || len(role) > 64 {
		return false
	}
	for _, ch := range role {
		if (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '_' {
			return false
		}
	}
	return true
}

func validSource(s string) bool { return s == "DCAI" || s == "DCZD" || s == "DCJK" }

type createProductInput struct {
	Code, Name, PeriodMode string
	CycleStartDay          *int
}
type createMappingInput struct {
	ProductID                 int64
	Source, Domain, SourceKey string
}
type createPeriodInput struct {
	ProductID              int64
	Name, StartsOn, EndsOn string
	SettlementMonth        string
	SourcePeriodKey        string
	Ordinal                int
}
type publishRuleInput struct {
	ProductID     int64
	Label         string
	PeriodID      *int64
	FromOrdinal   *int
	EffectiveFrom *time.Time
	Components    []component
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
	if !textOK(p.Code, 48) || !textOK(p.Name, 80) || (p.PeriodMode != "class" && p.PeriodMode != "calendar") ||
		(p.CycleStartDay != nil && (p.PeriodMode != "calendar" || *p.CycleStartDay < 1 || *p.CycleStartDay > 28)) {
		return nil, invalid
	}
	return s.insert(ctx, "INSERT INTO products(code,name,period_mode,cycle_start_day) VALUES($1,$2,$3,$4) RETURNING id", strings.ToUpper(strings.TrimSpace(p.Code)), strings.TrimSpace(p.Name), p.PeriodMode, p.CycleStartDay)
}
func (s workspace) createMapping(ctx context.Context, p createMappingInput) (any, error) {
	if p.ProductID < 1 || !validSource(p.Source) || !textOK(p.Domain, 80) || !textOK(p.SourceKey, 128) {
		return nil, invalid
	}
	var id int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "INSERT INTO source_mappings(product_id,source,domain,source_key) VALUES($1,$2,$3,$4) RETURNING id", p.ProductID, p.Source, strings.TrimSpace(p.Domain), strings.TrimSpace(p.SourceKey)).Scan(&id); err != nil {
			return err
		}
		return enqueueDraftConfiguration(ctx, tx, draftRefreshProduct, p.ProductID)
	})
	return map[string]int64{"id": id}, err
}
func (s workspace) createPeriod(ctx context.Context, p createPeriodInput) (any, error) {
	if p.ProductID < 1 || !textOK(p.Name, 80) {
		return nil, invalid
	}
	var id int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var mode string
		var day *int
		if err := tx.QueryRow(ctx, "SELECT period_mode,cycle_start_day FROM products WHERE id=$1 FOR UPDATE", p.ProductID).Scan(&mode, &day); err != nil {
			return err
		}
		if p.SourcePeriodKey != "" && (mode != "class" || !validPracticeKey(p.SourcePeriodKey)) {
			return invalid
		}
		var month, from, to string
		ordinal := p.Ordinal
		if p.SettlementMonth != "" {
			if day == nil || p.StartsOn != "" || p.EndsOn != "" || p.Ordinal != 0 {
				return invalid
			}
			window, err := calendarWindow(p.SettlementMonth, *day)
			if err != nil {
				return err
			}
			month = p.SettlementMonth + "-01"
			date, err := time.Parse(time.DateOnly, month)
			if err != nil {
				return invalid
			}
			ordinal = date.Year()*100 + int(date.Month())
			from, to = window.Start.Format(time.DateOnly), window.Through.AddDate(0, 0, -1).Format(time.DateOnly)
		} else {
			start, e1 := time.Parse(time.DateOnly, p.StartsOn)
			end, e2 := time.Parse(time.DateOnly, p.EndsOn)
			if day != nil || ordinal < 1 || e1 != nil || e2 != nil || end.Before(start) {
				return invalid
			}
			from, to = p.StartsOn, p.EndsOn
		}
		if mode == "calendar" {
			var overlaps bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM periods WHERE product_id=$1
				AND starts_on<=$3::text::date AND ends_on>=$2::text::date)`, p.ProductID, from, to).Scan(&overlaps)
			if err != nil {
				return err
			}
			if overlaps {
				return conflict
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,calendar_month,source_period_key)
			VALUES($1,$2,$3,$4::text::date,$5::text::date,nullif($6::text,'')::date,nullif($7,'')) RETURNING id`,
			p.ProductID, strings.TrimSpace(p.Name), ordinal, from, to, month, p.SourcePeriodKey).Scan(&id); err != nil {
			return err
		}
		if p.SourcePeriodKey != "" {
			return enqueueDraftConfiguration(ctx, tx, draftRefreshRoster, p.SourcePeriodKey, nil)
		}
		return enqueueDraftConfiguration(ctx, tx, `SELECT id FROM periods WHERE id=$1`, id)
	})
	return map[string]int64{"id": id}, err
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
		var mode string
		var legacy bool
		if e := tx.QueryRow(ctx, `SELECT period_mode,EXISTS(SELECT 1 FROM legacy_catalog_products WHERE product_id=p.id)
		 FROM products p WHERE id=$1 FOR UPDATE`, p.ProductID).Scan(&mode, &legacy); e != nil {
			return e
		}
		if legacy {
			return errLegacyRuleNotAdapted
		}
		if p.EffectiveFrom != nil && p.EffectiveFrom.IsZero() {
			return invalid
		}
		var personIDs []int64
		var companyIDs []string
		for _, c := range p.Components {
			if mode != "class" && c.ClassMember != "" {
				return invalid
			}
			if c.Tiers != nil {
				if c.Tiers.OpeningID != 0 {
					var found bool
					if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM practice_count_openings WHERE id=$1 AND product_id=$2)`,
						c.Tiers.OpeningID, p.ProductID).Scan(&found); err != nil {
						return err
					}
					if !found {
						return errPracticeOpeningScope
					}
				}
				for _, company := range c.Tiers.CompanySchedules {
					companyIDs = append(companyIDs, company.CompanyID)
				}
				for _, start := range c.Tiers.PersonCountStarts {
					personIDs = append(personIDs, start.PersonID)
				}
			}
		}
		if len(companyIDs) != 0 {
			var missing bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unnest($1::text[]) AS company(id)
			 WHERE NOT EXISTS(SELECT 1 FROM configuration_options o
			  WHERE o.group_key='practiceSalesCompany' AND o.option_key=company.id AND o.enabled))`, companyIDs).Scan(&missing); err != nil {
				return err
			}
			if missing {
				return errPracticeTierCompany
			}
		}
		if len(personIDs) != 0 {
			var missing bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unnest($1::bigint[]) AS person(id)
			 WHERE NOT EXISTS(SELECT 1 FROM people WHERE id=person.id))`, personIDs).Scan(&missing); err != nil {
				return err
			}
			if missing {
				return errPracticeCountPerson
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO rule_versions(product_id,version,label,period_id,from_ordinal,effective_from,components)
   SELECT $1,coalesce(max(version),0)+1,$2,$3,$4,$5,$6 FROM rule_versions WHERE product_id=$1 RETURNING version`,
			p.ProductID, strings.TrimSpace(p.Label), p.PeriodID, p.FromOrdinal, p.EffectiveFrom, raw).Scan(&version); err != nil {
			return err
		}
		return enqueueDraftConfiguration(ctx, tx, draftRefreshRule, p.ProductID, p.PeriodID, p.FromOrdinal, p.EffectiveFrom)
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
		if err := tx.QueryRow(ctx, `INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES($1,$2,$3,$4) RETURNING id`, p.PersonID, p.Source, external, start).Scan(&id); err != nil {
			return err
		}
		return enqueueDraftConfiguration(ctx, tx, draftRefreshIdentity, p.Source, start)
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
	api := workspace{db: db, sources: readSources(), ruleAuth: cfg.ruleAuth}
	stopSync := api.startDCAISyncWorkers(ctx, cfg.dcaiSyncInterval)
	defer stopSync()
	stopDraftRefresh := api.startDraftRefreshWorker(ctx, cfg.draftInterval)
	defer stopDraftRefresh()
	server.AddRoutes(append([]rest.Route{{Method: http.MethodGet, Path: "/healthz", Handler: health.ServeHTTP}, {Method: http.MethodGet, Path: "/readyz", Handler: health.ServeHTTP}}, api.routes()...))
	server.Start()
	return nil
}
