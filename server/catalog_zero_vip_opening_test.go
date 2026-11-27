package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCatalogZeroVIPOpeningSalesReview(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	at := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	zero, paid := "0", "49900"
	empty := `,"attribution_snapshot":{"participants":[]}`
	hasSeller := `,"attribution_snapshot":{"participants":[{"source_role_code":"PID_USER","source_user_id":"unmapped"}]}`
	cases := []struct {
		name, source, method, amount, participants, provider string
		wantBlocking, wantAdvisory, wantIgnored              bool
		parentProduct                                        string
	}{
		{name: "VIP referral gift", source: "6", method: "0", amount: zero, participants: empty, wantIgnored: true},
		{name: "ambassador zero opening with seller", source: "5", method: "1", amount: zero, participants: hasSeller, wantIgnored: true},
		{name: "ambassador client claim", source: "14", method: "1", amount: zero, participants: hasSeller, wantIgnored: true},
		{name: "backend gift with ambassador parent", source: "4", method: "3", amount: zero, participants: empty, parentProduct: "HEALTH_AMBASSADOR", wantIgnored: true},
		{name: "backend gift with creator parent", source: "4", method: "3", amount: zero, participants: empty, parentProduct: "HEALTH_CREATOR", wantBlocking: true},
		{name: "manual opening", source: "1", method: "3", amount: zero, participants: empty, wantAdvisory: true},
		{name: "batch opening with unresolved sales", source: "2", method: "3", amount: zero, participants: hasSeller, wantAdvisory: true},
		{name: "paid purchase without seller", source: "0", method: "2", amount: paid, participants: empty, provider: "wx"},
		{name: "paid purchase with unresolved seller", source: "0", method: "2", amount: paid, participants: hasSeller, provider: "wx", wantBlocking: true},
		{name: "missing attribution snapshot", source: "0", method: "2", amount: paid, provider: "wx", wantBlocking: true},
		{name: "unclassified zero purchase", source: "0", method: "3", amount: zero, participants: empty, wantBlocking: true},
		{name: "positive health order still earns", source: "7", method: "2", amount: paid, participants: empty, provider: "wx"},
	}
	const sku = "VIP499"
	rule := catalogAggregateRule{ID: "rule", From: at, Definition: catalogPeriodDefinition{
		Scenarios: []string{sku},
		Items: []catalogPeriodItem{
			{ItemKey: "rate", TargetKind: "order_seller", Tiers: []catalogPeriodTier{{Rate: 1000}}},
			{ItemKey: "sales2", TargetKind: "order_seller", Tiers: []catalogPeriodTier{{Rate: 500}}},
			{ItemKey: "independent", TargetKind: "person", PersonID: 7, Tiers: []catalogPeriodTier{{Rate: 1000}}},
		},
	}}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			order, recordID := fmt.Sprintf("VIP%d", i), fmt.Sprint(i+1)
			payload := func(source string) sourceFact {
				t.Helper()
				fact, err := normalizeFact("DCAI", order, json.RawMessage(fmt.Sprintf(`{
"fact_schema_version":"source.order-fact/1.0","fact_type":"ORDER_PAID","occurred_at":%[1]q,
"order":{"source_order_id":%[2]q,"order_no":%[2]q,"source_status":{"code":"1"}},
"source_record":{"source_table":"ai_vip_order","record_id":%[8]q,"record_version":"v1","updated_at":%[1]q},
"business":{"source_domain_code":"VIP","source_entity_type_code":"VIP_ORDER","source_product_code":"VIP",
"classification_fields":{"order_source":%[3]s}},
"payment":{"source_payment_id":%[2]q,"method_code":%[4]q,"provider_transaction_id":%[5]q},
"amounts":{"paid_minor":%[6]q}%[7]s}`, at.Format(time.RFC3339), order, source, tc.method, tc.provider, tc.amount, tc.participants, recordID)))
				if err != nil {
					t.Fatal(err)
				}
				return fact
			}
			fact := payload(tc.source)
			if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
				// An existing version may have been imported before the source type was recorded.
				for _, fact := range []sourceFact{payload("null"), fact, fact} {
					if _, err := storeSourceFacts(ctx, tx, []sourceFact{fact}); err != nil {
						return err
					}
				}
				if tc.parentProduct != "" {
					parentID := "parent-" + order
					parent, err := normalizeFact("DCAI", parentID, json.RawMessage(fmt.Sprintf(`{
"fact_schema_version":"source.order-fact/1.0","fact_type":"ORDER_PAID","occurred_at":%[1]q,
"order":{"source_order_id":%[2]q,"order_no":%[2]q},
"source_record":{"source_table":"ai_health_ambassador_order","record_id":%[2]q,"record_version":"v1","updated_at":%[1]q},
"business":{"source_domain_code":"HEALTH_IDENTITY","source_entity_type_code":"HEALTH_IDENTITY_ORDER",
"source_product_code":%[3]q,"classification_fields":{"gift_vip_order_id":%[4]q}},
"payment":{"source_payment_id":%[2]q},"amounts":{"paid_minor":"99900"}}`, at.Format(time.RFC3339), parentID, tc.parentProduct, recordID)))
					if err != nil {
						return err
					}
					if _, err := storeSourceFacts(ctx, tx, []sourceFact{parent}); err != nil {
						return err
					}
				}
				var id int64
				if err := tx.QueryRow(ctx, "SELECT id FROM source_order_versions WHERE order_id=$1", order).Scan(&id); err != nil {
					return err
				}
				events := []catalogActivityEvent{{EventID: id, PaymentID: id, Amount: *fact.AmountMinor,
					PaidAt: at, Scenario: sku, selection: &catalogAggregateMatch{rule: &rule}}}
				performance, err := readCatalogPerformanceDirectory(ctx, tx, events)
				if err != nil {
					return err
				}
				report, err := groupCatalogPeriodEvents(events, []catalogAggregateRule{rule}, catalogAggregateDirectory{People: map[int64]string{7: "独立分润对象"}},
					nil, performance, at.AddDate(0, 1, 0))
				if err != nil {
					return err
				}
				// Both sales items fail on the same unresolved attribution, but zero back-office
				// orders retain at most one advisory and never enter the blocking count.
				var blocking, advisory int
				for _, issue := range report.postingIssues {
					if issue.Advisory {
						advisory++
					} else {
						blocking++
					}
				}
				if (blocking != 0) != tc.wantBlocking || (advisory == 1) != tc.wantAdvisory ||
					(len(report.events) == 0) != tc.wantIgnored {
					t.Fatalf("unexpected result: issues %v, source events %d", report.postingIssues, len(report.events))
				}
				if tc.amount == paid {
					if len(report.Groups) != 1 || report.Groups[0].quote().Total != 4990 {
						t.Fatalf("independent accrual: %+v", report.Groups)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVIPPaidWithoutSellerKeepsIndependentBenefit(t *testing.T) {
	const rule = "rule"
	paid := int64(49900)
	rate := int64(1000)
	at := time.Date(2026, 6, 3, 8, 0, 0, 0, time.UTC)
	policies := vipQuotePolicies{
		sales: map[string]vipSalesPolicy{rule: {Item: "sales"}},
		benefits: map[string]vipBenefitPolicy{rule: {actions: []vipBenefitAction{
			{Item: "seller", Mode: vipOrderSellerMode, Rate: &rate},
			{Item: "independent", Mode: "PARTY", Designated: "owner", Rate: &rate},
		}}},
	}
	data := vipRuntimeData{
		Claims:       map[int64][]vipClaim{1: {}, 2: {{Role: "PID_USER", User: "unmapped"}}},
		SellerAbsent: map[int64]bool{1: true}, BenefitReady: true,
		vipSalesData: vipSalesData{People: map[string]string{"owner": "负责人"}},
	}
	for _, id := range []int64{1, 2, 3} {
		row := catalogPaymentMatch{SourceVersionID: id, RuleID: rule, PaidMinor: &paid, PaidAt: at, Status: "MATCHED"}
		req, issue := vipPaymentInputs(row, data)
		policies.apply(&row, data, req, issue)
		if len(row.BenefitParts) != 2 || row.BenefitParts[1].Status != "CALCULATED" || row.BenefitParts[1].Amount != 4990 {
			t.Fatalf("order %d: independent benefit was not calculated: %+v", id, row.BenefitParts)
		}
		if id == 1 {
			if row.SalesStatus != vipSalesNotConfigured || row.BenefitParts[0].Status != vipCommissionOutsideScope {
				t.Fatalf("explicitly seller-free order has sales review: %s %+v", row.SalesStatus, row.BenefitParts[0])
			}
		} else if row.BenefitParts[0].Status == vipCommissionOutsideScope || row.SalesStatus == vipSalesNotConfigured {
			t.Fatalf("order %d lost an attribution problem: %s %+v", id, row.SalesStatus, row.BenefitParts[0])
		}
	}
}
