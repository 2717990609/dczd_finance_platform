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

ALTER TABLE products ADD COLUMN IF NOT EXISTS cycle_start_day smallint;
DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='products'::regclass AND conname='products_cycle_start_day_check') THEN
  ALTER TABLE products DROP CONSTRAINT IF EXISTS products_check,
   ADD CONSTRAINT products_cycle_start_day_check
   CHECK (cycle_start_day IS NULL OR (cycle_start_day BETWEEN 1 AND 28 AND period_mode='calendar'));
 END IF;
END
$$;
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

ALTER TABLE rule_versions ADD COLUMN IF NOT EXISTS effective_from timestamptz;
ALTER TABLE calculation_draft_lines ADD COLUMN IF NOT EXISTS rule_id bigint REFERENCES rule_versions(id);
UPDATE calculation_draft_lines AS line SET rule_id=draft.rule_id
 FROM calculation_drafts AS draft WHERE line.period_id=draft.period_id AND line.rule_id IS NULL;
ALTER TABLE calculation_draft_lines ALTER COLUMN rule_id SET NOT NULL;

ALTER TABLE products ADD COLUMN IF NOT EXISTS settlement_enabled boolean NOT NULL DEFAULT false;
CREATE UNIQUE INDEX IF NOT EXISTS calculation_drafts_period_version ON calculation_drafts(period_id, version);

CREATE TABLE IF NOT EXISTS settlements (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 period_id bigint NOT NULL UNIQUE REFERENCES periods(id),
 draft_version bigint NOT NULL,
 confirmed_at timestamptz NOT NULL DEFAULT now(),
 confirmed_by text NOT NULL CHECK (char_length(confirmed_by) BETWEEN 1 AND 128),
 FOREIGN KEY (period_id, draft_version) REFERENCES calculation_drafts(period_id, version)
);

