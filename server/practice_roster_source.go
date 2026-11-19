package main

import (
	"context"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const practiceRosterReadContract = "dczd.finance-practice-roster-read/1.0"

var (
	errPracticeRosterSource      = errors.New("暂时无法核验签到名单，请检查大春之道名单接口")
	errPracticeRosterUnconfirmed = errors.New("签到名单尚未确认或已有变更，请在大春之道确认后重新拉取")
	errPracticeRosterNeedsPull   = errors.New("签到名单版本已变化，请重新拉取并试算")
	errPracticeRosterOverlap     = errors.New("报名付款同时出现在多个期次，请在大春之道核对延期")
)

type practiceRosterSourceSnapshot struct {
	Contract        string              `json:"contract_version"`
	Key             string              `json:"period_key"`
	Current         *bool               `json:"current"`
	PaymentConflict *bool               `json:"has_payment_conflict"`
	Version         int64               `json:"roster_version"`
	Hash            string              `json:"roster_hash"`
	StartsAt        time.Time           `json:"period_start_at"`
	ConfirmedAt     time.Time           `json:"confirmed_at"`
	FirstConfirmed  time.Time           `json:"first_confirmed_at"`
	First           *practiceRosterWire `json:"first_roster"`
	Latest          *practiceRosterWire `json:"latest_roster"`
}

func (s workspace) fetchPracticeRoster(ctx context.Context, key string, includeVersions bool) (practiceRosterSourceSnapshot, error) {
	var snapshot practiceRosterSourceSnapshot
	client, err := newOrderFeedClient("DCZD", s.sources["DCZD"])
	if err != nil {
		if errors.Is(err, errSourceDisabled) {
			return snapshot, errPracticeSettlementUnavailable
		}
		return snapshot, errPracticeRosterSource
	}
	query := url.Values{"period_key": {key}}
	if includeVersions {
		query.Set("include_versions", "1")
	}
	deadline := 3 * time.Second
	if includeVersions {
		deadline = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	if err := client.getWithContract(ctx, "practice-rosters", practiceRosterReadContract, query, &snapshot); err != nil ||
		snapshot.Contract != practiceRosterReadContract || snapshot.Key != key || snapshot.Current == nil {
		return snapshot, errPracticeRosterSource
	}
	if !*snapshot.Current {
		return snapshot, errPracticeRosterUnconfirmed
	}
	hash, err := hex.DecodeString(strings.TrimPrefix(snapshot.Hash, "sha256:"))
	if err != nil || snapshot.PaymentConflict == nil || !strings.HasPrefix(snapshot.Hash, "sha256:") || len(hash) != 32 || snapshot.Version < 1 || snapshot.Version > 1000 ||
		snapshot.StartsAt.IsZero() || snapshot.ConfirmedAt.IsZero() || snapshot.FirstConfirmed.IsZero() ||
		snapshot.FirstConfirmed.After(snapshot.ConfirmedAt) || snapshot.Version == 1 && !snapshot.FirstConfirmed.Equal(snapshot.ConfirmedAt) {
		return snapshot, errPracticeRosterSource
	}
	return snapshot, nil
}

func (snapshot practiceRosterSourceSnapshot) rosters() ([]practiceRoster, error) {
	if snapshot.First == nil || snapshot.Latest == nil {
		return nil, errPracticeRosterSource
	}
	first, err := normalizePracticeRoster(*snapshot.First)
	if err != nil || first.Key != snapshot.Key || first.SourceVersion != 1 || !first.ConfirmedAt.Equal(snapshot.FirstConfirmed) {
		return nil, errPracticeRosterSource
	}
	latest, err := normalizePracticeRoster(*snapshot.Latest)
	if err != nil || latest.Key != snapshot.Key || latest.SourceVersion != snapshot.Version || latest.Hash != snapshot.Hash ||
		!latest.StartsAt.Equal(snapshot.StartsAt) || !latest.ConfirmedAt.Equal(snapshot.ConfirmedAt) {
		return nil, errPracticeRosterSource
	}
	if latest.SourceVersion == 1 {
		if first.Hash != latest.Hash || !first.StartsAt.Equal(latest.StartsAt) {
			return nil, errPracticeRosterSource
		}
		return []practiceRoster{latest}, nil
	}
	return []practiceRoster{first, latest}, nil
}

const practiceRosterPullScope = `SELECT p.source_period_key FROM periods p JOIN products pro ON pro.id=p.product_id
 WHERE p.id=$1 AND pro.period_mode='class' AND p.calendar_month IS NULL AND p.source_period_key IS NOT NULL
 AND EXISTS(SELECT 1 FROM source_mappings m WHERE m.product_id=p.product_id AND m.source='DCZD' AND m.domain='PRACTICE')`

func (s workspace) pullPracticeRoster(ctx context.Context, input calculateDraftInput) (any, error) {
	if input.PeriodID < 1 {
		return nil, invalid
	}
	var key string
	var closed bool
	err := s.db.QueryRow(ctx, `SELECT scope.source_period_key,EXISTS(SELECT 1 FROM settlements WHERE period_id=$1)
	 FROM (`+practiceRosterPullScope+`) scope`, input.PeriodID).Scan(&key, &closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errPracticeNotReady
	}
	if err != nil {
		return nil, err
	}
	if closed {
		return nil, errSettlementClosed
	}
	snapshot, err := s.fetchPracticeRoster(ctx, key, true)
	if err != nil {
		return nil, err
	}
	rosters, err := snapshot.rosters()
	if err != nil {
		return nil, err
	}
	var inserted bool
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var currentKey string
		if err := tx.QueryRow(ctx, practiceRosterPullScope+" FOR NO KEY UPDATE OF p", input.PeriodID).Scan(&currentKey); err != nil {
			return err
		}
		if key != currentKey {
			return errPracticeRosterNeedsPull
		}
		if _, err := readSettlement(ctx, tx, input.PeriodID); err == nil {
			return errSettlementClosed
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var currentVersion int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(source_version),0) FROM practice_rosters WHERE period_key=$1`, key).Scan(&currentVersion); err != nil {
			return err
		}
		if currentVersion > snapshot.Version {
			return errPracticeRosterNeedsPull
		}
		for _, roster := range rosters {
			changed, err := storePracticeRoster(ctx, tx, roster)
			if err != nil {
				return err
			}
			inserted = inserted || changed
		}
		return nil
	})
	return struct {
		Version int64 `json:"version"`
		Changed bool  `json:"changed"`
	}{Version: snapshot.Version, Changed: inserted}, err
}
