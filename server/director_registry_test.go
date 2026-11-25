package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/2717990609/dczd_finance_platform/internal/director"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xuri/excelize/v2"
)

const directorFixtureSQL = `
 INSERT INTO products(code,name,period_mode) VALUES('FAMILY_STUB','家庭教育样本','class'),('OTHER_STUB','其他样本','class');
 INSERT INTO source_mappings(product_id,source,domain,source_key) VALUES(1,'DCZD','PRACTICE','PRACTICE:2:4'),(2,'DCZD','PRACTICE','3');
 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,source_period_key) SELECT 1,'样本期'||n,n,date '2026-01-01'+n,date '2026-01-01'+n,'PRACTICE:2:4:'||n FROM generate_series(1,4) n;
 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,source_period_key) VALUES(2,'其他班次',1,'2026-01-01','2026-01-01','PRACTICE:1:3:9');
 INSERT INTO practice_rosters(period_key,source_version,source_hash,starts_at,confirmed_at,attendance_count)
 SELECT source_period_key,1,'sample',starts_on::timestamp AT TIME ZONE 'Asia/Shanghai'+interval '9 hours',clock_timestamp(),0 FROM periods;
 INSERT INTO rule_versions(product_id,version,label,components) VALUES(1,1,'动态总监规则','[{"type":"manager","mode":"director_rate","value":30000}]');
 INSERT INTO rule_versions(product_id,period_id,version,label,components) VALUES(1,4,2,'无总监项','[]');
 INSERT INTO people(name) VALUES('样本甲'),('样本乙'),('其他成员'),('例外账号样本');
 INSERT INTO organization_units(id,kind,name,status,sales_company_type) VALUES
 ('42000000-0000-4000-8000-000000000001','COMPANY','甲公司','ACTIVE','EXTERNAL'),
 ('42000000-0000-4000-8000-000000000002','COMPANY','乙公司','ACTIVE','EXTERNAL'),
 ('42000000-0000-4000-8000-000000000003','COMPANY','丙公司','ACTIVE','EXTERNAL'),
 ('42000000-0000-4000-8000-000000000004','COMPANY','内部公司','ACTIVE','INTERNAL');
 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from) SELECT 'DCZD',(100+right(id::text,1)::int)::text,id,'2020-01-01' FROM organization_units;
 INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(1,'DCZD','201','2020-01-01'),(2,'DCZD','202','2020-01-01'),(3,'DCZD','203','2020-01-01'),(4,'DCZD','8504698','2020-01-01');
 INSERT INTO organization_assignments(id,person_id,unit_id,kind,valid_from) SELECT gen_random_uuid(),n,('42000000-0000-4000-8000-00000000000'||n)::uuid,'MEMBER','2020-01-01' FROM generate_series(1,4) n;
 INSERT INTO organization_sales_categories(id,name,active) VALUES('43000000-0000-4000-8000-000000000001','幸福驿站',true);
 INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from) VALUES('42000000-0000-4000-8000-000000000003','43000000-0000-4000-8000-000000000001','2020-01-01');
`

