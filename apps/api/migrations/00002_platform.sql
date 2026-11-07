-- +goose Up
-- Infrastruktur lintas modul: outbox event, idempotency key HTTP, dan audit log.

-- Outbox: efek samping yang boleh menyusul ditulis di transaksi yang sama dengan
-- perubahan datanya, lalu diterbitkan worker (FOR UPDATE SKIP LOCKED).
CREATE TABLE outbox_events (
  id              uuid        PRIMARY KEY,
  event_type      text        NOT NULL,
  aggregate_type  text        NOT NULL,
  aggregate_id    uuid        NOT NULL,
  payload         jsonb       NOT NULL,
  attempts        integer     NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  published_at    timestamptz,
  last_error      text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  -- Payload berversi, misal reservation.preempted.v1.
  CONSTRAINT outbox_event_type_versioned CHECK (event_type ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+\.v[0-9]+$'),
  CONSTRAINT outbox_attempts_nonneg      CHECK (attempts >= 0),
  CONSTRAINT outbox_payload_object       CHECK (jsonb_typeof(payload) = 'object')
);
-- Antrean worker: hanya event yang belum terbit.
CREATE INDEX outbox_events_pending ON outbox_events (next_attempt_at) WHERE published_at IS NULL;
CREATE INDEX outbox_events_aggregate ON outbox_events (aggregate_type, aggregate_id);

-- Idempotency-Key untuk POST pembuat data. scope memisahkan pemilik key
-- (misal "user:<id>:orders.create") supaya key klien berbeda tidak bertabrakan.
CREATE TABLE idempotency_keys (
  id            uuid        PRIMARY KEY,
  scope         text        NOT NULL,
  key           text        NOT NULL,
  request_hash  bytea       NOT NULL,
  state         text        NOT NULL DEFAULT 'IN_PROGRESS',
  response_code integer,
  response_body jsonb,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,
  CONSTRAINT idempotency_key_once      UNIQUE (scope, key),
  CONSTRAINT idempotency_state_valid   CHECK (state IN ('IN_PROGRESS', 'DONE')),
  CONSTRAINT idempotency_key_length    CHECK (char_length(key) BETWEEN 1 AND 255),
  CONSTRAINT idempotency_done_response CHECK (state <> 'DONE' OR response_code IS NOT NULL)
);
CREATE INDEX idempotency_keys_expiry ON idempotency_keys (expires_at);

-- Audit aksi manusia yang sensitif. Tanpa FK ke users supaya ramah partisi dan
-- tetap utuh walau aktor diarsipkan.
CREATE TABLE audit_logs (
  id         uuid        PRIMARY KEY,
  actor_type text        NOT NULL,
  actor_id   uuid,
  action     text        NOT NULL,
  entity     text        NOT NULL,
  entity_id  uuid,
  before     jsonb,
  after      jsonb,
  reason     text,
  request_id text,
  ip_hash    bytea,
  at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT audit_actor_type_valid CHECK (actor_type IN ('USER', 'DEVICE', 'SYSTEM', 'CONSUMER'))
);
CREATE INDEX audit_logs_entity ON audit_logs (entity, entity_id, at);
CREATE INDEX audit_logs_actor ON audit_logs (actor_type, actor_id, at);

-- Append-only: UPDATE/DELETE per baris dan TRUNCATE ditolak.
CREATE TRIGGER audit_logs_append_only
  BEFORE UPDATE OR DELETE ON audit_logs
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER audit_logs_no_truncate
  BEFORE TRUNCATE ON audit_logs
  FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

-- +goose Down
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS outbox_events;
