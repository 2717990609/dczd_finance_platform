package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestSourceOrderDisplayBackfillKeepsFactVersion(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	base := `{"fact_schema_version":"source.order-fact/1.0","fact_type":"ORDER_PAID","occurred_at":"2026-03-18T09:00:00Z","order":{"source_order_id":"1100","order_no":"=1+1"},"source_record":{"source_table":"vip_order","record_id":"1100","record_version":"1","updated_at":"2026-03-18T09:00:00Z"},"business":{"source_domain_code":"VIP","source_product_code":"VIP"},"amounts":{"paid_minor":"49900"},"payment":{"source_payment_id":"1100"}}`
	rich := strings.ReplaceAll(base, `"paid_minor":"49900"`, `"paid_minor":"49900","gross_minor":"59900","source_settlement_basis_minor":"39800"`)
	rich = strings.ReplaceAll(rich, `"source_payment_id":"1100"`, `"source_payment_id":"1100","method_code":"4","paid_at":"2026-03-18T09:00:00Z","provider_txn_id":"TX-TEST"`)
	rich = strings.TrimSuffix(rich, "}") + `,"extensions":{"attributes":{"correction_item_id":"11","correction_batch_no":"TEST","correction_created_at":"2026-03-18T10:00:00Z"},"finance_order_display":{"customer_nickname":"客户示例","customer_phone":"13000000000","referrer_phone":"13123456789","seller_phone":"13234567890","referrer_name":"推荐人示例","referrer_company_name":"示例公司"}}}`
	save := func(raw string, expected int64) {
		t.Helper()
		fact, err := normalizeFact("DCAI", "1100", json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			n, err := storeSourceFacts(ctx, tx, []sourceFact{fact})
			if err == nil && n != expected {
				t.Fatalf("inserted %d versions, expected %d", n, expected)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	save(base, 1)
	save(rich, 0)
	save(base, 0)
	var id, factJSON, customer, referrer, seller string
	if err := db.QueryRow(ctx, `SELECT v.id::text,v.fact::text,d.details->>'customerPhone',d.details->>'referrerPhone',d.details->>'sellerPhone'
	 FROM source_order_versions v JOIN source_order_display d ON d.source_version_id=v.id WHERE v.order_id='1100'`).
		Scan(&id, &factJSON, &customer, &referrer, &seller); err != nil {
		t.Fatal(err)
	}
	if customer != "13000000000" || referrer != "13123456789" || seller != "13234567890" {
		t.Fatal("original phone numbers should be retained in the display sidecar")
	}
	for _, phone := range []string{customer, referrer, seller} {
		if strings.Contains(factJSON, phone) {
			t.Fatal("contact must stay out of canonical facts")
		}
	}
	reply := httptest.NewRecorder()
	workspace{db: db}.orderVersion(reply, httptest.NewRequest(http.MethodGet, "/api/v1/orders/version?id="+id, nil))
	var got struct {
		AmountMinor int64
		Display     struct {
			CustomerNickname, CustomerPhone, ReferrerName, ReferrerPhone, SellerPhone, ReferrerCompanyName, PaymentMethod, CorrectionItemID string
			OriginalAmountMinor, SourceSettlementBasisMinor                                                                                 int64
		}
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &got); err != nil || reply.Code != http.StatusOK ||
		got.AmountMinor != 49900 || got.Display.SourceSettlementBasisMinor != 39800 ||
		got.Display.OriginalAmountMinor != 59900 || got.Display.PaymentMethod != "4" ||
		got.Display.CustomerNickname != "客户示例" || got.Display.CustomerPhone != "130****0000" ||
		got.Display.ReferrerPhone != "131****6789" || got.Display.SellerPhone != "132****7890" ||
		got.Display.ReferrerName != "推荐人示例" || got.Display.ReferrerCompanyName != "示例公司" || got.Display.CorrectionItemID != "11" {
		t.Errorf("unexpected version display %d %+v (%v)", reply.Code, got, err)
	}
	t.Run("list", func(t *testing.T) {
		w := httptest.NewRecorder()
		workspace{db: db}.orders(w, httptest.NewRequest(http.MethodGet, "/api/v1/orders?source=DCAI&orderId=1100", nil))
		var rows []struct{ Display map[string]string }
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || w.Code != http.StatusOK || len(rows) != 1 ||
			rows[0].Display["customerPhone"] != "130****0000" || rows[0].Display["referrerPhone"] != "131****6789" ||
			rows[0].Display["sellerPhone"] != "132****7890" {
			t.Fatalf("unexpected list contact projection: HTTP %d, %d rows, %v", w.Code, len(rows), err)
		}
	})
	t.Run("export", func(t *testing.T) {
		w := httptest.NewRecorder()
		workspace{db: db}.orders(w, httptest.NewRequest(http.MethodGet, "/api/v1/orders?source=DCAI&orderId=1100&format=csv", nil))
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") ||
			!strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatalf("expected a downloadable CSV, HTTP %d %v", w.Code, w.Header())
		}
		records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\uFEFF"))).ReadAll()
		if err != nil || len(records) != 2 || records[1][4] != "499.00" || !strings.Contains(w.Body.String(), "'=1+1") ||
			!strings.Contains(w.Body.String(), "130****0000") || !strings.Contains(w.Body.String(), "131****6789") ||
			!strings.Contains(w.Body.String(), "132****7890") {
			t.Fatalf("CSV should contain one row, safe text and masked phones: %d rows, %v", len(records), err)
		}
		for _, phone := range []string{customer, referrer, seller} {
			if strings.Contains(w.Body.String(), phone) {
				t.Fatal("CSV exposed an original phone number")
			}
		}
	})
}

func TestPracticeRefundDisplayUsesReferencedPaymentVersion(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	paidAt := time.Date(2026, time.March, 17, 9, 0, 0, 0, time.UTC)
	refundedAt := paidAt.Add(48 * time.Hour)
	payment := sourceFact{Source: "DCZD", OrderID: "1100", LineID: "1", RecordKey: "practice_paid:101", Version: "1",
		Kind: "ORDER_PAID", Domain: "PRACTICE", SourceKey: "FAMILY", OccurredAt: paidAt, UpdatedAt: paidAt,
		Display: orderDisplayFields{"customerNickname": "原客户", "customerPhone": "13000000000", "referrerName": "原报名销售", "paymentMethod": "1"}}
	newVersion := payment
	newVersion.Version, newVersion.UpdatedAt = "2", refundedAt.Add(24*time.Hour)
	newVersion.Display = orderDisplayFields{"referrerName": "新报名销售"}
	other := payment
	other.LineID, other.RecordKey = "2", "practice_paid:102"
	other.Display = orderDisplayFields{"customerNickname": "另一笔付款"}
	refund := sourceFact{Source: "DCZD", OrderID: "1200", RecordKey: "practice_refund:201", Version: "1",
		Kind: "REFUND_SUCCEEDED", Domain: "PRACTICE", SourceKey: "FAMILY", OccurredAt: refundedAt, UpdatedAt: refundedAt,
		RefundsPayment: []sourcePaymentReference{{OrderID: "1100", LineID: "1", RecordID: "101"}},
		Display:        orderDisplayFields{"refundNo": "REFUND-201", "refundedAt": refundedAt}}
	if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		_, err := storeSourceFacts(ctx, tx, []sourceFact{payment, newVersion, other, refund})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(ctx, `SELECT id::text FROM source_order_versions WHERE order_id='1200'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	reply := httptest.NewRecorder()
	workspace{db: db}.orderVersion(reply, httptest.NewRequest(http.MethodGet, "/api/v1/orders/version?id="+id, nil))
	var got struct{ Display map[string]any }
	if err := json.Unmarshal(reply.Body.Bytes(), &got); err != nil || reply.Code != http.StatusOK ||
		got.Display["customerNickname"] != "原客户" || got.Display["customerPhone"] != "130****0000" || got.Display["referrerName"] != "原报名销售" ||
		got.Display["paymentMethod"] != "1" || got.Display["refundNo"] != "REFUND-201" {
		t.Fatalf("unexpected linked refund display %d %+v (%v)", reply.Code, got, err)
	}
}
