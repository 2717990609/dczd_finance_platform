package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestDirectorRateScope(t *testing.T) {
	rate := component{Type: "manager", Mode: "director_rate", Value: 30000}
	if err := validateComponents([]component{rate}); err != nil {
		t.Fatal(err)
	}
	newOnly, retrainOnly := rate, rate
	newOnly.ClassMember, retrainOnly.ClassMember = "new", "retrain"
	if err := validateComponents([]component{newOnly, retrainOnly}); err != nil {
		t.Fatal(err)
	}
	static := component{Type: "manager", Mode: "period_rate", Value: 30000, PeriodBenefit: &practicePeriodBenefit{PersonID: 9, Label: "原总监", RegistrationScope: &practiceRegistrationScope{Exclusive: true, SellerID: "71", SellerCompanyID: "99"}}}
	for _, parts := range [][]component{{rate, newOnly}, {static, rate}, {rate, static}, {{Type: "sales", Mode: "director_rate", Value: 30000}}, {{Type: "manager", Mode: "director_rate", Value: 30000, SalesPersonID: 2}}} {
		if err := validateComponents(parts); err == nil {
			t.Fatalf("accepted conflicting director component: %+v", parts)
		}
	}
}

func TestDirectorRegistryRollsForwardAndRefundPinsOriginalPayee(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	id := func(v any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "FAMILY", Name: "家庭教育实战班", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "4"}))
	alice := id(s.createPerson(ctx, createPersonInput{Name: "甲"}))
	bob := id(s.createPerson(ctx, createPersonInput{Name: "乙"}))
	start := time.Date(2026, 6, 8, 9, 38, 0, 0, shanghai)
	// Each new class is registered and signed in exactly once. Finance only
	// publishes the two rate changes and the single-class exception.
	addClass := func(ordinal int) (int64, time.Time, string) {
		t.Helper()
		at := start.AddDate(0, 0, ordinal-20)
		key := fmt.Sprintf("PRACTICE:2:4:%d", ordinal)
		order := fmt.Sprint(1000 + ordinal)
		period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: fmt.Sprint(ordinal), Ordinal: ordinal, SourcePeriodKey: key, StartsOn: at.Format("2006-01-02"), EndsOn: at.Format("2006-01-02")}))
		roster := practiceRoster{Key: key, Hash: key, SourceVersion: 1, StartsAt: at, ConfirmedAt: at.Add(time.Hour), Enrollments: []practiceEnrollment{{SourceEnrollmentID: order, ParticipantID: order, MembershipType: 3, CommissionEligible: true, PaidOrderIDs: []string{order}, SignedAt: at, ContentHash: fmt.Sprintf("%064x", ordinal)}}}
		amount := int64(10000)
		facts := []sourceFact{
			{Source: "DCZD", Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: "4", OrderID: order, PaymentID: order, RecordKey: "paid:" + order, Version: "1", AmountMinor: &amount, OccurredAt: at.Add(-time.Hour), UpdatedAt: at, ReferrerID: "11"},
			{Source: "DCZD", Kind: "DELIVERY_COMPLETED", Domain: "PRACTICE", SourceKey: "4", OrderID: order, RecordKey: "dc_offline_order_log_t:" + order, Version: "1", OccurredAt: at, UpdatedAt: at, ReferrerID: "11", PracticeRegistration: &sourcePracticeRegistration{CompanyID: "81", PeriodKey: key}},
		}
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := storePracticeRoster(ctx, tx, roster); err != nil {
				return err
			}
			_, err := storeSourceFacts(ctx, tx, facts)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return period, at, key
	}
	register := func(period, person int64, at time.Time, key string, n int) int64 {
		t.Helper()
		local := fmt.Sprint(person)
		doc := practiceRegistrationDocument{Version: n, PeriodKey: key, PeriodName: key, StartsAt: at, Rows: []practiceRegistrationImportRow{{Number: 1, LeaderID: local, LeaderCompany: "40000000-0000-4000-8000-000000000001", Recipient: local, SourceSeller: "20", SourceCompany: "80", SourceMember: "81", LeaderName: "总监"}}}
		var v int64
		err := s.db.QueryRow(ctx, `INSERT INTO practice_director_versions(product_id,period_id,starts_at,version_no,document,file_name,source_file,file_hash,preview_hash,request_hash,request_id)
		 VALUES($1,$2,$3,$4,$5,'fixture','','fixture','fixture','fixture',gen_random_uuid()) RETURNING id`, product, period, at, n, doc).Scan(&v)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	p20, a20, k20 := addClass(20)
	p21, a21, k21 := addClass(21)
	p22, _, _ := addClass(22)
	p23, _, _ := addClass(23)
	publish := func(ordinal int, period *int64, rate int64) {
		t.Helper()
		input := publishRuleInput{ProductID: product, PeriodID: period, Label: "总监比例", Components: []component{{Type: "manager", Mode: "director_rate", Value: rate}}}
		if period == nil {
			input.FromOrdinal = &ordinal
		}
		if _, err := s.publishRule(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	publish(20, nil, 30000)
	inspect := func(p, payee, amount, version int64) draftSummary {
		t.Helper()
		draft, err := s.calculateCurrentDraft(ctx, p, 0)
		if err != nil || draft.IssueCount != 0 || draft.Total != amount {
			t.Fatalf("period %d: %+v %v", p, draft, err)
		}
		var recipient, stored int64
		if err := s.db.QueryRow(ctx, `SELECT l.person_id,d.director_version_id FROM calculation_drafts d JOIN calculation_draft_lines l ON l.period_id=d.period_id WHERE d.period_id=$1`, p).Scan(&recipient, &stored); err != nil || recipient != payee || stored != version {
			t.Fatalf("expected %d/%d, saw %d/%d: %v", payee, version, recipient, stored, err)
		}
		return draft
	}
	missing, err := s.calculateCurrentDraft(ctx, p20, 0)
	if err != nil || missing.IssueCount != 1 || missing.Total != 0 {
		t.Fatalf("missing register must block settlement: %+v %v", missing, err)
	}
	v1 := register(p20, alice, a20, k20, 1)
	first := inspect(p20, alice, 300, v1)
	before := inspect(p21, alice, 300, v1)
	if _, err := s.db.Exec(ctx, "INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES($1,$2,'fixture')", p20, first.Version); err != nil {
		t.Fatal(err)
	}
	register(p20, bob, a20, k20, 2) // Same-start correction after period 20 was settled.
	v2 := register(p21, bob, a21, k21, 3)
	after := inspect(p21, bob, 300, v2)
	if after.Version <= before.Version || after.RuleID != before.RuleID {
		t.Fatal("transfer should refresh the draft without republishing the rule")
	}
	publish(22, nil, 40000)
	inspect(p22, bob, 400, v2)
	publish(0, &p22, 50000)
	inspect(p22, bob, 500, v2)
	inspect(p23, bob, 400, v2)
	// A later publication covering 21 onwards supersedes both the previous
	// carry-forward and the older single-period exception for period 22.
	publish(21, nil, 60000)
	inspect(p21, bob, 600, v2)
	latest := inspect(p22, bob, 600, v2)
	inspect(p23, bob, 600, v2)
	var listed []struct {
		ID          int64
		RuleVersion int
	}
	if err := s.db.QueryRow(ctx, periodsSQL, []int64{product}).Scan(&listed); err != nil {
		t.Fatal(err)
	}
	for _, p := range listed {
		want := latest.RuleVersion
		if p.ID == p20 {
			want = first.RuleVersion
		}
		if p.RuleVersion != want {
			t.Fatalf("period %d displays version %d, want %d", p.ID, p.RuleVersion, want)
		}
	}
	// Stopping the replacement must not reactivate an expired version.
	if _, err := s.retireRule(ctx, retireRuleInput{ProductID: product, RuleID: latest.RuleID, EffectiveFrom: a21}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.calculateCurrentDraft(ctx, p22, 0); !errors.Is(err, errDraftRule) {
		t.Fatalf("expired rule selected after retirement: %v", err)
	}
	// The later refund must read the originally frozen version, even though an
	// open replay of period 20 would now select the same-start correction.
	refundPeriod := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, SettlementMonth: "2026-07", Name: "七月退款"}))
	refunded := int64(5000)
	refundAt := time.Date(2026, 7, 9, 9, 0, 0, 0, shanghai)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE", SourceKey: "4", OrderID: "1020", RecordKey: "refund:1", Version: "1", RefundID: "1", AmountMinor: &refunded, RefundsPayment: []sourcePaymentReference{{OrderID: "1020", RecordID: "1020"}}, OccurredAt: refundAt, UpdatedAt: refundAt}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.calculateCurrentDraft(ctx, refundPeriod, 0)
	if err != nil || out.IssueCount != 0 || out.RefundTotal != -150 {
		t.Fatalf("historical refund: %+v %v", out, err)
	}
	var recipient int64
	if err := s.db.QueryRow(ctx, `SELECT l.person_id FROM calculation_draft_refunds r JOIN calculation_draft_lines l ON l.id=r.original_line_id WHERE r.period_id=$1`, refundPeriod).Scan(&recipient); err != nil || recipient != alice {
		t.Fatalf("refund payee %d: %v", recipient, err)
	}
}
