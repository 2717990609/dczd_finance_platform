package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"
)

type catalogRateComponent struct {
	ItemKey     string             `json:"itemKey"`
	Label       string             `json:"label"`
	Rate        *int64             `json:"rateBps,omitempty"`
	Rows        []catalogRateRow   `json:"rows,omitempty"`
	Recipient   *catalogRulePerson `json:"recipient,omitempty"`
	TargetKind  string             `json:"targetKind"`
	TargetLabel string             `json:"targetLabel,omitempty"`
	TeamSplit   *catalogSplitRates `json:"teamSplit,omitempty"`
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
	Row          int    `json:"row"`
	Company      string `json:"company"`
	Relationship string `json:"relationship"`
	Category     string `json:"category,omitempty"`
	Rate         int64  `json:"rateBps"`
	TeamRate     bool   `json:"teamRate,omitempty"`
	Fallback     bool   `json:"companyFallback,omitempty"`
}

type catalogRateSheet struct {
	ProductID          int64                    `json:"productId"`
	ProductName        string                   `json:"productName"`
	Label              string                   `json:"label"`
	Version            int                      `json:"version"`
	EffectiveFrom      time.Time                `json:"effectiveFrom"`
	EffectiveTo        *time.Time               `json:"effectiveTo"`
	MayPublish         bool                     `json:"mayPublish"`
	RequiresCompletion bool                     `json:"requiresCompletion,omitempty"`
	Components         []catalogRateComponent   `json:"components"`
	Teams              []catalogRuleTeam        `json:"teams,omitempty"`
	StructureOptions   *catalogStructureOptions `json:"structureOptions,omitempty"`
}

func catalogRateComponents(raw json.RawMessage, companies, categories, relationships, organizations map[string]string) ([]catalogRateComponent, error) {
	_, actions, err := catalogRateActions(raw)
	if err != nil {
		return nil, err
	}
	sales, _ := readVIPSalesPolicy(raw)
	items := make([]catalogRateComponent, 0, len(actions))
	caption := func(names map[string]string, code string) string {
		if name := names[code]; name != "" {
			return name
		}
		return code
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
		item := catalogRateComponent{ItemKey: action.Item, Label: action.Label, Rate: action.Rate, TargetKind: action.targetKind()}
		if item.ItemKey == sales.Item {
			item.TargetKind = "seller"
			if split := sales.Split; len(split.Teams) != 0 {
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
			item.TargetLabel = organizations[action.Company]
			if item.TargetLabel == "" {
				item.TargetLabel = "待核收款公司"
			}
		}
		if item.TargetKind == "pool" {
			item.TargetLabel = action.PoolName
			if name := organizations[action.PoolParty]; name != "" {
				item.TargetLabel += " · " + name
			}
		}
		if len(action.RateTable) > 0 {
			rows, valid := parseVIPRateTable(action.RateTable)
			if !valid {
				return nil, errCatalogRateUnavailable
			}
			for i, entry := range rows {
				item.Rows = append(item.Rows, catalogRateRow{
					Row: i + 1, Company: caption(companies, entry.Company), Category: caption(categories, entry.Category),
					Relationship: caption(relationships, entry.Relationship), Rate: entry.Rate,
					TeamRate: item.ItemKey == sales.Split.Item && slices.ContainsFunc(sales.Split.Teams, func(team vipInternalTeam) bool {
						return team.Company == entry.Company
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
		options, optionsErr := readCatalogStructureOptions(r.Context(), s.db)
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
		_, product, err := s.ruleRateProduct(ctx, input.BaseID)
		if err != nil {
			return catalogPublication{}, err
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
	var companyNames, categoryNames, relationshipNames, organizationNames map[string]string
	var supported bool
	err := s.db.QueryRow(ctx, `WITH rule AS (
	 SELECT p.product_id,pro.name AS product_name,p.name,v.version_no,v.effective_from,`+catalogDisplayedThrough+` AS effective_to,v.definition,
	  coalesce((`+catalogRateVersionEligibilitySQL+`) AND `+catalogVersionVisible+`,false) AS available,
	  `+catalogRateConfigurationSQL+` AS supported,`+catalogHealthRateRule+` AS requires_completion,
	  scope.source_system
	 FROM product_rule_versions v JOIN product_rule_packages p ON p.source_id=v.package_id
	 JOIN product_catalog_settings scope ON scope.product_id=p.product_id AND scope.project_code=p.project_code
	 JOIN products pro ON pro.id=p.product_id
	 WHERE v.source_id=$1::uuid AND p.scope_type='BUSINESS_SCENARIO' AND scope.source_system='DCAI'
	), companies AS (
	 SELECT a.company_uuid,min(u.name) AS name FROM rule r JOIN catalog_sales_context c ON c.source=r.source_system
	 JOIN catalog_sales_company_aliases a ON a.source=c.source AND a.observed_at=c.captured_at
	 JOIN organization_units u ON u.id=a.company_unit_id GROUP BY a.company_uuid HAVING count(DISTINCT u.name)=1
	), organizations AS (
	 SELECT b.party_uuid,min(u.name) AS name FROM rule r JOIN catalog_sales_context c ON c.source=r.source_system
	 JOIN catalog_benefit_organizations b ON b.source=c.source AND b.observed_at=c.captured_at
	 JOIN organization_units u ON u.id=b.unit_id GROUP BY b.party_uuid HAVING count(DISTINCT u.id)=1
	)
	SELECT r.product_id,r.product_name,r.name,r.version_no,r.effective_from,r.effective_to,r.definition,r.available,r.supported,r.requires_completion,
	 coalesce((SELECT jsonb_object_agg(company_uuid,name) FROM companies),'{}'),
	 coalesce((SELECT jsonb_object_agg(key,label) FROM catalog_sales_category_options),'{}'),
	 coalesce((SELECT jsonb_object_agg(option_key,label) FROM configuration_options WHERE group_key='catalogSalesRelationship' AND enabled),'{}'),
	 coalesce((SELECT jsonb_object_agg(party_uuid,name) FROM organizations),'{}')
	FROM rule r`, id).Scan(&sheet.ProductID, &sheet.ProductName, &sheet.Label, &sheet.Version, &sheet.EffectiveFrom, &sheet.EffectiveTo,
		&definition, &sheet.MayPublish, &supported, &sheet.RequiresCompletion, &companyNames, &categoryNames, &relationshipNames, &organizationNames)
	if err != nil {
		return sheet, err
	}
	if !supported {
		return sheet, errCatalogRateUnavailable
	}
	companyNames["ANY"] = "不限公司"
	sheet.Components, err = catalogRateComponents(definition, companyNames, categoryNames, relationshipNames, organizationNames)
	if err == nil {
		sheet.Teams, err = s.catalogRuleRecipients(ctx, definition, sheet.Components, companyNames)
	}
	return sheet, err
}
