package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type catalogBusinessRoute struct {
	SourceID          string     `json:"sourceId"`
	Source            string     `json:"source"`
	Domain            string     `json:"domain"`
	SourceProductCode string     `json:"sourceProductCode,omitempty"`
	Entity            string     `json:"entity"`
	RecordTable       string     `json:"recordTable"`
	BusinessKey       string     `json:"businessKey"`
	Classifier        string     `json:"classifier"`
	SKU               string     `json:"sku"`
	Label             string     `json:"label"`
	Version           int        `json:"version"`
	From              time.Time  `json:"from"`
	Through           *time.Time `json:"through,omitempty"`
}

type sourceClassification struct {
	Entity, Key, Classifier string
}

func classifySourcePayment(source, domain, entity, table, productCode string) sourceClassification {
	// The VIP feed preserves the original transaction type. Its current finance
	// classifier treats both first and repeat VIP purchases as the same scenario.
	if source == "DCAI" && domain == "VIP" && entity == "VIP_ORDER" && table == "ai_vip_order" {
		return sourceClassification{entity, "dcai:vip:new", "dcai.vip-lifecycle.v1"}
	}
	if source == "DCAI" && domain == "HEALTH_IDENTITY" && entity == "HEALTH_IDENTITY_ORDER" && table == "ai_health_ambassador_order" {
		switch productCode {
		case "HEALTH_AMBASSADOR":
			return sourceClassification{entity, "dcai:health-ambassador:new", "dcai.health-identity.v1"}
		case "HEALTH_CREATOR":
			return sourceClassification{entity, "dcai:health-creator:new", "dcai.health-identity.v1"}
		}
	}
	return sourceClassification{}
}

func storeSourceClassification(ctx context.Context, tx pgx.Tx, version int64, classification sourceClassification, claims []vipClaim, created bool) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier,sales_claims,sales_claims_completed_at)
	 VALUES($1,$2,$3,$4,$5,CASE WHEN NOT $6 AND $5::jsonb IS NOT NULL THEN now() END)
	  ON CONFLICT(source_version_id) DO UPDATE
	  SET sales_claims=excluded.sales_claims,sales_claims_completed_at=now()
	 WHERE source_order_classifications.sales_claims IS NULL AND excluded.sales_claims IS NOT NULL
	 AND (source_order_classifications.entity_type,source_order_classifications.business_key,source_order_classifications.classifier)
	 =(excluded.entity_type,excluded.business_key,excluded.classifier)`, version, classification.Entity, classification.Key, classification.Classifier, claims, created)
	if err != nil || tag.RowsAffected() != 0 {
		return err == nil, err
	}
	var same bool
	err = tx.QueryRow(ctx, `SELECT (entity_type,business_key,classifier)=($2,$3,$4)
	 AND ($5::jsonb IS NULL OR sales_claims=$5) FROM source_order_classifications WHERE source_version_id=$1`,
		version, classification.Entity, classification.Key, classification.Classifier, claims).Scan(&same)
	if err == nil && !same {
		err = conflict
	}
	return false, err
}

func normalizeCatalogRoutes(product *productConfigEntry, ids map[string]bool) error {
	for i := range product.BusinessRoutes {
		route := &product.BusinessRoutes[i]
		id, valid := canonicalSourceUUID(route.SourceID)
		if !valid || ids[id] || route.Source != product.Source || product.PeriodMode != "calendar" ||
			!textOK(route.Domain, 80) || !textOK(route.Entity, 80) || !textOK(route.RecordTable, 150) ||
			(route.SourceProductCode != "" || route.Domain == "HEALTH_IDENTITY") && !textOK(route.SourceProductCode, 200) ||
			!textOK(route.BusinessKey, 180) || !textOK(route.Classifier, 128) || !textOK(route.SKU, 128) ||
			!textOK(route.Label, 200) || route.Version < 1 || route.From.IsZero() ||
			route.Through != nil && !route.Through.After(route.From) {
			return errProductConfigInvalid
		}
		ids[id], route.SourceID = true, id
		route.From = route.From.UTC().Truncate(time.Microsecond)
		route.Through = canonicalConfigTime(route.Through)
	}
	return nil
}

func storeCatalogRoute(ctx context.Context, tx pgx.Tx, productID int64, observed time.Time, route catalogBusinessRoute) error {
	tag, err := tx.Exec(ctx, `INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,
	 classifier,sku_code,sku_label,mapping_version,effective_from,effective_to,observed_at,source_product_code)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,nullif($15,''))
	 ON CONFLICT(source_id) DO UPDATE SET sku_label=excluded.sku_label,effective_to=excluded.effective_to,observed_at=excluded.observed_at,
	 source_product_code=excluded.source_product_code
	 WHERE (catalog_business_routes.product_id,catalog_business_routes.source,catalog_business_routes.domain,catalog_business_routes.entity_type,
	  catalog_business_routes.record_table,catalog_business_routes.business_key,catalog_business_routes.classifier,
	  catalog_business_routes.sku_code,catalog_business_routes.mapping_version,catalog_business_routes.effective_from)
	 =(excluded.product_id,excluded.source,excluded.domain,excluded.entity_type,excluded.record_table,excluded.business_key,
	  excluded.classifier,excluded.sku_code,excluded.mapping_version,excluded.effective_from)
	  AND (catalog_business_routes.source_product_code IS NOT DISTINCT FROM excluded.source_product_code
	   OR excluded.domain='HEALTH_IDENTITY' AND catalog_business_routes.source_product_code IS NULL)`, route.SourceID, productID, route.Source, route.Domain,
		route.Entity, route.RecordTable, route.BusinessKey, route.Classifier, route.SKU, route.Label, route.Version, route.From, route.Through, observed, route.SourceProductCode)
	if err == nil && tag.RowsAffected() == 0 {
		return errProductConfigConflict
	}
	return err
}

const catalogRouteFactSQL = `(m.source,m.domain,m.record_table)=(v.source,v.fact->>'domain',split_part(v.record_key,':',1))
 AND (m.source_product_code=v.fact->>'sourceKey' OR m.source_product_code IS NULL AND m.domain<>'HEALTH_IDENTITY')`

// Catalogue routes own their source record family. A payment awaiting
// classification must not fall back to an unrelated raw product-code mapping.
const catalogSourceReservedSQL = `(
 EXISTS(SELECT 1 FROM source_order_classifications classification WHERE classification.source_version_id=v.id)
 OR EXISTS(SELECT 1 FROM catalog_business_routes route
  WHERE route.source=v.source AND route.domain=v.fact->>'domain' AND route.record_table=split_part(v.record_key,':',1))
)`
