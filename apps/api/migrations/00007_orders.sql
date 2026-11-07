-- +goose Up
-- Order omnichannel, riwayat status, pembayaran, dan reservasi stok.

CREATE TABLE order_statuses (
  code        text        PRIMARY KEY,
  name        text        NOT NULL,
  is_terminal boolean     NOT NULL,
  -- true = order dengan status ini memegang reservasi stok aktif.
  holds_stock boolean     NOT NULL,
  sort_order  smallint    NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE orders (
  id                      uuid        PRIMARY KEY,
  channel_code            text        NOT NULL REFERENCES channels (code),
  -- Nomor order di channel asal; dedupe webhook marketplace.
  external_ref            text,
  consumer_id             uuid        REFERENCES consumers (id),
  fulfillment_location_id uuid        REFERENCES locations (id),
  status                  text        NOT NULL REFERENCES order_statuses (code),
  currency                text        NOT NULL DEFAULT 'IDR',
  subtotal                bigint      NOT NULL,
  discount                bigint      NOT NULL DEFAULT 0,
  total                   bigint      NOT NULL,
  placed_at               timestamptz NOT NULL,
  paid_at                 timestamptz,
  payment_expires_at      timestamptz,
  metadata                jsonb       NOT NULL DEFAULT '{}',
  version                 bigint      NOT NULL DEFAULT 0,
  created_at              timestamptz NOT NULL DEFAULT now(),
  updated_at              timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_external_ref_once UNIQUE (channel_code, external_ref),
  CONSTRAINT order_amounts_nonneg    CHECK (subtotal >= 0 AND discount >= 0 AND total >= 0),
  CONSTRAINT order_total_consistent  CHECK (total = subtotal - discount),
  CONSTRAINT order_currency_format   CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT order_metadata_object   CHECK (jsonb_typeof(metadata) = 'object')
);
CREATE INDEX orders_status_placed ON orders (status, placed_at);
CREATE INDEX orders_consumer ON orders (consumer_id);
CREATE INDEX orders_fulfillment_location ON orders (fulfillment_location_id, status);

CREATE TABLE order_lines (
  id         uuid        PRIMARY KEY,
  order_id   uuid        NOT NULL REFERENCES orders (id),
  product_id uuid        NOT NULL REFERENCES products (id),
  qty        bigint      NOT NULL,
  unit_price bigint      NOT NULL,
  discount   bigint      NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_line_qty_positive  CHECK (qty > 0),
  CONSTRAINT order_line_price_nonneg  CHECK (unit_price >= 0),
  CONSTRAINT order_line_discount_range CHECK (discount >= 0 AND discount <= unit_price * qty),
  -- Target FK komposit dari stock_reservations (line harus milik order yang sama).
  CONSTRAINT order_line_belongs_to_order UNIQUE (id, order_id)
);
CREATE INDEX order_lines_order ON order_lines (order_id);
CREATE INDEX order_lines_product ON order_lines (product_id);

-- Satu unit serial hanya bisa terjual sekali.
CREATE TABLE order_line_serials (
  order_line_id  uuid        NOT NULL REFERENCES order_lines (id),
  serial_unit_id uuid        NOT NULL REFERENCES serial_units (id),
  assigned_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (order_line_id, serial_unit_id),
  CONSTRAINT serial_sold_once UNIQUE (serial_unit_id)
);

CREATE TABLE order_status_history (
  id          uuid        PRIMARY KEY,
  order_id    uuid        NOT NULL REFERENCES orders (id),
  from_status text        REFERENCES order_statuses (code),
  to_status   text        NOT NULL REFERENCES order_statuses (code),
  reason      text,
  actor_type  text        NOT NULL,
  actor_id    uuid,
  at          timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT order_history_actor_type_valid CHECK (actor_type IN ('USER', 'DEVICE', 'SYSTEM', 'CONSUMER'))
);
CREATE INDEX order_status_history_order ON order_status_history (order_id, at);

CREATE TRIGGER order_status_history_append_only
  BEFORE UPDATE OR DELETE ON order_status_history
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER order_status_history_no_truncate
  BEFORE TRUNCATE ON order_status_history
  FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

CREATE TABLE payments (
  id           uuid        PRIMARY KEY,
  order_id     uuid        NOT NULL REFERENCES orders (id),
  provider     text        NOT NULL,
  provider_ref text        NOT NULL,
  method       text,
  amount       bigint      NOT NULL,
  currency     text        NOT NULL DEFAULT 'IDR',
  status       text        NOT NULL,
  raw          jsonb       NOT NULL DEFAULT '{}',
  received_at  timestamptz NOT NULL DEFAULT now(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  -- Webhook yang dikirim ulang menjadi no-op.
  CONSTRAINT payment_provider_ref_once UNIQUE (provider, provider_ref),
  CONSTRAINT payment_amount_positive   CHECK (amount > 0),
  CONSTRAINT payment_status_valid      CHECK (status IN ('PENDING', 'SUCCEEDED', 'FAILED', 'REFUNDED')),
  CONSTRAINT payment_currency_format   CHECK (currency ~ '^[A-Z]{3}$')
);
CREATE INDEX payments_order ON payments (order_id);

-- Reservasi = janji. Status teknis cukup text + CHECK.
CREATE TABLE stock_reservations (
  id            uuid        PRIMARY KEY,
  order_id      uuid        NOT NULL REFERENCES orders (id),
  order_line_id uuid        NOT NULL,
  location_id   uuid        NOT NULL,
  product_id    uuid        NOT NULL,
  qty           bigint      NOT NULL,
  status        text        NOT NULL,
  expires_at    timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  closed_at     timestamptz,
  close_reason  text,
  CONSTRAINT reservation_qty_positive CHECK (qty > 0),
  CONSTRAINT reservation_status_valid CHECK (status IN ('ACTIVE', 'COMMITTED', 'RELEASED', 'EXPIRED', 'PREEMPTED')),
  CONSTRAINT reservation_closed_consistent CHECK ((status = 'ACTIVE') = (closed_at IS NULL)),
  CONSTRAINT reservation_line_fk FOREIGN KEY (order_line_id, order_id)
    REFERENCES order_lines (id, order_id),
  CONSTRAINT reservation_balance_fk FOREIGN KEY (location_id, product_id)
    REFERENCES inventory_balances (location_id, product_id)
);
CREATE UNIQUE INDEX one_active_reservation_per_line ON stock_reservations (order_line_id) WHERE status = 'ACTIVE';
CREATE INDEX reservations_due ON stock_reservations (expires_at) WHERE status = 'ACTIVE';
-- Pencarian reservasi yang bisa di-preempt di satu lokasi (Kasus 4).
CREATE INDEX reservations_active_by_stock ON stock_reservations (location_id, product_id, created_at) WHERE status = 'ACTIVE';
CREATE INDEX stock_reservations_order ON stock_reservations (order_id);

-- +goose Down
DROP TABLE IF EXISTS stock_reservations;
DROP TABLE IF EXISTS payments;
DROP TABLE IF EXISTS order_status_history;
DROP TABLE IF EXISTS order_line_serials;
DROP TABLE IF EXISTS order_lines;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS order_statuses;
