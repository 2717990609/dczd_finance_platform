package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestFinanceHistoryAdoptionChecksReplayAndCarriesOriginalRefund(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	mustID := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	day := 1
	product := mustID(s.createProduct(ctx, createProductInput{Code: "ARCHIVE_SAMPLE", Name: "历史核算样本", PeriodMode: "calendar", CycleStartDay: &day}))
	period := mustID(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "2025-08", SettlementMonth: "2025-08"}))
	later := mustID(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "2025-09", SettlementMonth: "2025-09"}))
	person := mustID(s.createPerson(ctx, createPersonInput{Name: "历史收款人员"}))
	mustID(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCAI", Domain: "ARCHIVE", SourceKey: "archive-sample"}))
	mustID(s.assignIdentity(ctx, assignIdentityInput{PersonID: person, Source: "DCAI", ExternalID: "seller", ValidFrom: "2025-01-01T00:00:00+08:00"}))
	components := []component{{Type: "sales", Mode: "rate", Value: 1000, SourceRole: "LOCK_SALES_USER", RefundPolicy: "proportional"}}
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "归档销售规则", Components: components}); err != nil {
		t.Fatal(err)
	}
	paidAt := time.Date(2025, 8, 3, 10, 0, 0, 0, shanghai)
	confirmedAt := time.Date(2025, 9, 2, 10, 0, 0, 0, shanghai)
	amount := int64(10000)
	paid := sourceFact{Source: "DCAI", OrderID: "archive-order", PaymentID: "archive-payment", RecordKey: "order:1", Domain: "ARCHIVE", SourceKey: "archive-sample",
		Kind: "ORDER_PAID", AmountMinor: &amount, Version: "1", OccurredAt: paidAt, UpdatedAt: paidAt,
		Participants: []sourceParticipant{{Role: "LOCK_SALES_USER", UserID: "seller"}}}
	importFact := func(f sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			_, err := storeSourceFacts(ctx, tx, []sourceFact{f})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	importFact(paid)
	if _, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period}); err != nil {
		t.Fatal(err)
	}
	input := financeHistoryInput{ProductCode: "ARCHIVE_SAMPLE", PeriodID: period, ConfirmedAt: confirmedAt,
		Expected: &financeHistoryTotals{PerformanceMinor: 10000, AccruedMinor: 1000, AccrualCount: 1, PerformanceCount: 1}}
	if err := s.db.QueryRow(ctx, `SELECT current_database(),current_schema()`).Scan(&input.Database, &input.Schema); err != nil {
		t.Fatal(err)
	}
	t.Run("original confirmation before refund cutoff", func(t *testing.T) {
		cutoff := confirmedAt.Add(time.Hour)
		if _, err := s.setPeriodRefundCutoff(ctx, periodRefundCutoffInput{PeriodID: period, CutoffAt: &cutoff}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.calculateCurrentDraft(ctx, period, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := s.runFinanceHistory(ctx, input, false, nil); !errors.Is(err, errFinanceHistoryTime) {
			t.Errorf("history accepted a confirmation before the refund cutoff: %v", err)
		}
		if _, err := s.setPeriodRefundCutoff(ctx, periodRefundCutoffInput{PeriodID: period}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.calculateCurrentDraft(ctx, period, 0); err != nil {
			t.Fatal(err)
		}
	})
	wrong := input
	wrong.Database = "another_database"
	if _, err := s.runFinanceHistory(ctx, wrong, false, nil); !errors.Is(err, errFinanceHistoryTarget) {
		t.Fatalf("another target must be rejected: %v", err)
	}
	wrong = input
	wrong.Expected = &financeHistoryTotals{PerformanceMinor: 9000}
	if _, err := s.runFinanceHistory(ctx, wrong, false, nil); !errors.Is(err, errFinanceHistoryMismatch) {
		t.Fatalf("archive differences must be rejected: %v", err)
	}
	if _, err := s.runFinanceHistory(ctx, input, true, nil); !errors.Is(err, errFinanceHistoryToken) {
		t.Fatalf("application without a review token must be rejected: %v", err)
	}
	preview, err := s.runFinanceHistory(ctx, input, false, nil)
	if err != nil || preview.PostingsSHA256 == "" || preview.Token != "" || preview.Settlement != nil || preview.Totals != *input.Expected {
		t.Fatalf("archive preview = %+v, %v", preview, err)
	}
	var exported bytes.Buffer
	if _, err := s.runFinanceHistory(ctx, input, false, &exported); err != nil ||
		fmt.Sprintf("%x", sha256.Sum256(exported.Bytes())) != preview.PostingsSHA256 || !bytes.Contains(exported.Bytes(), []byte(`["personRef",`)) {
		t.Fatalf("export must include reference rows and reproduce the preview fingerprint: %v", err)
	}
	input.PostingsSHA256 = preview.PostingsSHA256
	preview, err = s.runFinanceHistory(ctx, input, false, nil)
	if err != nil || preview.Token == "" {
		t.Fatalf("same postings should be ready for application: %+v, %v", preview, err)
	}
	var count int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM settlements`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("read-only preview published a settlement, count=%d error=%v", count, err)
	}
	input.Token = preview.Token
	if _, err := s.db.Exec(ctx, `UPDATE products SET settlement_enabled=true WHERE id=$1`, product); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runFinanceHistory(ctx, input, true, nil); !errors.Is(err, errFinanceHistoryEnabled) {
		t.Fatalf("historical replay must not become a routine closing path: %v", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE products SET settlement_enabled=false WHERE id=$1`, product); err != nil {
		t.Fatal(err)
	}
	other := mustID(s.createPerson(ctx, createPersonInput{Name: "其他收款人员"}))
	if _, err := s.db.Exec(ctx, `UPDATE calculation_draft_lines SET person_id=$2 WHERE period_id=$1`, period, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runFinanceHistory(ctx, input, true, nil); !errors.Is(err, errFinanceHistoryPostings) {
		t.Fatalf("a different saved payee with the same totals must be rejected: %v", err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE calculation_draft_lines SET person_id=$2 WHERE period_id=$1`, period, person); err != nil {
		t.Fatal(err)
	}
	// Another rule version with the same monetary values still invalidates review.
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, Label: "更新归档规则", Components: components}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runFinanceHistory(ctx, input, true, nil); !errors.Is(err, errFinanceHistoryPostings) {
		t.Fatalf("a stale review token was accepted: %v", err)
	}
	input.PostingsSHA256 = ""
	preview, err = s.runFinanceHistory(ctx, input, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	input.PostingsSHA256 = preview.PostingsSHA256
	if _, err := s.runFinanceHistory(ctx, input, true, nil); !errors.Is(err, errFinanceHistoryToken) {
		t.Fatalf("the revised postings cannot reuse an earlier review token: %v", err)
	}
	preview, err = s.runFinanceHistory(ctx, input, false, nil)
	if err != nil || preview.Token == "" {
		t.Fatalf("revised preview = %+v, %v", preview, err)
	}
	input.Token = preview.Token
	type adoption struct {
		review financeHistoryReview
		err    error
	}
	start := make(chan struct{})
	results := make(chan adoption, 2)
	for range 2 {
		go func() {
			<-start
			review, err := s.runFinanceHistory(ctx, input, true, nil)
			results <- adoption{review, err}
		}()
	}
	close(start)
	first, repeat := <-results, <-results
	if first.err != nil || repeat.err != nil || first.review.Settlement == nil || repeat.review.Settlement == nil ||
		first.review.Settlement.ID != repeat.review.Settlement.ID || !first.review.Settlement.ConfirmedAt.Equal(confirmedAt) {
		t.Fatalf("concurrent application must return one result at the original confirmation time: %+v / %+v", first, repeat)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
	 SELECT source_version_id,'ARCHIVE_ORDER','archive.sample','sample.v1' FROM calculation_draft_lines WHERE period_id=$1`, period); err != nil {
		t.Fatal(err)
	}
	if retry, err := s.runFinanceHistory(ctx, input, true, nil); err != nil || retry.Settlement == nil || retry.Settlement.ID != first.review.Settlement.ID {
		t.Fatalf("source enrichment after confirmation must keep the same retry result: %+v, %v", retry, err)
	}
	if _, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period}); !errors.Is(err, errSettlementClosed) {
		t.Fatalf("adopted history can still change: %v", err)
	}
	refundAmount := int64(2500)
	refundAt := confirmedAt.Add(time.Hour)
	importFact(sourceFact{Source: paid.Source, OrderID: paid.OrderID, RecordKey: "refund:1", Domain: paid.Domain, SourceKey: paid.SourceKey,
		Kind: "REFUND_SUCCEEDED", RefundID: "after-handoff", AmountMinor: &refundAmount, Version: "1", OccurredAt: refundAt, UpdatedAt: refundAt,
		RefundsPayment: []sourcePaymentReference{{OrderID: paid.OrderID, RecordID: "1"}}})
	result, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: later})
	if err != nil {
		t.Fatal(err)
	}
	draft := result.(draftSummary)
	if draft.Total != -250 || draft.RefundCount != 1 || draft.IssueCount != 0 {
		t.Fatalf("later refund did not recover the original historical payee: %+v", draft)
	}
}

