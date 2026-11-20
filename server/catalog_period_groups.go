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
	PaymentID int64
	PaidAt    time.Time
	Scenario  string
	Amount    int64
	Refund    bool
	selection *catalogAggregateMatch
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
}

type catalogPeriodGroupReport struct {
	Paused        bool                 `json:"paused,omitempty"`
	TrialOnPaused bool                 `json:"trialOnPaused,omitempty"`
	Groups        []catalogPeriodGroup `json:"groups"`
	Issues        map[string]int       `json:"issues"`
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
	BeforeCarry catalogPeriodQuote         `json:"beforeCarry"`
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
	for _, org := range d.Organizations {
		if org.Party == item.OrganizationKey && (item.unit == "" || org.Unit == item.unit) {
			if key.Unit != "" && key.Unit != org.Unit {
				return target, key, "BENEFIT_ORG_CONFLICT"
			}
			key.Unit, target.Name = org.Unit, org.Name
		}
	}
	if key.Unit == "" {
		if d.InactiveUnits[item.unit] {
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

func groupCatalogPeriodEvents(events []catalogActivityEvent, rules []catalogAggregateRule, directory catalogAggregateDirectory) (catalogPeriodGroupReport, error) {
	report := catalogPeriodGroupReport{Groups: []catalogPeriodGroup{}, Issues: map[string]int{}}
	type assignment struct {
		group *catalogPeriodGroup
		issue string
	}
	byPayment := make(map[int64]assignment)
	groups := make(map[string]*catalogPeriodGroup)
	ruleItems := make(map[string][]catalogPeriodItem, len(rules))
	for _, rule := range rules {
		ruleItems[rule.ID] = rule.Definition.Items
	}
	for _, event := range events {
		applied, found := byPayment[event.PaymentID]
		if !found {
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
				for _, item := range rule.Definition.Items {
					target, key, issue := directory.groupTarget(item, event.PaidAt)
					if issue != "" {
						applied.issue = issue
						break
					}
					targets, keys = append(targets, target), append(keys, key)
				}
				if applied.issue == "" {
					raw, err := json.Marshal(struct {
						Rule    string
						Targets []catalogGroupTargetKey
					}{rule.ID, keys})
					if err != nil {
						return report, err
					}
					key := string(raw)
					applied.group = groups[key]
					if applied.group == nil {
						hash := sha256.Sum256(raw)
						applied.group = &catalogPeriodGroup{ID: hex.EncodeToString(hash[:]), RuleID: rule.ID, Version: rule.Version, Targets: targets}
						groups[key] = applied.group
					}
				}
			}
			byPayment[event.PaymentID] = applied
		}
		if applied.issue != "" {
			report.Issues[applied.issue]++
			continue
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
		group.BeforeCarry, err = quoteCatalogPeriod(ruleItems[group.RuleID], group.Receipts, group.Refunds, 0)
		if err != nil {
			return report, err
		}
		report.Groups = append(report.Groups, *group)
	}
	return report, nil
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
	var parties, units, roles []string
	for _, version := range rules {
		for _, item := range version.Definition.Items {
			parties = append(parties, item.OrganizationKey)
			if item.TargetKind == "pool" {
				units = append(units, item.unit)
				roles = append(roles, item.OwnerRole, item.MemberRole)
			}
		}
	}
	rows, err := tx.Query(ctx, `SELECT b.party_uuid::text,o.id::text,o.name
	 FROM catalog_benefit_organizations b JOIN catalog_sales_context c ON c.source=b.source AND c.captured_at=b.observed_at
	 JOIN organization_units o ON o.id=b.unit_id
	 WHERE b.source='DCAI' AND c.benefit_ready AND b.party_uuid::text=ANY($1)
	 UNION ALL SELECT '',o.id::text,o.name FROM organization_units o WHERE o.id::text=ANY($2) AND o.status='INACTIVE'`, parties, units)
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
