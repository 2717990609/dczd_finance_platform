package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var errSourceDisabled = errors.New("source configuration is missing")
var errSourceRead = errors.New("source request failed")

const sourceSchema = "source.order-fact/1.0"

type sourceConfig struct{ BaseURL, Token string }

type pullInput struct{ Source, Stream, OrderID string }

type sourceFact struct {
	Source          string                   `json:"source"`
	OrderID         string                   `json:"orderId"`
	OrderNo         string                   `json:"orderNo"`
	LineID          string                   `json:"lineId"`
	RecordKey       string                   `json:"recordKey"`
	Version         string                   `json:"version"`
	Kind            string                   `json:"kind"`
	Domain          string                   `json:"domain"`
	SourceKey       string                   `json:"sourceKey"`
	TransactionType string                   `json:"transactionType"`
	SourceStatus    string                   `json:"sourceStatus"`
	PaymentID       string                   `json:"paymentId,omitempty"`
	RefundID        string                   `json:"refundId,omitempty"`
	RefundsPayment  []sourcePaymentReference `json:"refundsPayment,omitempty"`
	ReferrerID      string                   `json:"referrerId,omitempty"`
	Participants    []sourceParticipant      `json:"participants,omitempty"`
	AmountMinor     *int64                   `json:"amountMinor"`
	OccurredAt      time.Time                `json:"occurredAt"`
	UpdatedAt       time.Time                `json:"updatedAt"`
}

type sourcePaymentReference struct {
	OrderID  string `json:"orderId"`
	LineID   string `json:"lineId,omitempty"`
	RecordID string `json:"recordId,omitempty"`
}

type sourceDocument struct {
	Schema     string    `json:"fact_schema_version"`
	Kind       string    `json:"fact_type"`
	OccurredAt time.Time `json:"occurred_at"`
	Order      struct {
		ID     string `json:"source_order_id"`
		No     string `json:"order_no"`
		Line   string `json:"source_line_id"`
		Status struct {
			Code string `json:"code"`
		} `json:"source_status"`
	} `json:"order"`
	Record struct {
		Table     string    `json:"source_table"`
		ID        string    `json:"record_id"`
		Version   string    `json:"record_version"`
		UpdatedAt time.Time `json:"updated_at"`
	} `json:"source_record"`
	Business struct {
		Domain          string `json:"source_domain_code"`
		Code            string `json:"source_product_code"`
		TransactionType string `json:"source_transaction_type_code"`
	} `json:"business"`
	Amounts struct {
		Paid   string `json:"paid_minor"`
		Refund string `json:"refund_minor"`
	} `json:"amounts"`
	Payment struct {
		ID string `json:"source_payment_id"`
	} `json:"payment"`
	Refund struct {
		ID string    `json:"source_refund_id"`
		At time.Time `json:"refunded_at"`
	} `json:"refund"`
	Relations []struct {
		Kind     string `json:"relation"`
		OrderID  string `json:"target_source_order_id"`
		LineID   string `json:"target_source_line_id"`
		RecordID string `json:"target_source_record_id"`
	} `json:"relations"`
	Attribution struct {
		Participants []struct {
			Role   string `json:"source_role_code"`
			UserID string `json:"source_user_id"`
		} `json:"participants"`
	} `json:"attribution_snapshot"`
}

