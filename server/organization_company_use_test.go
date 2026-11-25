package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCompanyBusinessUseClosesOnlyMatchingPeriod(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	if _, err := s.db.Exec(ctx, directorFixtureSQL); err != nil {
		t.Fatal(err)
	}
	const company = "42000000-0000-4000-8000-000000000001"
	const sourceRows = `[{"source":"DCAI","sourceCompanyId":"601","companyId":"42000000-0000-4000-8000-000000000001","from":"2020-01-01T00:00:00Z"}]`
	const aliasRows = `[{"id":"42000000-1111-4000-8000-000000000004","externalId":"601","companyId":"42000000-2222-4000-8000-000000000004","unitId":"42000000-0000-4000-8000-000000000001","from":"2020-03-01T00:00:00Z"}]`
	captured := time.Now().UTC()
	if _, err := s.db.Exec(ctx, "INSERT INTO catalog_sales_context(source,captured_at,snapshot_digest) VALUES('DCAI',$1,''::bytea)", captured); err != nil {
		t.Fatal(err)
	}
	importBoth := func() {
		t.Helper()
		if _, err := s.db.Exec(ctx, importOrganizationCompanySourcesSQL, json.RawMessage(sourceRows)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(ctx, importVIPSalesCompanyLinks, json.RawMessage(aliasRows), "DCAI", captured); err != nil {
			t.Fatal(err)
		}
	}
	importBoth()
	in := closeCompanyUseInput{RequestID: uuid.NewString(), CompanyID: company, Source: "DCAI", ExternalID: "601", ExpectedFrom: time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC)}
	for range 2 {
		if _, err := s.closeCompanyUse(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	importBoth()
	var remaining, closed, historical int
	err := s.db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE active AND source='DCZD'),count(*) FILTER(WHERE NOT active AND source='DCAI'),
 count(*) FILTER(WHERE source='DCAI' AND tstzrange("validFrom","validTo",'[)') @> timestamptz '2021-01-01') FROM (`+companyBusinessUsesSQL+`) uses`, company).Scan(&remaining, &closed, &historical)
	if err != nil || remaining != 1 || closed != 2 || historical != 2 {
		t.Fatalf("other=%d closed=%d history=%d err=%v", remaining, closed, historical, err)
	}
	in.RequestID = uuid.NewString()
	if _, err = s.closeCompanyUse(ctx, in); err == nil {
		t.Fatal("accepted stale period")
	}
}
