package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	errRulePluginRatePrecision = errors.New("大春 AI 计提比例最多保留两位小数")
	errRulePluginInvalid       = errors.New("请核对规则名称和计提项")
	errRulePluginPeriodBasis   = errors.New("规则包含实战班专用计提项")
	errRulePluginLegacySplit   = errors.New("请移除旧的内部销售拆分项，将销售和岗位提成分别配置")
	errRulePluginChanged       = errors.New("规则已有更新，请刷新后再编辑")
	errPeriodRulePluginLocked  = errors.New("本期规则已锁定")
)

type rulePluginArrangement struct {
	Key              string                      `json:"key"`
	Label            string                      `json:"label"`
	Target           string                      `json:"targetKind"`
	PersonID         int64                       `json:"personId,omitempty"`
	UnitID           string                      `json:"unitId,omitempty"`
	PoolID           int64                       `json:"poolId,omitempty"`
	PoolRole         string                      `json:"poolRole,omitempty"`
	Method           string                      `json:"method"`
	Grain            string                      `json:"grain"`
	RateOverrides    []rulePluginRateOverride    `json:"rateOverrides,omitempty"`
	Rate             *int64                      `json:"rate,omitempty"`
	Fixed            *int64                      `json:"fixedMinor,omitempty"`
	Tiers            []catalogPeriodTier         `json:"tiers,omitempty"`
	SignupType       string                      `json:"signupType,omitempty"`
	FixedBasis       string                      `json:"fixedBasis,omitempty"`
	TierScope        string                      `json:"tierScope,omitempty"`
	PracticeSales    *practiceSalesTiers         `json:"practiceSales,omitempty"`
	PracticeCompany  *practiceCompanySales       `json:"practiceCompanySales,omitempty"`
	PracticeRetrain  []practiceRetrainSplit      `json:"practiceRetrainSplits,omitempty"`
	InternalSplit    json.RawMessage             `json:"internalSplit,omitempty"`
	RegistrationOnly bool                        `json:"newOnly,omitempty"`
	SalesUnits       []string                    `json:"salesUnits,omitempty"`
	SalesCategories  []string                    `json:"salesCategories,omitempty"`
	SalesPeople      []int64                     `json:"salesPeople,omitempty"`
	Appointment      *catalogPerformanceDuty     `json:"appointment,omitempty"`
	OrderAppointment *rulePluginOrderAppointment `json:"orderAppointment,omitempty"`
}

const practiceSplitSignupTypes = "new_retrain"

func (row rulePluginArrangement) practiceOnly() bool {
	return row.SignupType == "retrain" || row.SignupType == practiceSplitSignupTypes ||
		row.FixedBasis == "signup" || row.TierScope == "class_net" || row.PracticeSales != nil ||
		row.PracticeCompany != nil || len(row.PracticeRetrain)+len(row.SalesPeople) != 0
}

type rulePluginOrderAppointment struct {
	Level          string   `json:"level"`
	Role           string   `json:"role"`
	Groups         []string `json:"groupIds,omitempty"`
	SkipSellerHead bool     `json:"skipSellerHead,omitempty"`
}

func (a rulePluginOrderAppointment) valid() bool {
	return validVIPSalesOrganizationLevel(a.Level) && validSourceRole(a.Role) &&
		(len(a.Groups) == 0 && !a.SkipSellerHead || a.Level == "TEAM" && len(a.Groups) <= 50 && validUniqueScopeIDs(a.Groups))
}

type rulePluginRateOverride struct {
	CompanyID  string `json:"companyId,omitempty"`
	CategoryID string `json:"categoryId,omitempty"`
	Rate       int64  `json:"rate"`
}

