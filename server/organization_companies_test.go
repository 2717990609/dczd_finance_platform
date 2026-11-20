package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOrganizationCompanyDirectoryUsesCompanyTypeAndCategoryHistory(t *testing.T) {
	s := workspace{db: integrationDB(t)}
	const unit = "40000000-0000-4000-8000-000000000051"
	const sample = `{"capturedAt":"2026-11-20T00:00:00Z","people":[{"id":"40000000-0000-4000-8000-000000000054","name":"样本人员"}],
    "assignments":[{"id":"40000000-0000-4000-8000-000000000055","personId":"40000000-0000-4000-8000-000000000054",
      "unitId":"` + unit + `","kind":"MANAGER","position":"服务管理","appointmentType":"CONCURRENT","from":"2026-08-01T00:00:00Z"}],
	"units":[{"id":"` + unit + `","name":"外部公司","kind":"COMPANY","status":"ACTIVE","salesCompanyType":"EXTERNAL"},
	         {"id":"40000000-0000-4000-8000-000000000052","name":"内部公司","kind":"COMPANY","status":"ACTIVE","salesCompanyType":"INTERNAL"}],
	"salesCategories":[{"id":"40000000-0000-4000-8000-000000000053","name":"地区伙伴","active":true}],
	"salesCategoryHistory":[{"unitId":"` + unit + `","from":"2026-08-01T00:00:00Z","to":"2026-09-01T00:00:00Z"},
	                       {"unitId":"` + unit + `","categoryId":"40000000-0000-4000-8000-000000000053","from":"2026-09-01T00:00:00Z"}]}`
	if err := storeOrganizationSnapshot(context.Background(), s.db, json.RawMessage(sample)); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.organizationCompanies(w, httptest.NewRequest(http.MethodGet, "/api/v1/organization-sales-companies?type=EXTERNAL&categoryId=40000000-0000-4000-8000-000000000053", nil))
	var result struct {
		Total int
		Items []struct{ ID, CategoryName string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Total != 1 || len(result.Items) != 1 ||
		result.Items[0].ID != unit || result.Items[0].CategoryName != "地区伙伴" {
		t.Fatalf("unexpected company directory: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.organizationCompanyCategoryHistory(w, httptest.NewRequest(http.MethodGet, "/api/v1/organization-sales-company-categories?unitId="+unit, nil))
	var history []struct{ CategoryName string }
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || w.Code != 200 || len(history) != 2 || history[0].CategoryName != "地区伙伴" || history[1].CategoryName != "" {
		t.Fatalf("unexpected category history: %d %s", w.Code, w.Body.String())
	}
	// An overlapping update must roll back the complete snapshot.
	overlap := strings.Replace(sample, "2026-11-20T00:00:00Z", "2026-11-21T00:00:00Z", 1)
	overlap = strings.Replace(overlap, "2026-09-01T00:00:00Z", "2026-09-02T00:00:00Z", 1)
	if err := storeOrganizationSnapshot(context.Background(), s.db, json.RawMessage(overlap)); err == nil {
		t.Fatal("overlapping sales categories were accepted")
	}
	// A newer snapshot in the original format should preserve the additional fields.
	old := strings.Replace(sample, "2026-11-20T00:00:00Z", "2026-11-22T00:00:00Z", 1)
	old = strings.ReplaceAll(old, `,"salesCompanyType":"EXTERNAL"`, "")
	old = strings.ReplaceAll(old, `,"appointmentType":"CONCURRENT"`, "")
	old = old[:strings.Index(old, `"salesCategories"`)]
	old = strings.TrimSuffix(strings.TrimSpace(old), ",") + "}"
	if err := storeOrganizationSnapshot(context.Background(), s.db, json.RawMessage(old)); err != nil {
		t.Fatal(err)
	}
	var companyType, appointmentType string
	if err := s.db.QueryRow(context.Background(), `SELECT u.sales_company_type,a.appointment_type
	 FROM organization_units u JOIN organization_assignments a ON a.unit_id=u.id WHERE u.id=$1`, unit).
		Scan(&companyType, &appointmentType); err != nil || companyType != "EXTERNAL" || appointmentType != "CONCURRENT" {
		t.Fatalf("legacy snapshot removed company or appointment metadata: %s %s %v", companyType, appointmentType, err)
	}

}
