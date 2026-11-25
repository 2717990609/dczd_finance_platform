package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const orgHealthChangesPath = "/internal/v1/organization-sync/health/changes"
const orgHealthAckPath = "/internal/v1/organization-sync/health/ack"

type organizationHealthClient interface {
	Post(context.Context, string, any, any) error
}

type organizationHealthEvent struct {
	ID           int64     `json:"id"`
	Kind         string    `json:"entity_type"`
	ExternalID   string    `json:"external_id"`
	CompanyID    string    `json:"company_id"`
	UserID       string    `json:"user_id"`
	Name         string    `json:"name"`
	CategoryID   string    `json:"category_id"`
	CategoryName string    `json:"category_name"`
	Status       int       `json:"status"`
	Owner        int       `json:"is_owner"`
	At           time.Time `json:"occurred_at"`
	Base         int64     `json:"finance_base_version"`
}

// Missing wire fields must remain distinguishable from a legitimate disabled state or finance version zero.
func (e *organizationHealthEvent) UnmarshalJSON(data []byte) error {
	type wireEvent organizationHealthEvent
	value := wireEvent{Status: -1, Owner: -1, Base: -2}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*e = organizationHealthEvent(value)
	return nil
}

type organizationHealthAck struct {
	ID       int64  `json:"id"`
	WireID   string `json:"entity_id"`
	Category string `json:"category_id,omitempty"`
	Only     bool   `json:"acknowledge_only,omitempty"`
}

type healthFinanceState struct {
	EntityID, WireID, State string
	Version, Applied        int64
	Value, Intent           json.RawMessage
}

