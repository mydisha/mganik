-- +goose Up
-- Verifikasi keaslian (scan, aturan radar, anomali) dan loyalty (klaim PIN, ledger poin).

-- Setiap scan dicatat apa adanya, termasuk kode palsu. Volume tertinggi:
-- BRIN untuk rentang waktu, partisi bulanan nanti.
CREATE TABLE scan_events (
  id                uuid             PRIMARY KEY,
  raw_code          text             NOT NULL,
  serial_unit_id    uuid             REFERENCES serial_units (id),
  context           text             NOT NULL,
  consumer_id       uuid             REFERENCES consumers (id),
  -- HMAC fingerprint perangkat/IP, bukan nilai mentah.
  actor_fingerprint bytea,
  ip_hash           bytea,
  lat               double precision,
  lng               double precision,
  accuracy_m        integer,
  geo_source        text             NOT NULL DEFAULT 'NONE',
  region_code       text             REFERENCES regions (code),
  result            text             NOT NULL,
  rule_hits         text[]           NOT NULL DEFAULT '{}',
  occurred_at       timestamptz      NOT NULL DEFAULT now(),
  CONSTRAINT scan_context_valid    CHECK (context IN ('CONSUMER_VERIFY', 'LOYALTY_CLAIM', 'POS_CHECKOUT', 'TEST_BUY')),
  CONSTRAINT scan_geo_source_valid CHECK (geo_source IN ('GPS', 'IP', 'NONE')),
  CONSTRAINT scan_result_valid     CHECK (result IN ('GENUINE_FIRST', 'GENUINE_REPEAT', 'SUSPICIOUS', 'NOT_FOUND', 'RECALLED')),
  CONSTRAINT scan_raw_code_length  CHECK (char_length(raw_code) BETWEEN 1 AND 512),
  CONSTRAINT scan_lat_range        CHECK (lat BETWEEN -90 AND 90),
  CONSTRAINT scan_lng_range        CHECK (lng BETWEEN -180 AND 180),
  CONSTRAINT scan_accuracy_nonneg  CHECK (accuracy_m >= 0),
  CONSTRAINT scan_geo_consistent   CHECK ((lat IS NULL) = (lng IS NULL) AND (geo_source = 'NONE') = (lat IS NULL))
);
CREATE INDEX scan_events_unit_time ON scan_events (serial_unit_id, occurred_at DESC);
CREATE INDEX scan_events_occurred_brin ON scan_events USING brin (occurred_at);
CREATE INDEX scan_events_fingerprint_time ON scan_events (actor_fingerprint, occurred_at);
CREATE INDEX scan_events_region_time ON scan_events (region_code, occurred_at);

CREATE TRIGGER scan_events_append_only
  BEFORE UPDATE OR DELETE ON scan_events
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER scan_events_no_truncate
  BEFORE TRUNCATE ON scan_events
  FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

