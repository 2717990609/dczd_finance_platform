package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	dcaiOrderPageContract = "dcai.finance-order-pull-page/1.0"
	dcaiFactSchemaHash    = "d4981ce54a1018f5e6bd39c80e64fafdff6b0d58187120c8b05df298c8966c09"
	dcaiPageSchemaHash    = "5dfe80ac6c41077fa7912c38eb9892d542b1627b4e57f4aae4fdbbfec2752277"
	dcaiVIPStream         = "VIP_ORDERS"
	dcaiHealthStream      = "HEALTH_IDENTITY_ORDERS"
	dczdOrderStream       = "ORDER_FACTS"
	dczdPracticeStream    = "PRACTICE_ORDER_FACTS"
	dczdOrderPageContract = "dczd.finance-order-pull-page/1.0"
	orderPageSize         = 200
)

type orderCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        string    `json:"id"`
}

func (c orderCursor) atOrBefore(other orderCursor) bool {
	if c.UpdatedAt.Equal(other.UpdatedAt) {
		// Canonical unsigned decimal strings compare numerically by length first.
		return len(c.ID) < len(other.ID) || len(c.ID) == len(other.ID) && c.ID <= other.ID
	}
	return c.UpdatedAt.Before(other.UpdatedAt)
}

func (c orderCursor) valid() bool {
	number, err := strconv.ParseUint(c.ID, 10, 64)
	return err == nil && c.ID == strconv.FormatUint(number, 10) && validOrderWatermark(c.UpdatedAt)
}

func validOrderWatermark(value time.Time) bool {
	return !value.IsZero() && value.Equal(value.Truncate(time.Microsecond))
}

type dcaiGate struct {
	Code        string    `json:"code"`
	Status      string    `json:"status"`
	Reason      *string   `json:"reason_code"`
	OrderID     string    `json:"source_order_id"`
	EffectiveAt time.Time `json:"effective_at"`
}

type orderPageRow struct {
	Cursor                 orderCursor
	OrderID                string
	Facts                  []sourceFact
	Blockers               []string
	healthContractObserved bool
	healthCompletion       *sourceFact
}

type orderPage struct {
	Rows    []orderPageRow
	Next    orderCursor
	HasMore bool
}

type orderFeedClient struct {
	endpoint     *url.URL
	token        string
	source       string
	practiceOnly bool
}

type orderFeedHTTPStatus int

func (status orderFeedHTTPStatus) Error() string {
	return fmt.Sprintf("source request failed (HTTP %d)", status)
}

func (orderFeedHTTPStatus) Unwrap() error { return errSourceRead }

func newOrderFeedClient(source string, cfg sourceConfig) (orderFeedClient, error) {
	if cfg.BaseURL == "" || cfg.Token == "" {
		return orderFeedClient{}, errSourceDisabled
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return orderFeedClient{}, errSourceRead
	}
	return orderFeedClient{endpoint: base.JoinPath("/internal/v1/finance/source-orders"), token: cfg.Token, source: source, practiceOnly: source == "DCZD" && cfg.PracticeOnly}, nil
}

func (c orderFeedClient) contract() string {
	if c.source == "DCZD" {
		return dczdOrderPageContract
	}
	return dcaiOrderPageContract
}

