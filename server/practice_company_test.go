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