-- Aturan anomali sebagai data: ambang diubah tanpa deploy.
-- consumer_effect = hasil yang dilihat konsumen saat aturan kena
-- (NONE = internal saja, misal OUT_OF_TERRITORY).
CREATE TABLE anomaly_rules (
  code            text        PRIMARY KEY,
  name            text        NOT NULL,
  description     text        NOT NULL,
  category        text        NOT NULL,
  enabled         boolean     NOT NULL,
  severity        text        NOT NULL,
  consumer_effect text        NOT NULL,
  params          jsonb       NOT NULL DEFAULT '{}',
  version         bigint      NOT NULL DEFAULT 0,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT anomaly_rule_category_valid CHECK (category IN ('SCAN', 'CLAIM', 'POS', 'STOCK')),
  CONSTRAINT anomaly_rule_severity_valid CHECK (severity IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
  CONSTRAINT anomaly_rule_effect_valid   CHECK (consumer_effect IN ('NONE', 'NOT_FOUND', 'SUSPICIOUS', 'RECALLED')),
  CONSTRAINT anomaly_rule_params_object  CHECK (jsonb_typeof(params) = 'object')
);

-- Satu baris per kejadian unik (dedupe_key); kejadian berulang menaikkan hit_count.
CREATE TABLE anomalies (
  id              uuid        PRIMARY KEY,
  rule_code       text        NOT NULL REFERENCES anomaly_rules (code),
  severity        text        NOT NULL,
  status          text        NOT NULL DEFAULT 'OPEN',
  subject_type    text        NOT NULL,
  subject_id      uuid,
  party_id        uuid        REFERENCES parties (id),
  region_code     text        REFERENCES regions (code),
  dedupe_key      text        NOT NULL,
  hit_count       integer     NOT NULL DEFAULT 1,
  evidence        jsonb       NOT NULL DEFAULT '{}',
  first_seen_at   timestamptz NOT NULL,
  last_seen_at    timestamptz NOT NULL,
  resolved_by     uuid        REFERENCES users (id),
  resolved_at     timestamptz,
  resolution_note text,
  version         bigint      NOT NULL DEFAULT 0,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT anomaly_dedupe_once     UNIQUE (dedupe_key),
  CONSTRAINT anomaly_severity_valid  CHECK (severity IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
  CONSTRAINT anomaly_status_valid    CHECK (status IN ('OPEN', 'ACK', 'RESOLVED', 'FALSE_POSITIVE')),
  CONSTRAINT anomaly_hit_count_pos   CHECK (hit_count > 0),
  CONSTRAINT anomaly_seen_ordered    CHECK (last_seen_at >= first_seen_at),
  CONSTRAINT anomaly_evidence_object CHECK (jsonb_typeof(evidence) = 'object')
);
CREATE INDEX anomalies_queue ON anomalies (status, last_seen_at DESC);
CREATE INDEX anomalies_rule_status ON anomalies (rule_code, status);
CREATE INDEX anomalies_party ON anomalies (party_id);
CREATE INDEX anomalies_subject ON anomalies (subject_type, subject_id);

CREATE TABLE loyalty_claims (
  id             uuid        PRIMARY KEY,
  serial_unit_id uuid        NOT NULL REFERENCES serial_units (id),
  consumer_id    uuid        NOT NULL REFERENCES consumers (id),
  status         text        NOT NULL,
  points         bigint      NOT NULL,
  risk_score     integer     NOT NULL,
  risk_reasons   jsonb       NOT NULL DEFAULT '[]',
  -- Akhir masa tunggu; poin baru bisa dipakai setelah ini.
  available_at   timestamptz NOT NULL,
  -- Sengaja tanpa FK: scan_events akan dipartisi bulanan (PLANNING §5.8,
  -- "tidak ada FK masuk ke tabel event"). Konsistensi dijaga service.
  scan_event_id  uuid,
  decided_by     uuid        REFERENCES users (id),
  decided_at     timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT claim_status_valid      CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'REVERSED')),
  CONSTRAINT claim_points_nonneg     CHECK (points >= 0),
  CONSTRAINT claim_risk_score_range  CHECK (risk_score BETWEEN 0 AND 100),
  CONSTRAINT claim_risk_reasons_array CHECK (jsonb_typeof(risk_reasons) = 'array')
);
-- Klaim yang ditolak tidak mengunci pembeli asli.
CREATE UNIQUE INDEX one_active_claim_per_unit ON loyalty_claims (serial_unit_id)
  WHERE status IN ('PENDING', 'APPROVED');
CREATE INDEX loyalty_claims_consumer ON loyalty_claims (consumer_id, created_at);
CREATE INDEX loyalty_claims_pending_due ON loyalty_claims (available_at) WHERE status = 'PENDING';

-- Ledger poin bertanda. Saldo konsumen = SUM(points).
CREATE TABLE loyalty_ledger (
  id              uuid        PRIMARY KEY,
  consumer_id     uuid        NOT NULL REFERENCES consumers (id),
  entry_type      text        NOT NULL,
  points          bigint      NOT NULL,
  ref_type        text        NOT NULL,
  ref_id          uuid        NOT NULL,
  idempotency_key text        NOT NULL,
  occurred_at     timestamptz NOT NULL DEFAULT now(),
  recorded_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT loyalty_ledger_idempotent  UNIQUE (idempotency_key),
  CONSTRAINT loyalty_entry_type_valid   CHECK (entry_type IN ('EARN', 'REDEEM', 'EXPIRE', 'REVERSE', 'ADJUST')),
  CONSTRAINT loyalty_points_sign        CHECK (
    (entry_type = 'EARN' AND points > 0) OR
    (entry_type IN ('REDEEM', 'EXPIRE') AND points < 0) OR
    (entry_type IN ('REVERSE', 'ADJUST') AND points <> 0)
  )
);
CREATE INDEX loyalty_ledger_consumer_time ON loyalty_ledger (consumer_id, occurred_at);
CREATE INDEX loyalty_ledger_ref ON loyalty_ledger (ref_type, ref_id);

CREATE TRIGGER loyalty_ledger_append_only
  BEFORE UPDATE OR DELETE ON loyalty_ledger
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER loyalty_ledger_no_truncate
  BEFORE TRUNCATE ON loyalty_ledger
  FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

-- +goose Down
DROP TABLE IF EXISTS loyalty_ledger;
DROP TABLE IF EXISTS loyalty_claims;
DROP TABLE IF EXISTS anomalies;
DROP TABLE IF EXISTS anomaly_rules;
DROP TABLE IF EXISTS scan_events;
