package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCompanyCategoryMaintenanceUsesEffectiveHistory(t *testing.T) {
	s := workspace{db: integrationDB(t), development: true}
	ctx := context.Background()
	const unit = "00000000-0000-4000-8000-000000000061"
	const oldCategory = "00000000-0000-4000-8000-000000000062"
	_, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,kind,name,status,sales_company_type) VALUES
 ('00000000-0000-4000-8000-000000000061','COMPANY','公司样本','ACTIVE','EXTERNAL');
 INSERT INTO organization_sales_categories(id,name,active) VALUES ('00000000-0000-4000-8000-000000000062','原类别',true);
 INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from)
 VALUES ('00000000-0000-4000-8000-000000000061','00000000-0000-4000-8000-000000000062','2025-01-01T00:00:00Z');
 INSERT INTO products(code,name,period_mode) VALUES ('CATEGORY_TEST','类别产品','calendar');
 INSERT INTO product_catalog_settings(product_id,source_id,source_environment,project_code,source_system,source_key,captured_at,snapshot_digest)
 SELECT id,'00000000-0000-4000-8000-000000000064','test','AI','DCAI','sample',now(),decode(repeat('00',32),'hex') FROM products;
 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on)
 SELECT id,p.title,p.ordinal,current_date-1,current_date+2 FROM products
 CROSS JOIN (VALUES ('开放账期',1),('已结算账期',2)) p(title,ordinal);
 INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
 SELECT id,1,''::bytea,1200,0,0,now() FROM periods WHERE ordinal=2;
 INSERT INTO settlements(period_id,draft_version,confirmed_by)
 SELECT id,1,'category fixture' FROM periods WHERE ordinal=2;
 INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest) VALUES('DCAI','2025-01-02',''::bytea);
 INSERT INTO catalog_sales_company_aliases(id,source,external_id,company_uuid,company_unit_id,valid_from,observed_at)
 VALUES ('00000000-0000-4000-8000-000000000066','DCAI','source-company','00000000-0000-4000-8000-000000000067',
 '00000000-0000-4000-8000-000000000061','2025-01-01','2025-01-02');
 INSERT INTO catalog_sales_categories(source,company_uuid,category_uuid,valid_from,observed_at)
 VALUES ('DCAI','00000000-0000-4000-8000-000000000067','00000000-0000-4000-8000-000000000062','2025-01-01','2025-01-02');
 INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,occurred_at,fact)
 VALUES('DCAI','sample','sample','sample','1','ORDER_PAID',now(),now(),'{}');
 INSERT INTO source_order_classifications(source_version_id,entity_type,business_key,classifier,sales_claims)
 SELECT id,'VIP_ORDER','sample','sample','[{"role":"PID_USER","user":"sample","company":"source-company","relationship":"DIRECT"}]'::jsonb
 FROM source_order_versions;`)
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.createOrganizationCategory(ctx, organizationCategoryInput{Name: " 新类别 "})
	if err != nil || created.Name != "新类别" {
		t.Fatalf("new category: %+v %v", created, err)
	}
	_, err = s.createOrganizationCategory(ctx, organizationCategoryInput{Name: "新类别"})
	if !errors.Is(err, errOrganizationCategoryName) {
		t.Fatalf("duplicate name: %v", err)
	}
	before := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	input := assignOrganizationCategoryInput{CategoryID: created.ID, Companies: []categoryCompanyVersion{{UnitID: unit, ExpectedCategoryID: oldCategory, ExpectedFrom: &before}}}
	restricted := context.WithValue(ctx, financePrincipalKey{}, &financePrincipal{Workspace: true})
	if _, err = s.assignOrganizationCategory(restricted, input); !errors.Is(err, errFinanceForbidden) {
		t.Fatalf("write without permission: %v", err)
	}
	changed, err := s.assignOrganizationCategory(ctx, input)
	if err != nil || changed.Changed != 1 || changed.EffectiveAt == nil {
		t.Fatalf("assign category: %+v %v", changed, err)
	}
	duplicate, err := s.assignOrganizationCategory(ctx, input)
	if err != nil || duplicate.Changed != 0 {
		t.Fatalf("assign retry: %+v %v", duplicate, err)
	}
	stale := input
	stale.CategoryID = oldCategory
	if _, err = s.assignOrganizationCategory(ctx, stale); !errors.Is(err, errOrganizationCategoryChanged) {
		t.Fatalf("stale assignment: %v", err)
	}
	if _, err = s.updateOrganizationCategory(ctx, updateOrganizationCategoryInput{ID: created.ID, ExpectedVersion: created.Version, Name: created.Name, Active: false}); !errors.Is(err, errOrganizationCategoryInUse) {
		t.Fatalf("disabled category in use: %v", err)
	}
	var through time.Time
	err = s.db.QueryRow(ctx, "SELECT valid_to FROM organization_sales_category_history WHERE unit_id=$1 AND category_id=$2", unit, oldCategory).Scan(&through)
	if err != nil || !through.Equal(*changed.EffectiveAt) {
		t.Fatalf("historical boundary: %v %v", through, err)
	}
	// An imported interval extending through the local cutover must not reopen it.
	snapshot := organizationSnapshot{
		SalesCategories:      json.RawMessage(`[{"id":"` + created.ID + `","name":"旧快照名称","active":false}]`),
		SalesCategoryHistory: json.RawMessage(`[{"unitId":"` + unit + `","categoryId":"` + oldCategory + `","from":"2025-01-01T00:00:00Z"}]`),
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return importOrganizationSalesCategories(ctx, tx, snapshot) }); err != nil {
		t.Fatal(err)
	}
	var stillThrough time.Time
	if err := s.db.QueryRow(ctx, "SELECT valid_to FROM organization_sales_category_history WHERE unit_id=$1 AND category_id=$2", unit, oldCategory).Scan(&stillThrough); err != nil || !stillThrough.Equal(through) {
		t.Fatalf("reimport reopened previous category: %v %v", stillThrough, err)
	}
	var selectable string
	if err := s.db.QueryRow(ctx, "SELECT label FROM catalog_sales_category_options WHERE key=$1 AND active", created.ID).Scan(&selectable); err != nil || selectable != created.Name {
		t.Fatalf("edited category absent from rule options after import: %s %v", selectable, err)
	}
	var period, version int64
	if err := s.db.QueryRow(ctx, "SELECT id FROM periods WHERE ordinal=1").Scan(&period); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(ctx, "SELECT id FROM source_order_versions WHERE order_id='sample'").Scan(&version); err != nil {
		t.Fatal(err)
	}
	var booked int64
	var pending []int64
	if err := s.db.QueryRow(ctx, "SELECT array_agg(period_id ORDER BY period_id) FROM draft_refresh_queue").Scan(&pending); err != nil || len(pending) != 1 || pending[0] != period {
		t.Fatalf("refresh queue: %v %v", pending, err)
	}
	if err := s.db.QueryRow(ctx, "SELECT d.total_minor FROM calculation_drafts d JOIN settlements s USING(period_id)").Scan(&booked); err != nil || booked != 1200 {
		t.Fatalf("settlement changed: %d %v", booked, err)
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		amount := int64(10000)
		old := catalogPaymentMatch{SourceVersionID: version, PaidAt: through.Add(-time.Microsecond), PaidMinor: &amount}
		next := old
		next.PaidAt = through
		data, err := loadVIPRuntimeData(ctx, tx, period, []catalogPaymentMatch{old, next}, true)
		if err != nil {
			return err
		}
		rates := []vipRateRow{{Company: "ANY", Relationship: "ANY", Category: oldCategory, Rate: 500},
			{Company: "ANY", Relationship: "ANY", Category: created.ID, Rate: 800}}
		for _, result := range []struct {
			order    catalogPaymentMatch
			category string
			rate     int64
		}{{old, oldCategory, 500}, {next, created.ID, 800}} {
			req, issue := vipPaymentInputs(result.order, data)
			if issue != "" || req.Category == nil || *req.Category != result.category {
				t.Fatalf("category at payment time: %+v %s", req, issue)
			}
			if rate, issue := resolveVIPRate(rates, req.Company, req.Relationship, req.Category); issue != "" || rate != result.rate {
				t.Fatalf("rate at payment time: %d %s", rate, issue)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
