package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

const periodPageSQL = `WITH candidates AS MATERIALIZED (
 SELECT p.* FROM periods p JOIN products pro ON pro.id=p.product_id
 WHERE pro.code` + activeFinanceProductCodeSQL + ` AND ($1::bigint[] IS NULL OR p.product_id=ANY($1))
  AND ($2::bigint=0 OR p.product_id=$2)
  AND ($3::bigint=0 OR p.id<$3)
  AND ($4='' OR strpos(lower(concat_ws(' ',p.name,pro.name,p.ordinal::text,p.starts_on::text,p.ends_on::text,
   p.source_period_key,to_char(` + periodReportMonthSQL + `,'YYYY-MM'))),lower($4))>0)
 ORDER BY p.id DESC LIMIT ($5::int+1)
), page AS (
 ` + periodListColumnsSQL + ` FROM (SELECT * FROM candidates ORDER BY id DESC LIMIT $5) p ` + periodListJoinsSQL + `
)
SELECT jsonb_build_object('items',coalesce(jsonb_agg(to_jsonb(page) ORDER BY page.id DESC),'[]'),
 'nextBeforeId',CASE WHEN (SELECT count(*) FROM candidates)>$5 THEN min(page.id) ELSE NULL END)
FROM page`

func (s workspace) periodPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	product, productOK := optionalPositiveInteger(q.Get("productId"), 0)
	before, beforeOK := optionalPositiveInteger(q.Get("beforeId"), 0)
	limit, limitOK := optionalPositiveInteger(q.Get("limit"), 50)
	search := strings.TrimSpace(q.Get("q"))
	if !productOK || !beforeOK || !limitOK || limit > 100 || len(search) > 256 {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	ctx := r.Context()
	err := s.db.QueryRow(ctx, periodPageSQL, authorizedProductScope(ctx, true), product, before, search, limit).Scan(&result)
	respond(w, result, err)
}

func optionalPositiveInteger(raw string, fallback int64) (int64, bool) {
	if raw == "" {
		return fallback, true
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value > 0
}

const workflowPeriodsSQL = `WITH candidates AS MATERIALIZED (
 SELECT p.* FROM periods p
 WHERE ($1::bigint[] IS NULL OR p.product_id=ANY($1))
  AND ($2::bigint=0 OR p.product_id=$2)
  AND ($3::date IS NULL OR ` + periodReportMonthSQL + `=$3::date)
  AND ($4::bigint=0 OR p.id=$4)
)
SELECT coalesce(jsonb_agg(to_jsonb(selected) ORDER BY selected.id DESC),'[]') FROM (
 ` + periodListColumnsSQL + ` FROM candidates p ` + periodListJoinsSQL + `
 WHERE pro.code` + activeFinanceProductCodeSQL + `
) selected`

func (s workspace) periods(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	product, productOK := optionalPositiveInteger(q.Get("productId"), 0)
	id, idOK := optionalPositiveInteger(q.Get("periodId"), 0)
	if !productOK || !idOK {
		respond(w, nil, invalid)
		return
	}
	var month any
	if raw := q.Get("month"); raw != "" {
		date, err := parseReportMonth(raw)
		if err != nil {
			respond(w, nil, err)
			return
		}
		month = date
	}
	if product == 0 && id == 0 && month == nil {
		s.listForAuthorizedProducts(periodsSQL, true)(w, r)
		return
	}
	ctx := r.Context()
	var result json.RawMessage
	err := s.db.QueryRow(ctx, workflowPeriodsSQL, authorizedProductScope(ctx, true), product, month, id).Scan(&result)
	respond(w, result, err)
}
