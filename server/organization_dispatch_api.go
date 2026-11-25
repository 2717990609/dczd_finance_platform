package main

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
)

func (s workspace) organizationDispatchRoutes() []rest.Route {
	const base = "/api/v1/organization-dispatch"
	return []rest.Route{
		{Method: "POST", Path: base + "/create-unit", Handler: submitJSON(16<<10, s.createDispatchUnit)},
		{Method: "POST", Path: base + "/rename-person", Handler: submitJSON(16<<10, s.renameDispatchPerson)},
		{Method: "POST", Path: base + "/profile", Handler: submitJSON(32<<10, s.saveDispatchProfile)},
		{Method: "GET", Path: base + "/unit", Handler: s.getDispatchUnit},
		{Method: "POST", Path: base + "/unit", Handler: submitJSON(16<<10, s.linkDispatchUnit)},
		{Method: "POST", Path: base + "/cancel", Handler: submitJSON(16<<10, s.cancelDispatchMember)},
		{Method: "GET", Path: base, Handler: s.getOrganizationDispatch},
		{Method: "POST", Path: base + "/targets", Handler: submitJSON(32<<10, s.saveDispatchTargets)},
		{Method: "POST", Path: base + "/lookup", Handler: submitJSON(16<<10, s.lookupDispatchAccount)},
		{Method: "POST", Path: base + "/bind", Handler: submitJSON(16<<10, s.bindDispatchAccount)},
		{Method: "POST", Path: base + "/member", Handler: submitJSON(16<<10, s.operateDispatchMember)},
		{Method: "POST", Path: base + "/retry", Handler: submitJSON(16<<10, s.retryOrganizationDispatch)},
	}
}
func (s workspace) getOrganizationDispatch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var id *string
	var person *int64
	if raw := q.Get("companyId"); raw != "" {
		v, ok := canonicalSourceUUID(raw)
		if !ok {
			respond(w, nil, invalid)
			return
		}
		id = &v
	}
	if raw := q.Get("personId"); raw != "" {
		v, err := optionalPersonID(raw)
		if err != nil {
			respond(w, nil, invalid)
			return
		}
		person = v
	}
	if id != nil && person != nil {
		respond(w, nil, invalid)
		return
	}
	offset, _, _, err := organizationAssignmentFilters(q)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var data json.RawMessage
	err = s.db.QueryRow(r.Context(), `WITH history AS (SELECT q.id,q.target,q.entity_type AS kind,q.entity_id AS "entityId",q.version,q.message_type AS "messageType",q.payload->>'operation' AS operation,
 q.state,q.reason,q.external_id AS "externalId",q.attempts,q.effective_at AS "effectiveAt",coalesce(u.name,c.name,p.name) AS name
 FROM organization_dispatch_outbox q LEFT JOIN organization_units u ON q.entity_type='COMPANY' AND q.entity_id=u.id
 LEFT JOIN organization_sales_categories c ON q.entity_type='CATEGORY' AND q.entity_id=c.id
 LEFT JOIN organization_dispatch_links l ON l.entity_type='MEMBER' AND q.entity_type=l.entity_type AND q.entity_id=l.entity_id AND q.target=l.target
 LEFT JOIN people p ON p.id=l.person_id
 WHERE ($1::uuid IS NULL OR (q.entity_type='COMPANY' AND q.entity_id=$1)) AND ($2::bigint IS NULL OR l.person_id=$2)),
 page AS(SELECT * FROM history ORDER BY version DESC LIMIT 25 OFFSET $3)
 SELECT jsonb_build_object('targets',array(SELECT target FROM organization_dispatch_targets WHERE company_id=$1 ORDER BY target),
 'profile',(SELECT jsonb_build_object('name',u.name,'status',u.status,'version',u.dispatch_profile_version,'parentId',p.unit_id,'parentName',pu.name) FROM organization_units u LEFT JOIN organization_dispatch_company_keys k ON k.unit_id=u.id LEFT JOIN organization_dispatch_company_keys p ON p.wire_id=k.parent_wire_id LEFT JOIN organization_units pu ON pu.id=p.unit_id WHERE u.id=$1),'wireId',(SELECT wire_id FROM organization_dispatch_company_keys WHERE unit_id=$1),'total',(SELECT count(*) FROM history),
 'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY version DESC) FROM page),'[]'),
 'bindings',coalesce((SELECT jsonb_agg(jsonb_build_object('id',l.entity_id,'target',l.target,'userId',l.external_id,'companyId',l.external_company_id,
 'memberId',l.external_member_id,'role',l.role,'state',l.identity_state,'version',l.version) ORDER BY l.target,l.external_id)
 FROM organization_dispatch_links l WHERE person_id=$2 AND entity_type='MEMBER'),'[]'))`, id, person, offset).Scan(&data)
	respond(w, data, err)
}

type dispatchRetryInput struct {
	MessageID        string
	ExpectedAttempts int
}

func (s workspace) retryOrganizationDispatch(ctx context.Context, input dispatchRetryInput) (map[string]bool, error) {
	id, ok := canonicalSourceUUID(input.MessageID)
	if !ok || input.ExpectedAttempts < 0 {
		return nil, invalid
	}
	err := s.writeAppointments(ctx, func(tx pgx.Tx, _ *string) error {
		tag, err := tx.Exec(ctx, `UPDATE organization_dispatch_outbox q SET state='PENDING',reason='',next_attempt_at=now()
   WHERE id=$1 AND attempts=$2 AND state IN ('RETRYABLE','WAIT_PARENT_COMPANY','WAIT_CATEGORY','CONFLICT')
   AND NOT EXISTS(SELECT 1 FROM organization_dispatch_outbox n WHERE n.target=q.target AND n.entity_type=q.entity_type AND n.entity_id=q.entity_id AND n.version>q.version)
   AND (entity_type<>'COMPANY' OR EXISTS(SELECT FROM organization_dispatch_targets t WHERE t.company_id=q.entity_id AND t.target=q.target))`, id, input.ExpectedAttempts)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return conflict
		}
		return nil
	})
	return map[string]bool{"queued": err == nil}, err
}
