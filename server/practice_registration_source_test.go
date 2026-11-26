package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

const practiceRegistrationSourceFixture = `{
 "fact_schema_version":"source.order-fact/1.0","fact_type":"DELIVERY_COMPLETED","occurred_at":"2026-06-08T02:00:00Z",
 "source_record":{"source_table":"dc_offline_order_log_t","record_id":"1701","record_version":"v1","updated_at":"2026-06-08T02:00:00Z"},
 "order":{"source_order_id":"1100"},
 "business":{"source_domain_code":"PRACTICE","source_product_code":"PRACTICE:2:4",
  "classification_fields":{"strategy_id":4,"delivery_activity_id":95,"delivery_activity_type":2,"attendance_type":3}},
 "delivery":{"source_delivery_id":"1701","method_code":"PRACTICE_ATTENDANCE_3"},
 "relations":[{"relation":"DELIVERS_ORDER","target_source_order_id":"1100"}],
 "attribution_snapshot":{"participants":[{"source_role_code":"DIRECT_REFERRER","source_user_id":"481"},
  {"source_role_code":"DIRECT_COMPANY","source_company_id":"30"},{"source_role_code":"SECONDARY_COMPANY","source_company_id":"60"}]}
}`

func TestPracticeRegistrationSourceUsesOnlyTheLinkedAttendance(t *testing.T) {
	for _, tc := range []struct {
		name, from, to, company, period string
	}{
		{"older record without delivery period", `"delivery_activity_id":95,"delivery_activity_type":2`, `"activity_type":1`, "30", ""},
		{"separately captured attendance period", `"delivery_activity_id":95,"delivery_activity_type":2`, `"attendance_activity_id":"95","attendance_activity_type":2`, "30", "PRACTICE:2:4:95"},
		{"different registration", `"source_delivery_id":"1701"`, `"source_delivery_id":"1709"`, "", ""},
		{"different order", `"target_source_order_id":"1100"`, `"target_source_order_id":"1107"`, "", ""},
		{"secondary company only", `"source_role_code":"DIRECT_COMPANY"`, `"source_role_code":"OTHER_COMPANY"`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(practiceRegistrationSourceFixture, tc.from, tc.to, 1)
			fact, err := normalizeFact("DCZD", "1100", json.RawMessage(raw))
			if err != nil || fact.SalesCompanyID != "" {
				t.Fatalf("attendance import should not change payment attribution: %+v %v", fact, err)
			}
			registration := fact.PracticeRegistration
			if tc.company == "" {
				if registration != nil {
					t.Fatalf("incomplete registration should not carry a signup company: %+v", registration)
				}
			} else if registration == nil || registration.CompanyID != tc.company || registration.PeriodKey != tc.period {
				t.Fatalf("unexpected registration data: %+v", registration)
			}
		})
	}
}

func TestPracticeRegistrationSourceRetainsCompanyAndEnrollmentOnOrderVersion(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	fixture := practiceRegistrationSourceFixture
	legacy := strings.Replace(fixture, `"source_delivery_id":"1701"`, `"source_delivery_id":null`, 1)
	save := func(raw string) (int64, error) {
		t.Helper()
		fact, err := normalizeFact("DCZD", "1100", json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			var err error
			n, err = storeSourceFacts(ctx, tx, []sourceFact{fact})
			return err
		})
		return n, err
	}
	if n, err := save(legacy); err != nil || n != 1 {
		t.Fatalf("original attendance import: %d %v", n, err)
	}
	if n, err := save(fixture); err != nil || n != 0 {
		t.Fatalf("re-reading the same source version must fill registration details: %d %v", n, err)
	}
	if n, err := save(fixture); err != nil || n != 0 {
		t.Fatalf("re-reading unchanged registration details: %d %v", n, err)
	}
	if _, err := save(strings.Replace(fixture, `"source_company_id":"30"`, `"source_company_id":"31"`, 1)); !errors.Is(err, conflict) {
		t.Fatalf("changing the company must require a new source version: %v", err)
	}
	var id string
	if err := db.QueryRow(ctx, `SELECT id::text FROM source_order_versions WHERE source='DCZD' AND order_id='1100'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	reply := httptest.NewRecorder()
	workspace{db: db}.orderVersion(reply, httptest.NewRequest("GET", "/api/v1/orders/version?id="+id, nil))
	var got struct {
		ReferrerID           string `json:"referrerId"`
		PracticeRegistration struct {
			CompanyID string `json:"companyId"`
			PeriodKey string `json:"periodKey"`
		} `json:"practiceRegistration"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &got); err != nil || reply.Code != 200 ||
		got.ReferrerID != "481" || got.PracticeRegistration.CompanyID != "30" ||
		got.PracticeRegistration.PeriodKey != "PRACTICE:2:4:95" {
		t.Fatalf("order version must show the signup seller and company, got %d %s %v", reply.Code, reply.Body, err)
	}
}

func TestSourceSyncCanBackfillAroundFrozenFacts(t *testing.T) {
	ctx := context.Background()
	db := integrationDB(t)
	paid, err := normalizeFact("DCZD", "1100", json.RawMessage(strings.ReplaceAll(practiceRegistrationSourceFixture, "DELIVERY_COMPLETED", "ORDER_PAID")))
	if err != nil {
		t.Fatal(err)
	}
	save := func(expected int64, facts ...sourceFact) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			count, err := storeSourceFacts(ctx, tx, facts)
			if err == nil && count != expected {
				t.Errorf("got %d inserted facts, expected %d", count, expected)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	save(1, paid)
	if _, err := db.Exec(ctx, `INSERT INTO products(code,name,period_mode) VALUES('FREEZE','Freeze','class');
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on) VALUES(1,'Fixture',1,'2026-06-08','2026-06-08');
	 INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	 VALUES(1,1,decode('00','hex'),0,0,0,now());
	 INSERT INTO calculation_draft_performance(period_id,source_version_id) SELECT 1,id FROM source_order_versions;
	 INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(1,1,'fixture')`); err != nil {
		t.Fatal(err)
	}
	paid.PracticeInactiveCompany = &sourcePracticeSignup{EnrollmentID: 95, ParticipantID: 12, StrategyID: 4}
	revision := paid
	revision.Version = "v2"
	save(1, paid, revision)
	var frozenModified, revisionRecorded bool
	if err := db.QueryRow(ctx, `SELECT bool_or(fact ? 'practiceInactiveCompany') FILTER (WHERE version='v1'),
	 bool_or(fact ? 'practiceInactiveCompany') FILTER (WHERE version='v2') FROM source_order_versions`).Scan(&frozenModified, &revisionRecorded); err != nil || frozenModified || !revisionRecorded {
		t.Fatalf("old fact changed=%t, new fact present=%t, err=%v", frozenModified, revisionRecorded, err)
	}
}
