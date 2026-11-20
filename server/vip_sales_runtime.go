package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type catalogPaymentMatch struct {
	SourceVersionID       int64      `json:"sourceVersionId"`
	OrderID               string     `json:"orderId"`
	OrderNo               string     `json:"orderNo"`
	PaidAt                time.Time  `json:"paidAt"`
	PaidMinor             *int64     `json:"paidMinor"`
	PaidInWindow          bool       `json:"paidInWindow"`
	CompletionVersionID   int64      `json:"completionVersionId,omitempty"`
	CompletedAt           *time.Time `json:"completedAt,omitempty"`
	CompletionCarried     bool       `json:"completionCarried,omitempty"`
	RecognizedMinor       *int64     `json:"recognizedMinor,omitempty"`
	PriorRefundVersionID  int64      `json:"priorRefundVersionId,omitempty"`
	SourceDomain          string     `json:"sourceDomain"`
	SourceTransactionType string     `json:"sourceTransactionType"`
	Scenario              string     `json:"scenario"`
	ScenarioLabel         string     `json:"scenarioLabel"`
	MappingVersion        int        `json:"mappingVersion"`
	RuleID                string     `json:"ruleId"`
	RuleVersion           int        `json:"ruleVersion"`
	RuleLabel             string     `json:"ruleLabel"`
	Status                string     `json:"status"`
	FixedParts            []struct {
		ItemKey     string `json:"itemKey"`
		Label       string `json:"label"`
		AmountMinor int64  `json:"amountMinor"`
	} `json:"fixedParts"`
	SalesItem     string           `json:"-"`
	SalesStatus   string           `json:"salesStatus,omitempty"`
	SalesParts    []vipSalesPart   `json:"salesParts,omitempty"`
	BenefitStatus string           `json:"benefitStatus,omitempty"`
	BenefitParts  []vipBenefitPart `json:"benefitParts,omitempty"`
}

func (row catalogPaymentMatch) commissionBasis() int64 {
	if row.RecognizedMinor != nil {
		return *row.RecognizedMinor
	}
	return *row.PaidMinor
}

type catalogMatchReport struct {
	HasMembershipOrders bool                  `json:"hasMembershipOrders,omitempty"`
	OrderCount          int                   `json:"orderCount"`
	MatchedCount        int                   `json:"matchedCount"`
	MatchedMinor        int64                 `json:"matchedMinor"`
	PaidMinor           int64                 `json:"paidMinor"`
	Items               []catalogPaymentMatch `json:"items"`
}

type vipLink struct {
	Key, Value, Unit string
	vipInterval
}

type vipRuntimeData struct {
	Product, Project                      string
	ConfigurationRevision                 int64
	DirectoryFingerprint                  string
	Claims                                map[int64][]vipClaim
	Definitions                           map[string]json.RawMessage
	Users, Companies, Categories          []vipLink
	DirectoryUsers, DirectoryCompanies    []vipLink
	DirectoryCategories, DirectoryAliases []vipLink
	LocalCategories                       []vipLink
	BenefitReady                          bool
	Organizations                         []vipBenefitOrganization
	PersonIDs                             map[string]int64
	CompanyNames                          map[string]string
	vipSalesData
}

const vipReferralParticipantsPath = `'$.participants[*] ? (@.role == "DIRECT_PID_USER" || @.role == "LOCK_DIRECT_SHARE_USER")'`

