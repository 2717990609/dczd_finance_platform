package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCompanyPolicyUsesPersonalCountAndPerformanceAcrossCompanies(t *testing.T) {
	config := practiceSalesTiers{CountFromOrdinal: 1, CompanySchedules: []practiceCompanySchedule{
		{CompanyID: "first", Count: []practiceTier{{From: 2, Mode: "rate", Value: 1900}},
			Performance: []practiceTier{{Mode: "rate", Value: 1200}, {From: 15000, Mode: "rate", Value: 1300}}},
		{CompanyID: "second", Count: []practiceTier{{From: 2, Mode: "rate", Value: 1920}},
			Performance: []practiceTier{{Mode: "rate", Value: 1220}, {From: 15000, Mode: "rate", Value: 1320}}},
	}}
	at := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	purchases := []practiceTierPurchase{
		{EnrollmentID: 1, SequenceKey: "11", PaidAt: at, PersonID: 9, Base: 10000, CompanyID: "first"},
		{EnrollmentID: 2, SequenceKey: "12", PaidAt: at, PersonID: 9, Base: 10000, CompanyID: "second"},
		{EnrollmentID: 3, SequenceKey: "13", PaidAt: at, PersonID: 9, Base: 10000, CompanyID: "second"},
		{EnrollmentID: 4, SequenceKey: "14", PaidAt: at, PersonID: 8, Base: 10000, CompanyID: "second"},
	}
	awards, err := choosePracticeSalesTiers(config, purchases, nil)
	if err != nil || awards[1].Value != 1300 || awards[2].Value != 1320 || awards[3].Value != 1920 || awards[4].Value != 1220 {
		t.Fatalf("awards=%+v error=%v", awards, err)
	}
}

