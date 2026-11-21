package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestPracticeRetrainSplitsSaleServiceAndPoolAcrossPayments(t *testing.T) {
	var rule paymentRule
	const data = `{"ID":1,"Components":[{"type":"sales","mode":"fixed","value":9900,"sourceRole":"DIRECT_REFERRER","classMember":"retrain",
  "retrainSplits":[{"companyId":"direct","pool":{"poolId":9,"ownerId":7},"personalMinor":5000,"servicePersonId":4,"serviceMinor":600}]}]}`
	if err := json.Unmarshal([]byte(data), &rule); err != nil {
		t.Fatal(err)
	}
	pooledSales := rule.Components[0]
	directSales := component{Type: "company_sales", Mode: "company_sales", ClassMember: "retrain", SourceRole: "DIRECT_REFERRER",
		CompanySales: &practiceCompanySales{Categories: []practiceCompanyCategory{
			{Label: "外部标准", Companies: []string{"direct"}, practiceCompanyValue: practiceCompanyValue{Mode: "fixed", Value: 9900}},
		}}}
	if err := validateComponents([]component{pooledSales, directSales}); err == nil {
		t.Fatal("a company cannot receive direct payment and internal splits at once")
	}
	directSales.CompanySales.Categories[0].Companies = []string{"external"}
	if err := validateComponents([]component{pooledSales, directSales}); err != nil {
		t.Fatalf("independent internal and external company scopes should be publishable: %v", err)
	}
	if !practiceCompanyStandards(paymentRule{Components: []component{pooledSales, directSales}}).companies["retrain"]["direct"].internal {
		t.Fatal("a company with a split must be automatically included in the internal scope")
	}
	paid1, paid2, refundAmount := int64(12345), int64(37655), int64(45000)
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	cut := at.Add(time.Second)
	fact := func(id string, amount *int64, paidAt time.Time) sourceFact {
		return sourceFact{Source: "DCZD", OrderID: id, PaymentID: id, SalesCompanyID: "direct", ReferrerID: "seller", AmountMinor: amount, OccurredAt: paidAt}
	}
	members := []practiceEnrollment{{ID: 1, MembershipType: 2, SourceEnrollmentID: "11", CommissionEligible: true, ConfirmedAt: at.Add(time.Hour), PaidOrderIDs: []string{"31", "32"}}}
	orders := map[string][]paymentInput{"31": {{ID: 1, Fact: fact("31", &paid1, at)}}, "32": {{ID: 2, Fact: fact("32", &paid2, cut)}}}
	identities := map[identityKey][]identityAssignment{{"DCZD", "seller"}: {{ID: 5, PersonID: 2, ValidTo: &cut}, {ID: 6, PersonID: 2, ValidFrom: cut}}}
	runtime := practiceDraftRuntime{poolUnits: map[int64]pgtype.UUID{9: {Valid: true}}}
	result, err := calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, nil, runtime)
	if err != nil || len(result.Issues) != 0 || len(result.Lines) != 6 || result.Total != 9900 {
		t.Fatalf("expected two payments split to seller, service and pool: %+v %v", result, err)
	}
	byLeg := map[int64]int64{}
	byPerson := map[int64]int64{}
	for _, line := range result.Lines {
		if line.Component == "sales" && line.IdentityID != line.FactID+4 {
			t.Fatalf("source identity version must be specific to each payment: %+v", line)
		}
		byLeg[line.FactID] += line.Amount
		byPerson[line.PersonID] += line.Amount
	}
	if byLeg[1] != 2445 || byLeg[2] != 7455 || byPerson[2] != 5000 || byPerson[4] != 600 || byPerson[0] != 4300 {
		t.Fatalf("payment or recipient cents lost: payments=%v, recipients=%v", byLeg, byPerson)
	}
	// The approved fixed entitlement is based on a valid enrollment despite a partial refund.
	// A partial refund may apply across both payments. Re-test with one valid payment.
	members[0].PaidOrderIDs = []string{"32"}
	paid2 = 50000
	refunds := map[int64][]practiceRefundMatch{2: {{event: paymentInput{ID: 3, Fact: sourceFact{OccurredAt: at.Add(time.Minute), AmountMinor: &refundAmount}}}}}
	result, err = calculatePracticeDraft(members, rule, orders, identities, nil, practiceTierHistory{}, refunds, runtime)
	if err != nil || len(result.Issues) != 0 || len(result.Lines) != 3 || result.Total != 9900 || result.Lines[0].Base != 5000 {
		t.Fatalf("fixed commission changed after partial refund: %+v %v", result, err)
	}
}

