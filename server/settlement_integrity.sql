ALTER TABLE catalog_aggregate_event_groups ADD COLUMN IF NOT EXISTS fixed_accrued_minor bigint CHECK (fixed_accrued_minor>=0);
ALTER TABLE calculation_drafts ADD COLUMN IF NOT EXISTS posting_digest bytea CHECK (octet_length(posting_digest)=32);

INSERT INTO draft_refresh_queue(period_id)
SELECT d.period_id FROM calculation_drafts d
WHERE d.posting_digest IS NULL AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=d.period_id)
ON CONFLICT(period_id) DO NOTHING;

-- Define the persisted financial result independently of the calculation inputs and of editable pool plans.
CREATE OR REPLACE FUNCTION settlement_posting_rows(scope_id bigint)
RETURNS TABLE(section text, row_key text, body jsonb)
LANGUAGE sql STABLE SET TimeZone='UTC' SET bytea_output='hex' AS $$
 SELECT 'draft',d.period_id::text,to_jsonb(d)-'posting_digest' FROM calculation_drafts d WHERE d.period_id=scope_id
 UNION ALL SELECT 'accrual',l.id::text,to_jsonb(l) FROM calculation_draft_lines l WHERE l.period_id=scope_id
 UNION ALL SELECT 'refund',r.id::text,to_jsonb(r) FROM calculation_draft_refunds r WHERE r.period_id=scope_id
 UNION ALL SELECT 'correction',c.id::text,to_jsonb(c) FROM calculation_draft_corrections c WHERE c.period_id=scope_id
 UNION ALL SELECT 'performance',a.source_version_id::text,to_jsonb(a) FROM calculation_draft_performance a WHERE a.period_id=scope_id
 UNION ALL SELECT 'issue',i.id::text,to_jsonb(i) FROM calculation_draft_issues i WHERE i.period_id=scope_id
 UNION ALL SELECT 'practiceChange',c.class_period_id::text||':'||c.original_version_id::text,to_jsonb(c)
  FROM calculation_draft_practice_changes c WHERE c.period_id=scope_id
 UNION ALL SELECT 'aggregateGroup',g.group_key,to_jsonb(g) FROM catalog_aggregate_draft_groups g WHERE g.period_id=scope_id
 UNION ALL SELECT 'aggregateEvent',e.source_version_id::text,to_jsonb(e) FROM catalog_aggregate_draft_events e WHERE e.period_id=scope_id
 UNION ALL SELECT 'aggregateEventGroup',g.source_version_id::text||':'||g.group_key,to_jsonb(g)
  FROM catalog_aggregate_event_groups g WHERE g.period_id=scope_id
$$;

CREATE OR REPLACE FUNCTION settlement_posting_digest(scope_id bigint) RETURNS bytea
LANGUAGE sql STABLE AS $$
 SELECT sha256(convert_to(coalesce(string_agg(
  encode(sha256(convert_to(jsonb_build_array(section,row_key,CASE WHEN section='draft' THEN body-'calculated_at' ELSE body END)::text,'UTF8')),'hex'),
  '' ORDER BY section COLLATE "C",row_key COLLATE "C"),''),'UTF8'))
 FROM settlement_posting_rows(scope_id) WHERE section<>'issue' OR body->>'advisory'='false'
$$;

CREATE OR REPLACE FUNCTION protect_settlement_refund_cutoff() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE mode text;
BEGIN
 SELECT period_mode INTO mode FROM products WHERE id=OLD.product_id FOR NO KEY UPDATE;
 IF EXISTS(SELECT 1 FROM settlements s JOIN periods p ON p.id=s.period_id
  WHERE p.id=OLD.id OR ((mode='calendar' OR OLD.calendar_month IS NOT NULL)
   AND p.product_id=OLD.product_id AND p.starts_on=OLD.ends_on+1
   AND p.aggregate_package_id IS NOT DISTINCT FROM OLD.aggregate_package_id
   AND (p.calendar_month IS NULL)=(OLD.calendar_month IS NULL))) THEN
  RAISE EXCEPTION 'confirmed refund cutoff cannot be changed' USING ERRCODE='55000';
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE TRIGGER protect_settlement_refund_cutoff BEFORE UPDATE OF refund_cutoff_at ON periods
 FOR EACH ROW WHEN (OLD.refund_cutoff_at IS DISTINCT FROM NEW.refund_cutoff_at)
 EXECUTE FUNCTION protect_settlement_refund_cutoff();