func TestPracticeTierCompaniesCanShareOneSchedule(t *testing.T) {
	var tiers practiceSalesTiers
	err := json.Unmarshal([]byte(`{"countFromOrdinal":1,
	 "performance":[{"from":0,"mode":"rate","value":700}],
	 "companySchedules":[
	  {"companyIds":["first","second"],"count":[{"from":2,"mode":"rate","value":1900}],
	   "performance":[{"from":0,"mode":"rate","value":1200}]},
	  {"companyId":"legacy","performance":[{"from":0,"mode":"rate","value":900}]}],
	 "personSchedules":[{"personId":99,"performance":[{"from":0,"mode":"rate","value":1700}]}]}`), &tiers)
	if err != nil {
		t.Fatal(err)
	}
	part := component{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER", Tiers: &tiers}
	if !validPracticeSalesTiers(part) {
		t.Fatal("expected a shared company schedule and an older single-company schedule to be valid together")
	}
	at := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	purchases := []practiceTierPurchase{
		{EnrollmentID: 1, SequenceKey: "11", PaidAt: at, PersonID: 9, Base: 10000, CompanyID: "first"},
		{EnrollmentID: 2, SequenceKey: "12", PaidAt: at, PersonID: 9, Base: 10000, CompanyID: "second"},
		{EnrollmentID: 3, SequenceKey: "13", PaidAt: at, PersonID: 9, Base: 10000, CompanyID: "first"},
		{EnrollmentID: 4, SequenceKey: "14", PaidAt: at, PersonID: 8, Base: 10000, CompanyID: "other"},
		{EnrollmentID: 5, SequenceKey: "15", PaidAt: at, PersonID: 7, Base: 10000, CompanyID: "legacy"},
		{EnrollmentID: 6, SequenceKey: "16", PaidAt: at, PersonID: 99, Base: 10000, CompanyID: "second"},
	}
	got, err := choosePracticeSalesTiers(tiers, purchases, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{1200, 1200, 1900, 700, 900, 1700} {
		if actual := got[int64(i+1)].Value; actual != want {
			t.Fatalf("enrollment %d matched value %d, want %d", i+1, actual, want)
		}
	}
	tiers.CompanySchedules[1].CompanyID = "second"
	if validPracticeSalesTiers(part) {
		t.Fatal("a company cannot use two competing company schedules")
	}
}

func TestPracticePaymentUsesExplicitSalesCompanyWithoutChangingReferrer(t *testing.T) {
	raw := json.RawMessage(`{"fact_schema_version":"source.order-fact/1.0","fact_type":"ORDER_PAID","occurred_at":"2026-10-01T00:00:00Z","order":{"source_order_id":"o1"},"source_record":{"source_table":"orders","record_id":"r1","record_version":"v1","updated_at":"2026-10-01T00:00:00Z"},"business":{"source_domain_code":"CAMP"},"amounts":{"paid_minor":"10000"},"attribution_snapshot":{"participants":[{"source_role_code":"DIRECT_REFERRER","source_user_id":"person","source_organization_id":"secondary-reference"},{"source_role_code":"DIRECT_COMPANY","source_company_id":"real-company"},{"source_role_code":"SECONDARY_COMPANY","source_company_id":"other-company"}]}}`)
	fact, err := normalizeFact("DCZD", "o1", raw)
	if err != nil || fact.SalesCompanyID != "real-company" || fact.ReferrerID != "person" {
		t.Fatalf("sales attribution: %+v, %v", fact, err)
	}
}

func TestCompanyAttributionFillsExistingVersionAndRejectsConflictingReplay(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	paid := int64(10000)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fact := sourceFact{Source: "DCZD", OrderID: "abc", RecordKey: "orders:abc", Version: "v1", Kind: "ORDER_PAID",
		PaymentID: "abc", Domain: "PRACTICE", AmountMinor: &paid, OccurredAt: at, UpdatedAt: at}
	store := func() (int64, error) {
		var made int64
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			var err error
			made, err = storeSourceFacts(ctx, tx, []sourceFact{fact})
			return err
		})
		return made, err
	}
	if n, err := store(); err != nil || n != 1 {
		t.Fatalf("initial import=%d error=%v", n, err)
	}
	fact.SalesCompanyID = "first"
	if n, err := store(); err != nil || n != 0 {
		t.Fatalf("repeat import=%d error=%v", n, err)
	}
	fact.SalesCompanyID = "second"
	if _, err := store(); !errors.Is(err, conflict) {
		t.Fatalf("changed source version returned %v", err)
	}
	var company string
	var versions int
	if err := db.QueryRow(ctx, `SELECT c.company_id,(SELECT count(*) FROM source_order_versions)
	 FROM source_payment_companies c JOIN source_order_versions v ON v.id=c.source_version_id WHERE v.order_id='abc'`).Scan(&company, &versions); err != nil ||
		company != "first" || versions != 1 {
		t.Fatalf("company=%q versions=%d error=%v", company, versions, err)
	}
	fact.Version, fact.UpdatedAt = "v2", at.Add(time.Hour)
	if n, err := store(); err != nil || n != 1 {
		t.Fatalf("updated fact=%d error=%v", n, err)
	}
}

func TestPracticeCompanyConflictBlocksAffectedPerson(t *testing.T) {
	paid := int64(10000)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	payment := func(id int64, order, company, seller string) []paymentInput {
		return []paymentInput{{ID: id, Fact: sourceFact{Source: "DCZD", OrderID: order, PaymentID: order,
			SalesCompanyID: company, ReferrerID: seller, AmountMinor: &paid, OccurredAt: at}}}
	}
	orders := map[string][]paymentInput{
		"one": payment(1, "one", "first", "alice"), "two": payment(2, "two", "second", "alice"),
		"three": payment(3, "three", "first", "alice"), "four": payment(4, "four", "second", "bob"),
	}
	identities := map[identityKey][]identityAssignment{
		{"DCZD", "alice"}: {{ID: 1, PersonID: 1, ValidFrom: at}},
		{"DCZD", "bob"}:   {{ID: 2, PersonID: 2, ValidFrom: at}},
	}
	policy := practiceSalesTiers{CompanySchedules: []practiceCompanySchedule{
		{CompanyID: "first", Performance: []practiceTier{{Mode: "rate", Value: 1200}}},
		{CompanyID: "second", Performance: []practiceTier{{Mode: "rate", Value: 1220}}},
	}}
	rule := paymentRule{ID: 1, Components: []component{
		{Type: "sales", ClassMember: "new", Mode: "tiered", SourceRole: "DIRECT_REFERRER", Tiers: &policy},
	}}
	members := []practiceEnrollment{
		{ID: 1, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"one", "two"}},
		{ID: 2, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"three"}},
		{ID: 3, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"four"}},
	}
	draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
	if err != nil || len(draft.Issues) != 2 || len(draft.Lines) != 1 || draft.Total != 1220 ||
		draft.Lines[0].PersonID != 2 || draft.Issues[0].Code != "PRACTICE_TIER_COMPANY_CONFLICT" ||
		draft.Issues[1].Code != "PRACTICE_TIER_GROUP_INCOMPLETE" {
		t.Fatalf("draft=%+v error=%v", draft, err)
	}
}

