package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errLegacyCatalogInvalid  = errors.New("产品与规则配置文件格式无效")
	errLegacyCatalogStale    = errors.New("配置文件的生成时间早于已导入配置")
	errLegacyCatalogConflict = errors.New("配置文件与已登记主键或规则版本冲突")
	errLegacyRuleNotAdapted  = errors.New("该产品的规则待启用")
)

type legacyCatalogFile struct {
	Environment string                 `json:"environment"`
	CapturedAt  time.Time              `json:"capturedAt"`
	Products    []legacyCatalogProduct `json:"products"`
}

type legacyCatalogProduct struct {
	SourceID       string                 `json:"sourceId"`
	Code           string                 `json:"code"`
	Name           string                 `json:"name"`
	Project        string                 `json:"project"`
	Source         string                 `json:"source"`
	SourceKey      string                 `json:"sourceKey"`
	PeriodMode     string                 `json:"periodMode"`
	CycleStartDay  *int                   `json:"cycleStartDay,omitempty"`
	Packages       []legacyCatalogPackage `json:"packages"`
	BusinessRoutes []catalogBusinessRoute `json:"businessRoutes,omitempty"`
}

type legacyCatalogPackage struct {
	SourceID   string                 `json:"sourceId"`
	Code       string                 `json:"code"`
	Name       string                 `json:"name"`
	Project    string                 `json:"project"`
	Kind       string                 `json:"kind"`
	ScopeType  string                 `json:"scopeType"`
	ScopeID    string                 `json:"scopeId"`
	Paused     bool                   `json:"paused"`
	ResumeFrom *time.Time             `json:"resumeFrom,omitempty"`
	Versions   []legacyCatalogVersion `json:"versions"`
}

type legacyCatalogVersion struct {
	SourceID   string          `json:"sourceId"`
	Number     int             `json:"version"`
	Status     string          `json:"status"`
	From       time.Time       `json:"from"`
	Through    *time.Time      `json:"through,omitempty"`
	Hash       string          `json:"hash"`
	Definition json.RawMessage `json:"definition"`
}

const catalogRulesSQL = `SELECT coalesce(jsonb_agg(to_jsonb(result) ORDER BY result."effectiveFrom" DESC,result.id),'[]') FROM (
 SELECT v.source_id AS id,true AS "catalogPending",p.product_id AS "productId",pr.name AS "productName",p.code AS "packageCode",
 p.name AS label,p.project_code AS project,
 CASE WHEN p.scope_type IN ('BUSINESS_SCENARIO','CROSS_SCENARIO_AGGREGATE') THEN p.scope_type END AS "scopeType",
 p.scope_id AS "scopeId",
 v.version_no AS version,v.source_status AS "sourceStatus",v.effective_from AS "effectiveFrom",
 ` + catalogDisplayedThrough + ` AS "effectiveTo",
 p.paused,p.resume_from AS "resumeFrom",p.project_code=c.project_code AS "projectMatches",
 (v.observed_at=c.captured_at OR v.published_at IS NOT NULL) AS "inLatestSnapshot",
 v.published_at AS "publishedAt",
 coalesce(c.source_system='DCAI' AND p.scope_type='BUSINESS_SCENARIO'
  AND v.definition->'internal_sales_direct_split'->>'mode'='DCAI_INTERNAL_DIRECT',false) AS "draftSupported"
 FROM legacy_rule_versions v JOIN legacy_rule_packages p ON p.source_id=v.package_id
 JOIN legacy_catalog_products c ON c.product_id=p.product_id JOIN products pr ON pr.id=c.product_id
) result`

