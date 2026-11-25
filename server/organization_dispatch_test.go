package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOrganizationDispatchLifecycle(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	company, wire, other, otherWire := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	result, err := s.createOrganizationCategory(ctx, organizationCategoryInput{Name: "测试类别"})
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][]string{{company, wire}, {other, otherWire}} {
		_, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,name,kind,status,sales_company_type) VALUES($1,'测试公司','COMPANY','ACTIVE','INTERNAL')`, pair[0])
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.db.Exec(ctx, "INSERT INTO organization_dispatch_company_keys(unit_id,wire_id) VALUES($1,$2)", pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.assignOrganizationCategory(ctx, assignOrganizationCategoryInput{CategoryID: result.ID, Companies: []categoryCompanyVersion{{UnitID: pair[0]}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.saveDispatchTargets(ctx, dispatchTargetInput{CompanyID: pair[0], Targets: []string{"DCZD"}, ExpectedTargets: []string{}})
		if err != nil {
			t.Fatal(err)
		}
	}
	var person int64
	if err := s.db.QueryRow(ctx, "INSERT INTO people(name) VALUES('示例成员') RETURNING id").Scan(&person); err != nil {
		t.Fatal(err)
	}
	const key = "synthetic-local-fixture-key-length-32"
	var first string
	seenNonces := map[string]bool{}
	stub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(raw)
		m := hmac.New(sha256.New, []byte(key))
		nonce := r.Header.Get("X-Org-Sync-Nonce")
		_, _ = io.WriteString(m, strings.Join([]string{"POST", r.URL.Path, "local-fixture", r.Header.Get("X-Org-Sync-Timestamp"), nonce, hex.EncodeToString(sum[:])}, "\n"))
		if nonce == "" || seenNonces[nonce] || r.Header.Get("X-Org-Sync-Key-Id") != "local-fixture" || hex.EncodeToString(m.Sum(nil)) != r.Header.Get("X-Org-Sync-Signature") {
			t.Error("invalid signature or repeated nonce")
		}
		seenNonces[nonce] = true
		if r.URL.Path == organizationLookupPath {
			_ = json.NewEncoder(w).Encode(dispatchAccount{Status: "FOUND", State: "NONE", UserID: "u1"})
			return
		}
		var msg dispatchEnvelope
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Error(err)
			return
		}
		if msg.SourceSystem != "PLATFORM" || msg.TargetSystem != "DCZD" || msg.CorrelationID != msg.ID || msg.SchemaVersion != 1 {
			t.Error("invalid envelope")
		}
		receipt := dispatchReceipt{MessageID: msg.ID, Result: "APPLIED", AppliedVersion: msg.Version}
		switch msg.MessageType {
		case "sale_category_upsert":
			if first == "" {
				first = msg.ID
				w.WriteHeader(503)
				return
			}
			if first != msg.ID {
				t.Error("retry changed message id")
			}
			receipt.ExternalID = "c1"
		case "sale_company_upsert":
			switch msg.EntityID {
			case wire:
				receipt.ExternalID = "co1"
			case otherWire:
				receipt.ExternalID = "co2"
			default:
				t.Error("incorrect company wire id")
			}
		case "sale_company_membership_set":
			var d dispatchMemberMessage
			_ = json.Unmarshal(msg.Data, &d)
			receipt.ExternalID = "m1"
			role, company := "MEMBER", d.ExternalCompanyID
			if d.Operation == "TRANSFER" {
				company = d.DestinationExternalID
				if d.DestinationID != otherWire {
					t.Error("destination wire id")
				}
			}
			if d.Operation == "SET_OWNER" {
				role = "OWNER"
			}
			receipt.Identity = &dispatchIdentity{UserID: d.UserID, MemberID: "m1", CompanyID: company, Role: role, State: "ACTIVE"}
		default:
			t.Errorf("unexpected %s", msg.MessageType)
		}
		_ = json.NewEncoder(w).Encode(receipt)
	}))
	defer stub.Close()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = stub.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	t.Setenv("ORG_SYNC_DCZD_ENABLED", "true")
	t.Setenv("ORG_SYNC_DCZD_BASE_URL", stub.URL)
	t.Setenv("ORG_SYNC_DCZD_KEY_ID", "local-fixture")
	t.Setenv("ORG_SYNC_DCZD_SECRET", key)
	t.Setenv("ORG_SYNC_PHONE_HASH_SECRET", key)
	c, err := newOrganizationSyncClient("DCZD")
	if err != nil {
		t.Fatal(err)
	}
	deliver := func() {
		t.Helper()
		ok, err := s.deliverOrganization(ctx, "DCZD", c)
		if err != nil || !ok {
			t.Fatalf("deliver %v %v", ok, err)
		}
	}
	deliver()
	deliver()
	var waiting int
	if err := s.db.QueryRow(ctx, "SELECT count(*) FROM organization_dispatch_outbox WHERE state='WAIT_CATEGORY'").Scan(&waiting); err != nil || waiting != 1 {
		t.Fatalf("dependency wait %d %v", waiting, err)
	}
	if _, err := s.retryOrganizationDispatch(ctx, dispatchRetryInput{MessageID: first, ExpectedAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	deliver()
	_, err = s.db.Exec(ctx, "UPDATE organization_dispatch_outbox SET next_attempt_at=now() WHERE state='WAIT_CATEGORY'")
	if err != nil {
		t.Fatal(err)
	}
	deliver()
	deliver()
	account := dispatchBindInput{dispatchLookupInput: dispatchLookupInput{Target: "DCZD", Phone: "13800000000"}, PersonID: person, UserID: "u1", CompanyID: company, Owner: true}
	binding, err := s.bindDispatchAccount(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.bindDispatchAccount(ctx, account)
	if err != nil || again["bindingId"] != binding["bindingId"] {
		t.Fatal("binding retry", err)
	}
	deliver()
	deliver()
	operation := func(op, destination string, wantConflict bool) {
		t.Helper()
		var version int64
		if err := s.db.QueryRow(ctx, "SELECT version FROM organization_dispatch_links WHERE entity_id=$1", binding["bindingId"]).Scan(&version); err != nil {
			t.Fatal(err)
		}
		_, err := s.operateDispatchMember(ctx, dispatchMemberInput{PersonID: person, BindingID: binding["bindingId"], Operation: op, DestinationID: destination, ExpectedVersion: version})
		if wantConflict {
			if err == nil {
				t.Fatalf("expected blocked %s", op)
			}
			return
		}
		if err != nil {
			t.Fatal(op, err)
		}
		deliver()
	}
	later := time.Now().Add(time.Hour)
	cmd := dispatchMemberInput{PersonID: person, BindingID: binding["bindingId"], Operation: "UNSET_OWNER", EffectiveAt: &later, RequestID: uuid.NewString()}
	if err := s.db.QueryRow(ctx, "SELECT version FROM organization_dispatch_links WHERE entity_id=$1", cmd.BindingID).Scan(&cmd.ExpectedVersion); err != nil {
		t.Fatal(err)
	}
	scheduled, err := s.operateDispatchMember(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	againCommand, err := s.operateDispatchMember(ctx, cmd)
	if err != nil || againCommand["messageId"] != scheduled["messageId"] {
		t.Fatal("repeat command", err)
	}
	if processed, err := s.deliverOrganization(ctx, "DCZD", c); err != nil || processed {
		t.Fatal("premature delivery", processed, err)
	}
	if _, err := s.cancelDispatchMember(ctx, dispatchRetryInput{MessageID: scheduled["messageId"]}); err != nil {
		t.Fatal(err)
	}
	operation("DISABLE", "", true)
	operation("UNSET_OWNER", "", false)
	operation("TRANSFER", other, false)
	operation("DISABLE", "", false)
	operation("ENABLE", "", false)
	if _, err := s.renameDispatchPerson(ctx, dispatchRenameInput{PersonID: person, Name: "新姓名", ExpectedName: "示例成员"}); err != nil {
		t.Fatal(err)
	}
	deliver()
	var sourceName string
	if err := s.db.QueryRow(ctx, "SELECT source_name FROM organization_dispatch_links WHERE entity_id=$1", binding["bindingId"]).Scan(&sourceName); err != nil || sourceName != "新姓名" {
		t.Fatal(sourceName, err)
	}
	operation("RENAME", "", false)
	var identity, role, destination string
	if err := s.db.QueryRow(ctx, "SELECT identity_state,role,external_company_id FROM organization_dispatch_links WHERE entity_id=$1", binding["bindingId"]).Scan(&identity, &role, &destination); err != nil || identity != "ACTIVE" || role != "MEMBER" || destination != "co2" {
		t.Fatalf("identity %s %s %s %v", identity, role, destination, err)
	}
	request := httptest.NewRequest("GET", "/api/v1/organization-dispatch?personId=1", nil)
	w := httptest.NewRecorder()
	s.getOrganizationDispatch(w, request)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "co2") {
		t.Fatalf("report %d %s", w.Code, w.Body.String())
	}
}
func TestOrganizationDispatchSnapshotAndRollback(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t), development: true}
	company, wire := uuid.NewString(), uuid.NewString()
	_, err := s.db.Exec(ctx, `INSERT INTO organization_units(id,name,kind,status,sales_company_type) VALUES($1,'公司','COMPANY','ACTIVE','INTERNAL')`, company)
	if err != nil {
		t.Fatal(err)
	}
	floor := int64(150)
	snapshot := organizationSnapshot{DispatchVersionFloor: &floor, DispatchCompanies: []dispatchSnapshotCompany{{UnitID: company, EntityID: wire, Targets: []string{"DCAI"}}}, DispatchReceipts: []dispatchSnapshotReceipt{{Target: "DCAI", Kind: "COMPANY", EntityID: wire, ExternalID: "16", Version: 112}}}
	legacy := uuid.NewString()
	if _, err := s.db.Exec(ctx, "INSERT INTO people(name,legacy_party_id) VALUES('历史人员',$1)", legacy); err != nil {
		t.Fatal(err)
	}
	snapshot.DispatchPhoneBindings = []dispatchSnapshotPhone{{PhoneKey: strings.Repeat("ab", 32), PersonID: legacy}}
	for range 2 {
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return importDispatchSnapshot(ctx, tx, snapshot) }); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := s.createOrganizationCategory(ctx, organizationCategoryInput{Name: "零售"})
	if err != nil {
		t.Fatal(err)
	}
	var low int64
	if err := s.db.QueryRow(ctx, "SELECT min(version) FROM organization_dispatch_outbox").Scan(&low); err != nil || low <= floor {
		t.Fatal("version floor", low, err)
	}
	_, err = s.updateOrganizationCategory(ctx, updateOrganizationCategoryInput{ID: cat.ID, Name: "健康大使", ExpectedVersion: cat.Version, Active: true})
	if !errors.Is(err, errOrganizationDispatch) {
		t.Fatal(err)
	}
	var name string
	if err := s.db.QueryRow(ctx, "SELECT name FROM organization_sales_categories WHERE id=$1", cat.ID).Scan(&name); err != nil || name != cat.Name {
		t.Fatal("expected rollback", name, err)
	}
}

func TestOrganizationDispatchProfileUnitAndLimits(t *testing.T) {
	ctx := context.Background()
	s := workspace{development: true, db: integrationDB(t)}
	if _, err := s.createOrganizationCategory(ctx, organizationCategoryInput{Name: strings.Repeat("类", 11)}); !errors.Is(err, errDispatchCategoryName) {
		t.Fatal(err)
	}
	category, err := s.createOrganizationCategory(ctx, organizationCategoryInput{Name: strings.Repeat("类", 10)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.createPerson(ctx, createPersonInput{Name: strings.Repeat("人", 26)}); !errors.Is(err, errDispatchPersonName) {
		t.Fatal(err)
	}
	parent, err := s.saveDispatchProfile(ctx, dispatchProfileInput{Name: "上级", CompanyType: "INTERNAL", CategoryID: category.ID, Status: "ACTIVE", Targets: []string{"DCZD", "DCJK"}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.saveDispatchProfile(ctx, dispatchProfileInput{Name: "部门", CompanyType: "INTERNAL", CategoryID: category.ID, Status: "ACTIVE", ParentID: parent["id"], Targets: []string{"DCZD", "DCJK", "DCAI"}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(sql string, args ...any) int {
		t.Helper()
		var count int
		if err := s.db.QueryRow(ctx, sql, args...).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if n := run(`SELECT count(*) FROM organization_dispatch_outbox q JOIN organization_dispatch_company_keys p ON q.payload->>'parent_company_id'=p.wire_id::text WHERE p.unit_id=$1 AND q.entity_id=$2`, parent["id"], child["id"]); n != 3 {
		t.Fatalf("parent reference %d", n)
	}
	unit, err := s.createDispatchUnit(ctx, dispatchCreateUnitInput{Name: "部门", Kind: "DEPARTMENT", CompanyID: child["id"]})
	if err != nil {
		t.Fatal(err)
	}
	if n := run("SELECT count(*) FROM organization_dispatch_units WHERE unit_id=$1 AND company_id=$2", unit["id"], child["id"]); n != 1 {
		t.Fatal(n)
	}
	if _, err := s.saveDispatchProfile(ctx, dispatchProfileInput{ID: parent["id"], Name: "上级", Status: "INACTIVE", ExpectedVersion: 1}); !errors.Is(err, errOrganizationDispatch) {
		t.Fatal(err)
	}
	if _, err := s.saveDispatchProfile(ctx, dispatchProfileInput{ID: child["id"], Name: "部门调整", Status: "INACTIVE", ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if n := run("SELECT count(*) FROM organization_dispatch_outbox WHERE entity_id=$1 AND state='STOPPED' AND reason='SUPERSEDED'", child["id"]); n != 3 {
		t.Fatal(n)
	}
	if _, err := s.saveDispatchTargets(ctx, dispatchTargetInput{CompanyID: child["id"], Targets: []string{"DCAI"}, ExpectedTargets: []string{"DCZD", "DCAI", "DCJK"}}); err != nil {
		t.Fatal(err)
	}
	if n := run("SELECT count(*) FROM organization_dispatch_outbox WHERE entity_id=$1 AND state='PENDING'", child["id"]); n != 1 {
		t.Fatal(n)
	}
	readonly := workspace{db: s.db}
	if _, err := readonly.saveDispatchProfile(ctx, dispatchProfileInput{Name: "非法", CompanyType: "INTERNAL", CategoryID: category.ID, Status: "ACTIVE"}); !errors.Is(err, errFinanceLogin) {
		t.Fatal(err)
	}
}
func TestOrganizationDispatchHTTPException(t *testing.T) {
	t.Setenv("ORG_SYNC_DCZD_ENABLED", "true")
	t.Setenv("ORG_SYNC_DCZD_KEY_ID", "local-test")
	t.Setenv("ORG_SYNC_DCZD_SECRET", strings.Repeat("s", 32))
	t.Setenv("ORG_SYNC_DCZD_BASE_URL", "http://dachun-test-admin-shop-japi.xadazhihui.cn/api")
	for _, item := range []struct {
		environment string
		valid       bool
	}{{"test", true}, {"production", false}, {"", false}} {
		t.Setenv("ORG_SYNC_ENVIRONMENT", item.environment)
		_, err := newOrganizationSyncClient("DCZD")
		if (err == nil) != item.valid {
			t.Errorf("environment %q: %v", item.environment, err)
		}
	}
	t.Setenv("ORG_SYNC_ENVIRONMENT", "test")
	t.Setenv("ORG_SYNC_DCZD_BASE_URL", "http://127.0.0.1/api")
	if _, err := newOrganizationSyncClient("DCZD"); err == nil {
		t.Fatal("unexpected HTTP endpoint")
	}
}
