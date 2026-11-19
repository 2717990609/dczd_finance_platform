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

CREATE TABLE IF NOT EXISTS finance_accounts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 person_id bigint NOT NULL UNIQUE REFERENCES people(id),
 login text NOT NULL UNIQUE,
 password_hash text NOT NULL,
 enabled boolean NOT NULL,
 workspace_access boolean NOT NULL,
 failures int NOT NULL DEFAULT 0,
 retry_after timestamptz
);

CREATE TABLE IF NOT EXISTS finance_account_rule_grants (
 account_id uuid NOT NULL REFERENCES finance_accounts(id) ON DELETE CASCADE,
 product_id bigint NOT NULL REFERENCES products(id),
 can_publish boolean NOT NULL,
 PRIMARY KEY(account_id,product_id)
);

CREATE TABLE IF NOT EXISTS finance_account_settlement_grants (
 account_id uuid NOT NULL REFERENCES finance_accounts(id) ON DELETE CASCADE,
 product_id bigint NOT NULL REFERENCES products(id),
 PRIMARY KEY(account_id,product_id)
);

CREATE TABLE IF NOT EXISTS finance_account_sessions (
 token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash)=32),
 account_id uuid NOT NULL REFERENCES finance_accounts(id) ON DELETE CASCADE,
 expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS finance_sessions_account ON finance_account_sessions(account_id,expires_at);
ALTER TABLE rule_versions ADD COLUMN IF NOT EXISTS published_by uuid REFERENCES finance_accounts(id);

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
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='products'::regclass AND conname='products_cycle_start_day_check'
  AND strpos(pg_get_constraintdef(oid),'period_mode')=0) THEN
  ALTER TABLE products DROP CONSTRAINT IF EXISTS products_check,
   DROP CONSTRAINT IF EXISTS products_cycle_start_day_check,
   ADD CONSTRAINT products_cycle_start_day_check
   CHECK (cycle_start_day IS NULL OR cycle_start_day BETWEEN 1 AND 28);
 END IF;
END
$$;
ALTER TABLE periods ADD COLUMN IF NOT EXISTS calendar_month date;
CREATE UNIQUE INDEX IF NOT EXISTS periods_ordinal_without_month
 ON periods(product_id,ordinal) WHERE calendar_month IS NULL;
ALTER TABLE periods DROP CONSTRAINT IF EXISTS periods_product_id_ordinal_key;
CREATE UNIQUE INDEX IF NOT EXISTS periods_calendar_month
 ON periods(product_id, calendar_month) WHERE calendar_month IS NOT NULL;

ALTER TABLE source_order_versions ADD COLUMN IF NOT EXISTS occurred_at timestamptz;
UPDATE source_order_versions SET occurred_at = (fact->>'occurredAt')::timestamptz WHERE occurred_at IS NULL;
ALTER TABLE source_order_versions ALTER COLUMN occurred_at SET NOT NULL;
CREATE INDEX IF NOT EXISTS source_order_versions_payment_date
 ON source_order_versions(source, (fact->>'domain'), (fact->>'sourceKey'), occurred_at) WHERE kind = 'ORDER_PAID';
CREATE INDEX IF NOT EXISTS source_order_versions_payment_identity
 ON source_order_versions(source, (fact->>'domain'), (fact->>'paymentId'), line_id) WHERE kind = 'ORDER_PAID';

CREATE TABLE IF NOT EXISTS source_write_state (
 source text PRIMARY KEY,
 revision bigint NOT NULL
);
ALTER TABLE source_write_state ADD COLUMN IF NOT EXISTS provider_key text;
INSERT INTO source_write_state(source,revision) VALUES ('CONFIG',0),('DCAI',0),('DCZD',0),('DCJK',0) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS dcai_order_sync (
 stream text PRIMARY KEY,
 source_key text NOT NULL,
 initial_from timestamptz NOT NULL,
 completed_through timestamptz,
 target_through timestamptz,
 next_updated_at timestamptz NOT NULL,
 next_id text NOT NULL,
 pages bigint NOT NULL DEFAULT 0,
 scanned bigint NOT NULL DEFAULT 0,
 versions bigint NOT NULL DEFAULT 0,
 failed boolean NOT NULL DEFAULT false,
 CHECK (completed_through IS NULL OR completed_through>=initial_from),
 CHECK (target_through IS NULL OR target_through>=coalesce(completed_through,initial_from))
);
ALTER TABLE dcai_order_sync ADD COLUMN IF NOT EXISTS last_success_at timestamptz;

