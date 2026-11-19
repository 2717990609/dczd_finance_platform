package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errPracticeSettlementUnavailable = errors.New("实战班正式结算待核对来源和项目规则后开放")

func importPracticeFile(ctx context.Context, cfg config, filename string) error {
	if !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return errors.New("roster import requires the isolated development database")
	}
	file, err := os.Open(filename)
	if err != nil {
		return errors.New("could not open the roster file")
	}
	defer file.Close()
	const limit = 8 << 20
	info, err := file.Stat()
	if err != nil || info.Size() > limit {
		return errPracticeRoster
	}
	decoder := json.NewDecoder(io.LimitReader(file, limit+1))
	decoder.DisallowUnknownFields()
	var wire practiceRosterWire
	if err = decoder.Decode(&wire); err != nil {
		return errPracticeRoster
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errPracticeRoster
	}
	roster, err := normalizePracticeRoster(wire)
	if err != nil {
		return err
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("could not initialize the isolated database")
	}
	defer db.Close()
	if err = migrate(ctx, db); err != nil {
		return err
	}
	var inserted bool
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var writeErr error
		inserted, writeErr = storePracticeRoster(ctx, tx, roster)
		return writeErr
	})
	if err != nil {
		return err
	}
	fmt.Printf("Roster v%d, %d attendances, imported: %t\n", roster.SourceVersion, len(roster.Enrollments), inserted)
	return nil
}

func readPracticeHeader(ctx context.Context, tx pgx.Tx, periodKey string, summary *draftSummary) (time.Time, error) {
	if periodKey == "" {
		return time.Time{}, errPracticeNotReady
	}
	var startsAt time.Time
	err := tx.QueryRow(ctx, `SELECT id,source_version,starts_at FROM practice_rosters
	 WHERE period_key=$1 ORDER BY source_version DESC LIMIT 1`, periodKey).Scan(&summary.PracticeRosterID, &summary.PracticeRosterVersion, &startsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = errPracticeNotReady
	}
	return startsAt, err
}

const practicePaymentsSQL = `SELECT v.id,v.fact
 FROM source_order_versions v JOIN source_mappings m ON m.source=v.source AND m.domain=v.fact->>'domain' AND m.source_key=v.fact->>'sourceKey'
 WHERE m.product_id=(SELECT product_id FROM periods WHERE id=$1)
   AND v.source='DCZD' AND v.kind='ORDER_PAID' AND v.order_id=ANY($2::text[])
   AND NOT EXISTS(
    SELECT 1 FROM source_order_versions newer
    WHERE (newer.source,newer.order_id,newer.line_id,newer.record_key,newer.kind)=(v.source,v.order_id,v.line_id,v.record_key,v.kind)
      AND (newer.updated_at,newer.id)>(v.updated_at,v.id)
   )
 ORDER BY v.order_id,v.id LIMIT $3`

const practiceBlocksSQL = `SELECT e.id, CASE
 WHEN EXISTS(
  SELECT 1 FROM practice_enrollments other JOIN practice_roster_members link ON link.enrollment_id=other.id
  JOIN practice_rosters r ON r.id=link.roster_id
  WHERE other.source_enrollment_id=e.source_enrollment_id AND other.participant_id=e.participant_id
    AND split_part(other.period_key,':',3)=split_part(e.period_key,':',3) AND other.period_key<>e.period_key
    AND other.commission_eligible AND other.paid_order_ids&&e.paid_order_ids
    AND NOT EXISTS(SELECT 1 FROM practice_rosters newer WHERE newer.period_key=r.period_key AND newer.source_version>r.source_version)
 ) THEN 'PRACTICE_DUPLICATE_ENROLLMENT'
 WHEN EXISTS(
   SELECT 1 FROM unnest(e.paid_order_ids) AS payment(order_id)
   WHERE EXISTS(
     SELECT 1 FROM source_order_versions v WHERE v.source='DCZD' AND v.kind='REFUND_SUCCEEDED' AND v.order_id=payment.order_id
   ) OR EXISTS(
     SELECT 1 FROM source_order_versions v WHERE v.source='DCZD' AND v.kind='REFUND_SUCCEEDED'
       AND (v.fact->'refundsPayment') @> jsonb_build_array(jsonb_build_object('orderId',payment.order_id))
   )
 ) THEN 'PRACTICE_REFUND_PENDING'
 END AS issue
 FROM practice_roster_members member JOIN practice_enrollments e ON e.id=member.enrollment_id
 WHERE member.roster_id=ANY($1::bigint[]) AND e.commission_eligible`

func readPracticeMembers(ctx context.Context, tx pgx.Tx, rosterIDs []int64, eligibleOnly bool) ([]practiceEnrollment, error) {
	rows, err := tx.Query(ctx, `SELECT e.id,e.source_enrollment_id,e.participant_id,e.membership_type,e.commission_eligible,e.signed_at,e.paid_order_ids
	 FROM practice_roster_members m JOIN practice_enrollments e ON e.id=m.enrollment_id
	 WHERE m.roster_id=ANY($1::bigint[]) AND (NOT $2 OR e.commission_eligible)
	 ORDER BY m.roster_id,e.source_enrollment_id::bigint LIMIT $3`, rosterIDs, eligibleOnly, maxDraftPayments+1)
	if err != nil {
		return nil, err
	}
	members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceEnrollment, error) {
		var e practiceEnrollment
		err := row.Scan(&e.ID, &e.SourceEnrollmentID, &e.ParticipantID, &e.MembershipType, &e.CommissionEligible, &e.SignedAt, &e.PaidOrderIDs)
		return e, err
	})
	if len(members) > maxDraftPayments {
		return nil, errDraftLimit
	}
	return members, err
}

