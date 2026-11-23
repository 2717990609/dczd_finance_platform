package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
)

type catalogRateComponent struct {
	ItemKey                  string             `json:"itemKey"`
	Label                    string             `json:"label"`
	Rate                     *int64             `json:"rateBps,omitempty"`
	Fixed                    *int64             `json:"fixedMinor,omitempty"`
	Rows                     []catalogRateRow   `json:"rows,omitempty"`
	Recipient                *catalogRulePerson `json:"recipient,omitempty"`
	PoolOwner                *catalogRulePerson `json:"poolOwner,omitempty"`
	PoolID                   int64              `json:"poolId,omitempty"`
	TargetKind               string             `json:"targetKind"`
	TargetLabel              string             `json:"targetLabel,omitempty"`
	TargetOrg                string             `json:"targetOrganization,omitempty"`
	TargetRole               string             `json:"targetRole,omitempty"`
	AppointmentUnit          string             `json:"appointmentUnitId,omitempty"`
	AppointmentLevel         string             `json:"appointmentLevel,omitempty"`
	MemberRole               string             `json:"memberRole,omitempty"`
	TeamSplit                *catalogSplitRates `json:"teamSplit,omitempty"`
	LeaderScope              *vipLeaderScope    `json:"leaderScope,omitempty"`
	CommissionUnits          []string           `json:"salesScopeUnitIds,omitempty"`
	ScopeCategories          []string           `json:"salesScopeCategoryIds,omitempty"`
	OwnSales                 bool               `json:"ownSales,omitempty"`
	RecipientDuty            string             `json:"recipientDuty,omitempty"`
	Disabled                 bool               `json:"disabled,omitempty"`
	ServiceDisabled          bool               `json:"serviceDisabled,omitempty"`
	ServiceLabel             string             `json:"serviceLabel,omitempty"`
	ServiceRecipientDuty     string             `json:"serviceRecipientDuty,omitempty"`
	ServiceMembershipNewOnly *bool              `json:"serviceMembershipNewOnly,omitempty"`
	MembershipNewOnly        *bool              `json:"membershipNewOnly,omitempty"`
}

type catalogSplitRates struct {
	Total     int64 `json:"total"`
	Sales     int64 `json:"sales"`
	HeadSales int64 `json:"headSales"`
	GroupLead int64 `json:"groupLead"`
	Service   int64 `json:"service"`
	TeamLead  int64 `json:"teamLead"`
}

type catalogRateRow struct {
	Row                int      `json:"row"`
	Company            string   `json:"company"`
	Relationship       string   `json:"relationship"`
	LimitsRelationship bool     `json:"limitsRelationship,omitempty"`
	Category           string   `json:"category,omitempty"`
	Scope              string   `json:"scope"`
	Rate               int64    `json:"rateBps"`
	Payee              string   `json:"payee,omitempty"`
	TeamRate           bool     `json:"teamRate,omitempty"`
	Fallback           bool     `json:"companyFallback,omitempty"`
	CompanyIDs         []string `json:"companyIds,omitempty"`
	CategoryIDs        []string `json:"categoryIds,omitempty"`
	RelationshipCode   string   `json:"relationshipCode"`
}

type catalogRateSheet struct {
	ProductID          int64                    `json:"productId"`
	ProductName        string                   `json:"productName"`
	ScopeLabel         string                   `json:"scopeLabel,omitempty"`
	Label              string                   `json:"label"`
	Version            int                      `json:"version"`
	EffectiveFrom      time.Time                `json:"effectiveFrom"`
	EffectiveTo        *time.Time               `json:"effectiveTo"`
	MayPublish         bool                     `json:"mayPublish"`
	RequiresCompletion bool                     `json:"requiresCompletion,omitempty"`
	MembershipSKU      bool                     `json:"membershipSku,omitempty"`
	MembershipNewOnly  bool                     `json:"membershipNewOnly,omitempty"`
	Components         []catalogRateComponent   `json:"components"`
	LeaderArrangements []catalogRateComponent   `json:"leaderArrangements,omitempty"`
	Teams              []catalogRuleTeam        `json:"teams,omitempty"`
	StructureOptions   *catalogStructureOptions `json:"structureOptions,omitempty"`
	ScopeUnitNames     map[string]string        `json:"scopeUnitNames,omitempty"`
	ScopeCategoryNames map[string]string        `json:"scopeCategoryNames,omitempty"`
}

