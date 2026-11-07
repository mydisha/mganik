-- +goose Up
-- Stok: tipe mutasi sebagai data, saldo per (lokasi, produk) dengan tiga bucket,
-- ledger append-only, dan kebijakan stok per lokasi.
-- stock_reservations ada di migrasi order karena FK ke orders/order_lines.

-- Efek tiap tipe mutasi ke tiga bucket. Engine stok generik membaca tabel ini.
-- allow_either_sign = bucket bertanda bukan nol boleh bergerak ke dua arah
-- (stock opname), selain itu tanda delta wajib sama dengan kolom *_sign.
CREATE TABLE movement_types (
  code              text        PRIMARY KEY,
  name              text        NOT NULL,
  physical_sign     smallint    NOT NULL,
  reserved_sign     smallint    NOT NULL,
  quarantine_sign   smallint    NOT NULL,
  is_promise        boolean     NOT NULL,
  allow_either_sign boolean     NOT NULL DEFAULT false,
  created_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT movement_physical_sign_valid   CHECK (physical_sign IN (-1, 0, 1)),
  CONSTRAINT movement_reserved_sign_valid   CHECK (reserved_sign IN (-1, 0, 1)),
  CONSTRAINT movement_quarantine_sign_valid CHECK (quarantine_sign IN (-1, 0, 1)),
  CONSTRAINT movement_type_has_effect       CHECK (physical_sign <> 0 OR reserved_sign <> 0 OR quarantine_sign <> 0)
);

-- Saldo stok: tiga bucket fisik terpisah, ATP dihitung, oversell dikunci di DB.
CREATE TABLE inventory_balances (
  location_id    uuid        NOT NULL REFERENCES locations (id),
  product_id     uuid        NOT NULL REFERENCES products (id),
  physical_qty   bigint      NOT NULL DEFAULT 0,
  reserved_qty   bigint      NOT NULL DEFAULT 0,
  quarantine_qty bigint      NOT NULL DEFAULT 0,
  available_qty  bigint      GENERATED ALWAYS AS (physical_qty - reserved_qty - quarantine_qty) STORED,
  version        bigint      NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (location_id, product_id),
  CONSTRAINT physical_nonneg   CHECK (physical_qty >= 0),
  CONSTRAINT reserved_nonneg   CHECK (reserved_qty >= 0),
  CONSTRAINT quarantine_nonneg CHECK (quarantine_qty >= 0),
  CONSTRAINT no_oversell       CHECK (physical_qty - reserved_qty - quarantine_qty >= 0)
);
CREATE INDEX inventory_balances_product ON inventory_balances (product_id);

-- Ledger mutasi. Sumber kebenaran: SUM(delta) per (lokasi, produk) = saldo.
CREATE TABLE inventory_movements (
  id               uuid        PRIMARY KEY,
  location_id      uuid        NOT NULL,
  product_id       uuid        NOT NULL,
  movement_type    text        NOT NULL REFERENCES movement_types (code),
  physical_delta   bigint      NOT NULL DEFAULT 0,
  reserved_delta   bigint      NOT NULL DEFAULT 0,
  quarantine_delta bigint      NOT NULL DEFAULT 0,
  -- Referensi polimorfik ke dokumen asal (ORDER, POS_TRANSACTION, SHIPMENT, ...).
  ref_type         text        NOT NULL,
  ref_id           uuid        NOT NULL,
  idempotency_key  text        NOT NULL,
  reason           text,
  actor_type       text        NOT NULL,
  actor_id         uuid,
  occurred_at      timestamptz NOT NULL,
  recorded_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT movement_idempotent        UNIQUE (idempotency_key),
  CONSTRAINT movement_has_delta         CHECK (physical_delta <> 0 OR reserved_delta <> 0 OR quarantine_delta <> 0),
  CONSTRAINT movement_actor_type_valid  CHECK (actor_type IN ('USER', 'DEVICE', 'SYSTEM', 'CONSUMER')),
  CONSTRAINT movement_idempotency_length CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
  CONSTRAINT movement_balance_fk FOREIGN KEY (location_id, product_id)
    REFERENCES inventory_balances (location_id, product_id)
);
CREATE INDEX inventory_movements_loc_prod_time ON inventory_movements (location_id, product_id, occurred_at);
CREATE INDEX inventory_movements_ref ON inventory_movements (ref_type, ref_id);
-- Penjualan 28 hari terakhir untuk DOI/runout dibaca per tipe + waktu.
CREATE INDEX inventory_movements_type_time ON inventory_movements (movement_type, occurred_at);

