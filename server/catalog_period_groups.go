package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type catalogActivityEvent struct {
	EventID             int64
	PaymentID           int64
	CompletionVersionID int64
	PaidAt              time.Time
	RecognizedAt        time.Time
	OccurredAt          time.Time
	Scenario            string
	MembershipType      *string // nil for orders outside the membership source; empty for unmarked membership orders.
	Amount              int64
	OriginalAmount      int64
	PriorBookedRefunds  int64
	Refund              bool
	originClosed        bool
	selection           *catalogAggregateMatch
	frozenPeriod        int64
	frozenGroups        []catalogFrozenGroupReference
	fixedHistory        map[string]catalogFixedRefundHistory
}

type catalogFixedRefundHistory struct {
	accrued                         *int64
	refundedBeforePosting, deducted int64
}

func (h catalogFixedRefundHistory) original(award, gross int64) refundObligation {
	if h.accrued != nil {
		award = *h.accrued
	} else {
		award = legacyPosting.unpostedNet(award, h.refundedBeforePosting, gross)
	}
	return refundObligation{Base: gross - h.refundedBeforePosting, Accrued: award}
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
	ID       int64
	Name     string
	UnitName string
	vipInterval
}

type catalogAggregateDirectory struct {
	Organizations     []vipBenefitOrganization
	Duties            map[catalogAggregateDutyKey][]catalogAggregateDuty
	AppointmentDuties map[catalogAggregateDutyKey][]catalogAggregateDuty
	InactiveUnits     map[string]bool
	People            map[int64]string
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
	events          []catalogAggregateEventReference
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
	ID                string                     `json:"id"`
	RuleID            string                     `json:"ruleId"`
	Version           int                        `json:"version"`
	Targets           []catalogPeriodGroupTarget `json:"targets"`
	Receipts          int64                      `json:"receiptsMinor"`
	Refunds           int64                      `json:"refundsMinor"`
	Net               int64                      `json:"netMinor"`
	Payments          int                        `json:"paymentCount"`
	FixedOnly         bool                       `json:"fixedOnly,omitempty"`
	FixedPerOrder     bool                       `json:"fixedPerOrder,omitempty"`
	PerOrder          bool                       `json:"perOrder,omitempty"`
	paymentID         int64
	EventToken        string              `json:"eventToken,omitempty"`
	BeforeCarry       catalogPeriodQuote  `json:"beforeCarry"`
	CarryIn           int64               `json:"carryInMinor,omitempty"`
	AfterCarry        *catalogPeriodQuote `json:"afterCarry,omitempty"`
	posting           catalogAggregateGroupInput
	items             []catalogPeriodItem
	fixedPoolKey      string
	orderAccruedMinor int64
	orderExactCredit  *big.Rat
	originalLineID    int64
	orderRefunds      []refundLine
}

func (g catalogPeriodGroup) quote() catalogPeriodQuote {
	if g.AfterCarry != nil {
		return *g.AfterCarry
	}
	return g.BeforeCarry
}

func (g catalogPeriodGroup) countsOrders() bool { return g.FixedPerOrder || g.PerOrder }

func (g catalogPeriodGroup) calculateQuote(carry int64) (catalogPeriodQuote, error) {
	if !g.countsOrders() {
		return quoteCatalogPeriod(g.items, g.Receipts, g.Refunds, carry)
	}
	item := g.items[0]
	var rate int64
	if item.FixedMinor == nil {
		rate = item.Tiers[0].Rate
	}
	return catalogPeriodQuote{Basis: max(g.Net, 0), Total: g.orderAccruedMinor,
		Awards: []catalogPeriodAward{{ItemKey: item.ItemKey, Label: item.Label, Fixed: item.FixedMinor != nil, Rate: rate, Amount: g.orderAccruedMinor}}}, nil
}

type catalogPeriodGroupTarget struct {
	ItemKey          string `json:"itemKey"`
	Name             string `json:"name"`
	OwnerName        string `json:"ownerName,omitempty"`
	MemberCount      int    `json:"memberCount,omitempty"`
	OrganizationName string `json:"organizationName,omitempty"`
}

type catalogGroupTargetKey struct {
	Item, Unit      string
	Owner           int64
	Members         []int64
	Person          int64  `json:"person,omitempty"`
	PoolID          int64  `json:"poolId,omitempty"`
	AppointmentUnit string `json:"appointmentUnit,omitempty"`
}

