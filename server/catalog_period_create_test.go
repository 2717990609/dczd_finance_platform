package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/jackc/pgx/v5"
)

func TestCreateCatalogPeriodRuleFromEmptyConfiguration(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	for _, sql := range []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE VALUES(980,'DCAI_WHOLE','整期验收','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		 VALUES(980,gen_random_uuid(),'test','DCAI','DCAI','new','2026-11-01',sha256('fixture'::bytea))`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,
		 sku_code,sku_label,mapping_version,effective_from,observed_at) VALUES
		 (gen_random_uuid(),980,'DCAI','VIP','VIP_ORDER','ai_vip_order','first','vip','FIRST','会员',1,'2026-09-01','2026-11-01'),
		 (gen_random_uuid(),980,'DCAI','VIP','VIP_ORDER','ai_vip_order','second','vip','SECOND','另一个SKU',1,'2026-09-01','2026-11-01')`,
		`INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(9801,'整期个人收款')`,
		`INSERT INTO organization_units(id,kind,name,status) VALUES ('10000000-0000-4000-8000-000000000981','COMPANY','整期公司收款','ACTIVE'),
		 ('10000000-0000-4000-8000-000000000986','COMPANY','其他销售公司','ACTIVE')`,
		`INSERT INTO organization_units(id,parent_id,kind,name,status) VALUES
		 ('10000000-0000-4000-8000-000000000982','10000000-0000-4000-8000-000000000981','DEPARTMENT','销售一部','ACTIVE'),
		 ('10000000-0000-4000-8000-000000000983','10000000-0000-4000-8000-000000000981','DEPARTMENT','销售二部','ACTIVE'),
		 ('10000000-0000-4000-8000-000000000984','10000000-0000-4000-8000-000000000982','TEAM','销售一组','ACTIVE'),
		 ('10000000-0000-4000-8000-000000000985','10000000-0000-4000-8000-000000000981','TEAM','直属小组','ACTIVE'),
		 ('10000000-0000-4000-8000-000000000987','10000000-0000-4000-8000-000000000986','DEPARTMENT','其他公司销售部','ACTIVE')`,
		`INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(9802,'整期销售'),
		 (9803,'另一组销售'),(9804,'直属组销售')`,
		`INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES
		 (9802,'DCAI','seller','2026-09-01'),(9803,'DCAI','other','2026-09-01'),(9804,'DCAI','direct','2026-09-01')`,
		`INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
		 VALUES('DCAI','company','10000000-0000-4000-8000-000000000981','2026-09-01'),
		 ('DCAI','outside','10000000-0000-4000-8000-000000000986','2026-09-01')`,
		`INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,valid_to) VALUES
		 (gen_random_uuid(),9802,'10000000-0000-4000-8000-000000000984','MEMBER','2026-09-01','2026-10-03T00:00Z'),
		 (gen_random_uuid(),9802,'10000000-0000-4000-8000-000000000983','MEMBER','2026-10-03T00:00Z',NULL),
		 (gen_random_uuid(),9802,'10000000-0000-4000-8000-000000000987','PRIMARY','2026-09-01',NULL),
		 (gen_random_uuid(),9803,'10000000-0000-4000-8000-000000000984','PRIMARY','2026-09-01',NULL),
		 (gen_random_uuid(),9804,'10000000-0000-4000-8000-000000000985','PRIMARY','2026-09-01',NULL)`,
		`INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest)
		 VALUES('DCAI','2026-11-01',sha256('fixture'::bytea))`,
		`INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
		 VALUES(gen_random_uuid(),'DCAI',9801,'TEAM_LEAD','ORGANIZATION_UNIT','10000000-0000-4000-8000-000000000982',
		 '2026-10-02T00:00Z','2026-11-01')`,
		`WITH payment AS (INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
		 VALUES('DCAI','101','','ai_vip_order:101','1','ORDER_PAID','2026-10-02T00:00Z','2026-10-02T00:00Z',
		 '{"domain":"VIP","sourceKey":"new","paymentId":"101","amountMinor":10000}'),
		 ('DCAI','102','','ai_vip_order:102','1','ORDER_PAID','2026-10-03T00:00Z','2026-10-03T00:00Z',
		 '{"domain":"VIP","sourceKey":"new","paymentId":"102","amountMinor":30000}') RETURNING id,order_id)
		 INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier,sales_claims)
		 SELECT id,'VIP_ORDER',CASE order_id WHEN '101' THEN 'first' ELSE 'second' END,'vip',
		   '[{"role":"PID_USER","user":"seller","company":"company","relationship":"DIRECT"}]'::jsonb FROM payment`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	s := workspace{db: db, development: true, ruleAuth: catalogRuleAuth{issuer: "finance-test", key: []byte("isolated-finance-aggregate-test-key")},
		sources: map[string]sourceConfig{"DCAI": {BaseURL: "https://fixture.invalid", Token: "fixture"}}}
	submit := func(grant int64, path string, handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if grant == 0 {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			w := httptest.NewRecorder()
			handler(w, r)
			return w
		}
		claims := catalogRuleClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: s.ruleAuth.issuer, Subject: "fixture",
			Audience: jwt.ClaimStrings{"finance-platform"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))},
			Permissions: []string{catalogRulePublishPermission}, ProductIDs: []int64{grant}}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.ruleAuth.key)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	const path = "/api/v1/catalog-period-rules"
	const input = `{"create":{"productId":980,"label":"新整期规则","cycle":"MONTHLY_25_TO_24"},
	 "effectiveFrom":"2026-09-25T00:00:00+08:00","scenarioCodes":["FIRST","SECOND"],"components":[
	 {"label":"公司安排","targetKind":"company","organizationKey":"unit:10000000-0000-4000-8000-000000000981","tiers":[{"rate_bps":1500}]},
	 {"label":"个人安排","targetKind":"person","personId":9801,"tiers":[{"rate_bps":500}]}]}`
	quoteInput := strings.TrimSuffix(input, "}") + `,"receiptsMinor":40000}`
	preview := submit(0, path+"/preview", s.postCatalogPeriodQuote, quoteInput)
	var quote catalogPeriodQuote
	if err := json.Unmarshal(preview.Body.Bytes(), &quote); err != nil || preview.Code != 200 || quote.Total != 8000 {
		t.Fatalf("unsaved aggregate quote should price both benefits: %d %s %v", preview.Code, preview.Body.String(), err)
	}
	var packages int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM product_rule_packages WHERE product_id=980`).Scan(&packages); err != nil || packages != 0 {
		t.Fatalf("preview should not publish a package: %d %v", packages, err)
	}
	if bad := submit(980, path, s.postCatalogPeriodRule, strings.Replace(input, "2026-09-25", "2026-09-24", 1)); bad.Code != 400 {
		t.Fatalf("creation accepted a date outside the selected cycle: %d %s", bad.Code, bad.Body.String())
	}
	if unmapped := submit(980, path, s.postCatalogPeriodRule, strings.Replace(input, "SECOND", "UNMAPPED", 1)); unmapped.Code != 409 {
		t.Fatalf("creation accepted an unmapped SKU: %d %s", unmapped.Code, unmapped.Body.String())
	}
	w := submit(0, path, s.postCatalogPeriodRule, input)
	var published catalogPublication
	if err := json.Unmarshal(w.Body.Bytes(), &published); err != nil || w.Code != 200 || published.ID == "" || published.Version != 1 {
		t.Fatalf("first aggregate rule did not publish: %d %s %v", w.Code, w.Body.String(), err)
	}
	var publisher string
	if err := db.QueryRow(ctx, `SELECT published_by FROM product_rule_versions WHERE source_id=$1`, published.ID).Scan(&publisher); err != nil || publisher != "local-development" {
		t.Fatalf("anonymous local publication must identify the development actor: %q %v", publisher, err)
	}
	trial, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: published.ID, Month: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	if got := trial.(draftSummary); got.Total != 8000 || got.LineCount != 2 || got.IssueCount != 0 {
		t.Fatalf("first version should trial both mapped SKUs as one accounting period: %+v", got)
	}
	if denied := submit(979, path, s.postCatalogPeriodRule, input); denied.Code != 403 {
		t.Fatalf("out-of-scope creation: %d %s", denied.Code, denied.Body.String())
	}
	if duplicate := submit(980, path, s.postCatalogPeriodRule, input); duplicate.Code != 409 {
		t.Fatalf("duplicate aggregate package: %d %s", duplicate.Code, duplicate.Body.String())
	}

	if _, err := db.Exec(ctx, `WITH payment AS (INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
	 VALUES('DCAI','103','','ai_vip_order:103','1','ORDER_PAID','2026-10-01T00:00Z','2026-10-01T00:00Z',
	 '{"domain":"VIP","sourceKey":"new","paymentId":"103","amountMinor":70000}'),
	 ('DCAI','104','','ai_vip_order:104','1','ORDER_PAID','2026-10-04T00:00Z','2026-10-04T00:00Z',
	 '{"domain":"VIP","sourceKey":"new","paymentId":"104","amountMinor":4000}'),
	 ('DCAI','105','','ai_vip_order:105','1','ORDER_PAID','2026-10-04T00:00Z','2026-10-04T00:00Z',
	 '{"domain":"VIP","sourceKey":"new","paymentId":"105","amountMinor":90000}') RETURNING id,order_id)
	 INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier,sales_claims)
	 SELECT id,'VIP_ORDER','first','vip',jsonb_build_array(jsonb_build_object('role','PID_USER','user',
	  CASE order_id WHEN '103' THEN 'other' WHEN '104' THEN 'direct' ELSE 'unmapped-seller' END,
	  'company',CASE order_id WHEN '105' THEN 'outside' ELSE 'company' END,'relationship','DIRECT')) FROM payment`); err != nil {
		t.Fatal(err)
	}
	scopedInput := strings.TrimSuffix(strings.Replace(input, "新整期规则", "销售一部负责人", 1), "}") + `,
	 "performanceScope":{"unitIds":["10000000-0000-4000-8000-000000000982","10000000-0000-4000-8000-000000000984","10000000-0000-4000-8000-000000000985"],
	 "appointment":{"personId":9801,"unitId":"10000000-0000-4000-8000-000000000982","role":"TEAM_LEAD"}}}`
	w = submit(980, path, s.postCatalogPeriodRule, scopedInput)
	if err := json.Unmarshal(w.Body.Bytes(), &published); err != nil || w.Code != 200 {
		t.Fatalf("scoped aggregate rule did not publish: %d %s %v", w.Code, w.Body.String(), err)
	}
	trial, err = s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: published.ID, Month: "2026-10"})
	if err != nil {
		t.Fatal(err)
	}
	scoped := trial.(draftSummary)
	if scoped.Total != 2800 || scoped.LineCount != 2 || scoped.IssueCount != 0 {
		t.Fatalf("count the seller's in-company team at appointment start and the direct team once each, without blocking on an unrelated seller: %+v", scoped)
	}
	for _, sql := range []string{
		`UPDATE products SET settlement_enabled=true WHERE id=980`,
		`UPDATE source_write_state SET provider_key=$1 WHERE source='DCAI'`,
		`INSERT INTO dcai_order_sync(stream,source_key,initial_from,next_updated_at,next_id,completed_through,last_success_at)
		 VALUES('VIP_ORDERS',$1,'2026-09-01','2026-09-01','0',now(),now())`,
	} {
		var args []any
		if strings.Contains(sql, "$1") {
			args = append(args, s.dcaiSourceKey())
		}
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	w = httptest.NewRecorder()
	reviewURL := fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", scoped.PeriodID, scoped.Version)
	s.getSettlementReview(w, httptest.NewRequest(http.MethodGet, reviewURL, nil))
	var review settlementReview
	if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil || w.Code != 200 || review.Token == "" {
		t.Fatalf("scoped trial should be available to confirm: %d %s %v", w.Code, w.Body.String(), err)
	}
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: scoped.PeriodID,
		ExpectedVersion: scoped.Version, ReviewToken: review.Token}, "fixture"); err != nil {
		t.Fatal(err)
	}
	clear := fmt.Sprintf(`{"baseId":%q,"effectiveFrom":"2026-10-25T00:00:00+08:00","performanceScope":{}}`, published.ID)
	w = submit(980, path, s.postCatalogPeriodRule, clear)
	var later catalogPublication
	if err := json.Unmarshal(w.Body.Bytes(), &later); err != nil || w.Code != 200 || later.Version != 2 {
		t.Fatalf("removing the scope must publish a new version: %d %s %v", w.Code, w.Body.String(), err)
	}
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, shanghai)
	refundMinor := int64(5000)
	refund := sourceFact{Source: "DCAI", Domain: "VIP", SourceKey: "new", Kind: "REFUND_SUCCEEDED",
		OrderID: "101", RecordKey: "ai_vip_order:refund-101", Version: "1", RefundID: "refund-101",
		AmountMinor: &refundMinor, OccurredAt: at, UpdatedAt: at, RefundsPayment: []sourcePaymentReference{{OrderID: "101", RecordID: "101"}}}
	outsideScope := refund
	outsideAmount := int64(4000)
	outsideScope.OrderID, outsideScope.RecordKey, outsideScope.RefundID = "102", "ai_vip_order:refund-102", "refund-102"
	outsideScope.AmountMinor = &outsideAmount
	outsideScope.RefundsPayment = []sourcePaymentReference{{OrderID: "102", RecordID: "102"}}
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE organization_assignments SET unit_id=CASE WHEN valid_to IS NOT NULL
		 THEN '10000000-0000-4000-8000-000000000983'::uuid ELSE '10000000-0000-4000-8000-000000000984'::uuid END
		 WHERE person_id=9802 AND kind='MEMBER'`); err != nil {
			return err
		}
		_, err := storeSourceFactsFrom(ctx, tx, []sourceFact{refund, outsideScope}, s.dcaiSourceKey())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.getCatalogPeriodActivity(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/catalog-period-activity?ruleId="+later.ID+"&month=2026-11", nil))
	var activity catalogPeriodActivity
	if err := json.Unmarshal(w.Body.Bytes(), &activity); err != nil || w.Code != 200 ||
		activity.Refunds != 5000 || activity.Grouping == nil || len(activity.Grouping.Groups) != 1 || activity.Grouping.Groups[0].Version != 1 {
		t.Fatalf("activity before opening the next draft must use the original scoped refund: %d %s %v", w.Code, w.Body.String(), err)
	}
	trial, err = s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: later.ID, Month: "2026-11"})
	if err != nil || trial.(draftSummary).IssueCount != 0 {
		t.Fatalf("refund must follow frozen performance despite the later organization correction: %+v %v", trial, err)
	}
	var ruleID string
	var refunds, carry int64
	if err := db.QueryRow(ctx, `SELECT rule_id::text,refunds_minor,carry_out_minor FROM catalog_aggregate_draft_groups
	 WHERE period_id=$1`, trial.(draftSummary).PeriodID).Scan(&ruleID, &refunds, &carry); err != nil ||
		ruleID != published.ID || refunds != 5000 || carry != -5000 {
		t.Fatalf("refund must stay with its original group and version, got %s/%d/%d: %v", ruleID, refunds, carry, err)
	}
	refundDraft := trial.(draftSummary)
	if _, err := db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`,
		refundDraft.PeriodID, refundDraft.Version); err != nil {
		t.Fatal(err)
	}

	const resumePath = path + "/resume"
	var resume http.HandlerFunc
	for _, route := range s.routes() {
		if route.Path == resumePath && route.Method == http.MethodPost {
			resume = route.Handler
			break
		}
	}
	if resume == nil {
		t.Fatal("a paused aggregate package needs a resume action")
	}
	if _, err := db.Exec(ctx, `UPDATE product_rule_packages SET paused=true WHERE source_id=(SELECT package_id FROM product_rule_versions WHERE source_id=$1)`, later.ID); err != nil {
		t.Fatal(err)
	}
	at = firstCatalogPeriodChange(time.Now(), "MONTHLY_25_TO_24").AddDate(0, 2, 0)
	resumeInput := func(id string, from time.Time) string {
		return fmt.Sprintf(`{"ruleId":%q,"effectiveFrom":%q}`, id, from.Format(time.RFC3339))
	}
	if rejected := submit(979, resumePath, resume, resumeInput(later.ID, at)); rejected.Code != 403 {
		t.Fatalf("a different product grant cannot resume a package: %d %s", rejected.Code, rejected.Body.String())
	}
	if rejected := submit(980, resumePath, resume, resumeInput(later.ID, at.AddDate(0, 0, 1))); rejected.Code != 400 {
		t.Fatalf("resume must use the start of a complete period: %d %s", rejected.Code, rejected.Body.String())
	}
	if rejected := submit(980, resumePath, resume, resumeInput(published.ID, at)); rejected.Code != 409 {
		t.Fatalf("resume must check the version effective at the requested date: %d %s", rejected.Code, rejected.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE organization_units SET status='INACTIVE' WHERE id='10000000-0000-4000-8000-000000000981'`); err != nil {
		t.Fatal(err)
	}
	if rejected := submit(980, resumePath, resume, resumeInput(later.ID, at)); rejected.Code != 409 || !strings.Contains(rejected.Body.String(), "公司安排") {
		t.Fatalf("an inactive payee must identify the blocked arrangement: %d %s", rejected.Code, rejected.Body.String())
	}
	if _, err := db.Exec(ctx, `UPDATE organization_units SET status='ACTIVE' WHERE id='10000000-0000-4000-8000-000000000981'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if resumed := submit(980, resumePath, resume, resumeInput(later.ID, at)); resumed.Code != 200 {
			t.Fatalf("an authorized resume can be retried for the same date: %d %s", resumed.Code, resumed.Body.String())
		}
	}
	var paused bool
	var enabledAt time.Time
	var frozenVersion int64
	if err := db.QueryRow(ctx, `SELECT p.paused,p.resume_from,s.draft_version FROM product_rule_packages p
	 JOIN periods m ON m.aggregate_package_id=p.source_id JOIN settlements s ON s.period_id=m.id WHERE m.id=$1`, scoped.PeriodID).
		Scan(&paused, &enabledAt, &frozenVersion); err != nil || paused || !enabledAt.Equal(at) || frozenVersion != scoped.Version {
		t.Fatalf("resume should set the new date while preserving the closed draft: %v %s %d %v", paused, enabledAt, frozenVersion, err)
	}
	var archived productRulePackageConfig
	if err := db.QueryRow(ctx, `SELECT p.source_id::text,p.code,p.name,p.project_code,p.policy_kind,p.scope_type,p.scope_id
	 FROM product_rule_packages p JOIN product_rule_versions v ON v.package_id=p.source_id WHERE v.source_id=$1`, later.ID).
		Scan(&archived.SourceID, &archived.Code, &archived.Name, &archived.Project, &archived.Kind, &archived.ScopeType, &archived.ScopeID); err != nil {
		t.Fatal(err)
	}
	archived.Paused = true
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		return storeCatalogRulePackage(ctx, tx, 980, time.Now(), archived)
	}); err != nil {
		t.Fatal(err)
	}
	var resumed catalogPeriodVersion
	resumed, err = readCatalogPeriodVersion(ctx, db, later.ID)
	if err != nil || resumed.Paused || resumed.ResumeFrom == nil || !resumed.ResumeFrom.Equal(at) {
		t.Fatalf("reimporting an archived pause must retain the locally scheduled execution: %+v %v", resumed, err)
	}
	w = httptest.NewRecorder()
	s.getCatalogPeriodActivity(w, httptest.NewRequest(http.MethodGet, "/api/v1/catalog-period-activity?ruleId="+later.ID+
		"&month="+at.In(shanghai).AddDate(0, 1, 0).Format("2006-01"), nil))
	activity = catalogPeriodActivity{}
	if err := json.Unmarshal(w.Body.Bytes(), &activity); err != nil || w.Code != 200 ||
		activity.Grouping == nil || activity.Grouping.PriorUnsettled || len(activity.Grouping.Groups) != 1 ||
		activity.Grouping.Groups[0].quote().CarryForward != -5000 {
		t.Fatalf("the resumed period should recover the latest frozen carry even across paused months: %d %s %v", w.Code, w.Body.String(), err)
	}
}