func TestPracticeTierSalesCategoryDefaults(t *testing.T) {
	const standard = "6601265d-f6cd-4ed5-9d36-da7b3fa66d18"
	const additional = "0d533861-eba4-47d3-aefc-54cd49ce8241"
	var tiers practiceSalesTiers
	if err := json.Unmarshal([]byte(`{
	 "countFromOrdinal":1,
	 "performance":[{"from":0,"mode":"rate","value":700}],
	 "categorySchedules":[{"salesCategoryIds":["`+standard+`","`+additional+`"],
	   "count":[{"from":0,"mode":"rate","value":1200},{"from":1,"mode":"rate","value":1500}]}],
	 "companySchedules":[{"companyIds":["special-a","special-b"],"performance":[{"from":0,"mode":"rate","value":1700}]}],
	 "personSchedules":[{"personId":2,"performance":[{"from":0,"mode":"rate","value":1900}]}]
	}`), &tiers); err != nil {
		t.Fatal(err)
	}
	part := component{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER", Tiers: &tiers}
	if !validPracticeSalesTiers(part) {
		t.Fatal("expected multi-category cumulative tiers to be valid")
	}

	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	paid := int64(10000)
	members := []practiceEnrollment{
		{ID: 1, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"first"}},
		{ID: 2, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"second"}},
		{ID: 3, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"third"}},
		{ID: 4, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"fourth"}},
		{ID: 5, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{"fifth"}},
	}
	orders := make(map[string][]paymentInput)
	var facts []paymentInput
	for i, company := range []string{"new-company", "another-company", "special-a", "special-b", "other-company"} {
		order := members[i].PaidOrderIDs[0]
		seller := "alice"
		if i == 3 {
			seller = "bob"
		}
		fact := paymentInput{ID: int64(i + 1), Fact: sourceFact{Source: "DCZD", OrderID: order, PaymentID: order,
			SalesCompanyID: company, ReferrerID: seller, AmountMinor: &paid, OccurredAt: at.Add(time.Duration(i) * time.Minute)}}
		facts = append(facts, fact)
		orders[order] = []paymentInput{fact}
	}
	identities := map[identityKey][]identityAssignment{
		{"DCZD", "alice"}: {{ID: 1, PersonID: 1, ValidFrom: at}},
		{"DCZD", "bob"}:   {{ID: 2, PersonID: 2, ValidFrom: at}},
	}
	rule := paymentRule{ID: 1, Components: []component{part}}
	runtime := practiceDraftRuntime{companyClassifications: map[string]string{
		"first": practiceInternalCompany, "second": additional, "third": standard, "fifth": practiceInternalCompany,
	}, internalSalesCategories: map[string]string{"first": standard}}
	history := practiceTierHistory{replaying: true, frozenPrior: map[int64]int64{1: 0}}
	draft, err := calculatePracticeDraft(members, rule, orders, identities, nil, history, nil, runtime)
	if err != nil || len(draft.Issues) != 0 || len(draft.Lines) != 5 {
		t.Fatalf("draft=%+v error=%v", draft, err)
	}
	for i, want := range []int64{1200, 1500, 1700, 1900, 700} {
		if got := draft.Lines[i].Amount; got != want {
			t.Fatalf("enrollment %d: got %d, want %d", i+1, got, want)
		}
	}
	frozen := capturePracticeReplayInputs(rule, draft, facts, identities, runtime)
	if frozen.InternalSalesCategories["first"] != standard || frozen.CompanyClassifications["second"] != additional {
		t.Fatalf("replay categories: %+v", frozen)
	}
	tiers.CategorySchedules = append(tiers.CategorySchedules, tiers.CategorySchedules[0])
	if validPracticeSalesTiers(part) {
		t.Fatal("a sales category cannot use two competing tier schedules")
	}
}