func normalizeFact(source, orderID string, raw json.RawMessage) (sourceFact, error) {
	var wire sourceDocument
	err := json.Unmarshal(raw, &wire)
	if err != nil || wire.Schema != sourceSchema || wire.Order.ID != orderID || wire.Record.UpdatedAt.IsZero() ||
		wire.OccurredAt.IsZero() || !textOK(wire.Record.ID, 180) || !textOK(wire.Record.Table, 150) ||
		!textOK(wire.Record.Version, 180) || !textOK(wire.Kind, 64) || !textOK(wire.Business.Domain, 80) {
		return sourceFact{}, errSourceRead
	}
	var amount *int64
	amountText := ""
	switch wire.Kind {
	case "ORDER_PAID":
		amountText = wire.Amounts.Paid
	case "ORDER_REFUNDED", "REFUND_SUCCEEDED", "REFUND_ADJUSTED":
		amountText = wire.Amounts.Refund
	}
	if amountText != "" {
		n, err := strconv.ParseInt(amountText, 10, 64)
		if err != nil || n < 0 {
			return sourceFact{}, errSourceRead
		}
		amount = &n
	}
	referrer := ""
	var participants []sourceParticipant
	for _, p := range wire.Attribution.Participants {
		if validSourceRole(p.Role) && textOK(p.UserID, 128) {
			participants = append(participants, sourceParticipant{Role: p.Role, UserID: p.UserID})
		}
		if p.Role == "DIRECT_REFERRER" && p.UserID != "" {
			if referrer != "" && referrer != p.UserID {
				return sourceFact{}, errSourceRead
			}
			referrer = p.UserID
		}
	}
	sort.Slice(participants, func(i, j int) bool {
		if participants[i].Role != participants[j].Role {
			return participants[i].Role < participants[j].Role
		}
		return participants[i].UserID < participants[j].UserID
	})
	var refundTargets []sourcePaymentReference
	occurredAt := wire.OccurredAt
	if wire.Kind == "REFUND_SUCCEEDED" {
		if !wire.Refund.At.IsZero() {
			occurredAt = wire.Refund.At
		}
		for _, link := range wire.Relations {
			if link.Kind == "REFUNDS_PAYMENT" {
				refundTargets = append(refundTargets, sourcePaymentReference{
					OrderID: link.OrderID, LineID: link.LineID, RecordID: link.RecordID,
				})
			}
		}
	}
	return sourceFact{Source: source, OrderID: orderID, OrderNo: wire.Order.No, LineID: wire.Order.Line,
		RecordKey: wire.Record.Table + ":" + wire.Record.ID, Version: wire.Record.Version, Kind: wire.Kind, Domain: wire.Business.Domain,
		SourceKey: wire.Business.Code, TransactionType: wire.Business.TransactionType, SourceStatus: wire.Order.Status.Code,
		PaymentID: wire.Payment.ID, RefundID: wire.Refund.ID, RefundsPayment: refundTargets, ReferrerID: referrer, Participants: participants, AmountMinor: amount,
		OccurredAt: occurredAt, UpdatedAt: wire.Record.UpdatedAt}, nil
}

var sourcesList = []string{"DCAI", "DCZD", "DCJK"}

func readSources() map[string]sourceConfig {
	cfg := map[string]sourceConfig{}
	for _, name := range sourcesList {
		base, token := strings.TrimSpace(os.Getenv("FINANCE_"+name+"_URL")), strings.TrimSpace(os.Getenv("FINANCE_"+name+"_TOKEN"))
		if base != "" && token != "" {
			cfg[name] = sourceConfig{base, token}
		}
	}
	return cfg
}

