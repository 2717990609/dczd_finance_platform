package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var errCatalogPerformanceScope = errors.New("请选择有效的部门、小组或人员任职")

type catalogPerformanceScope struct {
	UnitIDs     []string                `json:"unitIds,omitempty"`
	Appointment *catalogPerformanceDuty `json:"appointment,omitempty"`
}

type catalogPerformanceDuty struct {
	PersonID int64  `json:"personId"`
	UnitID   string `json:"unitId"`
	Role     string `json:"role"`
}

func (s *catalogPerformanceScope) valid() bool {
	if s == nil {
		return true
	}
	validID := func(id string) bool {
		key, ok := canonicalSourceUUID(id)
		return ok && key == id
	}
	for i, unit := range s.UnitIDs {
		if !validID(unit) || slices.Contains(s.UnitIDs[:i], unit) {
			return false
		}
	}
	return s.Appointment == nil || s.Appointment.PersonID > 0 && validID(s.Appointment.UnitID) && textOK(s.Appointment.Role, 128)
}

func (s *catalogPerformanceScope) active() bool {
	return s != nil && (len(s.UnitIDs) != 0 || s.Appointment != nil)
}

func editCatalogPerformanceScope(ctx context.Context, db catalogPeriodReader, body json.RawMessage,
	before, scope *catalogPerformanceScope) (json.RawMessage, error) {
	if scope == nil {
		return body, nil
	}
	if !scope.valid() {
		return nil, errCatalogPerformanceScope
	}
	var additions []string
	for _, unit := range scope.UnitIDs {
		if before == nil || !slices.Contains(before.UnitIDs, unit) {
			additions = append(additions, unit)
		}
	}
	if len(additions) > 0 {
		var count int
		err := db.QueryRow(ctx, `SELECT count(*) FROM organization_units
		 WHERE id=ANY($1::uuid[]) AND kind IN ('DEPARTMENT','TEAM') AND status='ACTIVE'`, additions).Scan(&count)
		if err != nil {
			return nil, err
		}
		if count != len(additions) {
			return nil, errCatalogPerformanceScope
		}
	}
	if duty := scope.Appointment; duty != nil && (before == nil || before.Appointment == nil || *before.Appointment != *duty) {
		var exists bool
		err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_current_sales_appointments a
		 JOIN organization_units u ON u.id::text=a.scope_key AND u.status='ACTIVE'
		 WHERE a.source='DCAI' AND a.scope='ORGANIZATION_UNIT' AND a.scope_key=$1 AND a.role=$2 AND a.person_id=$3)`,
			duty.UnitID, duty.Role, duty.PersonID).Scan(&exists)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errCatalogPerformanceScope
		}
	}
	if !scope.active() && !before.active() {
		return body, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	if scope.active() {
		var err error
		root["performance_scope"], err = json.Marshal(scope)
		if err != nil {
			return nil, err
		}
	} else {
		delete(root, "performance_scope")
	}
	return json.Marshal(root)
}

type catalogPerformanceLabels struct {
	Units  map[string]string `json:"units"`
	Person string            `json:"person,omitempty"`
}

func nameCatalogPerformanceScope(ctx context.Context, db catalogPeriodReader, scope *catalogPerformanceScope) (*catalogPerformanceLabels, error) {
	if !scope.active() {
		return nil, nil
	}
	units := scope.UnitIDs
	var person int64
	if a := scope.Appointment; a != nil {
		units = append(slices.Clone(units), a.UnitID)
		person = a.PersonID
	}
	var labels catalogPerformanceLabels
	err := db.QueryRow(ctx, `SELECT jsonb_build_object(
	 'units',coalesce((SELECT jsonb_object_agg(u.id,concat_ws(' / ',p.name,u.name)) FROM organization_units u
	  LEFT JOIN organization_units p ON p.id=u.parent_id WHERE u.id=ANY($1::uuid[])),'{}'),
	 'person',coalesce((SELECT name FROM people WHERE id=$2),''))
	`, units, person).Scan(&labels)
	return &labels, err
}

type catalogPerformanceDecision struct {
	excluded bool
	issue    string
}

type catalogPerformanceDirectory struct {
	claims       map[int64]vipClaim
	claimIssues  map[int64]string
	users        [2]map[string][]vipLink
	companies    [2]map[string][]vipLink
	organization reportAttribution
	duties       map[catalogPerformanceDuty][]vipInterval
}

const catalogPerformanceLinksSQL = `WITH snapshot AS (SELECT captured_at FROM catalog_sales_context WHERE source='DCAI')
 SELECT 'PERSON',0,u.external_id,u.person_id::text,u.valid_from,u.valid_to
 FROM catalog_sales_people_sources u JOIN snapshot s ON s.captured_at=u.observed_at
 WHERE u.source='DCAI' AND u.external_id=ANY($1)
 UNION ALL
 SELECT 'PERSON',1,u.external_id,u.person_id::text,u.valid_from,u.valid_to
 FROM identity_mappings u WHERE u.source='DCAI' AND u.external_id=ANY($1)
 UNION ALL
 SELECT 'COMPANY',0,c.external_id,c.company_unit_id::text,c.valid_from,c.valid_to
 FROM catalog_sales_company_aliases c JOIN snapshot s ON s.captured_at=c.observed_at
 WHERE c.source='DCAI' AND c.external_id=ANY($2)
 UNION ALL
 SELECT 'COMPANY',1,c.source_company_id,c.company_id::text,c.valid_from,c.valid_to
 FROM organization_company_sources c WHERE c.source='DCAI' AND c.source_company_id=ANY($2)`

func readCatalogPerformanceDirectory(ctx context.Context, tx pgx.Tx, events []catalogActivityEvent) (catalogPerformanceDirectory, error) {
	dir := catalogPerformanceDirectory{claims: map[int64]vipClaim{}, claimIssues: map[int64]string{},
		duties: map[catalogPerformanceDuty][]vipInterval{}}
	var payments []int64
	seen := make(map[int64]bool)
	wanted := make(map[catalogPerformanceDuty]bool)
	for _, event := range events {
		if event.frozenPeriod != 0 || event.originClosed || seen[event.PaymentID] || event.selection == nil || event.selection.rule == nil {
			continue
		}
		seen[event.PaymentID] = true
		definition := event.selection.rule.Definition
		scope := definition.Performance
		if scope.active() && len(scope.UnitIDs) != 0 || slices.ContainsFunc(definition.Items, func(item catalogPeriodItem) bool {
			return item.OwnSales || len(item.CommissionUnits) != 0
		}) {
			payments = append(payments, event.PaymentID)
		}
		if scope.active() {
			if duty := scope.Appointment; duty != nil {
				wanted[*duty] = true
			}
		}
		for _, item := range definition.Items {
			if item.Appointment != nil {
				wanted[*item.Appointment] = true
			}
		}
	}
	if len(wanted) > 0 {
		keys := make([]catalogPerformanceDuty, 0, len(wanted))
		for duty := range wanted {
			keys = append(keys, duty)
		}
		rows, err := tx.Query(ctx, `SELECT a.person_id,a.scope_key,a.role,a.valid_from,a.valid_to
		 FROM jsonb_to_recordset($1::jsonb) AS q("personId" bigint,"unitId" text,role text)
		 JOIN catalog_current_sales_appointments a
		  ON (a.person_id,a.scope_key,a.role)=(q."personId",q."unitId",q.role)
		 WHERE a.source='DCAI' AND a.scope='ORGANIZATION_UNIT'`, keys)
		if err != nil {
			return dir, err
		}
		var duty catalogPerformanceDuty
		var interval vipInterval
		if _, err := pgx.ForEachRow(rows, []any{&duty.PersonID, &duty.UnitID, &duty.Role, &interval.From, &interval.To}, func() error {
			dir.duties[duty] = append(dir.duties[duty], interval)
			return nil
		}); err != nil {
			return dir, err
		}
	}
	if len(payments) == 0 {
		return dir, nil
	}
	rows, err := tx.Query(ctx, `SELECT source_version_id,sales_claims FROM source_order_classifications
	 WHERE source_version_id=ANY($1::bigint[]) AND jsonb_typeof(sales_claims)='array'`, payments)
	if err != nil {
		return dir, err
	}
	users, companies := make(map[string]bool), make(map[string]bool)
	var payment int64
	var claims []vipClaim
	_, err = pgx.ForEachRow(rows, []any{&payment, &claims}, func() error {
		claim, issue := vipSellerClaim(claims)
		if issue != "" {
			dir.claimIssues[payment] = issue
		} else {
			dir.claims[payment] = claim
			users[claim.User], companies[claim.Company] = true, true
		}
		return nil
	})
	if err != nil {
		return dir, err
	}
	links := func(keys map[string]bool) []string {
		result := make([]string, 0, len(keys))
		for key := range keys {
			result = append(result, key)
		}
		return result
	}
	rows, err = tx.Query(ctx, catalogPerformanceLinksSQL, links(users), links(companies))
	if err != nil {
		return dir, err
	}
	for i := range dir.users {
		dir.users[i], dir.companies[i] = make(map[string][]vipLink), make(map[string][]vipLink)
	}
	people := make(map[int64]bool)
	var kind string
	var level int
	var link vipLink
	_, err = pgx.ForEachRow(rows, []any{&kind, &level, &link.Key, &link.Value, &link.From, &link.To}, func() error {
		if kind == "PERSON" {
			dir.users[level][link.Key] = append(dir.users[level][link.Key], link)
			person, err := strconv.ParseInt(link.Value, 10, 64)
			if err != nil {
				return err
			}
			people[person] = true
		} else {
			dir.companies[level][link.Key] = append(dir.companies[level][link.Key], link)
		}
		return nil
	})
	if err != nil {
		return dir, err
	}
	ids := make([]int64, 0, len(people))
	for id := range people {
		ids = append(ids, id)
	}
	dir.organization, err = loadReportAttribution(ctx, tx, ids, nil)
	return dir, err
}

func (d catalogPerformanceDirectory) match(event catalogActivityEvent) catalogPerformanceDecision {
	if event.frozenPeriod != 0 || event.selection == nil || event.selection.rule == nil {
		return catalogPerformanceDecision{}
	}
	scope := event.selection.rule.Definition.Performance
	if !scope.active() {
		return catalogPerformanceDecision{}
	}
	if event.originClosed {
		return catalogPerformanceDecision{excluded: true}
	}
	if decision := d.matchAppointment(scope.Appointment, event.PaidAt); decision.excluded || decision.issue != "" {
		return decision
	}
	return d.matchSales(event, scope.UnitIDs, 0)
}

func (d catalogPerformanceDirectory) matchItem(event catalogActivityEvent, item catalogPeriodItem) catalogPerformanceDecision {
	if decision := d.matchAppointment(item.Appointment, event.PaidAt); decision.excluded || decision.issue != "" {
		return decision
	}
	var seller int64
	if item.OwnSales {
		seller = item.PersonID
	}
	return d.matchSales(event, item.CommissionUnits, seller)
}

func (d catalogPerformanceDirectory) matchAppointment(duty *catalogPerformanceDuty, at time.Time) catalogPerformanceDecision {
	if duty == nil {
		return catalogPerformanceDecision{}
	}
	intervals := d.duties[*duty]
	if len(intervals) == 0 {
		return catalogPerformanceDecision{issue: "AGGREGATE_PERFORMANCE_APPOINTMENT_MISSING"}
	}
	return catalogPerformanceDecision{excluded: !slices.ContainsFunc(intervals, func(interval vipInterval) bool { return interval.contains(at) })}
}

func (d catalogPerformanceDirectory) matchSales(event catalogActivityEvent, units []string, seller int64) catalogPerformanceDecision {
	if len(units) == 0 && seller == 0 {
		return catalogPerformanceDecision{}
	}
	if issue := d.claimIssues[event.PaymentID]; issue != "" {
		return catalogPerformanceDecision{issue: issue}
	}
	resolve := func(links [2]map[string][]vipLink, key string) (string, bool) {
		value, conflict := resolveVIPLink(links[0][key], key, event.PaidAt)
		if value == "" && !conflict {
			value, conflict = resolveVIPLink(links[1][key], key, event.PaidAt)
		}
		return value, conflict
	}
	claim := d.claims[event.PaymentID]
	var company pgtype.UUID
	if len(units) != 0 && claim.Company != "" {
		key, conflict := resolve(d.companies, claim.Company)
		if conflict {
			return catalogPerformanceDecision{issue: "SALES_COMPANY_CONFLICT"}
		}
		if company.Scan(key) != nil || !company.Valid {
			return catalogPerformanceDecision{issue: "AGGREGATE_SALES_ORGANIZATION_MISSING"}
		}
		if slices.Contains(units, company.String()) {
			return catalogPerformanceDecision{}
		}
		if !slices.ContainsFunc(units, func(id string) bool {
			var unit pgtype.UUID
			return unit.Scan(id) == nil && d.organization.ancestors(unit).Company == company
		}) {
			return catalogPerformanceDecision{excluded: true}
		}
	}
	key, conflict := resolve(d.users, claim.User)
	if conflict {
		return catalogPerformanceDecision{issue: "SALES_PERSON_CONFLICT"}
	}
	person, err := strconv.ParseInt(key, 10, 64)
	if err != nil || person <= 0 {
		return catalogPerformanceDecision{issue: "SALES_PERSON_MISSING"}
	}
	if seller != 0 {
		return catalogPerformanceDecision{excluded: person != seller}
	}
	org := d.organization.resolveAssignments(person, event.PaidAt, company, !company.Valid)
	if org.Company.Valid && slices.Contains(units, org.Company.String()) {
		return catalogPerformanceDecision{}
	}
	if !org.Department.Valid && org.Group == nil && len(org.scopeAlternatives) == 0 {
		return catalogPerformanceDecision{issue: "AGGREGATE_SALES_ORGANIZATION_MISSING"}
	}
	matches, conflict := matchOrganizationScope(units, org.Department.String(), org.groupID().String(), org.scopeAlternatives)
	if conflict {
		return catalogPerformanceDecision{issue: "SALES_ORGANIZATION_SCOPE_CONFLICT"}
	}
	return catalogPerformanceDecision{excluded: !matches}
}
