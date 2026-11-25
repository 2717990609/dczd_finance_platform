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
		{Method: "GET", Path: base + "/source-relations", Handler: s.organizationSourceRelations},
		{Method: "GET", Path: base + "/source-directors", Handler: s.list(`SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY version_no DESC),'[]') FROM organization_a2_director_versions v`)},
		{Method: "POST", Path: base + "/close-company-use", Handler: submitJSON(16<<10, s.closeCompanyUse)},
		{Method: "POST", Path: base + "/person-intake", Handler: submitJSON(64<<10, s.savePersonIntake)},
		{Method: "GET", Path: base + "/company-members", Handler: s.companyMemberDirectory},
		{Method: "GET", Path: base + "/available-companies", Handler: s.availableDispatchCompanies},
		{Method: "GET", Path: base + "/channels", Handler: s.organizationDispatchChannels},
		{Method: "GET", Path: base + "/people", Handler: s.organizationPeopleDirectory},
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

const dispatchRetryEligible = `q.state IN ('RETRYABLE','WAIT_PARENT_COMPANY','WAIT_CATEGORY','CONFLICT')
 AND NOT EXISTS(SELECT FROM organization_dispatch_outbox n WHERE n.target=q.target AND n.entity_type=q.entity_type AND n.entity_id=q.entity_id AND n.version>q.version)
 AND (q.entity_type<>'COMPANY' OR EXISTS(SELECT FROM organization_dispatch_targets t WHERE t.company_id=q.entity_id AND t.target=q.target))`

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
	if id != nil && person != nil || q.Get("target") != "" && !validDispatchTarget(q.Get("target")) || q.Get("issues") != "" && q.Get("issues") != "true" {
		respond(w, nil, invalid)
		return
	}
	offset, _, _, err := organizationAssignmentFilters(q)
	if err != nil {
		respond(w, nil, err)
		return
	}
	var data json.RawMessage
	err = s.db.QueryRow(r.Context(), `WITH history AS (SELECT false AS imported,q.id,q.target,q.entity_type AS kind,q.entity_id AS "entityId",q.version,q.message_type AS "messageType",q.payload->>'operation' AS operation,
 (`+dispatchRetryEligible+`) AS "canRetry",q.state,q.reason,q.external_id AS "externalId",q.attempts,q.effective_at AS "effectiveAt",coalesce(u.name,c.name,p.name) AS name
 FROM organization_dispatch_outbox q LEFT JOIN organization_units u ON q.entity_type='COMPANY' AND q.entity_id=u.id
 LEFT JOIN organization_sales_categories c ON q.entity_type='CATEGORY' AND q.entity_id=c.id
 LEFT JOIN organization_dispatch_links l ON l.entity_type='MEMBER' AND q.entity_type=l.entity_type AND q.entity_id=l.entity_id AND q.target=l.target
 LEFT JOIN people p ON p.id=l.person_id
 WHERE ($1::uuid IS NULL OR (q.entity_type='COMPANY' AND q.entity_id=$1)) AND ($2::bigint IS NULL OR l.person_id=$2)
 AND ($4='' OR q.target=$4) AND (NOT $5 OR q.state IN ('CONFLICT','RETRYABLE','WAIT_CATEGORY','WAIT_PARENT_COMPANY'))
 UNION ALL SELECT true,h.id,h.target,h.kind,h.entity_id,h.version,null,h.operation,false,h.state,h.reason,h.external_id,h.attempts,h.effective_at,
 coalesce(u.name,c.name,p.name) FROM organization_a2_delivery_history h
 LEFT JOIN organization_dispatch_company_keys k ON h.kind='COMPANY' AND k.wire_id=h.entity_id
 LEFT JOIN organization_units u ON u.id=k.unit_id LEFT JOIN organization_sales_categories c ON h.kind='CATEGORY' AND c.id=h.entity_id
 LEFT JOIN organization_dispatch_links l ON h.kind='MEMBER' AND l.entity_type=h.kind AND l.entity_id=h.entity_id AND l.target=h.target
 LEFT JOIN people p ON p.id=l.person_id WHERE ($1 IS NULL OR k.unit_id=$1) AND ($2 IS NULL OR l.person_id=$2)
 AND ($4='' OR h.target=$4) AND (NOT $5 OR h.state IN ('CONFLICT','RETRYABLE','WAIT_CATEGORY','WAIT_PARENT_COMPANY'))),
 page AS(SELECT * FROM history ORDER BY version DESC LIMIT 25 OFFSET $3)
 SELECT jsonb_build_object('targets',array(SELECT target FROM organization_dispatch_targets WHERE company_id=$1 ORDER BY target),
 'businessUses',coalesce((SELECT jsonb_agg(to_jsonb(uses) ORDER BY uses.active DESC,uses.source,uses."externalId",uses."validFrom" DESC)
 FROM (`+companyBusinessUsesSQL+`) uses),'[]'),
 'targetStatus',coalesce((SELECT jsonb_agg(jsonb_build_object('target',t.target,'state',coalesce(last.state,CASE WHEN link.external_id IS NOT NULL THEN 'APPLIED' ELSE 'NO_MESSAGE' END),'reason',last.reason,'externalId',link.external_id) ORDER BY t.target)
 FROM organization_dispatch_targets t LEFT JOIN organization_dispatch_links link ON link.target=t.target AND link.entity_id=t.company_id AND link.entity_type='COMPANY'
 LEFT JOIN LATERAL (SELECT state,reason FROM (
 SELECT q.version,q.state,q.reason FROM organization_dispatch_outbox q WHERE q.target=t.target AND q.entity_type='COMPANY' AND q.entity_id=t.company_id
 UNION ALL SELECT h.version,h.state,h.reason FROM organization_a2_delivery_history h JOIN organization_dispatch_company_keys k ON k.wire_id=h.entity_id
 WHERE h.kind='COMPANY' AND h.target=t.target AND k.unit_id=t.company_id) all_receipts ORDER BY version DESC LIMIT 1) last ON true WHERE t.company_id=$1),'[]'),
 'profile',(SELECT jsonb_build_object('name',u.name,'status',u.status,'version',u.dispatch_profile_version,'parentId',p.unit_id,'parentName',pu.name) FROM organization_units u LEFT JOIN organization_dispatch_company_keys k ON k.unit_id=u.id LEFT JOIN organization_dispatch_company_keys p ON p.wire_id=k.parent_wire_id LEFT JOIN organization_units pu ON pu.id=p.unit_id WHERE u.id=$1),'wireId',(SELECT wire_id FROM organization_dispatch_company_keys WHERE unit_id=$1),'total',(SELECT count(*) FROM history),
 'items',coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY version DESC) FROM page),'[]'),
 'bindings',coalesce((SELECT jsonb_agg(jsonb_build_object('id',l.entity_id,'target',l.target,'userId',l.external_id,'companyId',l.external_company_id,
 'sourceName',l.source_name,'companyName',coalesce((SELECT u.name FROM organization_dispatch_links comp JOIN organization_units u ON u.id=comp.entity_id WHERE comp.target=l.target AND comp.entity_type='COMPANY' AND comp.external_id=l.external_company_id),
 (SELECT CASE WHEN count(DISTINCT u.id)=1 THEN min(u.name) END FROM organization_company_sources s JOIN organization_units u ON u.id=s.company_id
 WHERE s.source=l.target AND s.source_company_id=l.external_company_id AND tstzrange(s.valid_from,s.valid_to,'[)') @> statement_timestamp())),
 'pending',EXISTS(SELECT FROM organization_dispatch_outbox q WHERE q.target=l.target AND q.entity_type='MEMBER' AND q.entity_id=l.entity_id AND q.state NOT IN ('APPLIED','STOPPED','CONFLICT')),
 'memberId',l.external_member_id,'role',l.role,'state',l.identity_state,'version',l.version) ORDER BY l.target,l.external_id)
 FROM organization_dispatch_links l WHERE person_id=$2 AND entity_type='MEMBER'),'[]'))`, id, person, offset, q.Get("target"), q.Get("issues") == "true").Scan(&data)
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
   WHERE id=$1 AND attempts=$2 AND `+dispatchRetryEligible, id, input.ExpectedAttempts)
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