CREATE TABLE IF NOT EXISTS dcai_order_blockers (
 stream text NOT NULL,
 source_row_id text NOT NULL,
 order_id text NOT NULL,
 cursor_updated_at timestamptz NOT NULL,
 reasons text[] NOT NULL,
 PRIMARY KEY(stream,source_row_id)
);
CREATE INDEX IF NOT EXISTS dcai_active_order_blockers ON dcai_order_blockers(stream,order_id) WHERE cardinality(reasons)>0;

CREATE TABLE IF NOT EXISTS dcai_sync_jobs (
 stream text PRIMARY KEY,
 id bigint GENERATED BY DEFAULT AS IDENTITY UNIQUE,
 source_key text NOT NULL,
 initial_from timestamptz,
 status text NOT NULL CHECK(status IN ('QUEUED','RUNNING','SUCCEEDED','FAILED')),
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 started_at timestamptz,
 finished_at timestamptz,
 message text NOT NULL DEFAULT ''
);

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
CREATE TABLE IF NOT EXISTS draft_refresh_queue (
 period_id bigint PRIMARY KEY REFERENCES periods(id) ON DELETE CASCADE,
 generation bigint GENERATED BY DEFAULT AS IDENTITY,
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 retry_on_scan boolean NOT NULL DEFAULT true,
 message text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS calculation_draft_issues_page ON calculation_draft_issues(period_id, id);

ALTER TABLE rule_versions ADD COLUMN IF NOT EXISTS effective_from timestamptz;
ALTER TABLE calculation_draft_lines ADD COLUMN IF NOT EXISTS rule_id bigint REFERENCES rule_versions(id);
UPDATE calculation_draft_lines AS line SET rule_id=draft.rule_id
 FROM calculation_drafts AS draft WHERE line.period_id=draft.period_id AND line.rule_id IS NULL AND draft.rule_id IS NOT NULL;

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
ALTER TABLE settlements ADD COLUMN IF NOT EXISTS review_token text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS settlement_removals (
 settlement_id bigint PRIMARY KEY,
 period_id bigint NOT NULL REFERENCES periods(id),
 draft_version bigint NOT NULL,
 removed_by text NOT NULL,
 removed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS settlement_removals_period ON settlement_removals(period_id,settlement_id DESC);

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
 ) OR EXISTS(
    SELECT 1 FROM periods original
    JOIN pool_account_activity a ON a.period_id=original.id
    JOIN periods later ON later.product_id=original.product_id AND later.ordinal>original.ordinal
    JOIN pool_account_activity b ON b.period_id=later.id
      AND (a.unit_id,a.item_key,a.scenario_key)=(b.unit_id,b.item_key,b.scenario_key)
    JOIN settlements s ON s.period_id=later.id WHERE original.id=OLD.period_id
 ) OR EXISTS(
    SELECT 1 FROM calculation_draft_performance o
    JOIN calculation_draft_performance a ON a.payment_version_id=o.source_version_id
    JOIN settlements s ON s.period_id=a.period_id WHERE o.period_id=OLD.period_id AND s.id<>OLD.id
 ) OR EXISTS(
    SELECT 1 FROM calculation_draft_lines l
    JOIN monthly_pool_allocation_funding f ON f.source_line_id=l.id
    JOIN monthly_pool_allocation_shares sh ON sh.id=f.share_id
    JOIN monthly_pool_allocations a ON a.id=sh.allocation_id
    JOIN settlements s ON s.period_id=a.period_id WHERE l.period_id=OLD.period_id AND s.id<>OLD.id
 ) THEN
  RAISE EXCEPTION 'settlement has booked adjustments' USING ERRCODE='55000';
 END IF;
 DELETE FROM calculation_drafts
 WHERE period_id IN (SELECT a.period_id FROM calculation_draft_refunds a
   JOIN calculation_draft_lines l ON l.id=a.original_line_id WHERE l.period_id=OLD.period_id
   UNION SELECT a.period_id FROM calculation_draft_corrections a
   JOIN calculation_draft_lines l ON l.id=a.original_line_id WHERE l.period_id=OLD.period_id
   UNION SELECT a.period_id FROM calculation_draft_performance a
   JOIN calculation_draft_performance o ON o.source_version_id=a.payment_version_id WHERE o.period_id=OLD.period_id)
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
     OR EXISTS(SELECT 1 FROM calculation_draft_performance a JOIN settlements s USING(period_id) WHERE a.source_version_id=OLD.id OR a.payment_version_id=OLD.id)
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
CREATE INDEX IF NOT EXISTS source_order_versions_practice_refund_record
 ON source_order_versions(record_key,updated_at DESC,id DESC)
 WHERE source='DCZD' AND kind='REFUND_SUCCEEDED';
CREATE INDEX IF NOT EXISTS practice_enrollments_paid_orders ON practice_enrollments USING gin(paid_order_ids);
INSERT INTO draft_refresh_queue(period_id)
 SELECT DISTINCT i.period_id FROM calculation_draft_issues i
 WHERE i.code='PRACTICE_REFUND_PENDING' AND NOT EXISTS(SELECT 1 FROM settlements s WHERE s.period_id=i.period_id)
 ON CONFLICT DO NOTHING;

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
 ('practicePeriodKind','class','开班期次',10,'{}'),
 ('practicePeriodKind','refund_month','退款账期',20,'{}'),
 ('practiceRefundCalendar','25','每月25日开始',10,'{}'),
 ('ruleScope','default','产品默认',10,'{}'),
 ('ruleScope','ordinal','从指定期数起',20,'{}'),
 ('ruleScope','period','指定期次',30,'{}'),
 ('component','sales','销售提成',10,'{}'),
 ('component','referral','推荐提成',20,'{}'),
 ('component','manager','负责人提成',30,'{}'),
 ('component','delivery','交付提成',40,'{}'),
 ('component','company_sales','外部销售公司提成',50,'{"periodModes":["class"]}'),
 ('classMember','','全部',10,'{}'),
 ('classMember','new','新报',20,'{}'),
 ('classMember','retrain','复训',30,'{}'),
 ('amountMode','rate','比例 %',10,'{}'),
 ('amountMode','fixed','金额 ¥',20,'{}'),
 ('amountMode','tiered','阶梯',30,'{"periodModes":["class"],"componentTypes":["sales"],"classMembers":["new"]}'),
 ('amountMode','period_rate','班次比例 %',40,'{"periodModes":["class"],"componentTypes":["manager","delivery"]}'),
 ('amountMode','company_sales','按公司类别配置',50,'{"periodModes":["class"],"componentTypes":["company_sales"],"classMembers":["new","retrain"]}'),
 ('practicePeriodRecipient','person','人员',10,'{}'),
 ('practicePeriodRecipient','unit','组织',20,'{}'),
 ('practicePeriodRecipient','pool','资金池',30,'{}'),
 ('practicePeriodSourceRole','DIRECT_REFERRER','推荐销售',10,'{}'),
 ('practicePeriodSourceRole','LOCK_SALES_USER','锁定销售',20,'{}'),
 ('refundPolicy','proportional','按实际退款比例扣回',10,'{}'),
 ('refundPolicy','full_reversal','有退款即整笔冲回',20,'{}'),
 ('refundPolicy','non_refundable','退款时不扣回',30,'{}'),
 ('dcaiOrderStream','VIP_ORDERS','AI 会员',10,'{}'),
 ('dcaiOrderStream','HEALTH_IDENTITY_ORDERS','健康大使或创客',20,'{}'),
 ('dczdOrderStream','ORDER_FACTS','大春之道订单与退款',10,'{}'),
 ('tierMetric','performance','本期新报实付',10,'{}'),
 ('tierMetric','count','累计新报人数',20,'{}'),
 ('tierMetric','fallback','人数优先，未命中按实付',30,'{}'),
 ('tierStartMode','ordinal','从期数开始',10,'{}'),
 ('tierStartMode','time','从开班时间开始',20,'{}')
ON CONFLICT (group_key,option_key) DO NOTHING;
UPDATE configuration_options SET applies_to='{}'
 WHERE group_key='component' AND option_key IN ('manager','delivery')
  AND applies_to='{"periodModes":["calendar"]}'::jsonb;

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

CREATE TABLE IF NOT EXISTS source_payment_companies (
 source_version_id bigint PRIMARY KEY REFERENCES source_order_versions(id),
 company_id text NOT NULL CHECK (length(company_id)>0)
);

ALTER TABLE source_payment_companies
 ADD COLUMN IF NOT EXISTS recommendation_company_id text,
 ADD COLUMN IF NOT EXISTS recommendation_conflict boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS legacy_catalog_products (
 product_id bigint PRIMARY KEY REFERENCES products(id),
 source_id uuid NOT NULL UNIQUE,
 source_environment text NOT NULL,
 project_code text NOT NULL,
 source_system text NOT NULL,
 source_key text NOT NULL,
 captured_at timestamptz NOT NULL,
 snapshot_digest bytea NOT NULL CHECK (octet_length(snapshot_digest)=32)
);

CREATE TABLE IF NOT EXISTS legacy_rule_packages (
 source_id uuid PRIMARY KEY,
 product_id bigint NOT NULL REFERENCES legacy_catalog_products(product_id),
 code text NOT NULL,
 name text NOT NULL,
 project_code text NOT NULL,
 policy_kind text NOT NULL,
 scope_type text NOT NULL,
 scope_id text NOT NULL,
 paused boolean NOT NULL,
 resume_from timestamptz
);
CREATE INDEX IF NOT EXISTS legacy_rule_packages_product ON legacy_rule_packages(product_id);

CREATE TABLE IF NOT EXISTS legacy_rule_versions (
 source_id uuid PRIMARY KEY,
 package_id uuid NOT NULL REFERENCES legacy_rule_packages(source_id),
 version_no integer NOT NULL CHECK(version_no>0),
 source_status text NOT NULL,
 effective_from timestamptz NOT NULL,
 effective_to timestamptz CHECK(effective_to>effective_from),
 content_hash text NOT NULL,
 definition jsonb NOT NULL CHECK(jsonb_typeof(definition)='object'),
 observed_at timestamptz NOT NULL,
 UNIQUE(package_id,version_no)
);
ALTER TABLE legacy_rule_versions
 ADD COLUMN IF NOT EXISTS published_at timestamptz,
 ADD COLUMN IF NOT EXISTS published_by text,
 ADD COLUMN IF NOT EXISTS predecessor_id uuid REFERENCES legacy_rule_versions(source_id);

INSERT INTO configuration_options(group_key,option_key,label,display_order) VALUES
 ('catalogSalesRelationship','ANY','不限',10),
 ('catalogSalesRelationship','DIRECT','直接',20),
 ('catalogSalesRelationship','INDIRECT','间接',30),
 ('catalogSalesRelationship','NONE','无关联',40)
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS organization_units (
 id uuid PRIMARY KEY,
 parent_id uuid REFERENCES organization_units(id) DEFERRABLE INITIALLY DEFERRED,
 kind text NOT NULL CHECK (btrim(kind) <> ''),
 name text NOT NULL CHECK (btrim(name) <> ''),
 status text NOT NULL CHECK (status IN ('ACTIVE','INACTIVE')),
 sort_order integer NOT NULL DEFAULT 100
);

CREATE TABLE IF NOT EXISTS organization_assignments (
 id uuid PRIMARY KEY,
 person_id bigint NOT NULL REFERENCES people(id),
 unit_id uuid NOT NULL REFERENCES organization_units(id),
 kind text NOT NULL CHECK (btrim(kind) <> ''),
 position text NOT NULL DEFAULT '',
 valid_from timestamptz NOT NULL,
 valid_to timestamptz CHECK (valid_to > valid_from)
);
CREATE INDEX IF NOT EXISTS organization_assignments_unit_time
 ON organization_assignments(unit_id,valid_from,valid_to);
CREATE INDEX IF NOT EXISTS organization_assignments_person_time
 ON organization_assignments(person_id,valid_from,valid_to);

CREATE TABLE IF NOT EXISTS organization_import_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
 captured_at timestamptz NOT NULL,
 digest bytea NOT NULL
);

