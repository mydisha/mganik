-- +goose Up
-- Fase 4: POS offline-first & sinkronisasi (PLANNING §4.3, §7.4, §8.4).

-- Transaksi yang ditolak (signature salah, kasir tidak dikenal) tetap dicatat
-- supaya pengiriman ulang idempoten dan ack_up_to_seq bisa maju. Kasir yang
-- diklaim payload tanpa signature sah, atau user yang tidak ada, tidak boleh
-- jadi FK; kolom boleh NULL hanya untuk hasil REJECTED.
ALTER TABLE pos_transactions ALTER COLUMN cashier_id DROP NOT NULL;
ALTER TABLE pos_transactions ADD CONSTRAINT pos_cashier_required_unless_rejected
  CHECK (cashier_id IS NOT NULL OR result = 'REJECTED');

-- GET /pos/bootstrap: serial UNIT IN_STOCK di satu lokasi, keyset per kode.
CREATE INDEX serial_units_pos_bootstrap ON serial_units (location_id, code)
  WHERE level_code = 'UNIT' AND status = 'IN_STOCK';

-- Job penanda OFFLINE: kandidat = lokasi ONLINE urut heartbeat terlama.
CREATE INDEX locations_online_heartbeat ON locations (last_heartbeat_at, id)
  WHERE sync_state = 'ONLINE';

-- GET /pos/devices: keyset (created_at, id) terbaru dulu.
CREATE INDEX pos_devices_created_keyset ON pos_devices (created_at DESC, id DESC);

-- Approval EDC/QRIS yang sama dipakai dua transaksi berbeda: satu pembayaran
-- untuk dua penjualan (indikasi kasir mengantongi uang) atau bug device.
INSERT INTO anomaly_rules (code, name, description, category, enabled, severity, consumer_effect, params) VALUES
  ('POS_PAYMENT_REF_REUSED', 'Approval pembayaran POS dipakai ulang',
   'approval_ref EDC/QRIS yang sama sudah tercatat untuk transaksi lain; penjualan tetap dicatat, pembayaran menunggu verifikasi.',
   'POS', true, 'HIGH', 'NONE', '{}'),
  -- Penjualan tetap fakta walau kasirnya sudah dinonaktifkan sebelum sync.
  ('POS_CASHIER_INACTIVE', 'Transaksi POS oleh kasir nonaktif',
   'Kasir punya role pos.sync di lokasi itu tetapi user sudah nonaktif/diarsipkan saat transaksi offline tersinkron.',
   'POS', true, 'MEDIUM', 'NONE', '{}'),
  ('POS_OFFLINE_VALUE_LIMIT', 'Nilai transaksi POS di atas batas offline',
   'Total satu transaksi POS melebihi POS_OFFLINE_VALUE_LIMIT; tetap dicatat, perlu ditinjau.',
   'POS', true, 'MEDIUM', 'NONE', '{}');

-- +goose Down
-- Gagal dengan jelas kalau masih ada anomali/transaksi yang bergantung pada
-- objek ini, alih-alih menghapus data bisnis.
DELETE FROM anomaly_rules WHERE code IN ('POS_PAYMENT_REF_REUSED', 'POS_CASHIER_INACTIVE', 'POS_OFFLINE_VALUE_LIMIT');
DROP INDEX IF EXISTS pos_devices_created_keyset;
DROP INDEX IF EXISTS locations_online_heartbeat;
DROP INDEX IF EXISTS serial_units_pos_bootstrap;
ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_cashier_required_unless_rejected;
ALTER TABLE pos_transactions ALTER COLUMN cashier_id SET NOT NULL;
