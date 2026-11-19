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
var errSourceLookupTimeout = errors.New("子系统单笔查询超时，可稍后重拉或使用订单同步")

const sourceSchema = "source.order-fact/1.0"

type sourceConfig struct{ BaseURL, Token string }

type pullInput struct{ Source, Stream, OrderID string }

type sourceFact struct {
	Source            string                   `json:"source"`
	OrderID           string                   `json:"orderId"`
	OrderNo           string                   `json:"orderNo"`
	LineID            string                   `json:"lineId"`
	RecordKey         string                   `json:"recordKey"`
	Version           string                   `json:"version"`
	Kind              string                   `json:"kind"`
	Domain            string                   `json:"domain"`
	SourceKey         string                   `json:"sourceKey"`
	TransactionType   string                   `json:"transactionType"`
	SourceStatus      string                   `json:"sourceStatus"`
	PaymentID         string                   `json:"paymentId,omitempty"`
	RefundID          string                   `json:"refundId,omitempty"`
	RefundsPayment    []sourcePaymentReference `json:"refundsPayment,omitempty"`
	ReferrerID        string                   `json:"referrerId,omitempty"`
	ReferrerCompanyID string                   `json:"-"`
	ReferrerConflict  bool                     `json:"-"`
	Participants      []sourceParticipant      `json:"participants,omitempty"`
	SalesCompanyID    string                   `json:"-"`
	Classification    sourceClassification     `json:"-"`
	VIPClaims         []vipClaim               `json:"-"`
	AmountMinor       *int64                   `json:"amountMinor"`
	OccurredAt        time.Time                `json:"occurredAt"`
	UpdatedAt         time.Time                `json:"updatedAt"`
}

type sourcePaymentReference struct {
	OrderID  string `json:"orderId"`
	LineID   string `json:"lineId,omitempty"`
	RecordID string `json:"recordId,omitempty"`
}

type sourceAttributionParticipant struct {
	Role           string `json:"source_role_code"`
	UserID         string `json:"source_user_id"`
	CompanyID      string `json:"source_company_id"`
	OrganizationID string `json:"source_organization_id"`
	Relationship   string `json:"source_sales_relationship_code"`
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
		Entity          string `json:"source_entity_type_code"`
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
		Participants []sourceAttributionParticipant `json:"participants"`
	} `json:"attribution_snapshot"`
}