INSERT INTO configuration_options(group_key,option_key,label,display_order) VALUES
 ('organizationKind','COMPANY','公司',10),
 ('organizationKind','DEPARTMENT','部门',20),
 ('organizationKind','TEAM','小组',30),
 ('organizationKind','STORE','门店',40),
 ('appointmentKind','PRIMARY','主要任职',10),
 ('appointmentKind','CONCURRENT','兼任',20),
 ('appointmentKind','MEMBER','公司成员',30),
 ('appointmentKind','MANAGER','公司管理人员',40),
 ('appointmentKind','OWNER','公司负责人',50)
ON CONFLICT DO NOTHING;

ALTER TABLE calculation_drafts ADD COLUMN IF NOT EXISTS performance_ready boolean NOT NULL DEFAULT false;
ALTER TABLE calculation_draft_lines ADD COLUMN IF NOT EXISTS company_id uuid REFERENCES organization_units(id);
ALTER TABLE calculation_draft_lines ADD COLUMN IF NOT EXISTS department_id uuid REFERENCES organization_units(id);
ALTER TABLE calculation_draft_corrections ADD COLUMN IF NOT EXISTS company_id uuid REFERENCES organization_units(id);
ALTER TABLE calculation_draft_corrections ADD COLUMN IF NOT EXISTS department_id uuid REFERENCES organization_units(id);