CREATE TABLE IF NOT EXISTS settlement_removals (
 settlement_id bigint PRIMARY KEY,
 period_id bigint NOT NULL REFERENCES periods(id),
 draft_version bigint NOT NULL,
 removed_by text NOT NULL,
 removed_at timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION protect_final_calculation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP <> 'INSERT' AND EXISTS (SELECT 1 FROM settlements WHERE period_id=OLD.period_id) THEN
  RAISE EXCEPTION 'confirmed calculation cannot be changed' USING ERRCODE='55000';
 END IF;
 IF TG_OP <> 'DELETE' AND EXISTS (SELECT 1 FROM settlements WHERE period_id=NEW.period_id) THEN
  RAISE EXCEPTION 'confirmed calculation cannot be changed' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END
$$;

CREATE OR REPLACE TRIGGER protect_confirmed_drafts BEFORE INSERT OR UPDATE OR DELETE ON calculation_drafts
 FOR EACH ROW EXECUTE FUNCTION protect_final_calculation();
CREATE OR REPLACE TRIGGER protect_confirmed_lines BEFORE INSERT OR UPDATE OR DELETE ON calculation_draft_lines
 FOR EACH ROW EXECUTE FUNCTION protect_final_calculation();
CREATE OR REPLACE TRIGGER protect_confirmed_issues BEFORE INSERT OR UPDATE OR DELETE ON calculation_draft_issues
 FOR EACH ROW EXECUTE FUNCTION protect_final_calculation();

CREATE OR REPLACE FUNCTION protect_settlement_record() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  RAISE EXCEPTION 'confirmed settlement cannot be changed' USING ERRCODE='55000';
 END IF;
 IF EXISTS(
    SELECT 1 FROM calculation_draft_lines l
    JOIN calculation_draft_refunds a ON a.original_line_id=l.id
    JOIN settlements s ON s.period_id=a.period_id WHERE l.period_id=OLD.period_id AND s.id<>OLD.id
 ) OR EXISTS(
    SELECT 1 FROM calculation_draft_lines l
    JOIN calculation_draft_corrections a ON a.original_line_id=l.id
    JOIN settlements s ON s.period_id=a.period_id WHERE l.period_id=OLD.period_id AND s.id<>OLD.id
 ) THEN
  RAISE EXCEPTION 'settlement has booked adjustments' USING ERRCODE='55000';
 END IF;
 DELETE FROM calculation_drafts
 WHERE period_id IN (SELECT a.period_id FROM calculation_draft_refunds a
   JOIN calculation_draft_lines l ON l.id=a.original_line_id WHERE l.period_id=OLD.period_id
   UNION SELECT a.period_id FROM calculation_draft_corrections a
   JOIN calculation_draft_lines l ON l.id=a.original_line_id WHERE l.period_id=OLD.period_id)
 AND period_id<>OLD.period_id;
 INSERT INTO settlement_removals(settlement_id,period_id,draft_version,removed_by)
 VALUES(OLD.id,OLD.period_id,OLD.draft_version,session_user);
 RETURN OLD;
END
$$;

CREATE OR REPLACE TRIGGER protect_settlements BEFORE UPDATE OR DELETE ON settlements
 FOR EACH ROW EXECUTE FUNCTION protect_settlement_record();

CREATE OR REPLACE FUNCTION protect_settlement_input() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='source_order_versions' THEN
  IF EXISTS(SELECT 1 FROM calculation_draft_lines l JOIN settlements s USING(period_id) WHERE l.source_version_id=OLD.id)
     OR EXISTS(SELECT 1 FROM calculation_draft_refunds a JOIN settlements s USING(period_id) WHERE a.refund_source_version_id=OLD.id)
     OR EXISTS(SELECT 1 FROM calculation_draft_corrections a JOIN settlements s USING(period_id) WHERE a.source_version_id=OLD.id) THEN
   RAISE EXCEPTION 'source version is referenced by a confirmed calculation' USING ERRCODE='55000';
  END IF;
 ELSE
  IF EXISTS(SELECT 1 FROM calculation_draft_lines l JOIN settlements s USING(period_id) WHERE l.rule_id=OLD.id)
     OR EXISTS(SELECT 1 FROM calculation_drafts d JOIN settlements s USING(period_id) WHERE d.rule_id=OLD.id) THEN
   RAISE EXCEPTION 'rule version is referenced by a confirmed calculation' USING ERRCODE='55000';
  END IF;
 END IF;
 RETURN NEW;
END
$$;

CREATE OR REPLACE TRIGGER protect_settled_order_versions BEFORE UPDATE ON source_order_versions
 FOR EACH ROW EXECUTE FUNCTION protect_settlement_input();
CREATE OR REPLACE TRIGGER protect_settled_rule_versions BEFORE UPDATE ON rule_versions
 FOR EACH ROW EXECUTE FUNCTION protect_settlement_input();

ALTER TABLE calculation_drafts ADD COLUMN IF NOT EXISTS refund_minor bigint NOT NULL DEFAULT 0 CHECK(refund_minor<=0);
ALTER TABLE calculation_drafts ADD COLUMN IF NOT EXISTS refund_count int NOT NULL DEFAULT 0 CHECK(refund_count>=0);
CREATE INDEX IF NOT EXISTS source_order_versions_refund_date
 ON source_order_versions(source, (fact->>'domain'), (fact->>'sourceKey'), occurred_at)
 WHERE kind='REFUND_SUCCEEDED';

CREATE INDEX IF NOT EXISTS source_order_versions_refund_identity
 ON source_order_versions(source,(fact->>'domain'),(fact->>'refundId')) WHERE kind='REFUND_SUCCEEDED';

CREATE TABLE IF NOT EXISTS calculation_draft_refunds (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 period_id bigint NOT NULL REFERENCES calculation_drafts(period_id) ON DELETE CASCADE,
 original_line_id bigint NOT NULL REFERENCES calculation_draft_lines(id),
 refund_period_id bigint NOT NULL REFERENCES periods(id),
 refund_source_version_id bigint NOT NULL REFERENCES source_order_versions(id),
 amount_minor bigint NOT NULL CHECK(amount_minor<=0),
 UNIQUE(period_id, original_line_id, refund_source_version_id)
);
CREATE INDEX IF NOT EXISTS calculation_refunds_page ON calculation_draft_refunds(period_id,id);
CREATE INDEX IF NOT EXISTS calculation_refunds_original ON calculation_draft_refunds(original_line_id);
CREATE INDEX IF NOT EXISTS calculation_refunds_source_version ON calculation_draft_refunds(refund_source_version_id);
CREATE OR REPLACE TRIGGER protect_confirmed_refunds BEFORE INSERT OR UPDATE OR DELETE ON calculation_draft_refunds
 FOR EACH ROW EXECUTE FUNCTION protect_final_calculation();

ALTER TABLE calculation_drafts ADD COLUMN IF NOT EXISTS correction_minor bigint NOT NULL DEFAULT 0;
ALTER TABLE calculation_drafts ADD COLUMN IF NOT EXISTS correction_count int NOT NULL DEFAULT 0 CHECK(correction_count>=0);

CREATE TABLE IF NOT EXISTS calculation_draft_corrections (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 period_id bigint NOT NULL REFERENCES calculation_drafts(period_id) ON DELETE CASCADE,
 original_line_id bigint NOT NULL REFERENCES calculation_draft_lines(id),
 source_version_id bigint NOT NULL REFERENCES source_order_versions(id),
 person_id bigint NOT NULL REFERENCES people(id),
 amount_minor bigint NOT NULL CHECK(amount_minor<>0),
 UNIQUE(period_id,original_line_id,person_id)
);
CREATE INDEX IF NOT EXISTS calculation_corrections_page ON calculation_draft_corrections(period_id,id);
CREATE INDEX IF NOT EXISTS calculation_corrections_original ON calculation_draft_corrections(original_line_id);
CREATE INDEX IF NOT EXISTS calculation_corrections_source ON calculation_draft_corrections(source_version_id);
CREATE OR REPLACE TRIGGER protect_confirmed_corrections BEFORE INSERT OR UPDATE OR DELETE ON calculation_draft_corrections
 FOR EACH ROW EXECUTE FUNCTION protect_final_calculation();

CREATE TABLE IF NOT EXISTS correction_recovery_decisions (
 original_line_id bigint NOT NULL REFERENCES calculation_draft_lines(id) ON DELETE CASCADE,
 person_id bigint NOT NULL REFERENCES people(id),
 outcome text NOT NULL CHECK(outcome IN ('recover','departed_unreachable')),
 reviewed_by text NOT NULL CHECK(char_length(reviewed_by) BETWEEN 1 AND 128),
 reviewed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(original_line_id,person_id)
);

ALTER TABLE periods ADD COLUMN IF NOT EXISTS source_period_key text;
CREATE UNIQUE INDEX IF NOT EXISTS periods_source_period_key ON periods(source_period_key) WHERE source_period_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS practice_rosters (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 period_key text NOT NULL,
 source_version bigint NOT NULL CHECK(source_version>0),
 source_hash text NOT NULL,
 starts_at timestamptz NOT NULL,
 confirmed_at timestamptz NOT NULL,
 attendance_count int NOT NULL CHECK(attendance_count>=0),
 UNIQUE(period_key,source_version)
);
CREATE TABLE IF NOT EXISTS practice_enrollments (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 period_key text NOT NULL,
 source_enrollment_id text NOT NULL,
 participant_id text NOT NULL,
 membership_type smallint NOT NULL CHECK(membership_type BETWEEN 1 AND 3),
 commission_eligible boolean NOT NULL,
 signed_at timestamptz NOT NULL,
 paid_order_ids text[] NOT NULL,
 hash bytea NOT NULL,
 UNIQUE(period_key,source_enrollment_id,hash)
);
CREATE TABLE IF NOT EXISTS practice_roster_members (
 roster_id bigint NOT NULL REFERENCES practice_rosters(id),
 enrollment_id bigint NOT NULL REFERENCES practice_enrollments(id),
 PRIMARY KEY(roster_id,enrollment_id)
);
CREATE INDEX IF NOT EXISTS practice_enrollments_source_id ON practice_enrollments(source_enrollment_id,participant_id);
CREATE INDEX IF NOT EXISTS practice_roster_members_enrollment ON practice_roster_members(enrollment_id);
ALTER TABLE calculation_drafts ADD COLUMN IF NOT EXISTS practice_roster_id bigint REFERENCES practice_rosters(id);
ALTER TABLE calculation_draft_lines ADD COLUMN IF NOT EXISTS practice_enrollment_id bigint REFERENCES practice_enrollments(id);
ALTER TABLE calculation_draft_issues ADD COLUMN IF NOT EXISTS practice_enrollment_id bigint REFERENCES practice_enrollments(id);

CREATE INDEX IF NOT EXISTS source_order_versions_practice_refund_payment
 ON source_order_versions USING gin ((fact->'refundsPayment') jsonb_path_ops)
 WHERE source='DCZD' AND kind='REFUND_SUCCEEDED';

CREATE TABLE IF NOT EXISTS configuration_options (
 group_key text NOT NULL,
 option_key text NOT NULL,
 label text NOT NULL CHECK (btrim(label)<>''),
 display_order integer NOT NULL,
 enabled boolean NOT NULL DEFAULT true,
 applies_to jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(applies_to)='object'),
 PRIMARY KEY (group_key,option_key)
);

