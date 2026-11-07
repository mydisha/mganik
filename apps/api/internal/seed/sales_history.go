package seed

import (
	"strconv"
	"time"

	"mganik/api/internal/model"
)

// SalesHistoryDays: panjang riwayat penjualan demo, sama dengan jendela DOI.
const SalesHistoryDays = 28

// salesHistory adalah pola penjualan harian demo per (lokasi, SKU). weekly[i]
// = unit terjual pada hari ke-(i+1), (i+8), ... sebelum seed dijalankan.
type salesHistory struct {
	location, sku string
	weekly        [7]int64
}

// demoSalesHistory dirancang supaya Command Center menampilkan setiap level
// runout dengan saldo demo yang ada (lihat openingBalances & stock_policies):
//
//   - Toko Jakarta Selatan SKU1: 1 unit/hari, stok 1 → DOI 1 < lead time 3 → CRITICAL (Kasus 4)
//   - Toko Jakarta Selatan SKU2: 3 unit/hari, stok 12 → DOI 4 < 3+2 → WARNING
//   - Toko Bandung SKU1: 3 unit/minggu, stok 6 → DOI 14 → OK
//   - Toko Bandung SKU2 dan gudang pusat: tanpa penjualan → INSUFFICIENT_DATA
var demoSalesHistory = []salesHistory{
	{StoreJakselCode, SKU1, [7]int64{1, 1, 0, 2, 1, 1, 1}},
	{StoreJakselCode, SKU2, [7]int64{3, 2, 4, 3, 3, 2, 4}},
	{StoreBandungCode, SKU1, [7]int64{0, 1, 0, 1, 0, 1, 0}},
}

// salesQty: unit terjual pada hari ke-day (1 = kemarin) untuk pola h.
func (h salesHistory) salesQty(day int) int64 { return h.weekly[(day-1)%7] }

// DemoSalesTotal: total unit terjual dalam riwayat demo untuk (lokasi, SKU).
func DemoSalesTotal(location, sku string) int64 {
	var total int64
	for _, h := range demoSalesHistory {
		if h.location != location || h.sku != sku {
			continue
		}
		for day := 1; day <= SalesHistoryDays; day++ {
			total += h.salesQty(day)
		}
	}
	return total
}

// salesHistory menulis riwayat DIRECT_SALE harian 28 hari terakhir di toko
// demo, relatif terhadap jam seed (Options.Clock), supaya DOI/runout punya data.
//
// Saldo akhir tidak berubah: setiap pola punya RECEIPT "stok masuk sebelum
// periode" sebesar total penjualannya, dicatat sebelum penjualan pertama.
// Jadi SUM(ledger) = saldo demo tetap (Toko Jakarta Selatan SKU1 tetap 1
// unit untuk Kasus 4), juga untuk database yang sudah di-seed versi lama:
// seed ulang menambah +N dan -N sekaligus dalam satu transaksi.
//
// ID dan idempotency key deterministik per hari-ke-n (bukan per tanggal),
// jadi seed ulang di hari lain tetap no-op (ON CONFLICT DO NOTHING). Akibatnya
// riwayat bergeser keluar jendela 28 hari seiring waktu; seed di DB baru
// untuk demo yang segar.
func (s *seeder) salesHistory() error {
	now := s.opts.Clock.Now().UTC().Truncate(time.Hour)
	reason := "Riwayat penjualan data demo"
	var moves []model.InventoryMovement
	for _, h := range demoSalesHistory {
		loc, prod := LocationID(h.location), ProductID(h.sku)
		key := h.location + ":" + h.sku
		ref := ID("sales_history:" + key)
		total := DemoSalesTotal(h.location, h.sku)
		moves = append(moves, model.InventoryMovement{
			ID: ID("movement:sales_history_receipt:" + key), LocationID: loc, ProductID: prod,
			MovementType: "RECEIPT", PhysicalDelta: total, RefType: "SEED_SALES_HISTORY", RefID: ref,
			IdempotencyKey: "seed:sales_history:receipt:" + key, Reason: ptr("Stok masuk sebelum periode riwayat penjualan demo"),
			ActorType: model.ActorSystem, OccurredAt: now.Add(-time.Duration(SalesHistoryDays+1) * 24 * time.Hour), RecordedAt: now,
		})
		for day := 1; day <= SalesHistoryDays; day++ {
			qty := h.salesQty(day)
			if qty == 0 {
				continue
			}
			dayKey := key + ":" + strconv.Itoa(day)
			moves = append(moves, model.InventoryMovement{
				ID: ID("movement:sales_history:" + dayKey), LocationID: loc, ProductID: prod,
				MovementType: "DIRECT_SALE", PhysicalDelta: -qty, RefType: "SEED_SALES_HISTORY", RefID: ref,
				IdempotencyKey: "seed:sales_history:sale:" + dayKey, Reason: ptr(reason),
				ActorType: model.ActorSystem,
				// +1 jam: tetap di dalam jendela [now-28 hari, now) walau jam seed
				// dibulatkan ke bawah.
				OccurredAt: now.Add(-time.Duration(day)*24*time.Hour + time.Hour), RecordedAt: now,
			})
		}
	}
	return insert(s, moves)
}