-- Delta setiap baris ledger harus cocok dengan efek tipe mutasinya. Lapis kedua
-- di DB untuk engine stok generik:
--   * tanda tiap bucket sama dengan kolom *_sign (0 = bucket tidak boleh berubah);
--   * tipe satu arah dengan beberapa bucket (COMMIT_SALE, WRITE_OFF) memindahkan
--     jumlah yang sama di semua bucket-nya: |physical| = |reserved| dst.
-- +goose StatementBegin
CREATE FUNCTION check_movement_effect() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  mt movement_types%ROWTYPE;
  bucket text;
  sign_val smallint;
  delta bigint;
  magnitude bigint;
BEGIN
  SELECT * INTO mt FROM movement_types WHERE code = NEW.movement_type;
  IF NOT FOUND THEN
    RETURN NEW; -- FK movement_type yang menolak
  END IF;
  FOREACH bucket IN ARRAY ARRAY['physical', 'reserved', 'quarantine'] LOOP
    CASE bucket
      WHEN 'physical'   THEN sign_val := mt.physical_sign;   delta := NEW.physical_delta;
      WHEN 'reserved'   THEN sign_val := mt.reserved_sign;   delta := NEW.reserved_delta;
      ELSE                   sign_val := mt.quarantine_sign; delta := NEW.quarantine_delta;
    END CASE;
    IF (sign_val = 0 AND delta <> 0)
       OR (sign_val <> 0 AND mt.allow_either_sign AND delta = 0)
       OR (sign_val <> 0 AND NOT mt.allow_either_sign AND sign(delta) <> sign_val) THEN
      RAISE EXCEPTION 'delta % % tidak cocok dengan tipe mutasi %', bucket, delta, mt.code
        USING ERRCODE = 'check_violation', CONSTRAINT = 'movement_effect_matches_type';
    END IF;
    IF sign_val <> 0 AND NOT mt.allow_either_sign THEN
      IF magnitude IS NULL THEN
        magnitude := abs(delta);
      ELSIF abs(delta) <> magnitude THEN
        RAISE EXCEPTION 'besaran delta antar bucket berbeda untuk tipe mutasi % (% vs %)', mt.code, magnitude, abs(delta)
          USING ERRCODE = 'check_violation', CONSTRAINT = 'movement_effect_matches_type';
      END IF;
    END IF;
  END LOOP;
  RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER inventory_movements_effect_check
  BEFORE INSERT ON inventory_movements
  FOR EACH ROW EXECUTE FUNCTION check_movement_effect();

CREATE TRIGGER inventory_movements_append_only
  BEFORE UPDATE OR DELETE ON inventory_movements
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();
CREATE TRIGGER inventory_movements_no_truncate
  BEFORE TRUNCATE ON inventory_movements
  FOR EACH STATEMENT EXECUTE FUNCTION forbid_mutation();

-- Parameter DOI/runout dan buffer channel online per (lokasi, produk).
CREATE TABLE stock_policies (
  location_id       uuid        NOT NULL REFERENCES locations (id),
  product_id        uuid        NOT NULL REFERENCES products (id),
  min_qty           bigint      NOT NULL DEFAULT 0,
  online_buffer_qty bigint      NOT NULL DEFAULT 0,
  lead_time_days    integer     NOT NULL DEFAULT 0,
  safety_days       integer     NOT NULL DEFAULT 0,
  version           bigint      NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (location_id, product_id),
  CONSTRAINT stock_policy_min_nonneg    CHECK (min_qty >= 0),
  CONSTRAINT stock_policy_buffer_nonneg CHECK (online_buffer_qty >= 0),
  CONSTRAINT stock_policy_lead_nonneg   CHECK (lead_time_days >= 0),
  CONSTRAINT stock_policy_safety_nonneg CHECK (safety_days >= 0)
);
CREATE INDEX stock_policies_product ON stock_policies (product_id);

-- +goose Down
DROP TABLE IF EXISTS stock_policies;
DROP TABLE IF EXISTS inventory_movements;
DROP FUNCTION IF EXISTS check_movement_effect();
DROP TABLE IF EXISTS inventory_balances;
DROP TABLE IF EXISTS movement_types;
