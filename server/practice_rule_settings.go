package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type practiceRuleSettings struct {
	ID                int64                      `json:"id"`
	Label             string                     `json:"label"`
	Version           int                        `json:"version"`
	PeriodID          *int64                     `json:"periodId,omitempty"`
	PeriodName        string                     `json:"periodName,omitempty"`
	FromOrdinal       *int                       `json:"fromOrdinal,omitempty"`
	EffectiveFrom     *time.Time                 `json:"effectiveFrom,omitempty"`
	ActivityType      int                        `json:"activityType,omitempty"`
	HasCount          bool                       `json:"hasCount"`
	PersonCountStarts []practicePersonCountStart `json:"personCountStarts,omitempty"`
	CountArrangements []practiceCountArrangement `json:"countArrangements,omitempty"`
	HasRetrain        bool                       `json:"hasRetrain"`
	RetrainTotal      int64                      `json:"retrainTotalMinor"`
	RetrainRate       *int64                     `json:"retrainRateBps,omitempty"`
	RetrainSplits     []practiceRetrainSplit     `json:"retrainSplits,omitempty"`
	SalesCompanies    []string                   `json:"salesCompanies,omitempty"`
}

type practiceCountArrangement struct {
	Member string                     `json:"member"`
	Label  string                     `json:"label,omitempty"`
	Starts []practicePersonCountStart `json:"personCountStarts,omitempty"`
}

func hasPracticeCountSettings(part component) bool {
	return part.Tiers != nil && part.Tiers.usesPriorTiers()
}

func isPracticeRetrainWithSplits(part component) bool {
	return part.Type == "sales" && (part.Mode == "fixed" || part.Mode == "rate") && part.ClassMember == "retrain"
}

const practiceRuleSettingsSQL = `WITH latest AS (
 SELECT DISTINCT ON (rv.period_id,rv.from_ordinal,rv.activity_type,rv.effective_from)
  rv.id,rv.period_id,rv.from_ordinal,rv.activity_type,rv.effective_from,rv.version,rv.label,rv.components
 FROM rule_versions rv JOIN products p ON p.id=rv.product_id
 WHERE rv.product_id=$1 AND p.period_mode='class' AND ` + componentRuleRetiredAtSQL + ` IS NULL
 ORDER BY rv.period_id,rv.from_ordinal,rv.activity_type,rv.effective_from,rv.version DESC
)
SELECT l.id,l.label,l.version,l.period_id,coalesce(p.name,''),l.from_ordinal,l.activity_type,l.effective_from,l.components
FROM latest l LEFT JOIN periods p ON p.id=l.period_id ORDER BY l.version DESC`

func readPracticeRuleSettings(ctx context.Context, db *pgxpool.Pool, productID int64) ([]practiceRuleSettings, error) {
	rows, err := db.Query(ctx, practiceRuleSettingsSQL, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]practiceRuleSettings, 0)
	for rows.Next() {
		var setting practiceRuleSettings
		var parts []component
		if err := rows.Scan(&setting.ID, &setting.Label, &setting.Version, &setting.PeriodID, &setting.PeriodName,
			&setting.FromOrdinal, &setting.ActivityType, &setting.EffectiveFrom, &parts); err != nil {
			return nil, err
		}
		for _, part := range parts {
			switch {
			case hasPracticeCountSettings(part):
				setting.CountArrangements = append(setting.CountArrangements,
					practiceCountArrangement{Member: part.ClassMember, Label: part.arrangementLabel(), Starts: part.Tiers.PersonCountStarts})
			case isPracticeRetrainWithSplits(part):
				setting.HasRetrain = true
				if part.Mode == "rate" {
					setting.RetrainRate = &part.Value
				} else {
					setting.RetrainTotal = part.Value
				}
				setting.RetrainSplits, setting.SalesCompanies = part.retrainSplits(), part.salesCompanies()
			}
		}
		setting.HasCount = len(setting.CountArrangements) != 0
		if len(setting.CountArrangements) == 1 {
			setting.PersonCountStarts = setting.CountArrangements[0].Starts
		}
		if setting.HasCount || setting.HasRetrain {
			items = append(items, setting)
		}
	}
	return items, rows.Err()
}

func (s workspace) getPracticeRuleSettings(w http.ResponseWriter, r *http.Request) {
	productID, err := strconv.ParseInt(r.URL.Query().Get("productId"), 10, 64)
	if err != nil || productID < 1 {
		respond(w, nil, invalid)
		return
	}
	if actor := financeUser(r.Context()); actor != nil && !actor.permitsRule(productID, false) {
		respond(w, nil, errFinanceForbidden)
		return
	}
	ctx := r.Context()
	rules, err := readPracticeRuleSettings(ctx, s.db, productID)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var people, pools []int64
	for _, rule := range rules {
		for _, arrangement := range rule.CountArrangements {
			for _, start := range arrangement.Starts {
				people = append(people, start.PersonID)
			}
		}
		for _, split := range rule.RetrainSplits {
			people = append(people, split.ServicePersonID, split.Pool.OwnerID)
			pools = append(pools, split.Pool.PoolID)
		}
	}
	directory, err := s.practiceRecipientNames(ctx, people, nil, pools)
	respond(w, struct {
		Rules     []practiceRuleSettings `json:"rules"`
		Directory json.RawMessage        `json:"directory,omitempty"`
	}{rules, directory}, err)
}

