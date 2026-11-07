-- +goose Up
-- Sesi konsumen dan challenge OTP. Tabel `sessions` (00004) khusus staff
-- (user_id NOT NULL -> users), jadi sesi konsumen punya tabel sendiri dengan
-- aturan yang sama: DB hanya menyimpan SHA-256 token, bisa dicabut.

CREATE TABLE consumer_sessions (
  id              uuid        PRIMARY KEY,
  consumer_id     uuid        NOT NULL REFERENCES consumers (id),
  token_hash      bytea       NOT NULL,
  idle_expires_at timestamptz NOT NULL,
  expires_at      timestamptz NOT NULL,
  revoked_at      timestamptz,
  last_seen_at    timestamptz NOT NULL DEFAULT now(),
  -- Prefix HMAC IP (pii.HashIP), bukan IP mentah.
  ip_hash         bytea,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT consumer_session_token_once      UNIQUE (token_hash),
  CONSTRAINT consumer_session_token_sha256    CHECK (octet_length(token_hash) = 32),
  CONSTRAINT consumer_session_idle_within_abs CHECK (idle_expires_at <= expires_at)
);
CREATE INDEX consumer_sessions_consumer ON consumer_sessions (consumer_id);
CREATE INDEX consumer_sessions_active_expiry ON consumer_sessions (expires_at) WHERE revoked_at IS NULL;

-- Satu baris per permintaan OTP. Nomor HP hanya muncul sebagai blind index
-- (sama dengan consumers.phone_bidx); kode hanya sebagai HMAC terikat id
-- challenge. attempts dinaikkan atomik sebelum kode dibandingkan.
CREATE TABLE otp_challenges (
  id           uuid        PRIMARY KEY,
  phone_bidx   bytea       NOT NULL,
  code_hash    bytea       NOT NULL,
  attempts     integer     NOT NULL DEFAULT 0,
  max_attempts integer     NOT NULL,
  expires_at   timestamptz NOT NULL,
  consumed_at  timestamptz,
  ip_hash      bytea,
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT otp_phone_bidx_sha256    CHECK (octet_length(phone_bidx) = 32),
  CONSTRAINT otp_code_hash_sha256     CHECK (octet_length(code_hash) = 32),
  CONSTRAINT otp_max_attempts_positive CHECK (max_attempts > 0),
  CONSTRAINT otp_attempts_within_max  CHECK (attempts BETWEEN 0 AND max_attempts),
  CONSTRAINT otp_expiry_after_create  CHECK (expires_at > created_at)
);
-- Mencari challenge aktif terbaru dan menghitung permintaan per nomor per jendela.
CREATE INDEX otp_challenges_phone_time ON otp_challenges (phone_bidx, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS otp_challenges;
DROP TABLE IF EXISTS consumer_sessions;
