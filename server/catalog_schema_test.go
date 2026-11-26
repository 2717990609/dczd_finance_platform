package main

import (
	"context"
	"testing"
)

func TestProductRuleCatalogUpgradePreservesRuleReferences(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	if _, err := db.Exec(ctx, `
	 DELETE FROM finance_schema_revisions;
	 ALTER TABLE product_rule_versions RENAME TO legacy_rule_versions;
	 ALTER TABLE product_rule_packages RENAME TO legacy_rule_packages;
	 ALTER TABLE product_catalog_settings RENAME TO legacy_catalog_products;
	 ALTER INDEX product_rule_packages_product RENAME TO legacy_rule_packages_product;
	 INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE
	 VALUES(151,'vip-upgrade','会员','calendar',25);
	 INSERT INTO legacy_catalog_products(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
	 VALUES(151,'b048ab55-8533-44d2-b568-a23b69bb2c10','test','AI','DCAI','test.vip',now(),decode(repeat('01',32),'hex'));
	 INSERT INTO legacy_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
	 VALUES('b048ab55-8533-44d2-b568-a23b69bb2c11',151,'member','会员分润','AI','LEGACY_BUNDLE','BUSINESS_SCENARIO','NEW',false);
	 INSERT INTO legacy_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at,published_at,published_by)
	 VALUES('b048ab55-8533-44d2-b568-a23b69bb2c12','b048ab55-8533-44d2-b568-a23b69bb2c11',1,'PUBLISHED',now(),'hash','{}',now(),now(),'finance')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	}
	var version int
	var kind string
	var hasDraftReference bool
	if err := db.QueryRow(ctx, `SELECT v.version_no,p.policy_kind,EXISTS(
	 SELECT 1 FROM pg_constraint c JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=ANY(c.conkey)
	 WHERE c.contype='f' AND c.conrelid='calculation_draft_lines'::regclass
	  AND c.confrelid='product_rule_versions'::regclass AND a.attname='catalog_rule_id')
	 FROM product_rule_versions v JOIN product_rule_packages p ON p.source_id=v.package_id
	 WHERE v.source_id='b048ab55-8533-44d2-b568-a23b69bb2c12'`).Scan(&version, &kind, &hasDraftReference); err != nil {
		t.Fatal(err)
	}
	if version != 1 || kind != "COMPONENT_RULE" || !hasDraftReference {
		t.Fatalf("version=%d, kind=%s, draft reference=%t", version, kind, hasDraftReference)
	}
	if _, err := db.Exec(ctx, `UPDATE product_rule_versions SET content_hash='changed'
	 WHERE source_id='b048ab55-8533-44d2-b568-a23b69bb2c12'`); err == nil {
		t.Fatal("published rule could be modified after the upgrade")
	}
}

func TestCatalogPoolUpgradeKeepsImportedPoolIDs(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	if _, err := db.Exec(ctx, `
	 INSERT INTO products(id,code,name,period_mode) OVERRIDING SYSTEM VALUE VALUES(151,'pool-upgrade','资金池测试','calendar');
	 INSERT INTO organization_units(id,kind,name,status)
	 VALUES('41000000-0000-4000-8000-000000000151','COMPANY','归集公司','ACTIVE');
	 INSERT INTO practice_pools(id,product_id,unit_id,name) OVERRIDING SYSTEM VALUE
	 VALUES(1,151,'41000000-0000-4000-8000-000000000151','归档资金池');
	 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
	 VALUES(151,'41000000-0000-4000-8000-000000000150','test','AI','DCAI','test.vip',now(),decode(repeat('01',32),'hex'));
	 INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
	 VALUES('41000000-0000-4000-8000-000000000152',151,'vip','会员分润','AI','COMPONENT_RULE','BUSINESS_SCENARIO','NEW',false);
	 INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at,published_at,published_by)
	 VALUES('41000000-0000-4000-8000-000000000153','41000000-0000-4000-8000-000000000152',1,'PUBLISHED',now(),'hash',
	  '{"benefit_actions":[{"beneficiary_mode":"TEAM_POOL","pool_name":"规则资金池","pool_organization_unit_id":"41000000-0000-4000-8000-000000000151"}]}',
	  now(),now(),'finance')`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := migrate(ctx, db); err != nil {
			t.Fatalf("startup must preserve manually registered pool IDs: %v", err)
		}
	}
	var imported, registered, count int64
	err := db.QueryRow(ctx, `SELECT max(id) FILTER(WHERE name='归档资金池'),
	 coalesce(max(id) FILTER(WHERE name='规则资金池'),0), count(*) FROM practice_pools WHERE product_id=151`).Scan(&imported, &registered, &count)
	if err != nil || imported != 1 || registered != 0 || count != 1 {
		t.Fatalf("imported ID=%d, registered ID=%d, count=%d, error=%v", imported, registered, count, err)
	}
}
