package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
