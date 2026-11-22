package main

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Orders, applied refunds, the roster and the rule are already referenced by
// the class draft. Keep only calculation inputs that those versions cannot
// supply when a later refund changes the outcome for the class.
type practiceReplayInputs struct {
	Version                 int                                  `json:"version"`
	IndependentRefunds      bool                                 `json:"independentRefunds,omitempty"`
	PriorCounts             map[int64]int64                      `json:"priorCounts,omitempty"`
	Identities              map[string][]identityAssignment      `json:"identities,omitempty"`
	RegistrationVersions    map[int64]int64                      `json:"registrationVersions,omitempty"`
	Appointments            map[int][]practiceAppointment        `json:"appointments,omitempty"`
	SalesOrganizations      practiceSalesOrganizations           `json:"salesOrganizations,omitempty"`
	Companies               map[string][]companySourceAssignment `json:"companies,omitempty"`
	CompanyClassifications  map[string]string                    `json:"companyClassifications,omitempty"`
	InternalSalesCategories map[string]string                    `json:"internalSalesCategories,omitempty"`
	ReferrerCompanies       map[string][]companySourceAssignment `json:"referrerCompanies,omitempty"`
	Recommendations         map[string]pgtype.UUID               `json:"recommendations,omitempty"`
	PaymentCompanies        map[string]practiceReplayCompany     `json:"paymentCompanies,omitempty"`
	PoolUnits               map[int64]pgtype.UUID                `json:"poolUnits,omitempty"`
}

const practiceReplayNetRateShares = 2
const practiceReplayIndependentBenefits = 3
const practiceReplayConfiguredBenefitRole = 4

const practiceRefundReplaySQL = `(d.practice_replay_inputs IS NOT NULL
 AND d.practice_replay_inputs->'independentRefunds' IS DISTINCT FROM 'true'::jsonb)`

func hasPracticeConfiguredPayees(rule paymentRule) bool {
	for _, part := range rule.Components {
		if part.CompanySales != nil || part.SalesPersonID != 0 {
			return true
		}
	}
	return false
}

