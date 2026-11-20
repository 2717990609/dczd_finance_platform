package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type practiceRegistrationImportInput struct {
	ProductID int64           `json:"productId"`
	PeriodID  *int64          `json:"periodId,omitempty"`
	Rate      int64           `json:"rate"`
	Document  json.RawMessage `json:"document"`
}

type practiceRegistrationDocument struct {
	Version    int                             `json:"version_no"`
	PeriodKey  string                          `json:"period_key"`
	PeriodName string                          `json:"period_name"`
	StartsAt   time.Time                       `json:"period_start"`
	Rows       []practiceRegistrationImportRow `json:"rows"`
}

type practiceRegistrationImportRow struct {
	Number        int    `json:"number"`
	LeaderID      string `json:"leader_id"`
	LeaderName    string `json:"leader_display_name"`
	LeaderCompany string `json:"leader_company_id"`
	Recipient     string `json:"recipient_id"`
	SourceSeller  string `json:"leader_source_user_id"`
	SourceCompany string `json:"leader_source_company_id"`
	SourceMember  string `json:"member_source_company_id"`
	MemberName    string `json:"member_company_name"`
	Pending       bool   `json:"pending_review"`
}

type practiceRegistrationImportIssue struct {
	Number  int    `json:"number,omitempty"`
	Message string `json:"message"`
}

type practiceRegistrationImportPreview struct {
	PeriodName string                            `json:"periodName"`
	StartsAt   time.Time                         `json:"startsAt"`
	Version    int                               `json:"version"`
	RowCount   int                               `json:"rowCount"`
	Components []component                       `json:"components"`
	Pending    []practiceRegistrationImportIssue `json:"pending"`
	Issues     []practiceRegistrationImportIssue `json:"issues"`
}