INSERT INTO configuration_options(group_key,option_key,label,display_order,applies_to) VALUES
 ('source','DCZD','大春之道 · DCZD',10,'{}'),
 ('source','DCAI','大春 AI · DCAI',20,'{}'),
 ('source','DCJK','大春见康 · DCJK',30,'{}'),
 ('periodMode','class','实际开班',10,'{}'),
 ('periodMode','calendar','固定账期',20,'{}'),
 ('ruleScope','default','产品默认',10,'{}'),
 ('ruleScope','ordinal','从指定期数起',20,'{}'),
 ('ruleScope','period','指定期次',30,'{}'),
 ('component','sales','销售提成',10,'{}'),
 ('component','referral','推荐提成',20,'{}'),
 ('component','manager','负责人提成',30,'{"periodModes":["calendar"]}'),
 ('component','delivery','交付提成',40,'{"periodModes":["calendar"]}'),
 ('classMember','','全部',10,'{}'),
 ('classMember','new','新报',20,'{}'),
 ('classMember','retrain','复训',30,'{}'),
 ('amountMode','rate','比例 %',10,'{}'),
 ('amountMode','fixed','金额 ¥',20,'{}'),
 ('amountMode','tiered','阶梯',30,'{"periodModes":["class"],"componentTypes":["sales"],"classMembers":["new"]}'),
 ('refundPolicy','proportional','按实际退款比例扣回',10,'{}'),
 ('refundPolicy','full_reversal','有退款即整笔冲回',20,'{}'),
 ('refundPolicy','non_refundable','退款时不扣回',30,'{}'),
 ('dcaiOrderStream','VIP_ORDERS','AI 会员',10,'{}'),
 ('dcaiOrderStream','HEALTH_IDENTITY_ORDERS','健康大使或创客',20,'{}'),
 ('tierMetric','performance','本期新报实付',10,'{}'),
 ('tierMetric','count','累计新报人数',20,'{}'),
 ('tierMetric','fallback','人数优先，未命中按实付',30,'{}'),
 ('tierStartMode','ordinal','从期数开始',10,'{}'),
 ('tierStartMode','time','从开班时间开始',20,'{}')