func TestHistoryFundingRequiresExplicitPayee(t *testing.T) {
	for _, raw := range []string{
		`{"sourceLineId":1,"amountMinor":100}`,
		`{"sourceLineId":1,"personId":null,"amountMinor":100}`,
		`{"sourceLineId":1,"personId":0,"amountMinor":100,"unknown":true}`,
	} {
		var cell financeHistoryFundingCell
		if json.Unmarshal([]byte(raw), &cell) == nil {
			t.Fatalf("accepted invalid funding %s", raw)
		}
	}
	var cell financeHistoryFundingCell
	if err := json.Unmarshal([]byte(`{"sourceLineId":1,"personId":0,"amountMinor":100}`), &cell); err != nil || cell.PersonID != 0 {
		t.Fatalf("explicit organization retention rejected: %+v %v", cell, err)
	}
	_, err := normalizeHistoryPoolFunding([]financeHistoryPool{{
		poolScope: poolScope{Unit: "00000000-0000-4000-8000-000000000001", Item: "1"},
		Funding:   []financeHistoryFundingCell{cell},
	}})
	if err != nil {
		t.Fatalf("empty scenario for a non-aggregate pool rejected: %v", err)
	}
}

func TestFinanceHistoryAdoptsOriginalPoolFunding(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	id := func(value any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "ARCHIVE_POOL", Name: "历史团队池", PeriodMode: "class"}))
	const rosterKey = "PRACTICE:1:1:88"
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "八月班", Ordinal: 1,
		StartsOn: "2025-08-08", EndsOn: "2025-08-09", SourcePeriodKey: rosterKey}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "archive-pool"}))
	seller := id(s.createPerson(ctx, createPersonInput{Name: "销售"}))
	recipient := id(s.createPerson(ctx, createPersonInput{Name: "历史受益人"}))
	id(s.assignIdentity(ctx, assignIdentityInput{Source: "DCZD", ExternalID: "seller", PersonID: seller, ValidFrom: "2025-08-01T00:00:00Z"}))
	const unit = "20000000-0000-0000-0000-000000000018"
	if _, err := s.db.Exec(ctx, "INSERT INTO organization_units(id,kind,name,status) VALUES($1,'DEPARTMENT','结算部门','ACTIVE')", unit); err != nil {
		t.Fatal(err)
	}
	pool := id(s.createPracticePool(ctx, createPracticePoolInput{ProductID: product, UnitID: unit, Name: "原分配池"}))
	if _, err := s.publishRule(ctx, publishRuleInput{ProductID: product, PeriodID: &period, Label: "原分润规则", Components: []component{
		{Type: "sales", Mode: "rate", Value: 0, SourceRole: "DIRECT_REFERRER", ClassMember: "new"},
		{Type: "delivery", Mode: "period_rate", Value: 10000, ClassMember: "new", PeriodBenefit: &practicePeriodBenefit{
			Label: "交付归集", Pool: &practicePoolTarget{PoolID: pool, OwnerID: seller}}},
	}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2025, 8, 8, 9, 0, 0, 0, shanghai)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := storePracticeRoster(ctx, tx, practiceRoster{Key: rosterKey, Hash: "archived", SourceVersion: 1,
			StartsAt: start, ConfirmedAt: start.Add(time.Hour),
			Enrollments: []practiceEnrollment{{SourceEnrollmentID: "1", ParticipantID: "11", MembershipType: 3,
				CommissionEligible: true, SignedAt: start, PaidOrderIDs: []string{"31", "32"}, ContentHash: "ab"}}}); err != nil {
			return err
		}
		amount := int64(100000)
		at := start.Add(-time.Hour)
		facts := []sourceFact{}
		for _, order := range []string{"31", "32"} {
			facts = append(facts, sourceFact{Source: "DCZD", Domain: "PRACTICE", SourceKey: "archive-pool", Kind: "ORDER_PAID",
				OrderID: order, PaymentID: order, RecordKey: "payment:" + order, Version: "1", AmountMinor: &amount, OccurredAt: at, UpdatedAt: at,
				ReferrerID: "seller"})
		}
		_, err := storeSourceFacts(ctx, tx, facts)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: period}); err != nil {
		t.Fatal(err)
	}
	var input financeHistoryInput
	if err := s.db.QueryRow(ctx, "SELECT current_database(),current_schema()").Scan(&input.Database, &input.Schema); err != nil {
		t.Fatal(err)
	}
	input.ProductCode, input.PeriodID, input.ConfirmedAt = "ARCHIVE_POOL", period, start.AddDate(0, 0, 3)
	if err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		totals, err := readFinanceHistoryTotals(ctx, tx, period)
		input.Expected = &totals
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var first, second int64
	if err := s.db.QueryRow(ctx, `SELECT min(l.id) FILTER(WHERE o.order_id='31'),min(l.id) FILTER(WHERE o.order_id='32')
		FROM calculation_draft_lines l JOIN source_order_versions o ON o.id=l.source_version_id
		WHERE l.period_id=$1 AND l.practice_pool_id=$2`,
		period, pool).Scan(&first, &second); err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("need two funding lots to exercise exact historical allocation")
	}
	poolScope := poolScope{Unit: unit, Item: fmt.Sprint(pool), Scenario: "class"}
	var basis string
	if err := s.db.QueryRow(ctx, poolAllocationBasisSQL+"$1", period).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if _, err := s.savePoolAllocation(ctx, savePoolAllocationInput{PeriodID: period, Version: 1, Basis: basis, poolScope: poolScope,
		Shares: []poolAllocationShare{{PersonID: recipient, Amount: 1000}}}); !errors.Is(err, errPoolEligibility) {
		t.Fatalf("ordinary allocation must still check member eligibility: %v", err)
	}
	input.PoolFunding = []financeHistoryPool{{poolScope: poolScope,
		Funding: []financeHistoryFundingCell{{LineID: first, PersonID: recipient, Amount: 1000}, {LineID: second, PersonID: 0, Amount: 1000}}}}
	for _, test := range []struct {
		name  string
		cells []financeHistoryFundingCell
	}{
		{"repeated pair", []financeHistoryFundingCell{{first, recipient, 500}, {first, recipient, 500}}},
		{"unavailable lot", []financeHistoryFundingCell{{second + 100, recipient, 1000}}},
		{"overallocated lot", []financeHistoryFundingCell{{first, recipient, 1001}, {second, 0, 999}}},
		{"unknown recipient", []financeHistoryFundingCell{{first, recipient + 100, 1000}, {second, 0, 1000}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			wrong := input
			wrong.PoolFunding = []financeHistoryPool{{poolScope: input.PoolFunding[0].poolScope, Funding: test.cells}}
			if _, err := s.runFinanceHistory(ctx, wrong, false, nil); !errors.Is(err, errFinanceHistoryPoolFunding) {
				t.Fatalf("expected invalid historical allocation, got %v", err)
			}
		})
	}
	var rows bytes.Buffer
	preview, err := s.runFinanceHistory(ctx, input, false, &rows)
	if err != nil || preview.PostingsSHA256 == "" || preview.Settlement != nil {
		t.Fatalf("read-only history pool preview failed: %v", err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(rows.Bytes())) != preview.PostingsSHA256 || !bytes.Contains(rows.Bytes(), []byte("archivedPoolFunding")) {
		t.Fatal("archived allocation is missing from reviewed rows")
	}
	var count int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM monthly_pool_allocations").Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview saved pool plans: count=%d error=%v", count, err)
	}
	input.PostingsSHA256 = preview.PostingsSHA256
	preview, err = s.runFinanceHistory(ctx, input, false, nil)
	if err != nil || preview.Token == "" {
		t.Fatalf("reconciled history pool preview failed: %v", err)
	}
	input.Token = preview.Token

	changed := input
	changed.PoolFunding = []financeHistoryPool{{poolScope: input.PoolFunding[0].poolScope, Funding: []financeHistoryFundingCell{
		{LineID: first, PersonID: 0, Amount: 1000}, {LineID: second, PersonID: recipient, Amount: 1000}}}}
	if _, err := s.runFinanceHistory(ctx, changed, true, nil); !errors.Is(err, errFinanceHistoryPostings) {
		t.Fatalf("same amounts with different source funding must fail reconciliation: %v", err)
	}
	final, err := s.runFinanceHistory(ctx, input, true, nil)
	if err != nil || final.Settlement == nil {
		t.Fatalf("apply exact history pool funding: %v", err)
	}
	reordered := input
	reordered.PoolFunding = []financeHistoryPool{{poolScope: input.PoolFunding[0].poolScope, Funding: []financeHistoryFundingCell{
		input.PoolFunding[0].Funding[1], input.PoolFunding[0].Funding[0]}}}
	repeat, err := s.runFinanceHistory(ctx, reordered, true, nil)
	if err != nil || repeat.Settlement == nil || repeat.Settlement.ID != final.Settlement.ID {
		t.Fatalf("retry of archived pool funding changed settlement: %v", err)
	}
	if _, err := s.runFinanceHistory(ctx, changed, true, nil); !errors.Is(err, errSettlementClosed) {
		t.Fatalf("changed plan must fail retry: %v", err)
	}
	var frozenRows bytes.Buffer
	if _, err := s.runFinanceHistory(ctx, input, false, &frozenRows); err != nil || !bytes.Equal(rows.Bytes(), frozenRows.Bytes()) {
		t.Fatalf("frozen export must retain exact reviewed rows: %v", err)
	}
	var fundedLine, fundedPerson, amount int64
	if err := s.db.QueryRow(ctx, "SELECT f.source_line_id,s.person_id,f.amount_minor FROM monthly_pool_allocation_funding f JOIN monthly_pool_allocation_shares s ON s.id=f.share_id WHERE s.person_id IS NOT NULL").
		Scan(&fundedLine, &fundedPerson, &amount); err != nil || fundedLine != first || fundedPerson != recipient || amount != 1000 {
		t.Fatalf("original archived funding was not preserved: %d/%d/%d %v", fundedLine, fundedPerson, amount, err)
	}
	if _, err := s.db.Exec(ctx, "UPDATE monthly_pool_allocation_funding SET amount_minor=1"); err == nil {
		t.Fatal("confirmed pool funding was mutable")
	}

	refundPeriod := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "九月退款", SettlementMonth: "2025-09"}))
	amount = 100000
	refundedAt := start.AddDate(0, 1, 4)
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{{Source: "DCZD", Domain: "PRACTICE", SourceKey: "archive-pool",
			OrderID: "31", Kind: "REFUND_SUCCEEDED", RefundID: "91", RecordKey: "refund:91", Version: "1",
			AmountMinor: &amount, OccurredAt: refundedAt, UpdatedAt: refundedAt,
			RefundsPayment: []sourcePaymentReference{{OrderID: "31", RecordID: "31"}}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.recalculateDraft(ctx, calculateDraftInput{PeriodID: refundPeriod}); err != nil {
		t.Fatal(err)
	}
	var refundMinor int64
	if err := s.db.QueryRow(ctx, `SELECT coalesce(sum(amount_minor),0) FROM calculation_draft_refunds
		WHERE period_id=$1 AND original_line_id=$2`, refundPeriod, first).Scan(&refundMinor); err != nil || refundMinor != -1000 {
		t.Fatalf("later refund must debit the archived source lot: %d %v", refundMinor, err)
	}
	if _, err := s.runFinanceHistory(ctx, input, true, nil); err != nil {
		t.Fatalf("later refund must preserve the original settlement and funding: %v", err)
	}
}