func readPracticeTierHistory(ctx context.Context, tx pgx.Tx, periodID int64, config practiceSalesTiers, startsAt time.Time) ([]int64, bool, error) {
	var ordinal int
	var rosterIDs []int64
	err := tx.QueryRow(ctx, `SELECT current.ordinal, ARRAY(
	 SELECT CASE WHEN roster.starts_at<$3 AND split_part(p.source_period_key,':',3)=split_part(current.source_period_key,':',3)
	  THEN roster.id ELSE 0 END FROM periods p
	 LEFT JOIN LATERAL (
	  SELECT r.id,r.starts_at FROM practice_rosters r
	  WHERE r.period_key=p.source_period_key ORDER BY r.source_version DESC LIMIT 1
	 ) roster ON true
	 WHERE p.product_id=current.product_id AND p.ordinal<current.ordinal AND CASE WHEN $4::timestamptz IS NULL THEN p.ordinal>=$2
	  WHEN roster.starts_at IS NOT NULL THEN roster.starts_at>=$4
	  ELSE p.ends_on>=($4 AT TIME ZONE 'Asia/Shanghai')::date END
	 ORDER BY p.ordinal
	 )
	 FROM periods current WHERE current.id=$1`, periodID, config.CountFromOrdinal, startsAt, config.CountFromAt).Scan(&ordinal, &rosterIDs)
	if err != nil {
		return nil, false, err
	}
	if config.CountFromAt != nil {
		if config.CountFromAt.After(startsAt) {
			return nil, false, nil
		}
	} else if config.CountFromOrdinal > ordinal || len(rosterIDs) != ordinal-config.CountFromOrdinal {
		return nil, false, nil
	}
	for _, rosterID := range rosterIDs {
		if rosterID == 0 {
			return nil, false, nil
		}
	}
	return rosterIDs, true, nil
}

func computePracticeDraft(ctx context.Context, tx pgx.Tx, summary draftSummary, rule paymentRule) (paymentDraft, []byte, error) {
	rosterIDs := []int64{*summary.PracticeRosterID}
	members, err := readPracticeMembers(ctx, tx, rosterIDs, false)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	var history practiceTierHistory
	part := practiceTierComponent(rule)
	if part != nil && len(part.Tiers.Count) != 0 {
		past, complete, err := readPracticeTierHistory(ctx, tx, summary.PeriodID, *part.Tiers, summary.PracticeStartsAt)
		if err != nil {
			return paymentDraft{}, nil, err
		}
		history.complete = complete
		if len(past) != 0 {
			history.members, err = readPracticeMembers(ctx, tx, past, true)
			if err != nil {
				return paymentDraft{}, nil, err
			}
			rosterIDs = append(rosterIDs, past...)
		}
	}

	orderIDs := make([]string, 0, len(members)+len(history.members))
	for _, entries := range [][]practiceEnrollment{members, history.members} {
		for _, member := range entries {
			orderIDs = append(orderIDs, member.PaidOrderIDs...)
		}
	}
	rows, err := tx.Query(ctx, practicePaymentsSQL, summary.PeriodID, orderIDs, maxDraftPayments+1)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	facts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (paymentInput, error) {
		var item paymentInput
		err := row.Scan(&item.ID, &item.Fact)
		return item, err
	})
	if err != nil {
		return paymentDraft{}, nil, err
	}
	if len(facts) > maxDraftPayments {
		return paymentDraft{}, nil, errDraftLimit
	}

	orders := make(map[string][]paymentInput, len(facts))
	keys := make(map[identityKey]bool)
	var window periodWindow
	for _, payment := range facts {
		f := payment.Fact
		orders[f.OrderID] = append(orders[f.OrderID], payment)
		if window.Start.IsZero() || f.OccurredAt.Before(window.Start) {
			window.Start = f.OccurredAt
		}
		if !f.OccurredAt.Before(window.Through) {
			window.Through = f.OccurredAt.Add(time.Microsecond)
		}
		for _, party := range f.Participants {
			keys[identityKey{f.Source, party.UserID}] = true
		}
		if f.ReferrerID != "" {
			keys[identityKey{f.Source, f.ReferrerID}] = true
		}
	}
	var identities map[identityKey][]identityAssignment
	if len(keys) != 0 {
		identities, err = readIdentityAssignments(ctx, tx, window, keys)
		if err != nil {
			return paymentDraft{}, nil, err
		}
	}

	rows, err = tx.Query(ctx, `SELECT result.id,result.issue FROM (`+practiceBlocksSQL+`) result WHERE result.issue IS NOT NULL`, rosterIDs)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	blocked := make(map[int64]string)
	for rows.Next() {
		var id int64
		var issue string
		if err = rows.Scan(&id, &issue); err != nil {
			rows.Close()
			return paymentDraft{}, nil, err
		}
		blocked[id] = issue
	}
	if err = rows.Err(); err != nil {
		return paymentDraft{}, nil, err
	}
	result, err := calculatePracticeDraft(members, rule, orders, identities, blocked, history)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	result.PracticeStartsAt = summary.PracticeStartsAt
	result.PracticeEnrollments = make([]int64, 0, len(members))
	for _, member := range members {
		result.PracticeEnrollments = append(result.PracticeEnrollments, member.ID)
	}
	digest, err := digestDraft(rule.ID, result, facts)
	return result, digest, err
}
