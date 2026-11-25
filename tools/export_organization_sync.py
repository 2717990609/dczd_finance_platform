"""Attach A2 organization delivery identities to a verified organization snapshot."""
import argparse
import json
import os
import subprocess
import tempfile
from pathlib import Path

# The source tables contain operating data. Only approved identifiers, states
# and version numbers leave the read-only transaction.
QUERY = """
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
WITH latest AS (
  SELECT DISTINCT ON (target_system, entity_type, entity_id) *
  FROM finance_organization_sync_outbox
  ORDER BY target_system, entity_type, entity_id, entity_version DESC
), applied AS (
  SELECT DISTINCT ON (target_system, entity_type, entity_id)
    target_system, entity_type, entity_id, external_id, entity_version
  FROM finance_organization_sync_outbox
  WHERE status='APPLIED' AND external_id<>''
  ORDER BY target_system, entity_type, entity_id, entity_version DESC
), binding AS (
  SELECT b.id AS "entityId", b.person_id AS "personId", b.target_system AS target,
    b.external_user_id AS "userId", b.external_member_id AS "memberId",
    b.external_company_id AS "companyId", b.role, b.identity_state AS state,
    coalesce(a.entity_version,0) AS version
  FROM finance_organization_sync_bindings b
  LEFT JOIN applied a ON a.entity_type='MEMBER' AND a.entity_id=b.id AND a.target_system=b.target_system
),
phone_binding AS (
  SELECT phone_key AS "phoneKey", person_id AS "personId" FROM finance_organization_sync_phones
), company AS (
  SELECT c.membership_scope_unit_id AS "unitId", c.organization_party_id AS "entityId",
    c.sync_target_systems AS targets, c.parent_organization_party_id AS "parentEntityId", u.unit_id AS "internalUnitId"
  FROM finance_sales_company_profiles c LEFT JOIN finance_organization_sync_units u ON u.company_id=c.organization_party_id
), receipts AS (
  SELECT target_system AS target,entity_type AS kind,entity_id AS "entityId",external_id AS "externalId",
    entity_version AS version FROM applied WHERE entity_type IN ('COMPANY','CATEGORY')
), ai_category AS (
  SELECT external_id AS "externalId",category_id AS "categoryId" FROM finance_organization_sync_ai_categories
), ai_receipt AS (
  SELECT source_change_id AS id,entity_type AS kind,external_id AS "externalId",entity_id AS "wireId",
    category_id AS "categoryId",finance_base_version AS "financeBaseVersion",
    acknowledge_only AS "acknowledgeOnly",acknowledged_at AS "acknowledgedAt"
  FROM finance_organization_sync_ai_inbox
), health_assignment AS (
  SELECT m.id, i.external_id AS member_id
  FROM finance_sales_company_memberships m JOIN finance_organization_sync_ai_inbox i
    ON i.entity_type='MEMBER' AND m.request_id='ai-health:'||i.source_change_id||':sales-membership'
  WHERE m.created_by='ai-health-sync'
)
SELECT jsonb_build_object(
 'pendingDelivery',(SELECT count(*) FROM latest WHERE status NOT IN ('APPLIED','STOPPED')),
 'inFlight',(SELECT count(*) FROM finance_organization_sync_outbox
   WHERE status IN ('PENDING','PROCESSING','RETRYABLE','WAIT_CATEGORY','WAIT_PARENT_COMPANY')),
 'pendingBindings',(SELECT count(*) FROM binding WHERE state NOT IN ('ACTIVE','INACTIVE')),
 'openAIConflicts',(SELECT count(*) FROM finance_organization_ai_conflicts WHERE state IN ('OPEN','RESOLVING')),
 'personDirectory',coalesce((SELECT jsonb_object_agg(id,jsonb_build_object(
   'status',status,'display_name_status',display_name_status)) FROM finance_parties WHERE party_type='PERSON'),'{}'),
 'healthAssignments',coalesce((SELECT jsonb_object_agg(id,member_id) FROM health_assignment),'{}'),
 'dispatchVersionFloor',coalesce((SELECT max(entity_version) FROM finance_organization_sync_outbox),0),
 'dispatchCompanies',coalesce((SELECT jsonb_agg(c ORDER BY "unitId") FROM company c),'[]'),
 'dispatchReceipts',coalesce((SELECT jsonb_agg(r ORDER BY target,kind,"entityId") FROM receipts r),'[]'),
 'dispatchBindings',coalesce((SELECT jsonb_agg(b ORDER BY "entityId") FROM binding b),'[]'),
 'dispatchPhoneBindings',coalesce((SELECT jsonb_agg(b ORDER BY "phoneKey") FROM phone_binding b),'[]'),
 'aiHealthCategories',coalesce((SELECT jsonb_agg(c ORDER BY "externalId") FROM ai_category c),'[]'),
 'aiHealthReceipts',coalesce((SELECT jsonb_agg(r ORDER BY id) FROM ai_receipt r),'[]'));
COMMIT;
"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("snapshot", type=Path, help="existing organization JSON from the same A2 cutover")
    parser.add_argument("output", type=Path, help="new private output file")
    args = parser.parse_args()
    base = json.loads(args.snapshot.read_text())
    if not base.get("capturedAt") or not all(isinstance(base.get(k), list) for k in ("units", "people", "assignments")):
        parser.error("select a complete organization snapshot from the same cutover")
    result = subprocess.run(["psql", "-XqAt", "-v", "ON_ERROR_STOP=1"], input=QUERY,
                            text=True, stdout=subprocess.PIPE, check=True)
    links = json.loads(result.stdout)
    blockers = {key: links.pop(key) for key in ("pendingDelivery", "inFlight", "pendingBindings", "openAIConflicts")}
    if any(blockers.values()):
        parser.error(f"resolve outstanding A2 operations before switching senders: {blockers}")
    people = {row["id"] for row in base["people"]}
    directory = links.pop("personDirectory")
    for person in base["people"]:
        if person["id"] not in directory:
            parser.error("a person in the organization snapshot is missing from the A2 directory")
        person.update(directory[person["id"]])
    units = {row["id"] for row in base["units"]}
    categories = {row["id"] for row in base.get("salesCategories", [])}
    if (any(row["unitId"] not in units for row in links["dispatchCompanies"])
            or any(row["personId"] not in people for rows in ("dispatchBindings", "dispatchPhoneBindings") for row in links[rows])
            or any(row["categoryId"] not in categories for row in links["aiHealthCategories"])):
        parser.error("sync identities reference people, companies or categories absent from the base snapshot")
    assignments = {row["id"]: row for row in base["assignments"]}
    for assignment, member in links.pop("healthAssignments").items():
        if assignment not in assignments:
            parser.error("an AI-managed sales appointment is missing from the base snapshot")
        assignments[assignment]["healthMemberId"] = member
    base.update(links)
    # Publish a complete file without overwriting a prior handoff.
    with tempfile.NamedTemporaryFile(dir=args.output.parent, mode="w", encoding="utf-8") as stage:
        json.dump(base, stage, separators=(",", ":"), ensure_ascii=False)
        stage.flush()
        os.link(stage.name, args.output)
    print("Exported", len(links["dispatchCompanies"]), "company keys and", len(links["dispatchBindings"]), "bindings")


if __name__ == "__main__":
    main()