func sourceSalesCompany(parties []sourceAttributionParticipant) (string, error) {
	var company, fallback string
	conflictingFallback := false
	for _, party := range parties {
		switch party.Role {
		case "DIRECT_COMPANY", "PARENT_COMPANY":
			id := strings.TrimSpace(party.CompanyID)
			if id == "" {
				continue
			}
			if !textOK(id, 128) || company != "" && company != id {
				return "", errSourceRead
			}
			company = id
		case "DIRECT_REFERRER", "PROFESSOR":
			id := strings.TrimSpace(party.OrganizationID)
			if id == "" {
				continue
			}
			conflictingFallback = conflictingFallback || !textOK(id, 128) || fallback != "" && fallback != id
			fallback = id
		}
	}
	if company != "" {
		return company, nil
	}
	if conflictingFallback {
		return "", errSourceRead
	}
	return fallback, nil
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
	var company, recommendedCompany string
	var recommendationConflict bool
	if source == "DCZD" && wire.Kind == "ORDER_PAID" {
		company, err = sourceSalesCompany(wire.Attribution.Participants)
		if err != nil {
			return sourceFact{}, err
		}
		for _, part := range wire.Attribution.Participants {
			if part.Role != "UPSTREAM_COMPANY" {
				continue
			}
			id := strings.TrimSpace(part.CompanyID)
			if !textOK(id, 128) || recommendedCompany != "" && recommendedCompany != id {
				recommendationConflict = true
				recommendedCompany = ""
				break
			}
			recommendedCompany = id
		}
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
	classification := classifySourcePayment(source, wire.Business.Domain, wire.Business.Entity, wire.Record.Table)
	var vipClaims []vipClaim
	if classification.Key != "" && wire.Kind == "ORDER_PAID" {
		vipClaims = vipSourceClaims(wire.Attribution.Participants)
	}
	return sourceFact{Source: source, OrderID: orderID, OrderNo: wire.Order.No, LineID: wire.Order.Line,
		RecordKey: wire.Record.Table + ":" + wire.Record.ID, Version: wire.Record.Version, Kind: wire.Kind, Domain: wire.Business.Domain,
		SourceKey: wire.Business.Code, TransactionType: wire.Business.TransactionType, SourceStatus: wire.Order.Status.Code,
		PaymentID: wire.Payment.ID, RefundID: wire.Refund.ID, RefundsPayment: refundTargets, ReferrerID: referrer, Participants: participants, SalesCompanyID: company, AmountMinor: amount,
		ReferrerCompanyID: recommendedCompany, ReferrerConflict: recommendationConflict,
		OccurredAt: occurredAt, UpdatedAt: wire.Record.UpdatedAt,
		Classification: classification, VIPClaims: vipClaims}, nil
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

var dczdLookupClient = &http.Client{Timeout: 12 * time.Second, CheckRedirect: pullClient.CheckRedirect}

type sourceOrderLookup struct {
	Facts []sourceFact
	DCAI  *orderPageRow
}

func lookupOrder(ctx context.Context, cfg sourceConfig, req pullInput) (sourceOrderLookup, error) {
	var lookup sourceOrderLookup
	if cfg.BaseURL == "" || cfg.Token == "" {
		return lookup, errSourceDisabled
	}
	endpoint, err := url.Parse(cfg.BaseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return lookup, errSourceRead
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
		return lookup, errSourceRead
	}
	version := strings.ToLower(req.Source) + ".finance-order-lookup/1.0"
	request.Header.Set("Authorization", "Bearer "+cfg.Token)
	request.Header.Set("X-"+req.Source+"-Finance-Contract", version)
	request.Header.Set("Content-Type", "application/json")
	client := pullClient
	if req.Source == "DCZD" {
		client = dczdLookupClient
	}
	response, err := client.Do(request)
	if err != nil {
		if req.Source == "DCZD" && errors.Is(err, context.DeadlineExceeded) {
			return lookup, errSourceLookupTimeout
		}
		return lookup, errSourceRead
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return lookup, errSourceRead
	}
	var result struct {
		Version string            `json:"contract_version"`
		Schema  string            `json:"source_fact_schema_version"`
		OrderID string            `json:"source_order_id"`
		Stream  string            `json:"stream"`
		Items   []json.RawMessage `json:"items"`
		Item    *orderWireRow     `json:"item"`
	}
	wire, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(wire) > 1<<20 || json.Unmarshal(wire, &result) != nil || result.Version != version || result.Schema != sourceSchema || result.OrderID != req.OrderID {
		return lookup, errSourceRead
	}
	items := result.Items
	if req.Source != "DCJK" {
		if result.Item == nil {
			return lookup, pgx.ErrNoRows
		}
		items = result.Item.Facts
	}
	if req.Source == "DCAI" {
		if result.Stream != req.Stream || !result.Item.Cursor.valid() || result.Item.Cursor.ID == "0" {
			return lookup, errSourceRead
		}
		row, err := normalizeDCAIOrderRow(req.Stream, time.Time{}, *result.Item)
		if err == nil && row.OrderID == "" {
			return lookup, pgx.ErrNoRows
		}
		if err != nil || row.OrderID != req.OrderID {
			return lookup, errSourceRead
		}
		return sourceOrderLookup{Facts: row.Facts, DCAI: &row}, nil
	}
	if req.Source == "DCZD" && (result.Stream != req.Stream || positiveSourceID(result.Item.Cursor.ID) == 0 ||
		!validOrderWatermark(result.Item.Cursor.UpdatedAt) || result.Item.Gates == nil || len(result.Item.Gates) != 0) {
		return lookup, errSourceRead
	}
	if len(items) == 0 {
		return lookup, pgx.ErrNoRows
	}
	if len(items) > 100 {
		return lookup, errSourceRead
	}
	facts := make([]sourceFact, 0, len(items))
	for _, raw := range items {
		fact, e := normalizeFact(req.Source, req.OrderID, raw)
		if e != nil {
			return lookup, e
		}
		facts = append(facts, fact)
	}
	return sourceOrderLookup{Facts: facts}, nil
}

func (s workspace) pullOrder(ctx context.Context, p pullInput) (any, error) {
	p.OrderID = strings.TrimSpace(p.OrderID)
	if !validSource(p.Source) || !textOK(p.OrderID, 128) {
		return nil, invalid
	}
	if p.Source == "DCAI" {
		if p.Stream != dcaiVIPStream && p.Stream != dcaiHealthStream {
			return nil, invalid
		}
	} else {
		p.Stream = dczdOrderStream
	}
	if p.Source == "DCZD" {
		// Leave time for the workspace's HTTP deadline to deliver a useful error.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 13*time.Second)
		defer cancel()
	}
	lookup, err := lookupOrder(ctx, s.sources[p.Source], p)
	if p.Source == "DCZD" && (err == nil || errors.Is(err, pgx.ErrNoRows)) {
		client, err := newOrderFeedClient(p.Source, s.sources[p.Source])
		if err != nil {
			return nil, err
		}
		if _, err := client.readiness(ctx, p.Stream); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, errSourceLookupTimeout
			}
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	var inserted int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		sourceKey := ""
		if p.Source != "DCJK" {
			sourceKey = s.orderSourceKey(p.Source)
		}
		var writeErr error
		inserted, writeErr = storeSourceFactsFrom(ctx, tx, lookup.Facts, sourceKey)
		if writeErr != nil || lookup.DCAI == nil {
			return writeErr
		}
		if len(lookup.Facts) == 0 {
			if err := lockOrderProvider(ctx, tx, p.Source, sourceKey); err != nil {
				return err
			}
		}
		return storeDCAIBlockers(ctx, tx, p.Stream, []orderPageRow{*lookup.DCAI})
	})
	return map[string]any{"facts": len(lookup.Facts), "newVersions": inserted}, err
}