// The directory fingerprint is independent of which people are needed by the
// current payments. Only reusable draft quotes require this fingerprint.
const vipRuntimeSQL = `WITH scope AS (
 SELECT c.captured_at,c.benefit_ready,pro.code,cfg.project_code FROM periods p JOIN products pro ON pro.id=p.product_id
 JOIN product_catalog_settings cfg ON cfg.product_id=pro.id
 LEFT JOIN catalog_sales_context c ON c.source=cfg.source_system
 WHERE p.id=$1 AND cfg.source_system='DCAI'
), definitions AS MATERIALIZED (
 SELECT v.source_id,v.definition FROM unnest($3::text[]) r(id) JOIN product_rule_versions v ON v.source_id=r.id::uuid
), claims AS MATERIALIZED (
 SELECT c.source_version_id,c.sales_claims || coalesce((
  SELECT jsonb_agg(jsonb_build_object('role',a->>'role','user',a->>'userId') ORDER BY a->>'role',a->>'userId')
  FROM jsonb_path_query(v.fact,` + vipReferralParticipantsPath + `) a
  WHERE EXISTS(SELECT 1 FROM definitions d WHERE d.definition->'benefit_actions' @> '[{"beneficiary_mode":"` + vipOrderReferrerMode + `"}]')
 ),'[]') AS sales_claims
 FROM source_order_classifications c JOIN source_order_versions v ON v.id=c.source_version_id
 WHERE c.source_version_id=ANY($2::bigint[]) AND jsonb_typeof(c.sales_claims)='array'
), keys AS MATERIALIZED (
 SELECT DISTINCT line->>'user' AS person,line->>'company' AS company
 FROM claims CROSS JOIN LATERAL jsonb_array_elements(sales_claims) line
), users AS MATERIALIZED (
 SELECT u.external_id AS key,` + vipPersonKey + ` AS value,p.id,u.valid_from AS "from",u.valid_to AS "to"
 FROM catalog_sales_people_sources u JOIN scope s ON u.source='DCAI' AND s.captured_at=u.observed_at
 JOIN people p ON p.id=u.person_id WHERE u.external_id IN (SELECT person FROM keys)
), directory_users AS MATERIALIZED (
 SELECT u.external_id AS key,` + vipPersonKey + ` AS value,p.id,u.valid_from AS "from",u.valid_to AS "to"
 FROM identity_mappings u JOIN people p ON p.id=u.person_id
 WHERE u.source='DCAI' AND u.external_id IN (SELECT person FROM keys)
), companies AS MATERIALIZED (
 SELECT a.external_id AS key,a.company_uuid AS value,a.company_unit_id AS unit,a.valid_from AS "from",a.valid_to AS "to"
 FROM catalog_sales_company_aliases a JOIN scope s ON a.source='DCAI' AND a.observed_at=s.captured_at
 WHERE a.external_id IN (SELECT company FROM keys)
), directory_companies AS MATERIALIZED (
 SELECT a.source_company_id AS key,a.company_id AS value,a.company_id AS unit,
  a.valid_from AS "from",a.valid_to AS "to"
 FROM organization_company_sources a JOIN organization_units u ON u.id=a.company_id AND u.kind='COMPANY'
 WHERE a.source='DCAI' AND a.source_company_id IN (SELECT company FROM keys)
), directory_aliases AS MATERIALIZED (
 SELECT b.company_unit_id AS key,b.company_uuid AS value,b.valid_from AS "from",b.valid_to AS "to"
 FROM catalog_sales_company_aliases b JOIN scope s ON b.source='DCAI' AND b.observed_at=s.captured_at
 WHERE b.company_unit_id IN (SELECT unit FROM directory_companies)
), roles AS (
 SELECT ` + vipPersonKey + ` AS person,p.id,a.role,a.scope,a.scope_key AS key,a.valid_from AS "from",a.valid_to AS "to"
 FROM catalog_current_sales_appointments a JOIN people p ON p.id=a.person_id CROSS JOIN scope s
 WHERE a.source='DCAI' AND (a.scope='ORGANIZATION_UNIT' OR a.scope='PRODUCT' AND a.scope_key=s.code OR a.scope='PROJECT' AND a.scope_key=s.project_code)
), named_people AS (
 SELECT key#>>'{}' AS key FROM definitions
 CROSS JOIN LATERAL jsonb_path_query(definition,'$.benefit_actions[*].designated_party_id') key
 UNION SELECT key#>>'{}' FROM definitions
 CROSS JOIN LATERAL jsonb_path_query(definition,'$.internal_sales_direct_split.teams[*].head_party_id') key
 UNION SELECT key#>>'{}' FROM definitions
 CROSS JOIN LATERAL jsonb_path_query(definition,'$.internal_sales_direct_split.teams[*].service_party_id') key
), persons AS MATERIALIZED (
 SELECT p.id,p.name,` + vipPersonKey + ` AS key FROM people p
 WHERE p.legacy_party_id IS NOT NULL OR p.id IN (SELECT id FROM users UNION SELECT id FROM directory_users UNION SELECT id FROM roles)
  OR ('person:'||p.id) IN (SELECT key FROM named_people)
)
SELECT jsonb_build_object('product',s.code,'project',s.project_code,'benefitReady',coalesce(s.benefit_ready,false),
 'configurationRevision',CASE WHEN $4 THEN coalesce((SELECT revision FROM source_write_state WHERE source='CONFIG'),0) END,
 'directoryFingerprint',CASE WHEN $4 THEN (SELECT encode(sha256(convert_to(
  coalesce(jsonb_agg(jsonb_build_array(` + vipPersonKey + `,p.name) ORDER BY p.id)::text,'[]'),'UTF8')),'hex') FROM people p) END,
 'organizations',(SELECT jsonb_agg(jsonb_build_object('party',b.party_uuid,'unit',o.id,'name',o.name) ORDER BY b.party_uuid,o.id)
  FROM catalog_benefit_organizations b JOIN organization_units o ON o.id=b.unit_id AND o.status='ACTIVE'
  WHERE b.source='DCAI' AND b.observed_at=s.captured_at),
 'claims',(SELECT jsonb_object_agg(source_version_id::text,sales_claims) FROM claims),
 'definitions',(SELECT jsonb_object_agg(source_id::text,jsonb_build_object(
  'amount_basis',definition->'amount_basis','stacking_policy',definition->'stacking_policy','rounding_policy',definition->'rounding_policy',
  'refund_policy',definition->'refund_policy',
  'internal_sales_direct_split',definition->'internal_sales_direct_split',
  'benefit_actions',definition->'benefit_actions')) FROM definitions),
 'users',(SELECT jsonb_agg(to_jsonb(users)-'id') FROM users),
 'directoryUsers',(SELECT jsonb_agg(to_jsonb(directory_users)-'id') FROM directory_users),
 'companies',(SELECT jsonb_agg(to_jsonb(companies)) FROM companies),
 'directoryCompanies',(SELECT jsonb_agg(to_jsonb(directory_companies)) FROM directory_companies),
 'companyNames',(SELECT jsonb_object_agg(u.id,u.name) FROM organization_units u WHERE u.kind='COMPANY'
  AND u.id IN (SELECT unit FROM companies UNION SELECT unit FROM directory_companies)),
 'directoryAliases',(SELECT jsonb_agg(to_jsonb(directory_aliases)) FROM directory_aliases),
 'categories',(SELECT jsonb_agg(jsonb_build_object('key',h.company_uuid,'value',h.category_uuid,'from',h.valid_from,'to',h.valid_to))
  FROM catalog_sales_categories h WHERE h.source='DCAI' AND h.observed_at=s.captured_at
   AND h.company_uuid IN (SELECT value FROM companies UNION SELECT value FROM directory_aliases)),
 'localCategories',(SELECT jsonb_agg(jsonb_build_object('key',h.unit_id,'value',h.category_id,'from',h.valid_from,'to',h.valid_to))
  FROM organization_sales_category_history h
  WHERE h.recorded_at IS NOT NULL AND h.unit_id IN (SELECT unit FROM companies)),
 'directoryCategories',(SELECT jsonb_agg(jsonb_build_object('key',h.unit_id,'value',h.category_id,'from',h.valid_from,'to',h.valid_to))
  FROM organization_sales_category_history h WHERE h.unit_id IN (SELECT unit FROM directory_companies)),
 'roles',(SELECT jsonb_agg(to_jsonb(roles)-'id' ORDER BY person,role,scope,key,"from","to") FROM roles),
 'memberships',(SELECT jsonb_agg(jsonb_build_object('person',` + vipPersonKey + `,'unit',a.unit_id,'kind',a.kind,'unitKind',u.kind,'from',a.valid_from,'to',a.valid_to)
   ORDER BY p.id,a.unit_id,a.kind,a.valid_from,a.valid_to)
  FROM organization_assignments a JOIN people p ON p.id=a.person_id JOIN organization_units u ON u.id=a.unit_id
  WHERE p.id IN (SELECT id FROM users UNION SELECT id FROM directory_users)),
 'people',(SELECT jsonb_object_agg(key,name) FROM persons),
 'personIds',(SELECT jsonb_object_agg(key,id) FROM persons))
FROM scope s`

