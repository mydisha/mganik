-- +goose Up
-- Perapian teks data lookup yang tampil di UI (nama status, jenis mutasi,
-- role, permission, transisi, aturan anomali). Hanya kolom teks; kode,
-- parameter, dan relasi tidak berubah. Istilah mengikuti glosarium produk:
-- "perangkat kasir" (bukan device), "pengiriman" (bukan shipment),
-- "dalam perjalanan", "ditarik (recall)", "mengikat label ke batch"
-- (commission). Down mengembalikan teks lama apa adanya.

UPDATE serial_unit_statuses s SET name = v.name
  FROM (VALUES
    ('GENERATED',          'Kode dibuat'),
    ('COMMISSIONED',       'Terikat ke batch'),
    ('IN_STOCK',           'Di gudang atau toko sendiri'),
    ('IN_TRANSIT',         'Dalam perjalanan'),
    ('CONSUMER_ACTIVATED', 'Diklaim konsumen'),
    ('DECOMMISSIONED',     'Dimusnahkan atau dibatalkan')
  ) AS v (code, name)
 WHERE s.code = v.code;

UPDATE movement_types m SET name = v.name
  FROM (VALUES
    ('RECEIPT',            'Penerimaan barang'),
    ('RELEASE',            'Pelepasan reservasi (batal atau kedaluwarsa)'),
    ('PREEMPT',            'Reservasi terpakai penjualan toko'),
    ('COMMIT_SALE',        'Pengiriman order online'),
    ('QUARANTINE_OUT',     'Keluar karantina'),
    ('WRITE_OFF',          'Pemusnahan barang karantina'),
    ('ADJUST_DISCREPANCY', 'Koreksi selisih penjualan POS')
  ) AS v (code, name)
 WHERE m.code = v.code;

UPDATE roles r SET description = v.description
  FROM (VALUES
    ('ADMIN',               'Akses penuh, termasuk mengelola user dan aturan anomali'),
    ('OPS_MANAGER',         'Stok, order, pengiriman, serta antrean anomali dan konflik'),
    ('PRODUCTION_OPERATOR', 'Membuat kode, mengikat label ke batch, dan mengemas di lini produksi'),
    ('WAREHOUSE_STAFF',     'Menerima dan mengirim barang, karantina, dan memproses order'),
    ('STORE_MANAGER',       'Operasional toko, perangkat kasir, dan stock opname'),
    ('CASHIER',             'Transaksi di kasir toko'),
    ('MARKETING_ANALYST',   'Melihat Command Center dan laporan analitik')
  ) AS v (code, description)
 WHERE r.code = v.code;

UPDATE permissions p SET description = v.description
  FROM (VALUES
    ('serial.commission',    'Mengikat label ke batch produksi'),
    ('serial.aggregate',     'Mengemas dan membongkar unit, karton, dan palet'),
    ('serial.quarantine',    'Mengarantina unit (QC gagal, rusak, atau ditarik)'),
    ('serial.release',       'Mengeluarkan unit dari karantina'),
    ('serial.decommission',  'Membatalkan atau memusnahkan unit'),
    ('batch.recall',         'Menarik (recall) batch produksi'),
    ('shipment.create',      'Membuat dan memberangkatkan pengiriman'),
    ('shipment.receive',     'Menerima pengiriman'),
    ('inventory.adjust',     'Menyesuaikan stok (wajib alasan, tercatat di audit)'),
    ('inventory.quarantine', 'Memindahkan stok ke dan dari karantina'),
    ('inventory.transfer',   'Memindahkan stok antarlokasi'),
    ('order.cancel',         'Membatalkan order dan mengembalikan dana'),
    ('order.fulfill',        'Mengambil barang, mencatat serial, dan mengirim order'),
    ('pos.sync',             'Menyinkronkan transaksi kasir'),
    ('pos.device.manage',    'Mendaftarkan dan mencabut perangkat kasir'),
    ('cc.read',              'Melihat Command Center'),
    ('loyalty.review',       'Meninjau klaim poin berisiko'),
    ('catalog.manage',       'Mengelola produk dan spesifikasi kemasan'),
    ('price.manage',         'Mengelola daftar harga'),
    ('rule.manage',          'Mengubah aturan anomali'),
    ('user.manage',          'Mengelola user dan role'),
    ('audit.read',           'Melihat audit log'),
    ('analytics.read',       'Melihat laporan analitik')
  ) AS v (code, description)
 WHERE p.code = v.code;

