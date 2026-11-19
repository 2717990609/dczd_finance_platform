package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const creatorSalesFixture = `{"item_key":"creator-sales","role":"SALES","label":"个人销售","type":"RATE_TABLE","beneficiary_mode":"PARTY",
 "rate_table":[{"sales_organization_party_id":"company","sales_relationship":"DIRECT","rate_bps":1500}]}`

const creatorActionsFixture = creatorSalesFixture + `,
 {"item_key":"rnd","label":"研发","role":"AI_RND_GENERAL_MANAGER","type":"RATE","beneficiary_mode":"PARTY","designated_party_id":"rnd","rate_bps":100},
 {"item_key":"chair","label":"董事长","role":"CHAIRMAN","type":"RATE","beneficiary_mode":"PARTY","designated_party_id":"chair","rate_bps":100},
 {"item_key":"owner","label":"项目负责人","role":"PROJECT_OWNER","type":"RATE","beneficiary_mode":"PARTY","designated_party_id":"owner","rate_bps":300},
 {"item_key":"technology","label":"技术","role":"TECHNOLOGY","type":"RATE","beneficiary_mode":"PARTY","company_beneficiary_party_id":"tech","rate_bps":200},
 {"item_key":"delivery","label":"交付","role":"DELIVERY_TEAM","type":"RATE","beneficiary_mode":"TEAM_POOL","pool_party_id":"team",
  "pool_code":"delivery","pool_name":"交付池","pool_organization_unit_id":"dept","allocation_owner_role":"TEAM_LEAD","rate_bps":300}`

func creatorBenefitDefinition(actions, extra string) json.RawMessage {
	return json.RawMessage(`{"amount_basis":"ACTUAL_RECEIVED","stacking_policy":"SUM","rounding_policy":{"mode":"HALF_UP","scale":2},` +
		extra + `"benefit_actions":[` + actions + `]}`)
}

func TestCreatorCalculatesSixBenefitsWithoutInternalSalesSplit(t *testing.T) {
	data := vipRuntimeData{
		Product: "creator", Project: "DCAI", BenefitReady: true,
		Definitions:   map[string]json.RawMessage{"v11": creatorBenefitDefinition(creatorActionsFixture, "")},
		Claims:        map[int64][]vipClaim{1: {{Role: "PID_USER", User: "source-user", Company: "source-company", Relationship: "DIRECT"}}},
		Users:         []vipLink{{Key: "source-user", Value: "seller"}},
		Companies:     []vipLink{{Key: "source-company", Value: "company"}},
		Organizations: []vipBenefitOrganization{{Party: "tech", Unit: "company", Name: "技术公司"}, {Party: "team", Unit: "dept", Name: "交付部"}},
		vipSalesData: vipSalesData{
			People: map[string]string{"seller": "销售", "rnd": "研发负责人", "chair": "董事长", "owner": "项目负责人", "lead": "池分配负责人"},
			Roles: []vipRole{
				{Person: "seller", Role: "SALES", Scope: "PROJECT", Key: "DCAI"},
				{Person: "rnd", Role: "AI_RND_GENERAL_MANAGER", Scope: "PROJECT", Key: "DCAI"},
				{Person: "chair", Role: "CHAIRMAN", Scope: "PRODUCT", Key: "creator"},
				{Person: "owner", Role: "PROJECT_OWNER", Scope: "PROJECT", Key: "DCAI"},
				{Person: "lead", Role: "TEAM_LEAD", Scope: "ORGANIZATION_UNIT", Key: "dept"},
			},
		},
	}
	amount := int64(1680000)
	rows := []catalogPaymentMatch{{SourceVersionID: 1, OrderID: "fixture", RuleID: "v11", PaidMinor: &amount, Status: "MATCHED"}}
	if err := json.Unmarshal([]byte(`[{"itemKey":"chair","label":"董事长","amountMinor":16800}]`), &rows[0].FixedParts); err != nil {
		t.Fatal(err)
	}
	applyVIPQuotes(rows, data)
	got := rows[0]
	if got.FixedParts == nil || len(got.FixedParts) != 0 {
		t.Fatalf("expected an empty fixed-rate preview once configured benefits are available: %+v", got.FixedParts)
	}
	if got.SalesItem != "creator-sales" || got.SalesStatus != "CALCULATED" || len(got.SalesParts) != 1 ||
		got.SalesParts[0].Amount != 252000 || got.SalesParts[0].Person != "seller" {
		t.Fatalf("direct sales quote: %+v", got)
	}
	expected := map[string]struct {
		amount int64
		target string
	}{"rnd": {16800, "rnd"}, "chair": {16800, "chair"}, "owner": {50400, "owner"},
		"technology": {33600, "tech"}, "delivery": {50400, "team"}}
	if len(got.BenefitParts) != len(expected) {
		t.Fatalf("expected all five additional benefits: %+v", got.BenefitParts)
	}
	for _, part := range got.BenefitParts {
		want, ok := expected[part.Item]
		if !ok || part.Status != "CALCULATED" || part.Amount != want.amount || part.Party != want.target {
			t.Fatalf("benefit %s: %+v", part.Item, part)
		}
		if part.Item == "delivery" && (part.Kind != "pool" || part.Owner != "lead" || part.Unit != "dept") {
			t.Fatalf("delivery pool: %+v", part)
		}
		delete(expected, part.Item)
	}
}

func TestCreatorRejectsAmbiguousSalesAndUnrecognizedSplit(t *testing.T) {
	for _, body := range []json.RawMessage{
		creatorBenefitDefinition(creatorActionsFixture+","+creatorSalesFixture, ""),
		creatorBenefitDefinition(creatorActionsFixture, `"internal_sales_direct_split":{"mode":"UNKNOWN","sales_item_key":"creator-sales"},`),
		creatorBenefitDefinition(strings.Replace(creatorActionsFixture, `"role":"SALES"`, `"role":"SALES","pool_party_id":"unrelated"`, 1), ""),
		creatorBenefitDefinition(strings.Replace(creatorActionsFixture, `"item_key":"owner"`, `"item_key":"creator-sales"`, 1), ""),
		creatorBenefitDefinition(strings.Replace(creatorActionsFixture, `"type":"RATE_TABLE"`, `"type":"RATE"`, 1), ""),
	} {
		if _, ok := readVIPSalesPolicy(body); ok {
			t.Error("unsupported split or duplicate sales action accepted")
		}
		if _, ok := readVIPBenefits(body); ok {
			t.Error("invalid sales interpretation accepted by benefit parser")
		}
	}
}

func TestCreatorCommissionCalculationDoesNotEnableRatePublication(t *testing.T) {
	if _, _, err := catalogRateActions(creatorBenefitDefinition(creatorActionsFixture, "")); !errors.Is(err, errCatalogRateUnavailable) {
		t.Fatalf("unavailable product rate publication error = %v", err)
	}
}
