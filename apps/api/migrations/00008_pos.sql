-- +goose Up
-- POS offline-first: device, transaksi yang disinkronkan, dan konflik alokasi.

CREATE TABLE pos_devices (
  id              uuid        PRIMARY KEY,
  location_id     uuid        NOT NULL REFERENCES locations (id),
  name            text        NOT NULL,
  -- SHA-256 token device; token asli hanya dipegang device.
  token_hash      bytea       NOT NULL,
  -- Signing key HMAC transaksi, terenkripsi AES-GCM (diisi saat provisioning).
  signing_key_enc bytea,
  status          text        NOT NULL DEFAULT 'ACTIVE',
  last_seen_at    timestamptz,
  last_acked_seq  bigint      NOT NULL DEFAULT 0,
  provisioned_by  uuid        REFERENCES users (id),
  revoked_at      timestamptz,
  version         bigint      NOT NULL DEFAULT 0,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT pos_device_token_once     UNIQUE (token_hash),
  CONSTRAINT pos_device_token_sha256   CHECK (octet_length(token_hash) = 32),
  CONSTRAINT pos_device_status_valid   CHECK (status IN ('ACTIVE', 'REVOKED')),
  CONSTRAINT pos_device_revoked_consistent CHECK ((status = 'REVOKED') = (revoked_at IS NOT NULL)),
  CONSTRAINT pos_device_acked_seq_nonneg   CHECK (last_acked_seq >= 0)
);
CREATE INDEX pos_devices_location ON pos_devices (location_id);

-- Satu baris per transaksi device. Hasil asli disimpan untuk di-replay saat
-- batch yang sama dikirim ulang.
CREATE TABLE pos_transactions (
  id            uuid        PRIMARY KEY,
  device_id     uuid        NOT NULL REFERENCES pos_devices (id),
  -- UUIDv7 yang dibuat device saat offline.
  client_txn_id uuid        NOT NULL,
  device_seq    bigint      NOT NULL,
  location_id   uuid        NOT NULL REFERENCES locations (id),
  cashier_id    uuid        NOT NULL REFERENCES users (id),
  occurred_at   timestamptz NOT NULL,
  received_at   timestamptz NOT NULL DEFAULT now(),
  clock_skew_ms bigint,
  payload_hash  bytea       NOT NULL,
  result        text        NOT NULL,
  result_detail jsonb       NOT NULL DEFAULT '{}',
  order_id      uuid        REFERENCES orders (id),
  CONSTRAINT pos_txn_once            UNIQUE (device_id, client_txn_id),
  CONSTRAINT pos_seq_once            UNIQUE (device_id, device_seq),
  CONSTRAINT pos_device_seq_positive CHECK (device_seq > 0),
  CONSTRAINT pos_result_valid        CHECK (result IN ('ACCEPTED', 'ACCEPTED_WITH_CONFLICT', 'REJECTED')),
  CONSTRAINT pos_payload_hash_sha256 CHECK (octet_length(payload_hash) = 32),
  CONSTRAINT pos_result_detail_object CHECK (jsonb_typeof(result_detail) = 'object')
);
CREATE INDEX pos_transactions_location_time ON pos_transactions (location_id, occurred_at);
CREATE INDEX pos_transactions_order ON pos_transactions (order_id);
CREATE INDEX pos_transactions_cashier ON pos_transactions (cashier_id);

CREATE TABLE allocation_conflicts (
  id                 uuid        PRIMARY KEY,
  kind               text        NOT NULL,
  pos_transaction_id uuid        REFERENCES pos_transactions (id),
  reservation_id     uuid        REFERENCES stock_reservations (id),
  order_id           uuid        REFERENCES orders (id),
  location_id        uuid        NOT NULL REFERENCES locations (id),
  product_id         uuid        NOT NULL REFERENCES products (id),
  serial_unit_id     uuid        REFERENCES serial_units (id),
  qty                bigint,
  status             text        NOT NULL DEFAULT 'OPEN',
  resolution         text,
  details            jsonb       NOT NULL DEFAULT '{}',
  resolved_by        uuid        REFERENCES users (id),
  resolved_at        timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT conflict_kind_valid   CHECK (kind IN ('OFFLINE_SALE_PREEMPT', 'SERIAL_DOUBLE_SALE', 'STOCK_DISCREPANCY')),
  CONSTRAINT conflict_status_valid CHECK (status IN ('OPEN', 'AUTO_RESOLVED', 'RESOLVED')),
  CONSTRAINT conflict_resolved_consistent CHECK ((status = 'OPEN') = (resolved_at IS NULL)),
  CONSTRAINT conflict_qty_positive CHECK (qty IS NULL OR qty > 0),
  CONSTRAINT conflict_details_object CHECK (jsonb_typeof(details) = 'object')
);
CREATE INDEX allocation_conflicts_status ON allocation_conflicts (status, created_at);
CREATE INDEX allocation_conflicts_pos_txn ON allocation_conflicts (pos_transaction_id);
CREATE INDEX allocation_conflicts_reservation ON allocation_conflicts (reservation_id);
CREATE INDEX allocation_conflicts_order ON allocation_conflicts (order_id);
CREATE INDEX allocation_conflicts_location ON allocation_conflicts (location_id, product_id);

-- +goose Down
DROP TABLE IF EXISTS allocation_conflicts;
DROP TABLE IF EXISTS pos_transactions;
DROP TABLE IF EXISTS pos_devices;
