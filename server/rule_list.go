package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

const ruleListSQL = `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY a.id DESC),'[]') FROM (
 SELECT ` + ruleVersionColumnsSQL + `,jsonb_array_length(rv.components) AS "componentCount",
 array_remove(ARRAY[CASE WHEN members.new_member THEN 'new' END,
   CASE WHEN members.retrain_member THEN 'retrain' END],NULL) AS "classMembers"` + ruleVersionTablesSQL + `
 LEFT JOIN LATERAL (
  SELECT bool_or(coalesce(part->>'classMember','') IN ('','new')) AS new_member,
   bool_or(coalesce(part->>'classMember','') IN ('','retrain') OR
    part->>'mode'='tiered' AND part#>'{tiers,includeRetrain}'='true'::jsonb) AS retrain_member
  FROM jsonb_array_elements(rv.components) AS parts(part)
  WHERE p.period_mode='class'
 ) members ON true
 WHERE ($1::bigint[] IS NULL OR rv.product_id=ANY($1)) ORDER BY rv.id DESC LIMIT 500
) a`

func (s workspace) getComponentRules(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	switch query.Get("view") {
	case "summary":
		s.listForAuthorizedProducts(ruleListSQL, false)(w, r)
	case "detail":
		id, err := strconv.ParseInt(query.Get("id"), 10, 64)
		if err != nil || id <= 0 {
			respond(w, nil, invalid)
			return
		}
		var result json.RawMessage
		err = s.db.QueryRow(r.Context(), `SELECT to_jsonb(detail) FROM (`+ruleVersionRowsSQL+`
		 WHERE rv.id=$1 AND ($2::bigint[] IS NULL OR rv.product_id=ANY($2))) detail`,
			id, authorizedProductScope(r.Context(), false)).Scan(&result)
		respond(w, result, err)
	default:
		s.listForAuthorizedProducts(rulesSQL, false)(w, r)
	}
}
