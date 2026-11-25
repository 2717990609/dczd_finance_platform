package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"log"
	"time"
)

func (s workspace) startOrganizationDispatch(parent context.Context) (func(), error) {
	clients := map[string]*organizationSyncClient{}
	for _, target := range organizationDispatchSystems {
		c, err := newOrganizationSyncClient(target)
		if errors.Is(err, errSourceDisabled) {
			continue
		}
		if err != nil {
			return nil, err
		}
		clients[target] = c
	}
	if len(clients) == 0 {
		return func() {}, nil
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(3 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				for _, target := range organizationDispatchSystems {
					if c := clients[target]; c != nil {
						for range 8 {
							ok, err := s.deliverOrganization(ctx, target, c)
							if err != nil {
								log.Printf("组织同步 %s 发件箱暂不可用", target)
								break
							}
							if !ok {
								break
							}
						}
					}
				}
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}
func (s workspace) deliverOrganization(ctx context.Context, target string, c *organizationSyncClient) (bool, error) {
	msg := dispatchEnvelope{SchemaVersion: 1, SourceSystem: "PLATFORM", TargetSystem: target}
	var local, kind string
	var attempt int
	err := s.db.QueryRow(ctx, `WITH due AS (
 SELECT q.id FROM organization_dispatch_outbox q
 WHERE q.target=$1 AND q.next_attempt_at<=clock_timestamp() AND q.effective_at<=clock_timestamp()
 AND (q.state IN ('PENDING','RETRYABLE','WAIT_CATEGORY','WAIT_PARENT_COMPANY') OR q.state='PROCESSING' AND q.lease_until<=clock_timestamp())
 AND (q.entity_type!='COMPANY' OR EXISTS(SELECT 1 FROM organization_dispatch_targets t WHERE t.company_id=q.entity_id AND t.target=q.target))
 AND NOT EXISTS(SELECT 1 FROM organization_dispatch_outbox earlier WHERE earlier.target=q.target AND earlier.entity_type=q.entity_type
  AND earlier.entity_id=q.entity_id AND earlier.version<q.version AND earlier.state NOT IN ('APPLIED','CONFLICT','STOPPED'))
 ORDER BY (q.entity_type='CATEGORY') DESC,(q.entity_type='COMPANY') DESC,q.version FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE organization_dispatch_outbox q SET state='PROCESSING',attempts=attempts+1,lease_until=clock_timestamp()+interval '30 seconds'
 FROM due WHERE q.id=due.id RETURNING q.id::text,q.entity_type,q.entity_id::text,q.message_type,q.version,q.occurred_at,q.effective_at,q.payload,q.attempts`, target).
		Scan(&msg.ID, &kind, &local, &msg.MessageType, &msg.Version, &msg.OccurredAt, &msg.EffectiveAt, &msg.Data, &attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	msg.EntityID = local
	msg.CorrelationID = msg.ID
	msg.OccurredAt = msg.OccurredAt.UTC()
	msg.EffectiveAt = msg.EffectiveAt.UTC()
	if attempt > 1 && kind != "MEMBER" {
		var superseded bool
		if err := s.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM organization_dispatch_outbox WHERE target=$1 AND entity_type=$2 AND entity_id=$3 AND version>$4)", target, kind, local, msg.Version).Scan(&superseded); err != nil {
			return true, err
		}
		if superseded {
			return true, s.finishDispatch(ctx, msg, local, kind, attempt, "STOPPED", "SUPERSEDED", "")
		}
	}
	if kind == "COMPANY" {
		err = s.db.QueryRow(ctx, "SELECT wire_id::text FROM organization_dispatch_company_keys WHERE unit_id=$1", local).Scan(&msg.EntityID)
		if err != nil {
			return true, err
		}
		var categoryReady, parentReady bool
		err = s.db.QueryRow(ctx, `WITH d AS (SELECT $2::jsonb AS payload)
  SELECT NOT EXISTS(SELECT 1 FROM d JOIN organization_dispatch_outbox q ON q.entity_id=(d.payload->>'category_id')::uuid AND q.target=$1 AND q.entity_type='CATEGORY'
    WHERE q.version=(SELECT max(n.version) FROM organization_dispatch_outbox n WHERE n.target=q.target AND n.entity_type=q.entity_type AND n.entity_id=q.entity_id) AND q.state!='APPLIED'),
  (payload->>'parent_company_id') IS NULL OR EXISTS(SELECT 1 FROM organization_dispatch_company_keys k
   JOIN organization_dispatch_links l ON l.entity_id=k.unit_id AND l.target=$1 AND l.entity_type='COMPANY'
   WHERE k.wire_id=(d.payload->>'parent_company_id')::uuid) FROM d`, target, msg.Data).Scan(&categoryReady, &parentReady)
		if err != nil {
			return true, err
		}
		if !categoryReady {
			return true, s.finishDispatch(ctx, msg, local, kind, attempt, "WAIT_CATEGORY", "CATEGORY_NOT_SYNCED", "")
		}
		if !parentReady {
			return true, s.finishDispatch(ctx, msg, local, kind, attempt, "WAIT_PARENT_COMPANY", "PARENT_COMPANY_NOT_SYNCED", "")
		}
	}
	if kind == "MEMBER" {
		var d dispatchMemberMessage
		if err := json.Unmarshal(msg.Data, &d); err != nil {
			return true, err
		}
		if d.Operation == "TRANSFER" || d.Operation == "CREATE" {
			wire, expected := d.CompanyID, d.ExternalCompanyID
			if d.Operation == "TRANSFER" {
				wire, expected = d.DestinationID, d.DestinationExternalID
			}
			var valid bool
			if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_dispatch_company_keys k JOIN organization_units u ON u.id=k.unit_id AND u.status='ACTIVE'
    JOIN organization_dispatch_targets t ON t.company_id=k.unit_id AND t.target=$1 JOIN organization_dispatch_links l ON l.entity_id=k.unit_id AND l.entity_type='COMPANY' AND l.target=t.target AND l.external_id=$3
    WHERE k.wire_id=$2)`, target, wire, expected).Scan(&valid); err != nil {
				return true, err
			}
			if !valid {
				return true, s.finishDispatch(ctx, msg, local, kind, attempt, "CONFLICT", "COMPANY_NOT_SYNCED", "")
			}
		}
	}
	var receipt dispatchReceipt
	state, reason, external := "RETRYABLE", "TRANSPORT_ERROR", ""
	if err := c.Post(ctx, organizationMessagePath, msg, &receipt); err == nil {
		state, reason, external = dispatchDeliveryResult(msg, receipt)
	}
	return true, s.finishDispatch(ctx, msg, local, kind, attempt, state, reason, external)
}
func (s workspace) finishDispatch(ctx context.Context, msg dispatchEnvelope, local, kind string, attempt int, state, reason, external string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockDispatchEntity(ctx, tx, msg.TargetSystem, kind, local); err != nil {
			return err
		}
		var active string
		err := tx.QueryRow(ctx, `SELECT id::text FROM organization_dispatch_outbox WHERE id=$1 AND attempts=$2 AND state='PROCESSING' FOR UPDATE`, msg.ID, attempt).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if state != "APPLIED" && kind != "MEMBER" {
			var newer bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM organization_dispatch_outbox WHERE target=$1 AND entity_type=$2 AND entity_id=$3 AND version>$4)", msg.TargetSystem, kind, local, msg.Version).Scan(&newer); err != nil {
				return err
			}
			if newer {
				state, reason = "STOPPED", "SUPERSEDED"
			}
		}
		if state == "APPLIED" {
			nested, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			err = applyDispatchIdentity(ctx, nested, msg, local, kind, external)
			if err != nil {
				_ = nested.Rollback(ctx)
				if !errors.Is(err, errOrganizationDispatch) {
					return err
				}
				state, reason = "CONFLICT", "RECEIPT_MAPPING_CONFLICT"
			} else if err := nested.Commit(ctx); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_dispatch_outbox SET state=$2,reason=$3,external_id=$4,lease_until=NULL,next_attempt_at=clock_timestamp()+$5::integer*interval '1 second' WHERE id=$1`, msg.ID, state, reason, external, min(attempt, 20)*15); err != nil {
			return err
		}
		if msg.TargetSystem == "DCAI" && (state == "APPLIED" || state == "CONFLICT") {
			return finishOrganizationHealthCorrection(ctx, tx, msg.ID, state)
		}
		return nil
	})
}
func applyDispatchIdentity(ctx context.Context, tx pgx.Tx, msg dispatchEnvelope, local, kind, external string) error {
	if kind == "MEMBER" {
		var d dispatchMemberMessage
		if err := json.Unmarshal(msg.Data, &d); err != nil {
			return err
		}
		company, role, state := d.ExternalCompanyID, "", "ACTIVE"
		switch d.Operation {
		case "DISABLE":
			state = "INACTIVE"
		case "SET_OWNER":
			role = "OWNER"
		case "UNSET_OWNER", "CREATE":
			role = "MEMBER"
		case "TRANSFER":
			role, company = "MEMBER", d.DestinationExternalID
		case "RECONCILE":
			role, state = d.DesiredRole, d.DesiredState
		}
		tag, err := tx.Exec(ctx, `UPDATE organization_dispatch_links SET version=$4,external_member_id=$5,external_company_id=$6,role=coalesce(nullif($7,''),role),identity_state=$8,source_name=coalesce(nullif($9,''),source_name)
  WHERE entity_type='MEMBER' AND target=$1 AND entity_id=$2 AND external_id=$3 AND version<=$4`, msg.TargetSystem, local, d.UserID, msg.Version, external, company, role, state, d.Name)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errOrganizationDispatch
		}
		if d.Operation == "CREATE" && d.DesiredRole == "OWNER" {
			_, err := enqueueDispatch(ctx, tx, msg.TargetSystem, "MEMBER", local, "sale_company_membership_set", map[string]any{"operation": "SET_OWNER", "external_user_id": d.UserID, "external_member_id": external, "expected_company_external_id": company}, nil, nil)
			return err
		}
		return nil
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "org-sync-external:"+msg.TargetSystem+":"+kind+":"+external); err != nil {
		return err
	}
	var clashes bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_dispatch_links WHERE target=$1 AND entity_type=$2
  AND ((entity_id=$3 AND external_id!=$4) OR (entity_id!=$3 AND external_id=$4)))`, msg.TargetSystem, kind, local, external).Scan(&clashes); err != nil {
		return err
	}
	if clashes {
		return errOrganizationDispatch
	}
	if kind == "COMPANY" {
		var active, exists bool
		err := tx.QueryRow(ctx, `SELECT status='ACTIVE',EXISTS(SELECT 1 FROM organization_company_sources WHERE source=$2 AND company_id=$1 AND source_company_id=$3 AND tstzrange(valid_from,valid_to,'[)') @> greatest($4,clock_timestamp())),
   EXISTS(SELECT 1 FROM organization_company_sources WHERE source=$2 AND ((company_id=$1 AND source_company_id!=$3) OR (company_id!=$1 AND source_company_id=$3)) AND (valid_to IS NULL OR valid_to>$4))
   FROM organization_units WHERE id=$1`, local, msg.TargetSystem, external, msg.EffectiveAt).Scan(&active, &exists, &clashes)
		if err != nil {
			return err
		}
		if clashes {
			return errOrganizationDispatch
		}
		if active && !exists {
			if _, err := tx.Exec(ctx, `INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from) VALUES($1,$2,$3,$4)`, msg.TargetSystem, external, local, msg.EffectiveAt); err != nil {
				return err
			}
			if err := enqueueDraftConfiguration(ctx, tx, draftRefreshCompanyClassification, []string{local}, msg.EffectiveAt, true); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id,version) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(target,entity_type,entity_id) DO UPDATE SET version=greatest(organization_dispatch_links.version,excluded.version) WHERE organization_dispatch_links.external_id=excluded.external_id`, msg.TargetSystem, kind, local, external, msg.Version)
	return err
}
