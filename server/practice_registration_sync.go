package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const practiceRegistrationsContract = "dczd.finance-practice-registrations-read/1.0"

var errPracticeRegistrationSync = errors.New("同步实战班报名和签到记录失败，请稍后重试")
var errPracticeRegistrationPayment = errors.New("有已签到报名缺少有效的付款关联，请检查来源报名记录")
var errPracticeRefundCutoffRequired = errors.New("请在核算期次中设置本期退款截止时间")

type practiceRegistrationSnapshotRow struct {
	ID             string     `json:"source_enrollment_id"`
	ParticipantID  string     `json:"source_participant_id"`
	OrderID        *string    `json:"registration_order_id"`
	MembershipType int        `json:"membership_type"`
	State          int        `json:"state"`
	Enabled        *bool      `json:"enabled"`
	SignedAt       *time.Time `json:"signed_at"`
	ReferrerID     *string    `json:"referrer_id"`
	CompanyID      *string    `json:"referrer_company_id"`
}

type practiceRegistrationSnapshot struct {
	practiceRosterWire
	Registrations     []practiceRegistrationSnapshotRow `json:"registrations"`
	ValidPaymentLinks *bool                             `json:"valid_payment_links"`
}

func (c orderFeedClient) practiceRegistrations(ctx context.Context, key string) (practiceRegistrationSnapshot, error) {
	var snapshot practiceRegistrationSnapshot
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	err := c.getWithContract(ctx, "practice-registrations", practiceRegistrationsContract, url.Values{"period_key": {key}}, &snapshot)
	if err != nil {
		return snapshot, err
	}
	if snapshot.ContractVersion != practiceRegistrationsContract || snapshot.PeriodKey != key || snapshot.SourceSystem != "DCZD" ||
		key != fmt.Sprintf("PRACTICE:%d:%d:%d", snapshot.ActivityType, snapshot.StrategyID, snapshot.ActivityID) ||
		snapshot.ValidPaymentLinks == nil || snapshot.Registrations == nil || snapshot.Enrollments == nil || len(snapshot.Registrations) > 30000 || len(snapshot.Enrollments) > 10000 ||
		snapshot.RosterHash != practiceRosterHash(snapshot.Enrollments) {
		return snapshot, errPracticeRegistrationSync
	}
	if _, err := time.Parse(time.RFC3339Nano, snapshot.PeriodStartAt); err != nil {
		return snapshot, errPracticeRegistrationSync
	}
	last := int64(0)
	signed := 0
	for _, row := range snapshot.Registrations {
		id := positiveSourceID(row.ID)
		if id <= last || positiveSourceID(row.ParticipantID) == 0 || row.Enabled == nil ||
			!validRegistrationSourceID(row.OrderID) || !validRegistrationSourceID(row.ReferrerID) || !validRegistrationSourceID(row.CompanyID) {
			return snapshot, errPracticeRegistrationSync
		}
		last = id
		if !*snapshot.ValidPaymentLinks || !*row.Enabled || row.State != 2 || row.SignedAt == nil {
			continue
		}
		if signed >= len(snapshot.Enrollments) {
			return snapshot, errPracticeRegistrationSync
		}
		member := snapshot.Enrollments[signed]
		signedAt, err := time.Parse(time.RFC3339Nano, member.SignedAt)
		if err != nil || member.SourceEnrollmentID != row.ID || member.ParticipantID != row.ParticipantID ||
			member.MembershipType != row.MembershipType || !signedAt.Equal(*row.SignedAt) ||
			member.CommissionEligible && (row.OrderID == nil || member.SourceOrderID == nil || *row.OrderID != *member.SourceOrderID) {
			return snapshot, errPracticeRegistrationSync
		}
		signed++
	}
	if signed != len(snapshot.Enrollments) {
		return snapshot, errPracticeRegistrationSync
	}
	return snapshot, nil
}

