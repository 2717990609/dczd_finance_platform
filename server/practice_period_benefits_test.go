package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeFixedSignupBenefitKeepsOriginalPaymentShares(t *testing.T) {
	var part component
	if err := json.Unmarshal([]byte(`{"type":"delivery","mode":"period_rate","classMember":"new",
	 "periodBenefit":{"label":"每份新报交付","personId":1,"fixedMinor":1000}}`), &part); err != nil {
		t.Fatal(err)
	}
	if err := validateComponents([]component{part}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	member := practiceEnrollment{ID: 1, MembershipType: 3, CommissionEligible: true,
		PaidOrderIDs: []string{"1", "2"}, ConfirmedAt: start}
	makePayment := func(id int64, amount int64) paymentInput {
		order := strconv.FormatInt(id, 10)
		return paymentInput{ID: id, Fact: sourceFact{Source: "DCZD", PaymentID: order, OrderID: order,
			AmountMinor: &amount, OccurredAt: start.Add(-time.Hour)}}
	}
	orders := map[string][]paymentInput{"1": {makePayment(1, 4000)}, "2": {makePayment(2, 6000)}}
	for _, tc := range []struct {
		name      string
		refund    int64
		replaying bool
		want      [2]int64
	}{
		{"one enrollment", 0, false, [2]int64{400, 600}},
		{"partial refund before confirmation", 2000, false, [2]int64{200, 600}},
		{"full deposit refund in later month", 4000, true, [2]int64{0, 600}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refunds := map[int64][]practiceRefundMatch{}
			if tc.refund != 0 {
				refunds[1] = []practiceRefundMatch{{event: makePayment(3, tc.refund)}}
			}
			draft, err := calculatePracticeDraft([]practiceEnrollment{member}, paymentRule{ID: 1, Components: []component{part}},
				orders, nil, nil, practiceTierHistory{replaying: tc.replaying}, refunds, practiceDraftRuntime{})
			if err != nil || len(draft.Issues) != 0 {
				t.Fatalf("fixed signup draft: %+v %v", draft.Issues, err)
			}
			var got [2]int64
			for _, line := range draft.Lines {
				got[line.FactID-1] += line.Amount
			}
			if got != tc.want || draft.Total != tc.want[0]+tc.want[1] {
				t.Fatalf("payment shares %v, total %d, want %v", got, draft.Total, tc.want)
			}
		})
	}
}

