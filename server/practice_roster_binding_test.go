package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPracticeRosterBindingUsesSourceKeyAndQueuesBothSeries(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	const fixture = `
	 INSERT INTO products(code,name,period_mode) VALUES('PRACTICE','实战班','class');
	 INSERT INTO periods(product_id,name,ordinal,starts_on,ends_on,source_period_key) VALUES
	  (1,'待更正期次',158,'2026-11-14','2026-11-15','PRACTICE:1:1:158'),
	  (1,'旧班型后续期次',159,'2026-11-21','2026-11-22','PRACTICE:1:1:159'),
	  (1,'新班型后续期次',162,'2026-11-25','2026-11-26','PRACTICE:1:2:162');
	 INSERT INTO practice_rosters(period_key,source_version,source_hash,starts_at,confirmed_at,attendance_count) VALUES
	  ('PRACTICE:1:1:158',1,'a','2026-11-14 09:00+08',now(),1),
	  ('PRACTICE:1:1:158',2,'b','2026-11-14 09:00+08',now(),2),
	  ('PRACTICE:1:1:159',1,'c','2026-11-21 09:00+08',now(),3),
	  ('PRACTICE:1:2:160',1,'d','2026-11-20 09:00+08',now(),4),
	  ('PRACTICE:1:2:161',1,'e','2026-11-24 16:00+00',now(),5);
	 INSERT INTO rule_versions(product_id,version,label,components) VALUES(1,1,'累计规则','[{"mode":"tiered"}]');`
	if _, err := s.db.Exec(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	type roster struct {
		Key      string
		Version  int
		StartsOn string
	}
	options := func(query string) []roster {
		t.Helper()
		w := httptest.NewRecorder()
		s.listPracticeRosterOptions(w, httptest.NewRequest(http.MethodGet, "/api/v1/practice-rosters/options?"+query, nil))
		var result []roster
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
			t.Fatalf("roster options HTTP %d: %s", w.Code, w.Body)
		}
		return result
	}
	if got := options(""); len(got) != 2 || got[0].Key != "PRACTICE:1:2:161" || got[0].StartsOn != "2026-11-25" {
		t.Fatalf("unassociated options or Shanghai date: %+v", got)
	}
	if got := options("periodId=1&q=2026-11-14"); len(got) != 1 || got[0].Key != "PRACTICE:1:1:158" || got[0].Version != 2 {
		t.Fatalf("edit options should pin current latest version: %+v", got)
	}
	input := periodPracticeRosterInput{PeriodID: 1, SourcePeriodKey: "PRACTICE:1:2:160", ExpectedSourcePeriodKey: "PRACTICE:1:1:158"}
	occupied := input
	occupied.SourcePeriodKey = "PRACTICE:1:1:159"
	var duplicate *pgconn.PgError
	if _, err := s.setPeriodPracticeRoster(ctx, occupied); !errors.As(err, &duplicate) || duplicate.Code != "23505" {
		t.Fatalf("duplicate association: %v", err)
	}
	changed, err := s.setPeriodPracticeRoster(ctx, input)
	if err != nil || !changed.(map[string]bool)["changed"] {
		t.Fatalf("correct association: %v %v", changed, err)
	}
	var sameDates bool
	if err := s.db.QueryRow(ctx, `SELECT starts_on='2026-11-14' AND ends_on='2026-11-15' FROM periods WHERE id=1`).Scan(&sameDates); err != nil || !sameDates {
		t.Fatalf("binding must preserve the accounting period dates: %t %v", sameDates, err)
	}
	var pending []int64
	if err := s.db.QueryRow(ctx, "SELECT array_agg(period_id ORDER BY period_id) FROM draft_refresh_queue").Scan(&pending); err != nil || !reflect.DeepEqual(pending, []int64{1, 2, 3}) {
		t.Fatalf("queue must include old and new series: %v %v", pending, err)
	}
	retry, err := s.setPeriodPracticeRoster(ctx, input)
	if err != nil || retry.(map[string]bool)["changed"] {
		t.Fatalf("retry must not enqueue again: %v %v", retry, err)
	}
	input.SourcePeriodKey = "PRACTICE:1:1:158"
	if _, err := s.setPeriodPracticeRoster(ctx, input); !errors.Is(err, conflict) {
		t.Fatalf("stale editor: %v", err)
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO calculation_drafts(period_id,rule_id,version,input_digest,total_minor,line_count,issue_count,calculated_at)
	 VALUES(1,1,1,'\x01',0,0,0,now()); INSERT INTO settlements(period_id,draft_version,confirmed_by) VALUES(1,1,'fixture');`); err != nil {
		t.Fatal(err)
	}
	input.ExpectedSourcePeriodKey = "PRACTICE:1:2:160"
	if _, err := s.setPeriodPracticeRoster(ctx, input); !errors.Is(err, errSettlementClosed) {
		t.Fatalf("closed period must stay unchanged: %v", err)
	}
}

func TestPracticeRosterOptionsAndBindingFollowProductStrategy(t *testing.T) {
	ctx := context.Background()
	s := workspace{db: integrationDB(t)}
	for _, sql := range []string{
		`INSERT INTO products(id,code,name,period_mode) OVERRIDING SYSTEM VALUE VALUES
		 (421,'HEALTH_PRACTICE','健康观念实战班','class'),(424,'FAMILY_PRACTICE','家庭教育实战班','class')`,
		`INSERT INTO source_mappings(product_id,source,domain,source_key) VALUES
		 (421,'DCZD','PRACTICE','1'),(424,'DCZD','PRACTICE','4')`,
		`INSERT INTO periods(id,product_id,name,ordinal,starts_on,ends_on) OVERRIDING SYSTEM VALUE VALUES
		 (4241,424,'家庭班待关联',20,'2026-06-08','2026-06-08')`,
		`INSERT INTO periods(id,product_id,name,ordinal,starts_on,ends_on,source_period_key) OVERRIDING SYSTEM VALUE VALUES
		 (4242,424,'既有误关联',21,'2026-07-08','2026-07-08','PRACTICE:2:1:101')`,
		`INSERT INTO practice_rosters(period_key,source_version,source_hash,starts_at,confirmed_at,attendance_count) VALUES
		 ('PRACTICE:2:1:96',1,'health','2026-06-08 09:38+08',now(),1),
		 ('PRACTICE:2:4:95',1,'family','2026-06-08 09:38+08',now(),1)`,
	} {
		if _, err := s.db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ query, key string }{
		{"productId=421", "PRACTICE:2:1:96"}, {"periodId=4241", "PRACTICE:2:4:95"},
	} {
		w := httptest.NewRecorder()
		s.listPracticeRosterOptions(w, httptest.NewRequest(http.MethodGet, "/api/v1/practice-rosters/options?"+item.query, nil))
		var items []struct{ Key string }
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || w.Code != http.StatusOK ||
			len(items) != 1 || items[0].Key != item.key {
			t.Fatalf("%s should only show %s: %d %s", item.query, item.key, w.Code, w.Body)
		}
	}
	input := periodPracticeRosterInput{PeriodID: 4241, SourcePeriodKey: "PRACTICE:2:1:96"}
	if _, err := s.setPeriodPracticeRoster(ctx, input); !errors.Is(err, errPracticeRosterProduct) {
		t.Fatalf("a healthy practice roster was accepted on a family period: %v", err)
	}
	if _, err := s.createPeriod(ctx, createPeriodInput{ProductID: 421, Name: "错误绑定", Ordinal: 150,
		StartsOn: "2026-06-08", EndsOn: "2026-06-08", SourcePeriodKey: "PRACTICE:2:4:95"}); !errors.Is(err, errPracticeRosterProduct) {
		t.Fatalf("a family roster was accepted on a healthy practice period: %v", err)
	}
	if _, err := s.pullPracticeRoster(ctx, calculateDraftInput{PeriodID: 4242}); !errors.Is(err, errPracticeRosterProduct) {
		t.Fatalf("an existing mismatch should be rejected before contacting the source: %v", err)
	}
	if _, err := s.calculateCurrentDraft(ctx, 4242, 0); !errors.Is(err, errPracticeRosterProduct) {
		t.Fatalf("an existing mismatch should be rejected before calculating: %v", err)
	}
	input.SourcePeriodKey = "PRACTICE:2:4:95"
	if result, err := s.setPeriodPracticeRoster(ctx, input); err != nil || !result.(map[string]bool)["changed"] {
		t.Fatalf("the mapped strategy should bind: %v %v", result, err)
	}
}