func storeSourceFacts(ctx context.Context, tx pgx.Tx, facts []sourceFact) (int64, error) {
	return storeSourceFactsFrom(ctx, tx, facts, "")
}

func storeSourceFactsFrom(ctx context.Context, tx pgx.Tx, facts []sourceFact, sourceKey string) (int64, error) {
	if len(facts) != 0 {
		sources := make(map[string]bool)
		for _, fact := range facts {
			sources[fact.Source] = true
		}
		keys := make([]string, 0, len(sources))
		for source := range sources {
			keys = append(keys, source)
		}
		if err := touchSourceWrites(ctx, tx, keys); err != nil {
			return 0, err
		}
		for _, source := range []string{"DCAI", "DCZD"} {
			if sources[source] {
				if err := checkOrderProvider(ctx, tx, source, sourceKey, true); err != nil {
					return 0, err
				}
			}
		}
	}
	var insertedIDs, metadataIDs []int64
	for _, f := range facts {
		raw, e := json.Marshal(f)
		if e != nil {
			return 0, e
		}
		var sourceVersion int64
		created := false
		e = tx.QueryRow(ctx, `INSERT INTO source_order_versions(source,order_id,line_id,record_key,version,kind,updated_at,fact,occurred_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING RETURNING id`,
			f.Source, f.OrderID, f.LineID, f.RecordKey, f.Version, f.Kind, f.UpdatedAt, raw, f.OccurredAt).Scan(&sourceVersion)
		if errors.Is(e, pgx.ErrNoRows) {
			var same bool
			e = tx.QueryRow(ctx, `SELECT id,fact=$7::jsonb OR (NOT (fact ? 'participants') AND fact=$7::jsonb - 'participants') FROM source_order_versions
			 WHERE source=$1 AND order_id=$2 AND line_id=$3 AND record_key=$4 AND version=$5 AND kind=$6`,
				f.Source, f.OrderID, f.LineID, f.RecordKey, f.Version, f.Kind, raw).Scan(&sourceVersion, &same)
			if e != nil {
				return 0, e
			}
			if !same {
				return 0, conflict
			}
		} else if e != nil {
			return 0, e
		} else {
			insertedIDs = append(insertedIDs, sourceVersion)
			created = true
		}
		metadataChanged := false
		if f.Classification.Key != "" {
			changed, e := storeSourceClassification(ctx, tx, sourceVersion, f.Classification, f.VIPClaims)
			if e != nil {
				return 0, e
			}
			metadataChanged = changed
		}
		if f.SalesCompanyID != "" {
			changed, e := storeSourcePaymentCompany(ctx, tx, sourceVersion, f)
			if e != nil {
				return 0, e
			}
			metadataChanged = metadataChanged || changed
		}
		if metadataChanged && !created {
			metadataIDs = append(metadataIDs, sourceVersion)
		}
	}
	if err := enqueueDraftFactChanges(ctx, tx, insertedIDs, metadataIDs); err != nil {
		return 0, err
	}
	return int64(len(insertedIDs)), nil
}

