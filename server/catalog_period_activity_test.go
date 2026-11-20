package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCatalogPeriodActivityReadsLatestPaidAndOriginalRefundScenario(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	const product = 901
	for _, statement := range []string{
		`INSERT INTO products(id,code,name,period_mode,cycle_start_day) OVERRIDING SYSTEM VALUE
		 VALUES(901,'ACTIVITY','整期数据样本','calendar',25)`,
		`INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
		 VALUES(901,gen_random_uuid(),'test','DCAI','DCAI','sample','2026-10-01',decode(repeat('00',32),'hex'))`,
		`INSERT INTO catalog_business_routes(source_id,product_id,source,domain,entity_type,record_table,business_key,
		  classifier,sku_code,sku_label,mapping_version,effective_from,observed_at,source_product_code) VALUES
		 (gen_random_uuid(),901,'DCAI','VIP','ORDER','vip','sample','VIP','FIRST','会员',1,'2026-01-01','2026-10-01','sample'),
		 (gen_random_uuid(),901,'DCAI','HEALTH_IDENTITY','ORDER','health','sample','HEALTH','SECOND','健康',1,'2026-01-01','2026-10-01','sample'),
		 (gen_random_uuid(),901,'DCAI','VIP','ORDER','vip','sample','EXCLUDED','OTHER','其他',1,'2026-01-01','2026-10-01','sample')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	window, _ := calendarWindow("2026-10", 1)
	insert := func(fact sourceFact, classifier string) {
		t.Helper()
		body, err := json.Marshal(fact)
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := db.QueryRow(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, fact.Source, fact.OrderID, fact.LineID,
			fact.RecordKey, fact.Version, fact.Kind, fact.UpdatedAt, body, fact.OccurredAt).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if classifier != "" {
			if _, err := db.Exec(ctx, `INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier)
			 VALUES($1,'ORDER','sample',$2)`, id, classifier); err != nil {
				t.Fatal(err)
			}
		}
	}
	payment := func(id, domain, table string, amount int64, when time.Time) sourceFact {
		return sourceFact{Source: "DCAI", Kind: "ORDER_PAID", Domain: domain, SourceKey: "sample", OrderID: id, PaymentID: id,
			RecordKey: table + ":" + id, Version: "1", OccurredAt: when, UpdatedAt: when, AmountMinor: &amount}
	}
	refund := func(id, refundID string, original sourceFact, amount int64) sourceFact {
		at := window.Start.Add(time.Hour)
		return sourceFact{Source: "DCAI", Kind: "REFUND_SUCCEEDED", Domain: original.Domain, SourceKey: "sample",
			OrderID: id, RefundID: refundID, RecordKey: original.RecordKey, Version: "1", OccurredAt: at, UpdatedAt: at,
			RefundsPayment: []sourcePaymentReference{{OrderID: original.OrderID, RecordID: original.OrderID}}, AmountMinor: &amount}
	}
	vip := payment("10", "VIP", "vip", 10000, window.Start)
	insert(vip, "VIP")
	vip.Version, vip.UpdatedAt = "2", window.Start.Add(time.Minute)
	newAmount := int64(12000)
	vip.AmountMinor = &newAmount
	insert(vip, "VIP")
	old := payment("11", "VIP", "vip", 10000, window.Start.Add(-time.Second))
	insert(old, "VIP")
	health := payment("20", "HEALTH_IDENTITY", "health", 20000, window.Start)
	insert(health, "HEALTH")
	insert(payment("12", "VIP", "vip", 7000, window.Start), "EXCLUDED")
	insert(payment("13", "VIP", "vip", 123, window.Start), "")
	insert(payment("14", "VIP", "vip", 40000, window.Through), "VIP")
	insert(refund("11", "a", old, 3000), "")
	insert(refund("refund-1", "b", vip, 1000), "")
	insert(refund("refund-2", "b", vip, 1000), "")
	missing := refund("missing", "c", vip, 1000)
	missing.RefundsPayment[0].OrderID = "unknown"
	insert(missing, "")
	insert(refund("refund-3", "d", health, 12500), "")
	insert(refund("refund-4", "e", health, 12500), "")
	older := payment("15", "VIP", "vip", 10000, window.Start.Add(-time.Hour))
	insert(older, "VIP")
	olderRefund := refund("15", "f", older, 9000)
	olderRefund.OccurredAt, olderRefund.UpdatedAt = window.Start.Add(-time.Minute), window.Start.Add(-time.Minute)
	insert(olderRefund, "")
	insert(refund("refund-5", "g", older, 2000), "")
	in := catalogPeriodActivityRequest{ProductID: product, Scenarios: []string{"FIRST", "SECOND", "MISSING"}, Window: window}
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		report, err := readCatalogPeriodActivity(ctx, tx, in)
		if err != nil {
			return err
		}
		if report.Receipts != 32000 || report.Refunds != 3000 || report.PriorPeriodRefunds != 3000 ||
			report.Issues["UNCLASSIFIED"] != 1 || report.Issues["DUPLICATE_EVENT"] != 2 ||
			report.Issues["REFUND_ORIGIN_MISSING"] != 1 || report.Issues["REFUND_EXCEEDS_PAYMENT"] != 3 {
			t.Fatalf("unexpected activity: %+v", report)
		}
		if len(report.Scenarios) != 3 || report.Scenarios[2].Mapped {
			t.Fatalf("missing scenario coverage was not reported: %+v", report.Scenarios)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
