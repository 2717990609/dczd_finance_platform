package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestSyncedCalendarPeriodUsesPinnedRulePlugin(t *testing.T) {
	db := integrationDB(t)
	s := workspace{db: db, development: true, sources: map[string]sourceConfig{"DCAI": {BaseURL: "https://fixtures.invalid", Token: "fixture"}}}
	ctx := t.Context()
	_, err := db.Exec(ctx, `
 INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE
 VALUES(970,'AI499','AI 499','calendar',25);
 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
 VALUES(970,gen_random_uuid(),'test','DCAI','DCAI','vip',now(),sha256('fixture'::bytea));
 INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,
  sku_code,sku_label,mapping_version,effective_from,observed_at)
 SELECT gen_random_uuid(),970,'DCAI','VIP','VIP_ORDER','ai_vip_order','vip','vip','VIP499','会员',1,'2025-01-01',captured_at
 FROM product_catalog_settings WHERE product_id=970;`)
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body string, status int, dst any) {
		t.Helper()
		for _, route := range s.routes() {
			if route.Path == path && route.Method == "POST" {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("POST", path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Sec-Fetch-Site", "same-origin")
				route.Handler.ServeHTTP(w, r)
				if w.Code != status {
					t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body)
				}
				if dst != nil && json.Unmarshal(w.Body.Bytes(), dst) != nil {
					t.Fatalf("invalid result %s", w.Body)
				}
				return
			}
		}
		t.Fatalf("missing POST %s", path)
	}
	const body = `{"label":"通用规则","arrangements":[{"key":"sales","label":"销售","targetKind":"order_seller","method":"rate","grain":"payment","rate":100000},{"key":"delivery","label":"交付","targetKind":"order_seller","method":"rate","grain":"period","rate":20000}]}`
	var saved struct{ ID, Version int64 }
	post("/api/v1/rule-plugins", body, 200, &saved)
	if saved.Version != 1 {
		t.Fatalf("expected initial version: %+v", saved)
	}
	var pulled practicePeriodPullResult
	post("/api/v1/periods/pull", `{"productId":970}`, 200, &pulled)
	if pulled.Added != 1 {
		t.Fatalf("expected current period: %+v", pulled)
	}
	post("/api/v1/periods/pull", `{"productId":970}`, 200, &pulled)
	if pulled.Added != 0 || pulled.Existing != 1 {
		t.Fatalf("sync must be repeatable: %+v", pulled)
	}
	var period int64
	var rangeLength int
	if err := db.QueryRow(ctx, "SELECT id,ends_on-starts_on+1 FROM periods WHERE product_id=970").Scan(&period, &rangeLength); err != nil ||
		rangeLength < 28 || rangeLength > 31 {
		t.Fatalf("invalid cycle: %d %d %v", period, rangeLength, err)
	}
	bind := func(version int64, expected int) {
		raw, _ := json.Marshal(map[string]int64{"periodId": period, "ruleVersionId": version})
		post("/api/v1/period-rule-plugin", string(raw), expected, nil)
	}
	bind(saved.ID, 200)
	bind(saved.ID, 200)
	raw, _ := json.Marshal(map[string]int64{"baseId": saved.ID})
	base := strings.TrimSuffix(string(raw), "}") + ","
	var revised struct{ ID, Version int64 }
	post("/api/v1/rule-plugins", base+strings.TrimPrefix(body, "{"), 200, &revised)
	if revised.Version != 2 || revised.ID == saved.ID {
		t.Fatalf("expected a new immutable version: %+v", revised)
	}
	bind(revised.ID, 409)
	list := httptest.NewRecorder()
	s.rulePluginList(list, httptest.NewRequest("GET", "/api/v1/rule-plugins", nil))
	var listed []struct {
		ID       int64
		Products []struct {
			ID   int64
			Name string
		}
	}
	if list.Code != 200 || json.Unmarshal(list.Body.Bytes(), &listed) != nil ||
		len(listed) != 1 || listed[0].ID != revised.ID || len(listed[0].Products) != 1 ||
		listed[0].Products[0].ID != 970 || listed[0].Products[0].Name != "AI 499" {
		t.Fatalf("rule list must associate the current rule with products using an older pinned snapshot: %d %s", list.Code, list.Body)
	}
	var pinned int64
	var sku string
	if err := db.QueryRow(ctx, `SELECT p.rule_plugin_version_id,v.definition#>>'{policy_scope,business_sku_codes,0}'
 FROM periods p JOIN product_rule_versions v ON v.package_id=p.aggregate_package_id WHERE p.id=$1`, period).
		Scan(&pinned, &sku); err != nil || pinned != saved.ID || sku != "VIP499" {
		t.Fatalf("period must own its compiled product scope and pin v1: %d %q %v", pinned, sku, err)
	}
	_, err = db.Exec(ctx, `
 INSERT INTO products(id,code,name,period_mode) OVERRIDING SYSTEM VALUE VALUES(971,'PRACTICE','实战班','class');
 INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(9711,'成交人');
 INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest) VALUES('DCAI',now(),sha256('fixture'::bytea));
 INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(9711,'DCAI','plugin-seller','2000-01-01');`)
	if err != nil {
		t.Fatal(err)
	}
	var classPeriod int64
	if err := db.QueryRow(ctx, `INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on) VALUES(971,'第1期',1,current_date,current_date) RETURNING id`).Scan(&classPeriod); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]int64{"periodId": classPeriod, "ruleVersionId": saved.ID})
	post("/api/v1/period-rule-plugin", string(raw), 200, nil)
	var classRuleExists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rule_versions rv JOIN periods p ON p.id=rv.period_id
 WHERE p.id=$1 AND p.rule_plugin_version_id=$2 AND rv.components->0->'periodBenefit'->>'calculationGrain'='payment'
 AND rv.components->1->'periodBenefit'->>'calculationGrain'='period')`, classPeriod, saved.ID).Scan(&classRuleExists); err != nil || !classRuleExists {
		t.Fatalf("the same plugin must supply both arrangements to the class period: %v %v", classRuleExists, err)
	}
	var paidAt time.Time
	if err := db.QueryRow(ctx, `SELECT starts_on::timestamp AT TIME ZONE 'Asia/Shanghai' FROM periods WHERE id=$1`, period).Scan(&paidAt); err != nil {
		t.Fatal(err)
	}
	amount := int64(10049)
	for _, id := range []string{"501", "502"} {
		err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCAI", Domain: "VIP", SourceKey: "vip", Kind: "ORDER_PAID", OrderID: id, RecordKey: "ai_vip_order:" + id, Version: "1",
				PaymentID: id, AmountMinor: &amount, OccurredAt: paidAt, UpdatedAt: paidAt,
				Classification: sourceClassification{Entity: "VIP_ORDER", Key: "vip", Classifier: "vip"}}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `UPDATE source_write_state SET provider_key=$1 WHERE source='DCAI'`, s.dcaiSourceKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO dcai_order_sync(stream,source_key,initial_from,next_updated_at,next_id,completed_through,last_success_at)
 VALUES('VIP_ORDERS',$1,'2000-01-01','2000-01-01','0',now(),now())`, s.dcaiSourceKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE source_order_classifications SET sales_claims='[{"role":"PID_USER","user":"plugin-seller","relationship":"DIRECT"}]'::jsonb`); err != nil {
		t.Fatal(err)
	}
	draft, err := s.calculateCurrentDraft(ctx, period, 0)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Total != 2410 || draft.IssueCount != 0 {
		t.Fatalf("both arrangements must be calculated using the pinned rule: %+v", draft)
	}

}

func TestPracticeRulePluginSeparatesRegistrationTypeAndCountsOneSignup(t *testing.T) {
	var input rulePluginInput
	err := json.Unmarshal([]byte(`{"label":"实战班验收","arrangements":[
 {"key":"retrain","label":"复训销售","method":"fixed","grain":"payment","targetKind":"order_seller","fixedMinor":9900,"signupType":"retrain","fixedBasis":"signup"},
 {"key":"delivery","label":"交付","method":"tiers","grain":"period","targetKind":"person","personId":2,"tierScope":"class_net",
 "tiers":[{"max_exclusive":100000,"rate_bps":2150},{"min_inclusive":100000,"rate_bps":2600}]}]}`), &input)
	if err != nil || validateRulePlugin(&input) != nil {
		t.Fatalf("configuration must be valid: %v", err)
	}
	items := practicePluginComponents(input.Arrangements)
	p := items[0]
	if p.ClassMember != "retrain" || !p.PracticeSignupBasis || p.PeriodBenefit.FixedPerOrder || p.matchesPracticeMember("new") {
		t.Fatalf("expected one payment-independent award for a retraining signup: %+v %+v", p, p.PeriodBenefit)
	}
	if !items[1].PeriodBenefit.TierOnWholeClass {
		t.Fatal("delivery must select a bracket from the complete class net")
	}
	start := time.Date(2026, 11, 8, 14, 30, 0, 0, shanghai)
	fixed := int64(9900)
	award := practicePluginComponents([]rulePluginArrangement{{Label: "复训池", Target: "person", PersonID: 2,
		Method: "fixed", Grain: "payment", SignupType: "retrain", FixedBasis: "signup", Fixed: &fixed}})
	enrolled := practiceEnrollment{ID: 1, MembershipType: 2, CommissionEligible: true, PaidOrderIDs: []string{"1", "2"}, ConfirmedAt: start}
	orders := map[string][]paymentInput{}
	for i, order := range []string{"1", "2"} {
		paid := int64(20000 + i*5000)
		orders[order] = []paymentInput{{ID: int64(i + 1), Fact: sourceFact{Kind: "ORDER_PAID", Source: "DCZD", OrderID: order, PaymentID: order,
			OccurredAt: start.Add(-time.Hour), AmountMinor: &paid}}}
	}
	draft, err := calculatePracticeDraft([]practiceEnrollment{enrolled}, paymentRule{ID: 1, Components: award},
		orders, nil, nil, practiceTierHistory{}, nil, practiceDraftRuntime{})
	if err != nil || draft.Total != 9900 || len(draft.Issues) != 0 {
		t.Fatalf("two payments for one confirmed enrollment must post once: %+v ‒ %v", draft, err)
	}
}

func TestRulePluginSalesRateUsesCompanyBeforeCategory(t *testing.T) {
	raw := []byte(`{"label":"499验收","arrangements":[{"key":"sales","label":"销售","method":"rate","grain":"payment","targetKind":"order_seller","rate":200000,
 "rateOverrides":[{"companyId":"2b000000-0000-4000-8000-000000000001","rate":0},{"categoryId":"2b000000-0000-4000-8000-000000000002","rate":300000}]}]}`)
	var input rulePluginInput
	if err := json.Unmarshal(raw, &input); err != nil || validateRulePlugin(&input) != nil {
		t.Fatalf("valid overrides: %v", err)
	}
	minimal := json.RawMessage(`{"benefit_actions":[{"item_key":"old"}]}`)
	saved, err := identifyPluginArrangements(minimal, rulePluginVersion{PluginID: 1, Arrangements: input.Arrangements})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "sales_rate_overrides") {
		t.Fatalf("expected the payout definition to pin the sales rate table: %s", saved)
	}
}

func TestRulePluginPoolAccruesWithoutRoleAndPersonalWindowUsesPaidAt(t *testing.T) {
	unit := "10000000-0000-4000-8000-000000000750"
	key := catalogPeriodUnitPrefix + unit
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, shanghai)
	join := start.AddDate(0, 0, 11)
	leave := start.AddDate(0, 0, 21)
	dir := catalogAggregateDirectory{People: map[int64]string{8: "项目负责人"}, Organizations: []vipBenefitOrganization{{Party: key, Unit: unit, Name: "交付部"}}}
	duty := catalogPerformanceDuty{PersonID: 8, Scope: "PROJECT", ScopeKey: "DCAI", Role: "PROJECT_OWNER"}
	performance := catalogPerformanceDirectory{duties: map[catalogPerformanceDuty][]vipInterval{duty: {{From: join, To: &leave}}}}
	newType := "NEW"
	var events []catalogActivityEvent
	for i, paidAt := range []time.Time{join.Add(-time.Second), join, leave.Add(-time.Second), leave} {
		events = append(events, catalogActivityEvent{EventID: int64(i + 1), PaymentID: int64(i + 1), Scenario: "VIP",
			PaidAt: paidAt, Amount: 10000, MembershipType: &newType})
	}
	rules := []catalogAggregateRule{{ID: "v", Version: 1, From: start, Definition: catalogPeriodDefinition{Scenarios: []string{"VIP"}, Items: []catalogPeriodItem{
		{ItemKey: "pool", Label: "交付池", TargetKind: "pool", PoolID: 1, PoolName: "交付池", OrganizationKey: key, unit: unit, Tiers: []catalogPeriodTier{{Rate: 300}}},
		{ItemKey: "person", Label: "项目负责人", TargetKind: "person", PersonID: 8, Tiers: []catalogPeriodTier{{Rate: 200}}, Appointment: &duty},
	}}}}
	report, err := groupCatalogPeriodEvents(events, rules, dir, nil, performance, start.AddDate(0, 1, 0))
	if err != nil || len(report.postingIssues) != 0 || len(report.Groups) != 2 {
		t.Fatalf("unexpected groups or issues: %v %+v", err, report)
	}
	for _, group := range report.Groups {
		if group.Targets[0].ItemKey == "pool" && group.Receipts != 40000 ||
			group.Targets[0].ItemKey == "person" && group.Receipts != 20000 {
			t.Fatalf("wrong order scope: %+v", group)
		}
	}
}

func fixturePluginAppointmentPerformance(t *testing.T, company, group string, start time.Time) catalogPerformanceDirectory {
	t.Helper()
	var groupID pgtype.UUID
	if err := groupID.Scan(group); err != nil {
		t.Fatal(err)
	}
	return catalogPerformanceDirectory{claims: map[int64]vipClaim{1: {Company: "company", User: "seller"}},
		companies: [2]map[string][]vipLink{{"company": {{Key: "company", Value: company}}}},
		users:     [2]map[string][]vipLink{{"seller": {{Key: "seller", Value: "1"}}}}, people: map[int64]string{1: "成交人"},
		organization: reportAttribution{Assignments: map[int64][]reportAssignment{1: {{Unit: groupID, From: start}}}}}
}

func TestTeamLeadReceivesGroupLeadShareForOwnSales(t *testing.T) {
	const company = "10000000-0000-4000-8000-000000000781"
	const team = "10000000-0000-4000-8000-000000000782"
	start := time.Date(2026, 11, 25, 0, 0, 0, 0, shanghai)
	handover := start.Add(time.Hour)
	sales, leader, group := int64(200000), int64(40000), int64(5000)
	owner := &rulePluginOrderAppointment{Level: "COMPANY", Role: "TEAM_LEAD"}
	subgroup := &rulePluginOrderAppointment{Level: "TEAM", Role: "GROUP_LEAD", Groups: []string{team}, SkipSellerHead: true}
	input := rulePluginInput{Label: "独立销售和岗位", Arrangements: []rulePluginArrangement{
		{Key: "sales", Label: "销售", Target: "order_seller", Method: "rate", Grain: "payment", Rate: &sales},
		{Key: "owner", Label: "团队负责人", Target: "person", Method: "rate", Grain: "period", Rate: &leader, OrderAppointment: owner},
		{Key: "group", Label: "小组负责人", Target: "person", Method: "rate", Grain: "period", Rate: &group, OrderAppointment: subgroup},
	}}
	if err := validateRulePlugin(&input); err != nil {
		t.Fatal(err)
	}
	const shell = `{"amount_basis":"ACTUAL_RECEIVED","recognition_trigger":"PAID","refund_policy":"PROPORTIONAL",
 "stacking_policy":"SUM","mixed_arrangements":true,"rounding_policy":{"mode":"HALF_UP","scale":2},
 "policy_scope":{"sales_systems":["DCAI"],"business_sku_codes":["VIP"],"financial_event_types":["order.paid"]},
 "settlement_policy":{"cycle":"MONTHLY_25_TO_24","negative_balance_policy":"CARRY_FORWARD"},"benefit_actions":[]}`
	options := catalogStructureOptions{Roles: []catalogStructureOption{{Value: "TEAM_LEAD", Organization: true}, {Value: "GROUP_LEAD", Organization: true}}}
	raw, err := writeCatalogPeriodComponents(json.RawMessage(shell), nil, calendarPluginComponents(input.Arrangements), options)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = identifyPluginArrangements(raw, rulePluginVersion{PluginID: 1, Arrangements: input.Arrangements})
	if err != nil {
		t.Fatal(err)
	}
	def, err := parseCatalogPeriodRule(raw)
	if err != nil {
		t.Fatal(err)
	}
	performance := fixturePluginAppointmentPerformance(t, company, team, start)
	performance.claims[2] = performance.claims[1]
	directory := catalogAggregateDirectory{AppointmentDuties: map[catalogAggregateDutyKey][]catalogAggregateDuty{
		{company, "TEAM_LEAD"}: {{ID: 2, Name: "原负责人", vipInterval: vipInterval{From: start, To: &handover}},
			{ID: 1, Name: "成交人", vipInterval: vipInterval{From: handover}}},
		{team, "GROUP_LEAD"}: {{ID: 4, Name: "组长", vipInterval: vipInterval{From: start}}},
	}}
	events := []catalogActivityEvent{
		{EventID: 1, PaymentID: 1, PaidAt: handover.Add(-time.Second), Scenario: "VIP", Amount: 49900},
		{EventID: 2, PaymentID: 2, PaidAt: handover, Scenario: "VIP", Amount: 49900},
	}
	report, err := groupCatalogPeriodEvents(events, []catalogAggregateRule{{ID: "rule", Version: 1, From: start, Definition: def}},
		directory, nil, performance, start.AddDate(0, 1, 0))
	if err != nil || len(report.postingIssues) != 0 {
		t.Fatalf("unexpected issue: %+v %v", report.postingIssues, err)
	}
	got := make(map[string]map[int64]int64)
	for _, group := range report.Groups {
		quote, err := group.calculateQuote(0)
		if err != nil {
			t.Fatal(err)
		}
		for _, award := range quote.Awards {
			if got[award.Label] == nil {
				got[award.Label] = make(map[int64]int64)
			}
			got[award.Label][group.posting.Targets[0].Person] += award.Amount
		}
	}
	if got["销售"][1] != 19960 || got["团队负责人"][1] != 1996 || got["团队负责人"][2] != 1996 ||
		got["小组负责人"][4] != 250 || got["小组负责人"][1] != 250 {
		t.Fatalf("team lead should receive the group lead share only on their own sale and keep the team lead commission: %v", got)
	}
}

func TestCalendarPluginBindsOrderAndProjectAppointments(t *testing.T) {
	db := integrationDB(t)
	s := workspace{db: db, development: true}
	ctx := t.Context()
	const company = "10000000-0000-4000-8000-000000000771"
	_, err := db.Exec(ctx, `
 INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE VALUES (971,'VIP499','499','calendar',25);
 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
 VALUES(971,gen_random_uuid(),'test','DCAI','DCAI','vip',now(),sha256('fixture'::bytea));
 INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,classifier,
  sku_code,sku_label,mapping_version,effective_from,observed_at)
 SELECT gen_random_uuid(),971,'DCAI','VIP','VIP_ORDER','ai_vip_order','vip','vip','VIP499','会员',1,'2025-01-01',captured_at
 FROM product_catalog_settings WHERE product_id=971;
 INSERT INTO periods(id,product_id,ordinal,name,starts_on,ends_on,calendar_month) OVERRIDING SYSTEM VALUE
 VALUES (972,971,202612,'499 2026-12','2026-11-25','2026-12-24','2026-12-01');
 INSERT INTO organization_units(id,name,kind,status) VALUES ('`+company+`','先锋','COMPANY','ACTIVE');
 INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(1,'服务岗'),(2,'项目负责人');
 INSERT INTO practice_pools(id,product_id,unit_id,name) OVERRIDING SYSTEM VALUE VALUES (7,971,'`+company+`','交付池');
 INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest) VALUES('DCAI','2026-11-26T00:00:00+08:00',sha256('fixture'::bytea));
 INSERT INTO catalog_sales_appointments(id,source,person_id,role,scope,scope_key,valid_from,observed_at)
 VALUES(gen_random_uuid(),'DCAI',2,'PROJECT_OWNER','PROJECT','DCAI','2026-12-10T00:00:00+08:00','2026-11-26T00:00:00+08:00');
 `)
	if err != nil {
		t.Fatal(err)
	}
	duty := &catalogPerformanceDuty{Scope: "PROJECT", Role: "PROJECT_OWNER"}
	rate := int64(30000)
	rule, err := s.saveRulePlugin(ctx, rulePluginInput{Label: "499", Arrangements: []rulePluginArrangement{
		{Key: "owner", Label: "项目负责人", Target: "person", PersonID: 2, Grain: "period", Method: "rate", Rate: &rate, SignupType: "new", Appointment: duty},
		{Key: "pool", Label: "交付部", Target: "pool", PoolID: 7, Grain: "period", Method: "rate", Rate: &rate, SignupType: "new"},
		{Key: "team", Label: "团队负责人", Target: "person", SignupType: "new", Method: "rate", Grain: "period", Rate: &rate,
			OrderAppointment: &rulePluginOrderAppointment{Level: "COMPANY", Role: "TEAM_LEAD"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.attachPeriodRulePlugin(ctx, periodRulePluginInput{PeriodID: 972, RuleVersionID: rule.ID}); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var raw json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT v.definition FROM product_rule_versions v
		 JOIN periods p ON p.aggregate_package_id=v.package_id WHERE p.id=972`).Scan(&raw); err != nil {
			return err
		}
		def, err := parseCatalogPeriodRule(raw)
		if err != nil {
			return err
		}
		resolved := def.Items[0].Appointment
		if len(def.Items) != 3 || resolved.ScopeKey != "DCAI" || def.Items[1].PoolID != 7 || def.Items[1].MemberRole != "" {
			t.Errorf("invalid bound snapshot: %+v", def.Items)
		}
		payment := catalogActivityEvent{PaymentID: 1, selection: &catalogAggregateMatch{rule: &catalogAggregateRule{Definition: def}}}
		dir, err := readCatalogPerformanceDirectory(ctx, tx, []catalogActivityEvent{payment})
		if err != nil {
			return err
		}
		start := time.Date(2026, 12, 10, 0, 0, 0, 0, shanghai)
		if before, after := dir.matchAppointment(resolved, start.Add(-time.Second)), dir.matchAppointment(resolved, start); !before.excluded || before.issue != "" || after.excluded || after.issue != "" {
			t.Errorf("expected the persisted project duty to start at midnight: before=%+v after=%+v", before, after)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPracticeRulePluginCompilesPersonalSalesTiers(t *testing.T) {
	var input rulePluginInput
	err := json.Unmarshal([]byte(`{"label":"健康观念实战班","arrangements":[
 {"key":"sales","label":"内部销售","method":"tiers","grain":"payment","targetKind":"order_seller","signupType":"new",
 "practiceSales":{"countFromOrdinal":156,"companySchedules":[
 {"companyIds":["8"],"count":[{"from":500,"mode":"rate","value":1930}],"performance":[{"from":0,"mode":"rate","value":1230}]},
 {"companyIds":["353"],"count":[{"from":500,"mode":"rate","value":1900}],"performance":[{"from":0,"mode":"rate","value":1200}]}]}}]}`), &input)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRulePlugin(&input); err != nil {
		t.Fatal(err)
	}
	parts := practicePluginComponents(input.Arrangements)
	if err := validateComponents(parts); err != nil {
		t.Fatal(err)
	}
	part := parts[0]
	if part.Tiers == nil || part.SourceRole != "SELLER" || !slices.Equal(part.salesCompanies(), []string{"8", "353"}) || part.matchesPracticeMember("retrain") {
		t.Fatalf("expected one shared personal sales calculation scoped to the configured companies: %+v", part)
	}
	purchases := []practiceTierPurchase{{EnrollmentID: 1, PersonID: 1, CompanyID: "8", Base: 50000}, {EnrollmentID: 2, PersonID: 1, CompanyID: "353", Base: 10000}}
	awards, err := choosePracticeSalesTiers(*part.Tiers, purchases, map[int64]int64{1: 499})
	if err != nil || awards[1].Value != 1230 || awards[2].Value != 1900 {
		t.Fatalf("expected first signup at the fallback rate and next company at its own count rate: %+v, %v", awards, err)
	}
	err = attachCalendarRulePlugin(t.Context(), nil, 1, 1, 25, time.Time{}, rulePluginVersion{Arrangements: input.Arrangements}, financeRuleActor{})
	if !errors.Is(err, errRulePluginPeriodBasis) {
		t.Fatalf("calendar settlement accepted a class-only calculation: %v", err)
	}
}

func TestCatalogPluginRateOverrideDistinguishesZeroFromNoMatch(t *testing.T) {
	at := time.Date(2026, 11, 25, 0, 0, 0, 0, shanghai)
	const company = "2b000000-0000-4000-8000-000000000001"
	category := "2b000000-0000-4000-8000-000000000002"
	item := catalogPeriodItem{Tiers: []catalogPeriodTier{{Rate: 2000}}, rateOverrides: []rulePluginRateOverride{
		{CategoryID: category, Rate: 300000}, {CompanyID: company, Rate: 0},
	}}
	directory := catalogPerformanceDirectory{
		claims: map[int64]vipClaim{1: {Company: "named"}, 2: {Company: "generic"}},
		companies: [2]map[string][]vipLink{{
			"named": {{Key: "named", Value: company}}, "generic": {{Key: "generic", Value: "other"}},
		}},
		categories: map[int64]catalogPaymentCategory{1: {id: &category}, 2: {id: &category}},
	}
	for id, want := range map[int64]int64{1: 0, 2: 3000} {
		configured := item
		issue := directory.salesRateOverride(catalogActivityEvent{PaymentID: id, PaidAt: at}, &configured)
		if issue != "" || configured.Tiers[0].Rate != want {
			t.Fatalf("payment %d got %d with %q, expected %d", id, configured.Tiers[0].Rate, issue, want)
		}
	}
}

func TestPracticePluginReusesSpecializedComponents(t *testing.T) {
	var input rulePluginInput
	if err := json.Unmarshal([]byte(`{"label":"第156期","arrangements":[
 {"key":"delivery","label":"交付","targetKind":"person","personId":11,"grain":"period","method":"rate","rate":2150,"signupType":"new_retrain"},
 {"key":"group","label":"小组组长","targetKind":"person","personId":12,"grain":"period","method":"rate","rate":11000,"signupType":"new","salesPeople":[21,22]},
 {"key":"external","label":"外部销售","targetKind":"company","grain":"payment","method":"rate","signupType":"new","practiceCompanySales":{"categories":[{"salesCategoryIds":["2b000000-0000-4000-8000-000000000001"],"companies":[],"mode":"rate","value":500000,"recommendationRate":50000}],"recommendationRecipients":["2b000000-0000-4000-8000-000000000002"]}},
 {"key":"retrain","label":"内部复训","targetKind":"order_seller","grain":"payment","method":"fixed","fixedBasis":"signup","signupType":"retrain","fixedMinor":9900,"practiceRetrainSplits":[{"companyId":"8","pool":{"poolId":2},"personalMinor":5000,"servicePersonId":13,"serviceMinor":600}]}
 ]}`), &input); err != nil {
		t.Fatal(err)
	}
	if err := validateRulePlugin(&input); err != nil {
		t.Fatal(err)
	}
	parts := practicePluginComponents(input.Arrangements)
	if len(parts) != 5 || parts[0].ClassMember != "new" || parts[1].ClassMember != "retrain" ||
		parts[0].Value != 2150 || parts[1].Value != 2150 {
		t.Fatalf("shared delivery should compile into two lifecycle groups: %+v", parts)
	}
	if !slices.Equal(parts[2].PeriodBenefit.SalesPeople, []int64{21, 22}) {
		t.Fatal("configured group member scope was lost")
	}
	if parts[3].CompanySales == nil || *parts[3].CompanySales.Categories[0].RecommendationRate != 50000 || !parts[3].PracticeSignupBasis {
		t.Fatal("external company and recommendation did not use the signup calculation")
	}
	applied := parts[4]
	if !applied.PracticeSignupBasis || !slices.Equal(applied.salesCompanies(), []string{"8"}) ||
		len(applied.retrainSplits()) != 1 || applied.retrainSplits()[0].ServiceMinor != 600 {
		t.Fatal("retrain split lost the company scope or signup amounts")
	}
	if err := validateRulePublication(publishRuleInput{ProductID: 1, Label: input.Label, Components: parts}); err != nil {
		t.Fatal(err)
	}
	for _, row := range input.Arrangements {
		if err := attachCalendarRulePlugin(t.Context(), nil, 1, 1, 25, time.Time{}, rulePluginVersion{Arrangements: []rulePluginArrangement{row}}, financeRuleActor{}); !errors.Is(err, errRulePluginPeriodBasis) {
			t.Fatalf("calendar rule allowed class-only arrangement %q: %v", row.Label, err)
		}
	}
}
