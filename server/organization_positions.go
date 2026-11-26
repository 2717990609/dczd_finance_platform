package main

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	errOrganizationPositionName    = errors.New("已有同名岗位")
	errOrganizationPositionChanged = errors.New("岗位已发生变化，请重新打开后再保存")
)

const positionRevisionSQL = `md5(jsonb_build_array(label,enabled,applies_to)::text)`
const positionScopesSQL = `coalesce(applies_to->'scopes','["ORGANIZATION_UNIT","PRODUCT","PROJECT"]'::jsonb)`
const organizationPositionsSQL = `SELECT coalesce(jsonb_agg(
 jsonb_build_object('id',option_key,'name',label,'active',enabled,'scopes',` + positionScopesSQL + `,
  'revision',` + positionRevisionSQL + `) ORDER BY enabled DESC,display_order,option_key),'[]')
 FROM configuration_options WHERE group_key='catalogAppointmentRole'`

type organizationPosition struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Active   bool     `json:"active"`
	Scopes   []string `json:"scopes"`
	Revision string   `json:"revision"`
}

type saveOrganizationPositionInput struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Active   *bool    `json:"active"`
	Scopes   []string `json:"scopes"`
	Revision string   `json:"revision"`
}

func (s workspace) saveOrganizationPosition(ctx context.Context, input saveOrganizationPositionInput) (organizationPosition, error) {
	var result organizationPosition
	if input.Active == nil {
		return result, invalid
	}
	if input.ID != "" {
		if !textOK(input.ID, 80) || len(input.Revision) != 32 || input.Name != "" || input.Scopes != nil {
			return result, invalid
		}
		return s.setOrganizationPositionActive(ctx, input.ID, input.Revision, *input.Active)
	}
	name := strings.TrimSpace(input.Name)
	if !textOK(name, 80) || len(input.Scopes) == 0 || len(input.Scopes) > 3 {
		return result, invalid
	}
	scopes := slices.Clone(input.Scopes)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	if len(scopes) != len(input.Scopes) {
		return result, invalid
	}
	for _, scope := range scopes {
		switch scope {
		case "PRODUCT", "PROJECT", "ORGANIZATION_UNIT":
		default:
			return result, invalid
		}
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, _ *string) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('catalogAppointmentRole'))`); err != nil {
			return err
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM configuration_options
		 WHERE group_key='catalogAppointmentRole' AND lower(btrim(label))=lower($1))`, name).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return errOrganizationPositionName
		}
		return tx.QueryRow(ctx, `INSERT INTO configuration_options(group_key,option_key,label,enabled,display_order,applies_to)
		 SELECT 'catalogAppointmentRole','ROLE_'||replace(gen_random_uuid()::text,'-',''),$1,$2,
		  coalesce(max(display_order),0)+10,jsonb_build_object('scopes',to_jsonb($3::text[]))
		 FROM configuration_options WHERE group_key='catalogAppointmentRole'
		 RETURNING option_key,label,enabled,`+positionScopesSQL+`,`+positionRevisionSQL,
			name, *input.Active, scopes).Scan(&result.ID, &result.Name, &result.Active, &result.Scopes, &result.Revision)
	})
	return result, err
}

func (s workspace) setOrganizationPositionActive(ctx context.Context, id, revision string, active bool) (organizationPosition, error) {
	var result organizationPosition
	err := s.writeAppointments(ctx, func(tx pgx.Tx, _ *string) error {
		err := tx.QueryRow(ctx, `UPDATE configuration_options SET enabled=$2
		 WHERE group_key='catalogAppointmentRole' AND option_key=$1 AND `+positionRevisionSQL+`=$3
		 RETURNING option_key,label,enabled,`+positionScopesSQL+`,`+positionRevisionSQL,
			id, active, revision).Scan(&result.ID, &result.Name, &result.Active, &result.Scopes, &result.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return errOrganizationPositionChanged
		}
		return err
	})
	return result, err
}
