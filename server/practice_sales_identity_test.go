package main

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeSellerUsesCurrentIdentityOnlyWhenPaymentIdentityIsMissing(t *testing.T) {
	db := integrationDB(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `
 INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES (101,'Former'),(102,'Current'),(103,'Other');
 INSERT INTO identity_mappings(source,external_id,person_id,valid_from,valid_to) VALUES
  ('DCZD','seller',101,'2025-02-01Z','2025-03-01Z'),
  ('DCZD','seller',102,'2025-03-01Z',NULL),
  ('DCZD','other',103,'2025-03-01Z',NULL),
  ('DCZD','future',103,now()+interval '1 year',NULL),
  ('DCZD','expired',103,'2025-02-01Z','2025-03-01Z')`)
	if err != nil {
		t.Fatal(err)
	}

	january := time.Date(2025, time.January, 17, 0, 0, 0, 0, time.UTC)
	february := january.AddDate(0, 1, 0)
	march := january.AddDate(0, 2, 0)
	window := periodWindow{Start: january, Through: february.Add(time.Hour)}
	keys := make(map[identityKey]bool)
	for _, id := range []string{"seller", "other", "future", "expired", "unknown"} {
		keys[identityKey{"DCZD", id}] = true
	}
	mappings, err := readPracticeIdentityAssignments(ctx, tx, window, keys)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, domain, role, account, issue string
		at                                 time.Time
		person                             int64
	}{
		{"deposit seller", "PRACTICE", "SELLER", "seller", "", january, 102},
		{"deposit direct referrer", "PRACTICE", "DIRECT_REFERRER", "seller", "", january, 102},
		{"historical seller", "PRACTICE", "SELLER", "seller", "", february, 101},
		{"other product", "CAMP", "DIRECT_REFERRER", "seller", "MISSING_IDENTITY", january, 0},
		{"other role", "PRACTICE", "UPSTREAM_COMPANY", "seller", "MISSING_IDENTITY", january, 0},
		{"different account", "PRACTICE", "SELLER", "unknown", "MISSING_IDENTITY", january, 0},
		{"future mapping", "PRACTICE", "SELLER", "future", "MISSING_IDENTITY", january, 0},
		{"no current mapping", "PRACTICE", "SELLER", "expired", "MISSING_IDENTITY", march, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fact := sourceFact{Source: "DCZD", Domain: tc.domain, OccurredAt: tc.at, ReferrerID: tc.account,
				Participants: []sourceParticipant{{Role: tc.role, UserID: tc.account}}}
			person, issue := paymentBeneficiary(fact, tc.role, mappings)
			if issue != tc.issue || person.PersonID != tc.person {
				t.Fatalf("resolved person=%d, issue=%q, want %d and %q", person.PersonID, issue, tc.person, tc.issue)
			}
			if tc.person != 0 && person.ID == 0 {
				t.Fatal("settlement must retain the identity record used")
			}
		})
	}

	key := identityKey{"DCZD", "seller"}
	mappings[key] = append(mappings[key], identityAssignment{ID: 999, PersonID: 103, ValidFrom: february.Add(-time.Hour), ValidTo: &march})
	fact := sourceFact{Source: "DCZD", Domain: "PRACTICE", OccurredAt: february, Participants: []sourceParticipant{{Role: "SELLER", UserID: "seller"}}}
	if _, issue := paymentBeneficiary(fact, "SELLER", mappings); issue != "AMBIGUOUS_IDENTITY" {
		t.Fatalf("overlapping payment-date identities must stay blocked, got %q", issue)
	}
}
