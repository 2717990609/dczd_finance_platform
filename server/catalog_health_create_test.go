package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCreateHealthScenarioUsesItsOwnRecognitionAndRefreshesDrafts(t *testing.T) {
	t.Run("role owner", func(t *testing.T) { verifyHealthScenarioRecognition(t, false) })
	t.Run("named owner", func(t *testing.T) { verifyHealthScenarioRecognition(t, true) })
}

func verifyHealthScenarioRecognition(t *testing.T, namedOwner bool) {
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
	store := func(sourceProduct, rowID string, paid, complete time.Time, hasSales bool) {
		t.Helper()
		wire := healthCompletionReply(sourceProduct, observed, complete, "42")
		for i, raw := range wire.Facts {
			content := strings.NewReplacer("order-42", "order-"+rowID, `"42"`, `"`+rowID+`"`).Replace(string(raw))
			if !paid.IsZero() {
				content = strings.ReplaceAll(content, "2026-08-02T12:00:00+08:00", paid.Format(time.RFC3339))
			}
			if sourceProduct == "HEALTH_AMBASSADOR" {
				content = strings.Replace(content, `"1680000"`, `"3980000"`, 1)
			}
			if hasSales {
				content = strings.TrimSuffix(content, "}") + `,"attribution_snapshot":{"participants":[
				 {"source_role_code":"PID_USER","source_user_id":"seller","source_organization_id":"company","source_sales_relationship_code":"DIRECT"}]}}`
			}
			wire.Facts[i] = []byte(content)
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
	store("HEALTH_CREATOR", "42", time.Time{}, observed, true)
	store("HEALTH_AMBASSADOR", "43", time.Time{}, time.Time{}, true)
	if _, err := s.db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
		t.Fatal(err)
	}
	actor := financeRuleActor{permits: func(int64, bool) bool { return true }}
	var creatorRule, creatorSales string
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
		if tc.compound {
			creatorRule, creatorSales = published.ID, sheet.Components[0].ItemKey
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
	rate := int64(1000)
	cut := time.Date(2026, 8, 3, 0, 0, 0, 0, shanghai)
	independent, err := s.publishCatalogRates(ctx, publishCatalogRatesInput{BaseID: creatorRule, EffectiveFrom: cut,
		RemovedItems: []string{creatorSales}, NewItems: []catalogNewBenefitItem{
			{Label: "直接推荐", catalogBenefitTarget: catalogBenefitTarget{Target: "referrer"},
				catalogBenefitCalculation: catalogBenefitCalculation{Rate: &rate}},
		}}, "fixture")
	if err != nil || independent.Version != 2 {
		t.Fatalf("could not publish a referral-only creator rule: %+v %v", independent, err)
	}
	store("HEALTH_CREATOR", "44", cut.Add(36*time.Hour), observed, false)
	const company = "ab010000-0000-4000-8000-000000000031"
	const pool = "ab010000-0000-4000-8000-000000000032"
	if _, err := s.db.Exec(ctx, `WITH company AS (
	 INSERT INTO organization_units(id,kind,name,status) VALUES($2,'COMPANY','公司收款验收','ACTIVE') RETURNING id
	), pool AS (
	 INSERT INTO organization_units(id,parent_id,kind,name,status)
	 SELECT $3,id,'DEPARTMENT','团队池归集验收','ACTIVE' FROM company RETURNING id
	), owners AS (INSERT INTO people(name) VALUES('原团队池负责人'),('接任团队池负责人') RETURNING id,name),
	 aliases AS (
	 INSERT INTO catalog_benefit_organizations(source,party_uuid,unit_id,observed_at)
	 SELECT 'DCAI',id,id,$1::timestamptz FROM company UNION ALL SELECT 'DCAI',id,id,$1::timestamptz FROM pool
	)
	 INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,valid_to,observed_at)
	 SELECT gen_random_uuid(),'DCAI',owners.id,'TEAM_LEAD','ORGANIZATION_UNIT',pool.id::text,
	  CASE WHEN name='原团队池负责人' THEN $1 ELSE $4::timestamptz END,CASE WHEN name='原团队池负责人' THEN $4 END,$1
	 FROM pool CROSS JOIN owners`, effective, company, pool, observed.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE catalog_sales_context SET benefit_ready=true WHERE source='DCAI'`); err != nil {
		t.Fatal(err)
	}
	sheet, err := s.catalogRuleRateSheet(ctx, independent.ID)
	if err != nil || len(sheet.Components) != 1 || sheet.Components[0].TargetKind != "referrer" {
		t.Fatalf("expected the referral-only version before publishing organization benefits: %+v %v", sheet, err)
	}
	fixed, poolRate := int64(20000), int64(200)
	poolTarget := catalogBenefitTarget{Target: "pool", Organization: pool, Role: "TEAM_LEAD"}
	if namedOwner {
		poolTarget.Role, poolTarget.MemberRole = "", "DELIVERY_TEAM"
		if err := s.db.QueryRow(ctx, "SELECT id FROM people WHERE name='原团队池负责人'").Scan(&poolTarget.OwnerPersonID); err != nil {
			t.Fatal(err)
		}
	}
	organizationRule, err := s.publishCatalogRates(ctx, publishCatalogRatesInput{
		BaseID: independent.ID, EffectiveFrom: cut.Add(72 * time.Hour), RemovedItems: []string{sheet.Components[0].ItemKey},
		NewItems: []catalogNewBenefitItem{
			{Label: "公司固定金额", catalogBenefitTarget: catalogBenefitTarget{Target: "company", Organization: company},
				catalogBenefitCalculation: catalogBenefitCalculation{Fixed: &fixed}},
			{Label: "交付池", catalogBenefitTarget: poolTarget,
				catalogBenefitCalculation: catalogBenefitCalculation{Rate: &poolRate}},
		}}, "fixture")
	if err != nil || organizationRule.Version != 3 {
		t.Fatalf("could not publish a company and pool rule without sales: %+v %v", organizationRule, err)
	}
	if namedOwner {
		saved, err := s.catalogRuleRateSheet(ctx, organizationRule.ID)
		if err != nil || len(saved.Components) != 2 || saved.Components[1].PoolOwner == nil ||
			saved.Components[1].PoolOwner.ID != poolTarget.OwnerPersonID || saved.Components[1].TargetRole != "" {
			t.Fatalf("stored pool owner should reopen as the selected person: %+v %v", saved, err)
		}
	}
	store("HEALTH_CREATOR", "45", cut.Add(84*time.Hour), observed, false)
	store("HEALTH_CREATOR", "46", cut.Add(96*time.Hour), observed, false)
	half := int64(840000)
	refund := func(record string, at time.Time) sourceFact {
		order := "order-" + record
		return sourceFact{Source: "DCAI", Domain: "HEALTH_IDENTITY", SourceKey: "HEALTH_CREATOR", Kind: "REFUND_SUCCEEDED",
			OrderID: order, RecordKey: "ai_health_ambassador_order:" + record, RefundID: record,
			Version: "refund", AmountMinor: &half, OccurredAt: at, UpdatedAt: at,
			RefundsPayment: []sourcePaymentReference{{OrderID: order, RecordID: record}}}
	}
	putRefunds := func(facts ...sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, facts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	putRefunds(refund("45", time.Date(2026, 8, 20, 12, 0, 0, 0, shanghai)))
	matches := httptest.NewRecorder()
	s.catalogMatches(matches, httptest.NewRequest("GET", "/api/v1/period-rule-matches?periodId="+strconv.FormatInt(creatorCompleted, 10), nil))
	var report catalogMatchReport
	if err := json.Unmarshal(matches.Body.Bytes(), &report); err != nil || matches.Code != 200 {
		t.Fatalf("could not read creator quotes: %d %v", matches.Code, err)
	}
	i := slices.IndexFunc(report.Items, func(match catalogPaymentMatch) bool { return match.OrderID == "order-45" })
	if i < 0 {
		t.Fatal("the partially refunded order should be recognized in its completion period")
	}
	match := report.Items[i]
	if match.RecognizedMinor == nil || *match.RecognizedMinor != half || len(match.BenefitParts) != 2 ||
		match.BenefitParts[0].Kind != "company" || match.BenefitParts[0].Amount != 10000 ||
		match.BenefitParts[1].Kind != "pool" || match.BenefitParts[1].Amount != 16800 ||
		match.BenefitParts[1].OwnerName != "原团队池负责人" {
		t.Fatalf("first quote should use the net payment and the configured recipients: %+v", match)
	}
	for _, tc := range []struct{ period, total int64 }{
		{creatorPaid, 0}, {creatorCompleted, 248400}, {ambassadorPaid, 398000},
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
		if tc.period == creatorCompleted {
			if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`,
				tc.period, trial.(draftSummary).Version); err != nil {
				t.Fatal(err)
			}
		}
	}
	readPools := func(period int64) vipPoolReport {
		t.Helper()
		res := httptest.NewRecorder()
		s.periodPoolContributions(res, httptest.NewRequest("GET", "/api/v1/period-pool-contributions?periodId="+strconv.FormatInt(period, 10), nil))
		var report vipPoolReport
		if err := json.Unmarshal(res.Body.Bytes(), &report); err != nil || res.Code != 200 {
			t.Fatalf("could not read pool totals: %d %v", res.Code, err)
		}
		return report
	}
	if got := readPools(creatorPaid); got.PaymentCount != 4 || len(got.Pools) != 0 || got.PendingPaymentCount != 0 {
		t.Fatalf("completed-in-another-period orders should not create pool contributions or issues: %+v", got)
	}
	if got := readPools(creatorCompleted); len(got.Pools) != 1 || got.PendingPaymentCount != 0 ||
		got.Pools[0].Amount != 50400 || got.Pools[0].PaymentCount != 2 {
		t.Fatalf("completed orders should contribute their recognized principal to the configured pool: %+v", got)
	}

	posting := createPeriod(creatorID, "2026-10")
	refundedAt := time.Date(2026, 10, 3, 12, 0, 0, 0, shanghai)
	putRefunds(refund("42", refundedAt), refund("44", refundedAt), refund("46", refundedAt))
	trial, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: posting})
	if err != nil {
		t.Fatal(err)
	}
	if got := trial.(draftSummary); got.RefundTotal != -110800 || got.RefundCount != 3 || got.IssueCount != 0 {
		var issue string
		_ = s.db.QueryRow(ctx, "SELECT code FROM calculation_draft_issues WHERE period_id=$1 LIMIT 1", posting).Scan(&issue)
		t.Fatalf("only orders that earned commission should reverse commission: %+v %s", got, issue)
	}
	var reversedOrders []string
	var reversedPrincipal int64
	if err := s.db.QueryRow(ctx, `SELECT array_agg(p.order_id ORDER BY p.order_id),sum((r.fact->>'amountMinor')::bigint)
	 FROM calculation_draft_performance a JOIN source_order_versions r ON r.id=a.source_version_id
	 JOIN source_order_versions p ON p.id=a.payment_version_id
	 JOIN calculation_draft_performance original ON original.source_version_id=p.id AND original.period_id=$1
	 WHERE a.period_id=$2 AND p.order_id=r.order_id AND original.completion_version_id IS NOT NULL`,
		creatorCompleted, posting).Scan(&reversedOrders, &reversedPrincipal); err != nil ||
		!slices.Equal(reversedOrders, []string{"order-42", "order-44", "order-46"}) || reversedPrincipal != 3*half {
		t.Fatalf("later refunds must reverse recognized performance from their exact completed orders: %v %d %v",
			reversedOrders, reversedPrincipal, err)
	}
	var companyDebit, poolDebit int64
	var owner string
	if err := s.db.QueryRow(ctx, `SELECT min(r.amount_minor) FILTER(WHERE l.beneficiary_unit_id=$2),
	 min(r.amount_minor) FILTER(WHERE l.beneficiary_unit_id=$3),
	 min(p.name) FILTER(WHERE l.beneficiary_unit_id=$3)
	 FROM calculation_draft_refunds r JOIN calculation_draft_lines l ON l.id=r.original_line_id
	 LEFT JOIN people p ON p.id=l.allocation_owner_id WHERE r.period_id=$1 AND l.catalog_rule_id=$4`,
		posting, company, pool, organizationRule.ID).Scan(&companyDebit, &poolDebit, &owner); err != nil ||
		companyDebit != -10000 || poolDebit != -16800 || owner != "原团队池负责人" {
		t.Fatalf("refund must reverse the original company and pool owner: %d %d %s %v", companyDebit, poolDebit, owner, err)
	}

	putRefunds(refund("99", refundedAt))
	trial, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: posting})
	if err != nil || trial.(draftSummary).IssueCount != 1 {
		t.Fatalf("refunds without recognized performance must remain pending: %+v %v", trial, err)
	}
	var issue string
	if err := s.db.QueryRow(ctx, `SELECT i.code FROM calculation_draft_issues i
	 JOIN source_order_versions r ON r.id=i.source_version_id WHERE i.period_id=$1 AND r.order_id='order-99'`,
		posting).Scan(&issue); err != nil || issue != "REFUND_PERFORMANCE_PAYMENT_MISSING" {
		t.Fatalf("expected a missing original for the unmatched refund: %q %v", issue, err)
	}
}
