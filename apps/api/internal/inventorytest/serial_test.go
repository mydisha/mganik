package inventorytest_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/clock"
	"mganik/api/internal/domain"
	"mganik/api/internal/inventorytest"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
	"mganik/api/testutil"
)

type recordingHook struct {
	orderID uuid.UUID
	serials []uuid.UUID
}

func (h *recordingHook) OnSerialsSold(_ context.Context, _ *gorm.DB, orderID, _ uuid.UUID, ids []uuid.UUID, _ service.LedgerActor) error {
	h.orderID, h.serials = orderID, append([]uuid.UUID(nil), ids...)
	return nil
}

// Fulfill dengan serial di atas data demo seed: serial dicatat di
// order_line_serials, hook serialisasi (Fase 2) dipanggil, dan satu unit
// tidak bisa terjual dua kali.
func TestFulfill_WithSerialsOnSeedData(t *testing.T) {
	db := testutil.NewDB(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := seed.Run(context.Background(), db, seed.Options{
		Enabled: true, Pepper: []byte("pepper-test"), DemoPassword: "password-demo-test",
		PasswordParams: domain.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, Log: log,
	}); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(inventorytest.Start)
	inv := service.NewInventoryService(db, clk)
	orders := service.NewOrderService(db, clk, inv, service.NewIdempotencyService(db, clk), 0)
	payments := service.NewPaymentService(orders)
	hook := &recordingHook{}
	orders.SetSerialSaleHook(hook)
	ops := inventorytest.Staff(inventorytest.AllInventoryPerms...)
	ctx := context.Background()

	place := func(sku string, qty int64) service.OrderView {
		t.Helper()
		var v service.OrderView
		_, err := orders.CreateIdempotent(ctx, ops, uuid.NewString(), service.CreateOrderInput{
			Lines: []service.CreateOrderLine{{ProductID: seed.ProductID(sku), Qty: qty}},
		}, func(ov service.OrderView) any { v = ov; return map[string]string{} })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := payments.HandleMockWebhook(ctx, service.PaymentWebhookInput{
			ProviderRef: uuid.NewString(), OrderID: v.Order.ID, Amount: v.Order.Total, Currency: "IDR", Status: "SUCCEEDED",
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	unit := func(i int) uuid.UUID { return seed.DemoSerialID("UNIT", i) }

	o := place(seed.SKU1, 2)
	line := o.Lines[0].ID
	for _, tc := range []struct {
		name    string
		serials []service.LineSerials
		want    error
	}{
		{"melebihi qty line", []service.LineSerials{{OrderLineID: line, SerialIDs: []uuid.UUID{unit(0), unit(1), unit(2)}}}, service.ErrOrderSerialInvalid},
		{"serial tidak ada", []service.LineSerials{{OrderLineID: line, SerialIDs: []uuid.UUID{uuid.New()}}}, service.ErrOrderSerialInvalid},
		{"serial duplikat", []service.LineSerials{{OrderLineID: line, SerialIDs: []uuid.UUID{unit(0), unit(0)}}}, service.ErrOrderSerialInvalid},
		{"line bukan milik order", []service.LineSerials{{OrderLineID: uuid.New(), SerialIDs: []uuid.UUID{unit(0)}}}, service.ErrOrderSerialInvalid},
		{"serial karton, bukan unit", []service.LineSerials{{OrderLineID: line, SerialIDs: []uuid.UUID{seed.DemoSerialID("CARTON", 0)}}}, service.ErrOrderSerialInvalid},
	} {
		if _, err := orders.Fulfill(ctx, ops, o.Order.ID, tc.serials); err != tc.want {
			t.Errorf("%s: dapat %v", tc.name, err)
		}
	}

	v, err := orders.Fulfill(ctx, ops, o.Order.ID, []service.LineSerials{{OrderLineID: line, SerialIDs: []uuid.UUID{unit(0), unit(1)}}})
	if err != nil {
		t.Fatal(err)
	}
	if v.Order.Status != "SHIPPED" || len(v.Serials) != 2 {
		t.Fatalf("status %s serial %d", v.Order.Status, len(v.Serials))
	}
	if hook.orderID != o.Order.ID || len(hook.serials) != 2 {
		t.Fatalf("hook tidak dipanggil dengan benar: %+v", hook)
	}

	other := place(seed.SKU1, 1)
	if _, err := orders.Fulfill(ctx, ops, other.Order.ID, []service.LineSerials{{OrderLineID: other.Lines[0].ID, SerialIDs: []uuid.UUID{unit(1)}}}); err != service.ErrOrderSerialSold {
		t.Fatalf("unit terjual dua kali: %v", err)
	}
	wrongProduct := place(seed.SKU2, 1)
	if _, err := orders.Fulfill(ctx, ops, wrongProduct.Order.ID, []service.LineSerials{{OrderLineID: wrongProduct.Lines[0].ID, SerialIDs: []uuid.UUID{unit(5)}}}); err != service.ErrOrderSerialInvalid {
		t.Fatalf("serial produk lain: %v", err)
	}

	// Data demo tetap konsisten dengan engine: ledger = saldo.
	(&inventorytest.Fixture{T: t, DB: db}).AssertInvariants()
}

// I2: dengan hook serialisasi sungguhan (wiring router), fulfill memindahkan
// serial ke SOLD di transaksi yang sama; serial yang tidak ada di lokasi
// pemenuhan menggagalkan seluruh fulfill (order tetap PAID, stok tidak
// berkurang, serial tidak tercatat).
func TestFulfill_MarksSerialsSold(t *testing.T) {
	db := testutil.NewDB(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := seed.Run(context.Background(), db, seed.Options{
		Enabled: true, Pepper: []byte("pepper-test"), DemoPassword: "password-demo-test",
		PasswordParams: domain.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}, Log: log,
	}); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(inventorytest.Start)
	inv := service.NewInventoryService(db, clk)
	orders := service.NewOrderService(db, clk, inv, service.NewIdempotencyService(db, clk), 0)
	orders.SetSerialSaleHook(service.NewOrderSerialSale(service.NewSerialService(db, clk, []byte("pepper-test"))))
	payments := service.NewPaymentService(orders)
	ops := inventorytest.Staff(inventorytest.AllInventoryPerms...)
	ctx := context.Background()

	place := func(qty int64) service.OrderView {
		t.Helper()
		var v service.OrderView
		_, err := orders.CreateIdempotent(ctx, ops, uuid.NewString(), service.CreateOrderInput{
			Lines: []service.CreateOrderLine{{ProductID: seed.ProductID(seed.SKU1), Qty: qty}},
		}, func(ov service.OrderView) any { v = ov; return map[string]string{} })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := payments.HandleMockWebhook(ctx, service.PaymentWebhookInput{
			ProviderRef: uuid.NewString(), OrderID: v.Order.ID, Amount: v.Order.Total, Currency: "IDR", Status: "SUCCEEDED",
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	status := func(id uuid.UUID) string {
		t.Helper()
		var s string
		if err := db.Raw("SELECT status FROM serial_units WHERE id = ?", id).Scan(&s).Error; err != nil {
			t.Fatal(err)
		}
		return s
	}

	// Serial di lokasi lain (unit lepas di toko, order dipenuhi dari gudang):
	// seluruh fulfill batal.
	storeUnit := seed.DemoSerialID("UNIT", seed.DemoStoreUnitIndex)
	o := place(1)
	if _, err := orders.Fulfill(ctx, ops, o.Order.ID, []service.LineSerials{{OrderLineID: o.Lines[0].ID, SerialIDs: []uuid.UUID{storeUnit}}}); err == nil {
		t.Fatal("serial di lokasi lain harus menggagalkan fulfill")
	}
	var after service.OrderView
	after, err := orders.Get(ctx, ops, o.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Order.Status != "PAID" || len(after.Serials) != 0 || status(storeUnit) != domain.SerialInStock {
		t.Fatalf("rollback tidak utuh: status %s serial %d unit %s", after.Order.Status, len(after.Serials), status(storeUnit))
	}

	// Serial di lokasi pemenuhan: SOLD, dengan event penjualan ber-ref ORDER.
	u0, u1 := seed.DemoSerialID("UNIT", 0), seed.DemoSerialID("UNIT", 1)
	two := place(2)
	if _, err := orders.Fulfill(ctx, ops, two.Order.ID, []service.LineSerials{{OrderLineID: two.Lines[0].ID, SerialIDs: []uuid.UUID{u0, u1}}}); err != nil {
		t.Fatal(err)
	}
	if status(u0) != domain.SerialSold || status(u1) != domain.SerialSold {
		t.Fatalf("serial belum SOLD: %s %s", status(u0), status(u1))
	}
	var events int64
	if err := db.Raw("SELECT count(*) FROM unit_events WHERE serial_unit_id IN (?, ?) AND ref_type = 'ORDER' AND ref_id = ?", u0, u1, two.Order.ID).Scan(&events).Error; err != nil {
		t.Fatal(err)
	}
	if events < 2 {
		t.Fatalf("event penjualan ber-ref ORDER: %d", events)
	}
	(&inventorytest.Fixture{T: t, DB: db}).AssertInvariants()
}