UPDATE lifecycle_transitions t SET description = v.description
  FROM (VALUES
    ('SERIAL_UNIT', NULL,              'GENERATED',      'Kode dibuat'),
    ('SERIAL_UNIT', 'GENERATED',       'COMMISSIONED',   'Label diikat ke batch di lini produksi'),
    ('SERIAL_UNIT', 'GENERATED',       'DECOMMISSIONED', 'Label dibatalkan atau rusak'),
    ('SERIAL_UNIT', 'IN_STOCK',        'SOLD',           'Dijual (POS atau D2C)'),
    ('SERIAL_UNIT', 'IN_STOCK',        'QUARANTINED',    'Ditahan atau ditarik'),
    ('SERIAL_UNIT', 'AT_PARTNER',      'IN_TRANSIT',     'Transfer atau retur dari mitra'),
    ('SERIAL_UNIT', 'AT_PARTNER',      'QUARANTINED',    'Ditarik dari mitra'),
    ('SERIAL_UNIT', 'QUARANTINED',     'IN_STOCK',       'Dikeluarkan dari karantina'),
    ('SERIAL_UNIT', 'CONSUMER_ACTIVATED', 'SOLD',        'Klaim ditolak atau dibatalkan, unit kembali terjual'),
    ('SERIAL_UNIT', 'CONSUMER_ACTIVATED', 'AT_PARTNER',  'Klaim ditolak atau dibatalkan, unit kembali di mitra'),
    ('ORDER',       NULL,              'PENDING_PAYMENT', 'Dibuat dan stok dipesan'),
    ('ORDER',       'PENDING_PAYMENT', 'PAID',           'Pembayaran diterima'),
    ('ORDER',       'PENDING_PAYMENT', 'EXPIRED',        'Batas bayar habis, reservasi dilepas'),
    ('ORDER',       'PENDING_PAYMENT', 'CANCELLED',      'Dibatalkan, reservasi dilepas'),
    ('ORDER',       'EXPIRED',         'PAID',           'Bayar telat, stok berhasil dipesan ulang'),
    ('ORDER',       'EXPIRED',         'NEEDS_ACTION',   'Bayar telat dan stok habis: perlu refund'),
    ('ORDER',       'PAID',            'FULFILLING',     'Barang diambil dan serial dicatat'),
    ('ORDER',       'PAID',            'NEEDS_ACTION',   'Stok terpakai penjualan toko, tidak ada lokasi pengganti'),
    ('ORDER',       'PAID',            'CANCELLED',      'Refund, reservasi dilepas'),
    ('ORDER',       'NEEDS_ACTION',    'PAID',           'Stok berhasil dipesan di lokasi lain'),
    ('ORDER',       'NEEDS_ACTION',    'CANCELLED',      'Refund dan kompensasi'),
    ('ORDER',       'FULFILLING',      'SHIPPED',        'Dikirim, stok dikurangi')
  ) AS v (entity_type, from_code, to_code, description)
 WHERE t.entity_type = v.entity_type
   AND t.from_code IS NOT DISTINCT FROM v.from_code
   AND t.to_code = v.to_code;

