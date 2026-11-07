-- +goose Up
-- Data referensi yang dibutuhkan aplikasi di setiap lingkungan (bukan data demo):
-- lookup status/tipe, state machine, tipe mutasi stok, aturan radar, dan RBAC.
-- Data demo (produk, lokasi, user, stok) ada di `api seed`.

INSERT INTO packaging_levels (code, name, rank) VALUES
  ('UNIT',   'Unit',   1),
  ('INNER',  'Inner box', 2),
  ('CARTON', 'Karton', 3),
  ('PALLET', 'Palet',  4);

INSERT INTO channels (code, name, type) VALUES
  ('D2C_WEB',     'Website D2C', 'D2C'),
  ('SHOPEE',      'Shopee',      'MARKETPLACE'),
  ('TOKOPEDIA',   'Tokopedia',   'MARKETPLACE'),
  ('TIKTOK_SHOP', 'TikTok Shop', 'MARKETPLACE'),
  ('POS',         'POS toko sendiri', 'POS'),
  ('B2B',         'Penjualan B2B', 'B2B');

INSERT INTO location_types (code, name, is_own, is_virtual) VALUES
  ('CENTRAL_WH',     'Gudang pusat',         true,  false),
  ('HUB',            'Hub regional',         true,  false),
  ('OWN_STORE',      'Toko sendiri',         true,  false),
  ('PARTNER_WH',     'Gudang mitra',         false, false),
  ('IN_TRANSIT',     'Dalam perjalanan',     true,  true),
  ('MARKETPLACE_WH', 'Gudang marketplace',   false, false);

INSERT INTO role_types (code, name) VALUES
  ('BRAND_OWNER',    'Pemilik brand'),
  ('MANUFACTURER',   'Produsen'),
  ('DISTRIBUTOR',    'Distributor'),
  ('RETAILER',       'Peritel'),
  ('RESELLER',       'Reseller'),
  ('PRINTER_VENDOR', 'Vendor cetak label'),
  ('LOGISTICS',      'Logistik');

-- ---------------------------------------------------------------- serial unit
INSERT INTO serial_unit_statuses (code, name, is_sellable, is_terminal, consumer_message_key, sort_order) VALUES
  ('GENERATED',          'Kode dicetak',          false, false, 'verify.not_released',   10),
  ('COMMISSIONED',       'Di-commission',         false, false, 'verify.not_released',   20),
  ('IN_STOCK',           'Di gudang/toko sendiri', true, false, 'verify.genuine',        30),
  ('IN_TRANSIT',         'Dalam pengiriman',      false, false, 'verify.genuine',        40),
  ('AT_PARTNER',         'Di mitra',              true,  false, 'verify.genuine',        50),
  ('SOLD',               'Terjual',               false, false, 'verify.genuine',        60),
  ('CONSUMER_ACTIVATED', 'Diaktifkan konsumen',   false, false, 'verify.genuine_claimed', 70),
  ('RETURNED',           'Diretur',               false, false, 'verify.returned',       80),
  ('QUARANTINED',        'Dikarantina',           false, false, 'verify.on_hold',        90),
  ('DECOMMISSIONED',     'Dimusnahkan/void',      false, true,  'verify.decommissioned', 100);

-- ---------------------------------------------------------------- order
INSERT INTO order_statuses (code, name, is_terminal, holds_stock, sort_order) VALUES
  ('PENDING_PAYMENT', 'Menunggu pembayaran', false, true,  10),
  ('PAID',            'Dibayar',             false, true,  20),
  ('FULFILLING',      'Diproses gudang',     false, true,  30),
  ('SHIPPED',         'Dikirim',             false, false, 40),
  ('DELIVERED',       'Diterima',            false, false, 50),
  ('COMPLETED',       'Selesai',             true,  false, 60),
  ('EXPIRED',         'Kedaluwarsa',         false, false, 70),
  ('NEEDS_ACTION',    'Perlu tindakan',      false, false, 80),
  ('CANCELLED',       'Dibatalkan',          true,  false, 90),
  ('RETURNED',        'Diretur',             true,  false, 100);