// Published definitions include configured UUIDs. Direct account references
// also allow local staff to hold roles without an external identity.
const vipPersonKey = `coalesce(p.legacy_party_id::text,'person:'||p.id)`

func loadVIPRuntimeData(ctx context.Context, tx pgx.Tx, periodID int64, matched []catalogPaymentMatch, quoteContext bool) (vipRuntimeData, error) {
	ids := make([]int64, 0, len(matched))
	var rules []string
	seen := make(map[string]bool)
	for _, item := range matched {
		if item.PaidMinor != nil && item.Status != "INVALID_PAYMENT" && item.Status != "DUPLICATE_PAYMENT" {
			ids = append(ids, item.SourceVersionID)
		}
		if item.Status != "MATCHED" || item.RuleID == "" {
			continue
		}
		if !seen[item.RuleID] {
			seen[item.RuleID] = true
			rules = append(rules, item.RuleID)
		}
	}
	if len(ids) == 0 {
		return vipRuntimeData{}, nil
	}
	var raw json.RawMessage
	if err := tx.QueryRow(ctx, vipRuntimeSQL, periodID, ids, rules, quoteContext).Scan(&raw); err != nil {
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
	row, conflict := resolveVIPLinkRecord(values, key, at)
	return row.Value, conflict
}

func resolveVIPLinkRecord(values []vipLink, key string, at time.Time) (selected vipLink, conflict bool) {
	found := false
	for _, row := range values {
		if row.Key != key || !row.contains(at) {
			continue
		}
		if found && (selected.Value != row.Value || selected.Unit != row.Unit) {
			return vipLink{}, true
		}
		selected, found = row, true
	}
	return selected, false
}

func vipPaymentInputs(sale catalogPaymentMatch, d vipRuntimeData) (vipSalesRequest, string) {
	req := vipSalesRequest{Order: sale.OrderID, At: sale.PaidAt, Paid: sale.commissionBasis(), OriginalPaid: *sale.PaidMinor,
		Product: d.Product, Project: d.Project}
	src, ok := d.Claims[sale.SourceVersionID]
	if !ok {
		return req, "SALES_SOURCE_MISSING"
	}
	req.Referrer = d.orderReferrer(src, sale.PaidAt)
	claim, issue := vipSellerClaim(src)
	if issue != "" {
		return req, issue
	}
	req.Relationship = claim.Relationship
	var conflict bool
	req.Seller, conflict = resolveVIPLink(d.Users, claim.User, sale.PaidAt)
	if req.Seller == "" && !conflict {
		req.Seller, conflict = resolveVIPLink(d.DirectoryUsers, claim.User, sale.PaidAt)
	}
	if conflict {
		return req, "SALES_PERSON_CONFLICT"
	}
	company, conflict := resolveVIPLinkRecord(d.Companies, claim.Company, sale.PaidAt)
	companyFromDirectory := company.Key == "" && !conflict
	if companyFromDirectory {
		company, conflict = resolveVIPLinkRecord(d.DirectoryCompanies, claim.Company, sale.PaidAt)
		if !conflict && company.Unit != "" {
			var alias string
			alias, conflict = resolveVIPLink(d.DirectoryAliases, company.Unit, sale.PaidAt)
			if alias != "" {
				company.Value = alias
			}
		}
	}
	if conflict {
		return req, "SALES_COMPANY_CONFLICT"
	}
	req.Company, req.CompanyUnit = company.Value, company.Unit
	req.CompanyName = d.CompanyNames[company.Unit]
	category, conflict := resolveVIPLinkRecord(d.LocalCategories, req.CompanyUnit, sale.PaidAt)
	if companyFromDirectory {
		category, conflict = resolveVIPLinkRecord(d.DirectoryCategories, req.CompanyUnit, sale.PaidAt)
	}
	if category.Key == "" && !conflict {
		category, conflict = resolveVIPLinkRecord(d.Categories, req.Company, sale.PaidAt)
	}
	if conflict {
		return req, "SALES_CATEGORY_CONFLICT"
	}
	if category.Key != "" {
		req.Category = &category.Value
	}
	return req, ""
}

type vipOrderReferrer struct{ Party, Issue string }

func (d vipRuntimeData) orderReferrer(claims []vipClaim, at time.Time) vipOrderReferrer {
	user := ""
	for _, candidate := range claims {
		if candidate.Role != "DIRECT_PID_USER" && candidate.Role != "LOCK_DIRECT_SHARE_USER" || candidate.User == "" {
			continue
		}
		if user != "" && user != candidate.User {
			return vipOrderReferrer{Issue: "REFERRER_SOURCE_CONFLICT"}
		}
		user = candidate.User
	}
	if user == "" {
		return vipOrderReferrer{}
	}
	party, conflict := resolveVIPLink(d.Users, user, at)
	if party == "" && !conflict {
		party, conflict = resolveVIPLink(d.DirectoryUsers, user, at)
	}
	switch {
	case conflict:
		return vipOrderReferrer{Issue: "REFERRER_PERSON_CONFLICT"}
	case party == "" || d.PersonIDs[party] == 0:
		return vipOrderReferrer{Issue: "REFERRER_PERSON_MISSING"}
	default:
		return vipOrderReferrer{Party: party}
	}
}

func applyVIPQuotes(items []catalogPaymentMatch, data vipRuntimeData) {
	policies := readVIPQuotePolicies(data.Definitions)
	for i := range items {
		row := &items[i]
		if row.Status == "MATCHED" && row.PaidMinor != nil {
			req, issue := vipPaymentInputs(*row, data)
			policies.apply(row, data, req, issue)
		}
	}
}

type vipQuotePolicies struct {
	sales    map[string]vipSalesPolicy
	benefits map[string]vipBenefitPolicy
}

func readVIPQuotePolicies(definitions map[string]json.RawMessage) vipQuotePolicies {
	sales := make(map[string]vipSalesPolicy, len(definitions))
	for id, definition := range definitions {
		if policy, ok := readVIPSalesPolicy(definition); ok {
			sales[id] = policy
		}
	}
	return vipQuotePolicies{sales: sales, benefits: readVIPBenefitPolicies(definitions)}
}

func (p vipQuotePolicies) apply(row *catalogPaymentMatch, data vipRuntimeData, req vipSalesRequest, issue string) {
	if policy, ok := p.sales[row.RuleID]; ok {
		row.SalesItem = policy.Item
		row.SalesParts, row.SalesStatus = nil, issue
		if issue == "" {
			row.SalesParts, row.SalesStatus = calculateVIPSales(policy, req, data.vipSalesData)
		}
		if row.SalesStatus == "" {
			row.SalesStatus = "CALCULATED"
		}
	}
	applyVIPBenefitQuote(row, data, p.benefits, req, issue)
}

func readVIPBenefitPolicies(definitions map[string]json.RawMessage) map[string]vipBenefitPolicy {
	benefits := make(map[string]vipBenefitPolicy, len(definitions))
	for id, definition := range definitions {
		if policy, ok := readVIPBenefits(definition); ok {
			benefits[id] = policy
		}
	}
	return benefits
}

func applyVIPBenefitQuotes(items []catalogPaymentMatch, data vipRuntimeData) {
	benefits := readVIPBenefitPolicies(data.Definitions)
	for i := range items {
		row := &items[i]
		if row.Status == "MATCHED" && row.PaidMinor != nil {
			req, issue := vipPaymentInputs(*row, data)
			applyVIPBenefitQuote(row, data, benefits, req, issue)
		}
	}
}

func applyVIPBenefitQuote(row *catalogPaymentMatch, data vipRuntimeData, benefits map[string]vipBenefitPolicy, req vipSalesRequest, issue string) {
	if policy, ok := benefits[row.RuleID]; ok {
		row.FixedParts = row.FixedParts[:0]
		row.BenefitParts = quoteVIPBenefits(req, issue, policy, data)
	} else if data.Product == "DCAI_VIP" {
		row.BenefitStatus = "BENEFIT_RULE_UNSUPPORTED"
	}
}
