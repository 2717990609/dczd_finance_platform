package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCopyCatalogScenarioRuleMakesTargetSKUSelectableAndLeavesSourceIntact(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	const base = "60000000-0000-4000-8000-000000000002"
	for _, statement := range []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE VALUES(911,'COPY','复制样本','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		  VALUES(911,gen_random_uuid(),'test','COPY','DCAI','sample','2026-10-01',sha256('copy'::bytea))`,
		`INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
		  VALUES('60000000-0000-4000-8000-000000000001',911,'old','原规则','COPY','COMPONENT_RULE','BUSINESS_SCENARIO','NEW',false)`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,
		   classifier,sku_code,sku_label,mapping_version,effective_from,observed_at,source_product_code)
		  VALUES(gen_random_uuid(),911,'DCAI','VIP','ORDER','vip','book','new','NEW','新报',1,'2026-07-01','2026-10-01','sample'),
		  (gen_random_uuid(),911,'DCAI','VIP','ORDER','vip','book','renew','RENEW','复购',1,'2026-09-01','2026-10-01','sample'),
		  (gen_random_uuid(),911,'DCAI','HEALTH_IDENTITY','ORDER','health','book','new','HEALTH','健康',1,'2026-09-01','2026-10-01','sample')`,
		`INSERT INTO periods(id,product_id,name,ordinal,starts_on,ends_on,calendar_month) OVERRIDING SYSTEM VALUE VALUES
		  (931,911,'2026-10',202610,'2026-09-25','2026-10-24','2026-10-01')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	definition := json.RawMessage(strings.NewReplacer(
		`"amount_basis"`, `"schema_version":"1.1","refund_policy":"PROPORTIONAL","recognition_trigger":"PAID",
		"settlement_policy":{"cycle":"MONTHLY_25_TO_24"},"policy_scope":{"business_sku_codes":["NEW"],"sales_systems":["DCAI"],"financial_event_types":["order.paid"]},"amount_basis"`,
		`"item_key":"sales","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY"`,
		`"item_key":"sales","label":"销售","role":"SALES","type":"RATE_TABLE","beneficiary_mode":"PARTY",
		"rate_table":[{"sales_organization_party_id":"ANY","sales_relationship":"ANY","rate_bps":1000}]`,
	).Replace(testVIPBenefits))
	if _, err := db.Exec(ctx, `INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,effective_to,content_hash,definition,observed_at)
	 VALUES($1,'60000000-0000-4000-8000-000000000001',1,'PUBLISHED','2026-08-01','2026-09-30','old',$2,'2026-10-01')`, base, definition); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 2, 0, 0, 0, 0, shanghai)
	input := publishCatalogRatesInput{BaseID: base, EffectiveFrom: from, Copy: &catalogScenarioCopy{SKU: "RENEW", Label: "复购分润"}}
	result, err := (workspace{db: db}).publishCatalogRates(ctx, input, "fixture")
	if err != nil || result.ID == base || result.Version != 1 {
		t.Fatalf("copy could not be published: %+v, %v", result, err)
	}
	var original json.RawMessage
	var originalSKU, targetSKU string
	if err := db.QueryRow(ctx, `SELECT old.definition,old.definition#>>'{policy_scope,business_sku_codes,0}',new.definition#>>'{policy_scope,business_sku_codes,0}'
	  FROM product_rule_versions old JOIN product_rule_versions new ON new.source_id=$2 WHERE old.source_id=$1`, base, result.ID).Scan(&original, &originalSKU, &targetSKU); err != nil ||
		originalSKU != "NEW" || targetSKU != "RENEW" {
		t.Fatalf("incorrect version definition: %s %s, %v", originalSKU, targetSKU, err)
	}
	// The new package must be visible to the same resolver used by order calculation.
	var count int
	var selected *string
	if err := db.QueryRow(ctx, catalogActiveRateVersionSQL, 911, "RENEW", from).Scan(&count, &selected); err != nil ||
		count != 1 || selected == nil || *selected != result.ID {
		t.Fatalf("target resolver count %d selection %v error %v", count, selected, err)
	}
	if _, err := (workspace{db: db}).publishCatalogRates(ctx, input, "fixture"); !errors.Is(err, errCatalogScenarioOccupied) {
		t.Fatalf("a second package would overlap: %v", err)
	}
	input.Copy.SKU = "HEALTH"
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		_, err := writeCatalogRates(ctx, tx, input, "fixture", false)
		return err
	})
	if !errors.Is(err, errCatalogScenarioTarget) {
		t.Fatalf("copied incompatible source domain: %v", err)
	}
}