-- ---------------------------------------------------------------- stok
-- Efek ke bucket (physical, reserved, quarantine). ADJUST_COUNT (stock opname)
-- satu-satunya yang boleh dua arah.
INSERT INTO movement_types (code, name, physical_sign, reserved_sign, quarantine_sign, is_promise, allow_either_sign) VALUES
  ('RECEIPT',            'Terima barang',                  1,  0,  0, false, false),
  ('RESERVE',            'Reservasi order online',         0,  1,  0, true,  false),
  ('RELEASE',            'Lepas reservasi (batal/expired)', 0, -1,  0, false, false),
  ('PREEMPT',            'Reservasi dikalahkan fakta POS', 0, -1,  0, false, false),
  ('COMMIT_SALE',        'Order online dikirim',          -1, -1,  0, false, false),
  ('DIRECT_SALE',        'Penjualan POS',                 -1,  0,  0, false, false),
  ('QUARANTINE_IN',      'Masuk karantina',                0,  0,  1, false, false),
  ('QUARANTINE_OUT',     'Rilis karantina',                0,  0, -1, false, false),
  ('WRITE_OFF',          'Musnahkan barang karantina',    -1,  0, -1, false, false),
  ('TRANSFER_OUT',       'Transfer keluar',               -1,  0,  0, false, false),
  ('TRANSFER_IN',        'Transfer masuk',                 1,  0,  0, false, false),
  ('ADJUST_COUNT',       'Penyesuaian stock opname',       1,  0,  0, false, true),
  ('ADJUST_DISCREPANCY', 'Fakta POS melebihi stok tercatat', 1, 0,  0, false, false);

-- ---------------------------------------------------------------- RBAC
INSERT INTO roles (code, name, description) VALUES
  ('ADMIN',               'Admin',               'Akses penuh, termasuk kelola user dan aturan'),
  ('OPS_MANAGER',         'Manajer operasional', 'Stok, order, shipment, antrean anomali dan konflik'),
  ('PRODUCTION_OPERATOR', 'Operator produksi',   'Mint kode, commission, agregasi di line produksi'),
  ('WAREHOUSE_STAFF',     'Staf gudang',         'Terima, kirim, karantina, fulfill order'),
  ('STORE_MANAGER',       'Kepala toko',         'Operasional toko, device POS, opname'),
  ('CASHIER',             'Kasir',               'Transaksi POS'),
  ('MARKETING_ANALYST',   'Analis marketing',    'Baca Command Center dan analitik');

INSERT INTO permissions (code, description) VALUES
  ('code.mint',           'Membuat job cetak kode serial'),
  ('serial.commission',   'Commission label ke batch produksi'),
  ('serial.aggregate',    'Agregasi dan bongkar unit, karton, palet'),
  ('serial.receive',      'Menerima hasil produksi ke gudang'),
  ('serial.quarantine',   'Menahan unit (QC gagal, rusak, recall)'),
  ('serial.release',      'Merilis unit dari karantina'),
  ('serial.decommission', 'Void atau memusnahkan unit'),
  ('serial.sell',         'Mencatat penjualan unit'),
  ('serial.return',       'Mencatat retur unit'),
  ('serial.trace',        'Melihat genealogi dan riwayat serial'),
  ('batch.recall',        'Recall batch produksi'),
  ('shipment.create',     'Membuat dan mengirim shipment'),
  ('shipment.receive',    'Menerima shipment'),
  ('inventory.read',      'Melihat saldo stok'),
  ('inventory.adjust',    'Penyesuaian stok (wajib alasan, diaudit)'),
  ('inventory.quarantine','Memindahkan stok ke/dari karantina'),
  ('inventory.transfer',  'Transfer stok antar lokasi'),
  ('order.read',          'Melihat order'),
  ('order.create',        'Membuat atau mengimpor order'),
  ('order.cancel',        'Membatalkan order dan refund'),
  ('order.fulfill',       'Pick, assign serial, kirim order'),
  ('order.return',        'Mencatat retur order'),
  ('pos.sync',            'Sinkronisasi transaksi POS'),
  ('pos.device.manage',   'Provisioning dan pencabutan device POS'),
  ('cc.read',             'Membaca Command Center'),
  ('anomaly.resolve',     'Menindaklanjuti anomali'),
  ('conflict.resolve',    'Menyelesaikan konflik alokasi'),
  ('loyalty.review',      'Review klaim poin berisiko'),
  ('catalog.manage',      'Kelola produk dan kemasan'),
  ('price.manage',        'Kelola daftar harga'),
  ('rule.manage',         'Ubah aturan anomali'),
  ('user.manage',         'Kelola user dan role'),
  ('audit.read',          'Membaca audit log'),
  ('analytics.read',      'Membaca laporan analitik');

