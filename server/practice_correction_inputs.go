package main

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type practiceCorrectionRevision struct {
	ClassID, OriginalID, VersionID int64
	Fact                           sourceFact
	Key                            string
	EarlierPeriod, Booked          bool
	Identities                     map[string][]identityAssignment
	Company                        *practiceReplayCompany
}

// A change belongs to the period in which the financial attribution was
// first observed for the entire enrollment. If its legs arrive in different
// months, they are handled together when the last changed leg is available.
// A display revision with the same participants keeps its receipt time.
const practiceCorrectionCandidatesSQL = `WITH target AS (
 SELECT id,product_id,starts_on,ends_on FROM periods WHERE id=$1
), changed AS MATERIALIZED (
SELECT class.id AS class_id,class.source_period_key,d.practice_roster_id,original.order_id,original.id AS original_id,
 latest.id AS source_id,latest.fact,latest.attribution,receipt.received_at
FROM target JOIN periods class ON class.product_id=target.product_id AND class.calendar_month IS NULL
JOIN settlements closed ON closed.period_id=class.id
JOIN calculation_drafts d ON d.period_id=class.id AND d.practice_roster_id IS NOT NULL
JOIN calculation_draft_performance a ON a.period_id=class.id AND a.payment_version_id IS NULL
JOIN source_order_versions original ON original.id=a.source_version_id AND original.source='DCZD'
LEFT JOIN source_payment_companies base_company ON base_company.source_version_id=original.id
JOIN LATERAL (
 SELECT v.id,v.fact,v.updated_at,jsonb_build_array(v.fact->'referrerId',v.fact->'participants',
  coalesce(pc.company_id,''),coalesce(pc.recommendation_company_id,''),coalesce(pc.recommendation_conflict,false)) AS attribution
 FROM source_order_versions v LEFT JOIN source_payment_companies pc ON pc.source_version_id=v.id
 WHERE ` + correctionOrderIdentity + `
 AND (v.updated_at,v.id)>(original.updated_at,original.id)
 ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
) latest ON true
LEFT JOIN LATERAL (
 SELECT v.fact,c.company_binding FROM calculation_draft_practice_changes c JOIN settlements s ON s.period_id=c.period_id
 JOIN source_order_versions v ON v.id=c.source_version_id
 WHERE c.class_period_id=class.id AND c.original_version_id=original.id
 ORDER BY s.id DESC LIMIT 1
) accepted ON true
LEFT JOIN LATERAL (
 SELECT v.id,v.updated_at FROM source_order_versions v LEFT JOIN source_payment_companies pc ON pc.source_version_id=v.id
 WHERE ` + correctionOrderIdentity + `
 AND (v.updated_at,v.id)<(latest.updated_at,latest.id)
 AND jsonb_build_array(v.fact->'referrerId',v.fact->'participants',coalesce(pc.company_id,''),
  coalesce(pc.recommendation_company_id,''),coalesce(pc.recommendation_conflict,false)) IS DISTINCT FROM latest.attribution
 ORDER BY v.updated_at DESC,v.id DESC LIMIT 1
) previous ON true
JOIN LATERAL (
 SELECT min(v.received_at) AS received_at FROM source_order_versions v WHERE ` + correctionOrderIdentity + `
 AND (previous.id IS NULL OR (v.updated_at,v.id)>(previous.updated_at,previous.id))
) receipt ON true
WHERE latest.attribution IS DISTINCT FROM jsonb_build_array(
 coalesce(accepted.fact,original.fact)->'referrerId',coalesce(accepted.fact,original.fact)->'participants',
 CASE WHEN accepted.company_binding IS NULL THEN coalesce(base_company.company_id,'') ELSE coalesce(accepted.company_binding->>'sales','') END,
 CASE WHEN accepted.company_binding IS NULL THEN coalesce(base_company.recommendation_company_id,'') ELSE coalesce(accepted.company_binding->>'referrer','') END,
 CASE WHEN accepted.company_binding IS NULL THEN coalesce(base_company.recommendation_conflict,false) ELSE coalesce((accepted.company_binding->>'conflict')::boolean,false) END)
), revisions AS (
 SELECT c.*,max(c.received_at) OVER (PARTITION BY c.class_id,member.enrollment_id) AS ready_at
 FROM changed c JOIN LATERAL (
  SELECT e.id AS enrollment_id FROM practice_enrollments e JOIN practice_roster_members roster ON roster.enrollment_id=e.id
  WHERE e.period_key=c.source_period_key AND roster.roster_id=c.practice_roster_id AND e.paid_order_ids@>ARRAY[c.order_id]
  ORDER BY e.id LIMIT 1
 ) member ON true
)
SELECT r.class_id,r.original_id,r.source_id,r.fact,r.attribution::text,
 coalesce(company.company_id,''),coalesce(company.recommendation_company_id,''),coalesce(company.recommendation_conflict,false),
 coalesce(coalesce(reserved.starts_on,observed.starts_on)<target.starts_on,false),false,NULL::jsonb,NULL::jsonb
FROM revisions r CROSS JOIN target
LEFT JOIN LATERAL (
 SELECT p.id,p.starts_on FROM calculation_draft_practice_changes c JOIN periods p ON p.id=c.period_id
 JOIN source_order_versions used ON used.id=c.source_version_id
 LEFT JOIN source_payment_companies pc ON pc.source_version_id=used.id
 WHERE c.class_period_id=r.class_id AND c.original_version_id=r.original_id
 AND r.attribution=jsonb_build_array(used.fact->'referrerId',used.fact->'participants',
  CASE WHEN c.company_binding IS NULL THEN coalesce(pc.company_id,'') ELSE coalesce(c.company_binding->>'sales','') END,
  CASE WHEN c.company_binding IS NULL THEN coalesce(pc.recommendation_company_id,'') ELSE coalesce(c.company_binding->>'referrer','') END,
  CASE WHEN c.company_binding IS NULL THEN coalesce(pc.recommendation_conflict,false) ELSE coalesce((c.company_binding->>'conflict')::boolean,false) END)
 AND r.ready_at<(p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai'
 AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=c.period_id)
 ORDER BY p.starts_on,p.id LIMIT 1
) reserved ON true
LEFT JOIN periods observed ON observed.product_id=target.product_id AND observed.calendar_month IS NOT NULL
 AND (r.ready_at AT TIME ZONE 'Asia/Shanghai')::date BETWEEN observed.starts_on AND observed.ends_on
 AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=observed.id)
LEFT JOIN source_payment_companies company ON company.source_version_id=r.source_id
WHERE coalesce(reserved.starts_on,observed.starts_on)<=target.starts_on OR coalesce(reserved.id,observed.id) IS NULL
 AND $2<=CURRENT_TIMESTAMP AND CURRENT_TIMESTAMP<$3 AND r.ready_at<$3
ORDER BY r.class_id,r.original_id LIMIT $4`