func validPluginRateOverrides(rows []rulePluginRateOverride) bool {
	if len(rows) > 500 {
		return false
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if (row.CompanyID == "") == (row.CategoryID == "") || row.Rate < 0 || row.Rate > periodRateScale {
			return false
		}
		id := row.CompanyID + row.CategoryID
		key := row.CompanyID + ":" + row.CategoryID
		if canonical, ok := canonicalSourceUUID(id); !ok || canonical != id || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

type rulePluginInput struct {
	BaseID       int64                   `json:"baseId,omitempty"`
	Label        string                  `json:"label"`
	Arrangements []rulePluginArrangement `json:"arrangements"`
}

type rulePluginVersion struct {
	ID           int64                   `json:"id"`
	PluginID     int64                   `json:"pluginId"`
	Version      int                     `json:"version"`
	Label        string                  `json:"label"`
	Arrangements []rulePluginArrangement `json:"arrangements"`
}

func (s workspace) canManageRulePlugins(ctx context.Context) bool {
	if p := financeUser(ctx); p != nil {
		return p.Workspace && slices.ContainsFunc(p.Rules, func(g financeRuleGrant) bool { return g.Publish })
	}
	return s.development
}

func (s workspace) rulePluginList(w http.ResponseWriter, r *http.Request) {
	if !s.canManageRulePlugins(r.Context()) {
		respond(w, nil, errRulePermission)
		return
	}
	s.listForAuthorizedProducts(`SELECT coalesce(jsonb_agg(jsonb_build_object(
 'id',latest.id,'pluginId',latest.plugin_id,'version',latest.version,'label',latest.label,'arrangements',latest.arrangements,
 'products',(SELECT coalesce(jsonb_agg(jsonb_build_object('id',pro.id,'name',pro.name) ORDER BY pro.name,pro.id),'[]')
 FROM products pro WHERE ($1::bigint[] IS NULL OR pro.id=ANY($1)) AND EXISTS (
 SELECT 1 FROM periods p JOIN finance_rule_plugin_versions v ON v.id=p.rule_plugin_version_id
 WHERE p.product_id=pro.id AND v.plugin_id=latest.plugin_id))
 ) ORDER BY latest.label,latest.id),'[]')
 FROM (SELECT DISTINCT ON (plugin_id) * FROM finance_rule_plugin_versions ORDER BY plugin_id,version DESC) latest`, false)(w, r)
}

func (s workspace) rulePluginOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.canManageRulePlugins(ctx) {
		respond(w, nil, errRulePermission)
		return
	}
	options, err := readCatalogPeriodStructureOptions(ctx, s.db, 0)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var pools json.RawMessage
	err = s.db.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object('id',p.id,'name',p.name,'unitName',u.name,'productName',pro.name)
 ORDER BY pro.name,p.name,p.id),'[]') FROM practice_pools p JOIN products pro ON pro.id=p.product_id
 JOIN organization_units u ON u.id=p.unit_id WHERE u.status='ACTIVE' AND ($1::bigint[] IS NULL OR p.product_id=ANY($1))`,
		authorizedProductScope(ctx, false)).Scan(&pools)
	respond(w, map[string]any{"organizations": options.SalesUnits, "roles": options.Roles, "categories": options.Categories, "teamPools": pools}, err)
}

func validateRulePlugin(input *rulePluginInput) error {
	input.Label = strings.TrimSpace(input.Label)
	if !textOK(input.Label, 80) || len(input.Arrangements) == 0 || len(input.Arrangements) > 50 || input.BaseID < 0 {
		return errRulePluginInvalid
	}
	keys := make(map[string]bool)
	var practiceSales bool
	for i := range input.Arrangements {
		row := &input.Arrangements[i]
		row.Label = strings.TrimSpace(row.Label)
		if !textOK(row.Key, 80) || keys[row.Key] || !textOK(row.Label, 80) {
			return errRulePluginInvalid
		}
		keys[row.Key] = true
		if len(row.InternalSplit) != 0 {
			return errRulePluginLegacySplit
		}
		if row.Grain != "payment" && row.Grain != "period" {
			return errRulePluginInvalid
		}
		if row.PracticeCompany != nil {
			if row.Target != "company" || row.Grain != "payment" || row.Method != "rate" ||
				(row.SignupType != "new" && row.SignupType != "retrain") ||
				row.UnitID != "" || row.PersonID != 0 || row.PoolID != 0 || row.PoolRole != "" ||
				row.Appointment != nil || row.OrderAppointment != nil || row.RegistrationOnly ||
				row.FixedBasis != "" || row.TierScope != "" ||
				row.Rate != nil || row.Fixed != nil || row.PracticeSales != nil ||
				len(row.Tiers)+len(row.RateOverrides)+len(row.PracticeRetrain)+len(row.SalesUnits)+len(row.SalesCategories)+len(row.SalesPeople) != 0 {
				return errRulePluginInvalid
			}
			row.PracticeCompany.Label = row.Label
			if !validPracticeCompanySales(practicePluginCompany(*row)) {
				return errRulePluginInvalid
			}
			continue
		}
		switch row.Target {
		case "person":
			if row.UnitID != "" || row.PoolID != 0 || (row.PersonID > 0) == (row.OrderAppointment != nil) {
				return errRulePluginInvalid
			}
		case "company":
			if _, ok := canonicalSourceUUID(row.UnitID); !ok || row.PersonID != 0 || row.PoolID != 0 {
				return errRulePluginInvalid
			}
		case "pool":
			if row.PoolID <= 0 || row.PersonID != 0 || row.UnitID != "" {
				return errRulePluginInvalid
			}
			row.PoolRole = ""
		case "order_seller":
			if row.PersonID != 0 || row.UnitID != "" || row.PoolID != 0 {
				return errRulePluginInvalid
			}
		default:
			return errRulePluginInvalid
		}
		if row.OrderAppointment != nil && (row.Target != "person" || row.PersonID != 0 || row.Appointment != nil ||
			!row.OrderAppointment.valid()) {
			return errRulePluginInvalid
		}
		if row.Appointment != nil {
			row.Appointment.PersonID = row.PersonID
			if row.Target != "person" || !row.Appointment.valid(false) {
				return errRulePluginInvalid
			}
		}
		if row.SignupType != "" && row.SignupType != "new" && row.SignupType != "retrain" && row.SignupType != practiceSplitSignupTypes ||
			row.RegistrationOnly && (row.SignupType == "retrain" || row.SignupType == practiceSplitSignupTypes) ||
			row.FixedBasis != "" && (row.FixedBasis != "signup" || row.Method != "fixed" || row.Grain != "payment") ||
			row.TierScope != "" && (row.TierScope != "class_net" || row.Method != "tiers" || row.Grain != "period") {
			return errRulePluginInvalid
		}
		for j, person := range row.SalesPeople {
			if person <= 0 || slices.Contains(row.SalesPeople[:j], person) {
				return errRulePluginInvalid
			}
		}
		if len(row.PracticeRetrain) != 0 {
			if row.SignupType != "retrain" || row.Target != "order_seller" || row.Method != "fixed" ||
				row.FixedBasis != "signup" || row.Fixed == nil || !validCatalogPeriodFixed(row.Fixed) ||
				row.Rate != nil || row.PracticeSales != nil ||
				row.Appointment != nil || row.OrderAppointment != nil ||
				len(row.Tiers)+len(row.RateOverrides)+len(row.SalesUnits)+len(row.SalesCategories)+len(row.SalesPeople) != 0 ||
				!validPracticeRetrainSplits(practicePluginRetrain(*row)) {
				return errRulePluginInvalid
			}
			continue
		}
		if len(row.RateOverrides) != 0 && (row.Method != "rate" || row.Grain != "payment" || row.Target != "order_seller" || !validPluginRateOverrides(row.RateOverrides)) {
			return errRulePluginInvalid
		}
		if row.PracticeSales != nil {
			if practiceSales || row.Target != "order_seller" || row.Method != "tiers" || row.Grain != "payment" || row.SignupType != "new" ||
				row.RegistrationOnly || row.Rate != nil || row.Fixed != nil || len(row.Tiers)+len(row.SalesUnits)+len(row.SalesCategories)+len(row.SalesPeople) != 0 ||
				!validPracticePluginSales(*row) {
				return errRulePluginInvalid
			}
			practiceSales = true
			continue
		}
		switch row.Method {
		case "rate":
			if row.Rate == nil || *row.Rate < 0 || *row.Rate > periodRateScale || row.Fixed != nil || len(row.Tiers) != 0 {
				return errRulePluginInvalid
			}
		case "fixed":
			if !validCatalogPeriodFixed(row.Fixed) || row.Rate != nil || len(row.Tiers) != 0 {
				return errRulePluginInvalid
			}
		case "tiers":
			if len(row.Tiers) < 2 || len(row.Tiers) > 30 || row.Fixed != nil || row.Rate != nil || row.Tiers[0].From != 0 {
				return errRulePluginInvalid
			}
			for n, tier := range row.Tiers {
				if tier.From < 0 || tier.Rate < 0 || tier.Rate > periodRateScale ||
					(n == len(row.Tiers)-1 && tier.Until != nil) ||
					(n < len(row.Tiers)-1 && (tier.Until == nil || *tier.Until <= tier.From || *tier.Until != row.Tiers[n+1].From)) {
					return errRulePluginInvalid
				}
			}
		default:
			return errRulePluginInvalid
		}
	}
	return nil
}

func (s workspace) saveRulePlugin(ctx context.Context, input rulePluginInput) (rulePluginVersion, error) {
	if !s.canManageRulePlugins(ctx) {
		return rulePluginVersion{}, errRulePermission
	}
	if err := validateRulePlugin(&input); err != nil {
		return rulePluginVersion{}, err
	}
	result := rulePluginVersion{Version: 1, Label: input.Label, Arrangements: input.Arrangements}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if actor := financeUser(ctx); actor != nil {
			if err := lockFinanceOperatorSession(ctx, tx, actor); err != nil {
				return err
			}
		}
		if input.BaseID == 0 {
			if err := tx.QueryRow(ctx, `INSERT INTO finance_rule_plugins DEFAULT VALUES RETURNING id`).Scan(&result.PluginID); err != nil {
				return err
			}
		} else {
			var base, latest int
			if err := tx.QueryRow(ctx, `SELECT p.id,v.version FROM finance_rule_plugin_versions v JOIN finance_rule_plugins p ON p.id=v.plugin_id
 WHERE v.id=$1 FOR UPDATE OF p`, input.BaseID).Scan(&result.PluginID, &base); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT max(version) FROM finance_rule_plugin_versions WHERE plugin_id=$1`, result.PluginID).Scan(&latest); err != nil {
				return err
			}
			if base != latest {
				return errRulePluginChanged
			}
			result.Version = latest + 1
		}
		return tx.QueryRow(ctx, `INSERT INTO finance_rule_plugin_versions(plugin_id,version,label,arrangements) VALUES($1,$2,$3,$4)
 RETURNING id`, result.PluginID, result.Version, result.Label, result.Arrangements).Scan(&result.ID)
	})
	return result, err
}