// Posting keys preserve the applied version. Carry uses the same scope and
// arrangements, with the resolved recipients of each payment group.
func catalogAggregateCarryKey(definition catalogPeriodDefinition, targets []catalogGroupTargetKey) (string, error) {
	type orderScope struct {
		Item              string
		Units             []string
		OwnSales          bool
		Appointment       *catalogPerformanceDuty
		Categories        []string `json:",omitempty"`
		SKUs              []string `json:",omitempty"`
		MembershipNewOnly bool     `json:",omitempty"`
		AppointmentLevel  string   `json:",omitempty"`
		AppointmentRole   string   `json:",omitempty"`
	}
	scopes := make([]orderScope, 0, len(targets))
	canonicalTargets := slices.Clone(targets)
	scenarios := definition.Scenarios
	for i, target := range targets {
		for _, item := range definition.Items {
			if item.ItemKey == target.Item {
				scope := orderScope{Item: item.ItemKey, Units: slices.Sorted(slices.Values(item.CommissionUnits)),
					OwnSales: item.OwnSales, Appointment: item.Appointment, Categories: slices.Sorted(slices.Values(item.ScopeCategories)),
					MembershipNewOnly: item.MembershipNewOnly, AppointmentLevel: item.AppointmentLevel, AppointmentRole: item.AppointmentRole}
				if target.PoolID > 0 && item.TargetKind == "pool" && item.FixedMinor != nil && !item.FixedPerOrder {
					// The pool receives the fixed award. Its distribution roster can change independently.
					canonicalTargets[i].Owner = 0
					canonicalTargets[i].Members = nil
				}
				if len(item.ScenarioCodes) > 0 {
					if len(targets) == 1 {
						scenarios = item.ScenarioCodes
					} else {
						scope.SKUs = slices.Sorted(slices.Values(item.ScenarioCodes))
					}
				}
				scopes = append(scopes, scope)
				break
			}
		}
	}
	slices.SortFunc(scopes, func(a, b orderScope) int { return cmp.Compare(a.Item, b.Item) })
	slices.SortFunc(canonicalTargets, func(a, b catalogGroupTargetKey) int { return cmp.Compare(a.Item, b.Item) })
	shared := catalogPerformanceScope{}
	if definition.Performance.active() {
		shared = *definition.Performance
		shared.UnitIDs = slices.Sorted(slices.Values(shared.UnitIDs))
	}
	raw, err := json.Marshal(struct {
		Cycle       string
		Scenarios   []string
		Recognition bool
		Shared      catalogPerformanceScope
		Scopes      []orderScope
		Targets     []catalogGroupTargetKey
	}{definition.Cycle, slices.Sorted(slices.Values(scenarios)), definition.ProductRecognition, shared, scopes, canonicalTargets})
	return string(raw), err
}

func (g catalogPeriodGroup) fixedPoolSettlementKey(definition catalogPeriodDefinition) (string, error) {
	if !g.FixedOnly || g.countsOrders() || g.items[0].TargetKind != "pool" || g.posting.Targets[0].PoolID <= 0 {
		return "", nil
	}
	return catalogAggregateCarryKey(definition, g.posting.Targets)
}

func netCatalogFixedPoolRefunds(groups map[string]*catalogPeriodGroup) error {
	active := make(map[string]*catalogPeriodGroup)
	for _, group := range groups {
		if group.fixedPoolKey == "" || group.Receipts == 0 {
			continue
		}
		current := active[group.fixedPoolKey]
		if current == nil || group.Version > current.Version || group.Version == current.Version && group.ID < current.ID {
			active[group.fixedPoolKey] = group
		}
	}
	for id, group := range groups {
		target := active[group.fixedPoolKey]
		if target == nil || group.Receipts != 0 || group.Refunds == 0 {
			continue
		}
		// The refund keeps its original payment reference. Its principal reduces
		// this cycle's net receipts of the same arrangement and registered pool.
		if err := addCatalogActivityAmount(group.Refunds, &target.Refunds); err != nil {
			return err
		}
		target.posting.Events = append(target.posting.Events, group.posting.Events...)
		delete(groups, id)
	}
	return nil
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
	key.PoolID = item.PoolID
	if item.OwnerPersonID != 0 {
		key.Owner, target.OwnerName = item.OwnerPersonID, d.People[item.OwnerPersonID]
		if target.OwnerName == "" {
			return target, key, "BENEFIT_POOL_OWNER_MISSING"
		}
	} else if item.PoolID == 0 || item.OwnerRole != "" {
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
	}
	key.Members = d.members(key.Unit, item.MemberRole, at)
	if len(key.Members) == 0 {
		return target, key, "BENEFIT_POOL_MEMBERS_MISSING"
	}
	target.MemberCount = len(key.Members)
	return target, key, ""
}