func normalizeLegacyCatalog(input *legacyCatalogFile) error {
	if !textOK(input.Environment, 80) || input.CapturedAt.IsZero() || len(input.Products) == 0 {
		return errLegacyCatalogInvalid
	}
	input.CapturedAt = input.CapturedAt.UTC().Truncate(time.Microsecond)
	ids, codes := make(map[string]bool), make(map[string]bool)
	for i := range input.Products {
		product := &input.Products[i]
		id, valid := canonicalSourceUUID(product.SourceID)
		dayValid := product.PeriodMode == "calendar" && product.CycleStartDay != nil && *product.CycleStartDay >= 1 && *product.CycleStartDay <= 28 ||
			product.PeriodMode == "class" && product.CycleStartDay == nil
		if !valid || ids[id] || codes[product.Code] || !dayValid ||
			!textOK(product.Code, 48) || product.Code != strings.TrimSpace(product.Code) || !textOK(product.Name, 80) ||
			!textOK(product.Project, 128) || !validSource(product.Source) || !textOK(product.SourceKey, 200) {
			return errLegacyCatalogInvalid
		}
		ids[id], codes[product.Code], product.SourceID = true, true, id
		if err := normalizeCatalogRoutes(product, ids); err != nil {
			return err
		}
		sort.Slice(product.BusinessRoutes, func(a, b int) bool { return product.BusinessRoutes[a].SourceID < product.BusinessRoutes[b].SourceID })
		for j := range product.Packages {
			pack := &product.Packages[j]
			id, valid := canonicalSourceUUID(pack.SourceID)
			if !valid || ids[id] || !textOK(pack.Code, 200) || !textOK(pack.Name, 200) || !textOK(pack.Project, 128) ||
				!textOK(pack.Kind, 80) || !textOK(pack.ScopeType, 80) || len(pack.ScopeID) > 256 || len(pack.Versions) == 0 {
				return errLegacyCatalogInvalid
			}
			ids[id], pack.SourceID = true, id
			pack.ResumeFrom = canonicalLegacyTime(pack.ResumeFrom)
			for k := range pack.Versions {
				rule := &pack.Versions[k]
				id, valid := canonicalSourceUUID(rule.SourceID)
				if !valid || ids[id] || rule.Number < 1 || !textOK(rule.Status, 48) ||
					!textOK(rule.Hash, 256) || rule.From.IsZero() || rule.Through != nil && !rule.Through.After(rule.From) {
					return errLegacyCatalogInvalid
				}
				ids[id], rule.SourceID = true, id
				rule.From = rule.From.UTC().Truncate(time.Microsecond)
				rule.Through = canonicalLegacyTime(rule.Through)
				body, err := legacyBusinessDefinition(rule.Definition)
				if err != nil {
					return err
				}
				rule.Definition = body
			}
			sort.Slice(pack.Versions, func(a, b int) bool { return pack.Versions[a].SourceID < pack.Versions[b].SourceID })
		}
		sort.Slice(product.Packages, func(a, b int) bool { return product.Packages[a].SourceID < product.Packages[b].SourceID })
	}
	return nil
}

func canonicalLegacyTime(source *time.Time) *time.Time {
	if source == nil {
		return nil
	}
	at := source.UTC().Truncate(time.Microsecond)
	return &at
}

func legacyBusinessDefinition(raw json.RawMessage) (json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var body map[string]any
	if err := d.Decode(&body); err != nil || body == nil {
		return nil, errLegacyCatalogInvalid
	}
	removeLegacyAttachments(body)
	return json.Marshal(body)
}

func removeLegacyAttachments(value any) {
	switch body := value.(type) {
	case map[string]any:
		for key, child := range body {
			switch key {
			case "evidence_ref", "evidence_refs", "evidence_path", "evidence_paths", "golden_cases":
				delete(body, key)
			default:
				removeLegacyAttachments(child)
			}
		}
	case []any:
		for _, child := range body {
			removeLegacyAttachments(child)
		}
	}
}

func storeLegacyCatalog(ctx context.Context, tx pgx.Tx, input legacyCatalogFile) error {
	if err := normalizeLegacyCatalog(&input); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(891520447)"); err != nil {
		return err
	}
	for _, product := range input.Products {
		id, changed, err := storeLegacyProduct(ctx, tx, input.Environment, input.CapturedAt, product)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		for _, route := range product.BusinessRoutes {
			if err := storeCatalogRoute(ctx, tx, id, input.CapturedAt, route); err != nil {
				return err
			}
		}
		for _, pack := range product.Packages {
			if err := storeLegacyPackage(ctx, tx, id, input.CapturedAt, pack); err != nil {
				return err
			}
		}
		if err := enqueueDraftConfiguration(ctx, tx, draftRefreshProduct, id); err != nil {
			return err
		}
	}
	return nil
}

