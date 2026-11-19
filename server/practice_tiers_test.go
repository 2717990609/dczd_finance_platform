package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeTierFallbackUsesOnlyBelowThresholdEnrollmentBase(t *testing.T) {
	config := practiceSalesTiers{
		CountFromOrdinal: 156,
		Count:            []practiceTier{{From: 500, Mode: "rate", Value: 1900}},
		Performance:      []practiceTier{{Mode: "rate", Value: 1200}, {From: 20000, Mode: "rate", Value: 1500}},
	}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	purchases := []practiceTierPurchase{
		{EnrollmentID: 13, SequenceKey: "33", PaidAt: at.Add(time.Hour), PersonID: 9, Base: 30000},
		{EnrollmentID: 11, SequenceKey: "31", PaidAt: at, PersonID: 9, Base: 10000},
		{EnrollmentID: 12, SequenceKey: "32", PaidAt: at, PersonID: 8, Base: 6000},
	}
	awards, err := choosePracticeSalesTiers(config, purchases, map[int64]int64{9: 499})
	if err != nil || awards[11].Value != 1200 || awards[13].Value != 1900 || awards[12].Value != 1200 {
		t.Fatalf("awards=%+v error=%v", awards, err)
	}
}

func TestPracticeTierPerformanceUsesSamePersonPeriodTotal(t *testing.T) {
	config := practiceSalesTiers{Performance: []practiceTier{{Mode: "rate", Value: 1000}, {From: 20000, Mode: "fixed", Value: 1700}}}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	purchases := []practiceTierPurchase{
		{EnrollmentID: 1, PersonID: 9, PaidAt: at, Base: 14000},
		{EnrollmentID: 2, PersonID: 9, PaidAt: at, Base: 6000},
		{EnrollmentID: 3, PersonID: 8, PaidAt: at, Base: 6000},
	}
	awards, err := choosePracticeSalesTiers(config, purchases, nil)
	if err != nil || awards[1].Mode != "fixed" || awards[2].Value != 1700 || awards[3].Value != 1000 {
		t.Fatalf("awards=%+v error=%v", awards, err)
	}
}