UPDATE anomaly_rules a SET name = v.name, description = v.description
  FROM (VALUES
    ('UNKNOWN_CODE', 'Kode tidak terdaftar',
     'Konsumen memindai kode yang tidak terdaftar. Pemindaian dikelompokkan per wilayah per hari untuk menemukan titik rawan pemalsuan.'),
    ('NON_SELLABLE_SCAN', 'Pemindaian unit yang belum beredar',
     'Konsumen memindai unit yang belum atau tidak lagi beredar, atau unit di gudang pusat yang belum pernah dikirim.'),
    ('IMPOSSIBLE_TRAVEL', 'Perjalanan mustahil',
     'Jarak ke pemindaian sebelumnya dibagi selisih waktunya melebihi batas kecepatan wajar.'),
    ('SCAN_VELOCITY', 'Dipindai terlalu banyak perangkat',
     'Jumlah perangkat berbeda yang memindai kode unit yang sama dalam satu jendela waktu melebihi batas.'),
    ('OUT_OF_TERRITORY', 'Di luar wilayah distributor',
     'Unit dipindai di luar wilayah resmi distributor pemegangnya (distributor dengan wilayah terbatas). Hanya untuk internal; konsumen tetap melihat hasil asli.'),
    ('BATCH_RECALLED', 'Batch ditarik',
     'Unit berasal dari batch yang ditarik (recall); konsumen mendapat petunjuk penarikan.'),
    ('CLAIM_NEAR_STORE', 'Klaim di dekat toko',
     'Klaim dilakukan di dekat toko atau gerai saat jam buka. Konsumen biasanya mengklaim di rumah.'),
    ('CLAIM_ACCOUNT_VELOCITY', 'Klaim beruntun satu akun',
     'Satu akun mengklaim lebih banyak dari batas dalam satu jendela waktu.'),
    ('CLAIM_SHARED_DEVICE', 'Satu perangkat, banyak akun',
     'Satu perangkat konsumen dipakai oleh lebih banyak akun dari batas.'),
    ('CLAIM_PIN_BRUTE_FORCE', 'Percobaan PIN berulang',
     'PIN klaim salah berkali-kali; unit dikunci setelah batas percobaan.'),
    ('POS_PAYLOAD_MISMATCH', 'Isi transaksi POS tidak konsisten',
     'ID transaksi yang sama dikirim dengan isi berbeda, atau tanda tangan transaksi tidak valid. Bisa berarti manipulasi atau bug di perangkat kasir.'),
    ('POS_UNREGISTERED_SERIAL', 'Toko menjual serial tidak terdaftar',
     'Serial yang dijual saat offline ternyata tidak terdaftar setelah sinkronisasi; bisa menandakan barang palsu di toko.'),
    ('STOCK_DISCREPANCY', 'Selisih stok POS',
     'Penjualan POS melebihi stok fisik tercatat; perlu stock opname.'),
    ('LEDGER_DRIFT', 'Mutasi tidak cocok dengan saldo',
     'Jumlah mutasi stok berbeda dengan saldo tersimpan untuk satu lokasi dan produk.'),
    ('POS_PAYMENT_REF_REUSED', 'Kode approval pembayaran dipakai ulang',
     'Kode approval EDC atau QRIS yang sama sudah tercatat untuk transaksi lain. Penjualan tetap dicatat; pembayarannya menunggu verifikasi.'),
    ('POS_CASHIER_INACTIVE', 'Transaksi POS oleh kasir nonaktif',
     'Kasir punya akses kasir di toko itu, tetapi akunnya sudah nonaktif atau diarsipkan saat transaksi offline tersinkron. Penjualan tetap dicatat.'),
    ('POS_OFFLINE_VALUE_LIMIT', 'Nilai transaksi POS di atas batas offline',
     'Total satu transaksi POS melebihi batas nilai offline (POS_OFFLINE_VALUE_LIMIT). Tetap dicatat dan perlu ditinjau.')
  ) AS v (code, name, description)
 WHERE a.code = v.code;

-- +goose Down
UPDATE serial_unit_statuses s SET name = v.name
  FROM (VALUES
    ('GENERATED',          'Kode dicetak'),
    ('COMMISSIONED',       'Di-commission'),
    ('IN_STOCK',           'Di gudang/toko sendiri'),
    ('IN_TRANSIT',         'Dalam pengiriman'),
    ('CONSUMER_ACTIVATED', 'Diaktifkan konsumen'),
    ('DECOMMISSIONED',     'Dimusnahkan/void')
  ) AS v (code, name)
 WHERE s.code = v.code;

UPDATE movement_types m SET name = v.name
  FROM (VALUES
    ('RECEIPT',            'Terima barang'),
    ('RELEASE',            'Lepas reservasi (batal/expired)'),
    ('PREEMPT',            'Reservasi dikalahkan fakta POS'),
    ('COMMIT_SALE',        'Order online dikirim'),
    ('QUARANTINE_OUT',     'Rilis karantina'),
    ('WRITE_OFF',          'Musnahkan barang karantina'),
    ('ADJUST_DISCREPANCY', 'Fakta POS melebihi stok tercatat')
  ) AS v (code, name)
 WHERE m.code = v.code;

