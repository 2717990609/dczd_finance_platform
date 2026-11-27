package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const practiceLateRefundRosterSuffix = "/late-refunds/"

// Rebuild the financial roster from the synchronized sign-ins and supported late
// refunds. The period lock serializes this with registration sync and settlement.
func refreshPracticeLateRefundRoster(ctx context.Context, tx pgx.Tx, periodID int64) (bool, error) {
	var roster practiceRoster
	var rosterID int64
	var cutoff time.Time
	var registrations []practiceRegistrationSnapshotRow
	err := tx.QueryRow(ctx, `SELECT r.id,r.period_key,r.source_version,r.source_hash,r.starts_at,p.refund_cutoff_at,s.registrations
	 FROM periods p JOIN practice_registrations s ON s.period_key=p.source_period_key AND s.valid_payment_links
	 JOIN LATERAL (SELECT * FROM practice_rosters WHERE period_key=s.period_key ORDER BY source_version DESC LIMIT 1) r
	  ON r.from_registration_sync
	 WHERE p.id=$1 AND p.calendar_month IS NULL AND p.refund_cutoff_at IS NOT NULL`, periodID).
		Scan(&rosterID, &roster.Key, &roster.SourceVersion, &roster.Hash, &roster.StartsAt, &cutoff, &registrations)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	active := make(map[string]bool, len(registrations))
	var candidates []practiceRegistrationSnapshotRow
	for _, row := range registrations {
		if row.Enabled == nil || !*row.Enabled || row.SignedAt == nil {
			continue
		}
		if row.State == 2 {
			active[row.ID] = true
		}
		if row.State == 3 && (row.MembershipType == 2 || row.MembershipType == 3) && row.OrderID != nil {
			candidates = append(candidates, row)
		}
	}
	restored, err := practiceLateRefundMembers(ctx, tx, periodID, cutoff, candidates)
	if err != nil {
		return false, err
	}
	sourceHash, _, _ := strings.Cut(roster.Hash, practiceLateRefundRosterSuffix)
	hash := sourceHash
	if len(restored) != 0 {
		data, err := json.Marshal(restored)
		if err != nil {
			return false, err
		}
		hash += fmt.Sprintf("%s%x", practiceLateRefundRosterSuffix, sha256.Sum256(data))
	}
	if hash == roster.Hash {
		return false, nil
	}

	enrollments, err := readPracticeMembers(ctx, tx, []int64{rosterID}, false)
	if err != nil {
		return false, err
	}
	enrollments = slices.DeleteFunc(enrollments, func(row practiceEnrollment) bool { return !active[row.SourceEnrollmentID] })
	enrollments = append(enrollments, restored...)
	if len(enrollments) > 10000 {
		return false, errPracticeRoster
	}
	for i := range enrollments {
		if err := hashPracticeEnrollment(&enrollments[i]); err != nil {
			return false, err
		}
	}
	roster.Hash, roster.SourceVersion = hash, roster.SourceVersion+1
	roster.FromRegistrationSync = true
	roster.ConfirmedAt = time.Now().UTC().Truncate(time.Microsecond)
	roster.Enrollments = enrollments
	return storePracticeRoster(ctx, tx, roster)
}

func practiceLateRefundMembers(ctx context.Context, tx pgx.Tx, periodID int64, cutoff time.Time,
	candidates []practiceRegistrationSnapshotRow) ([]practiceEnrollment, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(candidates))
	for _, row := range candidates {
		keys = append(keys, *row.OrderID)
	}

	// These two stored fields come from the source gross amount and its explicit
	// SETTLES_DEPOSIT relation. They prove the payment links for historical sign-ins
	// that were already refunded when the roster first synchronized.
	sourcePayments := `WITH paid(id,fact,company,recommendation,conflict) AS (` + practicePaymentsSQL + `)
	 SELECT paid.id,paid.fact,d.details->>'settlesDepositOrderId',(d.details->>'originalAmountMinor')::bigint
	 FROM paid LEFT JOIN source_order_display d ON d.source_version_id=paid.id`
	type paymentLink struct {
		paymentInput
		deposit *string
		gross   *int64
	}
	readPayments := func(keys []string) ([]paymentLink, error) {
		rows, err := tx.Query(ctx, sourcePayments, periodID, keys, maxDraftPayments+1)
		if err != nil {
			return nil, err
		}
		payments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (paymentLink, error) {
			var link paymentLink
			err := row.Scan(&link.ID, &link.Fact, &link.deposit, &link.gross)
			return link, err
		})
		if len(payments) > maxDraftPayments {
			return nil, errDraftLimit
		}
		return payments, err
	}
	primary, err := readPayments(keys)
	if err != nil {
		return nil, err
	}
	links := make(map[int64]paymentLink, len(primary))
	orders := make(map[string][]paymentInput, len(keys))
	var deposits []string
	for _, link := range primary {
		links[link.ID] = link
		orders[link.Fact.OrderID] = append(orders[link.Fact.OrderID], link.paymentInput)
		if link.deposit != nil && positiveSourceID(*link.deposit) != 0 {
			deposits = append(deposits, *link.deposit)
		}
	}
	if len(deposits) != 0 {
		parts, err := readPayments(deposits)
		if err != nil {
			return nil, err
		}
		for _, part := range parts {
			order := part.Fact.OrderID
			if _, isPrimary := links[part.ID]; !isPrimary {
				orders[order] = append(orders[order], part.paymentInput)
			}
		}
	}

	refunds, err := readPracticeRefunds(ctx, tx, periodID, orders)
	if err != nil {
		return nil, err
	}
	matches := matchPracticeRefunds(refunds, orders)
	late := func(payments []paymentInput, signedAt time.Time) bool {
		return slices.ContainsFunc(payments, func(p paymentInput) bool {
			return slices.ContainsFunc(matches[p.ID], func(match practiceRefundMatch) bool {
				at := match.event.Fact.OccurredAt
				return !at.Before(cutoff) && !at.Before(signedAt)
			})
		})
	}

	var restored []practiceEnrollment
	for _, row := range candidates {
		order := *row.OrderID
		payments := orders[order]
		if len(payments) != 1 {
			if late(payments, *row.SignedAt) {
				return nil, errPracticeRegistrationPayment
			}
			continue
		}
		link := links[payments[0].ID]
		ids := []string{order}
		if link.deposit != nil {
			ids = []string{*link.deposit, order}
		}
		if !late(payments, *row.SignedAt) && (link.deposit == nil || !late(orders[*link.deposit], *row.SignedAt)) {
			continue
		}

		// A type 2 order without a deposit link must prove it is a full payment.
		f := payments[0].Fact
		switch {
		case row.MembershipType == 2 && (f.TransactionType != "3" || link.deposit != nil),
			row.MembershipType == 3 && f.TransactionType != "1" && f.TransactionType != "2",
			f.AmountMinor == nil || *f.AmountMinor <= 0:
			return nil, errPracticeRegistrationPayment
		case link.deposit != nil:
			parts := orders[*link.deposit]
			if *link.deposit == order || f.TransactionType != "2" || len(parts) != 1 || parts[0].Fact.TransactionType != "1" {
				return nil, errPracticeRegistrationPayment
			}
		case f.TransactionType == "2":
			if link.gross == nil || *link.gross != *f.AmountMinor {
				return nil, errPracticeRegistrationPayment
			}
		}
		restored = append(restored, practiceEnrollment{SourceEnrollmentID: row.ID, ParticipantID: row.ParticipantID,
			MembershipType: row.MembershipType, CommissionEligible: true, SignedAt: row.SignedAt.UTC(), PaidOrderIDs: ids})
	}
	return restored, nil
}
