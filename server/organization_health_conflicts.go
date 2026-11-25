package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

const organizationHealthConflictsSQL = `SELECT coalesce(jsonb_agg(to_jsonb(rows) ORDER BY (state='RESOLVED'),id DESC),'[]') FROM (
 SELECT source_id AS id,kind,external_id AS "externalId",entity_id AS "entityId",version,applied_version AS "appliedVersion",
 finance_value AS "finance",ai_value AS "ai",reason,state,decision,evidence,resolved_at AS "resolvedAt"
 FROM organization_health_conflicts ORDER BY (state='RESOLVED'),source_id DESC LIMIT 100) rows`

type organizationHealthDecision struct {
	ID       int64  `json:"id"`
	Version  int64  `json:"version"`
	Decision string `json:"decision"`
	Evidence string `json:"evidence"`
}

func organizationHealthDispatchBase(ctx context.Context, tx pgx.Tx, kind, entity, member string) (int64, error) {
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_health_conflicts WHERE kind=$1 AND entity_id=$2 AND state IN ('OPEN','RESOLVING'))`, kind, entity).Scan(&pending); err != nil {
		return 0, err
	}
	if pending {
		return 0, conflict
	}
	var revision int64
	err := tx.QueryRow(ctx, `SELECT coalesce(max(source_id),0) FROM organization_health_inbox
    WHERE kind=$1 AND entity_id=$2 AND ($1='COMPANY' OR external_id=$3)`, kind, entity, member).Scan(&revision)
	return revision, err
}

func (s workspace) resolveOrganizationHealth(ctx context.Context, in organizationHealthDecision) (any, error) {
	in.Evidence = strings.TrimSpace(in.Evidence)
	if in.ID < 1 || in.Version < 0 || (in.Decision != "ACCEPT_AI" && in.Decision != "KEEP_FINANCE") || !textOK(in.Evidence, 256) {
		return nil, invalid
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		var e organizationHealthEvent
		err := tx.QueryRow(ctx, `SELECT ai_value FROM organization_health_conflicts WHERE source_id=$1`, in.ID).Scan(&e)
		if err != nil {
			return err
		}
		if err = lockHealthEvent(ctx, tx, e); err != nil {
			return err
		}
		actual, err := loadHealthFinanceState(ctx, tx, e)
		if err != nil {
			return err
		}
		var state, entity string
		var version int64
		var later bool
		err = tx.QueryRow(ctx, `SELECT state,coalesce(entity_id::text,''),version,
    EXISTS(SELECT 1 FROM organization_health_conflicts WHERE kind=$2 AND external_id=$3 AND source_id>$1
     UNION ALL SELECT 1 FROM organization_health_inbox WHERE kind=$2 AND external_id=$3 AND source_id>$1)
    FROM organization_health_conflicts WHERE source_id=$1 FOR UPDATE`, in.ID, e.Kind, e.ExternalID).Scan(&state, &entity, &version, &later)
		if err != nil {
			return err
		}
		if state != "OPEN" || later || actual.State == "PROCESSING" || version != in.Version || actual.Version != version || entity != "" && actual.EntityID != entity {
			return conflict
		}
		if actual.EntityID != "" {
			// Stop every older undelivered intent before adopting or correcting a native snapshot.
			// Lock the rows so a worker cannot claim one between this check and the decision.
			var processing bool
			err = tx.QueryRow(ctx, `SELECT coalesce(bool_or(state='PROCESSING'),false) FROM (
    SELECT state FROM organization_dispatch_outbox WHERE target='DCAI' AND entity_type=$1 AND entity_id=$2 AND version<=$3
    AND state NOT IN ('STOPPED','APPLIED') ORDER BY version FOR UPDATE) pending`, e.Kind, actual.EntityID, actual.Version).Scan(&processing)
			if err != nil {
				return err
			}
			if processing {
				return conflict
			}
			_, err = tx.Exec(ctx, `UPDATE organization_dispatch_outbox SET state='STOPPED',reason='AI_RECONCILIATION'
    WHERE target='DCAI' AND entity_type=$1 AND entity_id=$2 AND version<=$3 AND state NOT IN ('STOPPED','APPLIED')`, e.Kind, actual.EntityID, actual.Version)
			if err != nil {
				return err
			}
		}
		if in.Decision == "ACCEPT_AI" {
			if _, err = applyHealthSnapshot(ctx, tx, e); err != nil {
				if healthBusinessConflict(err) {
					return conflict
				}
				return err
			}
			return closeOlderHealthConflicts(ctx, tx, e, in.Decision, in.Evidence, by)
		}
		if actual.EntityID == "" || actual.WireID == "" {
			return conflict
		}
		typ, payload, err := healthCorrectionPayload(e, actual)
		if err != nil {
			return err
		}
		message, err := enqueueDispatch(ctx, tx, "DCAI", e.Kind, actual.EntityID, typ, payload, nil, by)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE organization_health_conflicts SET state='RESOLVING',decision=$4,evidence=$5,resolved_by=$6,resolution_message_id=$7,resolved_at=NULL
   WHERE kind=$1 AND external_id=$2 AND source_id<=$3 AND state IN ('OPEN','RESOLVING')`, e.Kind, e.ExternalID, e.ID, in.Decision, in.Evidence, by, message)
		return err
	})
	return map[string]int64{"id": in.ID}, err
}

