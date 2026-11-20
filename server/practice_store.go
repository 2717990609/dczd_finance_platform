package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errPracticeSettlementUnavailable = errors.New("尚未配置大春之道的签到名单读取连接")

func readImportFile(filename string, target any, limit int64) error {
	file, err := os.Open(filename)
	if err != nil {
		return errors.New("could not open the import file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > limit {
		return errors.New("could not read the import file within its size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(file, limit+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("the import file does not match the expected fields")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return errors.New("the import file must contain one JSON document")
	}
	return nil
}

func importPracticeFile(ctx context.Context, cfg config, filename string) error {
	if !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return errors.New("roster import requires the isolated development database")
	}
	var wire practiceRosterWire
	if err := readImportFile(filename, &wire, 8<<20); err != nil {
		return err
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

const practicePaymentsSQL = `SELECT v.id,v.fact,coalesce(company.company_id,''),
 coalesce(company.recommendation_company_id,''),coalesce(company.recommendation_conflict,false)
 FROM source_order_versions v JOIN source_mappings m ON m.source=v.source AND m.domain=v.fact->>'domain' AND m.source_key=v.fact->>'sourceKey'
 LEFT JOIN source_payment_companies company ON company.source_version_id=v.id
 WHERE m.product_id=(SELECT product_id FROM periods WHERE id=$1)
   AND v.source='DCZD' AND v.kind='ORDER_PAID' AND v.order_id=ANY($2::text[])
   AND NOT EXISTS(
    SELECT 1 FROM source_order_versions newer
    WHERE (newer.source,newer.order_id,newer.line_id,newer.record_key,newer.kind)=(v.source,v.order_id,v.line_id,v.record_key,v.kind)
      AND (newer.updated_at,newer.id)>(v.updated_at,v.id)
   )
 ORDER BY v.order_id,v.id LIMIT $3`

const practicePaymentAlreadySettled = "PRACTICE_PAYMENT_ALREADY_SETTLED"

const practiceDuplicatesSQL = `WITH candidates AS MATERIALIZED (
 SELECT e.id,e.period_key,e.paid_order_ids
 FROM practice_roster_members member JOIN practice_enrollments e ON e.id=member.enrollment_id
 WHERE member.roster_id=ANY($1::bigint[]) AND e.commission_eligible AND NOT EXISTS(
  SELECT 1 FROM periods p JOIN settlements s ON s.period_id=p.id WHERE p.source_period_key=e.period_key
 )
), booked AS (
 SELECT DISTINCT e.id FROM candidates e
 JOIN source_order_versions v ON v.order_id=ANY(e.paid_order_ids)
  AND v.source='DCZD' AND v.kind='ORDER_PAID' AND v.fact->>'domain'='PRACTICE'
 JOIN calculation_draft_performance a ON a.source_version_id=v.id AND a.payment_version_id IS NULL
 JOIN settlements s ON s.period_id=a.period_id
)
SELECT e.id,CASE WHEN booked.id IS NOT NULL THEN '` + practicePaymentAlreadySettled + `' ELSE 'PRACTICE_DUPLICATE_ENROLLMENT' END
 FROM candidates e LEFT JOIN booked USING(id)
 WHERE booked.id IS NOT NULL OR EXISTS(
  SELECT 1 FROM practice_enrollments other JOIN practice_roster_members link ON link.enrollment_id=other.id
  JOIN practice_rosters r ON r.id=link.roster_id
  WHERE split_part(other.period_key,':',3)=split_part(e.period_key,':',3) AND other.period_key<>e.period_key
    AND other.commission_eligible AND other.paid_order_ids&&e.paid_order_ids
    AND NOT EXISTS(SELECT 1 FROM practice_rosters newer WHERE newer.period_key=r.period_key AND newer.source_version>r.source_version)
    AND NOT EXISTS(SELECT 1 FROM periods p JOIN settlements s ON s.period_id=p.id WHERE p.source_period_key=r.period_key)
 )`

func readPracticeMembers(ctx context.Context, tx pgx.Tx, rosterIDs []int64, eligibleOnly bool) ([]practiceEnrollment, error) {
	rows, err := tx.Query(ctx, `SELECT e.id,e.source_enrollment_id,e.participant_id,e.membership_type,e.commission_eligible,e.signed_at,e.paid_order_ids,m.roster_id,first_roster.confirmed_at
	 FROM practice_roster_members m JOIN practice_enrollments e ON e.id=m.enrollment_id
	 JOIN LATERAL (SELECT r.confirmed_at FROM practice_rosters r WHERE r.period_key=e.period_key ORDER BY r.source_version LIMIT 1) first_roster ON true
	 WHERE m.roster_id=ANY($1::bigint[]) AND (NOT $2 OR e.commission_eligible)
	 ORDER BY m.roster_id,e.source_enrollment_id::bigint LIMIT $3`, rosterIDs, eligibleOnly, maxDraftPayments+1)
	if err != nil {
		return nil, err
	}
	members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceEnrollment, error) {
		var e practiceEnrollment
		err := row.Scan(&e.ID, &e.SourceEnrollmentID, &e.ParticipantID, &e.MembershipType, &e.CommissionEligible, &e.SignedAt, &e.PaidOrderIDs, &e.RosterID, &e.ConfirmedAt)
		return e, err
	})
	if len(members) > maxDraftPayments {
		return nil, errDraftLimit
	}
	return members, err
}

func readPracticeTierHistory(ctx context.Context, tx pgx.Tx, summary draftSummary, config practiceSalesTiers) (practiceTierHistory, error) {
	history := practiceTierHistory{current: practiceHistoryPeriod{
		rosterID: *summary.PracticeRosterID, ordinal: summary.PeriodOrdinal, startsAt: &summary.PracticeStartsAt,
	}}
	if config.OpeningID != 0 {
		opening, err := readPracticeCountOpening(ctx, tx, config.OpeningID, summary.PeriodID)
		if err != nil {
			return history, err
		}
		history.opening = &opening
	}
	starts := []practiceCountStart{history.defaultStart(config)}
	for _, personal := range config.PersonCountStarts {
		starts = append(starts, personal.practiceCountStart)
	}
	var firstOrdinal int
	var firstTime *time.Time
	for _, start := range starts {
		if !start.includes(history.current) {
			continue
		}
		if start.FromAt != nil {
			if firstTime == nil || start.FromAt.Before(*firstTime) {
				firstTime = start.FromAt
			}
		} else if firstOrdinal == 0 || start.FromOrdinal < firstOrdinal {
			firstOrdinal = start.FromOrdinal
		}
	}

	rows, err := tx.Query(ctx, `SELECT p.ordinal,p.ends_on,roster.starts_at,
	 CASE WHEN roster.starts_at<$4
	  AND split_part(p.source_period_key,':',3)=split_part(current.source_period_key,':',3)
	  THEN roster.id ELSE 0 END, selected.components, settlement.period_id IS NOT NULL,
	  closed.practice_replay_inputs->'companyClassifications'
	 FROM periods current JOIN periods p ON p.product_id=current.product_id AND p.ordinal<current.ordinal AND p.calendar_month IS NULL
	 LEFT JOIN settlements settlement ON settlement.period_id=p.id
	 LEFT JOIN calculation_drafts closed ON closed.period_id=settlement.period_id
	 LEFT JOIN LATERAL (SELECT r.id,r.starts_at FROM practice_rosters r
	  WHERE r.period_key=p.source_period_key AND (closed.practice_roster_id IS NULL OR r.id=closed.practice_roster_id)
	  ORDER BY r.source_version DESC LIMIT 1) roster ON true
	 LEFT JOIN LATERAL (SELECT rv.components FROM rule_versions rv CROSS JOIN LATERAL (SELECT `+componentRuleRetiredAtSQL+` AS at) stopped
	  WHERE CASE WHEN closed.rule_id IS NOT NULL THEN rv.id=closed.rule_id
	   ELSE `+matchingRuleScope+` AND roster.starts_at IS NOT NULL AND (rv.effective_from IS NULL OR rv.effective_from<=roster.starts_at)
	     AND (stopped.at IS NULL OR roster.starts_at<stopped.at) END
	  ORDER BY `+matchingRuleOrder+` LIMIT 1) selected ON true
	 WHERE current.id=$1 AND (($2>0 AND p.ordinal>=$2)
	  OR ($3::timestamptz IS NOT NULL AND (roster.starts_at>=$3 OR p.ends_on>=($3 AT TIME ZONE 'Asia/Shanghai')::date)))
	 ORDER BY p.ordinal`, summary.PeriodID, firstOrdinal, firstTime, summary.PracticeStartsAt)
	if err != nil {
		return history, err
	}
	history.periods, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceHistoryPeriod, error) {
		var period practiceHistoryPeriod
		var components []component
		err := row.Scan(&period.ordinal, &period.endsOn, &period.startsAt, &period.rosterID, &components, &period.settled, &period.categories)
		period.ruleKnown = len(components) != 0
		period.companyOffers = practiceCompanyStandards(paymentRule{Components: components})
		for _, part := range components {
			if part.Type == "sales" && (part.ClassMember == "" || part.ClassMember == "new") {
				period.personalRole = part.SourceRole
				break
			}
		}
		return period, err
	})
	if err == nil {
		history.periods = slices.DeleteFunc(history.periods, func(period practiceHistoryPeriod) bool {
			for _, start := range starts {
				if start.includes(history.current) && start.includes(period) {
					return false
				}
			}
			return true
		})
	}
	return history, err
}

