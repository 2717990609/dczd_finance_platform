package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

const organizationCompaniesSQL = `WITH matched AS (
 SELECT u.id,u.name,u.sales_company_type AS "companyType",u.status,coalesce(wireParent.name,parent.name) AS "parentName",
  c.name AS "categoryName",c.id AS "categoryId",h.valid_from AS "categoryFrom"
 FROM organization_units u LEFT JOIN organization_units parent ON parent.id=u.parent_id
 LEFT JOIN organization_dispatch_company_keys k ON k.unit_id=u.id
 LEFT JOIN organization_dispatch_company_keys kp ON kp.wire_id=k.parent_wire_id
 LEFT JOIN organization_units wireParent ON wireParent.id=kp.unit_id
 LEFT JOIN LATERAL (
  SELECT h.category_id,h.valid_from FROM organization_sales_category_history h
  WHERE h.unit_id=u.id AND h.valid_from<=statement_timestamp() AND (h.valid_to IS NULL OR h.valid_to>statement_timestamp())
  ORDER BY h.valid_from DESC LIMIT 1
 ) h ON true LEFT JOIN organization_sales_categories c ON c.id=h.category_id
 WHERE u.sales_company_type IS NOT NULL AND ($1='' OR u.sales_company_type=$1)
 AND ($5='' OR u.status=$5) AND ($2::uuid IS NULL OR c.id=$2) AND ($3='' OR strpos(lower(u.name),lower($3))>0)
 ), page AS (SELECT * FROM matched ORDER BY name,id LIMIT 25 OFFSET $4),
 enriched AS (SELECT page.*,m.owners,m.members FROM page CROSS JOIN LATERAL (
 SELECT count(DISTINCT a.person_id) AS members,string_agg(DISTINCT p.name,'、' ORDER BY p.name) FILTER(WHERE a.kind='OWNER') AS owners
 FROM organization_assignments a JOIN people p ON p.id=a.person_id WHERE a.unit_id=page.id AND a.kind IN ('OWNER','MANAGER','MEMBER')
 AND tstzrange(a.valid_from,a.valid_to,'[)') @> statement_timestamp()) m)
 SELECT jsonb_build_object(
 'total',(SELECT count(*) FROM matched),
 'items',coalesce((SELECT jsonb_agg(to_jsonb(enriched) ORDER BY name,id) FROM enriched),'[]'),
 'categories',coalesce((SELECT jsonb_agg(jsonb_build_object('value',id,'label',name) ORDER BY name,id)
 FROM organization_sales_categories WHERE active),'[]'))`

func (s workspace) organizationCompanies(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	companyType := params.Get("type")
	switch companyType {
	case "", "INTERNAL", "EXTERNAL", "UNKNOWN":
	default:
		respond(w, nil, invalid)
		return
	}
	category, err := parseOrganizationScope(params.Get("categoryId"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	offset, _, name, err := organizationAssignmentFilters(params)
	status := params.Get("status")
	if err != nil || len(name) > 240 || status != "" && status != "ACTIVE" && status != "INACTIVE" {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), organizationCompaniesSQL, companyType, category, name, offset, status).Scan(&result)
	respond(w, result, err)
}

func (s workspace) organizationCompanyCategoryHistory(w http.ResponseWriter, r *http.Request) {
	unit, ok := canonicalSourceUUID(strings.TrimSpace(r.URL.Query().Get("unitId")))
	if !ok {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err := s.db.QueryRow(r.Context(), `SELECT coalesce(jsonb_agg(jsonb_build_object('categoryName',coalesce(c.name,''),
	 'validFrom',h.valid_from,'validTo',h.valid_to,'locallyAssigned',h.recorded_at IS NOT NULL) ORDER BY h.valid_from DESC),'[]')
	 FROM organization_sales_category_history h LEFT JOIN organization_sales_categories c ON c.id=h.category_id WHERE h.unit_id=$1`, unit).Scan(&result)
	respond(w, result, err)
}

func importOrganizationSalesCategories(ctx context.Context, tx pgx.Tx, snapshot organizationSnapshot) error {
	if len(snapshot.SalesCategories) != 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO organization_sales_categories(id,name,active)
		 SELECT id,name,active FROM jsonb_to_recordset($1) AS i(id uuid,name text,active boolean)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name,active=excluded.active,version=organization_sales_categories.version+1
		 WHERE organization_sales_categories.recorded_at IS NULL
		  AND (organization_sales_categories.name,organization_sales_categories.active) IS DISTINCT FROM (excluded.name,excluded.active)`, snapshot.SalesCategories); err != nil {
			return err
		}
	}
	if len(snapshot.SalesCategoryHistory) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from,valid_to)
	 SELECT u.id,i."categoryId",i."from",least(i."to", managed.starts_at)
	 FROM jsonb_to_recordset($1) AS i("unitId" uuid,"categoryId" uuid,"from" timestamptz,"to" timestamptz)
	 LEFT JOIN organization_units u ON u.id=i."unitId" AND u.sales_company_type IS NOT NULL
	 LEFT JOIN LATERAL (SELECT min(valid_from) AS starts_at FROM organization_sales_category_history h
	  WHERE h.unit_id=u.id AND h.recorded_at IS NOT NULL) managed ON true
	 WHERE managed.starts_at IS NULL OR i."from"<managed.starts_at
	 ON CONFLICT(unit_id,valid_from) DO UPDATE SET category_id=excluded.category_id,valid_to=excluded.valid_to`, snapshot.SalesCategoryHistory); err != nil {
		return err
	}
	var overlapping bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM (
	 SELECT valid_to,lead(valid_from) OVER (PARTITION BY unit_id ORDER BY valid_from) AS next_start
	 FROM organization_sales_category_history) ranges WHERE next_start IS NOT NULL AND (valid_to IS NULL OR valid_to>next_start))`).Scan(&overlapping)
	if err == nil && overlapping {
		return invalid
	}
	return err
}
