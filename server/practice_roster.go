package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var errPracticeRoster = errors.New("实战班名单格式或源版本不正确")
var errPracticeNotReady = errors.New("请关联实际开班的来源期次并导入确认名单")

type practiceRosterWire struct {
	ContractVersion string                   `json:"contract_version"`
	SourceSystem    string                   `json:"source_system"`
	PeriodKey       string                   `json:"period_key"`
	ActivityName    string                   `json:"activity_name,omitempty"`
	PeriodStartAt   string                   `json:"period_start_at"`
	StrategyID      int64                    `json:"strategy_id"`
	ActivityType    int64                    `json:"activity_type"`
	ActivityID      int64                    `json:"activity_id"`
	RosterVersion   int64                    `json:"roster_version"`
	RosterHash      string                   `json:"roster_hash"`
	ConfirmedAt     string                   `json:"confirmed_at"`
	ConfirmedBy     string                   `json:"confirmed_by"`
	Enrollments     []practiceEnrollmentWire `json:"enrollments"`
}

type practiceEnrollmentWire struct {
	SourceEnrollmentID string    `json:"source_enrollment_id"`
	SourceOrderID      *string   `json:"source_order_id"`
	PaidOrderIDs       *[]string `json:"paid_order_ids,omitempty"`
	ParticipantID      string    `json:"source_participant_id"`
	MembershipType     int       `json:"membership_type"`
	CommissionEligible bool      `json:"commission_eligible"`
	SignedAt           string    `json:"signed_at"`
}

type practiceEnrollment struct {
	ID                 int64     `json:"-"`
	RosterID           int64     `json:"-"`
	PeriodKey          string    `json:"-"`
	SourceEnrollmentID string    `json:"source_enrollment_id"`
	ParticipantID      string    `json:"participant_id"`
	MembershipType     int       `json:"membership_type"`
	CommissionEligible bool      `json:"commission_eligible"`
	SignedAt           time.Time `json:"signed_at"`
	ConfirmedAt        time.Time `json:"-"`
	PaidOrderIDs       []string  `json:"paid_order_ids"`
	ContentHash        string    `json:"hash,omitempty"`
}

type practiceRoster struct {
	Key, Hash             string
	SourceVersion         int64
	StartsAt, ConfirmedAt time.Time
	Enrollments           []practiceEnrollment
}