INSERT INTO role_permissions (role_code, permission_code)
SELECT 'ADMIN', code FROM permissions;

INSERT INTO role_permissions (role_code, permission_code)
SELECT r.role_code, r.permission_code
  FROM (VALUES
    ('OPS_MANAGER', 'serial.quarantine'), ('OPS_MANAGER', 'serial.release'),
    ('OPS_MANAGER', 'serial.decommission'), ('OPS_MANAGER', 'serial.return'),
    ('OPS_MANAGER', 'serial.trace'), ('OPS_MANAGER', 'batch.recall'),
    ('OPS_MANAGER', 'shipment.create'), ('OPS_MANAGER', 'shipment.receive'),
    ('OPS_MANAGER', 'inventory.read'), ('OPS_MANAGER', 'inventory.adjust'),
    ('OPS_MANAGER', 'inventory.quarantine'), ('OPS_MANAGER', 'inventory.transfer'),
    ('OPS_MANAGER', 'order.read'), ('OPS_MANAGER', 'order.create'),
    ('OPS_MANAGER', 'order.cancel'), ('OPS_MANAGER', 'order.fulfill'),
    ('OPS_MANAGER', 'order.return'), ('OPS_MANAGER', 'pos.device.manage'),
    ('OPS_MANAGER', 'cc.read'), ('OPS_MANAGER', 'anomaly.resolve'),
    ('OPS_MANAGER', 'conflict.resolve'), ('OPS_MANAGER', 'loyalty.review'),
    ('OPS_MANAGER', 'price.manage'), ('OPS_MANAGER', 'rule.manage'),
    ('OPS_MANAGER', 'audit.read'),

    ('PRODUCTION_OPERATOR', 'code.mint'), ('PRODUCTION_OPERATOR', 'serial.commission'),
    ('PRODUCTION_OPERATOR', 'serial.aggregate'), ('PRODUCTION_OPERATOR', 'serial.receive'),
    ('PRODUCTION_OPERATOR', 'serial.quarantine'), ('PRODUCTION_OPERATOR', 'serial.decommission'),
    ('PRODUCTION_OPERATOR', 'serial.trace'), ('PRODUCTION_OPERATOR', 'inventory.read'),

    ('WAREHOUSE_STAFF', 'serial.aggregate'), ('WAREHOUSE_STAFF', 'serial.receive'),
    ('WAREHOUSE_STAFF', 'serial.quarantine'), ('WAREHOUSE_STAFF', 'serial.sell'),
    ('WAREHOUSE_STAFF', 'serial.trace'), ('WAREHOUSE_STAFF', 'shipment.create'),
    ('WAREHOUSE_STAFF', 'shipment.receive'), ('WAREHOUSE_STAFF', 'inventory.read'),
    ('WAREHOUSE_STAFF', 'inventory.quarantine'), ('WAREHOUSE_STAFF', 'inventory.transfer'),
    ('WAREHOUSE_STAFF', 'order.read'), ('WAREHOUSE_STAFF', 'order.fulfill'),

    ('STORE_MANAGER', 'serial.sell'), ('STORE_MANAGER', 'serial.return'),
    ('STORE_MANAGER', 'serial.quarantine'), ('STORE_MANAGER', 'serial.trace'),
    ('STORE_MANAGER', 'shipment.receive'), ('STORE_MANAGER', 'inventory.read'),
    ('STORE_MANAGER', 'inventory.adjust'), ('STORE_MANAGER', 'order.read'),
    ('STORE_MANAGER', 'order.fulfill'), ('STORE_MANAGER', 'pos.sync'),
    ('STORE_MANAGER', 'pos.device.manage'), ('STORE_MANAGER', 'cc.read'),

    ('CASHIER', 'pos.sync'), ('CASHIER', 'serial.sell'),
    ('CASHIER', 'serial.return'), ('CASHIER', 'inventory.read'),

    ('MARKETING_ANALYST', 'cc.read'), ('MARKETING_ANALYST', 'analytics.read'),
    ('MARKETING_ANALYST', 'serial.trace')
  ) AS r (role_code, permission_code);