func (d catalogAggregateDirectory) groupAppointmentTarget(item catalogPeriodItem, unit string, at time.Time) (catalogPeriodGroupTarget, catalogGroupTargetKey, string) {
	target := catalogPeriodGroupTarget{ItemKey: item.ItemKey}
	key := catalogGroupTargetKey{Item: item.ItemKey, AppointmentUnit: unit}
	for _, duty := range d.AppointmentDuties[catalogAggregateDutyKey{unit, item.AppointmentRole}] {
		if !duty.contains(at) {
			continue
		}
		if key.Person != 0 && key.Person != duty.ID {
			return target, key, "BENEFIT_APPOINTMENT_CONFLICT"
		}
		key.Person, target.Name, target.OrganizationName = duty.ID, duty.Name, duty.UnitName
	}
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
	frozen map[catalogFrozenGroupReference]catalogPeriodGroup, performance catalogPerformanceDirectory, periodEnd time.Time) (catalogPeriodGroupReport, error) {
	report := catalogPeriodGroupReport{Groups: []catalogPeriodGroup{}, Issues: map[string]int{}}
	type assignment struct {
		groups   []*catalogPeriodGroup
		issues   []*catalogPeriodTargetIssue
		issue    string
		excluded bool
	}
	type issueKey struct{ rule, item, code string }
	targetIssues := make(map[issueKey]*catalogPeriodTargetIssue)
	byPayment := make(map[int64]assignment)
	groups := make(map[string]*catalogPeriodGroup)
	type fixedPayment struct {
		original, priorRefunds, refunds int64
		paidHere                        bool
		refundEvents                    []catalogActivityEvent
	}
	fixedPayments := make(map[int64]fixedPayment)
	for _, event := range events {
		applied, found := byPayment[event.PaymentID]
		if !found && event.frozenPeriod != 0 {
			for _, ref := range event.frozenGroups {
				original, known := frozen[ref]
				if !known {
					return report, errProductConfigConflict
				}
				group := groups[original.ID]
				if group == nil {
					group = &original
					groups[original.ID] = group
				}
				applied.groups = append(applied.groups, group)
			}
		} else if !found {
			var selected catalogAggregateMatch
			if event.selection == nil {
				selected = matchCatalogAggregateRule(rules, event)
			} else {
				selected = *event.selection
			}
			applied.excluded, applied.issue = selected.excluded, selected.issue
			if rule := selected.rule; rule != nil {
				for index, item := range rule.Definition.Items {
					if len(item.ScenarioCodes) > 0 && !slices.Contains(item.ScenarioCodes, event.Scenario) {
						continue
					}
					typedMembership := item.MembershipNewOnly && event.MembershipType != nil
					if typedMembership && *event.MembershipType == "RENEWAL" {
						continue
					}
					decision := performance.matchItem(event, item)
					if decision.excluded {
						continue
					}
					at := event.PaidAt
					if item.FixedMinor != nil && !item.FixedPerOrder && !item.PerOrder && !periodEnd.IsZero() {
						at = periodEnd
					}
					var target catalogPeriodGroupTarget
					var key catalogGroupTargetKey
					issue := decision.issue
					if issue == "" {
						if item.TargetKind == "order_seller" {
							target, key, issue = performance.orderSellerTarget(event, item)
						} else if item.AppointmentLevel == "" {
							target, key, issue = directory.groupTarget(item, at)
						} else {
							unit, lookupIssue := performance.appointmentOrganization(event, item.AppointmentLevel)
							issue = lookupIssue
							if issue == "" {
								target, key, issue = directory.groupAppointmentTarget(item, unit, at)
								if issue == "" && key.Person == 0 {
									continue
								}
							}
						}
					}
					if issue == "" && typedMembership && *event.MembershipType != "NEW" {
						issue = "MEMBERSHIP_TYPE_PENDING"
					}
					if issue != "" {
						problemKey := issueKey{rule.ID, item.ItemKey, issue}
						problem := targetIssues[problemKey]
						if problem == nil {
							problem = &catalogPeriodTargetIssue{RuleID: rule.ID, Version: rule.Version, ItemKey: item.ItemKey, Label: item.Label, Code: issue}
							if rule.ID == "" {
								problem.ProposedItemIndex = &index
							}
							targetIssues[problemKey] = problem
						}
						applied.issues = append(applied.issues, problem)
						continue
					}
					groupTarget := key
					if rule.ID == "" {
						// New items receive random keys during every preview rebuild.
						groupTarget.Item = strconv.Itoa(index)
					}
					var paymentID int64
					if item.PerOrder {
						paymentID = event.PaymentID
					}
					raw, err := json.Marshal(struct {
						Rule      string
						Target    catalogGroupTargetKey
						PaymentID int64 `json:",omitempty"`
					}{rule.ID, groupTarget, paymentID})
					if err != nil {
						return report, err
					}
					hash := sha256.Sum256(raw)
					id := hex.EncodeToString(hash[:])
					group := groups[id]
					if group == nil {
						group = &catalogPeriodGroup{ID: id, RuleID: rule.ID, Version: rule.Version, Targets: []catalogPeriodGroupTarget{target},
							posting: catalogAggregateGroupInput{Targets: []catalogGroupTargetKey{key}}, items: []catalogPeriodItem{item},
							FixedOnly: item.FixedMinor != nil, FixedPerOrder: item.FixedPerOrder, PerOrder: item.PerOrder, paymentID: paymentID}
						group.fixedPoolKey, err = group.fixedPoolSettlementKey(rule.Definition)
						if err != nil {
							return report, err
						}
						groups[id] = group
					}
					applied.groups = append(applied.groups, group)
				}
			}
		}
		byPayment[event.PaymentID] = applied
		if applied.excluded {
			continue
		}
		if applied.issue != "" {
			report.Issues[applied.issue]++
			if event.EventID != 0 {
				report.postingIssues = append(report.postingIssues, draftIssue{FactID: event.EventID, Code: applied.issue})
			}
			continue
		}
		for _, problem := range applied.issues {
			problem.Count++
			report.Issues[problem.Code]++
			if event.EventID != 0 {
				report.postingIssues = append(report.postingIssues, draftIssue{FactID: event.EventID, Code: problem.Code, Component: problem.ItemKey})
			}
		}
		if event.EventID != 0 {
			report.events = append(report.events, catalogAggregateEventReference{EventID: event.EventID, PaymentID: event.PaymentID,
				Scenario: event.Scenario, CompletionVersionID: event.CompletionVersionID})
		}
		for _, group := range applied.groups {
			if event.EventID != 0 {
				group.posting.Events = append(group.posting.Events, event.EventID)
			}
			sum := &group.Refunds
			if !event.Refund {
				group.Payments++
				sum = &group.Receipts
			}
			if err := addCatalogActivityAmount(event.Amount, sum); err != nil {
				return report, err
			}
		}
		if slices.ContainsFunc(applied.groups, func(group *catalogPeriodGroup) bool { return group.countsOrders() }) {
			payment := fixedPayments[event.PaymentID]
			if event.Refund {
				if payment.original == 0 {
					payment.original = event.OriginalAmount
				}
				payment.priorRefunds = event.PriorBookedRefunds
				payment.refundEvents = append(payment.refundEvents, event)
				if err := addCatalogActivityAmount(event.Amount, &payment.refunds); err != nil {
					return report, err
				}
			} else {
				payment.original, payment.paidHere = event.Amount, true
			}
			fixedPayments[event.PaymentID] = payment
		}
	}
	for paymentID, payment := range fixedPayments {
		if payment.original <= 0 || !payment.paidHere && (len(payment.refundEvents) == 0 || payment.refundEvents[0].frozenPeriod == 0) {
			continue
		}
		if payment.priorRefunds > payment.original || payment.refunds > payment.original-payment.priorRefunds {
			return report, errRefundAmount
		}
		slices.SortFunc(payment.refundEvents, func(a, b catalogActivityEvent) int {
			return cmp.Or(a.OccurredAt.Compare(b.OccurredAt), cmp.Compare(a.EventID, b.EventID))
		})
		for _, group := range byPayment[paymentID].groups {
			if !group.countsOrders() {
				continue
			}
			item := group.items[0]
			var award int64
			if item.FixedMinor != nil {
				award = *item.FixedMinor
			} else {
				award = truncatePosting.rate(payment.original, item.Tiers[0].Rate, 10000)
			}
			var amount int64
			if payment.paidHere {
				base := payment.original - payment.refunds
				var precise *big.Rat
				if item.FixedMinor != nil {
					amount = truncatePosting.unpostedNet(award, payment.refunds, payment.original)
					precise = exactMinor(award, base, payment.original)
				} else {
					amount = truncatePosting.rate(base, item.Tiers[0].Rate, 10000)
					precise = exactMinor(base, item.Tiers[0].Rate, 10000)
				}
				if group.posting.FixedAccruals == nil {
					group.posting.FixedAccruals = make(map[int64]int64)
				}
				group.posting.FixedAccruals[paymentID] = amount
				if group.orderExactCredit == nil {
					group.orderExactCredit = new(big.Rat)
				}
				group.orderExactCredit.Add(group.orderExactCredit, precise)
			} else {
				history, known := payment.refundEvents[0].fixedHistory[group.ID]
				original := history.original(award, payment.original)
				refundPrincipal := payment.priorRefunds - history.refundedBeforePosting
				deducted := history.deducted
				if !known {
					deducted = prorateMinor(original.Accrued, refundPrincipal, original.Base)
				}
				if refundPrincipal < 0 {
					return report, errRefundAmount
				}
				policy := component{RefundPolicy: "proportional"}
				for _, event := range payment.refundEvents {
					refundPrincipal += event.Amount
					delta, err := refundDelta(policy, original.Base, original.Accrued, refundPrincipal, deducted)
					if err != nil {
						return report, err
					}
					precise := preciseRefundAmount(policy, original, refundPrincipal, deducted, delta)
					deducted -= delta
					amount += delta
					if delta != 0 && event.EventID != 0 {
						origin := frozen[catalogFrozenGroupReference{PeriodID: event.frozenPeriod, Key: group.ID}]
						group.orderRefunds = append(group.orderRefunds, refundLine{OriginalLineID: origin.originalLineID,
							RefundSourceVersionID: event.EventID, Amount: delta, Principal: event.Amount, OccurredAt: event.OccurredAt,
							PreciseAmount: precise})
					}
				}
			}
			if err := addCatalogActivityAmount(amount, &group.orderAccruedMinor); err != nil {
				return report, err
			}
		}
	}
	if err := netCatalogFixedPoolRefunds(groups); err != nil {
		return report, err
	}
	for _, group := range groups {
		group.Net = group.Receipts - group.Refunds
		var err error
		group.BeforeCarry, err = group.calculateQuote(0)
		if err != nil {
			return report, err
		}
		report.Groups = append(report.Groups, *group)
	}
	slices.SortFunc(report.Groups, func(a, b catalogPeriodGroup) int {
		if a.FixedOnly != b.FixedOnly {
			if a.FixedOnly {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(a.Version, b.Version), cmp.Compare(a.ID, b.ID))
	})
	for _, problem := range targetIssues {
		report.TargetIssues = append(report.TargetIssues, *problem)
	}
	slices.SortFunc(report.TargetIssues, func(a, b catalogPeriodTargetIssue) int {
		return cmp.Or(cmp.Compare(a.Version, b.Version), cmp.Compare(a.ItemKey, b.ItemKey), cmp.Compare(a.Code, b.Code))
	})
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
	var parties, units, directUnits, targetUnits, roles, appointmentRoles []string
	var people []int64
	for _, version := range rules {
		for _, item := range version.Definition.Items {
			if item.AppointmentLevel != "" {
				appointmentRoles = append(appointmentRoles, item.AppointmentRole)
			} else if item.PersonID > 0 {
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
				if item.OwnerPersonID != 0 {
					people = append(people, item.OwnerPersonID)
				}
			}
		}
	}
	if len(appointmentRoles) > 0 {
		rows, err := tx.Query(ctx, `SELECT a.scope_key,a.role,p.id,p.name,coalesce(u.name,''),a.valid_from,a.valid_to
		 FROM catalog_current_sales_appointments a JOIN people p ON p.id=a.person_id
		 LEFT JOIN organization_units u ON u.id::text=a.scope_key
		 WHERE a.source='DCAI' AND a.scope='ORGANIZATION_UNIT' AND a.role=ANY($1)
		  AND a.valid_from<$3 AND (a.valid_to IS NULL OR a.valid_to>$2)`, appointmentRoles, from, through)
		if err != nil {
			return directory, err
		}
		directory.AppointmentDuties = make(map[catalogAggregateDutyKey][]catalogAggregateDuty)
		var key catalogAggregateDutyKey
		var duty catalogAggregateDuty
		if _, err := pgx.ForEachRow(rows, []any{&key.Unit, &key.Role, &duty.ID, &duty.Name, &duty.UnitName, &duty.From, &duty.To}, func() error {
			directory.AppointmentDuties[key] = append(directory.AppointmentDuties[key], duty)
			return nil
		}); err != nil {
			return directory, err
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
