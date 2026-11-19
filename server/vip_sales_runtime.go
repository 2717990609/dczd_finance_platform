package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type catalogPaymentMatch struct {
	SourceVersionID       int64     `json:"sourceVersionId"`
	OrderID               string    `json:"orderId"`
	OrderNo               string    `json:"orderNo"`
	PaidAt                time.Time `json:"paidAt"`
	PaidMinor             *int64    `json:"paidMinor"`
	SourceTransactionType string    `json:"sourceTransactionType"`
	Scenario              string    `json:"scenario"`
	ScenarioLabel         string    `json:"scenarioLabel"`
	MappingVersion        int       `json:"mappingVersion"`
	RuleID                string    `json:"ruleId"`
	RuleVersion           int       `json:"ruleVersion"`
	RuleLabel             string    `json:"ruleLabel"`
	Status                string    `json:"status"`
	FixedParts            []struct {
		ItemKey     string `json:"itemKey"`
		Label       string `json:"label"`
		AmountMinor int64  `json:"amountMinor"`
	} `json:"fixedParts"`
	SalesStatus string         `json:"salesStatus,omitempty"`
	SalesParts  []vipSalesPart `json:"salesParts,omitempty"`
}

type catalogMatchReport struct {
	OrderCount   int                   `json:"orderCount"`
	MatchedCount int                   `json:"matchedCount"`
	MatchedMinor int64                 `json:"matchedMinor"`
	PaidMinor    int64                 `json:"paidMinor"`
	Items        []catalogPaymentMatch `json:"items"`
}

type vipLink struct {
	Key, Value string
	vipInterval
}

type vipRuntimeData struct {
	Product, Project             string
	Claims                       map[int64][]vipClaim
	Definitions                  map[string]json.RawMessage
	Users, Companies, Categories []vipLink
	vipSalesData
}

const vipRuntimeSQL = `WITH scope AS (
 SELECT c.captured_at,pro.code,cfg.project_code FROM periods p JOIN products pro ON pro.id=p.product_id
 JOIN legacy_catalog_products cfg ON cfg.product_id=pro.id
 LEFT JOIN catalog_sales_context c ON c.source=cfg.source_system
 WHERE p.id=$1 AND cfg.source_system='DCAI'
), claims AS MATERIALIZED (
 SELECT source_version_id,sales_claims FROM source_order_classifications
 WHERE source_version_id=ANY($2::bigint[]) AND jsonb_typeof(sales_claims)='array'
), keys AS MATERIALIZED (
 SELECT DISTINCT line->>'user' AS person,line->>'company' AS company
 FROM claims CROSS JOIN LATERAL jsonb_array_elements(sales_claims) line
), users AS MATERIALIZED (
 SELECT u.external_id AS key,p.legacy_party_id AS value,p.id,u.valid_from AS "from",u.valid_to AS "to"
 FROM catalog_sales_people_sources u JOIN scope s ON u.source='DCAI' AND s.captured_at=u.observed_at
 JOIN people p ON p.id=u.person_id WHERE u.external_id IN (SELECT person FROM keys)
), companies AS MATERIALIZED (
 SELECT a.external_id AS key,a.company_uuid AS value,a.valid_from AS "from",a.valid_to AS "to"
 FROM catalog_sales_company_aliases a JOIN scope s ON a.source='DCAI' AND a.observed_at=s.captured_at
 WHERE a.external_id IN (SELECT company FROM keys)
), roles AS (
 SELECT p.legacy_party_id AS person,a.role,a.scope,a.scope_key AS key,a.valid_from AS "from",a.valid_to AS "to"
 FROM catalog_sales_appointments a JOIN scope s ON a.source='DCAI' AND a.observed_at=s.captured_at
 JOIN people p ON p.id=a.person_id
 WHERE a.scope='ORGANIZATION_UNIT' OR a.person_id IN (SELECT id FROM users) AND (
  a.scope='PRODUCT' AND a.scope_key=s.code OR a.scope='PROJECT' AND a.scope_key=s.project_code)
), definitions AS MATERIALIZED (
 SELECT v.source_id,v.definition FROM unnest($3::text[]) r(id) JOIN legacy_rule_versions v ON v.source_id=r.id::uuid
)
SELECT jsonb_build_object('product',s.code,'project',s.project_code,
 'claims',(SELECT jsonb_object_agg(source_version_id::text,sales_claims) FROM claims),
 'definitions',(SELECT jsonb_object_agg(source_id::text,jsonb_build_object(
  'amount_basis',definition->'amount_basis','stacking_policy',definition->'stacking_policy','rounding_policy',definition->'rounding_policy',
  'internal_sales_direct_split',definition->'internal_sales_direct_split',
  'benefit_actions',(SELECT jsonb_agg(a) FROM jsonb_array_elements(CASE WHEN jsonb_typeof(definition->'benefit_actions')='array'
   THEN definition->'benefit_actions' ELSE '[]'::jsonb END) a
   WHERE a->>'item_key'=definition->'internal_sales_direct_split'->>'sales_item_key'))) FROM definitions),
 'users',(SELECT jsonb_agg(to_jsonb(users)-'id') FROM users),
 'companies',(SELECT jsonb_agg(to_jsonb(companies)) FROM companies),
 'categories',(SELECT jsonb_agg(jsonb_build_object('key',h.company_uuid,'value',h.category_uuid,'from',h.valid_from,'to',h.valid_to))
  FROM catalog_sales_categories h WHERE h.source='DCAI' AND h.observed_at=s.captured_at AND h.company_uuid IN (SELECT value FROM companies)),
 'roles',(SELECT jsonb_agg(to_jsonb(roles)) FROM roles),
 'memberships',(SELECT jsonb_agg(jsonb_build_object('person',p.legacy_party_id,'unit',a.unit_id,'from',a.valid_from,'to',a.valid_to))
  FROM organization_assignments a JOIN people p ON p.id=a.person_id WHERE p.id IN (SELECT id FROM users)),
 'people',(SELECT jsonb_object_agg(p.legacy_party_id::text,p.name) FROM people p WHERE p.legacy_party_id IS NOT NULL))
FROM scope s`

