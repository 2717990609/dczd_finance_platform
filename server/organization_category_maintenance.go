package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	errOrganizationCategoryChanged = errors.New("销售类别已变化，请重新读取后再保存")
	errOrganizationCategoryInUse   = errors.New("仍有启用公司使用该销售类别，请先调整公司的类别")
	errOrganizationCategoryName    = errors.New("已有同名的启用销售类别")
	errOrganizationCategoryTarget  = errors.New("请选择启用的销售类别及销售公司")
)

const organizationCategoryListSQL = `SELECT coalesce(jsonb_agg(
 jsonb_build_object('id',id,'name',name,'active',active,'version',version) ORDER BY active DESC,name,id),'[]')
 FROM organization_sales_categories`

type organizationCategoryInput struct {
	Name string `json:"name"`
}

type updateOrganizationCategoryInput struct {
	ID              string `json:"id"`
	ExpectedVersion int64  `json:"expectedVersion"`
	Name            string `json:"name"`
	Active          bool   `json:"active"`
}

type organizationCategory struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Version int64  `json:"version"`
}

func (s workspace) writeOrganizationCategory(ctx context.Context, apply func(pgx.Tx, *string) error) error {
	return s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(637619251)"); err != nil {
			return err
		}
		return apply(tx, by)
	})
}

func checkOrganizationCategoryName(ctx context.Context, tx pgx.Tx, name, id string) error {
	var taken bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_sales_categories
	 WHERE active AND lower(btrim(name))=lower($1) AND id::text<>$2)`, name, id).Scan(&taken)
	if err == nil && taken {
		return errOrganizationCategoryName
	}
	return err
}

func (s workspace) createOrganizationCategory(ctx context.Context, input organizationCategoryInput) (organizationCategory, error) {
	result := organizationCategory{Name: strings.TrimSpace(input.Name), Active: true}
	if !textOK(result.Name, 10) {
		return result, errDispatchCategoryName
	}
	err := s.writeOrganizationCategory(ctx, func(tx pgx.Tx, by *string) error {
		if err := checkOrganizationCategoryName(ctx, tx, result.Name, ""); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO organization_sales_categories(id,name,active,recorded_at,recorded_by)
		 VALUES(gen_random_uuid(),$1,true,clock_timestamp(),$2) RETURNING id::text,version`, result.Name, by).Scan(&result.ID, &result.Version); err != nil {
			return err
		}
		return enqueueDispatchCategory(ctx, tx, result, by)
	})
	return result, err
}