func (s workspace) startOrganizationHealth(ctx context.Context) (func(), error) {
	if os.Getenv("ORG_SYNC_DCAI_ENABLED") != "true" {
		return func() {}, nil
	}
	client, err := newOrganizationSyncClient("DCAI")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		var after int64
		for {
			if err := s.pollOrganizationHealth(ctx, client, &after); err != nil && ctx.Err() == nil {
				log.Print("DCAI health organization polling failed")
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

const healthAckColumns = `source_id,wire_id::text,coalesce(category_id::text,''),acknowledge_only`

func readOrganizationHealthAck(row pgx.Row) (organizationHealthAck, error) {
	var ack organizationHealthAck
	err := row.Scan(&ack.ID, &ack.WireID, &ack.Category, &ack.Only)
	return ack, err
}

func (s workspace) sendHealthAck(ctx context.Context, client organizationHealthClient, ack organizationHealthAck) error {
	var reply struct {
		Acknowledged bool `json:"acknowledged"`
	}
	if err := client.Post(ctx, orgHealthAckPath, ack, &reply); err != nil {
		return err
	}
	if !reply.Acknowledged {
		return errors.New("AI health acknowledgment rejected")
	}
	_, err := s.db.Exec(ctx, `UPDATE organization_health_inbox SET acknowledged_at=coalesce(acknowledged_at,now()) WHERE source_id=$1`, ack.ID)
	return err
}

func (s workspace) pollOrganizationHealth(ctx context.Context, client organizationHealthClient, after *int64) error {
	rows, err := s.db.Query(ctx, `SELECT `+healthAckColumns+` FROM organization_health_inbox WHERE acknowledged_at IS NULL ORDER BY source_id LIMIT 50`)
	if err != nil {
		return err
	}
	pending, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (organizationHealthAck, error) { return readOrganizationHealthAck(row) })
	if err != nil {
		return err
	}
	for _, ack := range pending {
		if err := s.sendHealthAck(ctx, client, ack); err != nil && ctx.Err() == nil {
			log.Printf("DCAI health acknowledgment %d pending", ack.ID)
		}
	}
	for batch := 0; batch < 20; batch++ {
		var page struct {
			List []organizationHealthEvent `json:"list"`
		}
		if err := client.Post(ctx, orgHealthChangesPath, map[string]int64{"after_id": *after}, &page); err != nil {
			return err
		}
		for _, e := range page.List {
			if e.ID <= *after {
				return errors.New("unexpected AI health page order")
			}
			*after = e.ID
			ack, err := s.receiveOrganizationHealth(ctx, e)
			if err != nil {
				log.Printf("DCAI health event %d pending", e.ID)
				continue
			}
			if ack.ID != 0 {
				if err := s.sendHealthAck(ctx, client, ack); err != nil {
					log.Printf("DCAI health acknowledgment %d pending", e.ID)
				}
			}
		}
		if len(page.List) < 50 {
			*after = 0
			break
		}
	}
	return ctx.Err()
}

func lockHealthEvent(ctx context.Context, tx pgx.Tx, e organizationHealthEvent) error {
	if e.Kind == "MEMBER" && e.UserID != "" {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "ai-health-user:"+e.UserID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "ai-health:"+e.Kind+":"+e.ExternalID)
	return err
}

func (s workspace) receiveOrganizationHealth(ctx context.Context, e organizationHealthEvent) (organizationHealthAck, error) {
	var ack organizationHealthAck
	if e.ID <= 0 || (e.Kind != "COMPANY" && e.Kind != "MEMBER") || !textOK(e.ExternalID, 128) {
		return ack, invalid
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(637619241)"); err != nil {
			return err
		}
		if err := lockHealthEvent(ctx, tx, e); err != nil {
			return err
		}
		existing, err := readOrganizationHealthAck(tx.QueryRow(ctx, "SELECT "+healthAckColumns+" FROM organization_health_inbox WHERE source_id=$1", e.ID))
		if err == nil {
			ack = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// A late page or a replay after an operator decision may contain a superseded change.
		err = tx.QueryRow(ctx, `INSERT INTO organization_health_inbox(source_id,kind,external_id,entity_id,wire_id,finance_base_version,acknowledge_only)
   SELECT $1,kind,external_id,entity_id,wire_id,$4,true FROM organization_health_inbox
   WHERE kind=$2 AND external_id=$3 AND source_id>$1 ORDER BY source_id DESC LIMIT 1
   RETURNING wire_id::text`, e.ID, e.Kind, e.ExternalID, e.Base).Scan(&ack.WireID)
		if err == nil {
			ack.ID = e.ID
			ack.Only = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_health_conflicts WHERE source_id=$1)`, e.ID).Scan(&exists); err != nil {
			return err
		}
		actual, err := loadHealthFinanceState(ctx, tx, e)
		if err != nil {
			return err
		}
		if exists {
			// An operator may refresh the finance-side mapping or stop a failed delivery while a native change is pending.
			_, err = tx.Exec(ctx, `UPDATE organization_health_conflicts SET entity_id=nullif($2,'')::uuid,version=$3,applied_version=$4,
    finance_value=jsonb_build_object('observed',$5::jsonb,'intent',$6::jsonb)
    WHERE source_id=$1 AND state='OPEN' AND (version,applied_version,entity_id,finance_value) IS DISTINCT FROM
      ($3,$4,nullif($2,'')::uuid,jsonb_build_object('observed',$5::jsonb,'intent',$6::jsonb))`, e.ID, actual.EntityID, actual.Version, actual.Applied, actual.Value, actual.Intent)
			return err
		}
		reason := ""
		switch {
		case !validHealthFields(e):
			reason = "SOURCE_INVALID_FIELDS"
		case actual.Version > e.Base && actual.Version > 0 && actual.State != "STOPPED" || actual.Applied > e.Base && actual.Applied > 0 || e.Base > actual.Version:
			reason = "DIVERGENT_EDITS"
		case actual.State == "PROCESSING" || actual.State == "PENDING" || actual.State == "RETRYABLE":
			return nil
		case e.Base > actual.Applied:
			reason = "DIVERGENT_EDITS"
		default:
			err = pgx.BeginFunc(ctx, tx, func(inner pgx.Tx) error {
				var err error
				ack, err = applyHealthSnapshot(ctx, inner, e)
				return err
			})
			if err == nil {
				return closeOlderHealthConflicts(ctx, tx, e, "SUPERSEDED", "后续 AI 事件已应用", nil)
			}
			if !healthBusinessConflict(err) {
				return err
			}
			ack = organizationHealthAck{}
			reason = "SOURCE_STATE_CONFLICT"
		}
		_, err = tx.Exec(ctx, `INSERT INTO organization_health_conflicts(source_id,kind,external_id,entity_id,version,applied_version,finance_value,ai_value,reason)
   VALUES($1,$2,$3,nullif($4,'')::uuid,$5,$6,jsonb_build_object('observed',$7::jsonb,'intent',$8::jsonb),$9,$10)`, e.ID, e.Kind, e.ExternalID, actual.EntityID, actual.Version, actual.Applied, actual.Value, actual.Intent, e, reason)
		return err
	})
	return ack, err
}

func validHealthFields(e organizationHealthEvent) bool {
	return !e.At.IsZero() && e.ExternalID != "0" && textOK(e.Name, 200) && (e.Status == 0 || e.Status == 1) && e.Base >= -1 && ((e.Kind == "COMPANY" && e.CategoryID != "0" && textOK(e.CategoryID, 128)) ||
		(e.Kind == "MEMBER" && e.CompanyID != "0" && e.UserID != "0" && textOK(e.CompanyID, 128) && textOK(e.UserID, 128) && (e.Owner == 0 || e.Owner == 1)))
}

func healthBusinessConflict(err error) bool {
	var dbErr *pgconn.PgError
	return errors.Is(err, conflict) || errors.Is(err, invalid) || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errAppointmentOverlap) ||
		errors.Is(err, errAppointmentUnitInactive) || errors.As(err, &dbErr) && (dbErr.Code == "23505" || dbErr.Code == "23P01")
}
