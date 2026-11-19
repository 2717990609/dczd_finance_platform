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