CREATE TABLE IF NOT EXISTS settlement_removal_rows (
 settlement_id bigint NOT NULL REFERENCES settlement_removals(settlement_id),
 section text NOT NULL,
 row_key text NOT NULL,
 body jsonb NOT NULL,
 PRIMARY KEY(settlement_id,section,row_key)
);

CREATE OR REPLACE FUNCTION snapshot_removed_settlement() RETURNS trigger
LANGUAGE plpgsql SET TimeZone='UTC' SET bytea_output='hex' AS $$
BEGIN
 -- The preceding protection trigger has checked dependencies and recorded the removal.
 INSERT INTO settlement_removal_rows(settlement_id,section,row_key,body)
 WITH postings AS MATERIALIZED (SELECT * FROM settlement_posting_rows(OLD.period_id)),
 allocations AS MATERIALIZED (SELECT * FROM monthly_pool_allocations a WHERE a.period_id=OLD.period_id AND a.draft_version=OLD.draft_version),
 shares AS MATERIALIZED (SELECT s.* FROM monthly_pool_allocation_shares s JOIN allocations a ON a.id=s.allocation_id),
 funding AS MATERIALIZED (SELECT f.* FROM monthly_pool_allocation_funding f JOIN shares s ON s.id=f.share_id),
 originals AS MATERIALIZED (
  SELECT l.* FROM calculation_draft_lines l WHERE l.period_id<>OLD.period_id AND l.id IN (
   SELECT (body->>'original_line_id')::bigint FROM postings WHERE section IN ('refund','correction')
   UNION SELECT source_line_id FROM funding)
 ), order_refs AS (
  SELECT (p.body->>key.name)::bigint FROM postings p CROSS JOIN (VALUES
    ('source_version_id'),('payment_version_id'),('completion_version_id'),('prior_refund_version_id'),
    ('refund_source_version_id'),('original_version_id'),('anchor_payment_id')) key(name)
  WHERE p.body ? key.name
  UNION SELECT source_version_id FROM originals
 ), rule_refs AS (
  SELECT (body->>'rule_id')::bigint FROM postings WHERE section IN ('draft','accrual')
  UNION SELECT rule_id FROM originals
 ), catalog_refs AS (
  SELECT (body->>'catalog_rule_id')::uuid FROM postings WHERE section='accrual'
  UNION SELECT (body->>'rule_id')::uuid FROM postings WHERE section='aggregateGroup'
  UNION SELECT catalog_rule_id FROM originals
 ), archive AS (
  SELECT section,row_key,body FROM postings
  UNION ALL SELECT 'settlement',OLD.id::text,to_jsonb(OLD)
  UNION ALL SELECT 'period',p.id::text,to_jsonb(p) FROM periods p WHERE p.id=OLD.period_id
  UNION ALL SELECT 'pool',a.id::text,to_jsonb(a) FROM allocations a
  UNION ALL SELECT 'poolShare',s.id::text,to_jsonb(s) FROM shares s
  UNION ALL SELECT 'poolFunding',f.share_id::text||':'||f.source_line_id::text,to_jsonb(f) FROM funding f
  UNION ALL SELECT 'originalAccrual',l.id::text,to_jsonb(l) FROM originals l
  UNION ALL SELECT 'orderVersion',v.id::text,to_jsonb(v) FROM source_order_versions v WHERE v.id IN (SELECT * FROM order_refs)
  UNION ALL SELECT 'ruleVersion',v.id::text,to_jsonb(v) FROM rule_versions v WHERE v.id IN (SELECT * FROM rule_refs)
  UNION ALL SELECT 'catalogRuleVersion',v.source_id::text,to_jsonb(v) FROM product_rule_versions v WHERE v.source_id IN (SELECT * FROM catalog_refs)
 ) SELECT OLD.id,section,row_key,body FROM archive;
 RETURN OLD;
END $$;

CREATE OR REPLACE TRIGGER snapshot_removed_settlement BEFORE DELETE ON settlements
 FOR EACH ROW EXECUTE FUNCTION snapshot_removed_settlement();

CREATE OR REPLACE FUNCTION protect_settlement_removal_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'settlement removal history cannot be changed' USING ERRCODE='55000';
END $$;

CREATE OR REPLACE TRIGGER protect_settlement_removals BEFORE UPDATE OR DELETE ON settlement_removals
 FOR EACH ROW EXECUTE FUNCTION protect_settlement_removal_history();
CREATE OR REPLACE TRIGGER protect_settlement_removal_rows BEFORE UPDATE OR DELETE ON settlement_removal_rows
 FOR EACH ROW EXECUTE FUNCTION protect_settlement_removal_history();