var pullClient = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func lookupOrder(ctx context.Context, cfg sourceConfig, req pullInput) ([]sourceFact, error) {
	if cfg.BaseURL == "" || cfg.Token == "" {
		return nil, errSourceDisabled
	}
	endpoint, err := url.Parse(cfg.BaseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errSourceRead
	}
	endpoint = endpoint.JoinPath("/internal/v1/finance/source-orders/lookup")
	method := http.MethodGet
	var body io.Reader
	if req.Source == "DCJK" {
		method = http.MethodPost
		raw, _ := json.Marshal(map[string]string{"source_order_id": req.OrderID})
		body = bytes.NewReader(raw)
	} else {
		q := endpoint.Query()
		q.Set("stream", req.Stream)
		q.Set("source_order_id", req.OrderID)
		endpoint.RawQuery = q.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, errSourceRead
	}
	version := strings.ToLower(req.Source) + ".finance-order-lookup/1.0"
	request.Header.Set("Authorization", "Bearer "+cfg.Token)
	request.Header.Set("X-"+req.Source+"-Finance-Contract", version)
	request.Header.Set("Content-Type", "application/json")
	response, err := pullClient.Do(request)
	if err != nil {
		return nil, errSourceRead
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errSourceRead
	}
	var result struct {
		Version string            `json:"contract_version"`
		Schema  string            `json:"source_fact_schema_version"`
		OrderID string            `json:"source_order_id"`
		Items   []json.RawMessage `json:"items"`
		Item    *struct {
			Facts []json.RawMessage `json:"facts"`
		} `json:"item"`
	}
	wire, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(wire) > 1<<20 || json.Unmarshal(wire, &result) != nil || result.Version != version || result.Schema != sourceSchema || result.OrderID != req.OrderID {
		return nil, errSourceRead
	}
	items := result.Items
	if req.Source != "DCJK" {
		if result.Item == nil {
			return nil, pgx.ErrNoRows
		}
		items = result.Item.Facts
	}
	if len(items) == 0 {
		return nil, pgx.ErrNoRows
	}
	if len(items) > 100 {
		return nil, errSourceRead
	}
	facts := make([]sourceFact, 0, len(items))
	for _, raw := range items {
		fact, e := normalizeFact(req.Source, req.OrderID, raw)
		if e != nil {
			return nil, e
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

func (s workspace) pullOrder(ctx context.Context, p pullInput) (any, error) {
	p.OrderID = strings.TrimSpace(p.OrderID)
	if !validSource(p.Source) || !textOK(p.OrderID, 128) {
		return nil, invalid
	}
	if p.Source == "DCAI" {
		if p.Stream != "VIP_ORDERS" && p.Stream != "HEALTH_IDENTITY_ORDERS" {
			return nil, invalid
		}
	} else {
		p.Stream = "ORDER_FACTS"
	}
	facts, err := lookupOrder(ctx, s.sources[p.Source], p)
	if err != nil {
		return nil, err
	}
	var inserted int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var writeErr error
		inserted, writeErr = storeSourceFacts(ctx, tx, facts)
		return writeErr
	})
	return map[string]any{"facts": len(facts), "newVersions": inserted}, err
}

func storeSourceFacts(ctx context.Context, tx pgx.Tx, facts []sourceFact) (int64, error) {
	var inserted int64
	for _, f := range facts {
		raw, e := json.Marshal(f)
		if e != nil {
			return 0, e
		}
		tag, e := tx.Exec(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`,
			f.Source, f.OrderID, f.LineID, f.RecordKey, f.Version, f.Kind, f.UpdatedAt, raw, f.OccurredAt)
		if e != nil {
			return 0, e
		}
		if tag.RowsAffected() == 0 {
			var same bool
			e = tx.QueryRow(ctx, `SELECT fact=$7::jsonb OR (NOT (fact ? 'participants') AND fact=$7::jsonb - 'participants') FROM source_order_versions
			 WHERE source=$1 AND order_id=$2 AND line_id=$3 AND record_key=$4 AND version=$5 AND kind=$6`,
				f.Source, f.OrderID, f.LineID, f.RecordKey, f.Version, f.Kind, raw).Scan(&same)
			if e != nil {
				return 0, e
			}
			if !same {
				return 0, conflict
			}
		}
		inserted += tag.RowsAffected()
	}
	return inserted, nil
}

const ordersSQL = `SELECT coalesce(jsonb_agg(q.data ORDER BY q.id DESC),'[]') FROM (
 SELECT v.id,v.fact || jsonb_build_object('id',v.id,'productName',p.name,'mapped',p.id IS NOT NULL) AS data
 FROM (
  SELECT DISTINCT ON(source,order_id,line_id,record_key,kind) id,source,fact FROM source_order_versions
  WHERE ($1='' OR source=$1) AND ($2='' OR order_id=$2)
  ORDER BY source,order_id,line_id,record_key,kind,updated_at DESC,id DESC
 ) v
 LEFT JOIN source_mappings m ON m.source=v.source AND m.domain=v.fact->>'domain' AND m.source_key=v.fact->>'sourceKey'
 LEFT JOIN products p ON p.id=m.product_id
 WHERE ($3::bigint=0 OR v.id<$3) ORDER BY v.id DESC LIMIT 50
 ) q`

func (s workspace) orders(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	source, order, before := params.Get("source"), strings.TrimSpace(params.Get("orderId")), params.Get("beforeId")
	var boundary int64
	if before != "" {
		id, err := strconv.ParseInt(before, 10, 64)
		if err != nil || id < 1 {
			respond(w, nil, invalid)
			return
		}
		boundary = id
	}
	if (source != "" && !validSource(source)) || len(order) > 128 {
		respond(w, nil, invalid)
		return
	}
	var result json.RawMessage
	err := s.db.QueryRow(r.Context(), ordersSQL, source, order, boundary).Scan(&result)
	respond(w, result, err)
}

func (s workspace) sourceStatuses(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), "SELECT source,max(received_at) FROM source_order_versions GROUP BY source")
	if err != nil {
		respond(w, nil, err)
		return
	}
	defer rows.Close()
	updates := map[string]time.Time{}
	for rows.Next() {
		var source string
		var t time.Time
		if err = rows.Scan(&source, &t); err != nil {
			respond(w, nil, err)
			return
		}
		updates[source] = t
	}
	if err = rows.Err(); err != nil {
		respond(w, nil, err)
		return
	}
	status := make([]map[string]any, 0, len(sourcesList))
	for _, name := range sourcesList {
		_, configured := s.sources[name]
		row := map[string]any{"source": name, "configured": configured, "lastUpdate": nil}
		if t, ok := updates[name]; ok {
			row["lastUpdate"] = t
		}
		status = append(status, row)
	}
	respond(w, status, nil)
}
