package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestPracticeRegistrationScopeCanUseCompanyMappedAtItsEffectiveStart(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	id := func(result any, err error) int64 {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]int64)["id"]
	}
	product := id(s.createProduct(ctx, createProductInput{Code: "REGISTRATION_IMPORT", Name: "班次范围导入验收", PeriodMode: "class"}))
	id(s.createMapping(ctx, createMappingInput{ProductID: product, Source: "DCZD", Domain: "PRACTICE", SourceKey: "PRACTICE:2:4"}))
	person := id(s.createPerson(ctx, createPersonInput{Name: "收款人"}))
	if _, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,kind,name,status)
	 VALUES('45000000-0000-4000-8000-000000000001','COMPANY','历史下属公司','ACTIVE');
	 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from,valid_to)
	 VALUES('DCZD','98812','45000000-0000-4000-8000-000000000001','2020-06-08 08:00+08','2021-01-01');`); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2020, 6, 8, 9, 38, 0, 0, shanghai)
	ordinal := 20
	input := publishRuleInput{ProductID: product, FromOrdinal: &ordinal, EffectiveFrom: &at, Label: "从第20届生效",
		Components: []component{{Type: "manager", Mode: "period_rate", Value: 30000, PeriodBenefit: &practicePeriodBenefit{
			Label: "收款人", PersonID: person, RegistrationScope: &practiceRegistrationScope{Companies: []string{"98812"}},
		}}}}
	if _, err := s.publishRule(ctx, input); err != nil {
		t.Fatalf("company mapped at the rule's effective start: %v", err)
	}
	var directory http.HandlerFunc
	for _, route := range s.routes() {
		if route.Method == "GET" && route.Path == "/api/v1/practice-registration-companies" {
			directory = route.Handler
		}
	}
	if directory == nil {
		t.Fatal("the rule editor needs a scoped company directory")
	}
	hasHistoricalCompany := func(query string) bool {
		t.Helper()
		recorder := httptest.NewRecorder()
		directory(recorder, httptest.NewRequest("GET", "/api/v1/practice-registration-companies?productId="+
			strconv.FormatInt(product, 10)+"&"+query, nil))
		var options []struct{ Value, Label string }
		if err := json.Unmarshal(recorder.Body.Bytes(), &options); err != nil || recorder.Code != http.StatusOK {
			t.Fatalf("could not read scoped companies: HTTP %d %v", recorder.Code, err)
		}
		for _, option := range options {
			if option.Value == "98812" {
				return option.Label == "历史下属公司"
			}
		}
		return false
	}
	if hasHistoricalCompany("effectiveFrom=2020-06-07T23:59:59Z") ||
		!hasHistoricalCompany("effectiveFrom=2020-06-08T01:38:00Z") || hasHistoricalCompany("effectiveFrom=2022-06-08T01:38:00Z") {
		t.Fatal("historical company names must be selectable for the time when their mapping was effective")
	}
	const periodKey = "PRACTICE:2:4:95"
	period := id(s.createPeriod(ctx, createPeriodInput{ProductID: product, Name: "旧期次", Ordinal: ordinal,
		StartsOn: "2020-06-08", EndsOn: "2020-06-08", SourcePeriodKey: periodKey}))
	if _, err := s.db.Exec(ctx, `INSERT INTO practice_rosters(period_key,source_version,source_hash,starts_at,confirmed_at,attendance_count)
	 VALUES($1,1,'old-session',$2,$2,0)`, periodKey, at); err != nil {
		t.Fatal(err)
	}
	if !hasHistoricalCompany("periodId=" + strconv.FormatInt(period, 10)) {
		t.Fatal("a selected old class must offer the company used at that class")
	}
	at = at.AddDate(2, 0, 0)
	if _, err := s.publishRule(ctx, input); !errors.Is(err, errPracticeTierCompany) {
		t.Fatalf("mapping expired by this rule's effective start: %v", err)
	}
}

func TestPracticeRegistrationImportPreviewsApprovedGroupsAndPendingRows(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	const corporate = "45000000-0000-4000-8000-000000000002"
	if _, err := s.db.Exec(ctx, `INSERT INTO products(id,code,name,period_mode) OVERRIDING SYSTEM VALUE
	 VALUES(9001,'IMPORT_FAMILY','家庭教育实战班','class'),(9002,'IMPORT_HEALTH','健康观念实战班','class');
	 INSERT INTO source_mappings(product_id,source,domain,source_key)
	 VALUES(9001,'DCZD','PRACTICE','4'),(9002,'DCZD','PRACTICE','1');
	 INSERT INTO organization_units(id,kind,name,status) VALUES
	 ('45000000-0000-4000-8000-000000000001','COMPANY','本人公司','ACTIVE'),
	 ('45000000-0000-4000-8000-000000000002','COMPANY','公司收款方','ACTIVE'),
	 ('45000000-0000-4000-8000-000000000003','COMPANY','下属公司','ACTIVE'),
	 ('45000000-0000-4000-8000-000000000004','COMPANY','后来关联的下属公司','ACTIVE');
	 INSERT INTO organization_company_sources(source,source_company_id,company_id,valid_from,valid_to) VALUES
	 ('DCZD','98911','45000000-0000-4000-8000-000000000001','2026-01-01',NULL),
	 ('DCZD','98912','45000000-0000-4000-8000-000000000002','2026-01-01',NULL),
	 ('DCZD','98913','45000000-0000-4000-8000-000000000003','2026-01-01','2026-10-01'),
	 ('DCZD','98914','45000000-0000-4000-8000-000000000004','2026-07-01',NULL);
	 INSERT INTO people(id,name) OVERRIDING SYSTEM VALUE VALUES(9876,'个人收款方');
	 INSERT INTO identity_mappings(person_id,source,external_id,valid_from) VALUES(9876,'DCZD','100001','2026-01-01');`); err != nil {
		t.Fatal(err)
	}
	doc := practiceRegistrationDocument{Version: 1, PeriodKey: "PRACTICE:2:4:95", PeriodName: "第20届",
		StartsAt: time.Date(2026, 6, 8, 9, 38, 0, 0, shanghai),
		Rows: []practiceRegistrationImportRow{
			{Number: 1, LeaderID: "p1", LeaderName: "甲", LeaderCompany: "c1", Recipient: "p1", SourceSeller: "100001", SourceCompany: "98911", SourceMember: "98913"},
			{Number: 2, LeaderID: "p2", LeaderName: "乙", LeaderCompany: "c2", Recipient: "c2", SourceSeller: "100002", SourceCompany: "98912", Pending: true, MemberName: "待核公司"},
			{Number: 3, LeaderID: "p1", LeaderName: "甲", LeaderCompany: "c1", Recipient: "p1", SourceSeller: "100001", SourceCompany: "98911", SourceMember: "98914"},
		}}
	input := practiceRegistrationImportInput{ProductID: 9001, Rate: 30000}
	preview := func() (practiceRegistrationImportPreview, error) {
		t.Helper()
		var err error
		if input.Document, err = json.Marshal(doc); err != nil {
			t.Fatal(err)
		}
		return s.previewPracticeRegistrationImport(ctx, input)
	}
	result, err := preview()
	if err != nil || len(result.Issues) != 0 || len(result.Components) != 2 || len(result.Pending) != 1 {
		t.Fatalf("expected two arrangements and an excluded pending row: %+v %v", result, err)
	}
	personal, company := result.Components[0].PeriodBenefit, result.Components[1].PeriodBenefit
	if personal.PersonID != 9876 || !personal.RegistrationScope.Exclusive || !company.RegistrationScope.Exclusive ||
		len(personal.RegistrationScope.Companies) != 2 ||
		personal.RegistrationScope.Companies[0] != "98913" || personal.RegistrationScope.Companies[1] != "98914" ||
		company.UnitID != corporate || len(company.RegistrationScope.Companies) != 0 || result.Pending[0].Message != "待核公司" {
		t.Fatalf("unexpected payer or scope: %+v %+v %+v", personal, company, result.Pending)
	}
	previous := doc.Rows[1]
	doc.Rows[1].Pending, doc.Rows[1].SourceMember = false, "98913"
	if duplicate, err := preview(); err != nil || len(duplicate.Issues) != 1 || len(duplicate.Components) != 0 ||
		duplicate.Issues[0].Message != errPracticeRegistrationOverlap.Error() {
		t.Fatalf("two directors may not claim the same company in one import: %+v %v", duplicate, err)
	}
	doc.Rows[1] = previous
	var versions int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM rule_versions`).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("preview cannot publish any rule version: %d %v", versions, err)
	}
	if _, err := s.db.Exec(ctx, `
	 INSERT INTO periods(id,product_id,name,ordinal,starts_on,ends_on,source_period_key) OVERRIDING SYSTEM VALUE VALUES
	 (9010,9001,'起始班次',20,'2026-06-08','2026-06-08','PRACTICE:2:4:95'),
	 (9011,9001,'上午更早的班次',19,'2026-06-08','2026-06-08','PRACTICE:2:4:94'),
	 (9012,9001,'下午班次',21,'2026-06-08','2026-06-08','PRACTICE:2:4:96');
	 INSERT INTO practice_rosters(period_key,source_version,source_hash,starts_at,confirmed_at,attendance_count) VALUES
	 ('PRACTICE:2:4:94',1,'morning','2026-06-08 08:00+08','2026-06-09 00:00+08',0),
	 ('PRACTICE:2:4:96',1,'previous','2026-06-08 08:30+08','2026-06-09 00:00+08',0),
	 ('PRACTICE:2:4:96',2,'afternoon','2026-06-08 14:00+08','2026-06-10 00:00+08',0)`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{9010, 9012} {
		input.PeriodID = &id
		if result, err := preview(); err != nil || len(result.Components) != 2 {
			t.Fatalf("the start period and a later same-day class can use the relationship version: period %d, %+v %v", id, result, err)
		}
	}
	earlier := int64(9011)
	input.PeriodID = &earlier
	if _, err := preview(); !errors.Is(err, errPracticeRosterProduct) {
		t.Fatalf("an earlier class on the same day must be rejected: %v", err)
	}
	input.PeriodID = nil
	input.ProductID = 9002
	if _, err := preview(); !errors.Is(err, errPracticeRosterProduct) {
		t.Fatalf("different product scope should be rejected: %v", err)
	}
	input.ProductID = 9001
	doc.Rows[0].SourceMember = "99199"
	if result, err := preview(); err != nil || len(result.Issues) != 1 || result.Issues[0].Number != 1 || len(result.Components) != 0 {
		t.Fatalf("unmapped approved company blocks adding the batch: %+v %v", result, err)
	}
}
