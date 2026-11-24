package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
)

const retentionHistoryPageSize = 25

var errRetentionDuplicate = errors.New("已有该笔期初、报销或冲销记录，请核对台账")

// The retained share is the credit. Its funding rows only explain which
// team pool amounts funded it, so joining them here would multiply the credit.
var departmentRetentionSQL = organizationScopeQuery(`WITH RECURSIVE `+organizationScopeCTE+`,
manual AS MATERIALIZED (
 SELECT entry.*, original.kind AS original_kind FROM department_retention_entries entry
 LEFT JOIN department_retention_entries original ON original.id=entry.original_id
 WHERE entry.book_month<=$1::date AND ({{unit}} IS NULL OR entry.unit_id IN (SELECT id FROM organization_scope))
), credits AS (
 SELECT share.id,a.unit_id,share.amount_minor,p.id AS period_id,p.name AS period_name,
  product.name AS product_name,a.item_key,a.scenario_key,settlement.confirmed_at,`+periodReportMonthSQL+` AS book_month
 FROM monthly_pool_allocation_shares share JOIN monthly_pool_allocations a ON a.id=share.allocation_id
 JOIN settlements settlement ON (settlement.period_id,settlement.draft_version)=(a.period_id,a.draft_version)
 JOIN periods p ON p.id=a.period_id JOIN products product ON product.id=p.product_id
 LEFT JOIN manual opening ON opening.unit_id=a.unit_id AND opening.kind='opening'
 WHERE share.person_id IS NULL AND `+periodReportMonthSQL+` <= $1::date
  AND (opening.id IS NULL OR `+periodReportMonthSQL+`>opening.book_month)
  AND ({{unit}} IS NULL OR a.unit_id IN (SELECT id FROM organization_scope))
), entries AS MATERIALIZED (
 SELECT 'credit-'||id AS key,unit_id,amount_minor,'income' AS kind,'income' AS category,
  period_id,period_name,product_name,item_key,scenario_key,confirmed_at AS recorded_at,book_month,NULL::bigint AS manual_id,
  '' AS note,NULL::text AS reimbursement_no,false AS reversible
 FROM credits
 UNION ALL
 SELECT 'record-'||id,unit_id,amount_minor,kind,coalesce(original_kind,kind),
  NULL,NULL,NULL,NULL,NULL,recorded_at,book_month,id,note,reimbursement_no,
  kind IN ('expense','correction') AND NOT EXISTS(SELECT 1 FROM department_retention_entries r WHERE r.original_id=manual.id)
 FROM manual
), accounts AS (
 SELECT unit_id, coalesce(sum(amount_minor) FILTER(WHERE book_month < $1 OR kind='opening'),0) AS opening_minor,
  coalesce(sum(amount_minor) FILTER(WHERE book_month=$1 AND category='income'),0) AS income_minor,
  coalesce(sum(amount_minor) FILTER(WHERE book_month=$1 AND category='expense'),0) AS expense_minor,
  coalesce(sum(amount_minor) FILTER(WHERE book_month=$1 AND category='correction'),0) AS correction_minor,
  sum(amount_minor) AS balance_minor
 FROM entries GROUP BY unit_id
), statement AS (
 SELECT key AS id,period_id AS "periodId",period_name AS "periodName",product_name AS "productName",
  unit_id AS "unitId",item_key AS "itemKey",scenario_key AS scenario,
  amount_minor AS "amountMinor",recorded_at AS "confirmedAt",to_char(book_month,'YYYY-MM') AS month,
  kind,manual_id AS "manualId",note,reimbursement_no AS "reimbursementNo",reversible
 FROM entries WHERE unit_id=$3::uuid
 ORDER BY book_month DESC,recorded_at DESC,key DESC LIMIT $4 OFFSET $5
)
SELECT jsonb_build_object(
 'items',coalesce((SELECT jsonb_agg(jsonb_build_object(
  'unitId',u.id,'name',u.name,'scopeName',concat_ws(' / ',grandparent.name,parent.name,u.name),
  'openingMinor',a.opening_minor,'incomeMinor',a.income_minor,'expenseMinor',a.expense_minor,
  'correctionMinor',a.correction_minor,'balanceMinor',a.balance_minor
 ) ORDER BY grandparent.name,parent.name,u.name,u.id) FROM accounts a
  JOIN organization_units u ON u.id=a.unit_id LEFT JOIN organization_units parent ON parent.id=u.parent_id
  LEFT JOIN organization_units grandparent ON grandparent.id=parent.parent_id),'[]'),
 'entries',coalesce((SELECT jsonb_agg(to_jsonb(statement) ORDER BY month DESC,"confirmedAt" DESC,id DESC) FROM statement),'[]'),
 'totalEntries',(SELECT count(*) FROM entries WHERE unit_id=$3::uuid),
 'canRecord',$6::boolean
)`, "$2")