type savePracticeRuleSettingsInput struct {
	ProductID         int64
	RuleID            int64
	CountStart        *practicePersonCountStart
	CountMember       *string
	ExistingPersonID  int64
	RetrainSplit      *practiceRetrainSplit
	ExistingCompanyID string
}

var errPracticeServiceSeparate = errors.New("请在分润规则中单独配置服务提成")

func (s workspace) savePracticeRuleSettings(ctx context.Context, input savePracticeRuleSettingsInput) (any, error) {
	countChange := input.CountStart != nil || input.ExistingPersonID > 0
	splitChange := input.RetrainSplit != nil || input.ExistingCompanyID != ""
	if input.ProductID < 1 || input.RuleID < 1 || input.ExistingPersonID < 0 || countChange == splitChange {
		return nil, invalid
	}
	actor := financeUser(ctx)
	if actor != nil && !actor.permitsRule(input.ProductID, true) {
		return nil, errFinanceForbidden
	}
	var version int
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if actor != nil {
			if err := lockFinanceOperatorSession(ctx, tx, actor); err != nil {
				return err
			}
		}
		// The product lock also serializes retirement and publication from the full rule editor.
		var productID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM products WHERE id=$1 AND period_mode='class' FOR UPDATE`, input.ProductID).Scan(&productID); err != nil {
			return err
		}
		pub := publishRuleInput{ProductID: productID, EditBaseID: &input.RuleID}
		var retired *time.Time
		if err := tx.QueryRow(ctx, `SELECT rv.label,rv.components,`+componentRuleRetiredAtSQL+`
		 FROM rule_versions rv WHERE rv.id=$1 AND rv.product_id=$2`, input.RuleID, productID).
			Scan(&pub.Label, &pub.Components, &retired); err != nil {
			return err
		}
		if retired != nil {
			return errRuleEditStale
		}
		if err := editPracticeRuleSettings(&pub, input, countChange); err != nil {
			return err
		}
		if err := validateRulePublication(pub); err != nil {
			return err
		}
		var err error
		version, err = writeComponentRule(ctx, tx, pub, true)
		return err
	})
	return map[string]int{"version": version}, err
}

func replacePracticeRuleSetting[T any](rows []T, next *T, matches func(T) bool, editing bool) ([]T, error) {
	index := slices.IndexFunc(rows, matches)
	if editing {
		if index == -1 {
			return nil, errRuleEditStale
		}
		if next == nil {
			return slices.Delete(rows, index, index+1), nil
		}
		rows[index] = *next
		return rows, nil
	}
	if index != -1 {
		return nil, conflict
	}
	return append(rows, *next), nil
}

func editPracticeRuleSettings(pub *publishRuleInput, input savePracticeRuleSettingsInput, countChange bool) error {
	if countChange {
		var tiers *practiceSalesTiers
		for _, part := range pub.Components {
			if !hasPracticeCountSettings(part) || input.CountMember != nil && part.ClassMember != *input.CountMember {
				continue
			}
			if tiers != nil {
				return errors.New("请选择要设置累计起点的收款安排")
			}
			tiers = part.Tiers
		}
		if tiers == nil {
			return invalid
		}
		person := input.ExistingPersonID
		editing := person != 0
		if start := input.CountStart; start != nil {
			if editing && start.PersonID != person {
				return invalid
			}
			person = start.PersonID
		}
		rows, err := replacePracticeRuleSetting(tiers.PersonCountStarts, input.CountStart,
			func(row practicePersonCountStart) bool { return row.PersonID == person }, editing)
		tiers.PersonCountStarts = rows
		return err
	}
	i := slices.IndexFunc(pub.Components, isPracticeRetrainWithSplits)
	if i == -1 {
		return invalid
	}
	company := input.ExistingCompanyID
	editing := company != ""
	part := &pub.Components[i]
	split := input.RetrainSplit
	if split != nil {
		if editing && split.CompanyID != company {
			return invalid
		}
		company = split.CompanyID
		if split.ServicePersonID != 0 || split.ServiceMinor != 0 || split.ServiceRate != nil ||
			split.ServiceLabel != "" || len(split.ServiceSalesPeople)+len(split.ServiceSalesUnits) != 0 {
			return errPracticeServiceSeparate
		}
		if editing {
			for _, saved := range part.retrainSplits() {
				if saved.CompanyID == company {
					next := *split
					next.ServicePersonID, next.ServiceMinor, next.ServiceRate = saved.ServicePersonID, saved.ServiceMinor, saved.ServiceRate
					next.ServiceLabel, next.ServiceSalesPeople, next.ServiceSalesUnits = saved.ServiceLabel, saved.ServiceSalesPeople, saved.ServiceSalesUnits
					split = &next
					break
				}
			}
		}
	}
	rows, err := replacePracticeRuleSetting(part.retrainSplits(), split,
		func(row practiceRetrainSplit) bool { return row.CompanyID == company }, editing)
	if len(rows) > 0 {
		part.RetrainSplits = &rows
	} else {
		part.RetrainSplits = nil
	}
	return err
}
