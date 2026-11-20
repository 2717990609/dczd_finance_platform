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

func TestPracticeRosterBindingChecksDatesAndQueuesBothSeries(t *testing.T) {
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
	  ('PRACTICE:1:2:160',1,'d','2026-11-14 09:00+08',now(),4),
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
	if got := options("periodId=1&q=2026-11-14"); len(got) != 2 || got[0].Key != "PRACTICE:1:1:158" || got[0].Version != 2 {
		t.Fatalf("edit options should pin current latest version: %+v", got)
	}
	input := periodPracticeRosterInput{PeriodID: 1, SourcePeriodKey: "PRACTICE:1:2:161", StartsOn: "2026-11-14", EndsOn: "2026-11-15",
		ExpectedSourcePeriodKey: "PRACTICE:1:1:158", ExpectedStartsOn: "2026-11-14", ExpectedEndsOn: "2026-11-15"}
	if _, err := s.setPeriodPracticeRoster(ctx, input); !errors.Is(err, errPracticeStart) {
		t.Fatalf("wrong date: %v", err)
	}
	occupied := input
	occupied.SourcePeriodKey, occupied.StartsOn, occupied.EndsOn = "PRACTICE:1:1:159", "2026-11-21", "2026-11-22"
	var duplicate *pgconn.PgError
	if _, err := s.setPeriodPracticeRoster(ctx, occupied); !errors.As(err, &duplicate) || duplicate.Code != "23505" {
		t.Fatalf("duplicate association: %v", err)
	}
	input.SourcePeriodKey = "PRACTICE:1:2:160"
	changed, err := s.setPeriodPracticeRoster(ctx, input)
	if err != nil || !changed.(map[string]bool)["changed"] {
		t.Fatalf("correct association: %v %v", changed, err)
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
