package main

import (
	"errors"
	"math"
	"time"
)

var errCalculationOverflow = errors.New("calculation exceeds the supported amount")

type sourceParticipant struct {
	Role   string `json:"role"`
	UserID string `json:"userId"`
}

type identityKey struct{ Source, ExternalID string }
type paymentKey struct{ Source, PaymentID string }

type identityAssignment struct {
	ID, PersonID int64
	ValidFrom    time.Time
	ValidTo      *time.Time
}

type paymentInput struct {
	ID   int64
	Fact sourceFact
}

type draftLine struct {
	FactID, PersonID, IdentityID int64
	Component                    string
	Base, Amount                 int64
}

type draftIssue struct {
	FactID    int64  `json:"factId,omitempty"`
	Component string `json:"component,omitempty"`
	Code      string `json:"code"`
}

type paymentDraft struct {
	Lines  []draftLine
	Issues []draftIssue
	Total  int64
}

func isPeriodPayment(window periodWindow, fact sourceFact) bool {
	return fact.Kind == "ORDER_PAID" && !fact.OccurredAt.Before(window.Start) && fact.OccurredAt.Before(window.Through)
}

func calculatePaymentDraft(window periodWindow, components []component, facts []paymentInput,
	identities map[identityKey][]identityAssignment) (paymentDraft, error) {
	var result paymentDraft
	counts := make(map[paymentKey]int, len(facts))
	for _, input := range facts {
		fact := input.Fact
		if isPeriodPayment(window, fact) && fact.PaymentID != "" {
			counts[paymentKey{fact.Source, fact.PaymentID}]++
		}
	}
	var applicable []component
	for _, c := range components {
		if (c.Type != "sales" && c.Type != "referral") || c.SourceRole == "" {
			result.Issues = append(result.Issues, draftIssue{Component: c.Type, Code: "UNCONFIGURED_COMPONENT"})
			continue
		}
		applicable = append(applicable, c)
	}
	for _, input := range facts {
		fact := input.Fact
		if !isPeriodPayment(window, fact) {
			continue
		}
		switch {
		case fact.PaymentID == "":
			result.Issues = append(result.Issues, draftIssue{FactID: input.ID, Code: "MISSING_PAYMENT_ID"})
			continue
		case counts[paymentKey{fact.Source, fact.PaymentID}] > 1:
			result.Issues = append(result.Issues, draftIssue{FactID: input.ID, Code: "DUPLICATE_PAYMENT"})
			continue
		case fact.AmountMinor == nil || *fact.AmountMinor < 0:
			result.Issues = append(result.Issues, draftIssue{FactID: input.ID, Code: "MISSING_PAID_AMOUNT"})
			continue
		case *fact.AmountMinor == 0:
			continue
		}
		for _, c := range applicable {
			assignment, issue := paymentBeneficiary(fact, c.SourceRole, identities)
			if issue != "" {
				result.Issues = append(result.Issues, draftIssue{FactID: input.ID, Component: c.Type, Code: issue})
				continue
			}
			amount := calculatePaymentAmount(*fact.AmountMinor, c)
			if amount > math.MaxInt64-result.Total {
				return paymentDraft{}, errCalculationOverflow
			}
			result.Total += amount
			result.Lines = append(result.Lines, draftLine{
				FactID: input.ID, PersonID: assignment.PersonID, IdentityID: assignment.ID, Component: c.Type,
				Base: *fact.AmountMinor, Amount: amount,
			})
		}
	}
	return result, nil
}

func paymentBeneficiary(fact sourceFact, role string, identities map[identityKey][]identityAssignment) (identityAssignment, string) {
	var externalID string
	for _, party := range fact.Participants {
		if party.Role != role || party.UserID == "" {
			continue
		}
		if externalID != "" && externalID != party.UserID {
			return identityAssignment{}, "AMBIGUOUS_PARTICIPANT"
		}
		externalID = party.UserID
	}
	if externalID == "" && role == "DIRECT_REFERRER" {
		externalID = fact.ReferrerID
	}
	if externalID == "" {
		return identityAssignment{}, "MISSING_PARTICIPANT"
	}
	var selected identityAssignment
	for _, candidate := range identities[identityKey{fact.Source, externalID}] {
		at := fact.OccurredAt
		if at.Before(candidate.ValidFrom) || candidate.ValidTo != nil && !at.Before(*candidate.ValidTo) {
			continue
		}
		if selected.PersonID != 0 {
			return identityAssignment{}, "AMBIGUOUS_IDENTITY"
		}
		selected = candidate
	}
	if selected.PersonID == 0 {
		return identityAssignment{}, "MISSING_IDENTITY"
	}
	return selected, ""
}

func calculatePaymentAmount(base int64, c component) int64 {
	if c.Mode == "fixed" {
		return c.Value
	}
	// Split the division to keep multiplication in range, including amounts close to MaxInt64.
	return (base/10000)*c.Value + ((base%10000)*c.Value+5000)/10000
}
