package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeCorrectionMovesCompanyAndChargesOriginalPool(t *testing.T) {
	t.Run("公司与资金池", func(t *testing.T) { verifyPracticeOrganizationCorrection(t, true) })
	t.Run("仅公司分润", func(t *testing.T) { verifyPracticeOrganizationCorrection(t, false) })
}

func verifyPracticeOrganizationCorrection(t *testing.T, withPool bool) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	const oldCompany, newCompany, poolUnit = "43000000-0000-4000-8000-000000000001", "43000000-0000-4000-8000-000000000002", "43000000-0000-4000-8000-000000000003"
	const rosterKey = "PRACTICE:1:1:56"
	product := id(s.createProduct(ctx, createProductInput{Code: "PRACTICE_CORPORATE_CORRECTION", Name: "公司更正样本", PeriodMode: "class"}))
	class := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 56,
		StartsOn: "2026-08-08", EndsOn: "2026-08-09", SourcePeriodKey: rosterKey}))
	september := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月退款与更正", SettlementMonth: "2026-09"}))
	october := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "十月退款与更正", SettlementMonth: "2026-10"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "fixture"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "样本销售"}))
	id(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCZD", ExternalID: "seller", ValidFrom: "2026-01-01T00:00:00Z"}))
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,parent_id,name,kind,status) VALUES
	 ('43000000-0000-4000-8000-000000000001',NULL,'公司甲','COMPANY','ACTIVE'),
	 ('43000000-0000-4000-8000-000000000002',NULL,'公司乙','COMPANY','ACTIVE'),
	 ('43000000-0000-4000-8000-000000000003','43000000-0000-4000-8000-000000000001','归集部门','DEPARTMENT','ACTIVE');
	 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from) VALUES
	 ('DCZD','old-company','43000000-0000-4000-8000-000000000001','2026-01-01'),
	 ('DCZD','new-company','43000000-0000-4000-8000-000000000002','2026-01-01');
	 INSERT INTO configuration_options(group_key,option_key,label,display_order) VALUES
	 ('practiceSalesCompany','old-company','公司甲',10),('practiceSalesCompany','new-company','公司乙',20)`); err != nil {
		t.Fatal(err)
	}
	pool := id(s.createPracticePool(ctx, createPracticePoolInput{ProductID: product, UnitID: poolUnit, Name: "交付池"}))
	var rule publishRuleInput
	if err := json.Unmarshal([]byte(fmt.Sprintf(`{"label":"公司与交付","components":[
	 {"type":"company_sales","mode":"company_sales","classMember":"new","sourceRole":"DIRECT_REFERRER",
	  "companySales":{"categories":[{"label":"外部","companies":["old-company","new-company"],"mode":"rate","value":100000,
	   "overrides":[{"companyId":"new-company","mode":"rate","value":200000}]}]}},
	 {"type":"delivery","mode":"period_rate","value":100000,"classMember":"new",
	  "periodBenefit":{"label":"交付","salesCompanies":["old-company"],"pool":{"poolId":%d,"ownerId":%d}}}]}`, pool, person)), &rule); err != nil {
		t.Fatal(err)
	}
	rule.ProductID, rule.PeriodID = product, &class
	if !withPool {
		rule.Components = rule.Components[:1]
	}
	if _, err := s.publishRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 8, 8, 8, 0, 0, 0, shanghai)
	paidAt := start.Add(-24 * time.Hour)
	changeAt := start.AddDate(0, 1, 0)
	refundAt := start.AddDate(0, 2, 0)
	store := func(fact sourceFact, received time.Time) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "UPDATE source_order_versions SET received_at=$1 WHERE kind=$2 AND version=$3", received, fact.Kind, fact.Version)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	amount := int64(9998)
	payment := sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "fixture", Kind: "ORDER_PAID", OrderID: "moving",
		RecordKey: "payment:moving", PaymentID: "moving", Version: "1", AmountMinor: &amount, OccurredAt: paidAt, UpdatedAt: paidAt,
		ReferrerID: "seller", SalesCompanyID: "old-company"}
	store(payment, paidAt)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: rosterKey, Hash: "company-correction", SourceVersion: 1,
			StartsAt: start, ConfirmedAt: start.Add(time.Hour), Enrollments: []practiceEnrollment{
				{SourceEnrollmentID: "1", ParticipantID: "1", MembershipType: 3, CommissionEligible: true,
					SignedAt: start, PaidOrderIDs: []string{"moving"}, ContentHash: "aa"},
			}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	draftAt := func(periodID int64) draftSummary {
		t.Helper()
		value, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: periodID})
		if err != nil {
			t.Fatal(err)
		}
		return value.(draftSummary)
	}
	freeze := func(draft draftSummary) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := insertSettlement(ctx, tx, draft.PeriodID, draft.Version, "fixture", "", nil)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	original := draftAt(class)
	expected := int64(1000)
	if withPool {
		expected += 1000
	}
	if original.Total != expected || original.IssueCount != 0 {
		t.Fatalf("expected company and pool gain of 1000 each: %+v", original)
	}
	var basis string
	if err := s.db.QueryRow(ctx, poolAllocationBasisSQL+"$1", class).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	scope := poolScope{Unit: poolUnit, Item: fmt.Sprint(pool), Scenario: "class"}
	if withPool {
		if _, err := s.savePoolAllocation(ctx, savePoolAllocationInput{PeriodID: class, Version: original.Version, Basis: basis, Retained: 1000, poolScope: scope}); err != nil {
			t.Fatal(err)
		}
	}
	freeze(original)
	payment.Version, payment.UpdatedAt, payment.SalesCompanyID = "2", changeAt, "new-company"
	store(payment, changeAt)

	changed := draftAt(september)
	if !withPool {
		if changed.CorrectionCount != 2 || changed.CorrectionTotal != 1000 || changed.IssueCount != 0 {
			t.Fatalf("company-only rules must be able to post the two signed company amounts: %+v", changed)
		}
		freeze(changed)
		refundAmount := int64(2001)
		store(sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "fixture", Kind: "REFUND_SUCCEEDED",
			OrderID: "moving", RecordKey: "refund:1", RefundID: "r1", Version: "1", AmountMinor: &refundAmount,
			OccurredAt: refundAt, UpdatedAt: refundAt, RefundsPayment: []sourcePaymentReference{{OrderID: "moving", RecordID: "moving"}}}, refundAt)
		refunded := draftAt(october)
		if refunded.RefundTotal != -200 || refunded.CorrectionTotal != -200 || refunded.CorrectionCount != 2 || refunded.IssueCount != 0 {
			t.Fatalf("company-only refund must preserve its original refund policy and debit the revised company: %+v", refunded)
		}
		return
	}
	if changed.CorrectionCount != 3 || changed.CorrectionTotal != 0 || changed.IssueCount != 0 {
		t.Fatalf("the original company and pool should debit 1000 each and the new company receive 2000: %+v", changed)
	}
	readPool := func(period int64) poolDraftItem {
		t.Helper()
		var stages map[poolScope]*poolStage
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var err error
			stages, err = readPoolStages(ctx, tx, period)
			return err
		}); err != nil || stages[scope] == nil {
			t.Fatalf("cannot read original pool: %v", err)
		}
		return stages[scope].poolDraftItem
	}
	if got := readPool(september); got.Opening != 0 || got.Balance != -1000 {
		t.Fatalf("the original allocation remains paid and the pool absorbs the deficit: %+v", got)
	}
	if got := readPool(class); got.Confirmed != 1000 || got.Balance != 0 {
		t.Fatalf("the frozen class pool changed: %+v", got)
	}
	freeze(changed)

	// Seed a later credit from a payment with a different membership window.
	// The isolated ledger leaves 200 available after filling the deficit.
	const laterKey = "PRACTICE:1:1:57"
	later := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "十月补班", Ordinal: 57,
		StartsOn: "2026-10-01", EndsOn: "2026-10-01", SourcePeriodKey: laterKey}))
	laterStart := time.Date(2026, 10, 1, 8, 0, 0, 0, shanghai)
	laterPerson := id(s.createPerson(ctx, createPersonInput{Name: "后续成员"}))
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from,valid_to)
	 VALUES('43000000-0000-4000-8000-000000000007',$1,$3,'MEMBER',$4,$5),
	 ('43000000-0000-4000-8000-000000000008',$2,$3,'MEMBER',$5,NULL)`,
		person, laterPerson, poolUnit, paidAt.Add(-time.Hour), laterStart.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &later, Label: "交付归集",
		Components: []component{{Type: "delivery", Mode: "period_rate", Value: 100000,
			PeriodBenefit: &practicePeriodBenefit{Label: "交付归集", Pool: &practicePoolTarget{PoolID: pool, OwnerID: person}}}}}); err != nil {
		t.Fatal(err)
	}
	laterAmount := int64(12000)
	laterPayment := payment
	laterPayment.OrderID, laterPayment.PaymentID, laterPayment.RecordKey, laterPayment.Version = "independent", "independent", "payment:independent", "later"
	laterPayment.SalesCompanyID, laterPayment.AmountMinor = "old-company", &laterAmount
	laterPayment.OccurredAt, laterPayment.UpdatedAt = laterStart.Add(-time.Hour), laterStart.Add(-time.Hour)
	store(laterPayment, laterPayment.UpdatedAt)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storePracticeRoster(ctx, tx, practiceRoster{Key: laterKey, Hash: "independent-correction", SourceVersion: 1,
			StartsAt: laterStart, ConfirmedAt: laterStart.Add(time.Hour), Enrollments: []practiceEnrollment{
				{SourceEnrollmentID: "2", ParticipantID: "2", MembershipType: 3, CommissionEligible: true,
					SignedAt: laterStart, PaidOrderIDs: []string{"independent"}, ContentHash: "aa"},
			}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	laterDraft := draftAt(later)
	if laterDraft.Total != 1200 || laterDraft.IssueCount != 0 {
		t.Fatalf("expected unrelated pool credit: %+v", laterDraft)
	}
	freeze(laterDraft)
	refundAmount := int64(2001)
	store(sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "fixture", Kind: "REFUND_SUCCEEDED",
		OrderID: "moving", RecordKey: "refund:1", RefundID: "r1", Version: "1", AmountMinor: &refundAmount,
		OccurredAt: refundAt, UpdatedAt: refundAt, RefundsPayment: []sourcePaymentReference{{OrderID: "moving", RecordID: "moving"}}}, refundAt)
	refunded := draftAt(october)
	if refunded.RefundTotal != -400 || refunded.CorrectionTotal != 0 || refunded.CorrectionCount != 3 || refunded.IssueCount != 0 {
		t.Fatalf("refund should charge 400 to the corrected company and offset the original company and pool debits: %+v", refunded)
	}
	if got := readPool(october); got.Opening != 200 || got.Refund != -200 || got.Correction != 200 || got.Balance != 200 {
		t.Fatalf("the refund's correction should cancel the extra pool debit: %+v", got)
	}
	for dim, expected := range map[string]map[string]int64{
		"company":    {newCompany: -400},
		"department": {"company-direct": -400},
		"person":     {"company": -400},
	} {
		var report struct {
			Items []struct {
				ID         string
				TotalMinor int64
			}
		}
		if err := s.db.QueryRow(ctx, draftReportSQL, october, refunded.Version, dim, 0).Scan(&report); err != nil {
			t.Fatal(err)
		}
		for _, row := range report.Items {
			if row.TotalMinor != expected[row.ID] {
				t.Fatalf("%s report attributed refund to wrong payee: %+v", dim, report)
			}
		}
	}
	if err := s.db.QueryRow(ctx, poolAllocationBasisSQL+"$1", october).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if _, err := s.savePoolAllocation(ctx, savePoolAllocationInput{PeriodID: october, Version: refunded.Version, Basis: basis,
		Shares: []poolAllocationShare{{PersonID: laterPerson, Amount: 200}}, poolScope: scope}); err != nil {
		t.Fatalf("the offsetting refund and correction must not move funds away from the source payment and eligible member: %v", err)
	}
	freeze(refunded)

	november := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "十一月退款与更正", SettlementMonth: "2026-11"}))
	restoredAt := start.AddDate(0, 3, 0)
	payment.Version, payment.UpdatedAt, payment.SalesCompanyID = "3", restoredAt, "old-company"
	store(payment, restoredAt)
	restored := draftAt(november)
	if restored.CorrectionCount != 3 || restored.CorrectionTotal != 0 || restored.IssueCount != 0 {
		t.Fatalf("restoring the original company should use the already-refunded payment balance: %+v", restored)
	}
	if got := readPool(november); got.Opening != 0 || got.Correction != 800 || got.Balance != 800 {
		t.Fatalf("the restored company must replenish the original payment's pool lot: %+v", got)
	}
	if err := s.db.QueryRow(ctx, poolAllocationBasisSQL+"$1", november).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if _, err := s.savePoolAllocation(ctx, savePoolAllocationInput{PeriodID: november, Version: restored.Version, Basis: basis,
		Shares: []poolAllocationShare{{PersonID: person, Amount: 800}}, poolScope: scope}); err != nil {
		t.Fatalf("positive corrections should fund the originally eligible member: %v", err)
	}
	freeze(restored)
	if _, err := s.db.Exec(ctx, "DELETE FROM settlements WHERE period_id=$1", september); err == nil {
		t.Fatal("cannot remove an earlier company or pool difference still used by confirmed adjustments")
	}
}