func TestPracticePoolCombinesSplitsAndPeriodBenefitsAndFundsAllocations(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_POOL", Name: "实战班", PeriodMode: "class"}))
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 1, StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: "PRACTICE:1:1:88"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice-test"}))
	seller := id(s.createPerson(ctx, createPersonInput{Name: "销售"}))
	service := id(s.createPerson(ctx, createPersonInput{Name: "服务"}))
	member := id(s.createPerson(ctx, createPersonInput{Name: "成员"}))
	id(s.assignIdentity(ctx, assignIdentityInput{Source: "DCZD", ExternalID: "seller", PersonID: seller, ValidFrom: "2026-08-01T00:00:00Z"}))
	const company = "40000000-0000-4000-8000-000000000011"
	const dept = "40000000-0000-4000-8000-000000000012"
	_, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,kind,name,status) VALUES
 ('40000000-0000-4000-8000-000000000011',NULL,'COMPANY','销售公司','ACTIVE'),
 ('40000000-0000-4000-8000-000000000012','40000000-0000-4000-8000-000000000011','DEPARTMENT','归集部门','ACTIVE');
 INSERT INTO configuration_options(group_key,option_key,label,display_order) VALUES('practiceSalesCompany','direct','销售公司',10);
 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
 VALUES('DCZD','direct','40000000-0000-4000-8000-000000000011','2026-01-01');`)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	_, err = s.db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from)
 VALUES('40000000-0000-4000-8000-000000000013',$1,$2::uuid,'MEMBER',$3)`, member, dept, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	poolInput := createPracticePoolInput{ProductID: product, UnitID: dept, Name: "共用资金池"}
	unauthorized := context.WithValue(ctx, financePrincipalKey{}, &financePrincipal{Rules: []financeRuleGrant{{ProductID: product}}})
	if _, err := s.createPracticePool(unauthorized, poolInput); !errors.Is(err, errFinanceForbidden) {
		t.Fatalf("read only account created pool: %v", err)
	}
	pool := id(s.createPracticePool(ctx, poolInput))
	if again := id(s.createPracticePool(ctx, poolInput)); again != pool {
		t.Fatal("same named pool did not retain identity")
	}
	route := practicePoolTarget{PoolID: pool, OwnerID: service}
	rule := publishRuleInput{ProductID: product, Label: "复训及班次规则", Components: []component{
		{Type: "sales", Mode: "fixed", Value: 9900, SourceRole: "DIRECT_REFERRER", ClassMember: "retrain",
			RetrainSplits: &[]practiceRetrainSplit{{CompanyID: "direct", Pool: route, PersonalMinor: 5000, ServicePersonID: service, ServiceMinor: 600}}},
		{Type: "delivery", Mode: "period_rate", Value: 10000, ClassMember: "retrain",
			PeriodBenefit: &practicePeriodBenefit{Label: "交付分润归集", Pool: &route}},
	}}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	payment := func(order string, paid int64, when time.Time) sourceFact {
		return sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "practice-test", Kind: "ORDER_PAID", OrderID: order, PaymentID: order,
			RecordKey: "payment:" + order, Version: "1", AmountMinor: &paid, OccurredAt: when, UpdatedAt: when, ReferrerID: "seller", SalesCompanyID: "direct"}
	}
	start := at.AddDate(0, 0, 1)
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, practiceRoster{Key: "PRACTICE:1:1:88", Hash: "sample", SourceVersion: 1, StartsAt: start, ConfirmedAt: start.Add(time.Hour),
			Enrollments: []practiceEnrollment{{SourceEnrollmentID: "1", ParticipantID: "11", MembershipType: 2, CommissionEligible: true, SignedAt: start,
				PaidOrderIDs: []string{"31", "32"}, ContentHash: "ab"}}}); err != nil {
			return err
		}
		_, err := storeSourceFacts(ctx, tx, []sourceFact{payment("31", 12345, at), payment("32", 37655, at.Add(time.Minute))})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	draft := result.(draftSummary)
	if draft.Total != 10400 || draft.LineCount != 8 || draft.IssueCount != 0 {
		t.Fatalf("unexpected normal draft: %+v", draft)
	}
	t.Run("共用团队池只换分配负责人", func(t *testing.T) {
		next := rule
		next.Components = append([]component(nil), rule.Components...)
		newPool := practicePoolTarget{PoolID: pool, OwnerID: member}
		split := (*rule.Components[0].RetrainSplits)[0]
		split.Pool = newPool
		next.Components[0].RetrainSplits = &[]practiceRetrainSplit{split}
		delivery := *rule.Components[1].PeriodBenefit
		delivery.Pool = &newPool
		next.Components[1].PeriodBenefit = &delivery
		result, err := s.postComponentRuleImpact(ctx, componentRuleImpactInput{PeriodID: period, Rule: next})
		if err != nil {
			t.Fatal(err)
		}
		report := result.(ruleImpactReport)
		if report.BeforeMinor != draft.Total || report.AfterMinor != draft.Total || report.DeltaMinor != 0 || len(report.Items) != 4 {
			t.Fatalf("two payments must show both owners for the same pool without changing the total: %+v", report)
		}
		var current, proposed int64
		for _, row := range report.Items {
			if row.Recipient != "共用资金池" || row.EntryKind != "accrual" {
				t.Fatalf("expected only pool accrual changes: %+v", row)
			}
			switch {
			case row.AllocationOwner == "服务" && row.AfterMinor == 0:
				current += row.BeforeMinor
			case row.AllocationOwner == "成员" && row.BeforeMinor == 0:
				proposed += row.AfterMinor
			default:
				t.Fatalf("unexpected owner allocation: %+v", row)
			}
		}
		if current != 4800 || proposed != current {
			t.Fatalf("same pool should receive 4800 cents with either owner: %d, %d", current, proposed)
		}
	})
	params := fmt.Sprintf("periodId=%d&version=%d", period, draft.Version)
	readPools := func() poolDraftReport {
		t.Helper()
		w := httptest.NewRecorder()
		s.draftPools(w, httptest.NewRequest(http.MethodGet, "/api/v1/draft-pools?"+params, nil))
		var pools poolDraftReport
		if err := json.Unmarshal(w.Body.Bytes(), &pools); w.Code != http.StatusOK || err != nil || len(pools.Items) != 1 {
			t.Fatalf("expected a shared pool: %d %s", w.Code, w.Body.String())
		}
		return pools
	}
	report := readPools()
	entry := report.Items[0]
	if entry.Name != "共用资金池" || entry.Unit != dept || entry.Income != 4800 || entry.Balance != 4800 || len(entry.Members) != 1 {
		t.Fatalf("unexpected account: %+v", entry)
	}
	// Cumulative splitting leaves exactly the same funding capacity per payment.
	// The member joined just in time for the balance so the deposit cannot fund them.
	if entry.Members[0].ID != member || entry.Members[0].Available != 3614 {
		t.Fatalf("incorrect payment-date eligibility: %+v", entry.Members)
	}
	allocation := savePoolAllocationInput{PeriodID: period, Version: draft.Version, Basis: report.Basis, poolScope: entry.poolScope,
		Shares: []poolAllocationShare{{PersonID: member, Amount: 1000}}, Retained: 3800}
	if _, err := s.savePoolAllocation(ctx, allocation); err != nil {
		t.Fatal(err)
	}
	if !readPools().Items[0].PlanCurrent {
		t.Fatal("saved plan not visible")
	}
	for _, dimension := range []string{"person", "company", "department"} {
		var body struct {
			Items []struct {
				ID                          string
				TotalMinor, AllocationMinor int64
			}
		}
		if err := s.db.QueryRow(ctx, draftReportSQL, period, draft.Version, dimension, 0).Scan(&body); err != nil {
			t.Fatal(err)
		}
		var sum int64
		found := false
		for _, item := range body.Items {
			sum += item.TotalMinor
			if dimension == "person" && item.ID == strconv.FormatInt(member, 10) && item.AllocationMinor == 1000 {
				found = true
			}
			if dimension == "company" && item.ID == company && item.TotalMinor == 9800 {
				found = true
			}
			if dimension == "department" && item.ID == dept && item.TotalMinor == 4800 {
				found = true
			}
		}
		if sum != 10400 || !found {
			t.Fatalf("%s totals after allocation: %+v", dimension, body)
		}
	}
	if err := storeOrganizationSnapshot(ctx, s.db, []byte(`{"capturedAt":"2026-08-20T00:00:00Z","people":[],"units":[],"assignments":[]}`)); err != nil {
		t.Fatal(err)
	}
	if readPools().Items[0].PlanCurrent {
		t.Fatal("organization updates must invalidate the saved allocation basis")
	}
	allocation.Revision = 1
	if _, err := s.savePoolAllocation(ctx, allocation); !errors.Is(err, errPoolStale) {
		t.Fatalf("stale pool allocation was accepted: %v", err)
	}
	rule.Components = rule.Components[:1]
	rule.Components[0].RetrainSplits = nil
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	result, err = s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period})
	if err != nil {
		t.Fatal(err)
	}
	draft = result.(draftSummary)
	params = fmt.Sprintf("periodId=%d&version=%d", period, draft.Version)
	var show bool
	if err := s.db.QueryRow(ctx, "SELECT (("+draftDetailSQL+")->>'poolAllocationAvailable')::boolean", period, 0, 0, draft.Version, 0, 0).Scan(&show); err != nil || !show {
		t.Fatalf("a saved plan stays visible after funding has been removed: %t %v", show, err)
	}
	report = readPools()
	if report.Items[0].Name != "共用资金池" || report.Items[0].PlanCurrent || report.Items[0].Balance != 0 {
		t.Fatalf("stale plan lost its original pool name: %+v", report.Items[0])
	}
	allocation.Version, allocation.Basis, allocation.Shares, allocation.Retained = draft.Version, report.Basis, nil, 0
	if _, err := s.savePoolAllocation(ctx, allocation); err != nil {
		t.Fatalf("could not clear stale pool plan: %v", err)
	}
}