UPDATE roles r SET description = v.description
  FROM (VALUES
    ('ADMIN',               'Akses penuh, termasuk kelola user dan aturan'),
    ('OPS_MANAGER',         'Stok, order, shipment, antrean anomali dan konflik'),
    ('PRODUCTION_OPERATOR', 'Mint kode, commission, agregasi di line produksi'),
    ('WAREHOUSE_STAFF',     'Terima, kirim, karantina, fulfill order'),
    ('STORE_MANAGER',       'Operasional toko, device POS, opname'),
    ('CASHIER',             'Transaksi POS'),
    ('MARKETING_ANALYST',   'Baca Command Center dan analitik')
  ) AS v (code, description)
 WHERE r.code = v.code;

UPDATE permissions p SET description = v.description
  FROM (VALUES
    ('serial.commission',    'Commission label ke batch produksi'),
    ('serial.aggregate',     'Agregasi dan bongkar unit, karton, palet'),
    ('serial.quarantine',    'Menahan unit (QC gagal, rusak, recall)'),
    ('serial.release',       'Merilis unit dari karantina'),
    ('serial.decommission',  'Void atau memusnahkan unit'),
    ('batch.recall',         'Recall batch produksi'),
    ('shipment.create',      'Membuat dan mengirim shipment'),
    ('shipment.receive',     'Menerima shipment'),
    ('inventory.adjust',     'Penyesuaian stok (wajib alasan, diaudit)'),
    ('inventory.quarantine', 'Memindahkan stok ke/dari karantina'),
    ('inventory.transfer',   'Transfer stok antar lokasi'),
    ('order.cancel',         'Membatalkan order dan refund'),
    ('order.fulfill',        'Pick, assign serial, kirim order'),
    ('pos.sync',             'Sinkronisasi transaksi POS'),
    ('pos.device.manage',    'Provisioning dan pencabutan device POS'),
    ('cc.read',              'Membaca Command Center'),
    ('loyalty.review',       'Review klaim poin berisiko'),
    ('catalog.manage',       'Kelola produk dan kemasan'),
    ('price.manage',         'Kelola daftar harga'),
    ('rule.manage',          'Ubah aturan anomali'),
    ('user.manage',          'Kelola user dan role'),
    ('audit.read',           'Membaca audit log'),
    ('analytics.read',       'Membaca laporan analitik')
  ) AS v (code, description)
 WHERE p.code = v.code;

UPDATE lifecycle_transitions t SET description = v.description
  FROM (VALUES
    ('SERIAL_UNIT', NULL,              'GENERATED',      'Mint kode'),
    ('SERIAL_UNIT', 'GENERATED',       'COMMISSIONED',   'Commission di line produksi'),
    ('SERIAL_UNIT', 'GENERATED',       'DECOMMISSIONED', 'Label void/rusak'),
    ('SERIAL_UNIT', 'IN_STOCK',        'SOLD',           'Dijual (POS/D2C)'),
    ('SERIAL_UNIT', 'IN_STOCK',        'QUARANTINED',    'Hold/recall'),
    ('SERIAL_UNIT', 'AT_PARTNER',      'IN_TRANSIT',     'Transfer/retur dari mitra'),
    ('SERIAL_UNIT', 'AT_PARTNER',      'QUARANTINED',    'Recall'),
    ('SERIAL_UNIT', 'QUARANTINED',     'IN_STOCK',       'Dirilis'),
    ('SERIAL_UNIT', 'CONSUMER_ACTIVATED', 'SOLD',        'Klaim ditolak/dibalik, unit kembali terjual'),
    ('SERIAL_UNIT', 'CONSUMER_ACTIVATED', 'AT_PARTNER',  'Klaim ditolak/dibalik, unit kembali di mitra'),
    ('ORDER',       NULL,              'PENDING_PAYMENT', 'Dibuat + reserve'),
    ('ORDER',       'PENDING_PAYMENT', 'PAID',           'Webhook bayar (idempotent)'),
    ('ORDER',       'PENDING_PAYMENT', 'EXPIRED',        'TTL habis, release'),
    ('ORDER',       'PENDING_PAYMENT', 'CANCELLED',      'Dibatalkan, release'),
    ('ORDER',       'EXPIRED',         'PAID',           'Bayar telat, re-reserve berhasil'),
    ('ORDER',       'EXPIRED',         'NEEDS_ACTION',   'Bayar telat, re-reserve gagal: refund otomatis'),
    ('ORDER',       'PAID',            'FULFILLING',     'Pick + assign serial'),
    ('ORDER',       'PAID',            'NEEDS_ACTION',   'Di-preempt dan realokasi gagal'),
    ('ORDER',       'PAID',            'CANCELLED',      'Refund, release'),
    ('ORDER',       'NEEDS_ACTION',    'PAID',           'Realokasi berhasil'),
    ('ORDER',       'NEEDS_ACTION',    'CANCELLED',      'Refund + kompensasi'),
    ('ORDER',       'FULFILLING',      'SHIPPED',        'Commit stok')
  ) AS v (entity_type, from_code, to_code, description)
 WHERE t.entity_type = v.entity_type
   AND t.from_code IS NOT DISTINCT FROM v.from_code
   AND t.to_code = v.to_code;

