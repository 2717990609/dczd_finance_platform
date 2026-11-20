package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCreateHealthScenarioUsesItsOwnRecognitionAndRefreshesDrafts(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	effective := time.Date(2026, 7, 25, 0, 0, 0, 0, shanghai)
	creator := creatorTestProduct(effective, nil)
	creator.Packages = nil
	ambassador := creator
	ambassador.SourceID = "ab010000-0000-4000-8000-000000000021"
	ambassador.Code, ambassador.Name, ambassador.SourceKey = "AMBASSADOR", "健康大使", "ambassador"
	ambassador.BusinessRoutes = slices.Clone(creator.BusinessRoutes)
	route := &ambassador.BusinessRoutes[0]
	route.SourceID, route.SKU, route.Label = "ab010000-0000-4000-8000-000000000022", "ambassador-sku", "大使首购"
	route.SourceProductCode, route.BusinessKey = "HEALTH_AMBASSADOR", "dcai:health-ambassador:new"
	config := productConfigFile{Environment: "test", CapturedAt: effective, Products: []productConfigEntry{creator, ambassador}}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return storeProductConfig(ctx, tx, config) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `WITH seller AS (INSERT INTO people(name) VALUES('健康项目销售') RETURNING id),
	 context AS (INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest)
	     VALUES('DCAI',$1,sha256('fixture'::bytea)) RETURNING captured_at),
	 identity AS (INSERT INTO identity_mappings(person_id,source,external_id,valid_from)
	     SELECT seller.id,'DCAI','seller',$1 FROM seller)
	 INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
	 SELECT gen_random_uuid(),'DCAI',seller.id,'SALES','PROJECT','DCAI',$1,context.captured_at FROM seller CROSS JOIN context`, effective); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `WITH company AS (
	 INSERT INTO organization_units(id,kind,name,status) VALUES(gen_random_uuid(),'COMPANY','健康项目样本公司','ACTIVE') RETURNING id
	) INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
	 SELECT 'DCAI','company',id,$1 FROM company`, effective); err != nil {
		t.Fatal(err)
	}
	createPeriod := func(product int64, month string) int64 {
		t.Helper()
		created, err := s.createPeriod(ctx, createPeriodInput{ProductID: product, SettlementMonth: month, Name: month})
		if err != nil {
			t.Fatal(err)
		}
		return created.(map[string]int64)["id"]
	}
	var creatorID, ambassadorID int64
	for code, id := range map[string]*int64{creator.Code: &creatorID, ambassador.Code: &ambassadorID} {
		if err := s.db.QueryRow(ctx, "SELECT id FROM products WHERE code=$1", code).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	creatorPaid := createPeriod(creatorID, "2026-08")
	creatorCompleted := createPeriod(creatorID, "2026-09")
	ambassadorPaid := createPeriod(ambassadorID, "2026-08")
	observed := time.Date(2026, 9, 3, 9, 0, 0, 0, shanghai)
	store := func(sourceProduct, rowID string, complete time.Time) {
		t.Helper()
		wire := healthCompletionReply(sourceProduct, observed, complete, "42")
		for i, raw := range wire.Facts {
			content := strings.NewReplacer("order-42", "order-"+rowID, `"42"`, `"`+rowID+`"`).Replace(string(raw))
			if sourceProduct == "HEALTH_AMBASSADOR" {
				content = strings.Replace(content, `"1680000"`, `"3980000"`, 1)
			}
			wire.Facts[i] = []byte(strings.TrimSuffix(content, "}") + `,"attribution_snapshot":{"participants":[
			 {"source_role_code":"PID_USER","source_user_id":"seller","source_organization_id":"company","source_sales_relationship_code":"DIRECT"}]}}`)
		}
		wire.Cursor.ID, wire.Gates[0].OrderID = rowID, "order-"+rowID
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
	store("HEALTH_CREATOR", "42", observed)
	store("HEALTH_AMBASSADOR", "43", time.Time{})
	if _, err := s.db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
		t.Fatal(err)
	}
	actor := financeRuleActor{permits: func(int64, bool) bool { return true }}
	for _, tc := range []struct {
		product  int64
		sku      string
		compound bool
	}{
		{creatorID, "creator-sku", true}, {ambassadorID, "ambassador-sku", false},
	} {
		options, err := s.catalogScenarioOptions(ctx, strconv.FormatInt(tc.product, 10), actor)
		if err != nil || len(options.SKUs) != 1 || options.SKUs[0].Code != tc.sku {
			t.Fatalf("mapped health SKU must be offered in the editor: %+v %v", options.SKUs, err)
		}
		rate := int64(1000)
		input := publishCatalogRatesInput{EffectiveFrom: effective, Create: &catalogScenarioCreate{ProductID: tc.product,
			catalogScenarioCopy: catalogScenarioCopy{SKU: tc.sku, Label: "首版"}, SalesRates: []catalogConditionInput{{Company: "ANY", Relationship: "ANY", Rate: &rate}}}}
		published, err := s.publishCatalogRates(ctx, input, "fixture")
		if err != nil || published.Version != 1 {
			t.Fatalf("could not publish first rule for %s: %+v %v", tc.sku, published, err)
		}
		sheet, err := s.catalogRuleRateSheet(ctx, published.ID)
		if err != nil || !sheet.MayPublish || sheet.RequiresCompletion != tc.compound {
			t.Fatalf("first published version is not editable or has the wrong trigger: %+v %v", sheet, err)
		}
		if _, err := s.publishCatalogRates(ctx, input, "fixture"); !errors.Is(err, errCatalogScenarioOccupied) {
			t.Fatalf("duplicate health rule publication: %v", err)
		}
	}
	var queued []int64
	if err := s.db.QueryRow(ctx, "SELECT array_agg(period_id ORDER BY period_id) FROM draft_refresh_queue").Scan(&queued); err != nil ||
		!slices.Equal(queued, []int64{creatorPaid, creatorCompleted, ambassadorPaid}) {
		t.Fatalf("first publication must queue payment and applicable completion periods: %v %v", queued, err)
	}
	for _, tc := range []struct{ period, total int64 }{
		{creatorPaid, 0}, {creatorCompleted, 168000}, {ambassadorPaid, 398000},
	} {
		trial, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: tc.period})
		if err != nil {
			t.Fatal(err)
		}
		if got := trial.(draftSummary); got.Total != tc.total || got.IssueCount != 0 {
			var issue string
			_ = s.db.QueryRow(ctx, "SELECT code FROM calculation_draft_issues WHERE period_id=$1 LIMIT 1", tc.period).Scan(&issue)
			t.Fatalf("period %d expected total %d, got %+v %s", tc.period, tc.total, got, issue)
		}
	}
}