const practiceCorrectionBookedSQL = `SELECT DISTINCT ON(c.class_period_id,c.original_version_id)
 c.class_period_id,c.original_version_id,v.id,v.fact,'',
 coalesce(company.company_id,''),coalesce(company.recommendation_company_id,''),coalesce(company.recommendation_conflict,false),
 false,true,c.identity_bindings,c.company_binding
FROM calculation_draft_practice_changes c JOIN settlements s ON s.period_id=c.period_id
JOIN source_order_versions v ON v.id=c.source_version_id
LEFT JOIN source_payment_companies company ON company.source_version_id=v.id
WHERE c.class_period_id=ANY($1::bigint[])
ORDER BY c.class_period_id,c.original_version_id,s.id DESC`

func readPracticeCorrectionRevisions(rows pgx.Rows) ([]practiceCorrectionRevision, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (practiceCorrectionRevision, error) {
		var c practiceCorrectionRevision
		err := row.Scan(&c.ClassID, &c.OriginalID, &c.VersionID, &c.Fact, &c.Key,
			&c.Fact.SalesCompanyID, &c.Fact.ReferrerCompanyID, &c.Fact.ReferrerConflict, &c.EarlierPeriod, &c.Booked, &c.Identities, &c.Company)
		return c, err
	})
}

func storePracticeCorrectionRevisions(ctx context.Context, tx pgx.Tx, periodID int64, changes []practiceCorrectionRevision) error {
	if _, err := tx.Exec(ctx, "DELETE FROM calculation_draft_practice_changes WHERE period_id=$1", periodID); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"calculation_draft_practice_changes"},
		[]string{"period_id", "class_period_id", "original_version_id", "source_version_id", "identity_bindings", "company_binding"},
		pgx.CopyFromSlice(len(changes), func(i int) ([]any, error) {
			change := changes[i]
			return []any{periodID, change.ClassID, change.OriginalID, change.VersionID, change.Identities, change.Company}, nil
		}))
	return err
}
