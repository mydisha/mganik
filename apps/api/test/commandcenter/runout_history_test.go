package commandcenter_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/dto"
)

// receiptAt mencatat satu RECEIPT pada waktu at.
func (e *env) receiptAt(loc, prod uuid.UUID, qty int64, at time.Time) {
	e.t.Helper()
	e.exec(`INSERT INTO inventory_movements (id, location_id, product_id, movement_type, physical_delta,
	                                          ref_type, ref_id, idempotency_key, actor_type, occurred_at)
	        VALUES (gen_random_uuid(), ?, ?, 'RECEIPT', ?, 'TEST', gen_random_uuid(), 'test:' || gen_random_uuid(), 'SYSTEM', ?)`,
		loc, prod, qty, at)
}

// TestRunout_ShortHistoryAndRounding:
//   - SKU baru 2 hari, laku 20/hari, stok 30 → rata-rata dibagi 2 hari
//     (DOI 1,5 → CRITICAL), bukan 28 hari (DOI 21 → OK), plus SHORT_HISTORY;
//   - SKU lama (RECEIPT 40 hari lalu) yang baru laku 2 hari → pembagi 28;
//   - kasus batas pembulatan: available 107, terjual 1000/28 hari, lead 3 →
//     DOI 2,996 dibulatkan 3,00; level dihitung dari 3,00 (WARNING), sama
//     dengan angka yang ditampilkan.
func TestRunout_ShortHistoryAndRounding(t *testing.T) {
	e := newEnv(t)
	loc := e.location("TKO-RIWAYAT", "OWN_STORE", nil, ptr("31.71"))

	baru := e.product("N-BARU")
	e.balance(loc, baru, 30, 0)
	e.policy(loc, baru, 1, 3, 2)
	e.dailySales(loc, baru, "DIRECT_SALE", 20, 2)

	lama := e.product("O-LAMA")
	e.balance(loc, lama, 30, 0)
	e.policy(loc, lama, 1, 3, 2)
	e.receiptAt(loc, lama, 70, testNow.AddDate(0, 0, -40))
	e.dailySales(loc, lama, "DIRECT_SALE", 20, 2)

	batas := e.product("R-BATAS")
	e.balance(loc, batas, 107, 0)
	e.policy(loc, batas, 1, 3, 2)
	e.saleAt(loc, batas, 1000, testNow.Add(-28*24*time.Hour+time.Hour))

	enam := e.product("S-ENAM")
	e.balance(loc, enam, 100, 0)
	e.policy(loc, enam, 1, 3, 2)
	e.dailySales(loc, enam, "DIRECT_SALE", 1, 7) // 7 hari: tidak lagi SHORT_HISTORY

	var got dto.CCRunoutResponse
	getJSON(t, e.engine(opsPrincipal()), "/api/v1/cc/runout?location_id="+loc.String(), &got)
	by := map[string]dto.CCRunoutItem{}
	for _, it := range got.Items {
		by[it.SKU] = it
	}
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		sku     string
		level   string
		history int
		avg     *float64
		doi     *float64
		reasons []string
	}{
		{"N-BARU", "CRITICAL", 2, f(20), f(1.5), []string{"BELOW_LEAD_TIME", "SHORT_HISTORY"}},
		{"O-LAMA", "OK", 28, f(1.4286), f(21), []string{}},
		{"R-BATAS", "WARNING", 28, f(35.7143), f(3), []string{"BELOW_SAFETY_DAYS"}},
		{"S-ENAM", "OK", 7, f(1), f(100), []string{}},
	}
	for _, c := range cases {
		it, ok := by[c.sku]
		if !ok {
			t.Fatalf("%s tidak ada", c.sku)
		}
		if it.Level != c.level || it.HistoryDays == nil || *it.HistoryDays != c.history ||
			!eqFloat(it.AvgDailySales, c.avg) || !eqFloat(it.DaysOfInventory, c.doi) || !slices.Equal(it.Reasons, c.reasons) {
			t.Errorf("%s: level=%s history=%v avg=%v doi=%v reasons=%v; ingin %+v",
				c.sku, it.Level, derefInt(it.HistoryDays), deref(it.AvgDailySales), deref(it.DaysOfInventory), it.Reasons, c)
		}
	}
	// Level konsisten dengan angka yang ditampilkan: CRITICAL ⇔ DOI < lead.
	for _, it := range got.Items {
		if it.DaysOfInventory == nil || it.LeadTimeDays == nil {
			continue
		}
		if (*it.DaysOfInventory < float64(*it.LeadTimeDays)) != (it.Level == "CRITICAL") && it.AvailableQty > 0 {
			t.Errorf("%s: DOI tampil %v dan lead %d tidak cocok dengan level %s", it.SKU, *it.DaysOfInventory, *it.LeadTimeDays, it.Level)
		}
	}
	// Tanpa penjualan: history_days null.
	var all dto.CCRunoutResponse
	getJSON(t, e.engine(opsPrincipal()), "/api/v1/cc/runout?level=INSUFFICIENT_DATA", &all)
	for _, it := range all.Items {
		if it.HistoryDays != nil {
			t.Fatalf("%s tanpa penjualan tapi history_days %d", it.SKU, *it.HistoryDays)
		}
	}
}

func derefInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
