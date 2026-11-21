package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

type catalogActivityEvent struct {
	EventID             int64
	PaymentID           int64
	CompletionVersionID int64
	PaidAt              time.Time
	RecognizedAt        time.Time
	Scenario            string
	Amount              int64
	Refund              bool
	originClosed        bool
	selection           *catalogAggregateMatch
	frozen              catalogFrozenGroupReference
}

type catalogAggregateRule struct {
	ID         string
	Version    int
	From       time.Time
	Through    *time.Time
	Definition catalogPeriodDefinition
}

type catalogAggregateDutyKey struct{ Unit, Role string }

type catalogAggregateDuty struct {
	ID   int64
	Name string
	vipInterval
}

type catalogAggregateDirectory struct {
	Organizations []vipBenefitOrganization
	Duties        map[catalogAggregateDutyKey][]catalogAggregateDuty
	InactiveUnits map[string]bool
	People        map[int64]string
}

type catalogPeriodGroupReport struct {
	Paused          bool                       `json:"paused,omitempty"`
	TrialOnPaused   bool                       `json:"trialOnPaused,omitempty"`
	PriorUnsettled  bool                       `json:"priorUnsettled,omitempty"`
	Groups          []catalogPeriodGroup       `json:"groups"`
	Issues          map[string]int             `json:"issues"`
	TargetIssues    []catalogPeriodTargetIssue `json:"targetIssues,omitempty"`
	carryFromPeriod int64
	postingIssues   []draftIssue
}

type catalogPeriodTargetIssue struct {
	RuleID            string `json:"ruleId"`
	Version           int    `json:"version"`
	ItemKey           string `json:"itemKey"`
	ProposedItemIndex *int   `json:"proposedItemIndex,omitempty"`
	Label             string `json:"label"`
	Code              string `json:"code"`
	Count             int    `json:"count"`
}

type catalogPeriodGroup struct {
	ID          string                     `json:"id"`
	RuleID      string                     `json:"ruleId"`
	Version     int                        `json:"version"`
	Targets     []catalogPeriodGroupTarget `json:"targets"`
	Receipts    int64                      `json:"receiptsMinor"`
	Refunds     int64                      `json:"refundsMinor"`
	Net         int64                      `json:"netMinor"`
	Payments    int                        `json:"paymentCount"`
	FixedOnly   bool                       `json:"fixedOnly,omitempty"`
	BeforeCarry catalogPeriodQuote         `json:"beforeCarry"`
	CarryIn     int64                      `json:"carryInMinor,omitempty"`
	AfterCarry  *catalogPeriodQuote        `json:"afterCarry,omitempty"`
	posting     catalogAggregateGroupInput
	items       []catalogPeriodItem
}

func (g catalogPeriodGroup) quote() catalogPeriodQuote {
	if g.AfterCarry != nil {
		return *g.AfterCarry
	}
	return g.BeforeCarry
}

type catalogPeriodGroupTarget struct {
	ItemKey     string `json:"itemKey"`
	Name        string `json:"name"`
	OwnerName   string `json:"ownerName,omitempty"`
	MemberCount int    `json:"memberCount,omitempty"`
}

type catalogGroupTargetKey struct {
	Item, Unit string
	Owner      int64
	Members    []int64
	Person     int64 `json:"person,omitempty"`
}

// Posting keys preserve the applied version. Carry uses the same scope and
// arrangements, with the resolved recipients of each payment group.
func catalogAggregateCarryKey(definition catalogPeriodDefinition, targets []catalogGroupTargetKey) (string, error) {
	type fixedArrangement struct {
		Item, Kind, Unit, OwnerRole, MemberRole string
		Person                                  int64
	}
	var fixed []fixedArrangement
	for _, item := range definition.Items {
		if item.FixedMinor != nil {
			unit := item.unit
			if unit == "" {
				unit = item.OrganizationKey
			}
			fixed = append(fixed, fixedArrangement{item.ItemKey, item.TargetKind, unit, item.OwnerRole, item.MemberRole, item.PersonID})
		}
	}
	slices.SortFunc(fixed, func(a, b fixedArrangement) int { return cmp.Compare(a.Item, b.Item) })
	canonicalTargets := slices.Clone(targets)
	slices.SortFunc(canonicalTargets, func(a, b catalogGroupTargetKey) int { return cmp.Compare(a.Item, b.Item) })
	scope := catalogPerformanceScope{}
	if definition.Performance.active() {
		scope = *definition.Performance
		scope.UnitIDs = slices.Sorted(slices.Values(scope.UnitIDs))
	}
	raw, err := json.Marshal(struct {
		Cycle       string
		Scenarios   []string
		Recognition bool
		Scope       catalogPerformanceScope
		Targets     []catalogGroupTargetKey
		Fixed       []fixedArrangement
	}{definition.Cycle, slices.Sorted(slices.Values(definition.Scenarios)), definition.ProductRecognition, scope, canonicalTargets, fixed})
	return string(raw), err
}