func TestDirectorRegistryPublicationAndManualChanges(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, directorFixtureSQL); err != nil {
		t.Fatal(err)
	}
	const companyA = "42000000-0000-4000-8000-000000000001"
	const companyB = "42000000-0000-4000-8000-000000000002"
	const companyC = "42000000-0000-4000-8000-000000000003"
	const companyInternal = "42000000-0000-4000-8000-000000000004"
	book := excelize.NewFile()
	defer book.Close()
	sheet := book.GetSheetName(0)
	cells := [][]string{director.Headers, {"1", "业务一部", "样本甲", "甲公司（代表）", "成员甲", "丙公司"}, {"2", "业务二部", "样本乙", "乙公司", "成员乙", "待核样本"}}
	for i, row := range cells {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := book.SetSheetRow(sheet, cell, &row); err != nil {
			t.Fatal(err)
		}
	}
	buffer, err := book.WriteToBuffer()
	if err != nil {
		t.Fatal(err)
	}
	req := directorRequest{PeriodID: 2, Edit: directorEdit{Kind: "import", FileName: "原表.xlsx", File: buffer.Bytes()}}
	p, err := s.previewDirectorEdit(ctx, req)
	if err != nil || len(p.Issues) == 0 {
		t.Fatalf("unresolved company accepted: %+v %v", p.Issues, err)
	}
	req.Edit.Bindings = []directorBinding{{Number: 2, Leader: "2", Company: companyB, Pending: true}}
	save := func(r directorRequest) (int64, directorPreview) {
		t.Helper()
		v, err := s.previewDirectorEdit(ctx, r)
		if err != nil || len(v.Issues) > 0 {
			t.Fatalf("preview: %+v %v", v.Issues, err)
		}
		r.RequestID = uuid.NewString()
		r.Expected = v.Hash
		id, err := s.saveDirectorEdit(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		again, err := s.saveDirectorEdit(ctx, r)
		if err != nil || id != again {
			t.Fatalf("retry: %d %d %v", id, again, err)
		}
		return id, v
	}
	first, p := save(req)
	if p.Document.Rows[0].LeaderID != "1" || p.Document.Rows[0].RecipientID != companyA || p.Document.Rows[0].MemberSourceCompanyID != "103" || !p.Document.Rows[1].PendingReview {
		t.Fatalf("unfrozen IDs: %+v", p.Document.Rows)
	}
	var queued []int64
	if err := s.db.QueryRow(ctx, "SELECT array_agg(period_id ORDER BY period_id) FROM draft_refresh_queue").Scan(&queued); err != nil || len(queued) != 2 || queued[0] != 2 || queued[1] != 3 {
		t.Fatalf("impacted periods: %v %v", queued, err)
	}
	applicable := func(period, want int64) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			id, _, err := readApplicableDirectorVersion(ctx, tx, 1, period)
			if id != want {
				t.Fatalf("period %d: got %d want %d", period, id, want)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	applicable(1, 0)
	applicable(2, first)
	applicable(3, first)
	applicable(5, 0)
	if _, err := s.db.Exec(ctx, `INSERT INTO calculation_drafts(period_id,version,input_digest,total_minor,line_count,issue_count,calculated_at,rule_id,director_version_id)
 VALUES(2,1,'sample',0,0,0,now(),1,$1)`, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(2,1,'验收');DELETE FROM draft_refresh_queue`); err != nil {
		t.Fatal(err)
	}
	second, p := save(directorRequest{PeriodID: 2, BaseID: first, Edit: directorEdit{Kind: "member", Company: companyC, Director: companyB}})
	if len(p.Document.Rows) != 3 || !p.Document.Rows[2].DirectorOnly {
		t.Fatalf("old director lost own sales: %+v", p.Document.Rows)
	}
	applicable(3, second)
	var frozen int64
	if err := s.db.QueryRow(ctx, `SELECT director_version_id FROM calculation_drafts WHERE period_id=2 AND NOT EXISTS(SELECT 1 FROM draft_refresh_queue WHERE period_id=2)`).Scan(&frozen); err != nil || frozen != first {
		t.Fatalf("confirmed history changed: %d %v", frozen, err)
	}
	third, p := save(directorRequest{PeriodID: 3, BaseID: second, Edit: directorEdit{Kind: "member", Company: companyC}})
	for _, r := range p.Document.Rows {
		if r.MemberCompanyID == companyC {
			t.Fatal("company was not cleared")
		}
	}
	_, err = s.previewDirectorEdit(ctx, directorRequest{PeriodID: 3, BaseID: first, Edit: directorEdit{Kind: "member", Company: companyC}})
	if !errors.Is(err, conflict) {
		t.Fatalf("stale base accepted: %v", err)
	}
	_, err = s.previewDirectorEdit(ctx, directorRequest{PeriodID: 2, BaseID: third, Edit: directorEdit{Kind: "member", Company: companyC}})
	if !errors.Is(err, invalid) {
		t.Fatalf("backdated version accepted: %v", err)
	}
	_, err = s.previewDirectorEdit(ctx, directorRequest{PeriodID: 3, BaseID: third, Edit: directorEdit{Kind: "member", Company: companyInternal, Director: companyA}})
	if !errors.Is(err, invalid) {
		t.Fatalf("non-eligible category: %v", err)
	}
	_, err = s.previewDirectorEdit(ctx, directorRequest{PeriodID: 3, BaseID: third, Edit: directorEdit{Kind: "director", Company: companyInternal, Person: "3", Department: "样本组"}})
	if !errors.Is(err, invalid) {
		t.Fatalf("non-member incorrectly eligible: %v", err)
	}
	fourth, p := save(directorRequest{PeriodID: 3, BaseID: third, Edit: directorEdit{Kind: "director", Company: companyInternal, Person: "4", Department: "样本组"}})
	row := p.Document.Rows[len(p.Document.Rows)-1]
	if !row.DirectorOnly || row.RecipientID != "4" || row.LeaderSourceUserID != "8504698" {
		t.Fatal("internal exception lost")
	}
	applicable(2, second)
	applicable(3, fourth)
	if _, err := s.db.Exec(ctx, "UPDATE practice_director_versions SET file_name='x' WHERE id=$1", first); err == nil {
		t.Fatal("published version mutable")
	}
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		doc, err := readDirectorVersionDocument(ctx, tx, 1, first)
		if err == nil && (len(doc.Rows) != 2 || doc.Rows[0].SourceMember != "103") {
			t.Fatal("frozen version lost")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, "INSERT INTO source_mappings(product_id,source,domain,source_key) VALUES(2,'DCZD','PRACTICE','4')"); err != nil {
		t.Fatal(err)
	}
	_, err = s.previewDirectorEdit(ctx, directorRequest{PeriodID: 3, BaseID: fourth})
	if err == nil || !strings.Contains(err.Error(), "家庭教育") {
		t.Fatalf("canonical route precedence: %v", err)
	}
}

func TestDirectorRegistryImportsFrozenA2VersionWithoutNameRematching(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, directorFixtureSQL); err != nil {
		t.Fatal(err)
	}
	const oldPerson = "44000000-0000-4000-8000-000000000001"
	const company = "42000000-0000-4000-8000-000000000001"
	const member = "42000000-0000-4000-8000-000000000003"
	if _, err := s.db.Exec(ctx, `UPDATE people SET legacy_party_id=$1 WHERE id=1`, oldPerson); err != nil {
		t.Fatal(err)
	}
	const oldMember = "46000000-0000-4000-8000-000000000001"
	const oldPending = "46000000-0000-4000-8000-000000000002"
	const pending = "42000000-0000-4000-8000-000000000002"
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_dispatch_company_keys(unit_id,wire_id) VALUES ($1,$2),($3,$4)`, member, oldMember, pending, oldPending); err != nil {
		t.Fatal(err)
	}
	var period directorPeriod
	if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { var err error; period, err = directorPeriodFor(ctx, tx, 2); return err }); err != nil {
		t.Fatal(err)
	}
	original := directorDocument{Version: 8, PeriodKey: period.Key, StartsAt: period.Start, Rows: []director.Row{
		{Number: 1, Department: "一部", LeaderID: oldPerson, LeaderName: "历史显示名", LeaderCompanyID: company, LeaderSourceUserID: "201", LeaderSourceCompanyID: "101", RecipientID: company, DirectorOnly: true},
		{Number: 2, Department: "一部", LeaderID: oldPerson, LeaderCompanyID: company, LeaderSourceUserID: "201", LeaderSourceCompanyID: "101", RecipientID: company, MemberCompanyID: oldMember, MemberSourceCompanyID: "103"},
		{Number: 3, Department: "一部", LeaderID: oldPerson, LeaderCompanyID: company, LeaderSourceUserID: "201", LeaderSourceCompanyID: "101", RecipientID: company, PendingReview: true, PendingCompanyID: oldPending, MemberCompanyName: "待核原名"},
	}}
	req := directorRequest{PeriodID: 2, Edit: directorEdit{Kind: "legacy", FileName: "版本.json", File: directorMarshal(original)}}
	v, err := s.previewDirectorEdit(ctx, req)
	if err != nil || len(v.Issues) != 0 {
		t.Fatalf("valid historical file: %+v %v", v.Issues, err)
	}
	if v.Document.Rows[0].LeaderID != "1" || v.Document.Rows[0].LeaderName != "历史显示名" || v.Document.Rows[0].LeaderDisplayName != "样本甲" || !v.Document.Rows[0].DirectorOnly || !v.Document.Rows[2].PendingReview || v.Document.SourceVersion != 8 || v.Document.Rows[1].MemberCompanyID != member || v.Document.Rows[2].PendingCompanyID != pending {
		t.Fatal("archive did not preserve original and local identities")
	}
	req.RequestID = uuid.NewString()
	req.Expected = v.Hash
	id, err := s.saveDirectorEdit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.BaseID = id
	original.Rows[0].RecipientID = oldPerson
	req.Edit.File = directorMarshal(original)
	v, err = s.previewDirectorEdit(ctx, req)
	if err != nil || len(v.Issues) == 0 {
		t.Fatalf("reassigned recipient accepted: %+v %v", v.Issues, err)
	}
	req.PeriodID = 3
	if _, err = s.previewDirectorEdit(ctx, req); !errors.Is(err, invalid) {
		t.Fatalf("moved legacy opening accepted: %v", err)
	}
}

