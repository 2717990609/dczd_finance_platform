package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func healthFixture(t *testing.T) (workspace, organizationHealthEvent) {
	t.Helper()
	ctx := context.Background()
	s := workspace{development: true, db: integrationDB(t)}
	_, err := s.db.Exec(ctx, `INSERT INTO organization_sales_categories(id,name,active) VALUES('10000000-1234-1234-1234-123456789001','健康大使',true);
  INSERT INTO organization_dispatch_links(target,entity_type,entity_id,external_id)
  SELECT 'DCAI','CATEGORY',id,'71' FROM organization_sales_categories`)
	if err != nil {
		t.Fatal(err)
	}
	return s, organizationHealthEvent{ID: 1, Kind: "COMPANY", Name: "合成公司", CategoryID: "71", ExternalID: "72", Status: 1, Base: -1, At: time.Now().UTC().Add(-time.Hour)}
}

func TestOrganizationHealthInboundAndRetry(t *testing.T) {
	s, company := healthFixture(t)
	ctx := context.Background()
	member := organizationHealthEvent{ID: 2, Kind: "MEMBER", ExternalID: "73", Name: "合成成员", UserID: "74", CompanyID: "72", Status: 1, Owner: 1, At: company.At, Base: -1}
	settled := map[int64]bool{}
	attempts := 0
	var memberWire string
	handler := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Org-Sync-Signature") == "" {
			w.WriteHeader(400)
			return
		}
		switch r.URL.Path {
		case orgHealthChangesPath:
			var req struct {
				After int64 `json:"after_id"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				w.WriteHeader(400)
				return
			}
			page := []organizationHealthEvent{}
			for _, ev := range []organizationHealthEvent{company, member} {
				if ev.ID > req.After && !settled[ev.ID] {
					page = append(page, ev)
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"list": page})
		case orgHealthAckPath:
			var ack organizationHealthAck
			if json.NewDecoder(r.Body).Decode(&ack) != nil || ack.WireID == "" {
				w.WriteHeader(400)
				return
			}
			if ack.ID == 2 {
				attempts++
				if attempts == 1 {
					memberWire = ack.WireID
					w.WriteHeader(503)
					return
				}
				if ack.WireID != memberWire {
					t.Errorf("retry changed member id")
				}
			}
			settled[ack.ID] = true
			json.NewEncoder(w).Encode(map[string]bool{"acknowledged": true})
		default:
			w.WriteHeader(404)
		}
	}))
	defer handler.Close()
	client := &organizationSyncClient{base: handler.URL, keyID: "fixture", secret: []byte("isolated-test-fixture"), http: handler.Client()}
	var after int64
	for range 2 {
		if err := s.pollOrganizationHealth(ctx, client, &after); err != nil {
			t.Fatal(err)
		}
	}
	if !settled[1] || !settled[2] || attempts != 2 {
		t.Fatalf("ack count %d settled %v", attempts, settled)
	}
	repeat, err := s.receiveOrganizationHealth(ctx, member)
	if err != nil || repeat.WireID != memberWire {
		t.Fatalf("member replay %v %v", repeat, err)
	}
	var assignments, outbound, acks int
	err = s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM organization_assignments),(SELECT count(*) FROM organization_dispatch_outbox),
  (SELECT count(*) FROM organization_health_inbox WHERE acknowledged_at IS NOT NULL)`).Scan(&assignments, &outbound, &acks)
	if err != nil || assignments != 1 || outbound != 0 || acks != 2 {
		t.Fatalf("inbound assignment/outbound/acks %d/%d/%d %v", assignments, outbound, acks, err)
	}
	other := member
	other.ID = 3
	other.UserID = "75"
	other.ExternalID = "76"
	blocked, err := s.receiveOrganizationHealth(ctx, other)
	if blocked.ID != 0 || err != nil {
		t.Fatalf("owner conflict %v %v", blocked, err)
	}
	var people int
	if err = s.db.QueryRow(ctx, "SELECT count(*) FROM people").Scan(&people); err != nil || people != 1 {
		t.Fatalf("partial conflict write %d %v", people, err)
	}
	stopped := member
	stopped.ID = 5
	stopped.Status = 0
	stopped.At = member.At.Add(time.Minute)
	if _, err = s.receiveOrganizationHealth(ctx, stopped); err != nil {
		t.Fatal(err)
	}
	late := member
	late.ID = 4
	ack, err := s.receiveOrganizationHealth(ctx, late)
	if err != nil || !ack.Only || ack.WireID != memberWire {
		t.Fatalf("older change %v %v", ack, err)
	}
	var end time.Time
	if err = s.db.QueryRow(ctx, "SELECT valid_to FROM organization_assignments").Scan(&end); err != nil || !end.Equal(stopped.At.Truncate(time.Microsecond)) {
		t.Fatalf("closed interval %v %v", end, err)
	}
}

