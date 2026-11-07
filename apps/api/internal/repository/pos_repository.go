package repository

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// PosRepository mengakses device POS, transaksi sync, data bootstrap, dan
// resolusi konflik alokasi (Fase 4). Semua method menerima tx/db dari service.
//
// Urutan lock global (sama dengan jalur online, lihat OrderRepository):
// pos_transactions (baris baru, unique index) → order → reservasi (urut
// lokasi, produk, id) → saldo (urut lokasi, produk) → serial_units (urut id)
// → pos_devices → locations. Job penanda OFFLINE hanya mengunci satu baris
// locations per transaksi (SKIP LOCKED).
type PosRepository struct{}

// ------------------------------------------------------------------ device

// PosDeviceRow adalah device beserta identitas lokasinya. SigningKeyEnc
// hanya dibaca jalur sync; tidak pernah dikirim ke klien.
type PosDeviceRow struct {
	ID            uuid.UUID
	LocationID    uuid.UUID
	LocationCode  string
	LocationName  string
	Name          string
	Status        string
	SigningKeyEnc []byte
	LastSeenAt    *time.Time
	LastAckedSeq  int64
	ProvisionedBy *uuid.UUID
	RevokedAt     *time.Time
	Version       int64
	CreatedAt     time.Time
}

const posDeviceColumns = `d.id, d.location_id, l.code AS location_code, l.name AS location_name, d.name, d.status,
	       d.signing_key_enc, d.last_seen_at, d.last_acked_seq, d.provisioned_by, d.revoked_at, d.version, d.created_at`

// InsertDevice menulis device baru (token_hash dan signing_key_enc sudah jadi).
func (PosRepository) InsertDevice(tx *gorm.DB, d *model.PosDevice) error {
	return tx.Exec(`
		INSERT INTO pos_devices (id, location_id, name, token_hash, signing_key_enc, status, provisioned_by, created_at, updated_at)
		VALUES (?, ?, ?, ?::bytea, ?::bytea, 'ACTIVE', ?, ?, ?)`,
		d.ID, d.LocationID, d.Name, d.TokenHash, d.SigningKeyEnc, d.ProvisionedBy, d.CreatedAt, d.UpdatedAt).Error
}

