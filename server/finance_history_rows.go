package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/jackc/pgx/v5"
)

// References in an exported reconciliation belong to this prepared database.
// Include the actual stored postings and allocation funding, independently of the
// draft header digest. Keep an explicit order for a byte-identical export.
const financeHistoryRowsSQL = `WITH lines AS MATERIALIZED (SELECT * FROM calculation_draft_lines WHERE period_id=$1),
 performance AS MATERIALIZED (SELECT * FROM calculation_draft_performance WHERE period_id=$1),
 refunds AS MATERIALIZED (SELECT * FROM calculation_draft_refunds WHERE period_id=$1),
 corrections AS MATERIALIZED (SELECT * FROM calculation_draft_corrections WHERE period_id=$1),
 draft AS MATERIALIZED (SELECT * FROM calculation_drafts WHERE period_id=$1),
 allocations AS MATERIALIZED (SELECT a.* FROM monthly_pool_allocations a JOIN draft d USING(period_id)
  WHERE a.draft_version=d.version AND $2::jsonb IS NULL),
 shares AS MATERIALIZED (SELECT s.* FROM monthly_pool_allocation_shares s JOIN allocations a ON a.id=s.allocation_id),
 funding AS MATERIALIZED (SELECT f.* FROM monthly_pool_allocation_funding f JOIN shares s ON s.id=f.share_id),
 archived_pools AS MATERIALIZED (SELECT value,ordinality FROM jsonb_array_elements($2::jsonb) WITH ORDINALITY),
 archived_funding AS MATERIALIZED (
  SELECT (f->>'sourceLineId')::bigint AS source_line_id,nullif((f->>'personId')::bigint,0) AS person_id
  FROM archived_pools a CROSS JOIN LATERAL jsonb_array_elements(a.value->'funding') f
 ),
 original_lines AS MATERIALIZED (
  SELECT l.* FROM calculation_draft_lines l WHERE l.period_id<>$1 AND l.id IN (
   SELECT original_line_id FROM refunds UNION SELECT original_line_id FROM corrections
   UNION SELECT source_line_id FROM funding UNION SELECT source_line_id FROM archived_funding)
 ),
 rosters AS MATERIALIZED (SELECT r.* FROM practice_rosters current JOIN draft d ON current.id=d.practice_roster_id
  JOIN practice_rosters r ON r.period_key=current.period_key AND (r.id=current.id OR r.source_version=1)),
 order_refs(id) AS (
  SELECT source_version_id FROM lines UNION SELECT source_version_id FROM performance UNION SELECT payment_version_id FROM performance
  UNION SELECT completion_version_id FROM performance UNION SELECT prior_refund_version_id FROM performance
  UNION SELECT refund_source_version_id FROM refunds UNION SELECT source_version_id FROM corrections UNION SELECT source_version_id FROM original_lines
 ), person_refs(id) AS (
  SELECT person_id FROM lines UNION SELECT allocation_owner_id FROM lines UNION SELECT person_id FROM performance
  UNION SELECT person_id FROM corrections UNION SELECT person_id FROM shares UNION SELECT person_id FROM original_lines
  UNION SELECT allocation_owner_id FROM original_lines UNION SELECT person_id FROM archived_funding
 ), rows(section,row_key,value) AS (
  SELECT 'period',id::text,to_jsonb(p) FROM periods p WHERE p.id=$1
  UNION ALL SELECT 'draft',period_id::text,to_jsonb(d)-'posting_digest' FROM draft d
  UNION ALL SELECT CASE WHEN practice_tier_reference THEN 'tierReference' ELSE 'accrual' END,id::text,
   CASE WHEN practice_tier_reference THEN to_jsonb(l) ELSE to_jsonb(l)-'practice_tier_reference' END FROM lines l
  UNION ALL SELECT 'originalAccrual',id::text,
   CASE WHEN practice_tier_reference THEN to_jsonb(l) ELSE to_jsonb(l)-'practice_tier_reference' END FROM original_lines l
  UNION ALL SELECT 'performance',source_version_id::text,to_jsonb(a) FROM performance a
  UNION ALL SELECT 'refund',id::text,to_jsonb(r) FROM refunds r
  UNION ALL SELECT 'correction',id::text,to_jsonb(c) FROM corrections c
  UNION ALL SELECT 'pool',id::text,to_jsonb(a) FROM allocations a
  UNION ALL SELECT 'poolShare',id::text,to_jsonb(s) FROM shares s
  UNION ALL SELECT 'poolFunding',f.share_id::text||':'||f.source_line_id,to_jsonb(f) FROM funding f
  UNION ALL SELECT 'archivedPoolFunding',ordinality::text,value FROM archived_pools
  UNION ALL SELECT 'personRef',p.id::text,jsonb_build_object('id',p.id,'party_id',p.legacy_party_id)
   FROM person_refs x JOIN people p USING(id)
  UNION ALL SELECT 'orderVersion',v.id::text,to_jsonb(v) FROM order_refs x JOIN source_order_versions v USING(id)
  UNION ALL SELECT 'orderClassification',c.source_version_id::text,to_jsonb(c)
   FROM order_refs x JOIN source_order_classifications c ON c.source_version_id=x.id
  UNION ALL SELECT 'orderCompany',c.source_version_id::text,to_jsonb(c)
   FROM order_refs x JOIN source_payment_companies c ON c.source_version_id=x.id
  UNION ALL SELECT 'ruleVersion',r.id::text,to_jsonb(r) FROM rule_versions r
   WHERE r.id IN (SELECT rule_id FROM draft UNION SELECT rule_id FROM lines UNION SELECT rule_id FROM original_lines)
  UNION ALL SELECT 'productRuleVersion',v.source_id::text,to_jsonb(v)-'observed_at' FROM product_rule_versions v
   WHERE v.source_id IN (SELECT catalog_rule_id FROM lines UNION SELECT catalog_rule_id FROM original_lines)
  UNION ALL SELECT 'roster',r.id::text,to_jsonb(r) FROM rosters r
  UNION ALL SELECT 'rosterMember',m.roster_id::text||':'||m.enrollment_id,to_jsonb(m)
   FROM rosters r JOIN practice_roster_members m ON r.id=m.roster_id
  UNION ALL SELECT 'enrollment',e.id::text,to_jsonb(e) FROM practice_enrollments e
   WHERE e.id IN (SELECT enrollment_id FROM rosters r JOIN practice_roster_members m ON r.id=m.roster_id)
 ) SELECT jsonb_build_array(section,value)::text FROM rows ORDER BY section COLLATE "C",row_key COLLATE "C"`

func financeHistoryPostings(ctx context.Context, tx pgx.Tx, periodID int64, poolFunding json.RawMessage, details io.Writer) (string, error) {
	hash := sha256.New()
	var output io.Writer = hash
	if details != nil {
		output = io.MultiWriter(hash, details)
	}
	rows, err := tx.Query(ctx, financeHistoryRowsSQL, periodID, poolFunding)
	if err != nil {
		return "", err
	}
	var row string
	_, err = pgx.ForEachRow(rows, []any{&row}, func() error {
		_, err := io.WriteString(output, row+"\n")
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
