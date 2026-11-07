-- +goose Up
-- Identitas staff, sesi, RBAC dengan scope lokasi, dan konsumen. Datang setelah
-- katalog/lokasi karena users -> parties dan user_role_locations -> locations.
-- `consumers` ditaruh di sini (bukan di migrasi loyalty) karena orders sudah
-- membutuhkannya sebagai FK.

CREATE TABLE users (
  id                uuid        PRIMARY KEY,
  party_id          uuid        REFERENCES parties (id),
  email             text        NOT NULL,
  name              text        NOT NULL,
  -- String PHC argon2id ($argon2id$v=19$m=...,t=...,p=...$salt$hash).
  password_hash     text        NOT NULL,
  -- Hash argon2id PIN kasir 6 digit; ikut bootstrap POS untuk login lokal.
  pos_pin_hash      text,
  status            text        NOT NULL DEFAULT 'ACTIVE',
  failed_login_count integer    NOT NULL DEFAULT 0,
  locked_until      timestamptz,
  last_login_at     timestamptz,
  archived_at       timestamptz,
  version           bigint      NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT user_status_valid        CHECK (status IN ('ACTIVE', 'DISABLED')),
  CONSTRAINT user_email_format        CHECK (email ~ '^[^@[:space:]]+@[^@[:space:]]+$'),
  CONSTRAINT user_password_argon2id   CHECK (password_hash LIKE '$argon2id$%'),
  CONSTRAINT user_pos_pin_argon2id    CHECK (pos_pin_hash IS NULL OR pos_pin_hash LIKE '$argon2id$%'),
  CONSTRAINT user_failed_login_nonneg CHECK (failed_login_count >= 0)
);
CREATE UNIQUE INDEX users_email_unique ON users (lower(email)) WHERE archived_at IS NULL;
CREATE INDEX users_party ON users (party_id);

-- Token sesi opaque; DB hanya menyimpan SHA-256 supaya dump DB tidak bisa
-- dipakai login dan sesi bisa dicabut.
CREATE TABLE sessions (
  id              uuid        PRIMARY KEY,
  user_id         uuid        NOT NULL REFERENCES users (id),
  token_hash      bytea       NOT NULL,
  idle_expires_at timestamptz NOT NULL,
  expires_at      timestamptz NOT NULL,
  revoked_at      timestamptz,
  last_seen_at    timestamptz NOT NULL DEFAULT now(),
  ip_hash         bytea,
  user_agent      text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT session_token_once      UNIQUE (token_hash),
  CONSTRAINT session_token_sha256    CHECK (octet_length(token_hash) = 32),
  CONSTRAINT session_idle_within_abs CHECK (idle_expires_at <= expires_at)
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_active_expiry ON sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE roles (
  code        text        PRIMARY KEY,
  name        text        NOT NULL,
  description text,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE permissions (
  code        text        PRIMARY KEY,
  description text        NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT permission_code_format CHECK (code ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$')
);

CREATE TABLE role_permissions (
  role_code       text        NOT NULL REFERENCES roles (code),
  permission_code text        NOT NULL REFERENCES permissions (code),
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (role_code, permission_code)
);
CREATE INDEX role_permissions_permission ON role_permissions (permission_code);

-- Scope lokasi disimpan eksplisit: ALL atau SELECTED. Pembaca selalu memakai
-- scope_mode; user_role_locations yang kosong TIDAK berarti "semua lokasi".
CREATE TABLE user_roles (
  id         uuid        PRIMARY KEY,
  user_id    uuid        NOT NULL REFERENCES users (id),
  role_code  text        NOT NULL REFERENCES roles (code),
  scope_mode text        NOT NULL,
  granted_by uuid        REFERENCES users (id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT user_role_scope_mode_valid CHECK (scope_mode IN ('ALL', 'SELECTED')),
  CONSTRAINT user_role_once             UNIQUE (user_id, role_code)
);
CREATE INDEX user_roles_role ON user_roles (role_code);

-- Dibaca hanya kalau user_roles.scope_mode = 'SELECTED'.
CREATE TABLE user_role_locations (
  user_role_id uuid        NOT NULL REFERENCES user_roles (id) ON DELETE CASCADE,
  location_id  uuid        NOT NULL REFERENCES locations (id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_role_id, location_id)
);
CREATE INDEX user_role_locations_location ON user_role_locations (location_id);

-- Konsumen: nomor HP dienkripsi AES-GCM, dicari lewat blind index HMAC.
CREATE TABLE consumers (
  id                   uuid        PRIMARY KEY,
  phone_enc            bytea       NOT NULL,
  phone_bidx           bytea       NOT NULL,
  display_name         text,
  status               text        NOT NULL DEFAULT 'ACTIVE',
  consent_marketing_at timestamptz,
  consent_location_at  timestamptz,
  anonymized_at        timestamptz,
  version              bigint      NOT NULL DEFAULT 0,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT consumer_phone_once   UNIQUE (phone_bidx),
  CONSTRAINT consumer_status_valid CHECK (status IN ('ACTIVE', 'BLOCKED', 'ANONYMIZED'))
);

-- +goose Down
DROP TABLE IF EXISTS consumers;
DROP TABLE IF EXISTS user_role_locations;
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
