package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestCreatorRatePublicationRefreshesOpenCompletionPeriods(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	boundary := time.Date(2026, 8, 25, 0, 0, 0, 0, shanghai)
	cut := boundary.Add(-12 * time.Hour)
	definition := creatorBenefitDefinition(creatorActionsFixture, strings.TrimSuffix(strings.TrimPrefix(completionTestPolicy, "{"), "}")+",")
	product := creatorTestProduct(boundary, definition)
	input := productConfigFile{Environment: "test", CapturedAt: boundary, Products: []productConfigEntry{product}}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, input) }); err != nil {
		t.Fatal(err)
	}
	var productID int64
	if err := s.db.QueryRow(ctx, "SELECT id FROM products WHERE code='CREATOR'").Scan(&productID); err != nil {
		t.Fatal(err)
	}
	createPeriod := func(month string) int64 {
		t.Helper()
		result, err := s.createPeriod(ctx, createPeriodInput{ProductID: productID, Name: month, SettlementMonth: month})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	august, september, october := createPeriod("2026-08"), createPeriod("2026-09"), createPeriod("2026-10")
	createPeriod("2026-11")
	if _, err := s.db.Exec(ctx, `INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	 VALUES($1,1,decode('00','hex'),0,0,0,now());`, august); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,1,'fixture')`, august); err != nil {
		t.Fatal(err)
	}
	write := func(rowID string, paid, completed time.Time) {
		t.Helper()
		wire := healthCompletionReply("HEALTH_CREATOR", boundary.AddDate(0, 2, 1), completed, "42")
		wire.Cursor.ID = rowID
		for i := range wire.Facts {
			wire.Facts[i] = json.RawMessage(strings.NewReplacer(`"42"`, `"`+rowID+`"`, "order-42", "order-"+rowID,
				"2026-08-02T12:00:00+08:00", paid.Format(time.RFC3339)).Replace(string(wire.Facts[i])))
		}
		wire.Gates[0].OrderID = "order-" + rowID
		row, err := normalizeDCAIOrderRow(dcaiHealthStream, time.Time{}, wire)
		if err != nil {
			t.Fatal(err)
		}
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := storeSourceFacts(ctx, tx, row.Facts); err != nil {
				return err
			}
			return storeDCAIBlockers(ctx, tx, dcaiHealthStream, []orderPageRow{row})
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("41", cut.Add(-time.Second), boundary)
	write("42", cut, boundary.Add(-time.Second))
	write("43", cut.Add(time.Second), boundary.AddDate(0, 1, 0))
	if _, err := s.db.Exec(ctx, `DELETE FROM draft_refresh_queue`); err != nil {
		t.Fatal(err)
	}
	base := product.Packages[0].Versions[0].SourceID
	sheet, err := s.catalogRuleRateSheet(ctx, base)
	if err != nil || !sheet.MayPublish || !sheet.RequiresCompletion || len(sheet.Components) != 6 || len(sheet.Teams) != 0 {
		t.Fatalf("creator rate configuration unavailable: %+v %v", sheet, err)
	}
	var catalog []struct {
		RateConfigAvailable bool `json:"rateConfigAvailable"`
	}
	if err := s.db.QueryRow(ctx, catalogRulesSQL, []int64{productID}).Scan(&catalog); err != nil || len(catalog) != 2 || !catalog[0].RateConfigAvailable || !catalog[1].RateConfigAvailable {
		t.Fatalf("creator rule list did not enable rate configuration: %+v %v", catalog, err)
	}
	rate := int64(200)
	edits := publishCatalogRatesInput{BaseID: base, EffectiveFrom: cut, Changes: []catalogRateChange{{ItemKey: "rnd", Rate: &rate}}}
	published, err := s.publishCatalogRates(ctx, edits, "fixture-publisher")
	if err != nil || published.Version != 3 || published.ID == base {
		t.Fatalf("could not publish creator rule: %+v %v", published, err)
	}
	if _, err := s.publishCatalogRates(ctx, edits, "fixture-publisher"); !errors.Is(err, errCatalogRateStale) {
		t.Fatalf("an outdated creator form accepted a second edit: %v", err)
	}
	var queued []int64
	if err := s.db.QueryRow(ctx, `SELECT array_agg(period_id ORDER BY period_id) FROM draft_refresh_queue`).Scan(&queued); err != nil ||
		!slices.Equal(queued, []int64{september, october}) {
		t.Fatalf("publication should refresh the two open completion periods: %v %v", queued, err)
	}
	for _, period := range []int64{september, october} {
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			report, err := readCatalogMatches(ctx, tx, period, 0, 25)
			if err != nil {
				return err
			}
			want := 1
			if period == september {
				want = 2
			}
			if report.OrderCount != want {
				t.Fatalf("period %d should have %d orders: %+v", period, want, report)
			}
			for _, row := range report.Items {
				wantRule := published.ID
				wantRND := int64(33600)
				if row.OrderID == "order-41" {
					wantRule = base
					wantRND = 16800
				}
				if row.Status != "MATCHED" || row.RuleID != wantRule || row.CompletionCarried != (row.OrderID == "order-42") {
					t.Fatalf("period %d payment %s picked the wrong rule or posting period: %+v", period, row.OrderID, row)
				}
				var amount int64
				for _, part := range row.FixedParts {
					if part.ItemKey == "rnd" {
						amount = part.AmountMinor
					}
				}
				if amount != wantRND {
					t.Fatalf("order %s RND amount %d, want %d", row.OrderID, amount, wantRND)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(ctx, `UPDATE catalog_business_routes SET sku_code='unrelated-scenario' WHERE product_id=$1`, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.catalogRuleRateSheet(ctx, base); !errors.Is(err, errCatalogRateUnavailable) {
		t.Fatalf("a rule without its health scenario was readable: %v", err)
	}
	if _, err := s.publishCatalogRates(ctx, edits, "fixture-publisher"); !errors.Is(err, errCatalogRateUnavailable) {
		t.Fatalf("a rule without its health scenario was editable: %v", err)
	}
}
