package main

import (
	"cmp"
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const catalogSplitDraftRule = `(v.definition->>'recognition_plan' IS NULL
 AND v.definition->'internal_sales_direct_split'->>'mode'='DCAI_INTERNAL_DIRECT')`

const catalogDirectDraftRule = `(v.published_at IS NOT NULL AND v.definition->>'recognition_plan' IS NULL
 AND v.definition->>'sales_attribution'='ORDER_SELLER')`

const vipSalesNotConfigured = "SALES_NOT_CONFIGURED"

const catalogDraftAvailableSQL = `(pro.period_mode='calendar' AND EXISTS(
 SELECT 1 FROM product_catalog_settings c
 JOIN product_rule_packages k ON k.product_id=c.product_id AND k.project_code=c.project_code
 JOIN product_rule_versions v ON v.package_id=k.source_id AND (v.observed_at=c.captured_at OR v.published_at IS NOT NULL)
 WHERE c.product_id=pro.id AND c.source_system='DCAI'
   AND k.scope_type='BUSINESS_SCENARIO'
   AND EXISTS(SELECT 1 FROM catalog_business_routes m WHERE m.product_id=c.product_id AND m.observed_at=c.captured_at
    AND (` + catalogSplitDraftRule + ` OR (m.source='DCAI' AND m.sku_code=k.scope_id AND ` + catalogDirectDraftRule + `
      AND (m.domain='VIP' OR (m.domain='HEALTH_IDENTITY' AND m.source_product_code='HEALTH_AMBASSADOR')))
     OR (m.source='DCAI' AND m.domain='HEALTH_IDENTITY' AND m.sku_code=k.scope_id
     AND ` + catalogHealthRecognitionPlan + `)))
))`

type catalogDraftBenefit struct {
	Rule, Unit        pgtype.UUID
	AllocationOwnerID int64
	Rate              int64
	PoolID            int64 `json:",omitempty"`
}

type catalogRuleImpactMatch struct {
	ruleID   string
	payments int
}

func computeVIPDraft(ctx context.Context, tx pgx.Tx, periodID int64, window periodWindow, quotes *vipDraftQuoteCache,
	impact *catalogRuleImpactMatch) (paymentDraft, []byte, error) {
	matches, err := readCatalogMatches(ctx, tx, periodID, 0, maxDraftPayments)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	if len(matches.Items) != matches.OrderCount {
		return paymentDraft{}, nil, errDraftLimit
	}
	if len(matches.Items) > maxDraftCachedRows {
		quotes = nil
	}
	data, err := loadVIPRuntimeData(ctx, tx, periodID, matches.Items, quotes != nil)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	quotes.apply(periodID, matches.Items, data)
	payments, byID, err := readVIPDraftFacts(ctx, tx, matches.Items)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	people := vipDraftPeople(matches.Items, data)
	organizations, err := loadReportAttribution(ctx, tx, people, nil)
	if err != nil {
		return paymentDraft{}, nil, err
	}
	// An amend to the order number can move a source version's primary key.
	// Keep digest order tied to the business identity, even on equal timestamps.
	slices.SortFunc(matches.Items, func(a, b catalogPaymentMatch) int {
		p, q := byID[a.SourceVersionID], byID[b.SourceVersionID]
		return cmp.Or(cmp.Compare(p.Source, q.Source), cmp.Compare(p.OrderID, q.OrderID), cmp.Compare(p.LineID, q.LineID), cmp.Compare(p.RecordKey, q.RecordKey))
	})
	var result paymentDraft
	for _, row := range matches.Items {
		if impact != nil && row.Status == "MATCHED" && row.RuleID == impact.ruleID {
			impact.payments++
		}
		if err := addVIPDraftPayment(&result, row, data, organizations); err != nil {
			return paymentDraft{}, nil, err
		}
	}
	if err := addRefunds(ctx, tx, periodID, window, nil, payments, &result, vipRefundTermsByRule(data.Definitions)); err != nil {
		return paymentDraft{}, nil, err
	}
	if err := addVIPRecipientCorrections(ctx, tx, periodID, window, &result); err != nil {
		return paymentDraft{}, nil, err
	}
	digest, err := digestDraft(0, result, payments)
	return result, digest, err
}

func readVIPDraftFacts(ctx context.Context, tx pgx.Tx, items []catalogPaymentMatch) ([]paymentInput, map[int64]sourceFact, error) {
	ids := make([]int64, len(items))
	for i, item := range items {
		ids[i] = item.SourceVersionID
	}
	rows, err := tx.Query(ctx, `SELECT id,source,order_id,line_id,record_key,coalesce(fact->>'paymentId',''),occurred_at,
	 coalesce(fact->>'domain',''),coalesce(fact->>'sourceKey','')
	 FROM source_order_versions WHERE id=ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, nil, err
	}
	inputs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (paymentInput, error) {
		var item paymentInput
		f := &item.Fact
		f.Kind = "ORDER_PAID"
		err := row.Scan(&item.ID, &f.Source, &f.OrderID, &f.LineID, &f.RecordKey, &f.PaymentID, &f.OccurredAt, &f.Domain, &f.SourceKey)
		return item, err
	})
	if err != nil {
		return nil, nil, err
	}
	index := make(map[int64]sourceFact, len(inputs))
	for _, input := range inputs {
		index[input.ID] = input.Fact
	}
	return inputs, index, nil
}

func vipDraftPeople(rows []catalogPaymentMatch, data vipRuntimeData) []int64 {
	ids := make(map[int64]bool)
	for _, row := range rows {
		if row.PaidMinor == nil || *row.PaidMinor <= 0 || excludedVIPPayment(row.Status) {
			continue
		}
		req, _ := vipPaymentInputs(row, data)
		ids[data.PersonIDs[req.Seller]] = true
		for _, sale := range row.SalesParts {
			ids[data.PersonIDs[sale.Person]] = true
		}
		for _, benefit := range row.BenefitParts {
			if benefit.Kind == "person" {
				ids[data.PersonIDs[benefit.Party]] = true
			}
		}
	}
	out := make([]int64, 0, len(ids))
	for id := range ids {
		if id != 0 {
			out = append(out, id)
		}
	}
	return out
}

func addVIPDraftPayment(result *paymentDraft, row catalogPaymentMatch, data vipRuntimeData, organizations reportAttribution) error {
	if row.Status == "COMPOUND_COMPLETION_PENDING" || row.Status == "COMPOUND_COMPLETED_IN_OTHER_PERIOD" {
		return nil
	}
	var missingSourceSeller, reportedSourceSeller bool
	membershipIssue := -1
	issue := func(component, code string) {
		if missingSourceSeller && code == "SALES_PERSON_MISSING" {
			if reportedSourceSeller {
				return
			}
			reportedSourceSeller = true
			component, code = "", "SALES_ORDER_SELLER_MISSING"
		}
		if code == "MEMBERSHIP_TYPE_PENDING" {
			if membershipIssue >= 0 {
				// One source field can hold up several independent arrangements.
				if previous := &result.Issues[membershipIssue]; previous.Component != component {
					previous.Component = ""
				}
				return
			}
			membershipIssue = len(result.Issues)
		}
		result.Issues = append(result.Issues, draftIssue{FactID: row.SourceVersionID, Component: component, Code: code})
	}
	if row.PaidMinor == nil || excludedVIPPayment(row.Status) || !row.PaidInWindow && row.Status != "MATCHED" {
		issue("", row.Status)
		return nil
	}
	if *row.PaidMinor == 0 {
		return nil
	}
	req, sourceIssue := vipPaymentInputs(row, data)
	missingSourceSeller = sourceIssue == "SALES_PERSON_MISSING"
	person := data.PersonIDs[req.Seller]
	org := organizations.resolve(person, row.PaidAt, identityKey{})
	if req.CompanyUnit != "" {
		var unit pgtype.UUID
		if err := unit.Scan(req.CompanyUnit); err != nil {
			return err
		}
		company := organizations.ancestors(unit).Company
		if company.Valid {
			org.Company = company
			if organizations.ancestors(org.Department).Company != company {
				org.Department = pgtype.UUID{}
			}
			if org.Group != nil && organizations.ancestors(*org.Group).Company != company {
				org.Group = nil
			}
		}
	}
	result.Performance = append(result.Performance, draftPerformance{SourceVersionID: row.SourceVersionID, Principal: row.commissionBasis(),
		PersonID: person, Organization: org, CompletionVersionID: row.CompletionVersionID, PriorRefundVersionID: row.PriorRefundVersionID})
	if row.Status != "MATCHED" {
		if row.Status != "MEMBERSHIP_RENEWAL_EXCLUDED" {
			issue("", row.Status)
		}
		return nil
	}
	if row.commissionBasis() == 0 {
		return nil
	}
	var rule pgtype.UUID
	if err := rule.Scan(row.RuleID); err != nil {
		return err
	}
	if row.SalesStatus == "" {
		issue("sales", "VIP_SALES_RULE_UNSUPPORTED")
	} else if row.SalesStatus != "CALCULATED" && row.SalesStatus != vipSalesAppointmentInactive &&
		row.SalesStatus != vipSalesNotConfigured && row.SalesStatus != vipCommissionOutsideScope {
		issue("sales", row.SalesStatus)
	}
	var salesRate int64
	for _, part := range row.SalesParts {
		salesRate += part.Rate
	}
	for _, part := range row.SalesParts {
		key := row.SalesItem
		if len(row.SalesParts) > 1 {
			key += ":" + part.Role
		}
		if part.Status != "" {
			if part.Status != vipCommissionOutsideScope {
				issue(key, part.Status)
			}
			continue
		}
		precise := preciseYuan(exactMinor(row.commissionBasis(), part.Rate, 10000))
		if err := appendVIPDraftLine(result, row, rule, key, part.Person, part.CompanyUnit, "", 0, part.Amount, salesRate, precise, data, organizations); err != nil {
			return err
		}
	}
	// A parsed sales-only rule returns an empty, non-nil benefits slice.
	if row.BenefitParts == nil {
		issue("", "BENEFIT_RULE_UNSUPPORTED")
	}
	for _, part := range row.BenefitParts {
		switch part.Status {
		case "CALCULATED":
			if err := appendVIPDraftLine(result, row, rule, part.Item, part.Party, part.Unit, part.Owner, catalogPoolID(part.Pool), part.Amount, part.Rate, part.preciseAmount, data, organizations); err != nil {
				return err
			}
		case "ZERO", "INACTIVE", vipCommissionOutsideScope:
		default:
			issue(part.Label, part.Status)
		}
	}
	return nil
}

func excludedVIPPayment(status string) bool {
	switch status {
	case "INVALID_PAYMENT", "DUPLICATE_PAYMENT", "PAYMENT_ALREADY_SETTLED", "COMPOUND_RECOGNITION_UNSUPPORTED",
		"COMPOUND_COMPLETION_PENDING", "COMPOUND_COMPLETED_IN_OTHER_PERIOD", "COMPOUND_PRIOR_REFUND_INVALID":
		return true
	default:
		return false
	}
}

func appendVIPDraftLine(result *paymentDraft, row catalogPaymentMatch, rule pgtype.UUID, key, party, unit, owner string, poolID, amount, rate int64,
	preciseAmount string, data vipRuntimeData, organizations reportAttribution) error {
	if amount == 0 {
		return nil
	}
	benefit := catalogDraftBenefit{Rule: rule, AllocationOwnerID: data.PersonIDs[owner], Rate: rate, PoolID: poolID}
	person := int64(0)
	var org reportOrganization
	if unit != "" {
		if err := benefit.Unit.Scan(unit); err != nil {
			return err
		}
		org = organizations.ancestors(benefit.Unit)
		if owner != "" && benefit.AllocationOwnerID == 0 {
			return conflict
		}
	} else {
		person = data.PersonIDs[party]
		if person == 0 {
			return conflict
		}
		org = organizations.resolve(person, row.PaidAt, identityKey{})
	}
	total, err := addMinor(result.Total, amount)
	if err != nil {
		return err
	}
	result.Total = total
	result.Lines = append(result.Lines, draftLine{FactID: row.SourceVersionID, PersonID: person, Component: key,
		Base: row.commissionBasis(), Amount: amount, PreciseAmount: preciseAmount, Catalog: &benefit, organization: org})
	return nil
}
