package repository

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// OrderRepository mengakses order, line, reservasi, riwayat status, dan pembayaran.
//
// Urutan lock yang dipakai seluruh service (anti deadlock):
// order → reservasi (urut lokasi, produk, id) → saldo (urut lokasi, produk).
// Semua lock memakai FOR NO KEY UPDATE supaya tidak bentrok dengan pengecekan
// FK (KEY SHARE) dari insert baris anak.
type OrderRepository struct{}

// InsertOrder menulis order baru.
func (OrderRepository) InsertOrder(tx *gorm.DB, o *model.Order) error {
	return tx.Create(o).Error
}

// InsertOrderIfAbsent menulis order kecuali (channel_code, external_ref) sudah
// ada. inserted=false berarti duplikat; insert bersamaan dengan ref yang sama
// tertahan di unique index sampai transaksi pertama commit.
func (OrderRepository) InsertOrderIfAbsent(tx *gorm.DB, o *model.Order) (bool, error) {
	res := tx.Exec(`
		INSERT INTO orders (id, channel_code, external_ref, consumer_id, fulfillment_location_id, status, currency,
		       subtotal, discount, total, placed_at, paid_at, payment_expires_at, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT ON CONSTRAINT order_external_ref_once DO NOTHING`,
		o.ID, o.ChannelCode, o.ExternalRef, o.ConsumerID, o.FulfillmentLocationID, o.Status, o.Currency,
		o.Subtotal, o.Discount, o.Total, o.PlacedAt, o.PaidAt, o.PaymentExpiresAt, orderJSONOrEmpty(o.Metadata),
		o.CreatedAt, o.UpdatedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func orderJSONOrEmpty(j model.JSON) model.JSON {
	if j == nil {
		return model.JSON(`{}`)
	}
	return j
}

// InsertLines menulis line order dalam satu statement.
func (OrderRepository) InsertLines(tx *gorm.DB, lines []model.OrderLine) error {
	if len(lines) == 0 {
		return nil
	}
	return tx.Create(&lines).Error
}

// OrderByID membaca order tanpa lock (nil kalau tidak ada).
func (OrderRepository) OrderByID(tx *gorm.DB, id uuid.UUID) (*model.Order, error) {
	var o model.Order
	err := tx.Where("id = ?", id).Take(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// LockOrder mengunci order (FOR NO KEY UPDATE). nil kalau tidak ada.
func (OrderRepository) LockOrder(tx *gorm.DB, id uuid.UUID) (*model.Order, error) {
	var rows []model.Order
	if err := tx.Raw(`SELECT * FROM orders WHERE id = ? FOR NO KEY UPDATE`, id).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// OrderByExternalRef membaca order hasil impor channel (nil kalau tidak ada).
func (OrderRepository) OrderByExternalRef(tx *gorm.DB, channel, ref string) (*model.Order, error) {
	var o model.Order
	err := tx.Where("channel_code = ? AND external_ref = ?", channel, ref).Take(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// LineRow adalah line order beserta identitas produk.
type LineRow struct {
	model.OrderLine
	SKU         string `gorm:"column:sku"`
	ProductName string
}

func (OrderRepository) Lines(tx *gorm.DB, orderID uuid.UUID) ([]LineRow, error) {
	var rows []LineRow
	err := tx.Raw(`
		SELECT ol.*, p.sku, p.name AS product_name
		  FROM order_lines ol JOIN products p ON p.id = ol.product_id
		 WHERE ol.order_id = ?
		 ORDER BY ol.created_at, ol.id`, orderID).Scan(&rows).Error
	return rows, err
}

func (OrderRepository) Reservations(tx *gorm.DB, orderID uuid.UUID) ([]model.StockReservation, error) {
	var rows []model.StockReservation
	err := tx.Where("order_id = ?", orderID).Order("created_at, id").Find(&rows).Error
	return rows, err
}

// LockActiveReservations mengunci reservasi ACTIVE milik order, urut
// (lokasi, produk, id) supaya urutan lock konsisten antar transaksi.
func (OrderRepository) LockActiveReservations(tx *gorm.DB, orderID uuid.UUID) ([]model.StockReservation, error) {
	var rows []model.StockReservation
	err := tx.Raw(`
		SELECT * FROM stock_reservations
		 WHERE order_id = ? AND status = 'ACTIVE'
		 ORDER BY location_id, product_id, id
		   FOR NO KEY UPDATE`, orderID).Scan(&rows).Error
	return rows, err
}

func (OrderRepository) InsertReservations(tx *gorm.DB, rs []model.StockReservation) error {
	if len(rs) == 0 {
		return nil
	}
	return tx.Create(&rs).Error
}

// CloseReservation menutup reservasi ACTIVE. closed=false kalau reservasi
// sudah tidak ACTIVE (diproses transaksi lain).
func (OrderRepository) CloseReservation(tx *gorm.DB, id uuid.UUID, status, reason string, now time.Time) (bool, error) {
	res := tx.Exec(`
		UPDATE stock_reservations SET status = ?, closed_at = ?, close_reason = ?
		 WHERE id = ? AND status = 'ACTIVE'`, status, now, reason, id)
	return res.RowsAffected == 1, res.Error
}

// ClearReservationExpiry menghapus batas waktu reservasi ACTIVE order yang
// sudah dibayar (tidak lagi disapu sweeper).
func (OrderRepository) ClearReservationExpiry(tx *gorm.DB, orderID uuid.UUID) error {
	return tx.Exec(`UPDATE stock_reservations SET expires_at = NULL WHERE order_id = ? AND status = 'ACTIVE'`, orderID).Error
}

// Transition mengubah status order secara bersyarat: hanya kalau status saat
// ini = from DAN pasangan (from, to) ada di lifecycle_transitions (state
// machine sebagai data). changed=false berarti status sudah berubah atau
// transisi tidak diizinkan.
func (OrderRepository) Transition(tx *gorm.DB, orderID uuid.UUID, from, to string, paidAt *time.Time, now time.Time) (bool, error) {
	res := tx.Exec(`
		UPDATE orders
		   SET status = @to, paid_at = COALESCE(@paid_at, paid_at), version = version + 1, updated_at = @now
		 WHERE id = @id AND status = @from
		   AND EXISTS (SELECT 1 FROM lifecycle_transitions
		                WHERE entity_type = 'ORDER' AND from_code = @from AND to_code = @to)`,
		map[string]any{"to": to, "from": from, "id": orderID, "paid_at": paidAt, "now": now})
	return res.RowsAffected == 1, res.Error
}

// InsertHistory menulis riwayat status. Untuk status awal (from nil) baris
// hanya ditulis kalau transisi awal itu ada di lifecycle_transitions.
func (OrderRepository) InsertHistory(tx *gorm.DB, h *model.OrderStatusHistory) (bool, error) {
	res := tx.Exec(`
		INSERT INTO order_status_history (id, order_id, from_status, to_status, reason, actor_type, actor_id, at)
		SELECT ?, ?, ?::text, ?::text, ?, ?, ?, ?
		 WHERE EXISTS (SELECT 1 FROM lifecycle_transitions
		                WHERE entity_type = 'ORDER' AND from_code IS NOT DISTINCT FROM ?::text AND to_code = ?::text)`,
		h.ID, h.OrderID, h.FromStatus, h.ToStatus, h.Reason, h.ActorType, h.ActorID, h.At, h.FromStatus, h.ToStatus)
	return res.RowsAffected == 1, res.Error
}

// PatchMetadata menggabungkan patch (objek JSON) ke orders.metadata.
func (OrderRepository) PatchMetadata(tx *gorm.DB, orderID uuid.UUID, patch model.JSON, now time.Time) error {
	return tx.Exec(`UPDATE orders SET metadata = metadata || ?::jsonb, updated_at = ? WHERE id = ?`,
		patch, now, orderID).Error
}

func (OrderRepository) SetFulfillmentLocation(tx *gorm.DB, orderID, locationID uuid.UUID, now time.Time) error {
	return tx.Exec(`UPDATE orders SET fulfillment_location_id = ?, updated_at = ? WHERE id = ?`,
		locationID, now, orderID).Error
}

func (OrderRepository) History(tx *gorm.DB, orderID uuid.UUID) ([]model.OrderStatusHistory, error) {
	var rows []model.OrderStatusHistory
	err := tx.Where("order_id = ?", orderID).Order("at, id").Find(&rows).Error
	return rows, err
}

func (OrderRepository) Payments(tx *gorm.DB, orderID uuid.UUID) ([]model.Payment, error) {
	var rows []model.Payment
	err := tx.Where("order_id = ?", orderID).Order("received_at, id").Find(&rows).Error
	return rows, err
}

// OrderMetadata membaca metadata order terbaru di dalam tx.
func (OrderRepository) OrderMetadata(tx *gorm.DB, orderID uuid.UUID) (model.JSON, error) {
	var rows []struct{ Metadata model.JSON }
	if err := tx.Raw(`SELECT metadata FROM orders WHERE id = ?`, orderID).Scan(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0].Metadata, nil
}

// SucceededPaymentIDs mengembalikan id semua pembayaran SUCCEEDED order (urut waktu terima).
func (OrderRepository) SucceededPaymentIDs(tx *gorm.DB, orderID uuid.UUID) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := tx.Raw(`SELECT id FROM payments WHERE order_id = ? AND status = 'SUCCEEDED' ORDER BY received_at, id`,
		orderID).Scan(&ids).Error
	return ids, err
}

// PaymentByProviderRef membaca pembayaran (nil kalau belum ada).
func (OrderRepository) PaymentByProviderRef(tx *gorm.DB, provider, ref string) (*model.Payment, error) {
	var p model.Payment
	err := tx.Where("provider = ? AND provider_ref = ?", provider, ref).Take(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// InsertPaymentIfAbsent menulis pembayaran kecuali (provider, provider_ref)
// sudah ada. inserted=false = webhook dikirim ulang.
func (OrderRepository) InsertPaymentIfAbsent(tx *gorm.DB, p *model.Payment) (bool, error) {
	res := tx.Exec(`
		INSERT INTO payments (id, order_id, provider, provider_ref, method, amount, currency, status, raw,
		       received_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT ON CONSTRAINT payment_provider_ref_once DO NOTHING`,
		p.ID, p.OrderID, p.Provider, p.ProviderRef, p.Method, p.Amount, p.Currency, p.Status,
		orderJSONOrEmpty(p.Raw), p.ReceivedAt, p.CreatedAt, p.UpdatedAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ChannelType mengembalikan channels.type ("" kalau kode tidak dikenal).
func (OrderRepository) ChannelType(tx *gorm.DB, code string) (string, error) {
	var typ string
	err := tx.Raw(`SELECT type FROM channels WHERE code = ?`, code).Scan(&typ).Error
	return typ, err
}

// ProductRow adalah identitas produk aktif.
type ProductRow struct {
	ID   uuid.UUID
	SKU  string `gorm:"column:sku"`
	Name string
}

// ProductsByID membaca produk aktif berdasarkan id.
func (OrderRepository) ProductsByID(tx *gorm.DB, ids []uuid.UUID) (map[uuid.UUID]ProductRow, error) {
	var rows []ProductRow
	if err := tx.Raw(`SELECT id, sku, name FROM products WHERE id IN ? AND archived_at IS NULL`, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]ProductRow, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// ProductsBySKU membaca produk aktif berdasarkan SKU.
func (OrderRepository) ProductsBySKU(tx *gorm.DB, skus []string) (map[string]ProductRow, error) {
	var rows []ProductRow
	if err := tx.Raw(`SELECT id, sku, name FROM products WHERE sku IN ? AND archived_at IS NULL`, skus).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]ProductRow, len(rows))
	for _, r := range rows {
		out[r.SKU] = r
	}
	return out, nil
}

// Prices mengembalikan harga default channel (price list tanpa lokasi) yang
// berlaku pada waktu at, versi tertinggi menang.
func (OrderRepository) Prices(tx *gorm.DB, channel string, productIDs []uuid.UUID, at time.Time) (map[uuid.UUID]int64, error) {
	var rows []struct {
		ProductID uuid.UUID
		Price     int64
	}
	err := tx.Raw(`
		SELECT DISTINCT ON (pli.product_id) pli.product_id, pli.price
		  FROM price_list_items pli
		  JOIN price_lists pl ON pl.id = pli.price_list_id
		 WHERE pl.channel_code = ? AND pl.location_id IS NULL
		   AND pl.valid_from <= ? AND (pl.valid_to IS NULL OR pl.valid_to > ?)
		   AND pli.product_id IN ?
		 ORDER BY pli.product_id, pl.version DESC`, channel, at, at, productIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]int64, len(rows))
	for _, r := range rows {
		out[r.ProductID] = r.Price
	}
	return out, nil
}

// LockDueOrders mengambil order PENDING_PAYMENT yang batas bayarnya lewat dan
// menguncinya dengan SKIP LOCKED: dua sweeper paralel mendapat himpunan order
// yang berbeda dan tidak saling menunggu.
func (OrderRepository) LockDueOrders(tx *gorm.DB, now time.Time, limit int) ([]model.Order, error) {
	var rows []model.Order
	err := tx.Raw(`
		SELECT * FROM orders
		 WHERE status = 'PENDING_PAYMENT' AND payment_expires_at <= ?
		 ORDER BY payment_expires_at, id
		 LIMIT ?
		   FOR NO KEY UPDATE SKIP LOCKED`, now, limit).Scan(&rows).Error
	return rows, err
}

// SerialProducts mengembalikan product_id tiap serial unit yang ada.
func (OrderRepository) SerialProducts(tx *gorm.DB, ids []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	var rows []struct {
		ID        uuid.UUID
		ProductID *uuid.UUID
	}
	if err := tx.Raw(`SELECT id, product_id FROM serial_units WHERE id IN ? AND level_code = 'UNIT'`, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]uuid.UUID, len(rows))
	for _, r := range rows {
		if r.ProductID != nil {
			out[r.ID] = *r.ProductID
		}
	}
	return out, nil
}

func (OrderRepository) InsertLineSerials(tx *gorm.DB, rows []model.OrderLineSerial) error {
	if len(rows) == 0 {
		return nil
	}
	return tx.Create(&rows).Error
}

func (OrderRepository) LineSerials(tx *gorm.DB, orderID uuid.UUID) ([]model.OrderLineSerial, error) {
	var rows []model.OrderLineSerial
	err := tx.Raw(`
		SELECT ols.* FROM order_line_serials ols
		  JOIN order_lines ol ON ol.id = ols.order_line_id
		 WHERE ol.order_id = ?
		 ORDER BY ols.order_line_id, ols.serial_unit_id`, orderID).Scan(&rows).Error
	return rows, err
}
