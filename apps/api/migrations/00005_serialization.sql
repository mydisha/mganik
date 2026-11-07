-- +goose Up
-- Produksi dan serialisasi: job cetak label, batch produksi, serial unit dengan
-- hierarki kemasan, event ala GS1 EPCIS, dan shipment.

-- Job cetak label dari vendor; dipakai untuk rekonsiliasi kebocoran label.
CREATE TABLE code_batches (
  id             uuid        PRIMARY KEY,
  level_code     text        NOT NULL REFERENCES packaging_levels (code),
  product_id     uuid        REFERENCES products (id),
  qty            integer     NOT NULL,
  printer_vendor text        NOT NULL,
  issued_at      timestamptz NOT NULL,
  delivered_at   timestamptz,
  reconciled_at  timestamptz,
  void_qty       integer     NOT NULL DEFAULT 0,
  notes          text,
  created_by     uuid        REFERENCES users (id),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT code_batch_qty_positive CHECK (qty > 0),
  CONSTRAINT code_batch_void_range   CHECK (void_qty >= 0 AND void_qty <= qty)
);
CREATE INDEX code_batches_product ON code_batches (product_id);

CREATE TABLE batches (
  id            uuid        PRIMARY KEY,
  product_id    uuid        NOT NULL REFERENCES products (id),
  lot_no        text        NOT NULL,
  line          text,
  mfg_date      date        NOT NULL,
  exp_date      date        NOT NULL,
  status        text        NOT NULL DEFAULT 'ACTIVE',
  recalled_at   timestamptz,
  recall_reason text,
  version       bigint      NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT batch_lot_once       UNIQUE (product_id, lot_no),
  CONSTRAINT batch_status_valid   CHECK (status IN ('ACTIVE', 'ON_HOLD', 'RECALLED')),
  CONSTRAINT batch_dates_ordered  CHECK (exp_date > mfg_date),
  CONSTRAINT batch_recall_consistent CHECK ((status = 'RECALLED') = (recalled_at IS NOT NULL))
);

-- Status serial sebagai data: kode membaca atribut, bukan daftar hardcode.
CREATE TABLE serial_unit_statuses (
  code                 text        PRIMARY KEY,
  name                 text        NOT NULL,
  is_sellable          boolean     NOT NULL,
  is_terminal          boolean     NOT NULL,
  consumer_message_key text        NOT NULL,
  sort_order           smallint    NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now()
);

-- Transisi sebagai data untuk serial unit dan order. from_code NULL = status
-- awal saat entitas dibuat. permission NULL = transisi hanya dipicu sistem,
-- konsumen, atau pihak luar (webhook), bukan aksi staff.
CREATE TABLE lifecycle_transitions (
  id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  entity_type text        NOT NULL,
  from_code   text,
  to_code     text        NOT NULL,
  biz_step    text        NOT NULL,
  permission  text        REFERENCES permissions (code),
  description text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT lifecycle_entity_valid   CHECK (entity_type IN ('SERIAL_UNIT', 'ORDER')),
  CONSTRAINT lifecycle_not_self_loop  CHECK (from_code IS NULL OR from_code <> to_code),
  CONSTRAINT lifecycle_transition_once UNIQUE NULLS NOT DISTINCT (entity_type, from_code, to_code)
);

-- from_code/to_code menunjuk tabel status yang berbeda per entity_type, jadi
-- tidak bisa memakai FK biasa. Trigger ini menegakkan hal yang sama.
-- +goose StatementBegin
CREATE FUNCTION check_lifecycle_transition_codes() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  missing text;
BEGIN
  IF NEW.entity_type = 'SERIAL_UNIT' THEN
    SELECT c INTO missing
      FROM unnest(ARRAY[NEW.from_code, NEW.to_code]) AS c
     WHERE c IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM serial_unit_statuses s WHERE s.code = c)
     LIMIT 1;
  ELSE
    SELECT c INTO missing
      FROM unnest(ARRAY[NEW.from_code, NEW.to_code]) AS c
     WHERE c IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM order_statuses s WHERE s.code = c)
     LIMIT 1;
  END IF;
  IF missing IS NOT NULL THEN
    RAISE EXCEPTION 'status % tidak dikenal untuk %', missing, NEW.entity_type
      USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'lifecycle_transition_codes_exist';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER lifecycle_transitions_codes_exist
  BEFORE INSERT OR UPDATE ON lifecycle_transitions
  FOR EACH ROW EXECUTE FUNCTION check_lifecycle_transition_codes();