func practiceRosterHash(items []practiceEnrollmentWire) string {
	canonical := make([]map[string]any, 0, len(items))
	for _, row := range items {
		var order any
		if row.SourceOrderID != nil && *row.SourceOrderID != "" {
			order = *row.SourceOrderID
		}
		entry := map[string]any{
			"source_enrollment_id": row.SourceEnrollmentID, "source_order_id": order,
			"source_participant_id": row.ParticipantID, "membership_type": row.MembershipType,
			"commission_eligible": row.CommissionEligible, "signed_at": row.SignedAt,
		}
		if row.PaidOrderIDs != nil {
			entry["paid_order_ids"] = *row.PaidOrderIDs
		}
		canonical = append(canonical, entry)
	}
	data, _ := json.Marshal(canonical)
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func positiveSourceID(raw string) int64 {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != raw {
		return 0
	}
	return id
}

func validPracticeKey(key string) bool {
	parts := strings.Split(key, ":")
	return len(parts) == 4 && parts[0] == "PRACTICE" && (parts[1] == "1" || parts[1] == "2") &&
		positiveSourceID(parts[2]) != 0 && positiveSourceID(parts[3]) != 0
}

func normalizePracticeRoster(w practiceRosterWire) (practiceRoster, error) {
	starts, startError := time.Parse(time.RFC3339Nano, w.PeriodStartAt)
	confirmed, confirmedError := time.Parse(time.RFC3339Nano, w.ConfirmedAt)
	if w.ContractVersion != "dczd.practice-roster/1.0" || w.SourceSystem != "DCZD" ||
		!validPracticeKey(w.PeriodKey) || w.PeriodKey != fmt.Sprintf("PRACTICE:%d:%d:%d", w.ActivityType, w.StrategyID, w.ActivityID) ||
		w.RosterVersion < 1 || w.RosterVersion > 1000 || startError != nil || confirmedError != nil ||
		!textOK(w.ConfirmedBy, 64) || len(w.Enrollments) == 0 || len(w.Enrollments) > 10000 ||
		w.RosterHash != practiceRosterHash(w.Enrollments) {
		return practiceRoster{}, errPracticeRoster
	}
	result := practiceRoster{Key: w.PeriodKey, Hash: w.RosterHash, SourceVersion: w.RosterVersion, StartsAt: starts.UTC(), ConfirmedAt: confirmed.UTC()}
	previous := int64(0)
	for _, e := range w.Enrollments {
		signedAt, err := time.Parse(time.RFC3339Nano, e.SignedAt)
		sourceID := positiveSourceID(e.SourceEnrollmentID)
		if err != nil || signedAt.After(confirmed) || sourceID <= previous || positiveSourceID(e.ParticipantID) == 0 ||
			e.MembershipType < 1 || e.MembershipType > 3 {
			return practiceRoster{}, errPracticeRoster
		}
		previous = sourceID
		ids := make([]string, 0, 2)
		if e.PaidOrderIDs != nil {
			ids = append(ids, (*e.PaidOrderIDs)...)
		} else if e.CommissionEligible && e.SourceOrderID != nil {
			ids = append(ids, *e.SourceOrderID)
		}
		if e.SourceOrderID != nil && *e.SourceOrderID != "" && positiveSourceID(*e.SourceOrderID) == 0 {
			return practiceRoster{}, errPracticeRoster
		}
		if (!e.CommissionEligible && len(ids) != 0) || (e.CommissionEligible &&
			(e.MembershipType == 1 || len(ids) < 1 || len(ids) > 2 || e.SourceOrderID == nil || ids[len(ids)-1] != *e.SourceOrderID)) {
			return practiceRoster{}, errPracticeRoster
		}
		if len(ids) == 2 && ids[0] == ids[1] {
			return practiceRoster{}, errPracticeRoster
		}
		for _, id := range ids {
			if positiveSourceID(id) == 0 {
				return practiceRoster{}, errPracticeRoster
			}
		}
		row := practiceEnrollment{SourceEnrollmentID: e.SourceEnrollmentID, ParticipantID: e.ParticipantID, MembershipType: e.MembershipType,
			CommissionEligible: e.CommissionEligible, SignedAt: signedAt.UTC(), PaidOrderIDs: ids}
		data, err := json.Marshal(row)
		if err != nil {
			return practiceRoster{}, err
		}
		hash := sha256.Sum256(data)
		row.ContentHash = hex.EncodeToString(hash[:])
		result.Enrollments = append(result.Enrollments, row)
	}
	return result, nil
}

const practiceIncomingRows = `jsonb_to_recordset($2::jsonb) AS input(
 source_enrollment_id text, participant_id text, membership_type smallint, commission_eligible boolean,
 signed_at timestamptz, paid_order_ids text[], hash text)`

func storePracticeRoster(ctx context.Context, tx pgx.Tx, roster practiceRoster) (bool, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO practice_rosters(period_key,source_version,source_hash,starts_at,confirmed_at,attendance_count)
	 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(period_key,source_version) DO NOTHING RETURNING id`,
		roster.Key, roster.SourceVersion, roster.Hash, roster.StartsAt, roster.ConfirmedAt, len(roster.Enrollments)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var same bool
		err = tx.QueryRow(ctx, `SELECT source_hash=$3 AND starts_at=$4 AND confirmed_at=$5 FROM practice_rosters
		 WHERE period_key=$1 AND source_version=$2`, roster.Key, roster.SourceVersion, roster.Hash, roster.StartsAt, roster.ConfirmedAt).Scan(&same)
		if err == nil && !same {
			return false, conflict
		}
		return false, err
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO practice_enrollments(period_key,source_enrollment_id,participant_id,membership_type,
	 commission_eligible,signed_at,paid_order_ids,hash) SELECT $1,source_enrollment_id,participant_id,membership_type,
	 commission_eligible,signed_at,paid_order_ids,decode(hash,'hex') FROM `+practiceIncomingRows+` ON CONFLICT DO NOTHING`, roster.Key, roster.Enrollments); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO practice_roster_members(roster_id,enrollment_id) SELECT $3,e.id FROM `+practiceIncomingRows+`
	 JOIN practice_enrollments e ON e.period_key=$1 AND e.source_enrollment_id=input.source_enrollment_id AND e.hash=decode(input.hash,'hex')`,
		roster.Key, roster.Enrollments, id)
	if err == nil {
		err = enqueueDraftConfiguration(ctx, tx, draftRefreshRoster, roster.Key, roster.SourceVersion)
	}
	return err == nil, err
}