func catalogAggregateMemberRole(role, eligibility string) string {
	if eligibility != "" && eligibility != role {
		return eligibility
	}
	switch role {
	case "AGENT_SALES", "EMPLOYEE_SALES", "INTERNAL_SALES":
		return "SALES"
	default:
		return role
	}
}

func (d catalogAggregateDirectory) members(unit, role string, at time.Time) []int64 {
	var members []int64
	for _, duty := range d.Duties[catalogAggregateDutyKey{unit, role}] {
		if duty.contains(at) {
			members = append(members, duty.ID)
		}
	}
	slices.Sort(members)
	return slices.Compact(members)
}

func (d catalogAggregateDirectory) groupTarget(item catalogPeriodItem, at time.Time) (catalogPeriodGroupTarget, catalogGroupTargetKey, string) {
	target := catalogPeriodGroupTarget{ItemKey: item.ItemKey}
	key := catalogGroupTargetKey{Item: item.ItemKey}
	if item.TargetKind == "person" {
		key.Person, target.Name = item.PersonID, d.People[item.PersonID]
		if target.Name == "" {
			return target, key, "BENEFIT_PERSON_MISSING"
		}
		return target, key, ""
	}
	for _, org := range d.Organizations {
		if org.Party == item.OrganizationKey && (item.unit == "" || org.Unit == item.unit) {
			if key.Unit != "" && key.Unit != org.Unit {
				return target, key, "BENEFIT_ORG_CONFLICT"
			}
			key.Unit, target.Name = org.Unit, org.Name
		}
	}
	if key.Unit == "" {
		if d.InactiveUnits[item.unit] || d.InactiveUnits[item.OrganizationKey] {
			return target, key, "BENEFIT_ORG_INACTIVE"
		}
		return target, key, "BENEFIT_ORG_MISSING"
	}
	if item.TargetKind != "pool" {
		return target, key, ""
	}
	target.Name = item.PoolName
	owners := d.members(key.Unit, item.OwnerRole, at)
	switch len(owners) {
	case 0:
		return target, key, "BENEFIT_POOL_OWNER_MISSING"
	case 1:
		key.Owner = owners[0]
	default:
		return target, key, "BENEFIT_POOL_OWNER_CONFLICT"
	}
	for _, duty := range d.Duties[catalogAggregateDutyKey{key.Unit, item.OwnerRole}] {
		if duty.ID == key.Owner && duty.contains(at) {
			target.OwnerName = duty.Name
			break
		}
	}
	key.Members = d.members(key.Unit, item.MemberRole, at)
	if len(key.Members) == 0 {
		return target, key, "BENEFIT_POOL_MEMBERS_MISSING"
	}
	target.MemberCount = len(key.Members)
	return target, key, ""
}

type catalogAggregateMatch struct {
	rule     *catalogAggregateRule
	issue    string
	excluded bool
}

func matchCatalogAggregateRule(rules []catalogAggregateRule, event catalogActivityEvent) catalogAggregateMatch {
	var result catalogAggregateMatch
	hasRuleAtPayment := false
	for i := range rules {
		rule := &rules[i]
		if event.PaidAt.Before(rule.From) || rule.Through != nil && !event.PaidAt.Before(*rule.Through) {
			continue
		}
		hasRuleAtPayment = true
		if !slices.Contains(rule.Definition.Scenarios, event.Scenario) {
			continue
		}
		if result.rule != nil {
			return catalogAggregateMatch{issue: "ORIGINAL_RULE_CONFLICT"}
		}
		result.rule = rule
	}
	if result.rule == nil {
		result.excluded = hasRuleAtPayment
		if !result.excluded {
			result.issue = "ORIGINAL_RULE_MISSING"
		}
	}
	return result
}

