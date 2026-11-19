package main

import (
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
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

type paymentRule struct {
	ID         int64
	Version    int
	From       *time.Time
	Components []component
}

type draftLine struct {
	FactID, PersonID, IdentityID, RuleID int64
	EnrollmentID                         int64 `json:",omitempty"`
	Component                            string
	organization                         reportOrganization
	reportUnit                           pgtype.UUID
	Base, Amount                         int64
	Catalog                              *catalogDraftBenefit `json:",omitempty"`
	BeneficiaryUnit                      *pgtype.UUID         `json:",omitempty"`
	PracticePool                         *practicePoolTarget  `json:",omitempty"`
}

type draftIssue struct {
	FactID           int64                      `json:"factId,omitempty"`
	RuleID           int64                      `json:"ruleId,omitempty"`
	Component        string                     `json:"component,omitempty"`
	Code             string                     `json:"code"`
	EnrollmentID     int64                      `json:"enrollmentId,omitempty"`
	Advisory         bool                       `json:"advisory,omitempty"`
	Recovery         *pendingCorrectionRecovery `json:"recovery,omitempty"`
	CorrectionLineID int64                      `json:"correctionLineId,omitempty"`
	stableKey        string
}

type paymentDraft struct {
	PracticeStartsAt    time.Time
	PracticeEnrollments []int64
	practicePriorCounts map[int64]int64
	practiceReplay      *practiceReplayInputs
	Lines               []draftLine
	Performance         []draftPerformance
	Issues              []draftIssue
	Refunds             []refundLine
	Corrections         []correctionLine
	Total               int64
	RefundTotal         int64
	CorrectionTotal     int64
}

func (d paymentDraft) blockingIssues() int {
	count := 0
	for _, issue := range d.Issues {
		if !issue.Advisory {
			count++
		}
	}
	return count
}

func isPeriodPayment(window periodWindow, fact sourceFact) bool {
	return fact.Kind == "ORDER_PAID" && !fact.OccurredAt.Before(window.Start) && fact.OccurredAt.Before(window.Through)
}

func calculatePaymentDraft(window periodWindow, rules []paymentRule, facts []paymentInput,
	identities map[identityKey][]identityAssignment) (paymentDraft, error) {
	var result paymentDraft
	counts := make(map[paymentKey]int, len(facts))
	for _, input := range facts {
		fact := input.Fact
		if isPeriodPayment(window, fact) && fact.PaymentID != "" {
			counts[paymentKey{fact.Source, fact.PaymentID}]++
		}
	}
	type selectedComponents struct {
		components []component
		issues     []draftIssue
	}
	byRule := make(map[int64]selectedComponents, len(rules))
	for _, rule := range rules {
		var policy selectedComponents
		for _, c := range rule.Components {
			if (c.Type != "sales" && c.Type != "referral") || c.SourceRole == "" {
				policy.issues = append(policy.issues, draftIssue{RuleID: rule.ID, Component: c.Type, Code: "UNCONFIGURED_COMPONENT"})
				continue
			}
			policy.components = append(policy.components, c)
		}
		byRule[rule.ID] = policy
	}
	reportedRules := make(map[int64]bool)
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
		rule := selectPaymentRule(rules, fact.OccurredAt)
		result.Performance = append(result.Performance, draftPerformance{
			SourceVersionID: input.ID, Principal: *fact.AmountMinor,
			PersonID: performanceOwner(fact, rule, "", identities),
		})
		if rule == nil {
			result.Issues = append(result.Issues, draftIssue{FactID: input.ID, Code: "MISSING_EFFECTIVE_RULE"})
			continue
		}
		policy := byRule[rule.ID]
		if !reportedRules[rule.ID] {
			result.Issues = append(result.Issues, policy.issues...)
			reportedRules[rule.ID] = true
		}
		for _, c := range policy.components {
			assignment, issue := paymentBeneficiary(fact, c.SourceRole, identities)
			if issue != "" {
				result.Issues = append(result.Issues, draftIssue{FactID: input.ID, RuleID: rule.ID, Component: c.Type, Code: issue})
				continue
			}
			amount := calculatePaymentAmount(*fact.AmountMinor, c)
			if amount > math.MaxInt64-result.Total {
				return paymentDraft{}, errCalculationOverflow
			}
			result.Total += amount
			result.Lines = append(result.Lines, draftLine{
				FactID: input.ID, PersonID: assignment.PersonID, IdentityID: assignment.ID, RuleID: rule.ID, Component: c.Type,
				Base: *fact.AmountMinor, Amount: amount,
			})
		}
	}
	if len(reportedRules) == 0 && len(rules) != 0 {
		result.Issues = append(result.Issues, byRule[rules[0].ID].issues...)
	}
	return result, nil
}

// Rules arrive in scope priority order, then in descending effective time.
func selectPaymentRule(rules []paymentRule, at time.Time) *paymentRule {
	for i := range rules {
		if rules[i].From == nil || !at.Before(*rules[i].From) {
			return &rules[i]
		}
	}
	return nil
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
