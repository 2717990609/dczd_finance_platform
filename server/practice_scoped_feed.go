package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const practiceScopedFactsContract = "dczd.finance-practice-facts/1.0"
const practiceRefundsContract = "dczd.finance-practice-refunds/1.0"

type practiceFactSelection struct {
	Kind         string `json:"kind"`
	ID           int64  `json:"id"`
	KnownVersion string `json:"known_version,omitempty"`
}

func (s practiceFactSelection) recordKey() string {
	table := "dc_practice_total_order_t"
	if s.Kind == "DELIVERY_COMPLETED" {
		table = "dc_offline_order_log_t"
	}
	return table + ":" + strconv.FormatInt(s.ID, 10)
}

func (c orderFeedClient) practiceScopedFacts(ctx context.Context, selections []practiceFactSelection) ([]sourceFact, []practiceFactSelection, error) {
	if !c.practiceOnly || len(selections) == 0 || len(selections) > orderPageSize {
		return nil, nil, errSourceRead
	}
	wanted := make(map[string]practiceFactSelection, len(selections))
	for _, selection := range selections {
		if selection.ID <= 0 || (selection.Kind != "ORDER_PAID" && selection.Kind != "DELIVERY_COMPLETED") || wanted[selection.recordKey()].ID != 0 {
			return nil, nil, errSourceRead
		}
		wanted[selection.recordKey()] = selection
	}
	payload, err := json.Marshal(selections)
	if err != nil {
		return nil, nil, err
	}
	var body struct {
		Contract string                  `json:"contract_version"`
		Schema   string                  `json:"source_fact_schema_version"`
		Facts    []json.RawMessage       `json:"facts"`
		Missing  []practiceFactSelection `json:"missing"`
	}
	query := url.Values{}
	if canRequestOrderDisplay(c.endpoint) {
		query.Set("include", orderDisplayInclude)
	}
	err = c.requestWithContract(ctx, http.MethodPost, "practice-facts", practiceScopedFactsContract, query, payload, &body)
	if err != nil || body.Contract != practiceScopedFactsContract || body.Schema != sourceSchema || body.Facts == nil || body.Missing == nil {
		return nil, nil, errSourceRead
	}
	facts := make([]sourceFact, 0, len(body.Facts))
	for _, raw := range body.Facts {
		fact, err := practiceScopedFact(raw)
		selected := wanted[fact.RecordKey]
		if err != nil || selected.ID == 0 || selected.Kind != fact.Kind || selected.KnownVersion == fact.Version {
			return nil, nil, errSourceRead
		}
		delete(wanted, fact.RecordKey)
		facts = append(facts, fact)
	}
	for _, missing := range body.Missing {
		selected := wanted[missing.recordKey()]
		if selected.ID == 0 || selected.Kind != missing.Kind {
			return nil, nil, errSourceRead
		}
		delete(wanted, missing.recordKey())
	}
	for _, omitted := range wanted {
		if omitted.KnownVersion == "" {
			return nil, nil, errSourceRead
		}
	}
	return facts, body.Missing, nil
}

type practiceRefundCursor struct {
	SuccessAt time.Time `json:"success_at"`
	ID        string    `json:"id"`
	Code      string    `json:"code"`
}

func (c practiceRefundCursor) valid() bool {
	return (orderCursor{UpdatedAt: c.SuccessAt, ID: c.ID}).valid() &&
		(c.ID == "0" && c.Code == "" || c.ID != "0" && textOK(c.Code, 128))
}

func (c practiceRefundCursor) equal(other practiceRefundCursor) bool {
	return c.SuccessAt.Equal(other.SuccessAt) && c.ID == other.ID && c.Code == other.Code
}

func (c practiceRefundCursor) follows(other practiceRefundCursor) bool {
	if c.SuccessAt.Equal(other.SuccessAt) && c.ID == other.ID {
		return c.Code != other.Code
	}
	return !(orderCursor{UpdatedAt: c.SuccessAt, ID: c.ID}).atOrBefore(orderCursor{UpdatedAt: other.SuccessAt, ID: other.ID})
}

type practiceRefundPage struct {
	Through time.Time
	Next    practiceRefundCursor
	More    bool
	Facts   []sourceFact
}

func (c orderFeedClient) practiceRefunds(ctx context.Context, after practiceRefundCursor, through *time.Time, limit int) (practiceRefundPage, error) {
	if !c.practiceOnly || !after.valid() || limit < 1 || limit > orderPageSize {
		return practiceRefundPage{}, errSourceRead
	}
	query := url.Values{"after_success_at": {after.SuccessAt.UTC().Format(time.RFC3339Nano)}, "after_id": {after.ID}, "limit": {strconv.Itoa(limit)}}
	if after.Code != "" {
		query.Set("after_code", after.Code)
	}
	if through != nil {
		query.Set("through", through.UTC().Format(time.RFC3339Nano))
	}
	var body struct {
		Contract string               `json:"contract_version"`
		Schema   string               `json:"source_fact_schema_version"`
		Through  time.Time            `json:"through"`
		Next     practiceRefundCursor `json:"next_cursor"`
		More     *bool                `json:"has_more"`
		Items    []struct {
			Cursor practiceRefundCursor `json:"cursor"`
			Fact   json.RawMessage      `json:"fact"`
		} `json:"items"`
	}
	err := c.getWithContract(ctx, "practice-refunds", practiceRefundsContract, query, &body)
	if err != nil || body.Contract != practiceRefundsContract || body.Schema != sourceSchema || !validOrderWatermark(body.Through) ||
		body.Through.Before(after.SuccessAt) || through != nil && !through.Equal(body.Through) || body.More == nil ||
		body.Items == nil || len(body.Items) > limit || *body.More && len(body.Items) == 0 {
		return practiceRefundPage{}, errSourceRead
	}
	page := practiceRefundPage{Through: body.Through, Next: after, More: *body.More}
	seen := make(map[string]struct{}, len(body.Items))
	for _, item := range body.Items {
		fact, err := practiceScopedFact(item.Fact)
		if _, duplicate := seen[item.Cursor.ID]; err != nil || duplicate || !item.Cursor.valid() || item.Cursor.ID == "0" || !item.Cursor.follows(page.Next) ||
			item.Cursor.SuccessAt.After(page.Through) || fact.Kind != "REFUND_SUCCEEDED" || fact.RecordKey != "dc_payrefund_t:"+item.Cursor.ID ||
			fact.AmountMinor == nil || *fact.AmountMinor <= 0 || len(fact.RefundsPayment) != 1 {
			return practiceRefundPage{}, errSourceRead
		}
		seen[item.Cursor.ID] = struct{}{}
		page.Facts = append(page.Facts, fact)
		page.Next = item.Cursor
	}
	if !body.Next.equal(page.Next) {
		return practiceRefundPage{}, errSourceRead
	}
	return page, nil
}

func practiceScopedFact(raw json.RawMessage) (sourceFact, error) {
	var doc sourceDocument
	if json.Unmarshal(raw, &doc) != nil || positiveSourceID(doc.Order.ID) == 0 || !isEnrollmentPracticeFact(doc) {
		return sourceFact{}, errSourceRead
	}
	return normalizeFact("DCZD", doc.Order.ID, raw)
}
