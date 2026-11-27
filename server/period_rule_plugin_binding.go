package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

const periodRulePluginPackageCode = "period-rule-plugins"

type periodRulePluginInput struct {
	PeriodID      int64 `json:"periodId"`
	RuleVersionID int64 `json:"ruleVersionId"`
}

func (s workspace) attachPeriodRulePlugin(ctx context.Context, input periodRulePluginInput) (map[string]int64, error) {
	result := map[string]int64{"periodId": input.PeriodID, "ruleVersionId": input.RuleVersionID}
	if input.PeriodID < 1 || input.RuleVersionID < 1 {
		return nil, invalid
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var product int64
		var mode string
		var day *int
		if err := tx.QueryRow(ctx, `SELECT pro.id,pro.period_mode,pro.cycle_start_day FROM periods p
 JOIN products pro ON pro.id=p.product_id WHERE p.id=$1 FOR UPDATE OF pro`, input.PeriodID).Scan(&product, &mode, &day); err != nil {
			return err
		}
		actor := financeRuleActor{subject: "local-development", permits: func(int64, bool) bool { return s.development }}
		if p := financeUser(ctx); p != nil {
			if !p.Workspace || !p.permitsRule(product, true) {
				return errRulePermission
			}
			if err := lockCatalogProductPublisher(ctx, tx, p, product); err != nil {
				return err
			}
			actor = financeRuleActor{account: p, subject: p.ID, permits: p.permitsRule}
		} else if !s.development {
			return errRulePermission
		}
		var selected *int64
		var aggregate *string
		var calendarMonth *time.Time
		var done, draft bool
		if err := tx.QueryRow(ctx, `SELECT p.rule_plugin_version_id,p.aggregate_package_id::text,p.calendar_month,
 EXISTS(SELECT 1 FROM settlements WHERE period_id=p.id),EXISTS(SELECT 1 FROM calculation_drafts WHERE period_id=p.id)
 FROM periods p WHERE p.id=$1 FOR UPDATE OF p`, input.PeriodID).Scan(&selected, &aggregate, &calendarMonth, &done, &draft); err != nil {
			return err
		}
		if selected != nil && *selected == input.RuleVersionID {
			return nil
		}
		if selected != nil || done || aggregate != nil || mode == "calendar" && draft {
			return errPeriodRulePluginLocked
		}
		var rule rulePluginVersion
		if err := tx.QueryRow(ctx, `SELECT id,plugin_id,version,label,arrangements FROM finance_rule_plugin_versions WHERE id=$1`,
			input.RuleVersionID).Scan(&rule.ID, &rule.PluginID, &rule.Version, &rule.Label, &rule.Arrangements); err != nil {
			return err
		}
		if slices.ContainsFunc(rule.Arrangements, func(row rulePluginArrangement) bool { return len(row.InternalSplit) != 0 }) {
			return errRulePluginLegacySplit
		}
		if mode == "calendar" {
			if day == nil || calendarMonth == nil || (*day != 1 && *day != 25) {
				return errCalendarPeriodUnavailable
			}
			return attachCalendarRulePlugin(ctx, tx, input.PeriodID, product, *day, *calendarMonth, rule, actor)
		}
		if mode != "class" || calendarMonth != nil {
			return invalid
		}
		if slices.ContainsFunc(rule.Arrangements, func(row rulePluginArrangement) bool {
			return row.OrderAppointment != nil || row.Appointment != nil && (row.Appointment.Scope == "PROJECT" || row.Appointment.Scope == "PRODUCT")
		}) {
			return errCatalogPerformanceScope
		}
		publication := publishRuleInput{ProductID: product, Label: rule.Label, PeriodID: &input.PeriodID, Components: practicePluginComponents(rule.Arrangements)}
		if err := validateRulePublication(publication); err != nil {
			return err
		}
		if _, err := writeComponentRule(ctx, tx, publication, true); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE periods SET rule_plugin_version_id=$2 WHERE id=$1`, input.PeriodID, rule.ID)
		return err
	})
	return result, err
}

func practicePluginComponents(rows []rulePluginArrangement) []component {
	components := make([]component, 0, len(rows))
	for _, row := range rows {
		if row.PracticeSales != nil {
			components = append(components, practicePluginSales(row))
			continue
		}
		if row.PracticeCompany != nil {
			components = append(components, practicePluginCompany(row))
			continue
		}
		if len(row.PracticeRetrain) != 0 {
			components = append(components, practicePluginRetrain(row))
			continue
		}
		benefit := &practicePeriodBenefit{Label: row.Label, LabelIsCustom: true, CalculationGrain: row.Grain,
			SalesUnits: row.SalesUnits, SalesCategoryIDs: row.SalesCategories, SalesPeople: row.SalesPeople,
			PersonID: row.PersonID, UnitID: row.UnitID}
		switch row.Target {
		case "pool":
			benefit.Pool = &practicePoolTarget{PoolID: row.PoolID}
		case "order_seller":
			benefit.OrderRecipientRole = "SELLER"
		}
		if row.Appointment != nil {
			benefit.AppointmentUnitID, benefit.AppointmentKind = row.Appointment.UnitID, row.Appointment.Role
		}
		part := component{Type: "manager", Mode: "period_rate", SourceRole: "SELLER", PeriodBenefit: benefit}
		part.ClassMember = row.SignupType
		if row.RegistrationOnly {
			part.ClassMember = "new"
		}
		switch row.Method {
		case "fixed":
			benefit.FixedMinor = row.Fixed
			benefit.FixedPerClass = row.Grain == "period"
			part.PracticeSignupBasis = row.FixedBasis == "signup"
			benefit.FixedPerOrder = row.Grain == "payment" && !part.PracticeSignupBasis
		case "rate":
			if len(row.RateOverrides) == 0 {
				part.Value = *row.Rate
			} else {
				benefit.StandardRates = []practicePeriodStandardRate{{Value: *row.Rate}}
				for _, override := range row.RateOverrides {
					standard := practicePeriodStandardRate{Value: override.Rate}
					if override.CompanyID != "" {
						standard.SalesUnits = []string{override.CompanyID}
					} else {
						standard.SalesCategoryIDs = []string{override.CategoryID}
					}
					benefit.StandardRates = append(benefit.StandardRates, standard)
				}
			}
		case "tiers":
			benefit.TierOnWholeClass = row.TierScope == "class_net"
			for _, tier := range row.Tiers {
				var through *int64
				if tier.Until != nil {
					last := *tier.Until - 1
					through = &last
				}
				benefit.Tiers = append(benefit.Tiers, practicePeriodRate{From: tier.From, Through: through, Value: tier.Rate})
			}
		}
		if row.SignupType == practiceSplitSignupTypes {
			part.ClassMember = "new"
			components = append(components, part)
			part.ClassMember = "retrain"
		}
		components = append(components, part)
	}
	return components
}

func practicePluginCompany(row rulePluginArrangement) component {
	return component{Type: "company_sales", Mode: "company_sales", ClassMember: row.SignupType, SourceRole: "SELLER",
		CompanySales: row.PracticeCompany, PracticeSignupBasis: true}
}

func practicePluginRetrain(row rulePluginArrangement) component {
	companies := make([]string, 0, len(row.PracticeRetrain))
	for _, split := range row.PracticeRetrain {
		companies = append(companies, split.CompanyID)
	}
	return component{Type: "sales", Mode: "fixed", Value: *row.Fixed, ClassMember: "retrain", SourceRole: "SELLER",
		ArrangementLabel: row.Label, SalesCompanies: &companies, RetrainSplits: &row.PracticeRetrain, PracticeSignupBasis: true}
}

func practicePluginSales(row rulePluginArrangement) component {
	tiers := row.PracticeSales
	part := component{Type: "sales", Mode: "tiered", ClassMember: "new", SourceRole: "SELLER",
		ArrangementLabel: row.Label, Tiers: tiers, PracticeSignupBasis: true}
	if len(tiers.Performance) == 0 {
		var companies []string
		for _, schedule := range tiers.CompanySchedules {
			companies = append(companies, schedule.companyIDs()...)
		}
		part.SalesCompanies = &companies
	}
	return part
}

func validPracticePluginSales(row rulePluginArrangement) bool {
	tiers := row.PracticeSales
	if tiers.IncludeRetrain || tiers.CumulativeMetric != "" || len(tiers.CategorySchedules)+len(tiers.PersonSchedules) != 0 ||
		len(tiers.CompanySchedules) > 30 || !validPracticeSalesTiers(practicePluginSales(row)) {
		return false
	}
	historical := tiers.hasCountTiers()
	var threshold int64
	validSchedule := func(count, performance []practiceTier) bool {
		if len(performance) == 0 || len(performance) > 30 || performance[0].From != 0 ||
			historical && len(count) != 1 || !historical && len(count) != 0 {
			return false
		}
		for i, tier := range performance {
			if tier.Mode != "rate" || (i == len(performance)-1 && tier.Through != nil) ||
				(i < len(performance)-1 && (tier.Through == nil || *tier.Through != performance[i+1].From-1)) {
				return false
			}
		}
		if historical {
			first := count[0]
			if threshold == 0 {
				threshold = first.From
			}
			return first.From > 0 && first.From == threshold && first.Mode == "rate" && first.Through == nil
		}
		return true
	}
	if len(tiers.Count)+len(tiers.Performance) != 0 && !validSchedule(tiers.Count, tiers.Performance) {
		return false
	}
	for _, schedule := range tiers.CompanySchedules {
		if !validSchedule(schedule.Count, schedule.Performance) {
			return false
		}
	}
	return true
}

func calendarPluginComponents(rows []rulePluginArrangement) []catalogPeriodComponentEdit {
	components := make([]catalogPeriodComponentEdit, 0, len(rows))
	for _, row := range rows {
		edit := catalogPeriodComponentEdit{Label: row.Label, TargetKind: row.Target, PersonID: row.PersonID, PoolID: row.PoolID,
			PerOrder: row.Grain == "payment", CommissionUnits: row.SalesUnits, ScopeCategories: row.SalesCategories,
			Appointment: row.Appointment}
		if duty := row.OrderAppointment; duty != nil {
			edit.TargetKind, edit.AppointmentLevel, edit.AppointmentRole = "appointment", duty.Level, duty.Role
		}
		if row.UnitID != "" {
			edit.OrganizationKey = catalogPeriodUnitPrefix + row.UnitID
		}
		if row.RegistrationOnly || row.SignupType == "new" {
			only := true
			edit.MembershipNewOnly = &only
		}
		switch row.Method {
		case "fixed":
			edit.FixedMinor = row.Fixed
		case "rate":
			edit.Tiers = []catalogPeriodTier{{Rate: (*row.Rate + 50) / 100}}
		case "tiers":
			for _, tier := range row.Tiers {
				tier.Rate = (tier.Rate + 50) / 100
				edit.Tiers = append(edit.Tiers, tier)
			}
		}
		components = append(components, edit)
	}
	return components
}

func attachCalendarRulePlugin(ctx context.Context, tx pgx.Tx, period, product int64, day int, month time.Time, rule rulePluginVersion, actor financeRuleActor) error {
	var companies, categories []string
	for _, row := range rule.Arrangements {
		if row.practiceOnly() {
			return errRulePluginPeriodBasis
		}
		if row.Rate != nil && *row.Rate%100 != 0 {
			return errRulePluginRatePrecision
		}
		for _, tier := range row.Tiers {
			if tier.Rate%100 != 0 {
				return errRulePluginRatePrecision
			}
		}
		for _, override := range row.RateOverrides {
			if override.Rate%100 != 0 {
				return errRulePluginRatePrecision
			}
			if override.CompanyID != "" {
				companies = append(companies, override.CompanyID)
			} else {
				categories = append(categories, override.CategoryID)
			}
		}
	}
	if len(companies)+len(categories) != 0 {
		var valid bool
		err := tx.QueryRow(ctx, `SELECT NOT EXISTS(
 SELECT 1 FROM unnest($1::text[]) AS c(id)
 WHERE NOT EXISTS(SELECT 1 FROM organization_units u WHERE u.id=c.id::uuid AND u.kind='COMPANY' AND u.status='ACTIVE')
 UNION ALL SELECT 1 FROM unnest($2::text[]) AS c(id)
 WHERE NOT EXISTS(SELECT 1 FROM catalog_sales_category_options o WHERE o.key=c.id AND o.active))`, companies, categories).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return errCatalogCommissionScope
		}
	}
	cycle := "MONTHLY"
	if day == 25 {
		cycle = "MONTHLY_25_TO_24"
	}
	window, err := calendarWindow(month.Format("2006-01"), day)
	if err != nil {
		return err
	}
	var matches bool
	if err := tx.QueryRow(ctx, `SELECT starts_on=$2::date AND ends_on=$3::date FROM periods WHERE id=$1`,
		period, window.Start.Format(time.DateOnly), window.Through.AddDate(0, 0, -1).Format(time.DateOnly)).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return errCalendarPeriodUnavailable
	}
	options, err := readCatalogPeriodScenarioOptions(ctx, tx, product, &window.Start, &window.Through)
	if err != nil {
		return err
	}
	codes := make([]string, 0, len(options))
	for _, option := range options {
		if option.forMixedProduct(product) {
			codes = append(codes, option.Code)
		}
	}
	components := calendarPluginComponents(rule.Arrangements)
	for i, row := range components {
		if duty := row.Appointment; duty != nil && (duty.Scope == "PRODUCT" || duty.Scope == "PROJECT") {
			resolved := *duty
			if err := tx.QueryRow(ctx, `SELECT CASE WHEN $2='PRODUCT' THEN p.code ELSE s.project_code END
			 FROM products p JOIN product_catalog_settings s ON s.product_id=p.id WHERE p.id=$1`, product, duty.Scope).Scan(&resolved.ScopeKey); err != nil {
				return err
			}
			components[i].Appointment = &resolved
		}
	}
	body, err := buildCatalogPeriodRule(ctx, tx, catalogPeriodCreate{ProductID: product, Label: rule.Label, Cycle: cycle, Mixed: true},
		window.Start, codes, components, nil, actor)
	if err != nil {
		return err
	}
	body, err = identifyPluginArrangements(body, rule)
	if err != nil {
		return err
	}

	// A product keeps one posting history across months so refunds and carryovers can match their original postings.
	var packageID string
	err = tx.QueryRow(ctx, `SELECT source_id::text FROM product_rule_packages WHERE product_id=$1 AND code=$2 FOR UPDATE`,
		product, periodRulePluginPackageCode).Scan(&packageID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO product_rule_packages(source_id,product_id,code,name,project_code,policy_kind,scope_type,scope_id,paused)
 SELECT id,s.product_id,$2,'期次关联规则',s.project_code,'COMPONENT_RULE','CROSS_SCENARIO_AGGREGATE',id::text,false
 FROM product_catalog_settings s CROSS JOIN (SELECT gen_random_uuid() id) key WHERE s.product_id=$1 AND s.source_system='DCAI'
 RETURNING source_id::text`, product, periodRulePluginPackageCode).Scan(&packageID)
	}
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	if _, err := tx.Exec(ctx, `INSERT INTO product_rule_versions(source_id,package_id,version_no,source_status,effective_from,effective_to,
 content_hash,definition,observed_at,published_at,published_by)
 SELECT gen_random_uuid(),$1::uuid,coalesce((SELECT max(version_no) FROM product_rule_versions WHERE package_id=$1::uuid),0)+1,
 'PUBLISHED',$2,$3,$4,$5,now(),now(),$6`,
		packageID, window.Start, window.Through, hex.EncodeToString(digest[:]), body, actor.subject); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE periods SET calendar_month=NULL,report_month=$2,aggregate_package_id=$3::uuid,rule_plugin_version_id=$4
 WHERE id=$1`, period, month, packageID, rule.ID); err != nil {
		return err
	}
	return enqueueDraftPeriods(ctx, tx, draftRefreshNewAggregatePeriod, period)
}

func identifyPluginArrangements(body json.RawMessage, rule rulePluginVersion) (json.RawMessage, error) {
	var root map[string]json.RawMessage
	var actions []map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || json.Unmarshal(root["benefit_actions"], &actions) != nil || len(actions) != len(rule.Arrangements) {
		return nil, errRulePluginInvalid
	}
	for i, row := range rule.Arrangements {
		key := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", rule.PluginID, row.Key)))
		actions[i]["item_key"], _ = json.Marshal("plugin-" + hex.EncodeToString(key[:16]))
		if len(row.RateOverrides) != 0 {
			actions[i]["sales_rate_overrides"], _ = json.Marshal(row.RateOverrides)
		}
		if row.OrderAppointment != nil {
			actions[i]["order_appointment_scope"], _ = json.Marshal(row.OrderAppointment)
		}
	}
	root["period_rule_plugin"] = json.RawMessage("true")
	root["rounding_policy"] = json.RawMessage(`{"mode":"HALF_UP","scale":2}`)
	root["benefit_actions"], _ = json.Marshal(actions)
	return json.Marshal(root)
}
