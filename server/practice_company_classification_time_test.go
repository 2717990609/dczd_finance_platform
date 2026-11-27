package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPracticeCompanyCategoryUsesClassStartForBothPaymentLegs(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	const first = "00000000-0000-4000-8000-000000000072"
	const next = "00000000-0000-4000-8000-000000000073"
	if _, err := db.Exec(ctx, `INSERT INTO organization_units(id,name,kind,status,sales_company_type)
	 VALUES('00000000-0000-4000-8000-000000000071','外部销售公司','COMPANY','ACTIVE','EXTERNAL');
	 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
	 VALUES('DCZD','77','00000000-0000-4000-8000-000000000071','2025-01-01');
	 INSERT INTO organization_sales_categories(id,name,active) VALUES
	 ('00000000-0000-4000-8000-000000000072','大客户',true),('00000000-0000-4000-8000-000000000073','合作伙伴',true);
	 INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from,valid_to) VALUES
	 ('00000000-0000-4000-8000-000000000071','00000000-0000-4000-8000-000000000072','2026-06-01','2026-11-01'),
	 ('00000000-0000-4000-8000-000000000071','00000000-0000-4000-8000-000000000073','2026-11-01',NULL)`); err != nil {
		t.Fatal(err)
	}
	date := func(month time.Month) time.Time { return time.Date(2026, month, 8, 14, 30, 0, 0, shanghai) }
	payment := func(id string, month time.Month) paymentInput {
		return paymentInput{Fact: sourceFact{SalesCompanyID: "77", PaymentID: id, OccurredAt: date(month)}}
	}
	facts := []paymentInput{payment("deposit", time.May), payment("balance", time.November)}
	boundRule := []component{{Type: "company_sales", PracticeSignupBasis: true, CompanySales: &practiceCompanySales{}}}
	legacyRule := []component{{Type: "company_sales", CompanySales: &practiceCompanySales{}}}
	if err := pgx.BeginTxFunc(ctx, db, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		classifications, err := loadPracticeCompanyClassifications(ctx, tx, map[int64]practiceCompanyClassificationScope{
			1: {at: practiceCompanyCategoryAt(boundRule, date(time.October)), payments: facts},
			2: {at: practiceCompanyCategoryAt(boundRule, date(time.November)), payments: facts},
			3: {at: practiceCompanyCategoryAt(boundRule, date(time.May)), payments: facts},
			4: {at: practiceCompanyCategoryAt(legacyRule, date(time.October)), payments: facts},
		}, false, false)
		if err != nil {
			return err
		}
		for roster, want := range map[int64]string{1: first, 2: next, 3: practiceUncategorizedCompany} {
			categories := classifications[roster].sales
			if categories["deposit"] != want || categories["balance"] != want {
				t.Fatalf("roster %d classified each leg differently: %v, want %s", roster, categories, want)
			}
		}
		if legacy := classifications[4].sales; legacy["deposit"] != practiceUncategorizedCompany || legacy["balance"] != next {
			t.Fatalf("legacy per-payment rules must keep the category at payment: %v", legacy)
		}
		parts := []component{
			{Type: "company_sales", ClassMember: "new", CompanySales: &practiceCompanySales{
				Categories: []practiceCompanyCategory{{SalesCategoryID: first, practiceCompanyValue: practiceCompanyValue{Mode: "rate", Value: 500000}}},
			}},
			{Type: "sales", ClassMember: "new"},
		}
		offers := practiceCompanyStandards(paymentRule{Components: parts})
		offers.personalScope["new"] = true
		p := practicePurchase{member: practiceEnrollment{MembershipType: 3}, payments: facts}
		p.classifyCompany(offers, classifications[3].sales)
		if p.companyRoutingIssue != "PRACTICE_TIER_SALES_CATEGORY_MISSING" {
			t.Fatalf("missing historical category should be reported, got %q", p.companyRoutingIssue)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