UPDATE anomaly_rules a SET name = v.name, description = v.description
  FROM (VALUES
    ('UNKNOWN_CODE', 'Kode tidak terdaftar',
     'Kode yang dipindai tidak ada di DB. Diagregasi per wilayah/hari sebagai hotspot pemalsuan.'),
    ('NON_SELLABLE_SCAN', 'Scan pada status tidak wajar',
     'Konsumen memindai unit yang belum/tidak beredar, atau unit IN_STOCK di gudang pusat yang belum pernah dikirim.'),
    ('IMPOSSIBLE_TRAVEL', 'Perjalanan mustahil',
     'Jarak haversine ke scan sebelumnya dibagi selisih waktu melebihi batas kecepatan.'),
    ('SCAN_VELOCITY', 'Terlalu banyak pemindai',
     'Lebih dari N fingerprint berbeda memindai kode unit yang sama dalam jendela waktu.'),
    ('OUT_OF_TERRITORY', 'Di luar wilayah distributor',
     'Wilayah scan di luar wilayah custodian dengan coverage_mode SELECTED. Internal saja; konsumen tetap melihat GENUINE.'),
    ('BATCH_RECALLED', 'Batch ditarik',
     'Batch unit berstatus RECALLED; konsumen diberi instruksi recall.'),
    ('CLAIM_NEAR_STORE', 'Klaim di dekat toko',
     'Klaim dalam radius dekat toko/gerai saat jam buka; konsumen biasanya klaim di rumah.'),
    ('CLAIM_ACCOUNT_VELOCITY', 'Klaim beruntun satu akun',
     'Lebih dari N klaim per akun dalam jendela waktu.'),
    ('CLAIM_SHARED_DEVICE', 'Satu device, banyak akun',
     'Satu fingerprint device dipakai lebih dari N akun.'),
    ('CLAIM_PIN_BRUTE_FORCE', 'Percobaan PIN berulang',
     'PIN klaim salah berulang; unit dikunci setelah batas percobaan.'),
    ('POS_PAYLOAD_MISMATCH', 'Transaksi POS dimanipulasi',
     'client_txn_id sama dengan isi berbeda, atau signature tidak valid.'),
    ('POS_UNREGISTERED_SERIAL', 'Toko menjual serial tidak terdaftar',
     'Serial yang dijual offline ternyata tidak terdaftar setelah sync; indikasi barang palsu di toko.'),
    ('STOCK_DISCREPANCY', 'Selisih stok POS',
     'Fakta penjualan POS melebihi stok fisik tercatat; perlu stock opname.'),
    ('LEDGER_DRIFT', 'Ledger tidak cocok dengan saldo',
     'SUM delta inventory_movements berbeda dengan inventory_balances untuk satu (lokasi, produk).'),
    ('POS_PAYMENT_REF_REUSED', 'Approval pembayaran POS dipakai ulang',
     'approval_ref EDC/QRIS yang sama sudah tercatat untuk transaksi lain; penjualan tetap dicatat, pembayaran menunggu verifikasi.'),
    ('POS_CASHIER_INACTIVE', 'Transaksi POS oleh kasir nonaktif',
     'Kasir punya role pos.sync di lokasi itu tetapi user sudah nonaktif/diarsipkan saat transaksi offline tersinkron.'),
    ('POS_OFFLINE_VALUE_LIMIT', 'Nilai transaksi POS di atas batas offline',
     'Total satu transaksi POS melebihi POS_OFFLINE_VALUE_LIMIT; tetap dicatat, perlu ditinjau.')
  ) AS v (code, name, description)
 WHERE a.code = v.code;
