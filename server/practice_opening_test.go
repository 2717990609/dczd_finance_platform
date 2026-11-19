package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeOpeningIsIdempotentAndHasProductScope(t *testing.T) {
	ctx := context.Background()
	service := workspace{db: integrationDB(t)}
	product := func(code string) int64 {
		t.Helper()
		result, err := service.createProduct(ctx, createProductInput{Code: code, Name: code, PeriodMode: "class"})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	first, second := product("OPENING_ONE"), product("OPENING_TWO")
	one, zero, two := int64(1), int64(0), int64(2)
	file := practiceCountOpeningFile{ProductID: first, SourceSettlementID: "bd9c3187-aef9-424b-9601-af4b7884b701",
		ThroughAt: time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC),
		People: []practiceCountOpeningPerson{
			{LegacyPartyID: "bd9c3187-aef9-424b-9601-af4b7884b702", DisplayName: "Mapped Here", NewCount: &one},
			{LegacyPartyID: "bd9c3187-aef9-424b-9601-af4b7884b703", DisplayName: "Explicit Zero", NewCount: &zero},
		}}
	store := func(input practiceCountOpeningFile) (int64, bool, error) {
		var id int64
		var inserted bool
		err := pgx.BeginFunc(ctx, service.db, func(tx pgx.Tx) error {
			var err error
			id, inserted, err = storePracticeCountOpening(ctx, tx, input)
			return err
		})
		return id, inserted, err
	}
	id, inserted, err := store(file)
	if err != nil || !inserted {
		t.Fatalf("import=%t error=%v", inserted, err)
	}
	file.People[0], file.People[1] = file.People[1], file.People[0]
	file.People[1].DisplayName = "Edited Display Name"
	replay, inserted, err := store(file)
	if err != nil || inserted || replay != id {
		t.Fatalf("replay=%d inserted=%t error=%v", replay, inserted, err)
	}
	file.People[1].NewCount = &two
	if _, _, err := store(file); !errors.Is(err, errPracticeOpeningConflict) {
		t.Fatalf("changed opening returned %v", err)
	}
	_, err = service.publishRule(ctx, publishRuleInput{ProductID: second, Label: "Wrong product", Components: []component{{
		Type: "sales", Mode: "tiered", SourceRole: "DIRECT_REFERRER", ClassMember: "new",
		Tiers: &practiceSalesTiers{OpeningID: id, Count: []practiceTier{{Mode: "rate", Value: 1000}}},
	}}})
	if !errors.Is(err, errPracticeOpeningScope) {
		t.Fatalf("cross-product rule returned %v", err)
	}
	file.SourceSettlementID = "bd9c3187-aef9-424b-9601-af4b7884b704"
	file.People[1].LegacyPartyID, file.People[1].DisplayName = "bd9c3187-aef9-424b-9601-af4b7884b705", ""
	if _, _, err = store(file); !errors.Is(err, errPracticeCountPerson) {
		t.Fatalf("opening without person returned %v", err)
	}
	var openings, people, zeroPeople int
	err = service.db.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM practice_count_openings),
	 (SELECT count(*) FROM people WHERE legacy_party_id IS NOT NULL),
	 (SELECT count(*) FROM practice_count_opening_people WHERE new_count=0)`).Scan(&openings, &people, &zeroPeople)
	if err != nil || openings != 1 || people != 2 || zeroPeople != 1 {
		t.Fatalf("openings=%d people=%d zero=%d error=%v", openings, people, zeroPeople, err)
	}
}