-- ---------------------------------------------------------------- state machine
-- State machine PLANNING §5.7 + transisi dari catatan edge case di bagian yang
-- sama (EXPIRED -> NEEDS_ACTION: bayar telat, re-reserve gagal). from_code
-- NULL = status awal. permission NULL = hanya dipicu sistem, konsumen, atau webhook.
INSERT INTO lifecycle_transitions (entity_type, from_code, to_code, biz_step, permission, description) VALUES
  ('SERIAL_UNIT', NULL,                 'GENERATED',          'encoding',            'code.mint',           'Mint kode'),
  ('SERIAL_UNIT', 'GENERATED',          'COMMISSIONED',       'commissioning',       'serial.commission',   'Commission di line produksi'),
  ('SERIAL_UNIT', 'GENERATED',          'DECOMMISSIONED',     'decommissioning',     'serial.decommission', 'Label void/rusak'),
  ('SERIAL_UNIT', 'COMMISSIONED',       'IN_STOCK',           'receiving',           'serial.receive',      'Diterima gudang pusat, QC lolos'),
  ('SERIAL_UNIT', 'COMMISSIONED',       'QUARANTINED',        'holding',             'serial.quarantine',   'QC gagal'),
  ('SERIAL_UNIT', 'IN_STOCK',           'IN_TRANSIT',         'shipping',            'shipment.create',     'Dikirim'),
  ('SERIAL_UNIT', 'IN_STOCK',           'SOLD',               'retail_selling',      'serial.sell',         'Dijual (POS/D2C)'),
  ('SERIAL_UNIT', 'IN_STOCK',           'QUARANTINED',        'holding',             'serial.quarantine',   'Hold/recall'),
  ('SERIAL_UNIT', 'IN_TRANSIT',         'IN_STOCK',           'receiving',           'shipment.receive',    'Diterima lokasi sendiri'),
  ('SERIAL_UNIT', 'IN_TRANSIT',         'AT_PARTNER',         'receiving',           'shipment.receive',    'Diterima mitra'),
  ('SERIAL_UNIT', 'IN_TRANSIT',         'QUARANTINED',        'holding',             'serial.quarantine',   'Rusak di jalan'),
  ('SERIAL_UNIT', 'AT_PARTNER',         'IN_TRANSIT',         'shipping',            'shipment.create',     'Transfer/retur dari mitra'),
  ('SERIAL_UNIT', 'AT_PARTNER',         'SOLD',               'retail_selling',      'serial.sell',         'Laporan jual mitra'),
  ('SERIAL_UNIT', 'AT_PARTNER',         'CONSUMER_ACTIVATED', 'consumer_activating', NULL,                  'Klaim konsumen'),
  ('SERIAL_UNIT', 'AT_PARTNER',         'QUARANTINED',        'holding',             'serial.quarantine',   'Recall'),
  ('SERIAL_UNIT', 'SOLD',               'CONSUMER_ACTIVATED', 'consumer_activating', NULL,                  'Klaim PIN'),
  ('SERIAL_UNIT', 'SOLD',               'RETURNED',           'returning',           'serial.return',       'Retur'),
  ('SERIAL_UNIT', 'CONSUMER_ACTIVATED', 'RETURNED',           'returning',           'serial.return',       'Retur'),
  ('SERIAL_UNIT', 'RETURNED',           'QUARANTINED',        'inspecting',          'serial.quarantine',   'Inspeksi'),
  ('SERIAL_UNIT', 'QUARANTINED',        'IN_STOCK',           'releasing',           'serial.release',      'Dirilis'),
  ('SERIAL_UNIT', 'QUARANTINED',        'DECOMMISSIONED',     'destroying',          'serial.decommission', 'Dimusnahkan'),

  ('ORDER', NULL,              'PENDING_PAYMENT', 'order_placed',               'order.create',     'Dibuat + reserve'),
  ('ORDER', NULL,              'PAID',            'marketplace_imported',       'order.create',     'Impor marketplace (sudah dibayar)'),
  ('ORDER', NULL,              'COMPLETED',       'pos_sale',                   'pos.sync',         'Transaksi POS'),
  ('ORDER', 'PENDING_PAYMENT', 'PAID',            'payment_captured',           NULL,               'Webhook bayar (idempotent)'),
  ('ORDER', 'PENDING_PAYMENT', 'EXPIRED',         'payment_expired',            NULL,               'TTL habis, release'),
  ('ORDER', 'PENDING_PAYMENT', 'CANCELLED',       'cancelled',                  'order.cancel',     'Dibatalkan, release'),
  ('ORDER', 'EXPIRED',         'PAID',            'late_payment_rereserved',    NULL,               'Bayar telat, re-reserve berhasil'),
  ('ORDER', 'EXPIRED',         'NEEDS_ACTION',    'late_payment_unfulfillable', NULL,               'Bayar telat, re-reserve gagal: refund otomatis'),
  ('ORDER', 'PAID',            'FULFILLING',      'picking',                    'order.fulfill',    'Pick + assign serial'),
  ('ORDER', 'PAID',            'NEEDS_ACTION',    'reallocation_failed',        NULL,               'Di-preempt dan realokasi gagal'),
  ('ORDER', 'PAID',            'CANCELLED',       'refunded',                   'order.cancel',     'Refund, release'),
  ('ORDER', 'NEEDS_ACTION',    'PAID',            'reallocated',                'conflict.resolve', 'Realokasi berhasil'),
  ('ORDER', 'NEEDS_ACTION',    'CANCELLED',       'refunded',                   'order.cancel',     'Refund + kompensasi'),
  ('ORDER', 'FULFILLING',      'SHIPPED',         'shipping',                   'order.fulfill',    'Commit stok'),
  ('ORDER', 'SHIPPED',         'DELIVERED',       'delivered',                  'order.fulfill',    'Diterima pelanggan'),
  ('ORDER', 'DELIVERED',       'COMPLETED',       'completed',                  'order.fulfill',    'Selesai'),
  ('ORDER', 'DELIVERED',       'RETURNED',        'returning',                  'order.return',     'Retur');

