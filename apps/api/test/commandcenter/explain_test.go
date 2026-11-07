package commandcenter_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"mganik/api/internal/repository"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

// TestExplain mencetak EXPLAIN (ANALYZE, BUFFERS) query runout dan indeks
// kebocoran pada data bervolume. Ini alat ukur, bukan gate: rencana query
// bergantung statistik, jadi tidak di-assert di CI. Jalankan:
//
//	CC_EXPLAIN=1 go test -run TestExplain -v ./test/commandcenter/
func TestExplain(t *testing.T) {
	if os.Getenv("CC_EXPLAIN") != "1" {
		t.Skip("set CC_EXPLAIN=1 untuk mengukur rencana query")
	}
	e := newEnv(t)

	// Runout: 40 toko × 25 SKU, 60 hari penjualan (separuh di luar jendela)
	// + penerimaan dan reservasi sebagai noise ledger.
	e.exec(`INSERT INTO locations (id, code, name, type_code, timezone)
	        SELECT gen_random_uuid(), 'BULK-' || g, 'Toko bulk ' || g, 'OWN_STORE', 'Asia/Jakarta' FROM generate_series(1, 40) g`)
	e.exec(`INSERT INTO products (id, brand_id, sku, name)
	        SELECT gen_random_uuid(), ?, 'BULK-SKU-' || g, 'Produk bulk ' || g FROM generate_series(1, 25) g`, brandID)
	e.exec(`INSERT INTO inventory_balances (location_id, product_id, physical_qty)
	        SELECT l.id, p.id, (abs(hashtext(l.code || p.sku)) % 80)
	          FROM locations l CROSS JOIN products p WHERE l.code LIKE 'BULK-%' AND p.sku LIKE 'BULK-SKU-%'`)
	e.exec(`INSERT INTO stock_policies (location_id, product_id, min_qty, lead_time_days, safety_days)
	        SELECT b.location_id, b.product_id, 5, 3, 2 FROM inventory_balances b
	          JOIN locations l ON l.id = b.location_id WHERE l.code LIKE 'BULK-%'`)
	e.exec(`INSERT INTO inventory_movements (id, location_id, product_id, movement_type, physical_delta, reserved_delta,
	                                          ref_type, ref_id, idempotency_key, actor_type, occurred_at)
	        SELECT gen_random_uuid(), b.location_id, b.product_id, m.t, m.p, m.r, 'BULK', gen_random_uuid(),
	               'bulk:' || gen_random_uuid(), 'SYSTEM', CAST(? AS timestamptz) - make_interval(days => d)
	          FROM inventory_balances b
	          JOIN locations l ON l.id = b.location_id AND l.code LIKE 'BULK-%'
	          CROSS JOIN generate_series(1, 60) d
	          CROSS JOIN (VALUES ('DIRECT_SALE', -1, 0), ('RECEIPT', 2, 0), ('RESERVE', 0, 1)) AS m (t, p, r)`, testNow)

	// Kebocoran: 6 distributor × 500 unit, 100 ribu scan dalam 90 hari.
	for i := range 6 {
		d := e.distributor("Distributor bulk "+string(rune('A'+i)), "SELECTED", "35.78")
		e.exec(`INSERT INTO serial_units (id, code, level_code, product_id, batch_id, status, custodian_party_id, commissioned_at)
		        SELECT gen_random_uuid(), lpad((? * 1000 + g)::text, 16, '7'), 'UNIT', ?, ?, 'AT_PARTNER', ?, ?
		          FROM generate_series(1, 500) g`, i+1, sku1ID, seed.BatchID(), d, testNow.AddDate(0, -6, 0))
	}
	// Riwayat custodian: 7 event per unit (produksi s.d. diterima distributor),
	// event terakhir ber-party = distributor saat ini.
	e.exec(`INSERT INTO unit_events (id, serial_unit_id, biz_step, party_id, actor_type, occurred_at)
	        SELECT gen_random_uuid(), su.id, 'step_' || k,
	               CASE WHEN k = 7 THEN su.custodian_party_id ELSE ? END, 'SYSTEM',
	               CAST(? AS timestamptz) - make_interval(days => 200 - k * 10)
	          FROM serial_units su CROSS JOIN generate_series(1, 7) k
	         WHERE su.status = 'AT_PARTNER'`, seed.ID(seed.CompanyPartyKey), testNow)
	e.exec(`INSERT INTO scan_events (id, raw_code, serial_unit_id, context, geo_source, lat, lng, region_code, result, occurred_at)
	        SELECT gen_random_uuid(), 'KODE', su.id, 'CONSUMER_VERIFY', 'GPS', -7.2, 112.7,
	               CASE WHEN g % 20 = 0 THEN '31.74' ELSE '35.78' END, 'GENUINE_REPEAT',
	               CAST(? AS timestamptz) - make_interval(mins => abs(hashtext(su.code || '-' || g) % (90 * 24 * 60)))
	          FROM serial_units su CROSS JOIN generate_series(1, 34) g
	         WHERE su.status = 'AT_PARTNER'`, testNow)
	// Antrean anomali: 50 ribu baris, 1/5 OPEN.
	e.exec(`INSERT INTO anomalies (id, rule_code, severity, status, subject_type, dedupe_key, first_seen_at, last_seen_at, created_at)
	        SELECT gen_random_uuid(), 'OUT_OF_TERRITORY', 'MEDIUM', CASE WHEN g % 5 = 0 THEN 'OPEN' ELSE 'RESOLVED' END,
	               'PARTY', 'bulk:' || g, t, t, t
	          FROM generate_series(1, 50000) g, LATERAL (SELECT CAST(? AS timestamptz) - make_interval(mins => g) AS t) x`, testNow)
	e.exec(`UPDATE anomalies SET resolved_at = created_at WHERE status = 'RESOLVED'`)
	if os.Getenv("CC_EXPLAIN_DROP") != "" {
		e.exec(`DROP INDEX IF EXISTS ` + os.Getenv("CC_EXPLAIN_DROP"))
	}
	e.exec(`VACUUM (ANALYZE) inventory_movements`)
	e.exec(`VACUUM (ANALYZE) scan_events`)
	e.exec(`VACUUM (ANALYZE) unit_events`)
	e.exec(`ANALYZE`)
	t.Logf("baris: inventory_movements=%d scan_events=%d inventory_balances=%d",
		e.count(`SELECT count(*) FROM inventory_movements`), e.count(`SELECT count(*) FROM scan_events`),
		e.count(`SELECT count(*) FROM inventory_balances`))

	explain := func(name, sql string, args []any) {
		var plan []string
		if err := e.db.Raw("EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) "+sql, args...).Scan(&plan).Error; err != nil {
			t.Fatal(err)
		}
		t.Logf("EXPLAIN %s:\n%s", name, strings.Join(plan, "\n"))
	}

	runoutSQL, runoutArgs := repository.RunoutSQL(repository.CCRunoutFilter{
		Now: testNow, WindowDays: service.RunoutWindowDays, Scope: repository.CCScope{All: true}, Limit: 50,
	})
	explain("runout (scope ALL, halaman pertama)", runoutSQL, runoutArgs)

	leakSQL, leakArgs := repository.LeakageSQL(repository.CCLeakageFilter{
		From: testNow.Add(-30 * 24 * time.Hour), To: testNow, Scope: repository.CCScope{All: true},
		Params: repository.LeakageParams{MinScans: 20, BaselineRatio: 0.05, TestBuyImmediate: true}, Limit: 50,
	})
	explain("kebocoran wilayah (30 hari)", leakSQL, leakArgs)

	anomSQL := `SELECT id FROM anomalies an WHERE an.status = 'OPEN' AND (an.created_at, an.id) < (?, CAST(? AS uuid))
	            ORDER BY an.created_at DESC, an.id DESC LIMIT 51`
	explain("anomali keyset (status OPEN, halaman berikutnya)", anomSQL, []any{testNow.Add(-5 * 24 * time.Hour), newID()})
}