func (s workspace) departmentRetention(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	month, err := parseReportMonth(q.Get("month"))
	scope, scopeErr := parseOrganizationScope(q.Get("unitId"))
	account, accountErr := parseOrganizationScope(q.Get("accountId"))
	offset, pageErr := reportPageOffset(q.Get("page"))
	if err != nil || scopeErr != nil || accountErr != nil || pageErr != nil {
		respond(w, nil, invalid)
		return
	}
	allowed, err := s.canWriteRetention(r.Context(), s.db)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var result json.RawMessage
	err = s.db.QueryRow(r.Context(), departmentRetentionSQL, month, scope, account, retentionHistoryPageSize, offset, allowed).Scan(&result)
	respond(w, result, err)
}

type retentionEntryInput struct {
	UnitID          string `json:"unitId"`
	Month           string `json:"month"`
	Kind            string `json:"kind"`
	AmountMinor     int64  `json:"amountMinor"`
	Note            string `json:"note"`
	ReimbursementNo string `json:"reimbursementNo"`
	OriginalID      int64  `json:"originalId"`
	RequestID       string `json:"requestId"`
}

const retentionWriteGrantsSQL = `SELECT EXISTS(
 SELECT 1 FROM finance_accounts account JOIN finance_account_settlement_grants grant_row ON grant_row.account_id=account.id
 WHERE account.id=$1::uuid AND account.enabled AND account.workspace_access
) AND NOT EXISTS(
 SELECT 1 FROM products p WHERE (p.settlement_enabled OR EXISTS(
  SELECT 1 FROM periods pr JOIN settlements s ON s.period_id=pr.id WHERE pr.product_id=p.id
 )) AND NOT EXISTS(
  SELECT 1 FROM finance_account_settlement_grants grant_row WHERE grant_row.account_id=$1::uuid AND grant_row.product_id=p.id
 )
)`

func (s workspace) canWriteRetention(ctx context.Context, reader catalogPeriodReader) (bool, error) {
	operator := financeUser(ctx)
	if operator == nil {
		return s.development, nil
	}
	if !operator.Workspace {
		return false, nil
	}
	var allowed bool
	err := reader.QueryRow(ctx, retentionWriteGrantsSQL, operator.ID).Scan(&allowed)
	return allowed, err
}

func (s workspace) recordRetentionEntry(ctx context.Context, input retentionEntryInput) (any, error) {
	unit, validUnit := canonicalSourceUUID(input.UnitID)
	request, validRequest := canonicalSourceUUID(input.RequestID)
	month, err := parseReportMonth(input.Month)
	input.Note, input.ReimbursementNo = strings.TrimSpace(input.Note), strings.TrimSpace(input.ReimbursementNo)
	if !validUnit || !validRequest || err != nil || input.Month > time.Now().In(shanghai).Format("2006-01") ||
		input.AmountMinor < -100000000000 || input.AmountMinor > 100000000000 || len([]rune(input.Note)) > 200 {
		return nil, invalid
	}
	amount := input.AmountMinor
	switch input.Kind {
	case "opening":
	case "expense":
		if amount <= 0 || !textOK(input.ReimbursementNo, 80) {
			return nil, invalid
		}
		amount = -amount
	case "correction":
		if amount == 0 || !textOK(input.Note, 200) {
			return nil, invalid
		}
	case "reversal":
		if amount != 0 || input.OriginalID < 1 {
			return nil, invalid
		}
	default:
		return nil, invalid
	}
	if (input.Kind != "reversal" && input.OriginalID != 0) || (input.Kind != "expense" && input.ReimbursementNo != "") {
		return nil, invalid
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		allowed, err := s.canWriteRetention(ctx, tx)
		if err != nil {
			return err
		}
		if !allowed {
			return errFinanceForbidden
		}
		actor := "本地财务"
		if operator := financeUser(ctx); operator != nil {
			if err := lockFinanceOperatorSession(ctx, tx, operator); err != nil {
				return err
			}
			actor = operator.ID
		}
		var locked string
		if err := tx.QueryRow(ctx, "SELECT id::text FROM organization_units WHERE id=$1 FOR UPDATE", unit).Scan(&locked); err != nil {
			return err
		}
		if input.Kind == "reversal" {
			if err := tx.QueryRow(ctx, `SELECT -amount_minor FROM department_retention_entries
			 WHERE id=$1 AND unit_id=$2 AND book_month<=$3 AND kind IN ('expense','correction')`,
				input.OriginalID, unit, month).Scan(&amount); err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `INSERT INTO department_retention_entries
		 (unit_id,book_month,kind,amount_minor,note,reimbursement_no,original_id,request_id,recorded_by)
		 VALUES($1,$2,$3,$4,$5,nullif($6::text,''),nullif($7::bigint,0),$8,$9)
		 ON CONFLICT DO NOTHING RETURNING id`,
			unit, month, input.Kind, amount, input.Note, input.ReimbursementNo, input.OriginalID, request, actor).Scan(&id)
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT id FROM department_retention_entries WHERE request_id=$8
		 AND (unit_id,book_month,kind,amount_minor,note,reimbursement_no,original_id)
		 IS NOT DISTINCT FROM ($1::uuid,$2::date,$3::text,$4::bigint,$5::text,nullif($6::text,''),nullif($7::bigint,0))`,
			unit, month, input.Kind, amount, input.Note, input.ReimbursementNo, input.OriginalID, request).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errRetentionDuplicate
		}
		return err
	})
	return map[string]int64{"id": id}, err
}
