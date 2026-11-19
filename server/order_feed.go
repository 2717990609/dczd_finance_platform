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
	Cursor   orderCursor
	OrderID  string
	Facts    []sourceFact
	Blockers []string
}

type orderPage struct {
	Rows    []orderPageRow
	Next    orderCursor
	HasMore bool
}

type orderFeedClient struct {
	endpoint *url.URL
	token    string
	source   string
}

func newOrderFeedClient(source string, cfg sourceConfig) (orderFeedClient, error) {
	if cfg.BaseURL == "" || cfg.Token == "" {
		return orderFeedClient{}, errSourceDisabled
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return orderFeedClient{}, errSourceRead
	}
	return orderFeedClient{endpoint: base.JoinPath("/internal/v1/finance/source-orders"), token: cfg.Token, source: source}, nil
}

func (c orderFeedClient) contract() string {
	if c.source == "DCZD" {
		return dczdOrderPageContract
	}
	return dcaiOrderPageContract
}

func (c orderFeedClient) sourceKey() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(c.endpoint.String())))
}

func (c orderFeedClient) get(ctx context.Context, action string, query url.Values, result any) error {
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
	if action != "readiness" {
		req.Header.Set("X-"+c.source+"-Finance-Contract", c.contract())
	}
	res, err := pullClient.Do(req)
	if err != nil {
		return errSourceRead
	}
	defer res.Body.Close()
	const limit = 8 << 20
	if res.StatusCode != http.StatusOK {
		return errSourceRead
	}
	wire, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(wire) > limit || json.Unmarshal(wire, result) != nil {
		return errSourceRead
	}
	return nil
}

func (c orderFeedClient) readiness(ctx context.Context, stream string) (int, error) {
	var body struct {
		Status    string            `json:"status"`
		Version   string            `json:"contract_version"`
		Schema    string            `json:"source_fact_schema_version"`
		FactHash  string            `json:"source_fact_schema_sha256"`
		PageHash  string            `json:"pull_page_schema_sha256"`
		Streams   map[string]string `json:"streams"`
		PageSize  int               `json:"max_page_size"`
		Conflicts *int64            `json:"conflict_count"`
	}
	if err := c.get(ctx, "readiness", nil, &body); err != nil {
		return 0, err
	}
	if body.Status != "READY" || body.Streams[stream] != "READY" || body.Version != c.contract() || body.Schema != sourceSchema {
		return 0, errSourceRead
	}
	if c.source == "DCZD" {
		if body.PageSize < 1 || body.Conflicts == nil || *body.Conflicts != 0 {
			return 0, errSourceRead
		}
		return min(orderPageSize, body.PageSize), nil
	}
	if body.FactHash != dcaiFactSchemaHash || body.PageHash != dcaiPageSchemaHash {
		return 0, errSourceRead
	}
	return orderPageSize, nil
}

func (c orderFeedClient) cutoff(ctx context.Context) (time.Time, error) {
	var body struct {
		Version string    `json:"contract_version"`
		Through time.Time `json:"through"`
	}
	err := c.get(ctx, "cutoff", nil, &body)
	if err != nil || body.Version != c.contract() || !validOrderWatermark(body.Through) {
		return time.Time{}, errSourceRead
	}
	return body.Through, nil
}

type orderWireRow struct {
	Cursor orderCursor       `json:"cursor"`
	Facts  []json.RawMessage `json:"facts"`
	Gates  []dcaiGate        `json:"gates"`
}

func (c orderFeedClient) page(ctx context.Context, stream string, after orderCursor, through time.Time, limit int) (orderPage, error) {
	query := url.Values{
		"stream": {stream}, "after_updated_at": {after.UpdatedAt.UTC().Format(time.RFC3339Nano)},
		"after_id": {after.ID}, "through": {through.UTC().Format(time.RFC3339Nano)}, "limit": {strconv.Itoa(limit)},
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
	if err != nil || body.Version != c.contract() || body.Schema != sourceSchema || body.Stream != stream ||
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
	for _, raw := range wire.Facts {
		var doc sourceDocument
		if json.Unmarshal(raw, &doc) != nil || !textOK(doc.Order.ID, 128) || row.OrderID != "" && doc.Order.ID != row.OrderID {
			return row, errSourceRead
		}
		fact, err := normalizeFact("DCAI", doc.Order.ID, raw)
		// The page position and the fact revision clock are distinct fields in
		// this contract. Only the cursor controls pagination.
		if err != nil || fact.Domain != domain || !through.IsZero() && fact.UpdatedAt.After(through) {
			return row, errSourceRead
		}
		rank := 0
		switch fact.Kind {
		case "ORDER_PAID":
			if fact.AmountMinor != nil && textOK(fact.PaymentID, 128) {
				rank = 1
			}
		case "ORDER_COMPLETED":
			if stream == dcaiHealthStream {
				rank = 2
			}
		case "REFUND_SUCCEEDED":
			if fact.AmountMinor != nil && *fact.AmountMinor > 0 && textOK(fact.RefundID, 128) && len(fact.RefundsPayment) != 0 {
				rank = 3
			}
		}
		if rank <= previousRank {
			return row, errSourceRead
		}
		previousRank, row.OrderID = rank, doc.Order.ID
		row.Facts = append(row.Facts, fact)
	}
	seen := map[string]bool{}
	for _, gate := range wire.Gates {
		if (gate.Code != "REFUND_FINALITY" && (gate.Code != "CONTRACT_PREREQUISITE" || stream != dcaiHealthStream)) ||
			seen[gate.Code] || !textOK(gate.OrderID, 128) || gate.EffectiveAt.IsZero() || row.OrderID != "" && gate.OrderID != row.OrderID {
			return row, errSourceRead
		}
		seen[gate.Code], row.OrderID = true, gate.OrderID
		if gate.Status == "BLOCKED" && gate.Reason != nil && textOK(*gate.Reason, 64) {
			row.Blockers = append(row.Blockers, *gate.Reason)
		} else if gate.Status != "PASSED" || gate.Reason != nil {
			return row, errSourceRead
		}
	}
	return row, nil
}