func TestPracticeTierChangesWithPastRosterAndTimeBoundAccount(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t)}
	mustID := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	product := mustID(service.createProduct(ctx, createProductInput{Code: "PRACTICE", Name: "Practice", PeriodMode: "class"}))
	mustID(service.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice"}))
	current := mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "Second", Ordinal: 2, StartsOn: "2026-08-08", EndsOn: "2026-08-08", SourcePeriodKey: "PRACTICE:1:1:11"}))
	former := mustID(service.createPerson(ctx, createPersonInput{Name: "Former"}))
	replacement := mustID(service.createPerson(ctx, createPersonInput{Name: "Replacement"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: former, Source: "DCZD", ExternalID: "shared-account", ValidFrom: "2026-06-01T00:00:00+08:00"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: replacement, Source: "DCZD", ExternalID: "shared-account", ValidFrom: "2026-08-05T00:00:00+08:00"}))
	mustID(service.assignIdentity(ctx, assignIdentityInput{PersonID: former, Source: "DCZD", ExternalID: "another-account", ValidFrom: "2026-06-01T00:00:00+08:00"}))
	publish := func(tiers practiceSalesTiers) {
		t.Helper()
		_, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Personal sales", PeriodID: &current,
			Components: []component{{Type: "sales", ClassMember: "new", Mode: "tiered", SourceRole: "DIRECT_REFERRER", Tiers: &tiers}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	tiers := practiceSalesTiers{
		CountFromOrdinal: 1,
		Count:            []practiceTier{{From: 1, Mode: "rate", Value: 1900}},
		Performance:      []practiceTier{{Mode: "rate", Value: 1200}},
	}
	publish(tiers)

	addPayment := func(id, account string, day int, amount int64) {
		t.Helper()
		at := time.Date(2026, 8, day, 1, 0, 0, 0, time.UTC)
		fact := sourceFact{Source: "DCZD", Kind: "ORDER_PAID", OrderID: id, PaymentID: id, RecordKey: id, Version: "1",
			OccurredAt: at, UpdatedAt: at, Domain: "PRACTICE", SourceKey: "practice", AmountMinor: &amount, ReferrerID: account}
		_, err := service.db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
		 VALUES('DCZD',$1,'',$1,'1','ORDER_PAID',$2,$3,$2)`, id, at, fact)
		if err != nil {
			t.Fatal(err)
		}
	}
	addPayment("31", "shared-account", 1, 4000)
	addPayment("32", "shared-account", 1, 6000)
	addPayment("33", "another-account", 7, 10000)
	addPayment("34", "shared-account", 7, 10000)

	member := func(id string, paid ...string) practiceEnrollmentWire {
		row := practiceEnrollmentWire{SourceEnrollmentID: id, ParticipantID: id, MembershipType: 3}
		if len(paid) > 0 {
			row.CommissionEligible, row.PaidOrderIDs, row.SourceOrderID = true, &paid, &paid[len(paid)-1]
		}
		return row
	}
	writeRoster := func(activity, version int64, day int, members ...practiceEnrollmentWire) {
		t.Helper()
		start := time.Date(2026, 8, day, 4, 0, 0, 0, time.UTC)
		for i := range members {
			members[i].SignedAt = start.Add(time.Hour).Format(time.RFC3339)
		}
		wire := practiceRosterWire{ContractVersion: "dczd.practice-roster/1.0", SourceSystem: "DCZD", ActivityType: 1, StrategyID: 1,
			ActivityID: activity, PeriodKey: fmt.Sprintf("PRACTICE:1:1:%d", activity), RosterVersion: version, Enrollments: members,
			PeriodStartAt: start.Format(time.RFC3339), ConfirmedAt: start.Add(2 * time.Hour).Format(time.RFC3339), ConfirmedBy: "1"}
		wire.RosterHash = practiceRosterHash(members)
		roster, err := normalizePracticeRoster(wire)
		if err != nil {
			t.Fatal(err)
		}
		if err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			_, err := storePracticeRoster(ctx, tx, roster)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(version, total int64, issues int) {
		t.Helper()
		result, err := service.recalculateDraft(ctx, calculateDraftInput{PeriodID: current})
		if err != nil {
			t.Fatal(err)
		}
		draft := result.(draftSummary)
		if draft.Version != version || draft.Total != total || draft.IssueCount != issues {
			t.Fatalf("draft=%+v", draft)
		}
	}
	writeRoster(11, 1, 8, member("12", "33"), member("13", "34"))
	check(1, 0, 2)
	tiers.PersonCountStarts = []practicePersonCountStart{{PersonID: former, practiceCountStart: practiceCountStart{FromOrdinal: 2}}}
	publish(tiers)
	check(2, 1200, 1) // Only the person with a current-period reset is ready.
	writeRoster(10, 1, 1, member("11", "31", "32"))
	mustID(service.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "First", Ordinal: 1, StartsOn: "2026-08-01", EndsOn: "2026-08-01", SourcePeriodKey: "PRACTICE:1:1:10"}))
	if err := service.refreshPendingDrafts(ctx); err != nil {
		t.Fatal(err)
	}
	var version, total int64
	if err := service.db.QueryRow(ctx, `SELECT version,total_minor FROM calculation_drafts WHERE period_id=$1`, current).Scan(&version, &total); err != nil || version != 3 || total != 2400 {
		t.Fatalf("backfilled period did not refresh the cumulative tier: version=%d total=%d error=%v", version, total, err)
	}
	check(3, 2400, 0)
	tiers.PersonCountStarts[0].FromOrdinal = 3
	publish(tiers)
	check(4, 3100, 0) // A future reset leaves an already opened period on the default.
	check(4, 3100, 0)
	exactFirstClass := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC)
	tiers.PersonCountStarts[0].practiceCountStart = practiceCountStart{FromAt: &exactFirstClass}
	publish(tiers)
	check(5, 3100, 0) // The cutoff includes a class opening exactly at the configured time.
	afterFirstClass := exactFirstClass.Add(time.Second)
	tiers.PersonCountStarts[0].FromAt = &afterFirstClass
	publish(tiers)
	check(6, 2400, 0)
	duplicate := member("14", "31", "32")
	duplicate.MembershipType = 2
	writeRoster(10, 2, 1, member("11", "31", "32"), duplicate)
	check(7, 1200, 1) // The duplicate is outside only the reset person's window.

	tiers.PersonCountStarts = nil
	tiers.CountFromOrdinal, tiers.CountFromAt = 0, &afterFirstClass
	publish(tiers)
	check(8, 2400, 0) // Existing default time-based rules still select the same history.

	const formerLegacy = "f8bdd870-7f83-413b-ba52-4a97ab33d301"
	const replacementLegacy = "f8bdd870-7f83-413b-ba52-4a97ab33d302"
	if _, err := service.db.Exec(ctx, `UPDATE people SET legacy_party_id=CASE WHEN id=$1 THEN $3::uuid ELSE $4::uuid END WHERE id IN ($1,$2)`,
		former, replacement, formerLegacy, replacementLegacy); err != nil {
		t.Fatal(err)
	}
	one, zero := int64(1), int64(0)
	historical := practiceCountOpeningFile{ProductID: product, ThroughAt: exactFirstClass,
		SourceSettlementID: "f8bdd870-7f83-413b-ba52-4a97ab33d303",
		People:             []practiceCountOpeningPerson{{LegacyPartyID: formerLegacy, NewCount: &one}, {LegacyPartyID: replacementLegacy, NewCount: &zero}},
	}
	addOpening := func(opening practiceCountOpeningFile) int64 {
		t.Helper()
		var id int64
		err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			var err error
			id, _, err = storePracticeCountOpening(ctx, tx, opening)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	tiers.OpeningID, tiers.CountFromAt = addOpening(historical), nil
	publish(tiers)
	check(9, 3100, 0) // Opening carries the first signup. History right at the cutoff is already included.
	tiers.PersonCountStarts = []practicePersonCountStart{{PersonID: former, practiceCountStart: practiceCountStart{FromOrdinal: 2}}}
	publish(tiers)
	check(10, 2400, 0) // The reset excludes the imported count for this person.
	historical.SourceSettlementID = "f8bdd870-7f83-413b-ba52-4a97ab33d304"
	historical.People = historical.People[:1]
	tiers.OpeningID, tiers.PersonCountStarts = addOpening(historical), nil
	publish(tiers)
	check(11, 1900, 1) // Missing opening is different from the explicit zero tested above.

	writeRoster(10, 3, 1, member("11", "31", "32"))
	historical.SourceSettlementID = "f8bdd870-7f83-413b-ba52-4a97ab33d305"
	historical.ThroughAt = exactFirstClass.Add(-time.Second)
	historical.People = append(historical.People, practiceCountOpeningPerson{LegacyPartyID: replacementLegacy, NewCount: &zero})
	tiers.OpeningID = addOpening(historical)
	tiers.Count = append(tiers.Count, practiceTier{From: 2, Mode: "rate", Value: 2500})
	publish(tiers)
	check(12, 3700, 0) // A later class is included on top of the frozen opening.

	tiers.OpeningID, tiers.CountFromAt = 0, &afterFirstClass
	tiers.PersonCountStarts = []practicePersonCountStart{{PersonID: 999999, practiceCountStart: practiceCountStart{FromOrdinal: 2}}}
	_, err := service.publishRule(ctx, publishRuleInput{ProductID: product, Label: "Missing person", PeriodID: &current,
		Components: []component{{Type: "sales", ClassMember: "new", Mode: "tiered", SourceRole: "DIRECT_REFERRER", Tiers: &tiers}}})
	if !errors.Is(err, errPracticeCountPerson) {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestPracticeTierRangesCanHaveGapsAndRejectOverlap(t *testing.T) {
	through := int64(1)
	config := practiceSalesTiers{CountFromOrdinal: 1, Count: []practiceTier{
		{From: 1, Through: &through, Mode: "fixed", Value: 100}, {From: 3, Mode: "rate", Value: 1900},
	}}
	part := component{Type: "sales", ClassMember: "new", Mode: "tiered", SourceRole: "DIRECT_REFERRER", Tiers: &config}
	if !validPracticeSalesTiers(part) {
		t.Fatal("a deliberately empty range must be configurable")
	}
	if _, ok := matchingPracticeTier(config.Count, 2); ok {
		t.Fatal("the empty range selected a tier")
	}
	if tier, ok := matchingPracticeTier(config.Count, 3); !ok || tier.Value != 1900 {
		t.Fatalf("tier=%+v matched=%t", tier, ok)
	}
	through = 3
	if validPracticeSalesTiers(part) {
		t.Fatal("an overlapping range must be rejected")
	}
}