CREATE TABLE IF NOT EXISTS organization_company_sources (
 source text NOT NULL,
 source_company_id text NOT NULL,
 company_id uuid NOT NULL REFERENCES organization_units(id),
 valid_from timestamptz NOT NULL,
 valid_to timestamptz CHECK (valid_to > valid_from),
 PRIMARY KEY(source,source_company_id,valid_from)
);

CREATE TABLE IF NOT EXISTS organization_company_recommendations (
 company_id uuid PRIMARY KEY REFERENCES organization_units(id),
 referrer_company_id uuid REFERENCES organization_units(id),
 CHECK (company_id<>referrer_company_id)
);

CREATE TABLE IF NOT EXISTS calculation_draft_performance (
 period_id bigint NOT NULL REFERENCES calculation_drafts(period_id) ON DELETE CASCADE,
 source_version_id bigint NOT NULL REFERENCES source_order_versions(id),
 payment_version_id bigint REFERENCES source_order_versions(id),
 person_id bigint REFERENCES people(id),
 company_id uuid REFERENCES organization_units(id),
 department_id uuid REFERENCES organization_units(id),
 PRIMARY KEY(period_id,source_version_id),
 CHECK (source_version_id<>payment_version_id)
);
CREATE INDEX IF NOT EXISTS calculation_performance_source ON calculation_draft_performance(source_version_id);
CREATE INDEX IF NOT EXISTS calculation_performance_payment ON calculation_draft_performance(payment_version_id);
CREATE OR REPLACE TRIGGER protect_confirmed_performance BEFORE INSERT OR UPDATE OR DELETE ON calculation_draft_performance
 FOR EACH ROW EXECUTE FUNCTION protect_final_calculation();

