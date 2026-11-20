package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var errCatalogScenarioTarget = errors.New("请选择在生效时间已有同类订单映射的目标 SKU")
var errCatalogScenarioOccupied = errors.New("目标 SKU 已有生效或排定的规则，请从该 SKU 的规则配置维护")

type catalogScenarioCopy struct {
	SKU   string `json:"sku"`
	Label string `json:"label"`
}

const catalogScenarioOccupiedSQL = `EXISTS (
 SELECT 1 FROM product_catalog_settings scope
 JOIN product_rule_packages p ON p.product_id=scope.product_id AND p.project_code=scope.project_code
 JOIN product_rule_versions v ON v.package_id=p.source_id AND ` + catalogVersionVisible + `
 WHERE scope.product_id=$1 AND p.scope_type='BUSINESS_SCENARIO' AND p.scope_id=$2
  AND p.policy_kind='COMPONENT_RULE' AND v.source_status='PUBLISHED'
  AND (` + catalogRuleExecutionThrough + ` IS NULL OR ` + catalogRuleExecutionThrough + `>$3)
)`

func checkCatalogScenarioCopy(ctx context.Context, tx pgx.Tx, product int64, source string, target catalogScenarioCopy, from time.Time) error {
	if !textOK(target.Label, 80) || !textOK(target.SKU, 256) || source == target.SKU {
		return errCatalogScenarioTarget
	}
	var mapped, occupied bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
	 SELECT 1 FROM product_catalog_settings scope
	 JOIN catalog_business_routes destination ON destination.product_id=scope.product_id AND destination.observed_at=scope.captured_at
	 JOIN catalog_business_routes template ON template.product_id=scope.product_id AND template.observed_at=scope.captured_at
	   AND template.source=destination.source AND template.domain=destination.domain AND template.entity_type=destination.entity_type
	   AND (template.domain<>'HEALTH_IDENTITY' OR template.source_product_code=destination.source_product_code)
	 WHERE scope.product_id=$1 AND destination.source=scope.source_system AND destination.sku_code=$2 AND template.sku_code=$4
	  AND destination.effective_from<=$3 AND (destination.effective_to IS NULL OR destination.effective_to>$3)
	),`+catalogScenarioOccupiedSQL, product, target.SKU, from, source).Scan(&mapped, &occupied)
	if err != nil {
		return err
	}
	if !mapped {
		return errCatalogScenarioTarget
	}
	if occupied {
		return errCatalogScenarioOccupied
	}
	return nil
}

func copyCatalogScenarioDefinition(body json.RawMessage, source, target string) (json.RawMessage, error) {
	var root, scope map[string]json.RawMessage
	var skuCodes []string
	if json.Unmarshal(body, &root) != nil || json.Unmarshal(root["policy_scope"], &scope) != nil ||
		json.Unmarshal(scope["business_sku_codes"], &skuCodes) != nil || !slices.Contains(skuCodes, source) {
		return nil, errCatalogRateUnavailable
	}
	scope["business_sku_codes"], _ = json.Marshal([]string{target})
	root["policy_scope"], _ = json.Marshal(scope)
	return json.Marshal(root)
}

func insertCatalogScenarioRule(ctx context.Context, tx pgx.Tx, product int64, target catalogScenarioCopy,
	from time.Time, actor, hash string, body json.RawMessage) (catalogPublication, error) {
	var result catalogPublication
	err := tx.QueryRow(ctx, `WITH package AS (
	 INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
	 SELECT id,s.product_id,'local-'||id::text,$2,s.project_code,'COMPONENT_RULE','BUSINESS_SCENARIO',$3,false
	 FROM product_catalog_settings s CROSS JOIN (SELECT gen_random_uuid() AS id) key WHERE s.product_id=$1
	 RETURNING source_id
	)
	INSERT INTO product_rule_versions
	 (source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at,published_at,published_by)
	SELECT gen_random_uuid(),source_id,1,'PUBLISHED',$4,$5,$6,now(),now(),$7 FROM package
	RETURNING source_id::text,version_no`, product, strings.TrimSpace(target.Label), target.SKU, from, hash, body, actor).
		Scan(&result.ID, &result.Version)
	return result, err
}
