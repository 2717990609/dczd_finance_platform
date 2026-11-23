package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCatalogPeriodActivityReadsLatestPaidAndOriginalRefundScenario(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	const product = 901
	for _, statement := range []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE
		 VALUES(901,'ACTIVITY','整期数据样本','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		 VALUES(901,gen_random_uuid(),'test','DCAI','DCAI','sample','2026-10-01',decode(repeat('00',32),'hex'))`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,
		  classifier,sku_code,sku_label,mapping_version,effective_from,observed_at,source_product_code) VALUES
		 (gen_random_uuid(),901,'DCAI','VIP','ORDER','vip','sample','VIP','FIRST','会员',1,'2026-01-01','2026-10-01','sample'),
		 (gen_random_uuid(),901,'DCAI','HEALTH_IDENTITY','ORDER','health','sample','HEALTH','SECOND','健康',1,'2026-01-01','2026-10-01','sample'),
		 (gen_random_uuid(),901,'DCAI','VIP','ORDER','vip','sample','EXCLUDED','OTHER','其他',1,'2026-01-01','2026-10-01','sample')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	window, _ := calendarWindow("2026-10", 1)
	insert := func(fact sourceFact, classifier string) {
		t.Helper()
		body, err := json.Marshal(fact)
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := db.QueryRow(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, fact.Source, fact.OrderID, fact.LineID,
			fact.RecordKey, fact.Version, fact.Kind, fact.UpdatedAt, body, fact.OccurredAt).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if classifier != "" {
			if _, err := db.Exec(ctx, `INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
			 VALUES($1,'ORDER','sample',$2)`, id, classifier); err != nil {
				t.Fatal(err)
			}
		}
	}
	payment := func(id, domain, table string, amount int64, when time.Time) sourceFact {
		return sourceFact{Source: "DCAI", Kind: "ORDER_PAID", Domain: domain, SourceKey: "sample", OrderID: id, PaymentID: id,
			RecordKey: table + ":" + id, Version: "1", OccurredAt: when, UpdatedAt: when, AmountMinor: &amount}
	}
	refund := func(id, refundID string, original sourceFact, amount int64) sourceFact {
		at := window.Start.Add(time.Hour)
		return sourceFact{Source: "DCAI", Kind: "REFUND_SUCCEEDED", Domain: original.Domain, SourceKey: "sample",
			OrderID: id, RefundID: refundID, RecordKey: original.RecordKey, Version: "1", OccurredAt: at, UpdatedAt: at,
			RefundsPayment: []sourcePaymentReference{{OrderID: original.OrderID, RecordID: original.OrderID}}, AmountMinor: &amount}
	}
	vip := payment("10", "VIP", "vip", 10000, window.Start)
	insert(vip, "VIP")
	vip.Version, vip.UpdatedAt = "2", window.Start.Add(time.Minute)
	newAmount := int64(12000)
	vip.AmountMinor = &newAmount
	insert(vip, "VIP")
	old := payment("11", "VIP", "vip", 10000, window.Start.Add(-time.Second))
	insert(old, "VIP")
	health := payment("20", "HEALTH_IDENTITY", "health", 20000, window.Start)
	insert(health, "HEALTH")
	insert(payment("12", "VIP", "vip", 7000, window.Start), "EXCLUDED")
	insert(payment("13", "VIP", "vip", 123, window.Start), "")
	insert(payment("14", "VIP", "vip", 40000, window.Through), "VIP")
	insert(refund("11", "a", old, 3000), "")
	insert(refund("refund-1", "b", vip, 1000), "")
	insert(refund("refund-2", "b", vip, 1000), "")
	missing := refund("missing", "c", vip, 1000)
	missing.RefundsPayment[0].OrderID = "unknown"
	insert(missing, "")
	insert(refund("refund-3", "d", health, 12500), "")
	insert(refund("refund-4", "e", health, 12500), "")
	older := payment("15", "VIP", "vip", 10000, window.Start.Add(-time.Hour))
	insert(older, "VIP")
	olderRefund := refund("15", "f", older, 9000)
	olderRefund.OccurredAt, olderRefund.UpdatedAt = window.Start.Add(-time.Minute), window.Start.Add(-time.Minute)
	insert(olderRefund, "")
	insert(refund("refund-5", "g", older, 2000), "")
	in := catalogPeriodActivityRequest{ProductID: product, Scenarios: []string{"FIRST", "SECOND", "MISSING"}, Window: window}
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		report, err := readCatalogPeriodActivity(ctx, tx, in)
		if err != nil {
			return err
		}
		if report.Receipts != 32000 || report.Refunds != 3000 || report.PriorPeriodRefunds != 3000 ||
			report.Issues["UNCLASSIFIED"] != 1 || report.Issues["DUPLICATE_EVENT"] != 2 ||
			report.Issues["REFUND_ORIGIN_MISSING"] != 1 || report.Issues["REFUND_EXCEEDS_PAYMENT"] != 3 {
			t.Fatalf("unexpected activity: %+v", report)
		}
		if len(report.Scenarios) != 3 || report.Scenarios[2].Mapped {
			t.Fatalf("missing scenario coverage was not reported: %+v", report.Scenarios)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	const packageID = "10000000-0000-4000-8000-000000000101"
	const ruleID = "10000000-0000-4000-8000-000000000102"
	const unitID = "10000000-0000-4000-8000-000000000103"
	const orgID = "10000000-0000-4000-8000-000000000104"
	for _, statement := range []string{
		`INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
		 VALUES('` + packageID + `',901,'net','运营净额','DCAI','COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE','DCAI_NET',false)`,
		`INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest,benefit_ready) VALUES('DCAI','2026-10-01',sha256('fixture'::bytea),true)`,
		`INSERT INTO organization_units(id,kind,name,status) VALUES('` + unitID + `','DEPARTMENT','运营部','ACTIVE')`,
		`INSERT INTO catalog_benefit_organizations(source,party_uuid,unit_id,observed_at)
		 VALUES('DCAI','` + orgID + `','` + unitID + `','2026-10-01')`,
		`WITH staff AS (INSERT INTO people(name) VALUES('上期负责人'),('本期负责人'),('池成员'),('未加入运营部') RETURNING id,name),
		 membership AS (
		  INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
		  SELECT gen_random_uuid(),id,'` + unitID + `','PRIMARY','2026-01-01' FROM staff WHERE name<>'未加入运营部'
		 )
		 INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,valid_to,observed_at)
		 SELECT gen_random_uuid(),'DCAI',id,CASE WHEN name IN ('池成员','未加入运营部') THEN 'member' ELSE 'owner' END,
		  'ORGANIZATION_UNIT','` + unitID + `',CASE WHEN name='本期负责人' THEN '2026-10-01 00:00+08' ELSE '2026-01-01' END::timestamptz,
		  CASE WHEN name='上期负责人' THEN '2026-10-01 00:00+08' END::timestamptz,'2026-10-01' FROM staff`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	rule := strings.NewReplacer(`"pool_party_id":"party"`, `"pool_party_id":"`+orgID+`"`,
		`"pool_organization_unit_id":"unit"`, `"pool_organization_unit_id":"`+unitID+`"`,
		`"allocation_owner_role":"owner"`, `"allocation_owner_role":"owner","role":"member"`).Replace(twoTierPeriodRule)
	if _, err := db.Exec(ctx, `INSERT INTO product_rule_versions
	 (source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at)
	 VALUES($1,$2,1,'PUBLISHED','2026-08-01 00:00+08','fixture',$3,'2026-10-01')`, ruleID, packageID, json.RawMessage(rule)); err != nil {
		t.Fatal(err)
	}
	in.GroupRule = &catalogPeriodVersion{Cycle: "MONTHLY", packageID: packageID}
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		report, err := readCatalogPeriodActivity(ctx, tx, in)
		if err != nil {
			return err
		}
		if report.Grouping == nil || len(report.Grouping.Groups) != 2 || len(report.Grouping.Issues) != 0 {
			t.Fatalf("expected two resolved groups: %+v", report.Grouping)
		}
		for _, group := range report.Grouping.Groups {
			if group.RuleID != ruleID || len(group.Targets) != 1 || group.Targets[0].MemberCount != 1 {
				t.Fatalf("wrong group membership: %+v", group)
			}
			if group.Targets[0].OwnerName == "上期负责人" && group.Receipts == 0 && group.Refunds == 3000 {
				if group.BeforeCarry.Total != 0 || group.BeforeCarry.CarryForward != -3000 {
					t.Fatalf("refund-only group must stay negative before prior carry: %+v", group.BeforeCarry)
				}
				continue
			}
			if group.Targets[0].OwnerName != "本期负责人" || group.Receipts != 32000 || group.Refunds != 0 {
				t.Fatalf("wrong owner or amount at appointment boundary: %+v", group)
			}
			if group.BeforeCarry.Basis != 32000 || group.BeforeCarry.Total != 320 ||
				len(group.BeforeCarry.Awards) != 1 || group.BeforeCarry.Awards[0].Rate != 100 {
				t.Fatalf("each group must select a tier using its own net receipts: %+v", group.BeforeCarry)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	s := workspace{db: db, development: true}
	requestTrial := func(base, from, changes string) catalogPeriodActivity {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/v1/catalog-period-activity", strings.NewReader(`{
		  "baseId":"`+base+`","effectiveFrom":"`+from+`T00:00:00+08:00",`+changes+`}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		s.postCatalogPeriodActivity(response, request)
		var trial catalogPeriodActivity
		if err := json.Unmarshal(response.Body.Bytes(), &trial); err != nil || response.Code != 200 || trial.Grouping == nil {
			t.Fatalf("order trial failed: %d %s %v", response.Code, response.Body.String(), err)
		}
		return trial
	}
	trial := requestTrial(ruleID, "2026-10-01", `"scenarioCodes":["FIRST"],"edits":[{"itemKey":"operations","tiers":[{"rate_bps":1000}]}]`)
	if trial.Receipts != 12000 || trial.Refunds != 3000 || len(trial.Grouping.Groups) != 2 {
		t.Fatalf("edited rule should use the remaining SKU and retain the historical refund: %+v", trial)
	}
	for _, group := range trial.Grouping.Groups {
		if group.RuleID == ruleID {
			if group.Refunds != 3000 || group.quote().CarryForward != -3000 {
				t.Fatalf("historical refund was repriced: %+v", group)
			}
		} else if group.RuleID != "" || group.quote().Total != 1200 {
			t.Fatalf("order trial did not apply the edited tier to the new group: %+v", group)
		}
	}
	unchanged := requestTrial(ruleID, "2026-10-01", `"scenarioCodes":["FIRST","SECOND"]`)
	if unchanged.Receipts != 32000 || len(unchanged.Grouping.Groups) != 2 {
		t.Fatalf("unedited scenarios should keep the published groups: %+v", unchanged)
	}
	for _, group := range unchanged.Grouping.Groups {
		if group.RuleID != ruleID {
			t.Fatalf("an unchanged definition must keep its stored version and carry identity: %+v", group)
		}
	}
	const selectedCategory = "10000000-0000-4000-8000-000000000106"
	const otherCategory = "10000000-0000-4000-8000-000000000107"
	const sellerCompany = "10000000-0000-4000-8000-000000000108"
	const otherCompany = "10000000-0000-4000-8000-000000000109"
	for _, statement := range []string{
		`INSERT INTO organization_units(id,kind,name,status) VALUES
		 ('` + sellerCompany + `','COMPANY','成交公司','ACTIVE'),('` + otherCompany + `','COMPANY','健康业务公司','ACTIVE')`,
		`INSERT INTO organization_sales_categories VALUES ('` + selectedCategory + `','选择的类别',true),('` + otherCategory + `','其他类别',true)`,
		`INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from) VALUES
		 ('DCAI','seller-company','` + sellerCompany + `','2026-01-01')`,
		`INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from,valid_to) VALUES
		 ('` + sellerCompany + `','` + selectedCategory + `','2026-01-01','2026-10-02T00:00+08'),
		 ('` + sellerCompany + `','` + otherCategory + `','2026-10-02T00:00+08',NULL)`,
		`INSERT INTO catalog_sales_company_aliases(id,source,external_id,company_uuid,company_unit_id,valid_from,observed_at)
		 VALUES(gen_random_uuid(),'DCAI','other-company','` + otherCompany + `','` + otherCompany + `','2026-01-01','2026-10-01')`,
		`INSERT INTO catalog_sales_categories(source,company_uuid,category_uuid,valid_from,observed_at)
		 VALUES('DCAI','` + otherCompany + `','` + otherCategory + `','2026-01-01','2026-10-01')`,
		`UPDATE source_order_classifications c SET sales_claims=jsonb_build_array(jsonb_build_object(
		 'role','PID_USER','user','seller','relationship','DIRECT','company',CASE WHEN p.order_id='20' THEN 'other-company' ELSE 'seller-company' END))
		 FROM source_order_versions p WHERE c.source_version_id=p.id AND p.order_id IN ('10','11','20')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	categoryTrial := requestTrial(ruleID, "2026-10-01", `"components":[{"itemKey":"operations","label":"运营",
	 "targetKind":"pool","organizationKey":"`+orgID+`","poolName":"运营池","ownerRole":"owner","memberRole":"member",
	 "salesScopeCategoryIds":["`+selectedCategory+`"],"tiers":[{"rate_bps":1000}]}]`)
	if categoryTrial.Receipts != 32000 || categoryTrial.Refunds != 3000 || len(categoryTrial.Grouping.Groups) != 2 || len(categoryTrial.Grouping.Issues) != 0 {
		t.Fatalf("company category filters the arrangement and leaves product receipts and earlier refunds available: %+v", categoryTrial.Grouping)
	}
	for _, group := range categoryTrial.Grouping.Groups {
		if group.RuleID == ruleID && group.Refunds != 3000 || group.RuleID == "" && (group.Receipts != 12000 || group.quote().Total != 1200) {
			t.Fatalf("category matches the original payment date and leaves the earlier open-period refund on its rule: %+v", group)
		}
	}
	var versions int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM product_rule_versions WHERE package_id=$1`, packageID).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("order trial must leave published versions unchanged: %d %v", versions, err)
	}

	const nextID = "10000000-0000-4000-8000-000000000105"
	nextRule := strings.Replace(rule, `["FIRST","SECOND"]`, `["FIRST"]`, 1)
	if _, err := db.Exec(ctx, `INSERT INTO product_rule_versions
	 (source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at,published_at,predecessor_id)
	 VALUES($1,$2,2,'PUBLISHED',$3,'next',$4,now(),now(),$5)`,
		nextID, packageID, window.Start, json.RawMessage(nextRule), ruleID); err != nil {
		t.Fatal(err)
	}
	in.Scenarios = []string{"FIRST"}
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		report, err := readCatalogPeriodActivity(ctx, tx, in)
		if err != nil {
			return err
		}
		if report.Receipts != 12000 || report.Refunds != 3000 || report.Issues["REFUND_EXCEEDS_PAYMENT"] != 1 {
			t.Fatalf("removed SKU must not contribute new sales or their invalid refunds: %+v", report)
		}
		if len(report.Grouping.Groups) != 2 || report.Grouping.Groups[0].RuleID != ruleID ||
			report.Grouping.Groups[0].Refunds != 3000 || report.Grouping.Groups[1].RuleID != nextID {
			t.Fatalf("new sales and earlier refunds must use their respective versions: %+v", report.Grouping)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	in.GroupRule.Paused = true
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		paused, err := readCatalogPeriodActivity(ctx, tx, in)
		if err != nil {
			return err
		}
		if !paused.Grouping.Paused || paused.Grouping.TrialOnPaused || len(paused.Grouping.Groups) != 0 {
			t.Fatalf("a normal read must show that the rule is paused: %+v", paused.Grouping)
		}
		in.TrialPaused = true
		trial, err := readCatalogPeriodActivity(ctx, tx, in)
		if err != nil {
			return err
		}
		if !trial.Grouping.Paused || !trial.Grouping.TrialOnPaused || len(trial.Grouping.Groups) != 2 ||
			trial.Grouping.Groups[1].BeforeCarry.Total != 120 {
			t.Fatalf("explicit trial should price actual orders while preserving pause status: %+v", trial.Grouping)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,
	 classifier,sku_code,sku_label,mapping_version,effective_from,observed_at,source_product_code)
	 SELECT gen_random_uuid(),product_id,source,domain,entity_type,record_table,business_key,
	  classifier,'AAA','冲突场景',mapping_version,effective_from,observed_at,source_product_code
	 FROM catalog_business_routes WHERE sku_code='SECOND'`); err != nil {
		t.Fatal(err)
	}
	insert(refund("health-duplicate-1", "health-duplicate", health, 100), "")
	insert(refund("health-duplicate-2", "health-duplicate", health, 100), "")
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		report, err := readCatalogPeriodActivity(ctx, tx, in)
		if err == nil && (report.Issues["DUPLICATE_EVENT"] != 4 || report.Issues["MAPPING_CONFLICT"] == 0) {
			t.Fatalf("a conflicting mapping must not conceal duplicate events: %+v", report.Issues)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	const scheduledID = "10000000-0000-4000-8000-000000000106"
	scheduledAt := window.Through.AddDate(0, 0, 10)
	if _, err := db.Exec(ctx, `INSERT INTO product_rule_versions
	 (source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at,published_at,predecessor_id)
	 VALUES($1,$2,3,'PUBLISHED',$3,'scheduled',$4,now(),now(),$5)`,
		scheduledID, packageID, scheduledAt, json.RawMessage(nextRule), nextID); err != nil {
		t.Fatal(err)
	}
	insert(payment("nov-late", "VIP", "vip", 30000, scheduledAt), "VIP")
	overlap := requestTrial(nextID, "2026-11-01", `"edits":[{"itemKey":"operations","tiers":[{"rate_bps":1000}]}]`)
	if groups := overlap.Grouping.Groups; overlap.Receipts != 70000 || len(groups) != 2 ||
		groups[0].RuleID != "" || groups[0].quote().Total != 4000 ||
		groups[1].RuleID != scheduledID || groups[1].quote().Total != 300 {
		t.Fatalf("trial must leave the already scheduled version active at its later cutoff: %+v", overlap)
	}
}

func TestCatalogPeriodActivityUsesCreatorCompletionForNewRules(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	const product = 971
	const packageID = "10000000-0000-4000-8000-000000000971"
	const ruleID = "10000000-0000-4000-8000-000000000972"
	const company = "10000000-0000-4000-8000-000000000973"
	s := workspace{db: db}
	for _, query := range []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE
		 VALUES(971,'CREATOR_NET','创客整期','calendar',1)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		 VALUES(971,gen_random_uuid(),'test','DCAI','DCAI','creator','2026-07-01',sha256('fixture'::bytea))`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,
		  classifier,sku_code,sku_label,mapping_version,effective_from,observed_at,source_product_code) VALUES
		 (gen_random_uuid(),971,'DCAI','HEALTH_IDENTITY','HEALTH_IDENTITY_ORDER','ai_health_ambassador_order',
		  'dcai:health-creator:new','dcai.health-identity.v1','SECOND','创客',1,'2026-07-01','2026-07-01','HEALTH_CREATOR')`,
		`INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
		 VALUES('` + packageID + `',971,'creator-net','创客整期','DCAI','COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE','CREATOR_NET',false)`,
		`INSERT INTO organization_units(id,kind,name,status) VALUES('` + company + `','COMPANY','收款公司','ACTIVE')`,
	} {
		if _, err := db.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	actor := financeRuleActor{subject: "fixture", permits: func(productID int64, _ bool) bool { return productID == product }}
	first, _ := calendarWindow("2026-08", 1)
	definition, err := buildCatalogPeriodRule(ctx, db, catalogPeriodCreate{ProductID: product, Label: "创客整期", Cycle: "MONTHLY"}, first.Start,
		[]string{"SECOND"}, []catalogPeriodComponentEdit{
			{Label: "公司收款", TargetKind: "company", OrganizationKey: "unit:" + company, Tiers: []catalogPeriodTier{{Rate: 100}}},
		}, nil, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO product_rule_versions
	 (source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at)
	 VALUES($1,$2,1,'PUBLISHED',$3,'fixture',$4,'2026-07-01')`, ruleID, packageID, first.Start, definition); err != nil {
		t.Fatal(err)
	}
	// The September rate applies to new payments, even though the August payment completes in September.
	if _, err := db.Exec(ctx, `INSERT INTO product_rule_versions
	 (source_id,package_id,version_no,source_status,effective_from,content_hash,definition,observed_at,published_at,predecessor_id)
	 VALUES(gen_random_uuid(),$1,2,'PUBLISHED','2026-09-01 00:00+08','v2',
	 jsonb_set($2::jsonb,'{benefit_actions,0,tier_table,0,rate_bps}','200'),now(),now(),$3)`, packageID, definition, ruleID); err != nil {
		t.Fatal(err)
	}
	write := func(recordID string, observed, completed time.Time) {
		t.Helper()
		reply := healthCompletionReply("HEALTH_CREATOR", observed, completed, recordID)
		reply.Cursor.ID = recordID
		for i := range reply.Gates {
			reply.Gates[i].OrderID = "order-" + recordID
		}
		for i, fact := range reply.Facts {
			reply.Facts[i] = json.RawMessage(strings.NewReplacer(
				"order-42", "order-"+recordID, `"42"`, `"`+recordID+`"`,
			).Replace(string(fact)))
		}
		row, err := normalizeDCAIOrderRow(dcaiHealthStream, time.Time{}, reply)
		if err != nil {
			t.Fatal(err)
		}
		err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			if _, err := storeSourceFacts(ctx, tx, row.Facts); err != nil {
				return err
			}
			return storeDCAIBlockers(ctx, tx, dcaiHealthStream, []orderPageRow{row})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	open := func(month string) int64 {
		t.Helper()
		var period int64
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			var err error
			period, err = createCatalogAggregatePeriod(ctx, tx, catalogAggregatePeriodInput{RuleID: ruleID, Month: month}, actor)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return period
	}
	august, september, october := open("2026-08"), open("2026-09"), open("2026-10")
	read := func(month string, period int64, expectedPaid, expectedRefunds, expectedPriorRefunds int64) {
		t.Helper()
		window, _ := calendarWindow(month, 1)
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			report, err := readCatalogPeriodActivity(ctx, tx, catalogPeriodActivityRequest{
				ProductID: product, Scenarios: []string{"SECOND"}, Window: window, DraftPeriodID: period,
				GroupRule: &catalogPeriodVersion{Cycle: "MONTHLY", packageID: packageID},
			})
			if err != nil {
				return err
			}
			if report.Receipts != expectedPaid || report.Refunds != expectedRefunds ||
				report.PriorPeriodRefunds != expectedPriorRefunds || len(report.Issues) != 0 {
				t.Fatalf("%s: want paid/refunds/prior %d/%d/%d, got %+v",
					month, expectedPaid, expectedRefunds, expectedPriorRefunds, report)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	pending := time.Date(2026, 8, 3, 0, 0, 0, 0, shanghai)
	write("42", pending, time.Time{})
	if _, err := db.Exec(ctx, `INSERT INTO source_order_versions
	 (source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact) VALUES
	 ('DCAI','order-42','','fixture_refund:1','1','REFUND_SUCCEEDED','2026-08-15','2026-08-15',
	  '{"domain":"HEALTH_IDENTITY","sourceKey":"HEALTH_CREATOR","refundId":"r1","amountMinor":80000,"refundsPayment":[{"orderId":"order-42","recordId":"42"}]}')`); err != nil {
		t.Fatal(err)
	}
	read("2026-08", august, 0, 0, 0)
	closePeriod := func(period int64, expectedTotal int64) {
		t.Helper()
		draft, err := s.calculateCurrentDraft(ctx, period, 0)
		if err != nil || draft.Total != expectedTotal || draft.IssueCount != 0 {
			t.Fatalf("period %d: expected total %d and no issues, got %+v, %v", period, expectedTotal, draft, err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, draft.Version); err != nil {
			t.Fatal(err)
		}
	}
	closePeriod(august, 0)
	if _, err := db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
		t.Fatal(err)
	}
	completed := time.Date(2026, 9, 1, 0, 0, 0, 0, shanghai)
	write("42", completed, completed)
	var queued bool
	if err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)", september).Scan(&queued); err != nil || !queued {
		t.Fatalf("completion did not queue September aggregate draft: %v, %v", queued, err)
	}
	read("2026-08", august, 0, 0, 0)
	read("2026-09", september, 1680000, 80000, 0)
	closePeriod(september, 16000)
	var completion int64
	if err := db.QueryRow(ctx, `SELECT a.completion_version_id FROM catalog_aggregate_draft_events a
	 JOIN source_order_versions v ON v.id=a.completion_version_id WHERE a.period_id=$1 AND a.source_version_id=a.payment_version_id
	 AND v.kind='ORDER_COMPLETED' AND v.occurred_at=$2`, september, completed).Scan(&completion); err != nil {
		t.Fatalf("first recognition should link to its own completion fact: %v", err)
	}
	corrected := completed.AddDate(0, 1, 0)
	write("42", corrected, corrected)
	read("2026-09", 0, 1680000, 80000, 0)
	read("2026-10", october, 0, 0, 0)
	if _, err := db.Exec(ctx, `INSERT INTO source_order_versions
	 (source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact) VALUES
	 ('DCAI','order-42','','fixture_refund:2','1','REFUND_SUCCEEDED','2026-10-03','2026-10-03',
	  '{"domain":"HEALTH_IDENTITY","sourceKey":"HEALTH_CREATOR","refundId":"r2","amountMinor":40000,"refundsPayment":[{"orderId":"order-42","recordId":"42"}]}')`); err != nil {
		t.Fatal(err)
	}
	read("2026-10", october, 0, 40000, 40000)
	draft, err := s.calculateCurrentDraft(ctx, october, 0)
	if err != nil || draft.IssueCount != 0 {
		t.Fatalf("refund should use the original confirmed group: %+v, %v", draft, err)
	}
	var refundCompletion int64
	if err := db.QueryRow(ctx, `SELECT completion_version_id FROM catalog_aggregate_draft_events
	 WHERE period_id=$1 AND source_version_id<>payment_version_id`, october).Scan(&refundCompletion); err != nil || completion != refundCompletion {
		t.Fatalf("refund should use confirmed completion %d, got %d: %v", completion, refundCompletion, err)
	}
	if _, err := db.Exec(ctx, "DELETE FROM draft_refresh_queue"); err != nil {
		t.Fatal(err)
	}
	write("43", corrected.Add(time.Minute), completed.Add(time.Hour))
	if err := db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=$1)", october).Scan(&queued); err != nil || !queued {
		t.Fatalf("late confirmation should queue the first open draft: %v, %v", queued, err)
	}
	read("2026-10", october, 1680000, 40000, 40000)
	read("2026-10", 0, 1680000, 40000, 40000)
	closePeriod(october, 16400)
}