func loadVIPRuntimeData(ctx context.Context, tx pgx.Tx, periodID int64, matched []catalogPaymentMatch) (vipRuntimeData, error) {
	ids := make([]int64, 0, len(matched))
	var rules []string
	seen := make(map[string]bool)
	for _, item := range matched {
		if item.Status != "MATCHED" || item.RuleID == "" {
			continue
		}
		ids = append(ids, item.SourceVersionID)
		if !seen[item.RuleID] {
			seen[item.RuleID] = true
			rules = append(rules, item.RuleID)
		}
	}
	if len(ids) == 0 {
		return vipRuntimeData{}, nil
	}
	var raw json.RawMessage
	if err := tx.QueryRow(ctx, vipRuntimeSQL, periodID, ids, rules).Scan(&raw); err != nil {
		if err == pgx.ErrNoRows {
			return vipRuntimeData{}, nil
		}
		return vipRuntimeData{}, err
	}
	var data vipRuntimeData
	err := json.Unmarshal(raw, &data)
	return data, err
}

func resolveVIPLink(values []vipLink, key string, at time.Time) (value string, conflict bool) {
	found := false
	for _, row := range values {
		if row.Key != key || !row.contains(at) {
			continue
		}
		if found && value != row.Value {
			return "", true
		}
		value, found = row.Value, true
	}
	return value, false
}

func quoteVIPPaid(sale catalogPaymentMatch, policy vipSalesPolicy, d vipRuntimeData) ([]vipSalesPart, string) {
	src, ok := d.Claims[sale.SourceVersionID]
	if !ok {
		return nil, "SALES_SOURCE_MISSING"
	}
	claim, issue := vipSellerClaim(src)
	if issue != "" {
		return nil, issue
	}
	seller, conflict := resolveVIPLink(d.Users, claim.User, sale.PaidAt)
	if conflict {
		return nil, "SALES_PERSON_CONFLICT"
	}
	company, conflict := resolveVIPLink(d.Companies, claim.Company, sale.PaidAt)
	if conflict {
		return nil, "SALES_COMPANY_CONFLICT"
	}
	category, conflict := resolveVIPLink(d.Categories, company, sale.PaidAt)
	if conflict {
		return nil, "SALES_CATEGORY_CONFLICT"
	}
	return calculateVIPSales(policy, vipSalesRequest{
		Order: sale.OrderID, At: sale.PaidAt, Paid: *sale.PaidMinor, Product: d.Product, Project: d.Project,
		Seller: seller, Company: company, Relationship: claim.Relationship, Category: category,
	}, d.vipSalesData)
}

func applyVIPSalesQuotes(items []catalogPaymentMatch, data vipRuntimeData) {
	policies := make(map[string]vipSalesPolicy, len(data.Definitions))
	for id, definition := range data.Definitions {
		if policy, ok := readVIPSalesPolicy(definition); ok {
			policies[id] = policy
		}
	}
	for i := range items {
		row := &items[i]
		policy, ok := policies[row.RuleID]
		if !ok || row.Status != "MATCHED" || row.PaidMinor == nil {
			continue
		}
		row.SalesParts, row.SalesStatus = quoteVIPPaid(*row, policy, data)
		if row.SalesStatus == "" {
			row.SalesStatus = "CALCULATED"
		}
	}
}