func groupCatalogPeriodEvents(events []catalogActivityEvent, rules []catalogAggregateRule, directory catalogAggregateDirectory,
	frozen map[catalogFrozenGroupReference]catalogPeriodGroup) (catalogPeriodGroupReport, error) {
	report := catalogPeriodGroupReport{Groups: []catalogPeriodGroup{}, Issues: map[string]int{}}
	type assignment struct {
		group       *catalogPeriodGroup
		issue       string
		targetIssue *catalogPeriodTargetIssue
	}
	byPayment := make(map[int64]assignment)
	groups := make(map[string]*catalogPeriodGroup)
	type targetIssueKey struct{ rule, item, code string }
	targetIssues := make(map[targetIssueKey]*catalogPeriodTargetIssue)
	for _, event := range events {
		applied, found := byPayment[event.PaymentID]
		if !found && event.frozen.PeriodID != 0 {
			original, known := frozen[event.frozen]
			if known {
				applied.group = groups[original.ID]
				if applied.group == nil {
					applied.group = &original
					groups[original.ID] = applied.group
				}
			} else {
				applied.issue = "ORIGINAL_RULE_MISSING"
			}
			byPayment[event.PaymentID] = applied
		} else if !found {
			var selected catalogAggregateMatch
			if event.selection != nil {
				selected = *event.selection
			} else {
				selected = matchCatalogAggregateRule(rules, event)
			}
			if selected.excluded {
				continue
			}
			rule := selected.rule
			applied.issue = selected.issue
			if rule != nil {
				targets := make([]catalogPeriodGroupTarget, 0, len(rule.Definition.Items))
				keys := make([]catalogGroupTargetKey, 0, len(rule.Definition.Items))
				var items []catalogPeriodItem
				for index, item := range rule.Definition.Items {
					if item.FixedMinor != nil {
						continue
					}
					target, key, issue := directory.groupTarget(item, event.PaidAt)
					if issue != "" {
						applied.issue = issue
						issueKey := targetIssueKey{rule.ID, item.ItemKey, issue}
						problem := targetIssues[issueKey]
						if problem == nil {
							problem = &catalogPeriodTargetIssue{RuleID: rule.ID, Version: rule.Version,
								ItemKey: item.ItemKey, Label: item.Label, Code: issue}
							if rule.ID == "" {
								problem.ProposedItemIndex = &index
							}
							targetIssues[issueKey] = problem
						}
						applied.targetIssue = problem
						break
					}
					targets, keys = append(targets, target), append(keys, key)
					items = append(items, item)
				}
				if applied.issue == "" {
					raw, err := json.Marshal(struct {
						Rule    string
						Targets []catalogGroupTargetKey
					}{rule.ID, keys})
					if err != nil {
						return report, err
					}
					hash := sha256.Sum256(raw)
					key := hex.EncodeToString(hash[:])
					applied.group = groups[key]
					if applied.group == nil {
						applied.group = &catalogPeriodGroup{ID: key, RuleID: rule.ID, Version: rule.Version, Targets: targets,
							posting: catalogAggregateGroupInput{Targets: keys}, items: items}
						groups[key] = applied.group
					}
				}
			}
			byPayment[event.PaymentID] = applied
		}
		if applied.issue != "" {
			report.Issues[applied.issue]++
			if applied.targetIssue != nil {
				applied.targetIssue.Count++
			}
			if event.EventID != 0 {
				report.postingIssues = append(report.postingIssues, draftIssue{FactID: event.EventID, Code: applied.issue})
			}
			continue
		}
		if event.EventID != 0 {
			posting := &applied.group.posting
			posting.Events = append(posting.Events, catalogAggregateEventReference{
				EventID: event.EventID, PaymentID: event.PaymentID, Scenario: event.Scenario, CompletionVersionID: event.CompletionVersionID,
			})
		}
		sum := &applied.group.Refunds
		if !event.Refund {
			applied.group.Payments++
			sum = &applied.group.Receipts
		}
		if err := addCatalogActivityAmount(event.Amount, sum); err != nil {
			return report, err
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(cmp.Compare(groups[a].Version, groups[b].Version), cmp.Compare(a, b))
	})
	for _, key := range keys {
		group := groups[key]
		group.Net = group.Receipts - group.Refunds
		var err error
		group.BeforeCarry, err = quoteCatalogPeriod(group.items, group.Receipts, group.Refunds, 0)
		if err != nil {
			return report, err
		}
		report.Groups = append(report.Groups, *group)
	}
	for _, problem := range targetIssues {
		report.TargetIssues = append(report.TargetIssues, *problem)
	}
	slices.SortFunc(report.TargetIssues, func(a, b catalogPeriodTargetIssue) int {
		return cmp.Or(cmp.Compare(a.Version, b.Version), cmp.Compare(a.ItemKey, b.ItemKey), cmp.Compare(a.Code, b.Code))
	})
	return report, nil
}