func TestPracticeInternalSalesCategorySettlesAndRefundsAtOriginalTier(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_CATEGORY_TIER", Name: "类别阶梯", PeriodMode: "class"}))
	const key, sku = "PRACTICE:1:1:1", "tier"
	class := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 1,
		StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: key}))
	month := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月", SettlementMonth: "2026-09"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: sku}))
	const standard = "41000000-0000-4000-8000-000000000001"
	const later = "41000000-0000-4000-8000-000000000002"
	const company = "41000000-0000-4000-8000-000000000003"
	rule := publishRuleInput{ProductID: product, PeriodID: &class, Label: "销售类别阶梯", Components: []component{
		{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "DIRECT_REFERRER", Tiers: &practiceSalesTiers{
			CountFromOrdinal: 1, CategorySchedules: []practiceCategorySchedule{
				{SalesCategoryIDs: []string{standard}, practiceTierSchedule: practiceTierSchedule{Count: []practiceTier{{Mode: "rate", Value: 1200}}}},
				{SalesCategoryIDs: []string{later}, practiceTierSchedule: practiceTierSchedule{Count: []practiceTier{{Mode: "rate", Value: 1700}}}},
			},
		}},
	}}
	if _, err := s.publishRule(ctx, rule); !errors.Is(err, errOrganizationCategoryTarget) {
		t.Fatalf("unknown categories: %v", err)
	}
	mustExec(`INSERT INTO organization_sales_categories(id,name,active) VALUES ($1,'类别一',true),($2,'类别二',true)`, standard, later)
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	person := id(s.createPerson(ctx, createPersonInput{Name: "新销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-08-01T00:00:00Z"}))
	mustExec(`INSERT INTO organization_units(id,name,kind,status,sales_company_type) VALUES($1,'新公司','COMPANY','ACTIVE','INTERNAL')`, company)
	mustExec(`INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from) VALUES ('DCZD','new',$1,'2026-08-01')`, company)
	mustExec(`INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from,valid_to)
	 VALUES($1,$2,'2026-08-01','2026-08-07'),($1,$3,'2026-08-07',NULL)`, company, standard, later)
	at := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	start := at.Add(48 * time.Hour)
	amount := int64(10000)
	payment := sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: sku, Kind: "ORDER_PAID", OrderID: "1", PaymentID: "1",
		RecordKey: "payment:1", Version: "1", SalesCompanyID: "new", AmountMinor: &amount, OccurredAt: at, UpdatedAt: at, ReferrerID: "seller"}
	store := func(tx pgx.Tx, fact sourceFact) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
		return err
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: key, Hash: "cafe", SourceVersion: 1, StartsAt: start,
			ConfirmedAt: start.Add(time.Hour), Enrollments: []practiceEnrollment{{SourceEnrollmentID: "1", ParticipantID: "2",
				MembershipType: 3, CommissionEligible: true, SignedAt: start, PaidOrderIDs: []string{"1"}, ContentHash: "fade"}}})
		if err != nil {
			return err
		}
		return store(tx, payment)
	}); err != nil {
		t.Fatal(err)
	}
	check := func(period, pay int64, lines, refunds int) draftSummary {
		t.Helper()
		value, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
		if err != nil {
			t.Fatal(err)
		}
		draft := value.(draftSummary)
		if draft.Total != pay || draft.IssueCount != 0 || draft.LineCount != lines || draft.RefundCount != refunds {
			t.Fatalf("draft=%+v, want %d across %d earning lines and %d refunds", draft, pay, lines, refunds)
		}
		return draft
	}
	closed := check(class, 1200, 1, 0)
	mustExec(`INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES ($1,$2,'fixture')`, class, closed.Version)
	mustExec(`UPDATE organization_sales_category_history SET category_id=$2 WHERE unit_id=$1`, company, later)
	refunded := int64(5000)
	refundAt := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		return store(tx, sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: sku, Kind: "REFUND_SUCCEEDED",
			OrderID: "1", RefundID: "1", RecordKey: "refund:1", Version: "1", OccurredAt: refundAt, UpdatedAt: refundAt,
			AmountMinor: &refunded, RefundsPayment: []sourcePaymentReference{{OrderID: "1", RecordID: "1"}}})
	}); err != nil {
		t.Fatal(err)
	}
	check(month, -600, 0, 1)
}