func closeOlderHealthConflicts(ctx context.Context, tx pgx.Tx, e organizationHealthEvent, decision, evidence string, by *string) error {
	_, err := tx.Exec(ctx, `UPDATE organization_health_conflicts SET state='RESOLVED',decision=$4,evidence=$5,resolved_by=$6,resolved_at=now()
  WHERE kind=$1 AND external_id=$2 AND source_id<=$3 AND state IN ('OPEN','RESOLVING')`, e.Kind, e.ExternalID, e.ID, decision, evidence, by)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO organization_health_inbox(source_id,kind,external_id,entity_id,wire_id,finance_base_version,acknowledge_only)
  SELECT c.source_id,c.kind,c.external_id,i.entity_id,i.wire_id,(c.ai_value->>'finance_base_version')::bigint,true
  FROM organization_health_conflicts c JOIN organization_health_inbox i ON i.source_id=$3
  WHERE c.kind=$1 AND c.external_id=$2 AND c.source_id<$3 AND c.state='RESOLVED' ON CONFLICT DO NOTHING`, e.Kind, e.ExternalID, e.ID)
	return err
}

func healthCorrectionPayload(e organizationHealthEvent, a healthFinanceState) (string, map[string]any, error) {
	var p, intent map[string]any
	if err := json.Unmarshal(a.Value, &p); err != nil {
		return "", nil, err
	}
	if err := json.Unmarshal(a.Intent, &intent); err != nil {
		return "", nil, err
	}
	// Completed operations are reflected in the observed finance records. Replaying a
	// completed transfer or a stopped rename would resurrect an obsolete intent.
	if a.State != "STOPPED" && a.State != "APPLIED" {
		for k, v := range intent {
			p[k] = v
		}
	}
	p["ai_resolution"] = true
	p["ai_base_revision"] = e.ID
	if e.Kind == "COMPANY" {
		if p["category_id"] == nil {
			return "", nil, conflict
		}
		return "sale_company_upsert", p, nil
	}
	if p["company_id"] == nil || p["external_member_id"] == "" || p["external_member_id"] != e.ExternalID {
		return "", nil, conflict
	}
	role, state := p["role"], p["status"]
	delete(p, "role")
	delete(p, "status")
	switch p["operation"] {
	case "TRANSFER", "RECONCILE":
		return "sale_company_membership_set", p, nil
	case "CREATE":
		return "", nil, conflict
	case "DISABLE":
		state = "INACTIVE"
	case "ENABLE":
		state = "ACTIVE"
	case "SET_OWNER":
		role = "OWNER"
	case "UNSET_OWNER":
		role = "MEMBER"
	}
	p["operation"] = "RECONCILE"
	p["desired_role"] = role
	p["desired_state"] = state
	return "sale_company_membership_set", p, nil
}

func finishOrganizationHealthCorrection(ctx context.Context, tx pgx.Tx, message, state string) error {
	if state != "APPLIED" && state != "CONFLICT" {
		return nil
	}
	if state == "CONFLICT" {
		_, err := tx.Exec(ctx, `UPDATE organization_health_conflicts c SET state='OPEN',version=o.version,
    finance_value=jsonb_set(c.finance_value,'{intent}',o.payload)
    FROM organization_dispatch_outbox o WHERE o.id=$1 AND c.resolution_message_id=o.id AND c.state='RESOLVING'`, message)
		return err
	}
	// The DCAI correction receipt already acknowledges all native events through ai_base_revision.
	_, err := tx.Exec(ctx, `WITH completed AS (
  UPDATE organization_health_conflicts c SET state='RESOLVED',resolved_at=now()
  FROM organization_dispatch_outbox o WHERE o.id=$1 AND c.resolution_message_id=o.id AND c.state='RESOLVING'
  RETURNING c.source_id,c.kind,c.external_id,(c.ai_value->>'finance_base_version')::bigint AS base,
   o.entity_id,(o.payload->>'ai_base_revision')::bigint AS revision)
  INSERT INTO organization_health_inbox(source_id,kind,external_id,entity_id,wire_id,finance_base_version,acknowledge_only,acknowledged_at)
  SELECT c.source_id,c.kind,c.external_id,c.entity_id,CASE WHEN c.kind='COMPANY' THEN k.wire_id ELSE c.entity_id END,c.base,c.source_id<c.revision,now()
  FROM completed c LEFT JOIN organization_dispatch_company_keys k ON k.unit_id=c.entity_id ON CONFLICT DO NOTHING`, message)
	return err
}

func importHealthCategoryLinks(ctx context.Context, tx pgx.Tx, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var links []struct {
		ExternalID string `json:"externalId"`
		CategoryID string `json:"categoryId"`
	}
	if err := json.Unmarshal(raw, &links); err != nil {
		return err
	}
	for _, l := range links {
		id, ok := canonicalSourceUUID(l.CategoryID)
		if !ok || !textOK(l.ExternalID, 128) {
			return invalid
		}
		var saved string
		err := tx.QueryRow(ctx, `INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id)
   SELECT 'DCAI','CATEGORY',id,$1 FROM organization_sales_categories WHERE id=$2
   ON CONFLICT(target,entity_type,entity_id) DO UPDATE SET external_id=excluded.external_id
   WHERE organization_dispatch_links.external_id=excluded.external_id RETURNING external_id`, l.ExternalID, id).Scan(&saved)
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict
		}
		if err != nil {
			return err
		}
	}
	return nil
}