func catalogRateComponents(raw json.RawMessage, companies, categories, relationships, organizations map[string]string) ([]catalogRateComponent, error) {
	_, actions, err := catalogRateActions(raw)
	if err != nil {
		return nil, err
	}
	sales, _ := readVIPSalesPolicy(raw)
	items := make([]catalogRateComponent, 0, len(actions))
	caption := func(names map[string]string, code, missing string) string {
		if name := names[code]; name != "" {
			return name
		}
		if _, isID := canonicalSourceUUID(code); isID {
			return missing
		}
		return code
	}
	joinedCaption := func(names map[string]string, codes []string, missing string) string {
		labels := make([]string, 0, len(codes))
		for _, code := range codes {
			labels = append(labels, caption(names, code, missing))
		}
		return strings.Join(labels, "、")
	}
	for _, body := range actions {
		rawAction, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		var action vipBenefitAction
		if err := json.Unmarshal(rawAction, &action); err != nil {
			return nil, err
		}
		if action.Leader != nil {
			continue
		}
		item := catalogRateComponent{ItemKey: action.Item, Label: action.Label, Rate: action.Rate, Fixed: action.Fixed, TargetKind: action.targetKind()}
		item.CommissionUnits = action.CommissionUnits
		item.ScopeCategories = action.ScopeCategories
		item.OwnSales, item.RecipientDuty = action.OwnSales, action.RecipientDuty
		item.MembershipNewOnly = action.MembershipNewOnly
		if item.ItemKey == sales.Item {
			item.TargetKind = "seller"
			if split := sales.Split; split.Item != "" {
				item.ServiceDisabled = slices.Contains(split.DisabledShares, vipInternalServiceItem)
				item.ServiceLabel = split.ServiceLabel
				item.ServiceRecipientDuty = split.RecipientDuties[vipInternalServiceItem]
				item.ServiceMembershipNewOnly = split.membershipOverride(vipInternalServiceItem)
				item.TeamSplit = &catalogSplitRates{
					Total: split.Total, Sales: split.Assistant, HeadSales: split.HeadSeller,
					GroupLead: split.Group, Service: split.Service, TeamLead: split.TeamHead,
				}
			}
		}
		if item.Label == "" {
			item.Label = item.ItemKey
		}
		if action.Company != "" {
			item.TargetOrg = action.Company
			item.TargetLabel = organizations[action.Company]
			if item.TargetLabel == "" {
				item.TargetLabel = "待核收款公司"
			}
		}
		if item.TargetKind == "pool" {
			item.TargetOrg, item.TargetRole = action.PoolParty, action.OwnerRole
			item.PoolID = catalogPoolID(action.PoolCode)
			item.MemberRole = action.Role
			item.TargetLabel = action.PoolName
			if name := organizations[action.PoolParty]; name != "" {
				item.TargetLabel += " · " + name
			}
		}
		if item.TargetKind == "person" {
			item.TargetRole = action.Role
			item.AppointmentUnit = action.AppointmentUnit
			if action.AppointmentLevel != "" {
				item.TargetKind, item.AppointmentLevel = "appointment", action.AppointmentLevel
			}
		}
		if len(action.RateTable) > 0 {
			rows, valid := parseVIPRateTable(action.RateTable)
			if !valid {
				return nil, errCatalogRateUnavailable
			}
			for i, entry := range rows {
				scope := "company"
				switch {
				case entry.ExternalDefault:
					scope = "external"
				case entry.Company == "ANY" && entry.hasCategory():
					scope = "category"
				case entry.Company == "ANY":
					scope = "general"
				case entry.SnapshotDefault:
					scope = "inherited"
				}
				var companyIDs, categoryIDs []string
				if entry.Company != "ANY" {
					companyIDs = entry.companies()
				}
				if entry.hasCategory() {
					categoryIDs = entry.categories()
				}
				item.Rows = append(item.Rows, catalogRateRow{
					Row: i + 1, Company: joinedCaption(companies, entry.companies(), "公司名称待补"),
					Category:         joinedCaption(categories, entry.categories(), "销售类别名称待补"),
					Relationship:     caption(relationships, entry.Relationship, "销售关系待核"),
					RelationshipCode: entry.Relationship, CompanyIDs: companyIDs, CategoryIDs: categoryIDs,
					LimitsRelationship: entry.Relationship != "ANY", Rate: entry.Rate, Payee: entry.Payee, Scope: scope,
					TeamRate: item.ItemKey == sales.Split.Item && slices.ContainsFunc(sales.Split.Teams, func(team vipInternalTeam) bool {
						return entry.namesCompany(team.Company)
					}),
					Fallback: entry.SnapshotDefault,
				})
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func (s workspace) ruleRateProduct(ctx context.Context, raw string) (string, int64, error) {
	id, ok := canonicalSourceUUID(raw)
	if !ok {
		return "", 0, invalid
	}
	var product int64
	err := s.db.QueryRow(ctx, `SELECT p.product_id FROM product_rule_versions v
	 JOIN product_rule_packages p ON p.source_id=v.package_id
	 JOIN product_catalog_settings c ON c.product_id=p.product_id AND c.project_code=p.project_code
	 WHERE v.source_id=$1::uuid AND c.source_system='DCAI' AND p.scope_type='BUSINESS_SCENARIO'`, id).Scan(&product)
	return id, product, err
}

func (s workspace) getCatalogRuleRates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, err := s.financeRateActor(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	if raw := r.URL.Query().Get("productId"); raw != "" {
		options, err := s.catalogScenarioOptions(r.Context(), raw, actor)
		respond(w, options, err)
		return
	}
	id, product, err := s.ruleRateProduct(r.Context(), r.URL.Query().Get("ruleId"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	if !actor.permits(product, false) {
		respond(w, nil, errRulePermission)
		return
	}
	sheet, err := s.catalogRuleRateSheet(r.Context(), id)
	sheet.MayPublish = sheet.MayPublish && actor.permits(product, true)
	if err == nil && sheet.MayPublish {
		options, optionsErr := readCatalogStructureOptions(r.Context(), s.db, product)
		sheet.StructureOptions, err = &options, optionsErr
	}
	respond(w, sheet, err)
}

func (s workspace) postCatalogRuleRates(w http.ResponseWriter, r *http.Request) {
	actor, err := s.financeRateActor(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	submitJSON(128<<10, func(ctx context.Context, input publishCatalogRatesInput) (catalogPublication, error) {
		var product int64
		if input.Create != nil {
			product = input.Create.ProductID
		} else {
			var err error
			_, product, err = s.ruleRateProduct(ctx, input.BaseID)
			if err != nil {
				return catalogPublication{}, err
			}
		}
		if !actor.permits(product, true) {
			return catalogPublication{}, errRulePermission
		}
		if actor.account != nil {
			ctx = context.WithValue(ctx, financePrincipalKey{}, actor.account)
		}
		return s.publishCatalogRates(ctx, input, actor.subject)
	})(w, r)
}

func (s workspace) catalogRuleRateSheet(ctx context.Context, id string) (catalogRateSheet, error) {
	var sheet catalogRateSheet
	var definition json.RawMessage
	var companyNames, categoryNames, relationshipNames, organizationNames, groupNames map[string]string
	var supported bool
	err := s.db.QueryRow(ctx, `WITH rule AS (
	 SELECT p.product_id,pro.name AS product_name,p.name,v.version_no,v.effective_from,`+catalogDisplayedThrough+` AS effective_to,v.definition,
	  coalesce((`+catalogRateVersionEligibilitySQL+`) AND `+catalogVersionVisible+`,false) AS available,
	  `+catalogRateConfigurationSQL+` AS supported,`+catalogHealthRateRule+` AS requires_completion,
	  scope.source_system,coalesce(scenario.membership,false) AS membership_sku,coalesce(scenario.label,'') AS scope_label
	 FROM product_rule_versions v JOIN product_rule_packages p ON p.source_id=v.package_id
	 JOIN product_catalog_settings scope ON scope.product_id=p.product_id AND scope.project_code=p.project_code
	 JOIN products pro ON pro.id=p.product_id
	 CROSS JOIN LATERAL (
	  SELECT bool_and(`+catalogMembershipSourceRoute+`) AS membership,
	   CASE WHEN count(DISTINCT r.sku_label) FILTER (WHERE r.effective_from<=v.effective_from
	    AND (r.effective_to IS NULL OR v.effective_from<r.effective_to))=1
	   THEN min(r.sku_label) FILTER (WHERE r.effective_from<=v.effective_from
	    AND (r.effective_to IS NULL OR v.effective_from<r.effective_to)) END AS label
	  FROM catalog_business_routes r WHERE r.product_id=p.product_id AND r.observed_at=scope.captured_at
	    AND r.source=scope.source_system AND r.sku_code=p.scope_id
	 ) scenario
	 WHERE v.source_id=$1::uuid AND p.scope_type='BUSINESS_SCENARIO' AND scope.source_system='DCAI'
	), company_keys AS (`+catalogSalesCompanyKeysSQL+`), companies AS (
	 SELECT a.key,min(u.name) AS name FROM company_keys a
	 JOIN organization_units u ON u.id=a.unit GROUP BY a.key HAVING count(DISTINCT u.name)=1
	), organizations AS (
	 SELECT b.party_uuid,min(u.name) AS name FROM rule r JOIN catalog_sales_context c ON c.source=r.source_system
	 JOIN catalog_benefit_organizations b ON b.source=c.source AND b.observed_at=c.captured_at
	 JOIN organization_units u ON u.id=b.unit_id GROUP BY b.party_uuid HAVING count(DISTINCT u.id)=1
	)
	SELECT r.product_id,r.product_name,r.scope_label,r.name,r.version_no,r.effective_from,r.effective_to,r.definition,r.available,r.supported,r.requires_completion,r.membership_sku,
	 coalesce((SELECT jsonb_object_agg(key,name) FROM companies),'{}'),
	 coalesce((SELECT jsonb_object_agg(key,label) FROM catalog_sales_category_options),'{}'),
	 coalesce((SELECT jsonb_object_agg(option_key,label) FROM configuration_options WHERE group_key='catalogSalesRelationship' AND enabled),'{}'),
	 coalesce((SELECT jsonb_object_agg(party_uuid,name) FROM organizations),'{}'),
	 coalesce((SELECT jsonb_object_agg(u.id,u.name) FROM organization_units u
	  WHERE u.id::text IN (SELECT group_id#>>'{}' FROM jsonb_path_query(r.definition,
	   '$.internal_sales_direct_split.teams[*].seller_group_unit_ids[*]') group_id)),'{}'),
	 coalesce((SELECT jsonb_object_agg(u.id,concat_ws(' / ',gp.name,p.name,u.name))
	  FROM organization_units u LEFT JOIN organization_units p ON p.id=u.parent_id
	  LEFT JOIN organization_units gp ON gp.id=p.parent_id
	  WHERE u.id::text IN (SELECT unit_id#>>'{}' FROM jsonb_path_query(r.definition,
	   '$.benefit_actions[*].sales_scope_unit_ids[*]') unit_id
	   UNION ALL SELECT unit_id#>>'{}' FROM jsonb_path_query(r.definition,
	   '$.benefit_actions[*].appointment_organization_unit_id') unit_id)),'{}')
	FROM rule r`, id).Scan(&sheet.ProductID, &sheet.ProductName, &sheet.ScopeLabel, &sheet.Label, &sheet.Version, &sheet.EffectiveFrom, &sheet.EffectiveTo,
		&definition, &sheet.MayPublish, &supported, &sheet.RequiresCompletion, &sheet.MembershipSKU, &companyNames, &categoryNames, &relationshipNames,
		&organizationNames, &groupNames, &sheet.ScopeUnitNames)
	if err != nil {
		return sheet, err
	}
	if !supported {
		return sheet, errCatalogRateUnavailable
	}
	sheet.MembershipNewOnly = catalogNewMembershipOnly(definition)
	companyNames["ANY"] = "不限公司"
	sheet.ScopeCategoryNames = categoryNames
	sheet.Components, err = catalogRateComponents(definition, companyNames, categoryNames, relationshipNames, organizationNames)
	if err == nil {
		policy, _ := readVIPBenefits(definition)
		regular := len(sheet.Components)
		items := append(sheet.Components, catalogLeaderComponents(policy)...)
		sheet.Teams, err = s.catalogRuleRecipients(ctx, policy, items, companyNames, groupNames)
		sheet.Components, sheet.LeaderArrangements = items[:regular], items[regular:]
	}
	return sheet, err
}
