-- +goose Up
-- Katalog (brand, produk, kemasan, harga), wilayah, party, lokasi, dan wilayah
-- resmi distributor. `channels` ikut di sini (bukan di migrasi order) karena
-- price_lists sudah membutuhkannya sebagai FK.

-- ---------------------------------------------------------------- lookup
CREATE TABLE packaging_levels (
  code       text        PRIMARY KEY,
  name       text        NOT NULL,
  rank       smallint    NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT packaging_level_rank_positive CHECK (rank > 0),
  CONSTRAINT packaging_level_rank_once     UNIQUE (rank)
);

CREATE TABLE channels (
  code       text        PRIMARY KEY,
  name       text        NOT NULL,
  type       text        NOT NULL,
  attributes jsonb       NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT channel_type_valid        CHECK (type IN ('D2C', 'MARKETPLACE', 'POS', 'B2B')),
  CONSTRAINT channel_attributes_object CHECK (jsonb_typeof(attributes) = 'object')
);

CREATE TABLE location_types (
  code       text        PRIMARY KEY,
  name       text        NOT NULL,
  is_own     boolean     NOT NULL,
  is_virtual boolean     NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE role_types (
  code       text        PRIMARY KEY,
  name       text        NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- katalog
CREATE TABLE brands (
  id          uuid        PRIMARY KEY,
  code        text        NOT NULL,
  name        text        NOT NULL,
  archived_at timestamptz,
  version     bigint      NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX brands_code_active ON brands (code) WHERE archived_at IS NULL;

CREATE TABLE products (
  id              uuid        PRIMARY KEY,
  brand_id        uuid        NOT NULL REFERENCES brands (id),
  sku             text        NOT NULL,
  gtin            text,
  name            text        NOT NULL,
  uom             text        NOT NULL DEFAULT 'PCS',
  shelf_life_days integer,
  -- Atribut ekor panjang (BPOM, halal, rasa). Dipromosikan jadi kolom kalau
  -- sering difilter.
  attributes      jsonb       NOT NULL DEFAULT '{}',
  archived_at     timestamptz,
  version         bigint      NOT NULL DEFAULT 0,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT product_gtin_format        CHECK (gtin ~ '^([0-9]{8}|[0-9]{12,14})$'),
  CONSTRAINT product_shelf_life_pos     CHECK (shelf_life_days > 0),
  CONSTRAINT product_attributes_object  CHECK (jsonb_typeof(attributes) = 'object')
);
CREATE UNIQUE INDEX products_sku_active ON products (sku) WHERE archived_at IS NULL;
CREATE UNIQUE INDEX products_gtin_active ON products (gtin) WHERE gtin IS NOT NULL AND archived_at IS NULL;
CREATE INDEX products_brand ON products (brand_id);

-- Hierarki kemasan per produk: berapa child_level dalam satu parent_level.
CREATE TABLE packaging_specs (
  id                uuid        PRIMARY KEY,
  product_id        uuid        NOT NULL REFERENCES products (id),
  child_level       text        NOT NULL REFERENCES packaging_levels (code),
  parent_level      text        NOT NULL REFERENCES packaging_levels (code),
  qty_per_parent    integer     NOT NULL,
  allow_mixed_batch boolean     NOT NULL DEFAULT false,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT packaging_qty_positive  CHECK (qty_per_parent > 0),
  CONSTRAINT packaging_levels_differ CHECK (child_level <> parent_level),
  CONSTRAINT packaging_spec_once     UNIQUE (product_id, child_level)
);

-- ---------------------------------------------------------------- wilayah
-- Kode wilayah Kemendagri (misal 35.78 = Kota Surabaya). Prototype memakai
-- centroid dan resolusi titik terdekat; produksi memakai polygon PostGIS.
CREATE TABLE regions (
  code         text             PRIMARY KEY,
  name         text             NOT NULL,
  level        text             NOT NULL,
  parent_code  text             REFERENCES regions (code),
  centroid_lat double precision,
  centroid_lng double precision,
  created_at   timestamptz      NOT NULL DEFAULT now(),
  CONSTRAINT region_code_format   CHECK (code ~ '^[0-9]{2}(\.[0-9]{2}(\.[0-9]{2}(\.[0-9]{4})?)?)?$'),
  CONSTRAINT region_level_valid   CHECK (level IN ('PROVINCE', 'REGENCY', 'CITY', 'DISTRICT', 'VILLAGE')),
  CONSTRAINT region_parent_needed CHECK ((level = 'PROVINCE') = (parent_code IS NULL)),
  CONSTRAINT region_lat_range     CHECK (centroid_lat BETWEEN -90 AND 90),
  CONSTRAINT region_lng_range     CHECK (centroid_lng BETWEEN -180 AND 180)
);
CREATE INDEX regions_parent ON regions (parent_code);

-- ---------------------------------------------------------------- party
CREATE TABLE parties (
  id          uuid        PRIMARY KEY,
  kind        text        NOT NULL,
  name        text        NOT NULL,
  legal_id    text,
  attributes  jsonb       NOT NULL DEFAULT '{}',
  archived_at timestamptz,
  version     bigint      NOT NULL DEFAULT 0,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT party_kind_valid        CHECK (kind IN ('ORG', 'PERSON')),
  CONSTRAINT party_attributes_object CHECK (jsonb_typeof(attributes) = 'object')
);

CREATE TABLE party_roles (
  id         uuid        PRIMARY KEY,
  party_id   uuid        NOT NULL REFERENCES parties (id),
  role_code  text        NOT NULL REFERENCES role_types (code),
  valid_from timestamptz NOT NULL DEFAULT now(),
  valid_to   timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT party_role_period_valid CHECK (valid_to IS NULL OR valid_to > valid_from),
  CONSTRAINT party_role_once         UNIQUE (party_id, role_code, valid_from)
);
CREATE INDEX party_roles_role ON party_roles (role_code);

-- ---------------------------------------------------------------- lokasi
CREATE TABLE locations (
  id                         uuid             PRIMARY KEY,
  code                       text             NOT NULL,
  name                       text             NOT NULL,
  type_code                  text             NOT NULL REFERENCES location_types (code),
  parent_id                  uuid             REFERENCES locations (id),
  party_id                   uuid             REFERENCES parties (id),
  region_code                text             REFERENCES regions (code),
  address                    text,
  lat                        double precision,
  lng                        double precision,
  timezone                   text             NOT NULL DEFAULT 'Asia/Jakarta',
  online_fulfillment_enabled boolean          NOT NULL DEFAULT false,
  -- Status sinkron POS. NOT_TRACKED untuk lokasi tanpa POS; OFFLINE membuat ATP
  -- online lokasi itu 0.
  sync_state                 text             NOT NULL DEFAULT 'NOT_TRACKED',
  last_heartbeat_at          timestamptz,
  archived_at                timestamptz,
  version                    bigint           NOT NULL DEFAULT 0,
  created_at                 timestamptz      NOT NULL DEFAULT now(),
  updated_at                 timestamptz      NOT NULL DEFAULT now(),
  CONSTRAINT location_not_own_parent CHECK (parent_id <> id),
  CONSTRAINT location_sync_state     CHECK (sync_state IN ('NOT_TRACKED', 'ONLINE', 'OFFLINE')),
  CONSTRAINT location_lat_range      CHECK (lat BETWEEN -90 AND 90),
  CONSTRAINT location_lng_range      CHECK (lng BETWEEN -180 AND 180)
);
CREATE UNIQUE INDEX locations_code_active ON locations (code) WHERE archived_at IS NULL;
CREATE INDEX locations_type ON locations (type_code);
CREATE INDEX locations_parent ON locations (parent_id);
CREATE INDEX locations_party ON locations (party_id);
CREATE INDEX locations_region ON locations (region_code);

-- ---------------------------------------------------------------- wilayah distributor
CREATE TABLE territories (
  id          uuid        PRIMARY KEY,
  code        text        NOT NULL,
  name        text        NOT NULL,
  description text,
  archived_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX territories_code_active ON territories (code) WHERE archived_at IS NULL;

CREATE TABLE territory_regions (
  territory_id uuid        NOT NULL REFERENCES territories (id) ON DELETE CASCADE,
  region_code  text        NOT NULL REFERENCES regions (code),
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (territory_id, region_code)
);
CREATE INDEX territory_regions_region ON territory_regions (region_code);

-- Mode cakupan disimpan eksplisit. NATIONAL tidak pernah disimpulkan dari
-- territory yang kosong; SELECTED wajib menunjuk territory.
CREATE TABLE partner_territories (
  id            uuid        PRIMARY KEY,
  party_id      uuid        NOT NULL REFERENCES parties (id),
  territory_id  uuid        REFERENCES territories (id),
  coverage_mode text        NOT NULL,
  valid_from    timestamptz NOT NULL DEFAULT now(),
  valid_to      timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT partner_coverage_mode_valid CHECK (coverage_mode IN ('NATIONAL', 'SELECTED')),
  CONSTRAINT partner_coverage_consistent CHECK (
    (coverage_mode = 'NATIONAL' AND territory_id IS NULL) OR
    (coverage_mode = 'SELECTED' AND territory_id IS NOT NULL)
  ),
  CONSTRAINT partner_territory_period    CHECK (valid_to IS NULL OR valid_to > valid_from),
  CONSTRAINT partner_territory_once      UNIQUE NULLS NOT DISTINCT (party_id, territory_id, valid_from)
);
CREATE INDEX partner_territories_party ON partner_territories (party_id);
CREATE INDEX partner_territories_territory ON partner_territories (territory_id);

-- ---------------------------------------------------------------- harga
-- location_id NULL = harga default channel; baris dengan location_id menimpa
-- untuk lokasi itu. POS membawa versi terakhir saat bootstrap.
CREATE TABLE price_lists (
  id           uuid        PRIMARY KEY,
  code         text        NOT NULL,
  name         text        NOT NULL,
  channel_code text        NOT NULL REFERENCES channels (code),
  location_id  uuid        REFERENCES locations (id),
  version      integer     NOT NULL,
  currency     text        NOT NULL DEFAULT 'IDR',
  valid_from   timestamptz NOT NULL,
  valid_to     timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT price_list_version_positive CHECK (version > 0),
  CONSTRAINT price_list_currency_format  CHECK (currency ~ '^[A-Z]{3}$'),
  CONSTRAINT price_list_period_valid     CHECK (valid_to IS NULL OR valid_to > valid_from),
  CONSTRAINT price_list_version_once     UNIQUE NULLS NOT DISTINCT (channel_code, location_id, version),
  CONSTRAINT price_list_code_once        UNIQUE (code)
);
CREATE INDEX price_lists_lookup ON price_lists (channel_code, location_id, valid_from);
CREATE INDEX price_lists_location ON price_lists (location_id);

CREATE TABLE price_list_items (
  price_list_id uuid        NOT NULL REFERENCES price_lists (id),
  product_id    uuid        NOT NULL REFERENCES products (id),
  -- Rupiah utuh, tidak pernah float.
  price         bigint      NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (price_list_id, product_id),
  CONSTRAINT price_nonneg CHECK (price >= 0)
);
CREATE INDEX price_list_items_product ON price_list_items (product_id);

-- +goose Down
DROP TABLE IF EXISTS price_list_items;
DROP TABLE IF EXISTS price_lists;
DROP TABLE IF EXISTS partner_territories;
DROP TABLE IF EXISTS territory_regions;
DROP TABLE IF EXISTS territories;
DROP TABLE IF EXISTS locations;
DROP TABLE IF EXISTS party_roles;
DROP TABLE IF EXISTS parties;
DROP TABLE IF EXISTS regions;
DROP TABLE IF EXISTS packaging_specs;
DROP TABLE IF EXISTS products;
DROP TABLE IF EXISTS brands;
DROP TABLE IF EXISTS role_types;
DROP TABLE IF EXISTS location_types;
DROP TABLE IF EXISTS channels;
DROP TABLE IF EXISTS packaging_levels;