func (s workspace) updateOrganizationCategory(ctx context.Context, input updateOrganizationCategoryInput) (organizationCategory, error) {
	id, ok := canonicalSourceUUID(input.ID)
	name := strings.TrimSpace(input.Name)
	var result organizationCategory
	if !textOK(name, 10) {
		return result, errDispatchCategoryName
	}
	if !ok || input.ExpectedVersion < 1 {
		return result, invalid
	}
	err := s.writeOrganizationCategory(ctx, func(tx pgx.Tx, by *string) error {
		result.ID = id
		if err := tx.QueryRow(ctx, `SELECT name,active,version FROM organization_sales_categories WHERE id=$1`, id).
			Scan(&result.Name, &result.Active, &result.Version); err != nil {
			return err
		}
		if result.Name == name && result.Active == input.Active {
			return nil
		}
		if result.Version != input.ExpectedVersion {
			return errOrganizationCategoryChanged
		}
		if input.Active {
			if err := checkOrganizationCategoryName(ctx, tx, name, id); err != nil {
				return err
			}
		} else if result.Active {
			var used bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_sales_category_history h
			 JOIN organization_units u ON u.id=h.unit_id WHERE h.category_id=$1 AND u.status='ACTIVE'
			  AND (h.valid_to IS NULL OR h.valid_to>statement_timestamp()))`, id).Scan(&used)
			if err != nil {
				return err
			}
			if used {
				return errOrganizationCategoryInUse
			}
		}
		if protectedDispatchCategory(result.Name) {
			return dispatchProblem("健康大使和健康创客类别由大春 AI 维护")
		}
		if err := tx.QueryRow(ctx, `UPDATE organization_sales_categories SET name=$2,active=$3,version=version+1,
		 recorded_at=clock_timestamp(),recorded_by=$4 WHERE id=$1 RETURNING name,active,version`, id, name, input.Active, by).
			Scan(&result.Name, &result.Active, &result.Version); err != nil {
			return err
		}
		return enqueueDispatchCategory(ctx, tx, result, by)
	})
	return result, err
}

type categoryCompanyVersion struct {
	ExpectedCategoryID string     `json:"expectedCategoryId"`
	UnitID             string     `json:"unitId"`
	ExpectedFrom       *time.Time `json:"expectedFrom"`
}
type assignOrganizationCategoryInput struct {
	CategoryID string                   `json:"categoryId"`
	Companies  []categoryCompanyVersion `json:"companies"`
}
type categoryAssignmentResult struct {
	Changed     int        `json:"changed"`
	EffectiveAt *time.Time `json:"effectiveAt,omitempty"`
}

const draftRefreshCompanyClassification = `SELECT p.id FROM periods p JOIN product_catalog_settings cfg USING(product_id)
 WHERE cfg.source_system='DCAI' AND $2<((p.ends_on+1)::timestamp AT TIME ZONE 'Asia/Shanghai')
 AND (EXISTS(SELECT 1 FROM catalog_sales_company_aliases a JOIN catalog_sales_context c
  ON c.source=a.source AND c.captured_at=a.observed_at WHERE a.source=cfg.source_system
  AND a.company_unit_id=ANY($1::uuid[]) AND (a.valid_to IS NULL OR a.valid_to>$2))
 OR EXISTS(SELECT 1 FROM organization_company_sources s WHERE s.source=cfg.source_system
  AND s.company_id=ANY($1::uuid[]) AND (s.valid_to IS NULL OR s.valid_to>$2)))
 UNION SELECT p.id FROM periods p JOIN source_mappings m USING(product_id)
 WHERE m.source='DCZD' AND m.domain='PRACTICE' AND p.calendar_month IS NULL
 AND EXISTS(SELECT 1 FROM organization_company_sources s WHERE s.source='DCZD'
  AND s.company_id=ANY($1::uuid[]) AND (s.valid_to IS NULL OR s.valid_to>$2))
 AND EXISTS(SELECT 1 FROM rule_versions r WHERE r.product_id=p.product_id
  AND (jsonb_path_exists(r.components,'$[*].companySales.categories[*].salesCategoryId')
    OR jsonb_path_exists(r.components,'$[*].companySales.categories[*].salesCategoryIds[0]')
    OR ($3::boolean AND (jsonb_path_exists(r.components,'$[*].companySales.defaultRate')
      OR jsonb_path_exists(r.components,'$[*].periodBenefit.standardRates[*] ? (@.externalCompanyDefault == true)')))
    OR jsonb_path_exists(r.components,'$[*].periodBenefit.salesCategoryIds[0]')
    OR jsonb_path_exists(r.components,'$[*].periodBenefit.standardRates[*].salesCategoryIds[0]')
    OR jsonb_path_exists(r.components,'$[*].saleScope.salesCategoryIds[0]')
    OR jsonb_path_exists(r.components,'$[*].tiers.categorySchedules[*].salesCategoryIds[0]')))`

func (s workspace) assignOrganizationCategory(ctx context.Context, input assignOrganizationCategoryInput) (categoryAssignmentResult, error) {
	var result categoryAssignmentResult
	id, ok := canonicalSourceUUID(input.CategoryID)
	if !ok || len(input.Companies) == 0 || len(input.Companies) > 100 {
		return result, invalid
	}
	pending := make(map[string]categoryCompanyVersion, len(input.Companies))
	for _, item := range input.Companies {
		unit, valid := canonicalSourceUUID(item.UnitID)
		_, exists := pending[unit]
		if !valid || exists {
			return result, invalid
		}
		if item.ExpectedCategoryID != "" {
			category, ok := canonicalSourceUUID(item.ExpectedCategoryID)
			if !ok {
				return result, invalid
			}
			item.ExpectedCategoryID = category
		}
		item.ExpectedFrom = canonicalConfigTime(item.ExpectedFrom)
		pending[unit] = item
	}
	ids := make([]string, 0, len(pending))
	for unit := range pending {
		ids = append(ids, unit)
	}
	sort.Strings(ids)
	err := s.writeOrganizationCategory(ctx, func(tx pgx.Tx, by *string) error {
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_sales_categories WHERE id=$1 AND active)`, id).Scan(&active); err != nil {
			return err
		}
		if !active {
			return errOrganizationCategoryTarget
		}
		var at time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT u.id::text,coalesce(h.category_id::text,''),h.valid_from,u.status='ACTIVE'
		 FROM organization_units u LEFT JOIN LATERAL (
		  SELECT h.category_id,h.valid_from FROM organization_sales_category_history h
		  WHERE h.unit_id=u.id AND tstzrange(h.valid_from,h.valid_to,'[)') @> $2::timestamptz
		  ORDER BY h.valid_from DESC LIMIT 1) h ON true
		 WHERE u.id=ANY($1::uuid[]) AND u.sales_company_type IS NOT NULL
		  AND (u.status='ACTIVE' OR cardinality($1::uuid[])=1) ORDER BY u.id FOR UPDATE OF u`, ids, at)
		if err != nil {
			return err
		}
		type assignment struct {
			Unit, Category string
			Since          *time.Time
			Active         bool
		}
		current, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (assignment, error) {
			var item assignment
			err := row.Scan(&item.Unit, &item.Category, &item.Since, &item.Active)
			return item, err
		})
		if err != nil {
			return err
		}
		if len(current) != len(ids) {
			return errOrganizationCategoryTarget
		}
		var changed, dispatchUnits []string
		for _, item := range current {
			if item.Category == id {
				continue
			}
			expected := pending[item.Unit]
			if item.Category != expected.ExpectedCategoryID || (item.Since == nil) != (expected.ExpectedFrom == nil) ||
				expected.ExpectedFrom != nil && !expected.ExpectedFrom.Equal(*item.Since) {
				return errOrganizationCategoryChanged
			}
			changed = append(changed, item.Unit)
			if item.Active {
				dispatchUnits = append(dispatchUnits, item.Unit)
			}
		}
		if len(changed) == 0 {
			return nil
		}
		// An imported future interval is authoritative until its date is reconciled.
		var future bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organization_sales_category_history
		 WHERE unit_id=ANY($1::uuid[]) AND valid_from>=$2)`, changed, at).Scan(&future); err != nil {
			return err
		}
		if future {
			return errOrganizationCategoryChanged
		}
		if _, err := tx.Exec(ctx, `UPDATE organization_sales_category_history SET valid_to=$2
		 WHERE unit_id=ANY($1::uuid[]) AND tstzrange(valid_from,valid_to,'[)') @> $2::timestamptz`, changed, at); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organization_sales_category_history(unit_id,category_id,valid_from,recorded_at,recorded_by)
		 SELECT unit_id,$2,$3,$3,$4 FROM unnest($1::uuid[]) AS targets(unit_id)`, changed, id, at, by); err != nil {
			return err
		}
		if err := enqueueDraftConfiguration(ctx, tx, draftRefreshCompanyClassification, changed, at, false); err != nil {
			return err
		}
		for _, unit := range dispatchUnits {
			if err := enqueueDispatchCompany(ctx, tx, unit, by); err != nil {
				return err
			}
		}
		result = categoryAssignmentResult{Changed: len(changed), EffectiveAt: &at}
		return nil
	})
	return result, err
}