// DeviceByID membaca device (nil kalau tidak ada). lock=true mengunci baris
// device (FOR NO KEY UPDATE OF d).
func (PosRepository) DeviceByID(tx *gorm.DB, id uuid.UUID, lock bool) (*PosDeviceRow, error) {
	q := `SELECT ` + posDeviceColumns + ` FROM pos_devices d JOIN locations l ON l.id = d.location_id WHERE d.id = ?`
	if lock {
		q += ` FOR NO KEY UPDATE OF d`
	}
	var rows []PosDeviceRow
	if err := tx.Raw(q, id).Scan(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// RevokeDevice mencabut device ACTIVE. changed=false kalau sudah dicabut.
func (PosRepository) RevokeDevice(tx *gorm.DB, id uuid.UUID, now time.Time) (bool, error) {
	res := tx.Exec(`
		UPDATE pos_devices SET status = 'REVOKED', revoked_at = ?, version = version + 1, updated_at = ?
		 WHERE id = ? AND status = 'ACTIVE'`, now, now, id)
	return res.RowsAffected == 1, res.Error
}

// PosDeviceFilter: AllLocations=false membatasi ke LocationIDs (kosong =
// tidak ada hasil). After = cursor keyset (created_at, id) urut terbaru dulu.
type PosDeviceFilter struct {
	AllLocations bool
	LocationIDs  []uuid.UUID
	LocationID   *uuid.UUID
	AfterAt      *time.Time
	AfterID      *uuid.UUID
	Limit        int
}

// ListDevices mengembalikan maksimal Limit+1 device (untuk pagination.Trim).
func (PosRepository) ListDevices(tx *gorm.DB, f PosDeviceFilter) ([]PosDeviceRow, error) {
	if !f.AllLocations && len(f.LocationIDs) == 0 {
		return nil, nil
	}
	q := `SELECT ` + posDeviceColumns + ` FROM pos_devices d JOIN locations l ON l.id = d.location_id WHERE true`
	var args []any
	if !f.AllLocations {
		q += ` AND d.location_id = ANY(CAST(? AS uuid[]))`
		args = append(args, SerialUUIDArray(f.LocationIDs))
	}
	if f.LocationID != nil {
		q += ` AND d.location_id = ?`
		args = append(args, *f.LocationID)
	}
	if f.AfterAt != nil && f.AfterID != nil {
		q += ` AND (d.created_at, d.id) < (?, CAST(? AS uuid))`
		args = append(args, *f.AfterAt, *f.AfterID)
	}
	q += ` ORDER BY d.created_at DESC, d.id DESC LIMIT ?`
	args = append(args, f.Limit+1)
	var rows []PosDeviceRow
	err := tx.Raw(q, args...).Scan(&rows).Error
	return rows, err
}

// TouchDevice memajukan last_seen_at (monoton).
func (PosRepository) TouchDevice(tx *gorm.DB, id uuid.UUID, now time.Time) error {
	return tx.Exec(`
		UPDATE pos_devices
		   SET last_seen_at = GREATEST(COALESCE(last_seen_at, CAST(@now AS timestamptz)), CAST(@now AS timestamptz))
		 WHERE id = @id`, map[string]any{"now": now, "id": id}).Error
}

// ------------------------------------------------------------------ lokasi & heartbeat

// PosLocationRow adalah lokasi tempat device dipasang.
type PosLocationRow struct {
	ID              uuid.UUID
	Code            string
	Name            string
	TypeCode        string
	Timezone        string
	SyncState       string
	LastHeartbeatAt *time.Time
	ArchivedAt      *time.Time
}

// LocationByID membaca lokasi (nil kalau tidak ada).
func (PosRepository) LocationByID(tx *gorm.DB, id uuid.UUID) (*PosLocationRow, error) {
	var rows []PosLocationRow
	err := tx.Raw(`SELECT id, code, name, type_code, timezone, sync_state, last_heartbeat_at, archived_at
	                 FROM locations WHERE id = ?`, id).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// MarkLocationTracked: lokasi yang baru mendapat device POS pertama berubah
// NOT_TRACKED → OFFLINE (ATP online 0 sampai heartbeat pertama).
func (PosRepository) MarkLocationTracked(tx *gorm.DB, id uuid.UUID, now time.Time) error {
	return tx.Exec(`UPDATE locations SET sync_state = 'OFFLINE', updated_at = ? WHERE id = ? AND sync_state = 'NOT_TRACKED'`,
		now, id).Error
}

// MarkLocationOnline mencatat heartbeat: sync_state ONLINE dan
// last_heartbeat_at maju secara monoton (heartbeat yang datang terlambat
// tidak memundurkan waktu).
func (PosRepository) MarkLocationOnline(tx *gorm.DB, id uuid.UUID, now time.Time) error {
	return tx.Exec(`
		UPDATE locations
		   SET sync_state = 'ONLINE',
		       last_heartbeat_at = GREATEST(COALESCE(last_heartbeat_at, CAST(@now AS timestamptz)), CAST(@now AS timestamptz)),
		       updated_at = CAST(@now AS timestamptz)
		 WHERE id = @id`, map[string]any{"now": now, "id": id}).Error
}

// PosStaleLocation adalah lokasi yang status sinkronnya diubah job.
type PosStaleLocation struct {
	ID        uuid.UUID
	SyncState string
}

// MarkOneStaleLocationOffline memproses SATU lokasi kandidat:
//   - ONLINE yang heartbeat terakhirnya sebelum cutoff, atau
//   - OFFLINE yang tidak punya device ACTIVE lagi (semua device dicabut).
//
// Lokasi yang masih punya device ACTIVE menjadi OFFLINE; tanpa device ACTIVE
// kembali NOT_TRACKED (bukan OFFLINE selamanya). Kandidat diambil dengan FOR
// UPDATE SKIP LOCKED (instance worker lain atau heartbeat yang sedang
// berjalan tidak ditunggu), lalu UPDATE bersyarat mengecek ulang kondisi pada
// versi baris terbaru. nil = tidak ada kandidat.
func (PosRepository) MarkOneStaleLocationOffline(tx *gorm.DB, cutoff, now time.Time) (*PosStaleLocation, error) {
	const stale = `((%[1]s.sync_state = 'ONLINE'
	                  AND (%[1]s.last_heartbeat_at IS NULL OR %[1]s.last_heartbeat_at < CAST(@cutoff AS timestamptz)))
	             OR (%[1]s.sync_state = 'OFFLINE'
	                  AND NOT EXISTS (SELECT 1 FROM pos_devices d WHERE d.location_id = %[1]s.id AND d.status = 'ACTIVE')))`
	var rows []PosStaleLocation
	err := tx.Raw(`
		UPDATE locations l
		   SET sync_state = CASE WHEN EXISTS (SELECT 1 FROM pos_devices d WHERE d.location_id = l.id AND d.status = 'ACTIVE')
		                         THEN 'OFFLINE' ELSE 'NOT_TRACKED' END,
		       updated_at = CAST(@now AS timestamptz)
		 WHERE l.id = (SELECT c.id FROM locations c
		                WHERE `+fmt.Sprintf(stale, "c")+`
		                ORDER BY c.last_heartbeat_at NULLS FIRST, c.id
		                LIMIT 1
		                  FOR UPDATE SKIP LOCKED)
		   AND `+fmt.Sprintf(stale, "l")+`
		RETURNING l.id, l.sync_state`, map[string]any{"now": now, "cutoff": cutoff}).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// DeviceHasGap: device punya transaksi tersimpan di atas ack, artinya ada
// seq yang belum diterima (missing_seqs tidak kosong).
func (PosRepository) DeviceHasGap(tx *gorm.DB, deviceID uuid.UUID, ack int64) (bool, error) {
	var gap bool
	err := tx.Raw(`SELECT EXISTS (SELECT 1 FROM pos_transactions WHERE device_id = ? AND device_seq > CAST(? AS bigint))`,
		deviceID, ack).Scan(&gap).Error
	return gap, err
}

// ------------------------------------------------------------------ bootstrap

// PosProductRow adalah produk aktif untuk katalog POS.
type PosProductRow struct {
	ID   uuid.UUID
	SKU  string `gorm:"column:sku"`
	GTIN *string
	Name string
	UOM  string `gorm:"column:uom"`
}

// ActiveProducts membaca katalog produk aktif (satu query).
func (PosRepository) ActiveProducts(tx *gorm.DB) ([]PosProductRow, error) {
	var rows []PosProductRow
	err := tx.Raw(`SELECT id, sku, gtin, name, uom FROM products WHERE archived_at IS NULL ORDER BY sku, id`).Scan(&rows).Error
	return rows, err
}

// PosPriceListRow adalah price list channel POS (default atau khusus lokasi).
type PosPriceListRow struct {
	ID         uuid.UUID
	Code       string
	Version    int64
	LocationID *uuid.UUID
	Currency   string
	ValidFrom  time.Time
	ValidTo    *time.Time
}

// POSPriceLists membaca semua price list channel POS yang berlaku untuk
// lokasi (default + khusus lokasi), untuk dipilih per waktu di service.
func (PosRepository) POSPriceLists(tx *gorm.DB, locationID uuid.UUID) ([]PosPriceListRow, error) {
	var rows []PosPriceListRow
	err := tx.Raw(`
		SELECT id, code, version, location_id, currency, valid_from, valid_to
		  FROM price_lists
		 WHERE channel_code = 'POS' AND (location_id IS NULL OR location_id = ?)
		 ORDER BY version DESC, id`, locationID).Scan(&rows).Error
	return rows, err
}

// PosPriceRow adalah harga satu produk.
type PosPriceRow struct {
	ProductID uuid.UUID
	Price     int64
}

// PriceListItems membaca isi satu price list.
func (PosRepository) PriceListItems(tx *gorm.DB, listID uuid.UUID) ([]PosPriceRow, error) {
	var rows []PosPriceRow
	err := tx.Raw(`SELECT product_id, price FROM price_list_items WHERE price_list_id = ? ORDER BY product_id`, listID).Scan(&rows).Error
	return rows, err
}

// PosStockRow adalah saldo satu produk di lokasi.
type PosStockRow struct {
	ProductID     uuid.UUID
	PhysicalQty   int64
	ReservedQty   int64
	QuarantineQty int64
	AvailableQty  int64
}

// LocationStock membaca snapshot saldo lokasi.
func (PosRepository) LocationStock(tx *gorm.DB, locationID uuid.UUID) ([]PosStockRow, error) {
	var rows []PosStockRow
	err := tx.Raw(`
		SELECT product_id, physical_qty, reserved_qty, quarantine_qty, available_qty
		  FROM inventory_balances WHERE location_id = ? ORDER BY product_id`, locationID).Scan(&rows).Error
	return rows, err
}

// PosSerialRow adalah serial UNIT yang boleh dijual di lokasi.
type PosSerialRow struct {
	Code      string
	ProductID uuid.UUID
}

// InStockSerials membaca serial UNIT IN_STOCK di lokasi, keyset per kode
// (index serial_units_pos_bootstrap). Maksimal limit+1 baris.
func (PosRepository) InStockSerials(tx *gorm.DB, locationID uuid.UUID, afterCode string, limit int) ([]PosSerialRow, error) {
	var rows []PosSerialRow
	err := tx.Raw(`
		SELECT code, product_id FROM serial_units
		 WHERE location_id = ? AND level_code = 'UNIT' AND status = 'IN_STOCK' AND code > ?
		 ORDER BY code
		 LIMIT ?`, locationID, afterCode, limit+1).Scan(&rows).Error
	return rows, err
}

// PosCashierRow adalah kasir yang boleh login di POS lokasi itu.
type PosCashierRow struct {
	ID         uuid.UUID
	Name       string
	PosPinHash string
}

// CashiersAt: user aktif yang punya pos.sync lewat role ber-scope ALL atau
// SELECTED yang memuat lokasi (permission dan scope dicek pada role yang
// sama, seperti AuthRepository.CashierAllowedAtLocation), dan sudah punya
// PIN POS. Satu query.
func (PosRepository) CashiersAt(tx *gorm.DB, locationID uuid.UUID) ([]PosCashierRow, error) {
	var rows []PosCashierRow
	err := tx.Raw(`
		SELECT u.id, u.name, u.pos_pin_hash
		  FROM users u
		 WHERE u.status = 'ACTIVE' AND u.archived_at IS NULL AND u.pos_pin_hash IS NOT NULL
		   AND EXISTS (
		     SELECT 1 FROM user_roles ur
		       JOIN role_permissions rp ON rp.role_code = ur.role_code AND rp.permission_code = 'pos.sync'
		      WHERE ur.user_id = u.id
		        AND (ur.scope_mode = 'ALL'
		             OR (ur.scope_mode = 'SELECTED' AND EXISTS (
		                   SELECT 1 FROM user_role_locations url
		                    WHERE url.user_role_id = ur.id AND url.location_id = ?))))
		 ORDER BY u.name, u.id`, locationID).Scan(&rows).Error
	return rows, err
}

// ------------------------------------------------------------------ sync

// PosCashierState: keadaan kasir yang disebut transaksi POS.
type PosCashierState struct {
	// Active: user berstatus ACTIVE dan tidak diarsipkan (saat sync).
	Active bool
	// HasRole: user punya role yang memberi pos.sync ber-scope ALL atau
	// SELECTED yang memuat lokasi (role masih terpasang, apa pun status user).
	HasRole bool
}

// CashierStatus mengembalikan keadaan setiap id yang ADA di users (satu
// query per batch, tanpa N+1). id yang tidak ada di users tidak muncul di map.
func (PosRepository) CashierStatus(tx *gorm.DB, ids []uuid.UUID, locationID uuid.UUID) (map[uuid.UUID]PosCashierState, error) {
	out := make(map[uuid.UUID]PosCashierState, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID      uuid.UUID
		Active  bool
		HasRole bool
	}
	err := tx.Raw(`
		SELECT u.id,
		       (u.status = 'ACTIVE' AND u.archived_at IS NULL) AS active,
		       EXISTS (
		          SELECT 1 FROM user_roles ur
		            JOIN role_permissions rp ON rp.role_code = ur.role_code AND rp.permission_code = 'pos.sync'
		           WHERE ur.user_id = u.id
		             AND (ur.scope_mode = 'ALL'
		                  OR (ur.scope_mode = 'SELECTED' AND EXISTS (
		                        SELECT 1 FROM user_role_locations url
		                         WHERE url.user_role_id = ur.id AND url.location_id = ?)))) AS has_role
		  FROM users u
		 WHERE u.id = ANY(CAST(? AS uuid[]))`, locationID, SerialUUIDArray(ids)).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = PosCashierState{Active: r.Active, HasRole: r.HasRole}
	}
	return out, nil
}

// PriceItemsByList membaca isi beberapa price list sekaligus:
// list → produk → harga (satu query per batch).
func (PosRepository) PriceItemsByList(tx *gorm.DB, listIDs []uuid.UUID) (map[uuid.UUID]map[uuid.UUID]int64, error) {
	out := make(map[uuid.UUID]map[uuid.UUID]int64, len(listIDs))
	if len(listIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		PriceListID uuid.UUID
		ProductID   uuid.UUID
		Price       int64
	}
	err := tx.Raw(`SELECT price_list_id, product_id, price FROM price_list_items WHERE price_list_id = ANY(CAST(? AS uuid[]))`,
		SerialUUIDArray(listIDs)).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if out[r.PriceListID] == nil {
			out[r.PriceListID] = map[uuid.UUID]int64{}
		}
		out[r.PriceListID][r.ProductID] = r.Price
	}
	return out, nil
}

// ProductsExisting mengembalikan id produk yang ada di katalog (termasuk
// yang diarsipkan: penjualan fakta produk lama tetap dicatat).
func (PosRepository) ProductsExisting(tx *gorm.DB, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var found []uuid.UUID
	if err := tx.Raw(`SELECT id FROM products WHERE id = ANY(CAST(? AS uuid[]))`, SerialUUIDArray(ids)).Scan(&found).Error; err != nil {
		return nil, err
	}
	for _, id := range found {
		out[id] = true
	}
	return out, nil
}

// PosTxnInsert adalah baris pos_transactions yang ditulis lebih dulu
// (insert-first idempotency). Hasil sementara REJECTED, diganti FinishTxn di
// transaksi DB yang sama.
type PosTxnInsert struct {
	ID          uuid.UUID
	DeviceID    uuid.UUID
	ClientTxnID uuid.UUID
	DeviceSeq   int64
	LocationID  uuid.UUID
	CashierID   *uuid.UUID
	OccurredAt  time.Time
	ReceivedAt  time.Time
	ClockSkewMs *int64
	PayloadHash []byte
}

// InsertTxnIfAbsent: INSERT ... ON CONFLICT DO NOTHING (tanpa target, jadi
// pos_txn_once dan pos_seq_once sama-sama ditangani). Insert bersamaan
// dengan kunci yang sama tertahan di unique index sampai transaksi pertama
// selesai. inserted=false = sudah ada (client_txn_id atau device_seq).
func (PosRepository) InsertTxnIfAbsent(tx *gorm.DB, r PosTxnInsert) (bool, error) {
	res := tx.Exec(`
		INSERT INTO pos_transactions (id, device_id, client_txn_id, device_seq, location_id, cashier_id,
		       occurred_at, received_at, clock_skew_ms, payload_hash, result, result_detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?::bytea, 'REJECTED', '{}')
		ON CONFLICT DO NOTHING`,
		r.ID, r.DeviceID, r.ClientTxnID, r.DeviceSeq, r.LocationID, r.CashierID,
		r.OccurredAt, r.ReceivedAt, r.ClockSkewMs, r.PayloadHash)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// PosStoredTxn adalah transaksi yang sudah tersimpan (untuk replay DUPLICATE).
type PosStoredTxn struct {
	ID          uuid.UUID
	DeviceSeq   int64
	Result      string
	Detail      string
	PayloadHash []byte
}

// TxnByClientID membaca transaksi (device, client_txn_id). nil kalau tidak ada.
// result_detail dibaca sebagai teks jsonb, sama persis dengan yang
// dikembalikan FinishTxn saat pertama kali diproses.
func (PosRepository) TxnByClientID(tx *gorm.DB, deviceID, clientTxnID uuid.UUID) (*PosStoredTxn, error) {
	var rows []PosStoredTxn
	err := tx.Raw(`
		SELECT id, device_seq, result, result_detail::text AS detail, payload_hash
		  FROM pos_transactions WHERE device_id = ? AND client_txn_id = ?`, deviceID, clientTxnID).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// FinishTxn menulis hasil akhir dan mengembalikan result_detail sebagai teks
// jsonb (bentuk yang sama dengan pembacaan ulang untuk DUPLICATE).
func (PosRepository) FinishTxn(tx *gorm.DB, id uuid.UUID, result string, detail []byte, orderID *uuid.UUID) (string, error) {
	var out []string
	err := tx.Raw(`
		UPDATE pos_transactions SET result = ?, result_detail = ?::jsonb, order_id = ?
		 WHERE id = ?
		RETURNING result_detail::text`, result, string(detail), orderID, id).Scan(&out).Error
	if err != nil {
		return "", err
	}
	if len(out) != 1 {
		return "", errors.New("transaksi POS hilang saat menulis hasil")
	}
	return out[0], nil
}

// PosSerialState adalah keadaan serial saat dikunci jalur sync.
type PosSerialState struct {
	ID         uuid.UUID
	Code       string
	LevelCode  string
	ProductID  *uuid.UUID
	Status     string
	LocationID *uuid.UUID
}

// LockSerialsByCodes mengunci serial (FOR UPDATE, urut id: mode dan urutan
// sama dengan SerialRepository.LockByCodes) supaya klasifikasi dan
// SellSerials melihat keadaan yang sama.
func (PosRepository) LockSerialsByCodes(tx *gorm.DB, codes []string) ([]PosSerialState, error) {
	var rows []PosSerialState
	if len(codes) == 0 {
		return rows, nil
	}
	err := tx.Raw(`
		SELECT id, code, level_code, product_id, status, location_id
		  FROM serial_units WHERE code = ANY(CAST(? AS text[])) ORDER BY id FOR UPDATE`,
		model.StringArray(codes)).Scan(&rows).Error
	return rows, err
}

// InsertLineSerials mencatat serial yang terjual di order POS dalam SATU
// statement (lineIDs[i] berpasangan dengan serialIDs[i]). Serial yang pernah
// tercatat terjual di order lain (dijual ulang setelah retur) dilewati, bukan
// menggagalkan penjualan fakta.
func (PosRepository) InsertLineSerials(tx *gorm.DB, lineIDs, serialIDs []uuid.UUID, at time.Time) error {
	if len(lineIDs) == 0 {
		return nil
	}
	if len(lineIDs) != len(serialIDs) {
		return errors.New("jumlah line dan serial tidak sama")
	}
	return tx.Exec(`
		INSERT INTO order_line_serials (order_line_id, serial_unit_id, assigned_at)
		SELECT x.l, x.s, CAST(? AS timestamptz)
		  FROM unnest(CAST(? AS uuid[]), CAST(? AS uuid[])) AS x(l, s)
		ON CONFLICT DO NOTHING`, at, SerialUUIDArray(lineIDs), SerialUUIDArray(serialIDs)).Error
}

// posAckWindow membatasi rentang seq yang dipindai per langkah AdvanceAck
// (index pos_seq_once), dan posAckMaxSteps jumlah langkah per panggilan;
// sisa rentang dilanjutkan sync berikutnya.
const (
	posAckWindow   = 10_000
	posAckMaxSteps = 20
)

// AdvanceAck memajukan pos_devices.last_acked_seq ke seq tertinggi yang
// semua seq di bawahnya (1..N) sudah tersimpan, lalu mengembalikannya.
// Monoton (GREATEST), aman untuk sync bersamaan dari device yang sama. Setiap
// langkah hanya memindai seq dalam (ack, ack+posAckWindow], bukan semua
// transaksi di atas ack.
func (PosRepository) AdvanceAck(tx *gorm.DB, deviceID uuid.UUID) (int64, error) {
	var last []int64
	if err := tx.Raw(`SELECT last_acked_seq FROM pos_devices WHERE id = ?`, deviceID).Scan(&last).Error; err != nil {
		return 0, err
	}
	if len(last) == 0 {
		return 0, errors.New("device POS tidak ditemukan")
	}
	ack := last[0]
	for range posAckMaxSteps {
		var next []int64
		err := tx.Raw(`SELECT device_seq FROM pos_transactions WHERE device_id = ? AND device_seq = CAST(? AS bigint) + 1`,
			deviceID, ack).Scan(&next).Error
		if err != nil {
			return 0, err
		}
		if len(next) == 0 {
			break
		}
		// Ujung pulau seq berurutan yang dimulai di ack+1: seq terkecil di
		// jendela yang tidak punya penerus. Tidak ada di jendela = seluruh
		// jendela terisi; lanjut dari ujung jendela.
		var end struct{ EndSeq *int64 }
		err = tx.Raw(`
			SELECT min(t.device_seq) AS end_seq FROM pos_transactions t
			 WHERE t.device_id = ? AND t.device_seq > CAST(? AS bigint)
			   AND t.device_seq <= CAST(? AS bigint) + CAST(? AS bigint)
			   AND NOT EXISTS (SELECT 1 FROM pos_transactions n
			                    WHERE n.device_id = t.device_id AND n.device_seq = t.device_seq + 1)`,
			deviceID, ack, ack, int64(posAckWindow)).Scan(&end).Error
		if err != nil {
			return 0, err
		}
		if end.EndSeq != nil {
			ack = *end.EndSeq
			break
		}
		ack += posAckWindow
	}
	var out []int64
	err := tx.Raw(`
		UPDATE pos_devices SET last_acked_seq = GREATEST(last_acked_seq, CAST(? AS bigint))
		 WHERE id = ? RETURNING last_acked_seq`, ack, deviceID).Scan(&out).Error
	if err != nil || len(out) == 0 {
		return ack, err
	}
	return out[0], nil
}

// MissingSeqs mengembalikan seq yang belum tersimpan antara ack+1 dan seq
// tertinggi device, dicari dalam jendela window angka dan maksimal limit
// hasil. truncated=true kalau masih ada yang tidak dilaporkan.
func (PosRepository) MissingSeqs(tx *gorm.DB, deviceID uuid.UUID, ack int64, window int64, limit int) ([]int64, bool, error) {
	var top struct{ MaxSeq *int64 }
	if err := tx.Raw(`SELECT max(device_seq) AS max_seq FROM pos_transactions WHERE device_id = ?`, deviceID).Scan(&top).Error; err != nil {
		return nil, false, err
	}
	if top.MaxSeq == nil || *top.MaxSeq <= ack {
		return []int64{}, false, nil
	}
	maxSeq := *top.MaxSeq
	upper := min(maxSeq, ack+window)
	var seqs []int64
	err := tx.Raw(`
		SELECT g FROM generate_series(CAST(? AS bigint) + 1, CAST(? AS bigint)) AS g
		 WHERE NOT EXISTS (SELECT 1 FROM pos_transactions t WHERE t.device_id = ? AND t.device_seq = g)
		 ORDER BY g
		 LIMIT ?`, ack, upper, deviceID, limit+1).Scan(&seqs).Error
	if err != nil {
		return nil, false, err
	}
	truncated := upper < maxSeq
	if len(seqs) > limit {
		seqs, truncated = seqs[:limit], true
	}
	if seqs == nil {
		seqs = []int64{}
	}
	return seqs, truncated, nil
}

// ------------------------------------------------------------------ konflik alokasi

// ConflictState adalah konflik yang dikunci untuk diselesaikan.
type ConflictState struct {
	ID         uuid.UUID
	Kind       string
	Status     string
	LocationID uuid.UUID
	Resolution *string
	OrderID    *uuid.UUID
	// OrderStatus: status order terkait saat ini (nil kalau tanpa order).
	OrderStatus *string
}

// LockConflict mengunci satu konflik (nil kalau tidak ada) beserta status
// order terkait (order tidak dikunci; hanya dibaca untuk validasi resolusi).
func (PosRepository) LockConflict(tx *gorm.DB, id uuid.UUID) (*ConflictState, error) {
	var rows []ConflictState
	err := tx.Raw(`
		SELECT c.id, c.kind, c.status, c.location_id, c.resolution, c.order_id, o.status AS order_status
		  FROM allocation_conflicts c
		  LEFT JOIN orders o ON o.id = c.order_id
		 WHERE c.id = ?
		   FOR NO KEY UPDATE OF c`, id).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// ResolveConflict menutup konflik OPEN (transisi bersyarat). note ikut
// digabung ke details. changed=false kalau status sudah bukan OPEN.
func (PosRepository) ResolveConflict(tx *gorm.DB, id uuid.UUID, resolution string, detailsPatch []byte,
	actor *uuid.UUID, now time.Time) (bool, error) {
	res := tx.Exec(`
		UPDATE allocation_conflicts
		   SET status = 'RESOLVED', resolution = ?, resolved_by = ?, resolved_at = ?, updated_at = ?,
		       details = details || ?::jsonb
		 WHERE id = ? AND status = 'OPEN'`, resolution, actor, now, now, string(detailsPatch), id)
	return res.RowsAffected == 1, res.Error
}

// ConflictView membaca satu konflik dalam bentuk read model Command Center.
func (PosRepository) ConflictView(tx *gorm.DB, id uuid.UUID) (*CCConflictRow, error) {
	var rows []CCConflictRow
	err := tx.Raw(`
		SELECT c.id, c.kind, c.status, c.location_id, l.code AS location_code, l.name AS location_name,
		       c.product_id, p.sku, p.name AS product_name, c.qty, c.pos_transaction_id, c.reservation_id,
		       c.order_id, c.serial_unit_id, c.resolution, c.details::text AS details,
		       c.resolved_by, c.resolved_at, c.created_at
		  FROM allocation_conflicts c
		  JOIN locations l ON l.id = c.location_id
		  JOIN products p  ON p.id = c.product_id
		 WHERE c.id = ?`, id).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}