CREATE TABLE serial_units (
  id                         uuid        PRIMARY KEY,
  -- Kode publik di QR: 80-bit acak, Crockford Base32 16 karakter.
  code                       text        NOT NULL,
  level_code                 text        NOT NULL REFERENCES packaging_levels (code),
  product_id                 uuid        REFERENCES products (id),
  batch_id                   uuid        REFERENCES batches (id),
  code_batch_id              uuid        REFERENCES code_batches (id),
  parent_id                  uuid        REFERENCES serial_units (id),
  status                     text        NOT NULL REFERENCES serial_unit_statuses (code),
  location_id                uuid        REFERENCES locations (id),
  custodian_party_id         uuid        REFERENCES parties (id),
  -- HMAC-SHA256(CLAIM_PEPPER, id || PIN). PIN tidak pernah disimpan.
  claim_secret_hash          bytea,
  claim_failed_attempts      integer     NOT NULL DEFAULT 0,
  claim_locked_at            timestamptz,
  commissioned_at            timestamptz,
  first_verified_at          timestamptz,
  first_verified_region_code text        REFERENCES regions (code),
  verify_count               integer     NOT NULL DEFAULT 0,
  attributes                 jsonb       NOT NULL DEFAULT '{}',
  version                    bigint      NOT NULL DEFAULT 0,
  created_at                 timestamptz NOT NULL DEFAULT now(),
  updated_at                 timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT serial_code_unique        UNIQUE (code),
  CONSTRAINT serial_code_format        CHECK (code ~ '^[0-9A-HJKMNP-TV-Z]{16}$'),
  CONSTRAINT serial_not_own_parent     CHECK (parent_id <> id),
  -- Label yang belum di-commission (GENERATED) boleh tanpa produk/batch. Label
  -- yang di-void sebelum commission (DECOMMISSIONED tanpa commissioned_at) juga.
  -- Selain itu wajib terikat ke produk; batch wajib untuk level UNIT saja,
  -- karena karton/palet boleh berisi campuran batch (packaging_specs.allow_mixed_batch).
  CONSTRAINT serial_commissioned_has_product CHECK (
    status = 'GENERATED'
    OR (status = 'DECOMMISSIONED' AND commissioned_at IS NULL)
    OR (product_id IS NOT NULL AND (batch_id IS NOT NULL OR level_code <> 'UNIT'))
  ),
  CONSTRAINT serial_claim_hash_sha256  CHECK (claim_secret_hash IS NULL OR octet_length(claim_secret_hash) = 32),
  CONSTRAINT serial_claim_attempts_nonneg CHECK (claim_failed_attempts >= 0),
  CONSTRAINT serial_verify_count_nonneg   CHECK (verify_count >= 0),
  CONSTRAINT serial_attributes_object  CHECK (jsonb_typeof(attributes) = 'object')
);
CREATE INDEX serial_units_parent ON serial_units (parent_id);
CREATE INDEX serial_units_batch ON serial_units (batch_id);
CREATE INDEX serial_units_custodian_status ON serial_units (custodian_party_id, status);
CREATE INDEX serial_units_location_status ON serial_units (location_id, status);
CREATE INDEX serial_units_code_batch ON serial_units (code_batch_id);
CREATE INDEX serial_units_product ON serial_units (product_id);

-- Hierarki kemasan: induk wajib di level yang lebih tinggi (packaging_levels.rank)
-- daripada anaknya. Palet di dalam unit, atau karton di dalam karton, ditolak.
-- +goose StatementBegin
CREATE FUNCTION check_serial_parent_level() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  child_rank smallint;
  parent_rank smallint;
BEGIN
  -- Tanpa induk tidak ada yang dicek; induk = diri sendiri ditolak CHECK
  -- serial_not_own_parent.
  IF NEW.parent_id IS NULL OR NEW.parent_id = NEW.id THEN
    RETURN NEW;
  END IF;
  SELECT rank INTO child_rank FROM packaging_levels WHERE code = NEW.level_code;
  SELECT pl.rank INTO parent_rank
    FROM serial_units p JOIN packaging_levels pl ON pl.code = p.level_code
   WHERE p.id = NEW.parent_id;
  -- Induk yang tidak ada ditolak FK parent_id.
  IF parent_rank IS NOT NULL AND parent_rank <= child_rank THEN
    RAISE EXCEPTION 'induk serial harus berlevel lebih tinggi dari %', NEW.level_code
      USING ERRCODE = 'check_violation', CONSTRAINT = 'serial_parent_level_higher';
  END IF;
  RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER serial_units_parent_level
  BEFORE INSERT OR UPDATE OF parent_id, level_code ON serial_units
  FOR EACH ROW EXECUTE FUNCTION check_serial_parent_level();