func (c orderFeedClient) sourceKey() string {
	key := c.endpoint.String()
	if c.practiceOnly {
		key += "#" + dczdPracticeStream
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
}

func (c orderFeedClient) remoteStream(stream string) string {
	if c.practiceOnly {
		return dczdPracticeStream
	}
	return stream
}

func (c orderFeedClient) get(ctx context.Context, action string, query url.Values, result any) error {
	contract := c.contract()
	if action == "readiness" {
		contract = ""
	}
	return c.getWithContract(ctx, action, contract, query, result)
}

func (c orderFeedClient) getWithContract(ctx context.Context, action, contract string, query url.Values, result any) error {
	endpoint := *c.endpoint
	if action != "" {
		endpoint = *endpoint.JoinPath(action)
	}
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return errSourceRead
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if contract != "" {
		req.Header.Set("X-"+c.source+"-Finance-Contract", contract)
	}
	if contract == practiceRosterReadContract || contract == practicePeriodReadContract || contract == practiceRegistrationsContract {
		req.Header.Set("Cache-Control", "no-cache")
	}
	res, err := pullClient.Do(req)
	if err != nil {
		return errSourceRead
	}
	defer res.Body.Close()
	const limit = 8 << 20
	if res.StatusCode != http.StatusOK {
		return orderFeedHTTPStatus(res.StatusCode)
	}
	wire, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(wire) > limit || json.Unmarshal(wire, result) != nil {
		return errSourceRead
	}
	return nil
}

type orderFeedCapabilities struct {
	PageSize              int
	PracticeRegistrations bool
}

func (c orderFeedClient) readiness(ctx context.Context, stream string) (int, error) {
	capabilities, err := c.capabilities(ctx, stream)
	return capabilities.PageSize, err
}

func (c orderFeedClient) capabilities(ctx context.Context, stream string) (orderFeedCapabilities, error) {
	var body struct {
		Status                string            `json:"status"`
		Version               string            `json:"contract_version"`
		Schema                string            `json:"source_fact_schema_version"`
		FactHash              string            `json:"source_fact_schema_sha256"`
		PageHash              string            `json:"pull_page_schema_sha256"`
		Streams               map[string]string `json:"streams"`
		PageSize              int               `json:"max_page_size"`
		Conflicts             *int64            `json:"conflict_count"`
		PracticeConflicts     *int64            `json:"practice_conflict_count"`
		PracticeRegistrations string            `json:"practice_registrations_contract"`
	}
	if err := c.get(ctx, "readiness", nil, &body); err != nil {
		return orderFeedCapabilities{}, err
	}
	if (!c.practiceOnly && body.Status != "READY") || body.Streams[c.remoteStream(stream)] != "READY" ||
		body.Version != c.contract() || body.Schema != sourceSchema {
		return orderFeedCapabilities{}, errSourceRead
	}
	if c.source == "DCZD" {
		count := body.Conflicts
		if c.practiceOnly {
			count = body.PracticeConflicts
		}
		if body.PageSize < 1 || count == nil || *count != 0 {
			return orderFeedCapabilities{}, errSourceRead
		}
		return orderFeedCapabilities{PageSize: min(orderPageSize, body.PageSize),
			PracticeRegistrations: body.PracticeRegistrations == practiceRegistrationsContract}, nil
	}
	if body.FactHash != dcaiFactSchemaHash || body.PageHash != dcaiPageSchemaHash {
		return orderFeedCapabilities{}, errSourceRead
	}
	return orderFeedCapabilities{PageSize: orderPageSize}, nil
}

type orderFeedCutoff struct {
	Version       string    `json:"contract_version"`
	Through       time.Time `json:"through"`
	SourceThrough time.Time `json:"source_through"`
}

func (c orderFeedClient) cutoff(ctx context.Context) (orderFeedCutoff, error) {
	var body orderFeedCutoff
	var query url.Values
	if c.practiceOnly {
		query = url.Values{"stream": {dczdPracticeStream}}
	}
	err := c.get(ctx, "cutoff", query, &body)
	if err != nil || body.Version != c.contract() || !validOrderWatermark(body.Through) {
		return orderFeedCutoff{}, errSourceRead
	}
	if c.practiceOnly {
		if !validOrderWatermark(body.SourceThrough) || body.SourceThrough.After(body.Through) {
			return orderFeedCutoff{}, errSourceRead
		}
	} else {
		body.SourceThrough = body.Through
	}
	return body, nil
}

type orderWireRow struct {
	Cursor     orderCursor       `json:"cursor"`
	Facts      []json.RawMessage `json:"facts"`
	Gates      []dcaiGate        `json:"gates"`
	Extensions struct {
		Contacts map[string]json.RawMessage `json:"finance_order_display"`
	} `json:"extensions"`
}

func (c orderFeedClient) page(ctx context.Context, stream string, after orderCursor, through time.Time, limit int) (orderPage, error) {
	query := url.Values{
		"stream": {c.remoteStream(stream)}, "after_updated_at": {after.UpdatedAt.UTC().Format(time.RFC3339Nano)},
		"after_id": {after.ID}, "through": {through.UTC().Format(time.RFC3339Nano)}, "limit": {strconv.Itoa(limit)},
	}
	if canRequestOrderDisplay(c.endpoint) && (c.source == "DCAI" || c.practiceOnly) {
		query.Set("include", orderDisplayInclude)
	}
	var body struct {
		Version string         `json:"contract_version"`
		Schema  string         `json:"source_fact_schema_version"`
		Stream  string         `json:"stream"`
		Through time.Time      `json:"through"`
		Rows    []orderWireRow `json:"items"`
		Next    orderCursor    `json:"next_cursor"`
		HasMore *bool          `json:"has_more"`
	}
	err := c.get(ctx, "", query, &body)
	if err != nil || body.Version != c.contract() || body.Schema != sourceSchema || body.Stream != c.remoteStream(stream) ||
		!body.Through.Equal(through) || body.Rows == nil || body.HasMore == nil || len(body.Rows) > limit ||
		*body.HasMore && len(body.Rows) == 0 {
		return orderPage{}, errSourceRead
	}
	page := orderPage{Next: after, HasMore: *body.HasMore}
	for _, wire := range body.Rows {
		if !wire.Cursor.valid() || wire.Cursor.ID == "0" || wire.Cursor.atOrBefore(page.Next) || wire.Cursor.UpdatedAt.After(through) {
			return orderPage{}, errSourceRead
		}
		row, err := c.normalizeRow(stream, through, wire)
		if err != nil {
			return orderPage{}, err
		}
		page.Rows = append(page.Rows, row)
		page.Next = wire.Cursor
	}
	if body.Next.ID != page.Next.ID || !body.Next.UpdatedAt.Equal(page.Next.UpdatedAt) {
		return orderPage{}, errSourceRead
	}
	return page, nil
}

func (c orderFeedClient) normalizeRow(stream string, through time.Time, wire orderWireRow) (orderPageRow, error) {
	if c.source == "DCAI" {
		return normalizeDCAIOrderRow(stream, through, wire)
	}
	if positiveSourceID(wire.Cursor.ID) == 0 || len(wire.Facts) != 1 || wire.Gates == nil || len(wire.Gates) != 0 {
		return orderPageRow{}, errSourceRead
	}
	var doc sourceDocument
	if json.Unmarshal(wire.Facts[0], &doc) != nil || !textOK(doc.Order.ID, 128) || strings.TrimSpace(doc.Order.ID) != doc.Order.ID {
		return orderPageRow{}, errSourceRead
	}
	if c.practiceOnly && !isEnrollmentPracticeFact(doc) {
		return orderPageRow{}, errSourceRead
	}
	fact, err := normalizeFact(c.source, doc.Order.ID, wire.Facts[0])
	if err != nil {
		return orderPageRow{}, err
	}
	// The source paginates on its materialized row's creation time. The business
	// event and source record version retain their independent timestamps.
	return orderPageRow{Cursor: wire.Cursor, OrderID: fact.OrderID, Facts: []sourceFact{fact}}, nil
}

func normalizeDCAIOrderRow(stream string, through time.Time, wire orderWireRow) (orderPageRow, error) {
	row := orderPageRow{Cursor: wire.Cursor, Blockers: []string{}}
	if wire.Facts == nil || wire.Gates == nil || len(wire.Facts) > 3 || len(wire.Gates) > 2 {
		return row, errSourceRead
	}
	domain := "VIP"
	if stream == dcaiHealthStream {
		domain = "HEALTH_IDENTITY"
	}
	previousRank := 0
	productCode := ""
	for _, raw := range wire.Facts {
		var doc sourceDocument
		if json.Unmarshal(raw, &doc) != nil || !textOK(doc.Order.ID, 128) || row.OrderID != "" && doc.Order.ID != row.OrderID {
			return row, errSourceRead
		}
		fact, err := normalizeFact("DCAI", doc.Order.ID, raw)
		// The page position and the fact revision clock are distinct fields in
		// this contract. Only the cursor controls pagination.
		if err != nil || fact.Domain != domain || !through.IsZero() && fact.UpdatedAt.After(through) ||
			stream == dcaiHealthStream && (fact.LineID != "" || previousRank > 0 && productCode != fact.SourceKey) {
			return row, errSourceRead
		}
		rank := 0
		switch fact.Kind {
		case "ORDER_PAID":
			if fact.AmountMinor != nil && textOK(fact.PaymentID, 128) {
				rank = 1
			}
		case "ORDER_COMPLETED":
			if stream == dcaiHealthStream && previousRank == 1 && validHealthCompletion(doc, fact, row.Facts[0], wire.Cursor.ID) {
				rank = 2
				row.healthCompletion = &fact
			}
		case "REFUND_SUCCEEDED":
			if fact.AmountMinor != nil && *fact.AmountMinor > 0 && textOK(fact.RefundID, 128) && len(fact.RefundsPayment) != 0 {
				rank = 3
			}
		}
		if rank <= previousRank {
			return row, errSourceRead
		}
		fact.Display.addContacts(wire.Extensions.Contacts)
		previousRank, row.OrderID, productCode = rank, doc.Order.ID, fact.SourceKey
		row.Facts = append(row.Facts, fact)
	}
	seen := map[string]bool{}
	for _, gate := range wire.Gates {
		if (gate.Code != "REFUND_FINALITY" && (gate.Code != "CONTRACT_PREREQUISITE" || stream != dcaiHealthStream)) ||
			seen[gate.Code] || !textOK(gate.OrderID, 128) || gate.EffectiveAt.IsZero() || row.OrderID != "" && gate.OrderID != row.OrderID {
			return row, errSourceRead
		}
		seen[gate.Code], row.OrderID = true, gate.OrderID
		if gate.Code == "CONTRACT_PREREQUISITE" && (gate.Status == "PASSED") != (row.healthCompletion != nil) {
			return row, errSourceRead
		}
		if gate.Status == "BLOCKED" && gate.Reason != nil && textOK(*gate.Reason, 64) {
			row.Blockers = append(row.Blockers, *gate.Reason)
		} else if gate.Status != "PASSED" || gate.Reason != nil {
			return row, errSourceRead
		}
	}
	if stream == dcaiHealthStream {
		if (len(wire.Facts) != 0 || len(wire.Gates) != 0) && !seen["CONTRACT_PREREQUISITE"] {
			return row, errSourceRead
		}
		row.healthContractObserved = true
	}
	return row, nil
}

func validHealthCompletion(doc sourceDocument, completion, paid sourceFact, sourceRowID string) bool {
	if doc.Record.Table != "ai_health_ambassador_apply" || doc.Business.Entity != "HEALTH_IDENTITY_ORDER" ||
		paid.RecordKey != "ai_health_ambassador_order:"+sourceRowID || completion.OccurredAt.Before(paid.OccurredAt) {
		return false
	}
	matched := false
	for _, link := range doc.Relations {
		if link.Kind != "COMPLETES_ORDER" {
			continue
		}
		if matched || link.OrderID != paid.OrderID || link.LineID != paid.LineID || link.RecordID != sourceRowID {
			return false
		}
		matched = true
	}
	return matched
}