func TestPracticePeriodBenefitsRespectAppointmentsScopeAndWholeClassTiers(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "CLASS_PERIOD_BENEFITS", Name: "班次分润", PeriodMode: "class"}))
	if _, err := s.db.Exec(ctx, `INSERT INTO product_catalog_settings
	 (product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
	 VALUES($1,'40000000-0000-4000-8000-000000000005','test','PRACTICE','DCZD','test',
	 clock_timestamp(),decode(repeat('00',32),'hex'))`, product); err != nil {
		t.Fatal(err)
	}
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 1,
		StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: "PRACTICE:1:1:100"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "test"}))
	manager := id(s.createPerson(ctx, createPersonInput{Name: "负责人"}))
	const company = "40000000-0000-4000-8000-000000000001"
	const department = "40000000-0000-4000-8000-000000000002"
	const group = "40000000-0000-4000-8000-000000000003"
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,kind,name,status) VALUES
	  ($1,NULL,'COMPANY','试算公司','ACTIVE'),($2,$1,'DEPARTMENT','试算部门','ACTIVE'),($3,$2,'TEAM','试算小组','ACTIVE')`,
		company, department, group); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
	 VALUES('40000000-0000-4000-8000-000000000004',$1,$2,'MANAGER',$3)`, manager, group, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO configuration_options(group_key,option_key,label,display_order)
	 VALUES('practiceSalesCompany','1','指定销售公司',10)`); err != nil {
		t.Fatal(err)
	}
	def := map[string]any{"productId": product, "periodId": period, "label": "分润方案", "components": []any{
		map[string]any{"type": "manager", "mode": "period_rate", "value": 11000, "classMember": "new",
			"periodBenefit": map[string]any{"label": "小组负责人", "personId": manager, "appointmentUnitId": group, "appointmentKind": "MANAGER", "salesCompanies": []string{"1"}}},
		map[string]any{"type": "delivery", "mode": "period_rate", "classMember": "new",
			"periodBenefit": map[string]any{"label": "交付团队", "unitId": department, "tierOnWholeClass": true,
				"tiers": []any{map[string]int64{"from": 0, "through": 39999, "value": 500},
					map[string]int64{"from": 40000, "value": 2150}}}},
		map[string]any{"type": "delivery", "mode": "period_rate", "classMember": "retrain",
			"periodBenefit": map[string]any{"label": "复训零计提", "unitId": department}},
	}}
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var rule publishRuleInput
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatalf("could not publish the configured class components: %v", err)
	}
	var ruleID int64
	if err := s.db.QueryRow(ctx, "SELECT id FROM rule_versions WHERE product_id=$1", product).Scan(&ruleID); err != nil {
		t.Fatal(err)
	}
	id(s.createPerson(ctx, createPersonInput{Name: "未被规则引用的人"}))
	readVersionDirectory := func(productGrant int64) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/practice-rule-recipients?ruleId="+strconv.FormatInt(ruleID, 10), nil)
		req = req.WithContext(context.WithValue(req.Context(), financePrincipalKey{}, &financePrincipal{Rules: []financeRuleGrant{{ProductID: productGrant}}}))
		result := httptest.NewRecorder()
		s.practiceRuleRecipients(result, req)
		return result
	}
	allowed := readVersionDirectory(product)
	var directory struct {
		People []struct{ ID int64 }
		Units  []struct{ ID string }
	}
	if err := json.Unmarshal(allowed.Body.Bytes(), &directory); err != nil {
		t.Fatal(err)
	}
	if allowed.Code != http.StatusOK || len(directory.People) != 1 || directory.People[0].ID != manager || len(directory.Units) != 2 {
		t.Fatalf("rule read grant must return only referenced recipients: %d %s", allowed.Code, allowed.Body.String())
	}
	if denied := readVersionDirectory(product + 1); denied.Code != http.StatusNotFound {
		t.Fatalf("ungranted version must be hidden, got %d", denied.Code)
	}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "非班次计提", PeriodID: &period,
		Components: []component{{Type: "manager", Mode: "rate", Value: 500}}}); !errors.Is(err, invalid) {
		t.Fatalf("published a manager component the class calculation cannot use: %v", err)
	}
	for _, enabled := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/practice-rule-recipients?productId="+strconv.FormatInt(product, 10), nil)
		actor := financePrincipal{Rules: []financeRuleGrant{{ProductID: product, Publish: enabled}}}
		req = req.WithContext(context.WithValue(req.Context(), financePrincipalKey{}, &actor))
		result := httptest.NewRecorder()
		s.practiceRuleRecipients(result, req)
		want := http.StatusForbidden
		if enabled {
			want = http.StatusOK
		}
		if result.Code != want {
			t.Fatalf("read recipient choices with publish=%t: got %d, want %d", enabled, result.Code, want)
		}
	}

	start := at.AddDate(0, 0, 1)
	partIDs := []string{"31", "32"}
	both := "32"
	retrain := "33"
	wire := practiceRosterWire{ContractVersion: "dczd.practice-roster/1.0", SourceSystem: "DCZD", PeriodKey: "PRACTICE:1:1:100",
		ActivityType: 1, StrategyID: 1, ActivityID: 100, RosterVersion: 1, PeriodStartAt: start.Format(time.RFC3339),
		ConfirmedAt: start.Add(time.Hour).Format(time.RFC3339), ConfirmedBy: "100", Enrollments: []practiceEnrollmentWire{
			{SourceEnrollmentID: "21", MembershipType: 3, SignedAt: start.Format(time.RFC3339), ParticipantID: "10",
				SourceOrderID: &both, PaidOrderIDs: &partIDs, CommissionEligible: true},
			{SourceEnrollmentID: "22", MembershipType: 2, SignedAt: start.Format(time.RFC3339), ParticipantID: "11",
				SourceOrderID: &retrain, CommissionEligible: true},
		}}
	wire.RosterHash = practiceRosterHash(wire.Enrollments)
	roster, err := normalizePracticeRoster(wire)
	if err != nil {
		t.Fatal(err)
	}
	fact := func(order string, amount int64, when time.Time) sourceFact {
		return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "test", Kind: "ORDER_PAID", RecordKey: "payment:" + order,
			PaymentID: order, OrderID: order, Version: "1", AmountMinor: &amount, OccurredAt: when, UpdatedAt: when, SalesCompanyID: "1"}
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, []sourceFact{fact("31", 10001, at.Add(-time.Minute)), fact("32", 20002, at), fact("33", 10000, at)})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	summary := result.(draftSummary)
	if summary.Total != 285 || summary.LineCount != 3 || summary.IssueCount != 0 {
		t.Fatalf("manager receives 220 from the on-appointment balance and delivery receives 65 from the new enrollment: %+v", summary)
	}
	for dimension, want := range map[string]int64{"person": 285, "company": 285, "department": 285} {
		var output struct{ Items []struct{ TotalMinor int64 } }
		err := s.db.QueryRow(ctx, draftReportSQL, period, summary.Version, dimension, 0).Scan(&output)
		var total int64
		for _, row := range output.Items {
			total += row.TotalMinor
		}
		if err != nil || total != want {
			t.Fatalf("%s report total=%d and error=%v", dimension, total, err)
		}
	}

	seller := id(s.createPerson(ctx, createPersonInput{Name: "成交销售"}))
	const nextDepartment = "40000000-0000-4000-8000-000000000006"
	const nextGroup = "40000000-0000-4000-8000-000000000007"
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,kind,name,status) VALUES
	 ($1,$3,'DEPARTMENT','新部门','ACTIVE'),($2,$1,'TEAM','新小组','ACTIVE')`, nextDepartment, nextGroup, company); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,valid_to) VALUES
	 (gen_random_uuid(),$1,$2,'PRIMARY',$4,$5),(gen_random_uuid(),$1,$3,'PRIMARY',$5,NULL)`, seller, group, nextGroup, at.Add(-time.Hour), at); err != nil {
		t.Fatal(err)
	}
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: seller, Source: "DCZD", ExternalID: "10", ValidFrom: at.Add(-time.Hour).Format(time.RFC3339)}))
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
	 VALUES('DCZD','1',$1,$2)`, company, at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		payments := []sourceFact{fact("31", 10001, at.Add(-time.Minute)), fact("32", 20002, at)}
		for i := range payments {
			payments[i].Version = "2"
			payments[i].UpdatedAt = at.Add(time.Hour)
			payments[i].ReferrerID = "10"
		}
		_, err := storeSourceFacts(ctx, tx, payments)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := def["components"].([]any)
	line := parts[1].(map[string]any)
	line["sourceRole"] = "DIRECT_REFERRER"
	line["periodBenefit"].(map[string]any)["salesUnits"] = []string{department, group}
	raw, err = json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var scoped publishRuleInput
	if err := json.Unmarshal(raw, &scoped); err != nil {
		t.Fatal(err)
	}
	fixed := int64(1000)
	scoped.Components = append(scoped.Components, component{Type: "delivery", Mode: "period_rate", ClassMember: "new",
		PeriodBenefit: &practicePeriodBenefit{Label: "新报固定交付", UnitID: department, FixedMinor: &fixed}})
	bad := scoped.Components[len(scoped.Components)-1]
	bad.Value = 1000
	if !errors.Is(validateComponents([]component{bad}), invalid) {
		t.Fatal("fixed amount must exclude ratio and tier configuration")
	}
	scoped.EditBaseID = &ruleID
	for _, invalidUnit := range []string{company, "40000000-0000-4000-8000-000000000008"} {
		benefit := scoped.Components[1].PeriodBenefit
		valid := benefit.SalesUnits
		benefit.SalesUnits = []string{invalidUnit}
		if _, err := s.publishRule(ctx, scoped); !errors.Is(err, invalid) {
			t.Fatalf("source scope accepted a non-department/group %q: %v", invalidUnit, err)
		}
		benefit.SalesUnits = valid
	}
	if _, err := s.publishRule(ctx, scoped); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, scoped); !errors.Is(err, errRuleEditStale) {
		t.Fatalf("a concurrent edit based on a superseded version was accepted: %v", err)
	}
	result, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	summary = result.(draftSummary)
	if summary.Total != 1242 || summary.LineCount != 4 || summary.IssueCount != 0 {
		t.Fatalf("fixed delivery adds one award for the two-leg new signup, alongside the previous awards: %+v", summary)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')`, period, summary.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE organization_assignments SET unit_id=$2 WHERE person_id=$1 AND valid_to IS NOT NULL", seller, nextGroup); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		replay, _, err := readVerifiedPracticeReplay(ctx, tx, period)
		if err != nil {
			return err
		}
		beforeRefund, err := replay.calculate()
		if err != nil || beforeRefund.Total != 1242 {
			t.Fatalf("replay must use original scope after organization change: %d %v", beforeRefund.Total, err)
		}
		deposit := replay.payments["31"][0].ID
		replay.refunds[deposit] = append(replay.refunds[deposit], practiceRefundMatch{event: paymentInput{Fact: fact("refund", 1000, at)}})
		afterRefund, err := replay.calculate()
		if err != nil || afterRefund.Total != 1192 {
			t.Fatalf("refund must reselect tier using the whole class and apply it to the frozen scope: %d %v", afterRefund.Total, err)
		}
		var tail int64
		for _, line := range afterRefund.Lines {
			if line.Component == "period:delivery:3" && line.FactID == replay.payments["32"][0].ID {
				tail = line.Amount
			}
		}
		if tail != 666 {
			t.Fatalf("the original fixed share of the unrefunded balance is 666 cents, got %d", tail)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