type practiceReplayCompany struct {
	Sales    string `json:"sales,omitempty"`
	Referrer string `json:"referrer,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
}

func hasPracticeReplayDependencies(rule paymentRule, members []practiceEnrollment) bool {
	for _, part := range rule.Components {
		if part.Tiers != nil || part.PeriodBenefit != nil || len(part.salesCompanies()) != 0 || len(part.retrainSplits()) != 0 {
			return true
		}
	}
	for _, member := range members {
		if member.CommissionEligible && len(member.PaidOrderIDs) > 1 {
			return true
		}
	}
	return false
}

func capturePracticeReplayInputs(rule paymentRule, draft paymentDraft, facts []paymentInput,
	identities map[identityKey][]identityAssignment, runtime practiceDraftRuntime) *practiceReplayInputs {
	inputs := &practiceReplayInputs{
		Version: practiceReplayNetRateShares, PriorCounts: draft.practicePriorCounts,
		Identities: make(map[string][]identityAssignment), Appointments: make(map[int][]practiceAppointment),
		PaymentCompanies: make(map[string]practiceReplayCompany), Recommendations: make(map[string]pgtype.UUID),
		Companies: runtime.companyRecipients, ReferrerCompanies: runtime.recommendations.explicit, PoolUnits: runtime.poolUnits,
	}
	if len(runtime.salesOrganizations) != 0 {
		inputs.SalesOrganizations = make(practiceSalesOrganizations)
	}
	if len(runtime.registrations) != 0 {
		inputs.RegistrationVersions = make(map[int64]int64, len(runtime.registrations))
		for enrollment, input := range runtime.registrations {
			if input.Issue == "" {
				inputs.RegistrationVersions[enrollment] = input.VersionID
			}
		}
	}
	if practiceTierUsesCategories(rule) || runtime.companyOffers.needsClassifications() {
		inputs.CompanyClassifications = make(map[string]string)
	}
	if len(runtime.internalSalesCategories) != 0 {
		inputs.InternalSalesCategories = make(map[string]string)
	}
	current := make(map[int64]bool, len(draft.Performance))
	for _, posting := range draft.Performance {
		if posting.PaymentVersionID == 0 {
			current[posting.SourceVersionID] = true
		}
	}
	for _, payment := range facts {
		if !current[payment.ID] {
			continue
		}
		f := payment.Fact
		if category := runtime.companyClassifications[f.PaymentID]; category != "" && inputs.CompanyClassifications != nil {
			inputs.CompanyClassifications[f.PaymentID] = category
		}
		if category := runtime.internalSalesCategories[f.PaymentID]; category != "" {
			inputs.InternalSalesCategories[f.PaymentID] = category
		}
		if captured := runtime.salesOrganizations[payment.ID]; len(captured) != 0 {
			inputs.SalesOrganizations[payment.ID] = captured
		}
		if company := (practiceReplayCompany{f.SalesCompanyID, f.ReferrerCompanyID, f.ReferrerConflict}); company != (practiceReplayCompany{}) {
			inputs.PaymentCompanies[f.OrderID] = company
		}
		capture := func(user string) {
			if known, ok := identities[identityKey{f.Source, user}]; ok {
				inputs.Identities[user] = known
			}
		}
		capture(f.ReferrerID)
		for _, participant := range f.Participants {
			capture(participant.UserID)
		}
	}
	for index, part := range rule.Components {
		if benefit := part.PeriodBenefit; benefit != nil {
			if benefit.RegistrationScope == nil && !benefit.hasSalesScope() {
				inputs.Version = max(inputs.Version, practiceReplayIndependentBenefits)
			}
			if len(benefit.SalesPeople)+len(benefit.SalesUnits) != 0 {
				inputs.Version = practiceReplayConfiguredBenefitRole
			}
			if assignments := runtime.appointments[benefit.appointmentKey()]; len(assignments) != 0 {
				inputs.Appointments[index] = assignments
			}
		}
	}
	for company, parent := range runtime.recommendations.directory {
		inputs.Recommendations[company.String()] = parent
	}
	return inputs
}

type practiceReplay struct {
	independent        bool
	independentRefunds bool
	rule               paymentRule
	members            []practiceEnrollment
	payments           map[string][]paymentInput
	refunds            map[int64][]practiceRefundMatch
	lateRefunded       map[int64]int64
	identities         map[identityKey][]identityAssignment
	history            practiceTierHistory
	runtime            practiceDraftRuntime
	posted             map[int64]practiceReplayPosting
}

type practiceReplayPosting struct {
	paymentID, personID, principal int64
}

func readPracticeReplay(ctx context.Context, tx pgx.Tx, periodID int64) (practiceReplay, error) {
	var replay practiceReplay
	var saved *practiceReplayInputs
	var rosterID int64
	err := tx.QueryRow(ctx, `SELECT d.practice_roster_id,r.id,r.components,d.practice_replay_inputs
	 FROM calculation_drafts d JOIN rule_versions r ON r.id=d.rule_id JOIN settlements s ON s.period_id=d.period_id
	 WHERE d.period_id=$1 AND d.practice_roster_id IS NOT NULL`, periodID).
		Scan(&rosterID, &replay.rule.ID, &replay.rule.Components, &saved)
	if err != nil {
		return replay, err
	}
	if saved != nil && (saved.Version < 1 || saved.Version > practiceReplayConfiguredBenefitRole) {
		return replay, errPracticeRefundReplay
	}
	replay.members, err = readPracticeMembers(ctx, tx, []int64{rosterID}, false)
	if err != nil {
		return replay, err
	}
	var values practiceReplayInputs
	if saved != nil {
		values = *saved
		replay.independentRefunds = saved.IndependentRefunds
	} else {
		if hasPracticeReplayDependencies(replay.rule, replay.members) {
			return replay, errPracticeRefundReplay
		}
		for _, part := range replay.rule.Components {
			if (part.Type != "sales" && part.Type != "referral") || (part.Mode != "rate" && part.Mode != "fixed") {
				return replay, errPracticeRefundReplay
			}
		}
		replay.independent = true
	}
	replay.payments = make(map[string][]paymentInput)
	replay.refunds = make(map[int64][]practiceRefundMatch)
	replay.lateRefunded = make(map[int64]int64)
	replay.posted = make(map[int64]practiceReplayPosting)
	rows, err := tx.Query(ctx, `SELECT v.id,v.fact,coalesce(p.payment_version_id,0),coalesce(p.person_id,0),
	 coalesce(c.company_id,''),coalesce(c.recommendation_company_id,''),coalesce(c.recommendation_conflict,false)
	 FROM calculation_draft_performance p JOIN source_order_versions v ON v.id=p.source_version_id
	 LEFT JOIN source_payment_companies c ON c.source_version_id=v.id AND $2
	 WHERE p.period_id=$1 ORDER BY v.occurred_at,v.id`, periodID, replay.independent)
	if err != nil {
		return replay, err
	}
	var original, person int64
	var item paymentInput
	var company practiceReplayCompany
	_, err = pgx.ForEachRow(rows, []any{&item.ID, &item.Fact, &original, &person, &company.Sales, &company.Referrer, &company.Conflict}, func() error {
		if item.Fact.AmountMinor == nil || *item.Fact.AmountMinor <= 0 {
			return errPracticeRefundReplay
		}
		replay.posted[item.ID] = practiceReplayPosting{original, person, *item.Fact.AmountMinor}
		if original != 0 {
			replay.refunds[original] = append(replay.refunds[original], practiceRefundMatch{event: item})
		} else {
			f := &item.Fact
			assignment := company
			if !replay.independent {
				assignment = values.PaymentCompanies[f.OrderID]
			}
			f.SalesCompanyID, f.ReferrerCompanyID, f.ReferrerConflict = assignment.Sales, assignment.Referrer, assignment.Conflict
			replay.payments[f.OrderID] = append(replay.payments[f.OrderID], item)
		}
		return nil
	})
	if err != nil {
		return replay, err
	}
	replay.identities = make(map[identityKey][]identityAssignment, len(values.Identities))
	for user, assignments := range values.Identities {
		replay.identities[identityKey{"DCZD", user}] = assignments
	}
	if replay.independent {
		if err := replay.loadIndependentIdentities(ctx, tx, periodID); err != nil {
			return replay, err
		}
	}
	replay.history = practiceTierHistory{replaying: true, frozenPrior: values.PriorCounts}
	replay.runtime = practiceDraftRuntime{
		appointments: make(map[practiceAppointmentKey][]practiceAppointment), companyOffers: practiceCompanyStandards(replay.rule),
		salesOrganizations:     values.SalesOrganizations,
		companyClassifications: values.CompanyClassifications, internalSalesCategories: values.InternalSalesCategories,
		companyRecipients: values.Companies, poolUnits: values.PoolUnits,
		recommendations: practiceCompanyRecommendations{
			directory: make(map[pgtype.UUID]pgtype.UUID), explicit: values.ReferrerCompanies,
		},
		legacyGrossRateShares:       values.Version == 1,
		legacyBenefitSalesAgreement: values.Version < practiceReplayIndependentBenefits,
		legacyBenefitAttribution:    values.Version < practiceReplayConfiguredBenefitRole,
	}
	if usesPracticeRegistrations(replay.rule) {
		replay.runtime.registrations, err = readPracticeRegistrationInputs(ctx, tx, rosterID, false, values.RegistrationVersions)
		if err != nil {
			return replay, err
		}
		if len(replay.runtime.registrations) != len(values.RegistrationVersions) {
			return replay, errPracticeRefundReplay
		}
	}
	for index, assignments := range values.Appointments {
		if index < 0 || index >= len(replay.rule.Components) || replay.rule.Components[index].PeriodBenefit == nil {
			return replay, errPracticeRefundReplay
		}
		replay.runtime.appointments[replay.rule.Components[index].PeriodBenefit.appointmentKey()] = assignments
	}
	for company, parent := range values.Recommendations {
		var id pgtype.UUID
		if err := id.Scan(company); err != nil {
			return replay, errPracticeRefundReplay
		}
		replay.runtime.recommendations.directory[id] = parent
	}
	return replay, nil
}

func (r practiceReplay) loadIndependentIdentities(ctx context.Context, tx pgx.Tx, periodID int64) error {
	// Each original line pins the mapping used for its payment. Its current
	// validity end may have been filled in after the class was confirmed.
	rows, err := tx.Query(ctx, `SELECT DISTINCT i.external_id,i.id,l.person_id,v.occurred_at,v.occurred_at+interval '1 microsecond'
	 FROM calculation_draft_lines l JOIN identity_mappings i ON i.id=l.identity_id
	 JOIN source_order_versions v ON v.id=l.source_version_id WHERE l.period_id=$1`, periodID)
	if err != nil {
		return err
	}
	var external string
	var assignment identityAssignment
	_, err = pgx.ForEachRow(rows, []any{&external, &assignment.ID, &assignment.PersonID, &assignment.ValidFrom, &assignment.ValidTo}, func() error {
		key := identityKey{"DCZD", external}
		r.identities[key] = append(r.identities[key], assignment)
		return nil
	})
	return err
}

func (r practiceReplay) calculate() (paymentDraft, error) {
	result, err := calculatePracticeDraft(r.members, r.rule, r.payments, r.identities, nil, r.history, r.refunds, r.runtime)
	if err == nil && len(result.Issues) != 0 {
		err = errors.Join(errPracticeRefundReplay, errors.New(result.Issues[0].Code))
	}
	return result, err
}
