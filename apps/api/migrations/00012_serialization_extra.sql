-- +goose Up
-- Tambahan Fase 2 (serialisasi, verifikasi, klaim).

-- Klaim PENDING punya dua arti: menunggu masa tunggu (unit di mitra) atau
-- menunggu review manusia (skor risiko di atas ambang). Mode ini disimpan
-- eksplisit, tidak disimpulkan dari risk_score + ambang config yang bisa
-- berubah. Job auto-approve hanya menyentuh klaim review_required = false.
ALTER TABLE loyalty_claims
  ADD COLUMN review_required boolean NOT NULL DEFAULT false;

-- Aturan CLAIM_CARTON_SPREAD membaca klaim unit saudara (satu karton) dan
-- trace membaca klaim per unit; index partial one_active_claim_per_unit tidak
-- mencakup klaim REJECTED/REVERSED.
CREATE INDEX loyalty_claims_unit_time ON loyalty_claims (serial_unit_id, created_at);

-- Klaim yang ditolak (REJECT) atau dibalik (REVERSE) mengembalikan unit ke
-- status sebelum aktivasi, supaya unit itu kembali ikut recall, laporan jual
-- mitra, dan retur. Dipicu review staff (loyalty.review).
INSERT INTO lifecycle_transitions (entity_type, from_code, to_code, biz_step, permission, description) VALUES
  ('SERIAL_UNIT', 'CONSUMER_ACTIVATED', 'SOLD',       'claim_reversal', 'loyalty.review', 'Klaim ditolak/dibalik, unit kembali terjual'),
  ('SERIAL_UNIT', 'CONSUMER_ACTIVATED', 'AT_PARTNER', 'claim_reversal', 'loyalty.review', 'Klaim ditolak/dibalik, unit kembali di mitra');

-- Unit yang dijual dilepas dari kartonnya (parent_id NULL), jadi aturan
-- CLAIM_CARTON_SPREAD dan trace membaca karton historis dari event packing.
CREATE INDEX unit_events_packing_parent ON unit_events (parent_id) WHERE biz_step = 'packing';

-- +goose Down
DROP INDEX IF EXISTS unit_events_packing_parent;
DELETE FROM lifecycle_transitions
 WHERE entity_type = 'SERIAL_UNIT' AND from_code = 'CONSUMER_ACTIVATED' AND biz_step = 'claim_reversal';
DROP INDEX IF EXISTS loyalty_claims_unit_time;
ALTER TABLE loyalty_claims DROP COLUMN IF EXISTS review_required;