func appendCatalogPeriodFixedGroup(report *catalogPeriodGroupReport, rules []catalogAggregateRule, directory catalogAggregateDirectory, at time.Time) error {
	index := slices.IndexFunc(rules, func(rule catalogAggregateRule) bool {
		return !at.Before(rule.From) && (rule.Through == nil || at.Before(*rule.Through))
	})
	if index < 0 {
		return nil
	}
	rule := rules[index]
	if !slices.ContainsFunc(rule.Definition.Items, func(item catalogPeriodItem) bool { return item.FixedMinor != nil && *item.FixedMinor > 0 }) {
		return nil
	}
	group := catalogPeriodGroup{RuleID: rule.ID, Version: rule.Version, FixedOnly: true}
	for _, other := range report.Groups {
		if err := addCatalogActivityAmount(other.Receipts, &group.Receipts); err != nil {
			return err
		}
		if err := addCatalogActivityAmount(other.Refunds, &group.Refunds); err != nil {
			return err
		}
		if err := addCatalogActivityAmount(other.CarryIn, &group.CarryIn); err != nil {
			return err
		}
		group.Payments += other.Payments
	}
	group.Net = group.Receipts - group.Refunds
	net, err := addMinor(group.Net, group.CarryIn)
	if err != nil {
		return err
	}
	if net <= 0 {
		return nil
	}
	for index, item := range rule.Definition.Items {
		if item.FixedMinor == nil || *item.FixedMinor == 0 {
			continue
		}
		target, key, issue := directory.groupTarget(item, at)
		if issue != "" {
			report.Issues[issue]++
			problem := catalogPeriodTargetIssue{RuleID: rule.ID, Version: rule.Version, ItemKey: item.ItemKey,
				Label: item.Label, Code: issue, Count: 1}
			if rule.ID == "" {
				problem.ProposedItemIndex = &index
			}
			report.TargetIssues = append(report.TargetIssues, problem)
			report.postingIssues = append(report.postingIssues, draftIssue{Component: item.Label, Code: issue})
			continue
		}
		group.Targets = append(group.Targets, target)
		group.posting.Targets = append(group.posting.Targets, key)
		group.items = append(group.items, item)
	}
	if len(group.items) == 0 {
		return nil
	}
	groupID := sha256.Sum256([]byte("fixed-period:" + rule.ID))
	group.ID = hex.EncodeToString(groupID[:])
	group.BeforeCarry, err = quoteCatalogPeriod(group.items, group.Receipts, group.Refunds, 0)
	if err != nil {
		return err
	}
	if group.CarryIn != 0 {
		quote, err := quoteCatalogPeriod(group.items, group.Receipts, group.Refunds, group.CarryIn)
		if err != nil {
			return err
		}
		group.AfterCarry = &quote
	}
	report.Groups = append(report.Groups, group)
	return nil
}

const catalogAggregateVersionsSQL = `SELECT v.source_id::text,v.version_no,greatest(v.effective_from,p.resume_from),
 ` + catalogDisplayedThrough + `, v.definition
 FROM product_rule_packages p JOIN product_catalog_settings scope ON scope.product_id=p.product_id
 JOIN product_rule_versions v ON v.package_id=p.source_id
 WHERE p.source_id=$1::uuid AND ` + catalogPeriodRuleScope + ` AND v.source_status='PUBLISHED' AND ` + catalogVersionVisible + `
 AND greatest(v.effective_from,p.resume_from)<$2 AND v.definition->'settlement_policy'->>'cycle'=$3
 ORDER BY v.version_no`

func readCatalogAggregateVersions(ctx context.Context, tx pgx.Tx, packageID string, through time.Time, cycle string) ([]catalogAggregateRule, error) {
	rows, err := tx.Query(ctx, catalogAggregateVersionsSQL, packageID, through, cycle)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (catalogAggregateRule, error) {
		var rule catalogAggregateRule
		var raw json.RawMessage
		if err := row.Scan(&rule.ID, &rule.Version, &rule.From, &rule.Through, &raw); err != nil {
			return rule, err
		}
		var err error
		rule.Definition, err = parseCatalogPeriodRule(raw)
		return rule, err
	})
}

