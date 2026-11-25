package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func importHealthReceipts(ctx context.Context, tx pgx.Tx, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var rows []struct {
		ID             int64      `json:"id"`
		Kind           string     `json:"kind"`
		ExternalID     string     `json:"externalId"`
		WireID         string     `json:"wireId"`
		CategoryID     *string    `json:"categoryId"`
		Base           *int64     `json:"financeBaseVersion"`
		Only           bool       `json:"acknowledgeOnly"`
		AcknowledgedAt *time.Time `json:"acknowledgedAt"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	for _, r := range rows {
		if r.ID <= 0 || (r.Kind != "COMPANY" && r.Kind != "MEMBER") || !textOK(r.ExternalID, 128) || r.Base == nil || *r.Base < -1 {
			return invalid
		}
		wire, ok := canonicalSourceUUID(r.WireID)
		if !ok {
			return invalid
		}
		entity := wire
		if r.Kind == "COMPANY" {
			err := tx.QueryRow(ctx, "SELECT unit_id::text FROM organization_dispatch_company_keys WHERE wire_id=$1", wire).Scan(&entity)
			if err != nil {
				return err
			}
			// A native company may never have received an outbound finance message. Restore its current source link
			// from the verified company alias, without reviving a retired source number.
			if !r.Only {
				_, err = tx.Exec(ctx, `INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id)
      SELECT 'DCAI','COMPANY',$1::uuid,$2 WHERE EXISTS(SELECT 1 FROM organization_company_sources WHERE source='DCAI'
      AND source_company_id=$2 AND company_id=$1 AND valid_from<=now() AND (valid_to IS NULL OR valid_to>now()))
      ON CONFLICT(target,entity_type,entity_id) DO NOTHING`, entity, r.ExternalID)
				if err != nil {
					return err
				}
			}
		} else {
			var present bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_dispatch_links WHERE target='DCAI' AND entity_type='MEMBER' AND entity_id=$1)`, wire).Scan(&present); err != nil {
				return err
			}
			if !present {
				return conflict
			}
		}
		var saved int64
		err := tx.QueryRow(ctx, `INSERT INTO organization_health_inbox AS old(source_id,kind,external_id,entity_id,wire_id,category_id,finance_base_version,acknowledge_only,acknowledged_at)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(source_id) DO UPDATE
   SET acknowledged_at=coalesce(old.acknowledged_at,excluded.acknowledged_at)
   WHERE (old.kind,old.external_id,old.entity_id,old.wire_id,old.category_id,old.finance_base_version,old.acknowledge_only)
   IS NOT DISTINCT FROM (excluded.kind,excluded.external_id,excluded.entity_id,excluded.wire_id,excluded.category_id,excluded.finance_base_version,excluded.acknowledge_only)
   RETURNING source_id`, r.ID, r.Kind, r.ExternalID, entity, wire, r.CategoryID, *r.Base, r.Only, r.AcknowledgedAt).Scan(&saved)
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict
		}
		if err != nil {
			return err
		}
	}
	return nil
}
