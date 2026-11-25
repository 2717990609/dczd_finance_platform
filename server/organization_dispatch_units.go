package main

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

type dispatchUnitInput struct{ UnitID, CompanyID string }

func (s workspace) getDispatchUnit(w http.ResponseWriter, r *http.Request) {
	id, ok := canonicalSourceUUID(r.URL.Query().Get("unitId"))
	if !ok {
		respond(w, nil, invalid)
		return
	}
	var data json.RawMessage
	err := s.db.QueryRow(r.Context(), `SELECT jsonb_build_object('linkedId',c.id,'linkedName',c.name,'candidates',coalesce((SELECT jsonb_agg(jsonb_build_object('label',p.name,'value',p.id) ORDER BY p.name,p.id)
  FROM organization_units p WHERE u.sales_company_type IS NULL AND p.sales_company_type='INTERNAL' AND p.status='ACTIVE' AND p.id<>u.id
  AND lower(btrim(p.name))=lower(btrim(u.name)) AND NOT EXISTS(SELECT FROM organization_dispatch_units x WHERE x.company_id=p.id)),'[]'))
  FROM organization_units u LEFT JOIN organization_dispatch_units l ON l.unit_id=u.id
  LEFT JOIN organization_units c ON c.id=coalesce(l.company_id,CASE WHEN u.sales_company_type IS NOT NULL THEN u.id END) WHERE u.id=$1`, id).Scan(&data)
	respond(w, data, err)
}
func (s workspace) linkDispatchUnit(ctx context.Context, input dispatchUnitInput) (map[string]string, error) {
	unit, ok := canonicalSourceUUID(input.UnitID)
	company, valid := canonicalSourceUUID(input.CompanyID)
	if !ok || !valid || unit == company {
		return nil, invalid
	}
	err := s.writeOrganizationCategory(ctx, func(tx pgx.Tx, by *string) error {
		tag, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_units(unit_id,company_id,recorded_by)
  SELECT u.id,c.id,$3 FROM organization_units u,organization_units c WHERE u.id=$1 AND c.id=$2 AND u.sales_company_type IS NULL AND u.status='ACTIVE'
  AND c.sales_company_type='INTERNAL' AND c.status='ACTIVE' AND lower(btrim(c.name))=lower(btrim(u.name))
  ON CONFLICT(unit_id) DO UPDATE SET company_id=excluded.company_id WHERE organization_dispatch_units.company_id=excluded.company_id`, unit, company, by)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return conflict
		}
		return nil
	})
	return map[string]string{"companyId": company}, err
}
func (s workspace) cancelDispatchMember(ctx context.Context, input dispatchRetryInput) (map[string]bool, error) {
	id, ok := canonicalSourceUUID(input.MessageID)
	if !ok {
		return nil, invalid
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, by *string) error {
		tag, err := tx.Exec(ctx, `UPDATE organization_dispatch_outbox SET state='STOPPED',reason='CANCELED' WHERE id=$1 AND entity_type='MEMBER' AND state='PENDING' AND attempts=0 AND effective_at>clock_timestamp()`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return dispatchProblem("仅能取消尚未投递的未来生效操作")
		}
		return nil
	})
	return map[string]bool{"canceled": err == nil}, err
}

type dispatchCreateUnitInput struct{ Name, Kind, ParentID, CompanyID string }

func (s workspace) createDispatchUnit(ctx context.Context, input dispatchCreateUnitInput) (map[string]string, error) {
	name := strings.TrimSpace(input.Name)
	if !textOK(name, 100) {
		return nil, errDispatchCompanyName
	}
	switch input.Kind {
	case "COMPANY", "DEPARTMENT", "TEAM", "STORE":
	default:
		return nil, invalid
	}
	for _, raw := range []string{input.ParentID, input.CompanyID} {
		if raw != "" {
			if _, ok := canonicalSourceUUID(raw); !ok {
				return nil, invalid
			}
		}
	}
	var id string
	err := s.writeOrganizationCategory(ctx, func(tx pgx.Tx, by *string) error {
		if input.ParentID != "" {
			var parent string
			if err := tx.QueryRow(ctx, "SELECT id::text FROM organization_units WHERE id=$1 AND status='ACTIVE' FOR SHARE", input.ParentID).Scan(&parent); err != nil {
				return err
			}
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM organization_units WHERE parent_id IS NOT DISTINCT FROM nullif($1,'')::uuid
   AND sales_company_type IS NULL AND lower(btrim(name))=lower($2) AND status='ACTIVE')`, input.ParentID, name).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return dispatchProblem("同一上级已有同名组织")
		}
		if err := tx.QueryRow(ctx, "INSERT INTO organization_units(id,parent_id,name,kind,status) VALUES(gen_random_uuid(),nullif($1,'')::uuid,$2,$3,'ACTIVE') RETURNING id::text", input.ParentID, name, input.Kind).Scan(&id); err != nil {
			return err
		}
		if input.CompanyID != "" {
			tag, err := tx.Exec(ctx, `INSERT INTO organization_dispatch_units(unit_id,company_id,recorded_by)
   SELECT $1,id,$3 FROM organization_units WHERE id=$2 AND status='ACTIVE' AND sales_company_type='INTERNAL' AND lower(btrim(name))=lower($4)`, id, input.CompanyID, by, name)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return dispatchProblem("仅可关联同名的启用内部销售公司")
			}
		}
		return nil
	})
	return map[string]string{"id": id}, err
}