ON CONFLICT (group_key,option_key) DO NOTHING;

ALTER TABLE people ADD COLUMN IF NOT EXISTS legacy_party_id uuid;
CREATE UNIQUE INDEX IF NOT EXISTS people_legacy_party_id ON people(legacy_party_id);

CREATE TABLE IF NOT EXISTS practice_count_openings (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 product_id bigint NOT NULL REFERENCES products(id),
 source_settlement_id uuid NOT NULL UNIQUE,
 through_at timestamptz NOT NULL,
 source_digest bytea NOT NULL CHECK (octet_length(source_digest)=32),
 imported_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS practice_count_openings_product ON practice_count_openings(product_id,through_at DESC,id DESC);

CREATE TABLE IF NOT EXISTS practice_count_opening_people (
 opening_id bigint NOT NULL REFERENCES practice_count_openings(id),
 person_id bigint NOT NULL REFERENCES people(id),
 new_count bigint NOT NULL CHECK (new_count>=0),
 PRIMARY KEY(opening_id,person_id)
);

INSERT INTO configuration_options(group_key,option_key,label,display_order,applies_to)
 VALUES ('tierStartMode','opening','沿用冻结期初',30,'{"countScopes":["default"]}')
 ON CONFLICT(group_key,option_key) DO NOTHING;