const catalogAggregateDutySQL = `SELECT a.scope_key,a.role,person.id,person.name,
 greatest(a.valid_from,m.valid_from),least(a.valid_to,m.valid_to)
 FROM catalog_current_sales_appointments a JOIN people person ON person.id=a.person_id
 JOIN organization_assignments m ON m.person_id=a.person_id AND m.unit_id::text=a.scope_key
 WHERE a.source='DCAI' AND a.scope='ORGANIZATION_UNIT' AND a.scope_key=ANY($1) AND a.role=ANY($2)
 AND greatest(a.valid_from,m.valid_from)<$4 AND (least(a.valid_to,m.valid_to) IS NULL OR
 least(a.valid_to,m.valid_to)>greatest(a.valid_from,m.valid_from,$3))
 ORDER BY a.scope_key,a.role,person.id`

func readCatalogAggregateDirectory(ctx context.Context, tx pgx.Tx, rules []catalogAggregateRule, from, through time.Time) (catalogAggregateDirectory, error) {
	directory := catalogAggregateDirectory{Duties: map[catalogAggregateDutyKey][]catalogAggregateDuty{}, InactiveUnits: make(map[string]bool)}
	var parties, units, directUnits, targetUnits, roles []string
	var people []int64
	for _, version := range rules {
		for _, item := range version.Definition.Items {
			if item.PersonID > 0 {
				people = append(people, item.PersonID)
			} else if unit, direct := catalogPeriodUnit(item.OrganizationKey); direct {
				directUnits = append(directUnits, unit)
			} else {
				parties = append(parties, item.OrganizationKey)
			}
			if item.unit != "" {
				targetUnits = append(targetUnits, item.unit)
			}
			if item.TargetKind == "pool" {
				units = append(units, item.unit)
				roles = append(roles, item.OwnerRole, item.MemberRole)
			}
		}
	}
	if len(people) > 0 {
		rows, err := tx.Query(ctx, "SELECT id,name FROM people WHERE id=ANY($1::bigint[])", people)
		if err != nil {
			return directory, err
		}
		directory.People = make(map[int64]string)
		var id int64
		var name string
		if _, err := pgx.ForEachRow(rows, []any{&id, &name}, func() error {
			directory.People[id] = name
			return nil
		}); err != nil {
			return directory, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT CASE WHEN o.status='ACTIVE' THEN b.party_uuid::text ELSE '' END,
	 CASE WHEN o.status='ACTIVE' THEN o.id::text ELSE b.party_uuid::text END,o.name
	 FROM catalog_benefit_organizations b JOIN catalog_sales_context c ON c.source=b.source AND c.captured_at=b.observed_at
	 JOIN organization_units o ON o.id=b.unit_id
	 WHERE b.source='DCAI' AND c.benefit_ready AND b.party_uuid::text=ANY($1)
	 UNION ALL SELECT '',o.id::text,o.name FROM organization_units o WHERE o.id::text=ANY($2) AND o.status='INACTIVE'
	 UNION ALL SELECT $4 || o.id::text,o.id::text,o.name FROM organization_units o WHERE o.id=ANY($3::uuid[]) AND o.status='ACTIVE'`,
		parties, targetUnits, directUnits, catalogPeriodUnitPrefix)
	if err != nil {
		return directory, err
	}
	var org vipBenefitOrganization
	_, err = pgx.ForEachRow(rows, []any{&org.Party, &org.Unit, &org.Name}, func() error {
		if org.Party == "" {
			directory.InactiveUnits[org.Unit] = true
		} else {
			directory.Organizations = append(directory.Organizations, org)
		}
		return nil
	})
	if err != nil || len(units) == 0 {
		return directory, err
	}
	rows, err = tx.Query(ctx, catalogAggregateDutySQL, units, roles, from, through)
	if err != nil {
		return directory, err
	}
	var key catalogAggregateDutyKey
	var duty catalogAggregateDuty
	_, err = pgx.ForEachRow(rows, []any{&key.Unit, &key.Role, &duty.ID, &duty.Name, &duty.From, &duty.To}, func() error {
		directory.Duties[key] = append(directory.Duties[key], duty)
		return nil
	})
	return directory, err
}