func storeLegacyProduct(ctx context.Context, tx pgx.Tx, environment string, captured time.Time, product legacyCatalogProduct) (int64, bool, error) {
	raw, err := json.Marshal(product)
	if err != nil {
		return 0, false, err
	}
	digest := sha256.Sum256(raw)
	var id int64
	var storedEnvironment string
	var storedAt time.Time
	var stored []byte
	var same bool
	err = tx.QueryRow(ctx, `SELECT l.product_id,l.source_environment,l.captured_at,l.snapshot_digest,
	 (p.code=$2 AND p.period_mode=$3 AND p.cycle_start_day IS NOT DISTINCT FROM $4::smallint)
	 FROM legacy_catalog_products l JOIN products p ON p.id=l.product_id WHERE l.source_id=$1::uuid FOR UPDATE OF p,l`,
		product.SourceID, product.Code, product.PeriodMode, product.CycleStartDay).Scan(&id, &storedEnvironment, &storedAt, &stored, &same)
	if err == nil {
		if !same || storedEnvironment != environment {
			return 0, false, errLegacyCatalogConflict
		}
		if captured.Before(storedAt) {
			return 0, false, errLegacyCatalogStale
		}
		if captured.Equal(storedAt) {
			if !bytes.Equal(digest[:], stored) {
				return 0, false, errLegacyCatalogConflict
			}
			return id, false, nil
		}
		var published bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM legacy_rule_versions v
		 JOIN legacy_rule_packages p ON p.source_id=v.package_id WHERE p.product_id=$1 AND v.published_at IS NOT NULL)`, id).Scan(&published); err != nil {
			return 0, false, err
		}
		if published {
			return 0, false, errCatalogConfigurationOwned
		}
		if _, err = tx.Exec(ctx, "UPDATE products SET name=$2 WHERE id=$1", id, product.Name); err != nil {
			return 0, false, err
		}
		_, err = tx.Exec(ctx, `UPDATE legacy_catalog_products SET project_code=$2,source_system=$3,source_key=$4,captured_at=$5,snapshot_digest=$6 WHERE product_id=$1`,
			id, product.Project, product.Source, product.SourceKey, captured, digest[:])
		return id, err == nil, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO products(code,name,period_mode,cycle_start_day) VALUES($1,$2,$3,$4)
	 ON CONFLICT(code) DO NOTHING RETURNING id`, product.Code, product.Name, product.PeriodMode, product.CycleStartDay).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, errLegacyCatalogConflict
	}
	if err != nil {
		return 0, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO legacy_catalog_products(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
	 VALUES($1,$2::uuid,$3,$4,$5,$6,$7,$8)`, id, product.SourceID, environment, product.Project, product.Source, product.SourceKey, captured, digest[:])
	return id, err == nil, err
}

func storeLegacyPackage(ctx context.Context, tx pgx.Tx, productID int64, observed time.Time, pack legacyCatalogPackage) error {
	tag, err := tx.Exec(ctx, `INSERT INTO legacy_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused,resume_from)
	 VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	 ON CONFLICT(source_id) DO UPDATE SET name=excluded.name,paused=excluded.paused,resume_from=excluded.resume_from
	 WHERE (legacy_rule_packages.product_id,legacy_rule_packages.code,legacy_rule_packages.project_code,legacy_rule_packages.policy_kind,
	  legacy_rule_packages.scope_type,legacy_rule_packages.scope_id) =
	  (excluded.product_id,excluded.code,excluded.project_code,excluded.policy_kind,excluded.scope_type,excluded.scope_id)`,
		pack.SourceID, productID, pack.Code, pack.Name, pack.Project, pack.Kind, pack.ScopeType, pack.ScopeID, pack.Paused, pack.ResumeFrom)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLegacyCatalogConflict
	}
	for _, rule := range pack.Versions {
		tag, err = tx.Exec(ctx, `INSERT INTO legacy_rule_versions(source_id,package_id,version_no,source_status,effective_from,effective_to,content_hash,definition,observed_at)
		 VALUES($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT(source_id) DO UPDATE SET source_status=excluded.source_status,effective_from=excluded.effective_from,effective_to=excluded.effective_to,observed_at=excluded.observed_at
		 WHERE (legacy_rule_versions.package_id,legacy_rule_versions.version_no,legacy_rule_versions.content_hash,legacy_rule_versions.definition) =
		  (excluded.package_id,excluded.version_no,excluded.content_hash,excluded.definition)`,
			rule.SourceID, pack.SourceID, rule.Number, rule.Status, rule.From, rule.Through, rule.Hash, rule.Definition, observed)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errLegacyCatalogConflict
		}
	}
	return nil
}

func importProductConfig(ctx context.Context, cfg config, filename string) error {
	if !strings.HasSuffix(cfg.database.ConnConfig.Database, "_dev") {
		return errors.New("产品配置导入需要使用专用开发数据库")
	}
	var input legacyCatalogFile
	if err := readImportFile(filename, &input, 32<<20); err != nil {
		return err
	}
	db, err := pgxpool.NewWithConfig(ctx, cfg.database)
	if err != nil {
		return errors.New("could not initialize the isolated database")
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		return err
	}
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error { return storeLegacyCatalog(ctx, tx, input) }); err != nil {
		return err
	}
	fmt.Printf("已导入并核验 %d 个产品的配置\n", len(input.Products))
	return nil
}