-- ---------------------------------------------------------------- radar & risiko
-- Aturan PLANNING §7.6 (SCAN) plus aturan risiko klaim (§7.2), POS, dan stok.
-- risk_weight = kontribusi ke skor risiko klaim (0-100).
INSERT INTO anomaly_rules (code, name, description, category, enabled, severity, consumer_effect, params) VALUES
  ('UNKNOWN_CODE', 'Kode tidak terdaftar',
   'Kode yang dipindai tidak ada di DB. Diagregasi per wilayah/hari sebagai hotspot pemalsuan.',
   'SCAN', true, 'MEDIUM', 'NOT_FOUND',
   '{"hotspot_window_hours": 24, "hotspot_min_scans": 10}'),
  ('NON_SELLABLE_SCAN', 'Scan pada status tidak wajar',
   'Konsumen memindai unit yang belum/tidak beredar, atau unit IN_STOCK di gudang pusat yang belum pernah dikirim.',
   'SCAN', true, 'HIGH', 'SUSPICIOUS',
   '{"statuses": ["GENERATED", "COMMISSIONED", "DECOMMISSIONED"], "include_unshipped_central_stock": true}'),
  ('IMPOSSIBLE_TRAVEL', 'Perjalanan mustahil',
   'Jarak haversine ke scan sebelumnya dibagi selisih waktu melebihi batas kecepatan.',
   'SCAN', true, 'HIGH', 'SUSPICIOUS',
   '{"max_speed_kmh": 900, "min_distance_km_gps": 100, "min_distance_km_ip": 500}'),
  ('SCAN_VELOCITY', 'Terlalu banyak pemindai',
   'Lebih dari N fingerprint berbeda memindai kode unit yang sama dalam jendela waktu.',
   'SCAN', true, 'MEDIUM', 'SUSPICIOUS',
   '{"max_fingerprints": 5, "window_hours": 24}'),
  ('OUT_OF_TERRITORY', 'Di luar wilayah distributor',
   'Wilayah scan di luar wilayah custodian dengan coverage_mode SELECTED. Internal saja; konsumen tetap melihat GENUINE.',
   'SCAN', true, 'MEDIUM', 'NONE',
   '{"leak_index_min_scans": 20, "leak_index_baseline_ratio": 0.05, "test_buy_immediate": true}'),
  ('BATCH_RECALLED', 'Batch ditarik',
   'Batch unit berstatus RECALLED; konsumen diberi instruksi recall.',
   'SCAN', true, 'HIGH', 'RECALLED',
   '{}'),

  ('CLAIM_NEAR_STORE', 'Klaim di dekat toko',
   'Klaim dalam radius dekat toko/gerai saat jam buka; konsumen biasanya klaim di rumah.',
   'CLAIM', true, 'LOW', 'NONE',
   '{"radius_m": 100, "during_opening_hours": true, "risk_weight": 30}'),
  ('CLAIM_ACCOUNT_VELOCITY', 'Klaim beruntun satu akun',
   'Lebih dari N klaim per akun dalam jendela waktu.',
   'CLAIM', true, 'MEDIUM', 'NONE',
   '{"max_claims": 5, "window_hours": 24, "risk_weight": 40}'),
  ('CLAIM_CARTON_SPREAD', 'Satu karton, banyak akun',
   'Unit dari karton yang sama diklaim banyak akun berbeda dalam waktu singkat.',
   'CLAIM', true, 'MEDIUM', 'NONE',
   '{"max_accounts": 3, "window_hours": 24, "risk_weight": 30}'),
  ('CLAIM_SHARED_DEVICE', 'Satu device, banyak akun',
   'Satu fingerprint device dipakai lebih dari N akun.',
   'CLAIM', true, 'MEDIUM', 'NONE',
   '{"max_accounts": 3, "risk_weight": 40}'),
  ('CLAIM_PIN_BRUTE_FORCE', 'Percobaan PIN berulang',
   'PIN klaim salah berulang; unit dikunci setelah batas percobaan.',
   'CLAIM', true, 'HIGH', 'NONE',
   '{"max_failed_attempts": 5}'),

  ('POS_PAYLOAD_MISMATCH', 'Transaksi POS dimanipulasi',
   'client_txn_id sama dengan isi berbeda, atau signature tidak valid.',
   'POS', true, 'CRITICAL', 'NONE',
   '{}'),
  ('POS_UNREGISTERED_SERIAL', 'Toko menjual serial tidak terdaftar',
   'Serial yang dijual offline ternyata tidak terdaftar setelah sync; indikasi barang palsu di toko.',
   'POS', true, 'CRITICAL', 'NONE',
   '{}'),

  ('STOCK_DISCREPANCY', 'Selisih stok POS',
   'Fakta penjualan POS melebihi stok fisik tercatat; perlu stock opname.',
   'STOCK', true, 'MEDIUM', 'NONE',
   '{}'),
  ('LEDGER_DRIFT', 'Ledger tidak cocok dengan saldo',
   'SUM delta inventory_movements berbeda dengan inventory_balances untuk satu (lokasi, produk).',
   'STOCK', true, 'CRITICAL', 'NONE',
   '{}');

-- +goose Down
-- DELETE (bukan TRUNCATE CASCADE): gagal dengan jelas kalau data bisnis masih
-- mereferensikan lookup ini, alih-alih ikut menghapus data itu.
DELETE FROM anomaly_rules;
DELETE FROM lifecycle_transitions;
DELETE FROM role_permissions;
DELETE FROM permissions;
DELETE FROM roles;
DELETE FROM movement_types;
DELETE FROM order_statuses;
DELETE FROM serial_unit_statuses;
DELETE FROM role_types;
DELETE FROM location_types;
DELETE FROM channels;
DELETE FROM packaging_levels;
