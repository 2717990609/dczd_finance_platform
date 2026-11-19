package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type catalogBusinessRoute struct {
	SourceID    string     `json:"sourceId"`
	Source      string     `json:"source"`
	Domain      string     `json:"domain"`
	Entity      string     `json:"entity"`
	RecordTable string     `json:"recordTable"`
	BusinessKey string     `json:"businessKey"`
	Classifier  string     `json:"classifier"`
	SKU         string     `json:"sku"`
	Label       string     `json:"label"`
	Version     int        `json:"version"`
	From        time.Time  `json:"from"`
	Through     *time.Time `json:"through,omitempty"`
}

type sourceClassification struct {
	Entity, Key, Classifier string
}

func classifySourcePayment(source, domain, entity, table string) sourceClassification {
	// The VIP feed preserves the original transaction type. Its current finance
	// classifier treats both first and repeat VIP purchases as the same scenario.
	if source == "DCAI" && domain == "VIP" && entity == "VIP_ORDER" && table == "ai_vip_order" {
		return sourceClassification{entity, "dcai:vip:new", "dcai.vip-lifecycle.v1"}
	}
	return sourceClassification{}
}

func storeSourceClassification(ctx context.Context, tx pgx.Tx, version int64, classification sourceClassification) error {
	tag, err := tx.Exec(ctx, `INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
	 VALUES($1,$2,$3,$4) ON CONFLICT(source_version_id) DO UPDATE SET classifier=excluded.classifier
	 WHERE (source_order_classifications.entity_type,source_order_classifications.business_key,source_order_classifications.classifier)
	 =(excluded.entity_type,excluded.business_key,excluded.classifier)`, version, classification.Entity, classification.Key, classification.Classifier)
	if err == nil && tag.RowsAffected() == 0 {
		return conflict
	}
	return err
}

func normalizeCatalogRoutes(product *legacyCatalogProduct, ids map[string]bool) error {
	for i := range product.BusinessRoutes {
		route := &product.BusinessRoutes[i]
		id, valid := canonicalSourceUUID(route.SourceID)
		if !valid || ids[id] || route.Source != product.Source || product.PeriodMode != "calendar" ||
			!textOK(route.Domain, 80) || !textOK(route.Entity, 80) || !textOK(route.RecordTable, 150) ||
			!textOK(route.BusinessKey, 180) || !textOK(route.Classifier, 128) || !textOK(route.SKU, 128) ||
			!textOK(route.Label, 200) || route.Version < 1 || route.From.IsZero() ||
			route.Through != nil && !route.Through.After(route.From) {
			return errLegacyCatalogInvalid
		}
		ids[id], route.SourceID = true, id
		route.From = route.From.UTC().Truncate(time.Microsecond)
		route.Through = canonicalLegacyTime(route.Through)
	}
	return nil
}

func storeCatalogRoute(ctx context.Context, tx pgx.Tx, productID int64, observed time.Time, route catalogBusinessRoute) error {
	tag, err := tx.Exec(ctx, `INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,
	 classifier,sku_code,sku_label,mapping_version,effective_from,effective_to,observed_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	 ON CONFLICT(source_id) DO UPDATE SET sku_label=excluded.sku_label,effective_to=excluded.effective_to,observed_at=excluded.observed_at
	 WHERE (catalog_business_routes.product_id,catalog_business_routes.source,catalog_business_routes.domain,catalog_business_routes.entity_type,
	  catalog_business_routes.record_table,catalog_business_routes.business_key,catalog_business_routes.classifier,
	  catalog_business_routes.sku_code,catalog_business_routes.mapping_version,catalog_business_routes.effective_from)
	 =(excluded.product_id,excluded.source,excluded.domain,excluded.entity_type,excluded.record_table,excluded.business_key,
	  excluded.classifier,excluded.sku_code,excluded.mapping_version,excluded.effective_from)`, route.SourceID, productID, route.Source, route.Domain,
		route.Entity, route.RecordTable, route.BusinessKey, route.Classifier, route.SKU, route.Label, route.Version, route.From, route.Through, observed)
	if err == nil && tag.RowsAffected() == 0 {
		return errLegacyCatalogConflict
	}
	return err
}

// Catalogue routes own their source record family. A payment awaiting
// classification must not fall back to an unrelated raw product-code mapping.
const catalogSourceReservedSQL = `(
 EXISTS(SELECT 1 FROM source_order_classifications classification WHERE classification.source_version_id=v.id)
 OR EXISTS(SELECT 1 FROM catalog_business_routes route
  WHERE route.source=v.source AND route.domain=v.fact->>'domain' AND route.record_table=split_part(v.record_key,':',1))
)`
