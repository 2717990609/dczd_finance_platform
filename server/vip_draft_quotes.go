package main

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"sync"
)

const (
	maxVIPCachedPeriods = 8
	maxVIPCachedRows    = 64 * 1024 // Counts payments and their sales and benefit rows together.
)

// A small cache for the order-independent part of trial calculation only.
// Each stored period is immutable and the cache is disposable on restart.
type vipDraftQuoteCache struct {
	mu      sync.Mutex
	periods list.List
	rows    int
}

type vipDraftQuotePeriod struct {
	id     int64
	scope  [sha256.Size]byte
	quotes map[int64]vipDraftQuote
	rows   int
}

type vipDraftQuoteInput struct {
	request     vipSalesRequest
	rule, issue string
	category    string
	hasCategory bool
	memberships [sha256.Size]byte
	definition  [sha256.Size]byte
}

type vipDraftQuote struct {
	input                                 vipDraftQuoteInput
	salesItem, salesStatus, benefitStatus string
	sales                                 []vipSalesPart
	benefits                              []vipBenefitPart
}

func (c *vipDraftQuoteCache) apply(periodID int64, items []catalogPaymentMatch, data vipRuntimeData) {
	if c == nil {
		applyVIPQuotes(items, data)
		return
	}
	scope, memberships, err := vipDraftQuoteContext(data)
	if err != nil {
		applyVIPQuotes(items, data)
		return
	}
	current := vipDraftQuotePeriod{id: periodID, scope: scope, quotes: make(map[int64]vipDraftQuote, min(len(items), 1024))}
	previous := c.read(periodID, scope)
	definitions := make(map[string][sha256.Size]byte, len(data.Definitions))
	for id, definition := range data.Definitions {
		definitions[id] = sha256.Sum256(definition)
	}
	var policies *vipQuotePolicies
	for i := range items {
		row := &items[i]
		if row.Status != "MATCHED" || row.PaidMinor == nil {
			continue
		}
		req, issue := vipPaymentInputs(*row, data)
		input := vipDraftQuoteInput{request: req, rule: row.RuleID, issue: issue,
			memberships: memberships[req.Seller], definition: definitions[row.RuleID]}
		input.request.At = req.At.UTC()
		// Seller memberships are hashed separately, including validity dates.
		input.request.commissionSalesOrg = nil
		if req.Category != nil {
			input.category, input.hasCategory = *req.Category, true
			input.request.Category = nil
		}
		quote, ok := previous[row.SourceVersionID]
		if !ok || quote.input != input {
			if policies == nil {
				parsed := readVIPQuotePolicies(data.Definitions)
				policies = &parsed
			}
			policies.apply(row, data, req, issue)
			quote = vipDraftQuote{input: input, salesItem: row.SalesItem, salesStatus: row.SalesStatus, benefitStatus: row.BenefitStatus,
				sales: row.SalesParts, benefits: row.BenefitParts}
		} else {
			row.SalesItem, row.SalesStatus, row.BenefitStatus = quote.salesItem, quote.salesStatus, quote.benefitStatus
			row.SalesParts, row.BenefitParts = quote.sales, quote.benefits
			if len(quote.benefits) != 0 {
				row.FixedParts = row.FixedParts[:0]
			}
		}
		current.add(row.SourceVersionID, quote)
	}
	c.remember(current)
}

func (p *vipDraftQuotePeriod) add(id int64, quote vipDraftQuote) {
	p.rows += 1 + len(quote.sales) + len(quote.benefits)
	if p.rows <= maxVIPCachedRows {
		p.quotes[id] = quote
	}
}

func vipDraftQuoteContext(data vipRuntimeData) ([sha256.Size]byte, map[string][sha256.Size]byte, error) {
	// SQL returns roles, organizations, and memberships in deterministic order.
	// Keep seller memberships separate so one affected seller need not invalidate the period.
	var commissionUnits map[string]reportUnit
	if data.CommissionDirectory != nil {
		commissionUnits = make(map[string]reportUnit, len(data.CommissionDirectory.Units))
		for id, unit := range data.CommissionDirectory.Units {
			commissionUnits[id.String()] = unit
		}
	}
	scope, err := json.Marshal(struct {
		Revision        int64
		Product         string
		Project         string
		BenefitReady    bool
		Directory       string
		Roles           []vipRole
		Organizations   []vipBenefitOrganization
		CommissionUnits map[string]reportUnit
	}{data.ConfigurationRevision, data.Product, data.Project, data.BenefitReady, data.DirectoryFingerprint, data.Roles, data.Organizations, commissionUnits})
	if err != nil {
		return [sha256.Size]byte{}, nil, err
	}
	bySeller := make(map[string][]vipMembership)
	for _, membership := range data.Memberships {
		bySeller[membership.Person] = append(bySeller[membership.Person], membership)
	}
	memberships := make(map[string][sha256.Size]byte, len(bySeller))
	for seller, rows := range bySeller {
		encoded, err := json.Marshal(rows)
		if err != nil {
			return [sha256.Size]byte{}, nil, err
		}
		memberships[seller] = sha256.Sum256(encoded)
	}
	return sha256.Sum256(scope), memberships, nil
}

func (c *vipDraftQuoteCache) read(id int64, scope [sha256.Size]byte) map[int64]vipDraftQuote {
	c.mu.Lock()
	defer c.mu.Unlock()
	for entry := c.periods.Front(); entry != nil; entry = entry.Next() {
		stored := entry.Value.(vipDraftQuotePeriod)
		if stored.id == id && stored.scope == scope {
			c.periods.MoveToFront(entry)
			return stored.quotes
		}
	}
	return nil
}

func (c *vipDraftQuoteCache) remember(period vipDraftQuotePeriod) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for entry := c.periods.Front(); entry != nil; entry = entry.Next() {
		if entry.Value.(vipDraftQuotePeriod).id == period.id {
			c.rows -= c.periods.Remove(entry).(vipDraftQuotePeriod).rows
			break
		}
	}
	if period.rows == 0 || period.rows > maxVIPCachedRows {
		return
	}
	for c.periods.Len() >= maxVIPCachedPeriods || c.rows+period.rows > maxVIPCachedRows {
		c.rows -= c.periods.Remove(c.periods.Back()).(vipDraftQuotePeriod).rows
	}
	c.periods.PushFront(period)
	c.rows += period.rows
}
