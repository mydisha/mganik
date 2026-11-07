package postest

import (
	"net/http"
	"slices"
	"testing"

	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

// Data demo: unit terakhir SKU1 di Toko Jakarta Selatan (Kasus 4) punya
// hologram. Device demo menerimanya di bootstrap, kasir bisa menjualnya dengan
// memindai kodenya, dan stok berkurang tepat sekali walau batch dikirim ulang.
func TestPosDemo_SeededStoreUnitIsScannable(t *testing.T) {
	e := newEnv(t)
	store := seed.DemoStoreUnit()

	var b bootstrapBody
	e.device(http.MethodGet, "/api/v1/pos/bootstrap", e.token, nil).decode(t, &b)
	var codes []string
	for _, s := range b.Serials.Items {
		codes = append(codes, s.Code)
	}
	if !slices.Equal(codes, []string{store.Code}) {
		t.Fatalf("serial bootstrap toko demo %v, ingin [%s]", codes, store.Code)
	}
	if bal := e.balance(jakselID, sku1ID); bal.Physical != 1 || bal.Available != 1 {
		t.Fatalf("saldo SKU1 toko demo %+v, ingin 1 unit terakhir (Kasus 4)", bal)
	}

	sale := signed(t, e.key, e.newTxn(1, line(sku1ID, 1, priceSKU1, store.Code)))
	r := e.sync(sale).Results[0]
	if r.Status != service.PosStatusAccepted || !slices.Equal(r.detail(t).SerialsSold, []string{store.Code}) {
		t.Fatalf("jual unit toko: %s %s", r.Status, r.Detail)
	}
	if s := e.str(`SELECT status FROM serial_units WHERE id = ?`, store.ID); s != "SOLD" {
		t.Fatalf("status serial setelah dijual %s, ingin SOLD", s)
	}
	if again := e.sync(sale).Results[0]; again.Status != service.PosStatusDuplicate {
		t.Fatalf("kirim ulang: %s", again.Status)
	}
	if bal := e.balance(jakselID, sku1ID); bal.Physical != 0 {
		t.Fatalf("saldo SKU1 toko setelah jual %d, ingin 0", bal.Physical)
	}
	e.assertLedgerMatchesBalances()
}
