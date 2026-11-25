CREATE SEQUENCE IF NOT EXISTS organization_dispatch_version;
CREATE TABLE IF NOT EXISTS organization_dispatch_company_keys (
 unit_id uuid PRIMARY KEY REFERENCES organization_units(id), wire_id uuid NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS organization_dispatch_targets (
 company_id uuid NOT NULL REFERENCES organization_units(id),
 target text NOT NULL CHECK(target IN ('DCZD','DCJK','DCAI')), PRIMARY KEY(company_id,target)
);
CREATE TABLE IF NOT EXISTS organization_dispatch_links (
 target text NOT NULL CHECK(target IN ('DCZD','DCJK','DCAI')), entity_type text NOT NULL CHECK(entity_type IN ('CATEGORY','COMPANY','MEMBER')),
 entity_id uuid NOT NULL, external_id text NOT NULL CHECK(length(external_id) BETWEEN 1 AND 128),
 person_id bigint REFERENCES people(id), external_member_id text NOT NULL DEFAULT '', external_company_id text NOT NULL DEFAULT '',
 identity_state text NOT NULL DEFAULT '',role text NOT NULL DEFAULT '',version bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(target,entity_type,entity_id), UNIQUE(target,entity_type,external_id),
 CHECK((entity_type='MEMBER')=(person_id IS NOT NULL))
);
CREATE TABLE IF NOT EXISTS organization_dispatch_outbox (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), target text NOT NULL CHECK(target IN ('DCZD','DCJK','DCAI')),
 entity_type text NOT NULL CHECK(entity_type IN ('CATEGORY','COMPANY','MEMBER')),entity_id uuid NOT NULL,
 version bigint NOT NULL DEFAULT nextval('organization_dispatch_version'), message_type text NOT NULL, payload jsonb NOT NULL,
 effective_at timestamptz NOT NULL DEFAULT now(), occurred_at timestamptz NOT NULL DEFAULT now(),
 state text NOT NULL DEFAULT 'PENDING',reason text NOT NULL DEFAULT '', attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now(),lease_until timestamptz, external_id text NOT NULL DEFAULT '',
 actor uuid REFERENCES finance_accounts(id), UNIQUE(target,entity_type,entity_id,version)
);
CREATE INDEX IF NOT EXISTS organization_dispatch_due ON organization_dispatch_outbox(target,next_attempt_at,version)
 WHERE state IN ('PENDING','RETRYABLE','PROCESSING','WAIT_CATEGORY','WAIT_PARENT_COMPANY');
ALTER TABLE organization_dispatch_links ADD COLUMN IF NOT EXISTS source_name text NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS organization_dispatch_phone_bindings (
 phone_key bytea PRIMARY KEY,person_id bigint NOT NULL REFERENCES people(id)
);
ALTER TABLE organization_dispatch_company_keys ADD COLUMN IF NOT EXISTS parent_wire_id uuid,
 ADD COLUMN IF NOT EXISTS targets_managed boolean NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS organization_dispatch_units (
 unit_id uuid PRIMARY KEY REFERENCES organization_units(id),company_id uuid NOT NULL UNIQUE REFERENCES organization_units(id),
 recorded_by uuid REFERENCES finance_accounts(id),CHECK(unit_id<>company_id)
);
CREATE TABLE IF NOT EXISTS organization_dispatch_commands (
 request_id uuid PRIMARY KEY, message_id uuid NOT NULL REFERENCES organization_dispatch_outbox(id), input jsonb NOT NULL, actor uuid REFERENCES finance_accounts(id)
);
ALTER TABLE organization_units ADD COLUMN IF NOT EXISTS dispatch_profile_version bigint NOT NULL DEFAULT 0;
ALTER TABLE people ADD COLUMN IF NOT EXISTS dispatch_name_managed boolean NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS organization_person_intakes (
 request_id uuid PRIMARY KEY, fingerprint bytea NOT NULL, person_id bigint NOT NULL REFERENCES people(id),
 actor uuid REFERENCES finance_accounts(id)
);
CREATE TABLE IF NOT EXISTS organization_company_use_closures (
 request_id uuid PRIMARY KEY, company_id uuid NOT NULL REFERENCES organization_units(id),
 source text NOT NULL, external_id text NOT NULL, expected_from timestamptz NOT NULL,
 closed_at timestamptz NOT NULL, actor uuid REFERENCES finance_accounts(id)
);
CREATE INDEX IF NOT EXISTS organization_company_use_end ON organization_company_use_closures(company_id,source,external_id,closed_at);