INSERT INTO configuration_options(group_key,option_key,label,display_order) VALUES
 ('draftReportDimension','person','人员',10),
 ('draftReportDimension','company','公司',20),
 ('draftReportDimension','department','部门',30)
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS catalog_business_routes (
 source_id uuid PRIMARY KEY,
 product_id bigint NOT NULL REFERENCES legacy_catalog_products(product_id),
 source text NOT NULL,
 domain text NOT NULL,
 entity_type text NOT NULL,
 record_table text NOT NULL,
 business_key text NOT NULL,
 classifier text NOT NULL,
 sku_code text NOT NULL,
 sku_label text NOT NULL,
 mapping_version integer NOT NULL CHECK(mapping_version>0),
 effective_from timestamptz NOT NULL,
 effective_to timestamptz CHECK(effective_to>effective_from),
 observed_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS catalog_business_route_scope
 ON catalog_business_routes(product_id,observed_at,source,domain,record_table);

CREATE TABLE IF NOT EXISTS source_order_classifications (
 source_version_id bigint PRIMARY KEY REFERENCES source_order_versions(id),
 entity_type text NOT NULL,
 business_key text NOT NULL,
 classifier text NOT NULL
);
CREATE INDEX IF NOT EXISTS catalog_payment_record_scope
 ON source_order_versions(source,(fact->>'domain'),(split_part(record_key,':',1)),occurred_at)
 WHERE kind='ORDER_PAID';

ALTER TABLE source_order_classifications ADD COLUMN IF NOT EXISTS sales_claims jsonb;

CREATE TABLE IF NOT EXISTS catalog_sales_context (
 source text PRIMARY KEY,
 captured_at timestamptz NOT NULL,
 snapshot_digest bytea NOT NULL
);

CREATE TABLE IF NOT EXISTS catalog_sales_people_sources (
 id uuid PRIMARY KEY,
 source text NOT NULL REFERENCES catalog_sales_context(source),
 external_id text NOT NULL CHECK (external_id<>''),
 person_id bigint NOT NULL REFERENCES people(id),
 valid_from timestamptz NOT NULL,
 valid_to timestamptz CHECK (valid_to>valid_from),
 observed_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS catalog_sales_person_lookup ON catalog_sales_people_sources(source,external_id,observed_at);

CREATE TABLE IF NOT EXISTS catalog_sales_company_aliases (
 id uuid PRIMARY KEY,
 source text NOT NULL REFERENCES catalog_sales_context(source),
 external_id text NOT NULL CHECK (external_id<>''),
 company_uuid uuid NOT NULL,
 company_unit_id uuid NOT NULL REFERENCES organization_units(id),
 valid_from timestamptz NOT NULL,
 valid_to timestamptz CHECK (valid_to>valid_from),
 observed_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS catalog_sales_company_lookup ON catalog_sales_company_aliases(source,external_id,observed_at);

CREATE TABLE IF NOT EXISTS catalog_sales_categories (
 source text NOT NULL REFERENCES catalog_sales_context(source),
 company_uuid uuid NOT NULL,
 category_uuid uuid,
 valid_from timestamptz NOT NULL,
 valid_to timestamptz CHECK (valid_to>valid_from),
 observed_at timestamptz NOT NULL,
 PRIMARY KEY(source,company_uuid,valid_from)
);

CREATE TABLE IF NOT EXISTS catalog_sales_appointments (
 id uuid PRIMARY KEY,
 source text NOT NULL REFERENCES catalog_sales_context(source),
 person_id bigint NOT NULL REFERENCES people(id),
 role text NOT NULL CHECK (role<>''),
 scope text NOT NULL CHECK (scope IN ('PRODUCT','PROJECT','ORGANIZATION_UNIT')),
 scope_key text NOT NULL CHECK (scope_key<>''),
 valid_from timestamptz NOT NULL,
 valid_to timestamptz CHECK (valid_to>valid_from),
 observed_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS catalog_sales_appointment_lookup ON catalog_sales_appointments(source,scope,scope_key,observed_at);

ALTER TABLE catalog_sales_context ADD COLUMN IF NOT EXISTS benefit_ready boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS catalog_benefit_organizations (
 source text NOT NULL REFERENCES catalog_sales_context(source),
 party_uuid uuid NOT NULL,
 unit_id uuid NOT NULL REFERENCES organization_units(id),
 observed_at timestamptz NOT NULL,
 PRIMARY KEY(source,party_uuid,unit_id)
);

ALTER TABLE calculation_drafts ALTER COLUMN rule_id DROP NOT NULL;
ALTER TABLE calculation_drafts ADD COLUMN IF NOT EXISTS catalog_refunds_ready boolean NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS practice_pools (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 product_id bigint NOT NULL REFERENCES products(id),
 unit_id uuid NOT NULL REFERENCES organization_units(id),
 name text NOT NULL CHECK (length(btrim(name))>0),
 UNIQUE(product_id,unit_id,name)
);

ALTER TABLE calculation_draft_lines
 ALTER COLUMN rule_id DROP NOT NULL,
 ALTER COLUMN identity_id DROP NOT NULL,
 ALTER COLUMN person_id DROP NOT NULL,
 ADD COLUMN IF NOT EXISTS catalog_rule_id uuid REFERENCES legacy_rule_versions(source_id),
 ADD COLUMN IF NOT EXISTS beneficiary_unit_id uuid REFERENCES organization_units(id),
 ADD COLUMN IF NOT EXISTS allocation_owner_id bigint REFERENCES people(id),
 ADD COLUMN IF NOT EXISTS practice_pool_id bigint REFERENCES practice_pools(id),
 ADD COLUMN IF NOT EXISTS applied_rate_bps integer
  CHECK (applied_rate_bps IS NULL OR (applied_rate_bps BETWEEN 0 AND 10000 AND catalog_rule_id IS NOT NULL));

DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='calculation_draft_lines'::regclass
   AND conname='draft_line_rule_and_recipient_check' AND strpos(pg_get_constraintdef(oid),'practice_pool_id')>0) THEN
  ALTER TABLE calculation_draft_lines DROP CONSTRAINT IF EXISTS draft_line_rule_and_recipient_check;
  ALTER TABLE calculation_draft_lines ADD CONSTRAINT draft_line_rule_and_recipient_check CHECK (
   (catalog_rule_id IS NULL AND rule_id IS NOT NULL AND identity_id IS NOT NULL AND person_id IS NOT NULL
     AND beneficiary_unit_id IS NULL AND allocation_owner_id IS NULL AND practice_pool_id IS NULL)
   OR
   (catalog_rule_id IS NULL AND rule_id IS NOT NULL AND identity_id IS NULL AND practice_enrollment_id IS NOT NULL
     AND starts_with(component_type,'period:') AND ((person_id IS NOT NULL) <> (beneficiary_unit_id IS NOT NULL))
     AND ((practice_pool_id IS NULL AND allocation_owner_id IS NULL)
      OR (practice_pool_id IS NOT NULL AND allocation_owner_id IS NOT NULL AND beneficiary_unit_id IS NOT NULL)))
   OR
   (catalog_rule_id IS NOT NULL AND rule_id IS NULL AND identity_id IS NULL AND practice_enrollment_id IS NULL
     AND practice_pool_id IS NULL
     AND ((person_id IS NOT NULL AND beneficiary_unit_id IS NULL AND allocation_owner_id IS NULL)
       OR (person_id IS NULL AND beneficiary_unit_id IS NOT NULL)))
  );
 END IF;
END $$;

CREATE INDEX IF NOT EXISTS calculation_pool_lines_period ON calculation_draft_lines(period_id)
 WHERE allocation_owner_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS monthly_pool_allocations (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 period_id bigint NOT NULL REFERENCES calculation_drafts(period_id) ON DELETE CASCADE,
 unit_id uuid NOT NULL REFERENCES organization_units(id),
 item_key text NOT NULL,
 scenario_key text NOT NULL,
 draft_version bigint NOT NULL CHECK (draft_version>0),
 basis_token text NOT NULL,
 revision bigint NOT NULL CHECK (revision>0),
 UNIQUE(period_id,unit_id,item_key,scenario_key)
);

CREATE TABLE IF NOT EXISTS monthly_pool_allocation_shares (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 allocation_id bigint NOT NULL REFERENCES monthly_pool_allocations(id) ON DELETE CASCADE,
 person_id bigint REFERENCES people(id),
 amount_minor bigint NOT NULL CHECK (amount_minor>0),
 UNIQUE NULLS NOT DISTINCT(allocation_id,person_id)
);

CREATE TABLE IF NOT EXISTS monthly_pool_allocation_funding (
 share_id bigint NOT NULL REFERENCES monthly_pool_allocation_shares(id) ON DELETE CASCADE,
 source_line_id bigint NOT NULL REFERENCES calculation_draft_lines(id) ON DELETE CASCADE,
 amount_minor bigint NOT NULL CHECK (amount_minor>0),
 PRIMARY KEY(share_id,source_line_id)
);
CREATE INDEX IF NOT EXISTS monthly_pool_funding_source ON monthly_pool_allocation_funding(source_line_id);

CREATE OR REPLACE TRIGGER protect_confirmed_pool_allocations BEFORE INSERT OR UPDATE OR DELETE ON monthly_pool_allocations
 FOR EACH ROW EXECUTE FUNCTION protect_final_calculation();

CREATE OR REPLACE FUNCTION protect_confirmed_pool_children() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 allocation_ids bigint[];
 share_ids bigint[];
BEGIN
 IF TG_TABLE_NAME='monthly_pool_allocation_shares' THEN
  IF TG_OP<>'INSERT' THEN allocation_ids=array_append(allocation_ids,OLD.allocation_id); END IF;
  IF TG_OP<>'DELETE' THEN allocation_ids=array_append(allocation_ids,NEW.allocation_id); END IF;
 ELSE
  IF TG_OP<>'INSERT' THEN share_ids=array_append(share_ids,OLD.share_id); END IF;
  IF TG_OP<>'DELETE' THEN share_ids=array_append(share_ids,NEW.share_id); END IF;
  SELECT array_agg(allocation_id) INTO allocation_ids FROM monthly_pool_allocation_shares WHERE id=ANY(share_ids);
 END IF;
 IF EXISTS(SELECT 1 FROM monthly_pool_allocations a JOIN settlements s USING(period_id) WHERE a.id=ANY(allocation_ids)) THEN
  RAISE EXCEPTION 'confirmed pool allocation cannot be changed' USING ERRCODE='55000';
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE TRIGGER protect_confirmed_pool_shares BEFORE INSERT OR UPDATE OR DELETE ON monthly_pool_allocation_shares
 FOR EACH ROW EXECUTE FUNCTION protect_confirmed_pool_children();
CREATE OR REPLACE TRIGGER protect_confirmed_pool_funding BEFORE INSERT OR UPDATE OR DELETE ON monthly_pool_allocation_funding
 FOR EACH ROW EXECUTE FUNCTION protect_confirmed_pool_children();

CREATE OR REPLACE VIEW pool_line_accounts AS
 SELECT l.id AS line_id,l.period_id,l.beneficiary_unit_id AS unit_id,
  coalesce(l.practice_pool_id::text,l.component_type) AS item_key,
  CASE WHEN l.practice_pool_id IS NOT NULL THEN 'class' ELSE p.scope_id END AS scenario_key
 FROM calculation_draft_lines l LEFT JOIN legacy_rule_versions v ON v.source_id=l.catalog_rule_id
 LEFT JOIN legacy_rule_packages p ON p.source_id=v.package_id
 WHERE l.allocation_owner_id IS NOT NULL;

CREATE OR REPLACE VIEW pool_account_activity AS
 SELECT period_id,unit_id,item_key,scenario_key FROM pool_line_accounts
 UNION ALL
 SELECT r.period_id,a.unit_id,a.item_key,a.scenario_key
 FROM calculation_draft_refunds r JOIN pool_line_accounts a ON a.line_id=r.original_line_id
 WHERE r.amount_minor<0
 UNION ALL
 SELECT a.period_id,a.unit_id,a.item_key,a.scenario_key FROM monthly_pool_allocations a
 JOIN calculation_drafts d ON d.period_id=a.period_id AND d.version=a.draft_version
 WHERE EXISTS(SELECT 1 FROM monthly_pool_allocation_shares s WHERE s.allocation_id=a.id);

CREATE OR REPLACE FUNCTION protect_confirmed_catalog_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='legacy_rule_versions' THEN
  IF OLD.published_at IS NOT NULL OR EXISTS(SELECT 1 FROM calculation_draft_lines l JOIN settlements s USING(period_id) WHERE l.catalog_rule_id=OLD.source_id) THEN
   RAISE EXCEPTION 'confirmed rule version cannot be changed' USING ERRCODE='55000';
  END IF;
 ELSIF TG_TABLE_NAME='legacy_rule_packages' THEN
  IF EXISTS(SELECT 1 FROM legacy_rule_versions v JOIN calculation_draft_lines l ON l.catalog_rule_id=v.source_id
   JOIN settlements s USING(period_id) WHERE v.package_id=OLD.source_id) THEN
   RAISE EXCEPTION 'confirmed rule scope cannot be changed' USING ERRCODE='55000';
  END IF;
 ELSIF EXISTS(SELECT 1 FROM settlements WHERE period_id=OLD.id) THEN
  RAISE EXCEPTION 'confirmed period scope cannot be changed' USING ERRCODE='55000';
 END IF;
 RETURN NEW;
END $$;

CREATE INDEX IF NOT EXISTS calculation_draft_catalog_rules ON calculation_draft_lines(catalog_rule_id) WHERE catalog_rule_id IS NOT NULL;
CREATE OR REPLACE TRIGGER protect_confirmed_catalog_version BEFORE UPDATE ON legacy_rule_versions FOR EACH ROW
 WHEN ((OLD.source_id,OLD.package_id,OLD.version_no,OLD.effective_from,OLD.content_hash,OLD.definition) IS DISTINCT FROM
       (NEW.source_id,NEW.package_id,NEW.version_no,NEW.effective_from,NEW.content_hash,NEW.definition))
 EXECUTE FUNCTION protect_confirmed_catalog_scope();
CREATE OR REPLACE TRIGGER protect_published_catalog_version BEFORE UPDATE ON legacy_rule_versions FOR EACH ROW
 WHEN (OLD.published_at IS NOT NULL AND OLD IS DISTINCT FROM NEW)
 EXECUTE FUNCTION protect_confirmed_catalog_scope();
CREATE OR REPLACE TRIGGER protect_published_catalog_version_delete BEFORE DELETE ON legacy_rule_versions FOR EACH ROW
 WHEN (OLD.published_at IS NOT NULL) EXECUTE FUNCTION protect_confirmed_catalog_scope();
CREATE OR REPLACE TRIGGER protect_confirmed_catalog_package BEFORE UPDATE ON legacy_rule_packages FOR EACH ROW
 WHEN ((OLD.source_id,OLD.product_id,OLD.code,OLD.project_code,OLD.policy_kind,OLD.scope_type,OLD.scope_id) IS DISTINCT FROM
       (NEW.source_id,NEW.product_id,NEW.code,NEW.project_code,NEW.policy_kind,NEW.scope_type,NEW.scope_id))
 EXECUTE FUNCTION protect_confirmed_catalog_scope();
CREATE OR REPLACE TRIGGER protect_confirmed_period_scope BEFORE UPDATE ON periods FOR EACH ROW
 WHEN ((OLD.id,OLD.product_id,OLD.ordinal,OLD.starts_on,OLD.ends_on,OLD.source_period_key,OLD.calendar_month) IS DISTINCT FROM
       (NEW.id,NEW.product_id,NEW.ordinal,NEW.starts_on,NEW.ends_on,NEW.source_period_key,NEW.calendar_month))
 EXECUTE FUNCTION protect_confirmed_catalog_scope();