func computePracticeDraft(ctx context.Context, tx pgx.Tx, summary draftSummary, rule paymentRule) (paymentDraft, []byte, error) {
	rosterIDs := []int64{*summary.PracticeRosterID}
	members, err := readPracticeMembers(ctx, tx, rosterIDs, false)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	var history practiceTierHistory
	part := practiceTierComponent(rule)
	if part != nil && part.Tiers.hasCountTiers() {
		history, err = readPracticeTierHistory(ctx, tx, summary, *part.Tiers)
		if err != nil {
			return paymentDraft{}, nil, err
		}
		for _, period := range history.periods {
			if period.rosterID != 0 {
				rosterIDs = append(rosterIDs, period.rosterID)
			}
		}
		if len(rosterIDs) > 1 {
			history.members, err = readPracticeMembers(ctx, tx, rosterIDs[1:], true)
			if err != nil {
				return paymentDraft{}, nil, err
			}
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
		err := row.Scan(&item.ID, &item.Fact, &item.Fact.SalesCompanyID, &item.Fact.ReferrerCompanyID, &item.Fact.ReferrerConflict)
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

	rows, err = tx.Query(ctx, practiceDuplicatesSQL, rosterIDs)
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
	refunds, err := readPracticeRefunds(ctx, tx, summary.PeriodID, orders)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	appointments, err := loadPracticeAppointments(ctx, tx, rule)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	salesOrganizations, err := loadPracticeSalesOrganizations(ctx, tx, rule, members, orders, identities)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	companyOffers := practiceCompanyStandards(rule)
	var companyClassifications map[string]string
	if companyOffers.usesCategories() || slices.ContainsFunc(history.periods, func(period practiceHistoryPeriod) bool {
		return !period.settled && period.companyOffers.usesCategories()
	}) {
		companyClassifications, err = loadPracticeCompanyClassifications(ctx, tx, facts)
		if err != nil {
			return paymentDraft{}, nil, err
		}
		history.liveCategories = companyClassifications
	}
	companyRecipients, err := loadPracticeCompanyRecipients(ctx, tx, companyOffers, orders, companyClassifications)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	recommendations, err := loadPracticeCompanyRecommendations(ctx, tx, companyOffers, companyRecipients, orders, companyClassifications)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	pools, err := readPracticePoolUnits(ctx, tx, summary.PeriodID, rule.Components)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	runtime := practiceDraftRuntime{appointments: appointments, salesOrganizations: salesOrganizations, companyOffers: companyOffers,
		companyClassifications: companyClassifications, companyRecipients: companyRecipients, recommendations: recommendations, poolUnits: pools}
	if usesPracticeRegistrations(rule) {
		runtime.registrations, err = readPracticeRegistrationInputs(ctx, tx, *summary.PracticeRosterID, true, nil)
		if err != nil {
			return paymentDraft{}, nil, err
		}
	}
	result, err := calculatePracticeDraft(members, rule, orders, identities, blocked, history, matchPracticeRefunds(refunds, orders), runtime)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	result.PracticeStartsAt = summary.PracticeStartsAt
	result.PracticeEnrollments = make([]int64, 0, len(members))
	for _, member := range members {
		result.PracticeEnrollments = append(result.PracticeEnrollments, member.ID)
	}
	linkedRefunds := hasPracticeReplayDependencies(rule, members)
	if result.blockingIssues() == 0 && (linkedRefunds || hasPracticeCorporatePayees(rule)) {
		result.practiceReplay = capturePracticeReplayInputs(rule, result, facts, identities, runtime)
		result.practiceReplay.IndependentRefunds = !linkedRefunds
	}
	if err = attachReportOrganizations(ctx, tx, &result, facts); err != nil {
		return paymentDraft{}, nil, err
	}
	for _, refund := range refunds {
		facts = append(facts, refund.paymentInput)
	}
	digest, err := digestDraft(rule.ID, result, facts)
	return result, digest, err
}

func syncPracticeDraftMetadata(ctx context.Context, tx pgx.Tx, periodID, rosterID int64, issues []draftIssue) error {
	advisories := make([]draftIssue, 0)
	for _, issue := range issues {
		if issue.Advisory {
			advisories = append(advisories, issue)
		}
	}
	_, err := tx.Exec(ctx, `WITH incoming AS MATERIALIZED (
	 SELECT DISTINCT "factId" AS source_version_id,"enrollmentId" AS practice_enrollment_id,coalesce(component,'') AS component_type,code
	 FROM jsonb_to_recordset($3::jsonb) AS item("factId" bigint,"enrollmentId" bigint,component text,code text)
	), existing AS MATERIALIZED (
	 SELECT id,source_version_id,practice_enrollment_id,component_type,code
	 FROM calculation_draft_issues WHERE period_id=$1 AND advisory
	), removed AS (
	 DELETE FROM calculation_draft_issues WHERE id IN (
	  SELECT e.id FROM existing e WHERE NOT EXISTS(SELECT 1 FROM incoming i WHERE
	   (e.source_version_id,e.practice_enrollment_id,e.component_type,e.code)
	    IS NOT DISTINCT FROM (i.source_version_id,i.practice_enrollment_id,i.component_type,i.code))
	 ) RETURNING id
	), added AS (
	 INSERT INTO calculation_draft_issues(period_id,source_version_id,practice_enrollment_id,component_type,code,advisory)
	 SELECT $1,i.source_version_id,i.practice_enrollment_id,i.component_type,i.code,true FROM incoming i
	 WHERE NOT EXISTS(SELECT 1 FROM existing e WHERE
	  (e.source_version_id,e.practice_enrollment_id,e.component_type,e.code)
	   IS NOT DISTINCT FROM (i.source_version_id,i.practice_enrollment_id,i.component_type,i.code))
	 ORDER BY i.source_version_id,i.practice_enrollment_id,i.code RETURNING id
	)
	UPDATE calculation_drafts SET practice_roster_id=$2,calculated_at=clock_timestamp()
	WHERE period_id=$1 AND (practice_roster_id IS DISTINCT FROM $2 OR EXISTS(SELECT 1 FROM removed) OR EXISTS(SELECT 1 FROM added))`,
		periodID, rosterID, advisories)
	return err
}
