CREATE TABLE IF NOT EXISTS products (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 code text NOT NULL UNIQUE,
 name text NOT NULL,
 period_mode text NOT NULL CHECK (period_mode IN ('class','calendar')),
 created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS source_mappings (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 product_id bigint NOT NULL REFERENCES products(id),
 source text NOT NULL CHECK (source IN ('DCAI','DCZD','DCJK')),
 domain text NOT NULL,
 source_key text NOT NULL,
 UNIQUE(source, domain, source_key)
);

CREATE TABLE IF NOT EXISTS periods (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 product_id bigint NOT NULL REFERENCES products(id),
 name text NOT NULL,
 ordinal integer NOT NULL CHECK (ordinal > 0),
 starts_on date NOT NULL,
 ends_on date NOT NULL CHECK (ends_on >= starts_on),
 UNIQUE(product_id, ordinal),
 UNIQUE(product_id, id)
);

CREATE TABLE IF NOT EXISTS rule_versions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 product_id bigint NOT NULL REFERENCES products(id),
 version integer NOT NULL CHECK (version > 0),
 label text NOT NULL,
 period_id bigint,
 from_ordinal integer CHECK (from_ordinal > 0),
 components jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (period_id IS NULL OR from_ordinal IS NULL),
 FOREIGN KEY (product_id, period_id) REFERENCES periods(product_id, id),
 UNIQUE (product_id, version)
);

CREATE TABLE IF NOT EXISTS people (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 name text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS identity_mappings (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 person_id bigint NOT NULL REFERENCES people(id),
 source text NOT NULL CHECK (source IN ('DCAI','DCZD','DCJK')),
 external_id text NOT NULL,
 valid_from timestamptz NOT NULL,
 valid_to timestamptz CHECK (valid_to > valid_from),
 UNIQUE(source, external_id, valid_from)
);

CREATE UNIQUE INDEX IF NOT EXISTS identity_mappings_current
 ON identity_mappings(source, external_id) WHERE valid_to IS NULL;

CREATE TABLE IF NOT EXISTS source_order_versions (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 source text NOT NULL CHECK (source IN ('DCAI','DCZD','DCJK')),
 order_id text NOT NULL,
 line_id text NOT NULL,
 record_key text NOT NULL,
 version text NOT NULL,
 kind text NOT NULL,
 updated_at timestamptz NOT NULL,
 fact jsonb NOT NULL,
 received_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source, order_id, line_id, record_key, kind, version)
);

CREATE INDEX IF NOT EXISTS source_order_versions_lookup
 ON source_order_versions(source, order_id, line_id, record_key, kind, updated_at DESC, id DESC);

ALTER TABLE products ADD COLUMN IF NOT EXISTS cycle_start_day smallint
 CHECK (cycle_start_day BETWEEN 1 AND 28 AND period_mode = 'calendar');
ALTER TABLE periods ADD COLUMN IF NOT EXISTS calendar_month date;
CREATE UNIQUE INDEX IF NOT EXISTS periods_calendar_month
 ON periods(product_id, calendar_month) WHERE calendar_month IS NOT NULL;

ALTER TABLE source_order_versions ADD COLUMN IF NOT EXISTS occurred_at timestamptz;
UPDATE source_order_versions SET occurred_at = (fact->>'occurredAt')::timestamptz WHERE occurred_at IS NULL;
ALTER TABLE source_order_versions ALTER COLUMN occurred_at SET NOT NULL;
CREATE INDEX IF NOT EXISTS source_order_versions_payment_date
 ON source_order_versions(source, (fact->>'domain'), (fact->>'sourceKey'), occurred_at) WHERE kind = 'ORDER_PAID';

CREATE TABLE IF NOT EXISTS calculation_drafts (
 period_id bigint PRIMARY KEY REFERENCES periods(id),
 rule_id bigint NOT NULL REFERENCES rule_versions(id),
 version bigint NOT NULL CHECK (version > 0),
 input_digest bytea NOT NULL,
 total_minor bigint NOT NULL CHECK (total_minor >= 0),
 line_count int NOT NULL CHECK (line_count >= 0),
 issue_count int NOT NULL CHECK (issue_count >= 0),
 calculated_at timestamptz NOT NULL
);

CREATE TABLE IF NOT EXISTS calculation_draft_lines (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 period_id bigint NOT NULL REFERENCES calculation_drafts(period_id) ON DELETE CASCADE,
 source_version_id bigint NOT NULL REFERENCES source_order_versions(id),
 component_type text NOT NULL,
 identity_id bigint NOT NULL REFERENCES identity_mappings(id),
 person_id bigint NOT NULL REFERENCES people(id),
 base_minor bigint NOT NULL CHECK (base_minor >= 0),
 amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
 UNIQUE(period_id, source_version_id, component_type)
);
CREATE INDEX IF NOT EXISTS calculation_draft_lines_page ON calculation_draft_lines(period_id, id);

CREATE TABLE IF NOT EXISTS calculation_draft_issues (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 period_id bigint NOT NULL REFERENCES calculation_drafts(period_id) ON DELETE CASCADE,
 source_version_id bigint REFERENCES source_order_versions(id),
 component_type text NOT NULL,
 code text NOT NULL
);
CREATE INDEX IF NOT EXISTS calculation_draft_issues_page ON calculation_draft_issues(period_id, id);