-- Riwayat serial ala EPCIS. Append-only; tidak ada FK yang masuk ke tabel ini
-- supaya bisa dipartisi bulanan per occurred_at nanti. device_id sengaja tanpa
-- FK (pos_devices dibuat di migrasi berikutnya dan event harus tetap utuh).
CREATE TABLE unit_events (
  id             uuid        PRIMARY KEY,
  serial_unit_id uuid        NOT NULL REFERENCES serial_units (id),
  biz_step       text        NOT NULL,
  disposition    text,
  from_status    text        REFERENCES serial_unit_statuses (code),
  to_status      text        REFERENCES serial_unit_statuses (code),
  parent_id      uuid,
  location_id    uuid        REFERENCES locations (id),
  party_id       uuid        REFERENCES parties (id),
  actor_type     text        NOT NULL,
  actor_id       uuid,
  device_id      uuid,
  ref_type       text,
  ref_id         uuid,
  payload        jsonb       NOT NULL DEFAULT '{}',
  occurred_at    timestamptz NOT NULL,
  recorded_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT unit_event_actor_type_valid CHECK (actor_type IN ('USER', 'DEVICE', 'SYSTEM', 'CONSUMER')),
  CONSTRAINT unit_event_payload_object   CHECK (jsonb_typeof(payload) = 'object')
);
CREATE INDEX unit_events_unit_time ON unit_events (serial_unit_id, occurred_at);
CREATE INDEX unit_events_ref ON unit_events (ref_type, ref_id);

CREATE TRIGGER unit_events_append_only
  BEFORE UPDATE OR DELETE ON unit_events
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER unit_events_no_truncate
  BEFORE TRUNCATE ON unit_events
  FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

CREATE TABLE shipments (
  id               uuid        PRIMARY KEY,
  code             text        NOT NULL,
  from_location_id uuid        NOT NULL REFERENCES locations (id),
  to_location_id   uuid        NOT NULL REFERENCES locations (id),
  to_party_id      uuid        REFERENCES parties (id),
  -- Program harga, misal SUBSIDI_SULSEL_Q4; dipakai bukti kasus gray market.
  program_code     text,
  status           text        NOT NULL DEFAULT 'DRAFT',
  shipped_at       timestamptz,
  received_at      timestamptz,
  created_by       uuid        REFERENCES users (id),
  version          bigint      NOT NULL DEFAULT 0,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT shipment_code_once        UNIQUE (code),
  CONSTRAINT shipment_status_valid     CHECK (status IN ('DRAFT', 'SHIPPED', 'RECEIVED', 'CANCELLED')),
  CONSTRAINT shipment_route_differs    CHECK (from_location_id <> to_location_id),
  CONSTRAINT shipment_received_after   CHECK (received_at IS NULL OR (shipped_at IS NOT NULL AND received_at >= shipped_at))
);
CREATE INDEX shipments_from ON shipments (from_location_id);
CREATE INDEX shipments_to ON shipments (to_location_id);
CREATE INDEX shipments_to_party ON shipments (to_party_id);
CREATE INDEX shipments_status ON shipments (status);

CREATE TABLE shipment_items (
  id             uuid        PRIMARY KEY,
  shipment_id    uuid        NOT NULL REFERENCES shipments (id),
  serial_unit_id uuid        NOT NULL REFERENCES serial_units (id),
  product_id     uuid        NOT NULL REFERENCES products (id),
  qty            integer     NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT shipment_item_qty_positive CHECK (qty > 0),
  CONSTRAINT shipment_item_once         UNIQUE (shipment_id, serial_unit_id)
);
CREATE INDEX shipment_items_serial ON shipment_items (serial_unit_id);
CREATE INDEX shipment_items_product ON shipment_items (product_id);

-- +goose Down
DROP TABLE IF EXISTS shipment_items;
DROP TABLE IF EXISTS shipments;
DROP TABLE IF EXISTS unit_events;
DROP TABLE IF EXISTS serial_units;
DROP FUNCTION IF EXISTS check_serial_parent_level();
DROP TABLE IF EXISTS lifecycle_transitions;
DROP FUNCTION IF EXISTS check_lifecycle_transition_codes();
DROP TABLE IF EXISTS serial_unit_statuses;
DROP TABLE IF EXISTS batches;
DROP TABLE IF EXISTS code_batches;