func touchSourceWrites(ctx context.Context, tx pgx.Tx, sources []string) error {
	_, err := tx.Exec(ctx, `INSERT INTO source_write_state(source,revision)
	 SELECT source,1 FROM unnest($1::text[]) AS input(source) ORDER BY source
	 ON CONFLICT(source) DO UPDATE SET revision=source_write_state.revision+1`, sources)
	return err
}

func storeSourcePaymentCompany(ctx context.Context, tx pgx.Tx, version int64, f sourceFact) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO source_payment_companies(source_version_id,company_id,recommendation_company_id,recommendation_conflict)
	 VALUES($1,$2,nullif($3,''),$4)
	 ON CONFLICT(source_version_id) DO UPDATE SET recommendation_company_id=excluded.recommendation_company_id,
	  recommendation_conflict=excluded.recommendation_conflict
	 WHERE source_payment_companies.company_id=excluded.company_id
	  AND source_payment_companies.recommendation_company_id IS NULL AND NOT source_payment_companies.recommendation_conflict
	  AND (excluded.recommendation_company_id IS NOT NULL OR excluded.recommendation_conflict)`,
		version, f.SalesCompanyID, f.ReferrerCompanyID, f.ReferrerConflict)
	if err != nil || tag.RowsAffected() != 0 {
		return err == nil, err
	}
	var same bool
	if err = tx.QueryRow(ctx, `SELECT company_id=$2 AND (($3='' AND NOT $4) OR
	 (coalesce(recommendation_company_id,'')=$3 AND recommendation_conflict=$4))
	 FROM source_payment_companies WHERE source_version_id=$1`,
		version, f.SalesCompanyID, f.ReferrerCompanyID, f.ReferrerConflict).Scan(&same); err != nil {
		return false, err
	}
	if !same {
		return false, conflict
	}
	return false, nil
}

const ordersSQL = `SELECT coalesce(jsonb_agg(q.data ORDER BY q.id DESC),'[]') FROM (
 SELECT v.id,v.fact || jsonb_build_object('id',v.id,'productName',p.name,'mapped',p.id IS NOT NULL) AS data
 FROM (
  SELECT * FROM (
   SELECT DISTINCT ON(source,order_id,line_id,record_key,kind) id,source,record_key,occurred_at,fact FROM source_order_versions
   WHERE ($1='' OR source=$1) AND ($2='' OR order_id=$2)
   ORDER BY source,order_id,line_id,record_key,kind,updated_at DESC,id DESC
  ) latest
  WHERE ($3::bigint=0 OR latest.id<$3) ORDER BY latest.id DESC LIMIT 50
 ) v
 LEFT JOIN source_mappings m ON m.source=v.source AND m.domain=v.fact->>'domain' AND m.source_key=v.fact->>'sourceKey'
 LEFT JOIN source_order_classifications classification ON classification.source_version_id=v.id
 LEFT JOIN LATERAL (
  SELECT min(m.product_id) AS product_id FROM catalog_business_routes m
  JOIN product_catalog_settings c ON c.product_id=m.product_id AND c.captured_at=m.observed_at
  WHERE (m.source,m.domain,m.record_table,m.entity_type,m.business_key,m.classifier)=
   (v.source,v.fact->>'domain',split_part(v.record_key,':',1),classification.entity_type,classification.business_key,classification.classifier)
   AND m.effective_from<=v.occurred_at AND (m.effective_to IS NULL OR v.occurred_at<m.effective_to)
  HAVING count(DISTINCT m.product_id)=1
 ) route ON true
 LEFT JOIN products p ON p.id=CASE WHEN ` + catalogSourceReservedSQL + ` THEN route.product_id ELSE m.product_id END
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
	ctx := r.Context()
	rows, err := s.db.Query(ctx, "SELECT source,max(received_at) FROM source_order_versions GROUP BY source")
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
	var syncStatus json.RawMessage
	err = s.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object(
	 'stream',s.stream,'initialFrom',s.initial_from,'completedThrough',s.completed_through,'targetThrough',s.target_through,
	 'scannedOrders',s.scanned,'newVersions',s.versions,'failed',s.failed,'sourceMatches',s.source_key=$1) ORDER BY s.stream),'[]')
	 FROM dcai_order_sync s WHERE s.stream=ANY($2::text[])`, s.dcaiSourceKey(), []string{dcaiVIPStream, dcaiHealthStream}).Scan(&syncStatus)
	if err != nil {
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
		if name == "DCAI" {
			row["syncs"] = syncStatus
		}
		status = append(status, row)
	}
	respond(w, status, nil)
}
