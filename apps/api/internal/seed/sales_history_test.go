package seed_test

import (
	"context"
	"testing"
	"time"

	"mganik/api/internal/clock"
	"mganik/api/internal/seed"
	"mganik/api/testutil"
)

// TestSeed_SalesHistory: riwayat DIRECT_SALE 28 hari ada di jendela DOI
// relatif jam seed, saldo demo tidak berubah (Kasus 4 tetap 1 unit), ledger
// tetap sama dengan saldo, dan seed ulang di hari lain tetap no-op.
func TestSeed_SalesHistory(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 7, 9, 41, 0, 0, time.UTC)
	clk := clock.NewFake(now)
	opts := options(quietLog())
	opts.Clock = clk
	if _, err := seed.Run(ctx, db, opts); err != nil {
		t.Fatal(err)
	}

	jaksel, bandung := seed.LocationID(seed.StoreJakselCode), seed.LocationID(seed.StoreBandungCode)
	sku1, sku2 := seed.ProductID(seed.SKU1), seed.ProductID(seed.SKU2)

	// Saldo demo tetap: Toko Jakarta Selatan SKU1 = 1 unit (Kasus 4).
	for _, tc := range []struct {
		name      string
		loc, prod any
		want      int64
	}{
		{"jaksel sku1", jaksel, sku1, 1},
		{"jaksel sku2", jaksel, sku2, 12},
		{"bandung sku1", bandung, sku1, 6},
		{"bandung sku2", bandung, sku2, 8},
	} {
		bal := count(t, db, `SELECT physical_qty FROM inventory_balances WHERE location_id = ? AND product_id = ?`, tc.loc, tc.prod)
		ledger := count(t, db, `SELECT COALESCE(sum(physical_delta), 0) FROM inventory_movements WHERE location_id = ? AND product_id = ?`, tc.loc, tc.prod)
		if bal != tc.want || ledger != tc.want {
			t.Errorf("%s: saldo %d, ledger %d, ingin %d", tc.name, bal, ledger, tc.want)
		}
	}

	// Penjualan dalam jendela [now-28 hari, now) = total pola.
	since := now.Add(-28 * 24 * time.Hour)
	for _, tc := range []struct {
		code, sku string
		loc, prod any
	}{
		{seed.StoreJakselCode, seed.SKU1, jaksel, sku1},
		{seed.StoreJakselCode, seed.SKU2, jaksel, sku2},
		{seed.StoreBandungCode, seed.SKU1, bandung, sku1},
	} {
		want := seed.DemoSalesTotal(tc.code, tc.sku)
		if want == 0 {
			t.Fatalf("%s/%s tanpa pola penjualan", tc.code, tc.sku)
		}
		got := count(t, db, `SELECT COALESCE(-sum(physical_delta), 0) FROM inventory_movements
		                      WHERE movement_type = 'DIRECT_SALE' AND location_id = ? AND product_id = ?
		                        AND occurred_at >= ? AND occurred_at < ?`, tc.loc, tc.prod, since, now)
		if got != want {
			t.Errorf("%s/%s: terjual %d dalam jendela, ingin %d", tc.code, tc.sku, got, want)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM inventory_movements WHERE movement_type = 'DIRECT_SALE'
	                       AND (occurred_at < ? OR occurred_at >= ?)`, since, now); n != 0 {
		t.Fatalf("%d penjualan di luar jendela 28 hari", n)
	}
	// Toko Bandung SKU2 dan gudang pusat sengaja tanpa riwayat (INSUFFICIENT_DATA).
	if n := count(t, db, `SELECT count(*) FROM inventory_movements WHERE movement_type = 'DIRECT_SALE'
	                       AND ((location_id = ? AND product_id = ?) OR location_id = ?)`,
		bandung, sku2, seed.LocationID(seed.CentralWHCode)); n != 0 {
		t.Fatalf("ada %d penjualan yang seharusnya tidak ada", n)
	}
	// Running balance tidak pernah negatif bila ledger diurutkan menurut waktu.
	if n := count(t, db, `SELECT count(*) FROM (
	                         SELECT sum(physical_delta) OVER (PARTITION BY location_id, product_id
	                                ORDER BY occurred_at, id ROWS UNBOUNDED PRECEDING) AS running
	                           FROM inventory_movements) x WHERE running < 0`); n != 0 {
		t.Fatalf("%d titik waktu dengan saldo negatif", n)
	}

	// Seed ulang tiga hari kemudian: no-op, isi semua tabel identik.
	before := tableDigests(t, db)
	clk.Advance(72 * time.Hour)
	res, err := seed.Run(ctx, db, opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total() != 0 {
		t.Fatalf("seed ulang menulis %d baris: %v", res.Total(), res.Inserted)
	}
	requireSameDigests(t, before, tableDigests(t, db))
}
