package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestOrganizationCompanyTypeMaintainsIdentityAndHistory(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	const unit = "00000000-0000-4000-8000-000000000071"
	const unedited = "00000000-0000-4000-8000-000000000072"
	const snapshot = `{"capturedAt":"2025-01-01T00:00:00Z","people":[],"assignments":[],
	"units":[{"id":"` + unit + `","name":"历史公司","kind":"COMPANY","status":"INACTIVE","salesCompanyType":"UNKNOWN"},
	 {"id":"` + unedited + `","name":"来源维护公司","kind":"COMPANY","status":"ACTIVE","salesCompanyType":"INTERNAL"}]}`
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(snapshot)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO products(code,name,period_mode) VALUES
	 ('CATEGORY_TYPE_DCAI','AI 公司类型','calendar'),('CATEGORY_TYPE_PRACTICE','实战班公司类型','class'),
	 ('CATEGORY_TYPE_FIXED','实战班统一比例','class');
	 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
	 SELECT id,gen_random_uuid(),'test','AI','DCAI','company-type-fixture',now(),sha256('fixture'::bytea) FROM products WHERE code='CATEGORY_TYPE_DCAI';
	 INSERT INTO source_mappings(product_id,source,domain,source_key)
	 SELECT id,'DCZD','PRACTICE','company-type-fixture' FROM products WHERE code='CATEGORY_TYPE_PRACTICE';
	 INSERT INTO source_mappings(product_id,source,domain,source_key)
	 SELECT id,'DCZD','PRACTICE','fixed-rate-fixture' FROM products WHERE code='CATEGORY_TYPE_FIXED';
	 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from) VALUES
	 ('DCAI','company-type-fixture','00000000-0000-4000-8000-000000000071','2025-01-01'),
	 ('DCZD','company-type-fixture','00000000-0000-4000-8000-000000000071','2025-01-01');
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on)
	 SELECT p.id,period.title,period.ordinal,current_date-1,current_date+2 FROM products p
	 CROSS JOIN (VALUES('开放期',1),('已确认期',2)) period(title,ordinal);
	 INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	 SELECT id,1,''::bytea,1200,0,0,now() FROM periods WHERE ordinal=2;
	 INSERT INTO settlements(period_id,draft_version,confirmed_by)
	 SELECT id,1,'company-type fixture' FROM periods WHERE ordinal=2;
	 INSERT INTO rule_versions(product_id,version,label,components)
	 SELECT id,1,'外部公司默认比例','[{"type":"company_sales","mode":"company_sales","classMember":"new",
	  "sourceRole":"DIRECT_REFERRER","companySales":{"label":"外部公司默认比例","categories":[],"defaultRate":80000}}]'::jsonb
	 FROM products WHERE code='CATEGORY_TYPE_PRACTICE';
	 INSERT INTO rule_versions(product_id,version,label,components)
	 SELECT id,1,'统一销售比例','[{"type":"sales","mode":"rate","classMember":"new","sourceRole":"DIRECT_REFERRER","value":1000}]'::jsonb
	 FROM products WHERE code='CATEGORY_TYPE_FIXED';`); err != nil {
		t.Fatal(err)
	}
	input := updateOrganizationCompanyTypeInput{UnitID: unit, CompanyType: "EXTERNAL", ExpectedVersion: 1}
	restricted := context.WithValue(ctx, financePrincipalKey{}, &financePrincipal{Workspace: true})
	if _, err := s.updateOrganizationCompanyType(restricted, input); !errors.Is(err, errFinanceForbidden) {
		t.Fatalf("unprivileged change: %v", err)
	}
	updated, err := s.updateOrganizationCompanyType(ctx, input)
	if err != nil || !updated.Changed || updated.Version != 2 {
		t.Fatalf("first change: %+v %v", updated, err)
	}
	queue := func() []int64 {
		t.Helper()
		var generations []int64
		var staleClosed bool
		err := s.db.QueryRow(ctx, `SELECT array_agg(q.generation ORDER BY q.period_id),
		 coalesce(bool_or(p.ordinal=2),false) FROM draft_refresh_queue q JOIN periods p ON p.id=q.period_id`).
			Scan(&generations, &staleClosed)
		if err != nil || staleClosed || len(generations) != 2 {
			t.Fatalf("company type change should refresh both open periods and no closed period: %v %t %v", generations, staleClosed, err)
		}
		return generations
	}
	queued := queue()
	retry, err := s.updateOrganizationCompanyType(ctx, input)
	if err != nil || retry.Changed || retry.Version != 2 {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	if unchanged := queue(); unchanged[0] != queued[0] || unchanged[1] != queued[1] {
		t.Fatalf("unchanged retry must not enqueue a new generation: %v to %v", queued, unchanged)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var effectiveAt time.Time
		if err := tx.QueryRow(ctx, `SELECT changed_at FROM organization_sales_company_type_changes WHERE unit_id=$1 AND version=2`, unit).
			Scan(&effectiveAt); err != nil {
			return err
		}
		payment := func(id string, at time.Time) paymentInput {
			return paymentInput{Fact: sourceFact{SalesCompanyID: "company-type-fixture", PaymentID: id, OccurredAt: at}}
		}
		contexts, err := loadPracticeCompanyClassifications(ctx, tx, map[int64]practiceCompanyClassificationScope{
			1: {at: effectiveAt, payments: []paymentInput{payment("before", effectiveAt.Add(-time.Microsecond)), payment("after", effectiveAt)}},
		}, false, true)
		category := contexts[1].sales
		if err != nil {
			return err
		}
		if externalCompanyDefaultEligible(category["before"]) || category["after"] != practiceUncategorizedCompany {
			t.Fatalf("the external default only starts at the recorded change time: %v", category)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	input.CompanyType = "INTERNAL"
	if _, err := s.updateOrganizationCompanyType(ctx, input); !errors.Is(err, errOrganizationCompanyTypeChanged) {
		t.Fatalf("stale change: %v", err)
	}
	input.ExpectedVersion = 2
	updated, err = s.updateOrganizationCompanyType(ctx, input)
	if err != nil || !updated.Changed || updated.Version != 3 {
		t.Fatalf("second change: %+v %v", updated, err)
	}
	newerSnapshot := `{"capturedAt":"2025-01-02T00:00:00Z","people":[],"assignments":[],
	"units":[{"id":"` + unit + `","name":"历史公司","kind":"COMPANY","status":"INACTIVE","salesCompanyType":"EXTERNAL"},
	 {"id":"` + unedited + `","name":"来源维护公司","kind":"COMPANY","status":"ACTIVE","salesCompanyType":"EXTERNAL"}]}`
	if err := storeOrganizationSnapshot(ctx, s.db, json.RawMessage(newerSnapshot)); err != nil {
		t.Fatal(err)
	}
	var companyType, status string
	var changes, correctHistory int
	err = s.db.QueryRow(ctx, `SELECT sales_company_type,status,
	 (SELECT count(*) FROM organization_sales_company_type_changes t WHERE t.unit_id=u.id),
	 (SELECT count(*) FROM organization_sales_company_type_changes t WHERE t.unit_id=u.id AND
	  (t.version=2 AND t.previous_type='UNKNOWN' AND t.company_type='EXTERNAL' OR t.version=3 AND t.previous_type='EXTERNAL' AND t.company_type='INTERNAL'))
	 FROM organization_units u WHERE u.id=$1`, unit).Scan(&companyType, &status, &changes, &correctHistory)
	if err != nil || companyType != "INTERNAL" || status != "INACTIVE" || changes != 2 || correctHistory != 2 {
		t.Fatalf("company or history not preserved: %s %s %d %d %v", companyType, status, changes, correctHistory, err)
	}
	var version int64
	if err := s.db.QueryRow(ctx, "SELECT sales_company_type,sales_company_type_version FROM organization_units WHERE id=$1", unedited).
		Scan(&companyType, &version); err != nil || companyType != "EXTERNAL" || version != 2 {
		t.Fatalf("source change on unedited company: %s %d %v", companyType, version, err)
	}

}