func (snapshot practiceRegistrationSnapshot) signature() string {
	type attribution struct {
		ReferrerID *string
		CompanyID  *string
	}
	refs := make([]attribution, 0, len(snapshot.Enrollments))
	var refunded []practiceRegistrationSnapshotRow
	for _, row := range snapshot.Registrations {
		if !*row.Enabled || row.SignedAt == nil {
			continue
		}
		switch row.State {
		case 2:
			refs = append(refs, attribution{ReferrerID: row.ReferrerID, CompanyID: row.CompanyID})
		case 3:
			if row.OrderID != nil && (row.MembershipType == 2 || row.MembershipType == 3) {
				refunded = append(refunded, row)
			}
		}
	}
	encoded, _ := json.Marshal(refs)
	payload := fmt.Appendf(nil, "%t:%s:", *snapshot.ValidPaymentLinks, snapshot.RosterHash)
	payload = append(payload, encoded...)
	if len(refunded) != 0 {
		data, _ := json.Marshal(refunded)
		payload = append(payload, data...)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
}

func validRegistrationSourceID(value *string) bool {
	return value == nil || positiveSourceID(*value) != 0
}

type practiceSyncPeriod struct {
	id  int64
	key string
}

func readPracticeSyncPeriods(ctx context.Context, conn interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, jobID, periodID int64) ([]practiceSyncPeriod, error) {
	rows, err := conn.Query(ctx, `SELECT p.id,p.source_period_key
	 FROM periods p JOIN products pro ON pro.id=p.product_id
	 WHERE pro.period_mode='class' AND p.calendar_month IS NULL
	  AND EXISTS(SELECT 1 FROM source_mappings m WHERE m.product_id=p.product_id AND m.source='DCZD' AND m.domain='PRACTICE'
	   AND m.source_key IN ('1','4') AND m.source_key=split_part(p.source_period_key,':',3))
	  AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=p.id)
	  AND (p.id=$2 OR $2=0 AND `+periodReportMonthSQL+`>=date_trunc('month',coalesce(
	   (SELECT started_at FROM dcai_sync_jobs WHERE id=$1),statement_timestamp()) AT TIME ZONE 'Asia/Shanghai')::date)
	 ORDER BY p.starts_on DESC,p.id DESC`, jobID, periodID)
	if err != nil {
		return nil, err
	}
	periods, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceSyncPeriod, error) {
		var item practiceSyncPeriod
		err := row.Scan(&item.id, &item.key)
		return item, err
	})
	if err == nil && periodID != 0 && len(periods) == 0 {
		err = errPracticeNotReady
	}
	return periods, err
}

func syncPracticeRegistrations(ctx context.Context, conn *pgxpool.Conn, client orderFeedClient, jobID int64) error {
	periods, err := readPracticeSyncPeriods(ctx, conn, jobID, 0)
	if err != nil {
		return err
	}
	for _, period := range periods {
		snapshot, err := client.practiceRegistrations(ctx, period.key)
		if err != nil {
			return fmt.Errorf("%w: %w", errPracticeRegistrationSync, err)
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			eligible, err := lockSynchronizedPracticePeriod(ctx, tx, period)
			if err != nil || !eligible {
				return err
			}
			return storePracticeRegistrationSnapshot(ctx, tx, snapshot)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func lockSynchronizedPracticePeriod(ctx context.Context, tx pgx.Tx, period practiceSyncPeriod) (bool, error) {
	var productID int64
	var key string
	err := tx.QueryRow(ctx, practiceRosterPullScope+" FOR NO KEY UPDATE OF p", period.id).Scan(&productID, &key)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && key != period.key {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = readSettlement(ctx, tx, period.id)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return false, err
}

func storePracticeRegistrationSnapshot(ctx context.Context, tx pgx.Tx, snapshot practiceRegistrationSnapshot) error {
	var latest int64
	var same bool
	key := snapshot.PeriodKey
	signature := snapshot.signature()
	err := tx.QueryRow(ctx, `SELECT source_version,
	 from_registration_sync AND split_part(source_hash,$4,1)=$2 AND starts_at=$3::timestamptz
	 FROM practice_rosters WHERE period_key=$1 ORDER BY source_version DESC LIMIT 1`,
		key, signature, snapshot.PeriodStartAt, practiceLateRefundRosterSuffix).Scan(&latest, &same)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !same {
		wire := snapshot.practiceRosterWire
		wire.RosterVersion = latest + 1
		wire.ConfirmedAt, wire.ConfirmedBy = time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), "registration-sync"
		wire.ContractVersion = "dczd.practice-roster/1.0"
		roster, err := normalizePracticeRosterFromSync(wire)
		if err != nil {
			return err
		}
		roster.Hash = signature
		if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO practice_registrations(period_key,registrations,valid_payment_links) VALUES($1,$2,$3)
	 ON CONFLICT(period_key) DO UPDATE SET registrations=excluded.registrations,valid_payment_links=excluded.valid_payment_links
	 WHERE (practice_registrations.registrations,practice_registrations.valid_payment_links)
	   IS DISTINCT FROM (excluded.registrations,excluded.valid_payment_links)`, key, snapshot.Registrations, *snapshot.ValidPaymentLinks)
	return err
}