// Source-company scopes keep the same business IDs when an alias later expires.
// Explicitly disabled options are still excluded.
func readPracticeRegistrationCompanies(ctx context.Context, tx pgx.Tx, ids []string, at *time.Time) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT wanted.id,coalesce(mapped.unit,'')
	 FROM (SELECT DISTINCT unnest($1::text[]) AS id) wanted
	 LEFT JOIN configuration_options o ON o.group_key='practiceSalesCompany' AND o.option_key=wanted.id
	 LEFT JOIN LATERAL (
	  SELECT CASE WHEN count(DISTINCT u.id) FILTER (WHERE active.at_start)=1
	    THEN min(u.id::text) FILTER (WHERE active.at_start) END AS unit,
	   count(DISTINCT u.id) FILTER (WHERE active.now)=1 AS known
	  FROM organization_company_sources s
	  JOIN organization_units u ON u.id=s.company_id AND u.status='ACTIVE' AND u.kind='COMPANY'
	  CROSS JOIN LATERAL (
	   SELECT coalesce($2::timestamptz,CURRENT_TIMESTAMP)>=s.valid_from
	    AND (s.valid_to IS NULL OR coalesce($2::timestamptz,CURRENT_TIMESTAMP)<s.valid_to) AS at_start,
	    CURRENT_TIMESTAMP>=s.valid_from AND (s.valid_to IS NULL OR CURRENT_TIMESTAMP<s.valid_to) AS now
	  ) active
	  WHERE s.source='DCZD' AND s.source_company_id=wanted.id
	 ) mapped ON true WHERE coalesce(o.enabled,mapped.unit IS NOT NULL OR mapped.known)`, ids, at)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	var id, unit string
	_, err = pgx.ForEachRow(rows, []any{&id, &unit}, func() error {
		result[id] = unit
		return nil
	})
	return result, err
}

func validatePracticeRegistrationCompanies(ctx context.Context, tx pgx.Tx, rule publishRuleInput, ids []string) error {
	at := rule.EffectiveFrom
	if rule.PeriodID != nil {
		if err := tx.QueryRow(ctx, `SELECT coalesce(roster.starts_at,p.starts_on::timestamp AT TIME ZONE 'Asia/Shanghai')
		 FROM periods p LEFT JOIN LATERAL (
		  SELECT starts_at FROM practice_rosters WHERE period_key=p.source_period_key ORDER BY source_version DESC LIMIT 1
		 ) roster ON true WHERE p.id=$1 AND p.product_id=$2`, *rule.PeriodID, rule.ProductID).Scan(&at); err != nil {
			return err
		}
	}
	companies, err := readPracticeRegistrationCompanies(ctx, tx, ids, at)
	if err == nil && slices.ContainsFunc(ids, func(id string) bool { _, found := companies[id]; return !found }) {
		err = errPracticeTierCompany
	}
	return err
}

func (s workspace) previewPracticeRegistrationImport(ctx context.Context, input practiceRegistrationImportInput) (practiceRegistrationImportPreview, error) {
	var doc practiceRegistrationDocument
	if input.ProductID <= 0 || input.Rate < 0 || input.Rate > periodRateScale ||
		json.Unmarshal(input.Document, &doc) != nil || doc.Version < 1 || !validPracticeKey(doc.PeriodKey) ||
		doc.StartsAt.IsZero() || len(doc.Rows) == 0 {
		return practiceRegistrationImportPreview{}, invalid
	}
	if actor := financeUser(ctx); actor != nil && !actor.permitsRule(input.ProductID, true) {
		return practiceRegistrationImportPreview{}, errRulePermission
	}
	var result practiceRegistrationImportPreview
	err := pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var permitted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_mappings m JOIN products p ON p.id=m.product_id
		 WHERE p.id=$1 AND p.period_mode='class' AND m.source='DCZD' AND m.domain='PRACTICE' AND m.source_key=split_part($2,':',3))`,
			input.ProductID, doc.PeriodKey).Scan(&permitted); err != nil {
			return err
		}
		if !permitted {
			return errPracticeRosterProduct
		}
		if input.PeriodID != nil {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM periods p
			 LEFT JOIN LATERAL (
			  SELECT starts_at FROM practice_rosters WHERE period_key=p.source_period_key ORDER BY source_version DESC LIMIT 1
			 ) roster ON true
			 WHERE p.id=$1 AND p.product_id=$2 AND p.calendar_month IS NULL
			 AND split_part(p.source_period_key,':',2)=split_part($3,':',2) AND split_part(p.source_period_key,':',3)=split_part($3,':',3)
			 AND (p.source_period_key=$3 OR coalesce(roster.starts_at>$4::timestamptz,
			    p.starts_on>($4::timestamptz AT TIME ZONE 'Asia/Shanghai')::date)))`,
				*input.PeriodID, input.ProductID, doc.PeriodKey, doc.StartsAt).Scan(&permitted); err != nil {
				return err
			}
			if !permitted {
				return errPracticeRosterProduct
			}
		}
		var companies, sellers []string
		for _, row := range doc.Rows {
			companies = append(companies, row.SourceCompany)
			if !row.Pending {
				companies = append(companies, row.SourceMember)
			}
			if row.Recipient != "" && row.Recipient == row.LeaderID {
				sellers = append(sellers, row.SourceSeller)
			}
		}
		directory, err := readPracticeRegistrationCompanies(ctx, tx, companies, &doc.StartsAt)
		if err != nil {
			return err
		}
		recipients := make(map[string]int64)
		if len(sellers) != 0 {
			rows, err := tx.Query(ctx, `SELECT external_id,min(person_id) FROM identity_mappings WHERE source='DCZD'
			 AND external_id=ANY($1::text[]) AND valid_from<=$2 AND (valid_to IS NULL OR $2<valid_to)
			 GROUP BY external_id HAVING count(DISTINCT person_id)=1`, sellers, doc.StartsAt)
			if err != nil {
				return err
			}
			var seller string
			var id int64
			if _, err := pgx.ForEachRow(rows, []any{&seller, &id}, func() error { recipients[seller] = id; return nil }); err != nil {
				return err
			}
		}
		result = buildPracticeRegistrationImport(doc, input.Rate, directory, recipients)
		return nil
	})
	return result, err
}

func buildPracticeRegistrationImport(doc practiceRegistrationDocument, rate int64, companies map[string]string, people map[string]int64) practiceRegistrationImportPreview {
	out := practiceRegistrationImportPreview{PeriodName: doc.PeriodName, StartsAt: doc.StartsAt, Version: doc.Version, RowCount: len(doc.Rows)}
	type arrangement struct {
		row     practiceRegistrationImportRow
		benefit *practicePeriodBenefit
	}
	leaders := make(map[string]arrangement)
	numbers := make(map[int]bool)
	for _, row := range doc.Rows {
		issue := func(message string) {
			out.Issues = append(out.Issues, practiceRegistrationImportIssue{Number: row.Number, Message: message})
		}
		_, companyKnown := companies[row.SourceCompany]
		if row.Number < 1 || numbers[row.Number] || row.LeaderID == "" || row.LeaderCompany == "" ||
			positiveSourceID(row.SourceSeller) == 0 || positiveSourceID(row.SourceCompany) == 0 {
			issue("记录编号或本人销售归属不完整")
			continue
		}
		numbers[row.Number] = true
		if !companyKnown {
			issue("销售公司未关联：" + row.SourceCompany)
			continue
		}
		entry, exists := leaders[row.LeaderID]
		if !exists {
			name := strings.TrimSpace(row.LeaderName)
			if name == "" {
				name = row.SourceSeller
			}
			benefit := &practicePeriodBenefit{Label: name + " · 总监分润", RegistrationScope: &practiceRegistrationScope{
				SellerID: row.SourceSeller, SellerCompanyID: row.SourceCompany, Exclusive: true}}
			switch row.Recipient {
			case row.LeaderID:
				benefit.PersonID = people[row.SourceSeller]
			case row.LeaderCompany:
				benefit.UnitID = companies[row.SourceCompany]
			}
			if benefit.PersonID == 0 && benefit.UnitID == "" {
				issue("找不到唯一收款方，请核对人员账号或公司映射")
				continue
			}
			entry = arrangement{row, benefit}
			leaders[row.LeaderID] = entry
			out.Components = append(out.Components, component{Type: "manager", Mode: "period_rate", Value: rate, PeriodBenefit: benefit})
		} else if entry.row.SourceSeller != row.SourceSeller || entry.row.SourceCompany != row.SourceCompany ||
			entry.row.LeaderCompany != row.LeaderCompany || entry.row.Recipient != row.Recipient {
			issue("同一总监的销售归属或收款方有冲突")
			continue
		}
		if row.Pending {
			if row.SourceMember != "" {
				issue("待核记录仍有下属公司标识")
				continue
			}
			out.Pending = append(out.Pending, practiceRegistrationImportIssue{Number: row.Number, Message: row.MemberName})
			continue
		}
		_, known := companies[row.SourceMember]
		if !known || positiveSourceID(row.SourceMember) == 0 {
			issue(fmt.Sprintf("下属公司未关联：%s（%s）", row.MemberName, row.SourceMember))
			continue
		}
		entry.benefit.RegistrationScope.Companies = append(entry.benefit.RegistrationScope.Companies, row.SourceMember)
	}
	if len(out.Issues) == 0 {
		if err := validateComponents(out.Components); err != nil {
			out.Issues = append(out.Issues, practiceRegistrationImportIssue{Message: err.Error()})
		}
	}
	if len(out.Issues) != 0 {
		out.Components = nil
	}
	return out
}