func TestDirectorPublicationRejectsOlderSerializableCalculationSnapshot(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, directorFixtureSQL); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var existing int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM practice_director_versions").Scan(&existing); err != nil {
		t.Fatal(err)
	}
	req := directorRequest{PeriodID: 2, Edit: directorEdit{Kind: "director", Company: "42000000-0000-4000-8000-000000000001", Person: "1", Department: "测试部"}}
	preview, err := s.previewDirectorEdit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Expected = preview.Hash
	req.RequestID = uuid.NewString()
	if _, err := s.saveDirectorEdit(ctx, req); err != nil {
		t.Fatal(err)
	}
	_, _, err = readApplicableDirectorVersion(ctx, tx, 1, 2)
	var dbErr *pgconn.PgError
	if !errors.As(err, &dbErr) || dbErr.Code != "40001" {
		t.Fatalf("superseded snapshot was allowed: %v", err)
	}
}

func TestDirectorRegistryRejectsReassignedCompanySource(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, directorFixtureSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(ctx, `UPDATE organization_company_sources SET valid_to='2021-01-01' WHERE source_company_id='101';
 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from)
 VALUES('DCZD','101','42000000-0000-4000-8000-000000000003','2021-01-01')`); err != nil {
		t.Fatal(err)
	}
	_, err := s.previewDirectorEdit(ctx, directorRequest{PeriodID: 2, Edit: directorEdit{Kind: "director", Company: "42000000-0000-4000-8000-000000000001", Person: "1", Department: "样本部"}})
	if !errors.Is(err, invalid) {
		t.Fatalf("old account belonging to another company was accepted: %v", err)
	}
}
