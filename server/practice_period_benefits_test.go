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
}
