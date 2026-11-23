package main

import (
	"context"
	"encoding/json"
	"errors"
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
		 ('10000000-0000-4000-8000-000000000988','10000000-0000-4000-8000-000000000982','TEAM','并行销售组','ACTIVE'),
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
		 (gen_random_uuid(),9802,'10000000-0000-4000-8000-000000000988','MEMBER','2026-09-01','2026-10-03T00:00Z'),
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
	actual := submit(0, "/api/v1/catalog-period-activity", s.postCatalogPeriodActivity, input)
	var actualReport catalogPeriodActivity
	if err := json.Unmarshal(actual.Body.Bytes(), &actualReport); err != nil || actual.Code != 200 ||
		actualReport.Receipts != 40000 || actualReport.Grouping == nil || len(actualReport.Grouping.Groups) != 2 ||
		actualReport.Grouping.Groups[0].quote().Total+actualReport.Grouping.Groups[1].quote().Total != 8000 {
		t.Fatalf("new definition should group both real inputs without publishing: %d %s %v", actual.Code, actual.Body.String(), err)
	}
	var activityResponse struct {
		Grouping struct {
			Groups []struct{ ID, EventToken string }
		}
	}
	if err := json.Unmarshal(actual.Body.Bytes(), &activityResponse); err != nil ||
		len(activityResponse.Grouping.Groups) != 2 || len(activityResponse.Grouping.Groups[0].EventToken) != 64 {
		t.Fatalf("each proposed group with accepted payments needs an order drilldown: %d %s %v", actual.Code, actual.Body, err)
	}
	firstGroup := activityResponse.Grouping.Groups[0]
	detailPath := "/api/v1/catalog-period-activity?group=" + firstGroup.ID + "&eventToken=" + firstGroup.EventToken
	detail := submit(0, detailPath, s.postCatalogPeriodActivity, input)
	var eventPage struct {
		Total int
		Items []struct {
			OrderID, Kind string
			AmountMinor   int64
		}
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &eventPage); err != nil || detail.Code != 200 || eventPage.Total != 2 ||
		len(eventPage.Items) != 2 || eventPage.Items[0].OrderID != "101" || eventPage.Items[1].OrderID != "102" ||
		eventPage.Items[0].Kind != "ORDER_PAID" || eventPage.Items[0].AmountMinor+eventPage.Items[1].AmountMinor != 40000 {
		t.Fatalf("drilldown must use the same unsaved rule's group across requests: %d %s %v", detail.Code, detail.Body, err)
	}
	stale := submit(0, "/api/v1/catalog-period-activity?group="+firstGroup.ID+"&eventToken="+strings.Repeat("0", 64),
		s.postCatalogPeriodActivity, input)
	if stale.Code != 409 {
		t.Fatalf("an outdated list of trial events must be rejected: %d %s", stale.Code, stale.Body)
	}
	nextPage := submit(0, detailPath+"&page=2", s.postCatalogPeriodActivity, input)
	if err := json.Unmarshal(nextPage.Body.Bytes(), &eventPage); err != nil || nextPage.Code != 200 ||
		eventPage.Total != 2 || len(eventPage.Items) != 0 {
		t.Fatalf("drilldown must page the event list without changing its count: %d %s %v", nextPage.Code, nextPage.Body, err)
	}
	var writes int
	if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM product_rule_packages WHERE product_id=980)
		+(SELECT count(*) FROM periods WHERE product_id=980)`).Scan(&writes); err != nil || writes != 0 {
		t.Fatalf("order trial must not persist a rule or period: %d %v", writes, err)
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
	t.Run("independent scopes keep one source event", func(t *testing.T) {
		const proposal = `{"create":{"productId":980,"label":"独立收款范围","cycle":"MONTHLY_25_TO_24"},
		 "effectiveFrom":"2026-09-25T00:00:00+08:00","scenarioCodes":["SECOND"],"components":[
		 {"label":"销售提成","targetKind":"person","personId":9802,"ownSales":true,"tiers":[{"rate_bps":500}]},
		 {"label":"团队负责人提成","targetKind":"person","personId":9802,"salesScopeUnitIds":["10000000-0000-4000-8000-000000000983"],"tiers":[{"rate_bps":200}]}]}`
		trial := submit(980, "/api/v1/catalog-period-activity", s.postCatalogPeriodActivity, proposal)
		var activity catalogPeriodActivity
		if err := json.Unmarshal(trial.Body.Bytes(), &activity); err != nil || trial.Code != 200 ||
			activity.Receipts != 30000 || activity.Grouping == nil || len(activity.Grouping.Groups) != 2 {
			t.Fatalf("one 30000 receipt and two scopes: %d %s %v", trial.Code, trial.Body, err)
		}
		reply := submit(980, path, s.postCatalogPeriodRule, proposal)
		var rule catalogPublication
		if err := json.Unmarshal(reply.Body.Bytes(), &rule); err != nil || reply.Code != 200 {
			t.Fatalf("publish scoped rule: %d %s %v", reply.Code, reply.Body, err)
		}
		result, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: rule.ID, Month: "2026-10"})
		if err != nil {
			t.Fatal(err)
		}
		draft := result.(draftSummary)
		if draft.Total != 2100 || draft.LineCount != 2 || draft.IssueCount != 0 {
			t.Fatalf("scoped draft: %+v", draft)
		}
		var events, memberships int
		err = db.QueryRow(ctx, `SELECT
		 (SELECT count(*) FROM catalog_aggregate_draft_events WHERE period_id=$1),
		 (SELECT count(*) FROM catalog_aggregate_event_groups WHERE period_id=$1)`, draft.PeriodID).Scan(&events, &memberships)
		if err != nil || events != 1 || memberships != 2 {
			t.Fatalf("events=%d memberships=%d: %v", events, memberships, err)
		}
	})

	t.Run("unrestricted scope resolves the payment-time company appointment", func(t *testing.T) {
		if _, err := db.Exec(ctx, `INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,valid_to,observed_at)
		 VALUES (gen_random_uuid(),'DCAI',9801,'TEAM_LEAD','ORGANIZATION_UNIT','10000000-0000-4000-8000-000000000981',
		   '2026-09-25T00:00Z','2026-10-03T00:00Z','2026-11-01'),
		 (gen_random_uuid(),'DCAI',9803,'TEAM_LEAD','ORGANIZATION_UNIT','10000000-0000-4000-8000-000000000981',
		   '2026-10-03T00:00Z',NULL,'2026-11-01')`); err != nil {
			t.Fatal(err)
		}
		const proposal = `{"create":{"productId":980,"label":"全账期公司在任负责人","cycle":"MONTHLY_25_TO_24"},
		 "effectiveFrom":"2026-09-25T00:00:00+08:00","scenarioCodes":["FIRST","SECOND"],"components":[
		 {"label":"分润","targetKind":"appointment","appointmentLevel":"COMPANY","appointmentRole":"TEAM_LEAD","tiers":[{"rate_bps":1000}]},
		 {"label":"固定补贴","targetKind":"appointment","appointmentLevel":"COMPANY","appointmentRole":"TEAM_LEAD","fixedMinor":100000}]}`
		trial := submit(980, "/api/v1/catalog-period-activity", s.postCatalogPeriodActivity, proposal)
		var activity catalogPeriodActivity
		if err := json.Unmarshal(trial.Body.Bytes(), &activity); err != nil || trial.Code != 200 ||
			activity.Receipts != 40000 || activity.Grouping == nil || len(activity.Grouping.Groups) != 4 || len(activity.Grouping.Issues) != 0 {
			t.Fatalf("expected four award groups using the pre- and post-transfer holders: %d %s %v", trial.Code, trial.Body, err)
		}
		reply := submit(980, path, s.postCatalogPeriodRule, proposal)
		var rule catalogPublication
		if err := json.Unmarshal(reply.Body.Bytes(), &rule); err != nil || reply.Code != 200 {
			t.Fatalf("publish unrestricted rule: %d %s %v", reply.Code, reply.Body, err)
		}
		value, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: rule.ID, Month: "2026-10"})
		if err != nil {
			t.Fatal(err)
		}
		draft := value.(draftSummary)
		if draft.LineCount != 4 || draft.Total != 204000 || draft.IssueCount != 0 {
			t.Fatalf("unexpected draft: %+v", draft)
		}
		var amounts map[int64]int64
		if err := db.QueryRow(ctx, `SELECT jsonb_object_agg(person_id,total) FROM
		 (SELECT person_id,sum(amount_minor) AS total FROM calculation_draft_lines WHERE period_id=$1 GROUP BY person_id) awards`,
			draft.PeriodID).Scan(&amounts); err != nil || len(amounts) != 2 || amounts[9801] != 101000 || amounts[9803] != 103000 {
			t.Fatalf("each order must use the holder at the time of payment: %v %v", amounts, err)
		}
	})

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
	scopedPreview := submit(980, "/api/v1/catalog-period-activity", s.postCatalogPeriodActivity, scopedInput)
	var reconciliation catalogPeriodActivity
	if err := json.Unmarshal(scopedPreview.Body.Bytes(), &reconciliation); err != nil || scopedPreview.Code != 200 ||
		reconciliation.Receipts != 14000 || reconciliation.OutsideScope == nil ||
		*reconciliation.OutsideScope != (catalogPeriodScopeExclusions{Payments: 3, ReceiptsMinor: 190000}) {
		t.Fatalf("scope preview must reconcile included and excluded payments: %d %s %v", scopedPreview.Code, scopedPreview.Body.String(), err)
	}
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
	if _, err := db.Exec(ctx, `UPDATE catalog_sales_context SET appointments_at='2026-11-02T00:00Z' WHERE source='DCAI'`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.getCatalogPeriodActivity(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/catalog-period-activity?ruleId="+published.ID+"&month=2026-10", nil))
	var missingAppointment catalogPeriodActivity
	if err := json.Unmarshal(w.Body.Bytes(), &missingAppointment); err != nil || w.Code != 200 ||
		missingAppointment.OutsideScope != nil || missingAppointment.Issues["AGGREGATE_PERFORMANCE_APPOINTMENT_MISSING"] != 5 {
		t.Fatalf("a missing appointment history must be reported separately from a real out-of-scope payment: %d %s %v", w.Code, w.Body.String(), err)
	}
	trial, err = s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: published.ID, Month: "2026-10"})
	if err != nil || trial.(draftSummary).IssueCount != 5 || trial.(draftSummary).Total != 0 {
		t.Fatalf("missing appointment history must prevent settlement instead of producing an empty valid draft: %+v %v", trial, err)
	}
	if _, err := db.Exec(ctx, `UPDATE products SET settlement_enabled=true WHERE id=980`); err != nil {
		t.Fatal(err)
	}
	blocked := trial.(draftSummary)
	if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: blocked.PeriodID, ExpectedVersion: blocked.Version},
		"fixture"); !errors.Is(err, errSettlementPending) {
		t.Fatalf("the unresolved appointment must prevent confirmation: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE catalog_sales_context SET appointments_at=NULL WHERE source='DCAI'`); err != nil {
		t.Fatal(err)
	}
	trial, err = s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: published.ID, Month: "2026-10"})
	if err != nil || trial.(draftSummary).Total != 2800 || trial.(draftSummary).IssueCount != 0 {
		t.Fatalf("restored appointment history should restore the original trial: %+v %v", trial, err)
	}
	scoped = trial.(draftSummary)
	for _, tc := range []struct {
		label, selection string
		wantTotal        int64
		wantIssues       int
	}{
		{"仅销售一组", `"10000000-0000-4000-8000-000000000984"`, 0, 1},
		{"两个并行组", `"10000000-0000-4000-8000-000000000984","10000000-0000-4000-8000-000000000988"`, 2000, 0},
		{"只算直属组", `"10000000-0000-4000-8000-000000000985"`, 800, 0},
	} {
		body := strings.Replace(scopedInput, "销售一部负责人", tc.label, 1)
		body = strings.Replace(body, `"10000000-0000-4000-8000-000000000982","10000000-0000-4000-8000-000000000984","10000000-0000-4000-8000-000000000985"`, tc.selection, 1)
		versionResponse := submit(980, path, s.postCatalogPeriodRule, body)
		var version catalogPublication
		if err := json.Unmarshal(versionResponse.Body.Bytes(), &version); err != nil || versionResponse.Code != 200 {
			t.Fatalf("%s scope failed to publish: %d %s %v", tc.label, versionResponse.Code, versionResponse.Body.String(), err)
		}
		value, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: version.ID, Month: "2026-10"})
		if err != nil {
			t.Fatal(err)
		}
		actual := value.(draftSummary)
		if actual.Total != tc.wantTotal || actual.IssueCount != tc.wantIssues {
			t.Fatalf("%s must include, exclude or flag the concurrent group based on selected units: %+v", tc.label, actual)
		}
	}
	for _, sql := range []string{
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
	var scopeLine int64
	if err := db.QueryRow(ctx, `SELECT min(id) FROM calculation_draft_lines WHERE period_id=$1`, scoped.PeriodID).Scan(&scopeLine); err != nil {
		t.Fatal(err)
	}
	applied := httptest.NewRecorder()
	s.getDraftRule(applied, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/drafts/rule?periodId=%d&rowId=%d&section=lines", scoped.PeriodID, scopeLine), nil))
	var historical draftRuleDetails
	if err := json.Unmarshal(applied.Body.Bytes(), &historical); err != nil || applied.Code != 200 || historical.Aggregate == nil {
		t.Fatalf("open applied period rule: %d %s %v", applied.Code, applied.Body, err)
	}
	history := historical.Aggregate
	if !history.Performance.active() || history.ScopeLabels == nil ||
		len(history.ScopeLabels.Units) != 3 || history.ScopeLabels.Person != "整期个人收款" ||
		len(history.Scenarios) != 2 || history.Scenarios[0].Label != "会员" || history.MayPublish {
		t.Fatalf("applied period rule must include shared filters and readable SKU labels: %s", applied.Body)
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
		if _, err := tx.Exec(ctx, `UPDATE catalog_sales_context SET appointments_at='2026-11-02T00:00Z' WHERE source='DCAI'`); err != nil {
			return err
		}
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
		activity.Refunds != 5000 || activity.Grouping == nil || len(activity.Grouping.Groups) != 2 ||
		activity.Grouping.Groups[0].Version != 1 || activity.Grouping.Groups[1].Version != 1 {
		t.Fatalf("activity before opening the next draft must use the original scoped refund: %d %s %v", w.Code, w.Body.String(), err)
	}
	if activity.OutsideScope == nil || *activity.OutsideScope != (catalogPeriodScopeExclusions{Refunds: 1, RefundsMinor: 4000}) {
		t.Fatalf("the refund of an excluded original payment should stay outside the scope: %s", w.Body.String())
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
		activity.Grouping == nil || activity.Grouping.PriorUnsettled || len(activity.Grouping.Groups) != 2 ||
		activity.Grouping.Groups[0].quote().CarryForward != -5000 || activity.Grouping.Groups[1].quote().CarryForward != -5000 {
		t.Fatalf("the resumed period should recover the latest frozen carry even across paused months: %d %s %v", w.Code, w.Body.String(), err)
	}
	t.Run("fixed pool uses the closing owner", func(t *testing.T) {
		const pool = "10000000-0000-4000-8000-000000000983"
		var appointmentSnapshot time.Time
		if err := db.QueryRow(ctx, `SELECT coalesce(appointments_at,captured_at) FROM catalog_sales_context WHERE source='DCAI'`).Scan(&appointmentSnapshot); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
		 VALUES(gen_random_uuid(),9801,$1,'MEMBER','2026-09-01'),(gen_random_uuid(),9803,$1,'MEMBER','2026-09-01'),
		  (gen_random_uuid(),9804,$1,'MEMBER','2026-09-01')`, pool); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,valid_to,observed_at)
		 VALUES(gen_random_uuid(),'DCAI',9801,'PROJECT_OWNER','ORGANIZATION_UNIT',$1,'2026-09-01','2026-10-10',$2),
		  (gen_random_uuid(),'DCAI',9803,'PROJECT_OWNER','ORGANIZATION_UNIT',$1,'2026-10-10','2026-10-25',$2),
		  (gen_random_uuid(),'DCAI',9801,'PROJECT_OWNER','ORGANIZATION_UNIT',$1,'2026-10-25',NULL,$2),
		  (gen_random_uuid(),'DCAI',9804,'DELIVERY_TEAM','ORGANIZATION_UNIT',$1,'2026-09-01',NULL,$2)`, pool, appointmentSnapshot); err != nil {
			t.Fatal(err)
		}
		registered, err := s.createCatalogTeamPool(ctx, createPracticePoolInput{ProductID: 980, UnitID: pool, Name: "固定池"})
		if err != nil {
			t.Fatal(err)
		}
		poolID := registered.(teamPool).ID
		fixedInput := fmt.Sprintf(`{"create":{"productId":980,"label":"固定规则","cycle":"MONTHLY_25_TO_24"},
		 "effectiveFrom":"2026-09-25T00:00:00+08:00","scenarioCodes":["SECOND"],"components":[
		 {"label":"池比例计提","targetKind":"pool","poolId":%d,
		  "ownerPersonId":9801,"memberRole":"DELIVERY_TEAM","tiers":[{"rate_bps":1500}]},
		 {"label":"每期固定池","targetKind":"pool","poolId":%d,
		  "ownerRole":"PROJECT_OWNER","memberRole":"DELIVERY_TEAM","fixedMinor":5000}]}`, poolID, poolID)
		manual := submit(980, path+"/preview", s.postCatalogPeriodQuote, strings.TrimSuffix(fixedInput, "}")+`,"receiptsMinor":123}`)
		var fixedQuote catalogPeriodQuote
		if err := json.Unmarshal(manual.Body.Bytes(), &fixedQuote); err != nil || manual.Code != 200 || len(fixedQuote.Awards) != 2 ||
			!fixedQuote.Awards[1].Fixed || fixedQuote.Awards[1].Amount != 5000 {
			t.Fatalf("manual quote should use the configured amount on positive net receipts: %d %s %v", manual.Code, manual.Body, err)
		}
		proposed := submit(980, "/api/v1/catalog-period-activity", s.postCatalogPeriodActivity, fixedInput)
		var fixedReport catalogPeriodActivity
		if err := json.Unmarshal(proposed.Body.Bytes(), &fixedReport); err != nil || proposed.Code != 200 ||
			fixedReport.Grouping == nil || len(fixedReport.Grouping.Groups) != 2 || !fixedReport.Grouping.Groups[1].FixedOnly ||
			fixedReport.Grouping.Groups[1].Targets[0].OwnerName != "另一组销售" {
			t.Fatalf("order trial should use the incoming owner even though the payment preceded the handoff: %d %s %v", proposed.Code, proposed.Body, err)
		}
		explicitInput := strings.Replace(fixedInput, `"ownerRole":"PROJECT_OWNER"`, `"ownerPersonId":9801`, 1)
		explicit := submit(980, "/api/v1/catalog-period-activity", s.postCatalogPeriodActivity, explicitInput)
		var namedReport catalogPeriodActivity
		if err := json.Unmarshal(explicit.Body.Bytes(), &namedReport); err != nil || explicit.Code != 200 ||
			namedReport.Grouping == nil || len(namedReport.Grouping.Groups) != 2 ||
			len(namedReport.Grouping.Groups[1].Targets) != 1 || namedReport.Grouping.Groups[1].Targets[0].OwnerName != "整期个人收款" ||
			namedReport.Grouping.Groups[1].quote().Total != 5000 {
			t.Fatalf("a designated pool owner should receive the whole fixed award after the role changes: %d %s %v", explicit.Code, explicit.Body, err)
		}
		created := submit(980, path, s.postCatalogPeriodRule, fixedInput)
		var publishedFixed catalogPublication
		if err := json.Unmarshal(created.Body.Bytes(), &publishedFixed); err != nil || created.Code != 200 {
			t.Fatalf("fixed amount publication: %d %s %v", created.Code, created.Body, err)
		}
		opened, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: publishedFixed.ID, Month: "2026-10"})
		if err != nil || opened.(draftSummary).Total != 9500 || opened.(draftSummary).LineCount != 2 || opened.(draftSummary).IssueCount != 0 {
			t.Fatalf("both independent arrangements for the same pool must survive publication: %+v %v", opened, err)
		}
		draft := opened.(draftSummary)
		var owner, fixedAmount int64
		var fixedGroup string
		if err := db.QueryRow(ctx, `SELECT l.allocation_owner_id,l.amount_minor,g.group_key
		 FROM calculation_draft_lines l JOIN catalog_aggregate_draft_groups g
		  ON (g.period_id,g.group_key)=(l.period_id,l.aggregate_group_key)
		 WHERE l.period_id=$1 AND g.fixed_only AND l.source_version_id IS NULL`, draft.PeriodID).Scan(&owner, &fixedAmount, &fixedGroup); err != nil ||
			owner != 9803 || fixedAmount != 5000 {
			t.Fatalf("formal draft must identify the closing owner and the full amount: %d %d %v", owner, fixedAmount, err)
		}
		var groupEvents struct{ Total int }
		if err := db.QueryRow(ctx, catalogAggregateDraftEventsSQL, draft.PeriodID, draft.Version, fixedGroup, 0).Scan(&groupEvents); err != nil || groupEvents.Total != 1 {
			t.Fatalf("fixed award should trace existing payment versions without copying them: %+v %v", groupEvents, err)
		}
		poolResponse := httptest.NewRecorder()
		s.draftPools(poolResponse, httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/v1/draft-pools?periodId=%d&version=%d", draft.PeriodID, draft.Version), nil))
		var pools poolDraftReport
		if err := json.Unmarshal(poolResponse.Body.Bytes(), &pools); err != nil || poolResponse.Code != 200 ||
			len(pools.Items) != 1 || pools.Items[0].Balance != 9500 || len(pools.Items[0].Members) != 1 ||
			pools.Items[0].Scenario != "team-pool" || pools.Items[0].Item != fmt.Sprint(poolID) ||
			pools.Items[0].Members[0].ID != 9804 {
			t.Fatalf("the two arrangements must fund one pool with the selected members: %d %s %v", poolResponse.Code, poolResponse.Body, err)
		}
		if _, err := s.savePoolAllocation(ctx, savePoolAllocationInput{PeriodID: draft.PeriodID, Version: draft.Version,
			Basis: pools.Basis, poolScope: pools.Items[0].poolScope, Shares: []poolAllocationShare{{PersonID: 9804, Amount: 9500}}}); err != nil {
			t.Fatal(err)
		}
		reviewResponse := httptest.NewRecorder()
		s.getSettlementReview(reviewResponse, httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/api/v1/settlement-review?periodId=%d&version=%d", draft.PeriodID, draft.Version), nil))
		var checked settlementReview
		if err := json.Unmarshal(reviewResponse.Body.Bytes(), &checked); err != nil || reviewResponse.Code != 200 || checked.Token == "" {
			t.Fatalf("the fixed award should be ready for settlement: %d %s %v", reviewResponse.Code, reviewResponse.Body, err)
		}
		for range 2 {
			if _, err := s.confirmSettlement(ctx, confirmSettlementInput{PeriodID: draft.PeriodID, ExpectedVersion: draft.Version,
				ReviewToken: checked.Token}, "fixture"); err != nil {
				t.Fatal(err)
			}
		}
		empty, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: publishedFixed.ID, Month: "2026-11"})
		if err != nil || empty.(draftSummary).Total != 0 || empty.(draftSummary).LineCount != 0 {
			t.Fatalf("a subsequent cycle without receipts cannot generate a fixed payout: %+v %v", empty, err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
		 VALUES('DCAI','102','','refund:fixed-102','1','REFUND_SUCCEEDED','2026-11-05',
		  '{"domain":"VIP","sourceKey":"new","refundId":"fixed-102","amountMinor":26000,"refundsPayment":[{"orderId":"102","recordId":"102"}]}',
		  '2026-11-05')`); err != nil {
			t.Fatal(err)
		}
		laterDraft, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: publishedFixed.ID, Month: "2026-11"})
		if err != nil || laterDraft.(draftSummary).Total != 0 || laterDraft.(draftSummary).IssueCount != 0 {
			t.Fatalf("the later refund only reduces that cycle's net receipts: %+v %v", laterDraft, err)
		}
		var refunds, carry, closedAward, sourceRefunds int64
		if err := db.QueryRow(ctx, `SELECT coalesce(sum(refunds_minor),0)::bigint,coalesce(sum(carry_out_minor),0)::bigint,
		  (SELECT amount_minor FROM calculation_draft_lines WHERE period_id=$2 AND allocation_owner_id=$3),
		  (SELECT count(*) FROM catalog_aggregate_draft_events WHERE period_id=$1 AND source_version_id<>payment_version_id)
		 FROM catalog_aggregate_draft_groups WHERE period_id=$1`,
			laterDraft.(draftSummary).PeriodID, draft.PeriodID, owner).Scan(&refunds, &carry, &closedAward, &sourceRefunds); err != nil ||
			refunds != 60000 || carry != -60000 || closedAward != 5000 || sourceRefunds != 2 {
			t.Fatalf("each refund is recorded once and applied to both arrangements while the original award stays booked: %d %d %d %d %v", refunds, carry, closedAward, sourceRefunds, err)
		}
		activityURL := "/api/v1/catalog-period-activity?ruleId=" + publishedFixed.ID + "&month=2026-11"
		refundReport := httptest.NewRecorder()
		s.getCatalogPeriodActivity(refundReport, httptest.NewRequest(http.MethodGet, activityURL, nil))
		var current catalogPeriodActivity
		if err := json.Unmarshal(refundReport.Body.Bytes(), &current); err != nil || refundReport.Code != 200 || current.Grouping == nil ||
			len(current.Grouping.Groups) != 2 {
			t.Fatalf("read refund activity: %d %s %v", refundReport.Code, refundReport.Body, err)
		}
		for _, group := range current.Grouping.Groups {
			details := httptest.NewRecorder()
			s.getCatalogPeriodActivity(details, httptest.NewRequest(http.MethodGet, activityURL+"&group="+group.ID+"&eventToken="+group.EventToken, nil))
			if err := json.Unmarshal(details.Body.Bytes(), &eventPage); err != nil || details.Code != 200 || eventPage.Total != 2 ||
				len(eventPage.Items) != 2 || eventPage.Items[0].AmountMinor+eventPage.Items[1].AmountMinor != group.Refunds {
				t.Fatalf("each group's detail must reconcile to its refunds: %d %s %v", details.Code, details.Body, err)
			}
			for _, event := range eventPage.Items {
				if event.Kind != "REFUND_SUCCEEDED" || event.OrderID != "102" {
					t.Fatalf("refund must show the original payment's order: %+v", event)
				}
			}
		}

		rollbackCarryCheck := errors.New("rollback carry fixture")
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			refundDraft := laterDraft.(draftSummary)
			if _, err := insertSettlement(ctx, tx, refundDraft.PeriodID, refundDraft.Version, "carry fixture", "", nil); err != nil {
				return err
			}
			var raw json.RawMessage
			if err := tx.QueryRow(ctx, `SELECT definition FROM product_rule_versions WHERE source_id=$1`, publishedFixed.ID).Scan(&raw); err != nil {
				return err
			}
			definition, err := parseCatalogPeriodRule(raw)
			if err != nil {
				return err
			}
			var fixed catalogPeriodItem
			for _, item := range definition.Items {
				if item.FixedMinor != nil {
					fixed = item
				}
			}
			var previous []catalogGroupTargetKey
			if err := tx.QueryRow(ctx, `SELECT targets FROM catalog_aggregate_draft_groups WHERE period_id=$1 AND fixed_only`,
				refundDraft.PeriodID).Scan(&previous); err != nil {
				return err
			}
			if len(previous) != 1 || previous[0].Owner != 9803 || previous[0].PoolID != poolID {
				return fmt.Errorf("the original refund must retain its frozen pool reference: %+v", previous)
			}
			target := previous[0]
			target.Owner, target.Members = 9801, []int64{9803}
			for _, receipts := range []int64{20000, 40000} {
				incoming := catalogPeriodGroup{ID: "incoming", RuleID: publishedFixed.ID, Receipts: receipts, Net: receipts, FixedOnly: true,
					posting: catalogAggregateGroupInput{Targets: []catalogGroupTargetKey{target}}, items: []catalogPeriodItem{fixed}}
				incoming.BeforeCarry, err = incoming.calculateQuote(0)
				if err != nil {
					return err
				}
				groups, err := applyCatalogAggregateCarry(ctx, tx, refundDraft.PeriodID, 0, []catalogPeriodGroup{incoming},
					[]catalogAggregateRule{{ID: publishedFixed.ID, Definition: definition}}, false)
				if err != nil {
					return err
				}
				wantAward := int64(0)
				if receipts > 30000 {
					wantAward = *fixed.FixedMinor
				}
				got := groups[0]
				if len(groups) != 2 || got.CarryIn != -30000 || got.quote().Total != wantAward ||
					got.quote().CarryForward != min(receipts-30000, 0) || got.posting.Targets[0].Owner != 9801 {
					return fmt.Errorf("the same fixed pool must offset its carry and award the remainder under the incoming owner: %+v", groups)
				}
			}
			return rollbackCarryCheck
		}); !errors.Is(err, rollbackCarryCheck) {
			t.Fatal(err)
		}

		for _, tc := range []struct{ version, receipts, total int64 }{{1, 20000, 0}, {2, 30000, 0}, {3, 40000, 6500}} {
			if _, err := db.Exec(ctx, `WITH payment AS (
			 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
			 VALUES('DCAI','196','','ai_vip_order:196',$1::bigint::text,'ORDER_PAID',
			  '2026-11-06'::timestamptz+$1::bigint*interval '1 minute','2026-11-06',
			  jsonb_build_object('domain','VIP','sourceKey','new','paymentId','196','amountMinor',$2::bigint)) RETURNING id
			 ) INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
			 SELECT id,'VIP_ORDER','second','vip' FROM payment`, tc.version, tc.receipts); err != nil {
				t.Fatal(err)
			}
			opened, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: publishedFixed.ID, Month: "2026-11"})
			if err != nil || opened.(draftSummary).Total != tc.total || opened.(draftSummary).IssueCount != 0 {
				t.Fatalf("the fixed pool awards only on positive current net receipts (%d received against 30000 refunded): %+v %v", tc.receipts, opened, err)
			}
			currentDraft := opened.(draftSummary)
			var gotOwner, gotRefunds, gotCarry int64
			var groupKey string
			if err := db.QueryRow(ctx, `SELECT (targets->0->>'Owner')::bigint,refunds_minor,carry_out_minor,group_key
			 FROM catalog_aggregate_draft_groups WHERE period_id=$1 AND fixed_only AND receipts_minor=$2`,
				currentDraft.PeriodID, tc.receipts).Scan(&gotOwner, &gotRefunds, &gotCarry, &groupKey); err != nil ||
				gotOwner != 9801 || gotRefunds != 30000 || gotCarry != min(tc.receipts-30000, 0) {
				t.Fatalf("current pool must record the current owner and one net refund balance: %d %d %d %v", gotOwner, gotRefunds, gotCarry, err)
			}
			if err := db.QueryRow(ctx, catalogAggregateDraftEventsSQL, currentDraft.PeriodID, currentDraft.Version, groupKey, 0).Scan(&eventPage); err != nil ||
				eventPage.Total != 3 || eventPage.Items[0].OrderID != "102" || eventPage.Items[1].OrderID != "102" || eventPage.Items[2].OrderID != "196" {
				t.Fatalf("the draft group should expose both original refund orders and the current payment: %+v %v", eventPage, err)
			}
		}
		if err := db.QueryRow(ctx, `SELECT allocation_owner_id,amount_minor FROM calculation_draft_lines
		 WHERE period_id=$1 AND aggregate_group_key=$2`, draft.PeriodID, fixedGroup).Scan(&owner, &fixedAmount); err != nil ||
			owner != 9803 || fixedAmount != 5000 {
			t.Fatalf("October's frozen award should retain its original owner and amount: %d %d %v", owner, fixedAmount, err)
		}
	})

	t.Run("shared fixed standards and cumulative refund", func(t *testing.T) {
		if _, err := db.Exec(ctx, `INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
		 SELECT gen_random_uuid(),source,9801,'TEAM_LEAD','ORGANIZATION_UNIT','10000000-0000-4000-8000-000000000981',
		   '2026-09-01',coalesce(appointments_at,captured_at) FROM catalog_sales_context WHERE source='DCAI'`); err != nil {
			t.Fatal(err)
		}
		recordRefund := func(id, date string, amount int64) {
			t.Helper()
			if _, err := db.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
			 VALUES('DCAI','101','','ai_vip_order:'||$1::text,'1','REFUND_SUCCEEDED',$2::timestamptz,
			   jsonb_build_object('domain','VIP','sourceKey','new','refundId',$1::text,'amountMinor',$3::bigint,
			      'refundsPayment','[{"orderId":"101","recordId":"101"}]'::jsonb),$2::timestamptz)`, id, date, amount); err != nil {
				t.Fatal(err)
			}
		}
		recordRefund("fixed-lead-101-1", "2026-10-22", 1000)
		const body = `{"create":{"productId":980,"label":"按订单固定提成","cycle":"MONTHLY_25_TO_24"},
		 "effectiveFrom":"2026-09-25T00:00:00+08:00","scenarioCodes":["FIRST"],"components":[
		 {"label":"订单固定","targetKind":"appointment","appointmentLevel":"COMPANY","appointmentRole":"TEAM_LEAD","fixedMinor":100000,
		  "salesScopeUnitIds":["10000000-0000-4000-8000-000000000981"]}]}`
		w := submit(980, path, s.postCatalogPeriodRule, body)
		var published catalogPublication
		if err := json.Unmarshal(w.Body.Bytes(), &published); err != nil || w.Code != 200 {
			t.Fatalf("publish fixed standard: %d %s %v", w.Code, w.Body, err)
		}
		open := func(month string, want int64, lines int) draftSummary {
			t.Helper()
			result, err := s.openCatalogAggregateDraft(ctx, catalogAggregatePeriodInput{RuleID: published.ID, Month: month})
			if err != nil {
				t.Fatal(err)
			}
			draft := result.(draftSummary)
			if draft.Total != want || draft.LineCount != lines || draft.IssueCount != 0 {
				t.Fatalf("fixed order draft for %s: %+v, want %d", month, draft, want)
			}
			return draft
		}
		original := open("2026-10", 290000, 1)
		if _, err := db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`,
			original.PeriodID, original.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE catalog_sales_appointments SET person_id=9802 WHERE scope_key='10000000-0000-4000-8000-000000000981'`); err != nil {
			t.Fatal(err)
		}
		later := open("2026-11", -50000, 0)
		var person, amount int64
		var ruleID string
		if err := db.QueryRow(ctx, `SELECT l.person_id,r.amount_minor,l.catalog_rule_id::text
		  FROM calculation_draft_refunds r JOIN calculation_draft_lines l ON l.id=r.original_line_id WHERE r.period_id=$1`,
			later.PeriodID).Scan(&person, &amount, &ruleID); err != nil || person != 9801 || amount != -50000 || ruleID != published.ID {
			t.Fatalf("refund must be applied once to the original recipient: %d %d %s %v", person, amount, ruleID, err)
		}
		detail := httptest.NewRecorder()
		s.getDraft(detail, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts?periodId=%d&orderQuery=101", later.PeriodID), nil))
		var visible struct {
			Refunds []struct {
				ID              int64
				AmountMinor     int64
				OriginalOrderID string
			}
		}
		if err := json.Unmarshal(detail.Body.Bytes(), &visible); err != nil || detail.Code != 200 || len(visible.Refunds) != 1 ||
			visible.Refunds[0].OriginalOrderID != "101" || visible.Refunds[0].AmountMinor != -50000 {
			t.Fatalf("refund must be searchable using its original order: %d %s %v", detail.Code, detail.Body, err)
		}
		rule := httptest.NewRecorder()
		s.getDraftRule(rule, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/drafts/rule?periodId=%d&rowId=%d&section=refunds",
			later.PeriodID, visible.Refunds[0].ID), nil))
		var originalRule draftRuleDetails
		if err := json.Unmarshal(rule.Body.Bytes(), &originalRule); err != nil || rule.Code != 200 || originalRule.Aggregate == nil ||
			!originalRule.Aggregate.Items[0].FixedPerOrder || originalRule.Aggregate.ScopeLabels == nil ||
			originalRule.Aggregate.ScopeLabels.Units["10000000-0000-4000-8000-000000000981"] != "整期公司收款" ||
			originalRule.Aggregate.Scenarios[0].Label != "会员" {
			t.Fatalf("refund must open the source version of the aggregate rule: %d %s %v", rule.Code, rule.Body, err)
		}
	})

}