func TestOrganizationHealthDivergenceAndResolution(t *testing.T) {
	for _, decision := range []string{"ACCEPT_AI", "KEEP_FINANCE"} {
		t.Run(decision, func(t *testing.T) {
			ctx := context.Background()
			s, e := healthFixture(t)
			initial, err := s.receiveOrganizationHealth(ctx, e)
			if err != nil || initial.WireID == "" {
				t.Fatalf("initial %v %v", initial, err)
			}
			var msg string
			var desired int64
			err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
				var err error
				msg, err = enqueueDispatch(ctx, tx, "DCAI", "COMPANY", initial.WireID, "sale_company_upsert", map[string]any{"name": "财务公司", "status": "ACTIVE", "category_id": initial.Category}, nil, nil)
				if err != nil {
					return err
				}
				return tx.QueryRow(ctx, `SELECT version FROM organization_dispatch_outbox WHERE id=$1 AND (payload->>'ai_base_revision')='1'`, msg).Scan(&desired)
			})
			if err != nil {
				t.Fatal(err)
			}
			e.ID = 2
			e.Name = "AI 已改名"
			e.Base = 0
			ack, err := s.receiveOrganizationHealth(ctx, e)
			if ack.ID != 0 || err != nil {
				t.Fatalf("conflict should await human decision %v %v", ack, err)
			}
			if err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
				_, err := organizationHealthDispatchBase(ctx, tx, "COMPANY", initial.WireID, "")
				return err
			}); !errors.Is(err, conflict) {
				t.Fatalf("ordinary edit should pause: %v", err)
			}
			choice := organizationHealthDecision{ID: e.ID, Version: desired, Decision: decision, Evidence: "已核对资料"}
			stale := choice
			stale.Version++
			if _, err = s.resolveOrganizationHealth(ctx, stale); !errors.Is(err, conflict) {
				t.Fatalf("stale review: %v", err)
			}
			if _, err = s.resolveOrganizationHealth(ctx, choice); err != nil {
				t.Fatal(err)
			}
			var state string
			check := func(want string) {
				t.Helper()
				if err = s.db.QueryRow(ctx, `SELECT state FROM organization_health_conflicts WHERE source_id=2`).Scan(&state); err != nil || state != want {
					t.Fatalf("conflict state = %q want %q, %v", state, want, err)
				}
			}
			if decision == "ACCEPT_AI" {
				check("RESOLVED")
				var name string
				if err = s.db.QueryRow(ctx, `SELECT name FROM organization_units WHERE id=$1`, initial.WireID).Scan(&name); err != nil || name != e.Name {
					t.Fatalf("accepted name %s %v", name, err)
				}
			} else {
				check("RESOLVING")
				var correction string
				if err = s.db.QueryRow(ctx, `SELECT id::text FROM organization_dispatch_outbox WHERE (payload->>'ai_resolution')::boolean
      AND payload->>'ai_base_revision'='2' AND payload->>'name'='财务公司' AND version>$1`, desired).Scan(&correction); err != nil {
					t.Fatal(err)
				}
				// Use the actual signed dispatch loop and local receipt transaction for both outcomes.
				reject := true
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var outgoing dispatchEnvelope
					if err := json.NewDecoder(r.Body).Decode(&outgoing); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if outgoing.EntityID != initial.WireID || outgoing.MessageType != "sale_company_upsert" || r.URL.Path != organizationMessagePath {
						t.Errorf("incorrect outgoing correction")
					}
					result := dispatchReceipt{MessageID: outgoing.ID, Result: "APPLIED", AppliedVersion: outgoing.Version, ExternalID: e.ExternalID}
					if reject {
						result.Result = "CONFLICT"
						result.ReasonCode = "AI_CAUSAL_CONFLICT"
					}
					json.NewEncoder(w).Encode(result)
				}))
				defer server.Close()
				client := &organizationSyncClient{base: server.URL, keyID: "fixture", secret: []byte("isolated-test-fixture"), http: server.Client()}
				if ok, err := s.deliverOrganization(ctx, "DCAI", client); !ok || err != nil {
					t.Fatal(ok, err)
				}
				check("OPEN")
				reject = false
				e.ID = 3
				if _, err = s.receiveOrganizationHealth(ctx, e); err != nil {
					t.Fatal(err)
				}
				var latest int64
				if err = s.db.QueryRow(ctx, `SELECT version FROM organization_health_conflicts WHERE source_id=3`).Scan(&latest); err != nil {
					t.Fatal(err)
				}
				if _, err = s.resolveOrganizationHealth(ctx, organizationHealthDecision{ID: 3, Version: latest, Decision: decision, Evidence: "核对最新 AI 变更"}); err != nil {
					t.Fatal(err)
				}
				if ok, err := s.deliverOrganization(ctx, "DCAI", client); !ok || err != nil {
					t.Fatal(ok, err)
				}
				check("RESOLVED")
				var count int
				if err = s.db.QueryRow(ctx, `SELECT count(*) FROM organization_health_inbox WHERE source_id=2 AND acknowledged_at IS NOT NULL`).Scan(&count); err != nil || count != 1 {
					t.Fatal(count, err)
				}
			}
		})
	}
}

func TestOrganizationHealthHistoricalReceipts(t *testing.T) {
	ctx := context.Background()
	s, e := healthFixture(t)
	a, err := s.receiveOrganizationHealth(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	var wire string
	if err = s.db.QueryRow(ctx, `UPDATE organization_dispatch_company_keys SET wire_id=gen_random_uuid() WHERE unit_id=$1 RETURNING wire_id::text`, a.WireID).Scan(&wire); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(fmt.Sprintf(`[{"id":41,"kind":"COMPANY","externalId":"72","wireId":%q,"financeBaseVersion":-1,"acknowledgeOnly":false,"acknowledgedAt":"2026-01-02T00:00:00Z"}]`, wire))
	for range 2 {
		if err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return importHealthReceipts(ctx, tx, raw) }); err != nil {
			t.Fatal(err)
		}
	}
	var rev int64
	if err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		rev, err = organizationHealthDispatchBase(ctx, tx, "COMPANY", a.WireID, "")
		return err
	}); err != nil || rev != 41 {
		t.Fatalf("snapshot revision %d %v", rev, err)
	}
	e.ID = 42
	ack, err := s.receiveOrganizationHealth(ctx, e)
	if err != nil || ack.WireID != wire {
		t.Fatalf("restored wire UUID %q want %q: %v", ack.WireID, wire, err)
	}
}
