package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/2717990609/dczd_finance_platform/internal/director"
	"github.com/jackc/pgx/v5"
)

const directorFamilyProductSQL = `pro.period_mode='class' AND EXISTS(SELECT 1 FROM source_mappings m
 WHERE m.product_id=pro.id AND m.source='DCZD' AND m.domain='PRACTICE' AND (m.source_key='4' OR (m.source_key='PRACTICE:2:4' AND NOT EXISTS(SELECT 1 FROM source_mappings canonical
 WHERE canonical.source='DCZD' AND canonical.domain='PRACTICE' AND canonical.source_key='4'))))`

const directorPeriodsSQL = `SELECT p.id,p.product_id,p.name,p.source_period_key AS period_key,r.starts_at
 FROM periods p JOIN products pro ON pro.id=p.product_id JOIN LATERAL (
 SELECT starts_at FROM practice_rosters WHERE period_key=p.source_period_key ORDER BY source_version DESC LIMIT 1
 ) r ON true WHERE p.calendar_month IS NULL AND split_part(p.source_period_key,':',3)='4' AND ` + directorFamilyProductSQL

const directorPeriodsListSQL = `SELECT coalesce(jsonb_agg(jsonb_build_object('id',id,'productId',product_id,'name',name,'startsAt',starts_at) ORDER BY starts_at DESC,id DESC),'[]') FROM (` + directorPeriodsSQL + `) periods`

type directorPeriod struct {
	ID        int64     `json:"id"`
	ProductID int64     `json:"productId"`
	Name      string    `json:"name"`
	Key       string    `json:"key"`
	Start     time.Time `json:"startsAt"`
}

type directorDocument struct {
	SourceVersion int            `json:"source_version,omitempty"`
	Version       int            `json:"version_no"`
	PeriodKey     string         `json:"period_key"`
	PeriodName    string         `json:"period_name"`
	StartsAt      time.Time      `json:"period_start"`
	Rows          []director.Row `json:"rows"`
}

func directorPeriodFor(ctx context.Context, tx pgx.Tx, periodID int64) (p directorPeriod, err error) {
	err = tx.QueryRow(ctx, `SELECT * FROM (`+directorPeriodsSQL+`) p WHERE id=$1`, periodID).Scan(&p.ID, &p.ProductID, &p.Name, &p.Key, &p.Start)
	if errors.Is(err, pgx.ErrNoRows) {
		err = fmt.Errorf("%w：请选择已同步开班时间的家庭教育期次", invalid)
	}
	return
}

// Mutating the locked product on publication catches an older SERIALIZABLE reader
// even when it started its snapshot before it acquired the shared row lock.
func lockDirectorProduct(ctx context.Context, tx pgx.Tx, productID int64) error {
	var id int64
	return tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 FOR SHARE`, productID).Scan(&id)
}

func readDirectorVersionDocument(ctx context.Context, tx pgx.Tx, productID, versionID int64) (doc practiceRegistrationDocument, err error) {
	err = tx.QueryRow(ctx, `SELECT document FROM practice_director_versions WHERE product_id=$1 AND id=$2`, productID, versionID).Scan(&doc)
	return
}

func readApplicableDirectorVersion(ctx context.Context, tx pgx.Tx, productID, periodID int64) (id int64, doc practiceRegistrationDocument, err error) {
	// Read-only review transactions use their stable snapshot. Write transactions
	// additionally serialize draft changes and confirmation with publication.
	var readOnly bool
	if err = tx.QueryRow(ctx, "SELECT current_setting('transaction_read_only')::boolean").Scan(&readOnly); err != nil {
		return
	}
	if !readOnly {
		err = lockDirectorProduct(ctx, tx, productID)
	}
	if err != nil {
		return
	}
	var drift bool
	err = tx.QueryRow(ctx, `WITH family AS (`+directorPeriodsSQL+`) SELECT v.id,v.document,origin.starts_at IS DISTINCT FROM v.starts_at
 FROM family target JOIN (SELECT DISTINCT ON (period_id) * FROM practice_director_versions WHERE product_id=$1 ORDER BY period_id,version_no DESC) v ON v.product_id=target.product_id
 LEFT JOIN family origin ON origin.id=v.period_id
 WHERE target.id=$2 AND target.product_id=$1 AND (v.starts_at<=target.starts_at OR v.period_id=target.id)
 ORDER BY v.starts_at DESC,v.version_no DESC LIMIT 1`, productID, periodID).Scan(&id, &doc, &drift)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, doc, nil
	}
	if err == nil && drift {
		return 0, doc, fmt.Errorf("%w：关系版本起始期次的开班时间已变化，请重新核对并保存", conflict)
	}
	return
}

const directorVersionSummarySQL = `SELECT v.id,v.product_id AS "productId",v.period_id AS "periodId",v.document->>'period_name' AS "periodName",
 v.starts_at AS "startsAt",v.version_no AS version,v.file_name AS "fileName",v.file_hash AS "fileHash",v.created_at AS "createdAt",
 coalesce(p.name,'本地财务') AS "createdBy", jsonb_array_length(v.document->'rows') AS "rowCount"
 FROM practice_director_versions v LEFT JOIN finance_accounts a ON a.id=v.created_by LEFT JOIN people p ON p.id=a.person_id`

const directorVersionsListSQL = `SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY version DESC),'[]') FROM (` + directorVersionSummarySQL + `) v`

func readDirectorBase(ctx context.Context, tx pgx.Tx, product int64) (id int64, doc directorDocument, err error) {
	err = tx.QueryRow(ctx, `SELECT id,document FROM practice_director_versions WHERE product_id=$1 ORDER BY version_no DESC LIMIT 1`, product).Scan(&id, &doc)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return
}

func directorRawHash(raw []byte) string     { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func directorMarshal(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
