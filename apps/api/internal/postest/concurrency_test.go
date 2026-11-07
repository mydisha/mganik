package postest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"mganik/api/internal/service"
)

// TestPosSync_ConcurrentWithOnlineOrders: sync POS (multi-produk) berjalan
// bersamaan dengan order online multi-line dan job penanda OFFLINE di lokasi
// yang sama. Urutan lock reservasi → saldo → serial sama di kedua jalur:
// tidak ada error ke klien (tanpa RETRY/5xx), ledger = saldo, dan stok
// fisik = awal − terjual POS.
func TestPosSync_ConcurrentWithOnlineOrders(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE locations SET online_fulfillment_enabled = false WHERE id IN (?, ?, ?)`, centralID, hubID, bandungID)
	e.receive(jakselID, sku1ID, 29) // 30: permintaan online + POS (40) > stok, memaksa preempt
	e.receive(jakselID, sku2ID, 48) // 60
	e.heartbeat()

	inv := service.NewInventoryService(e.db, e.clk)
	orders := service.NewOrderService(e.db, e.clk, inv, service.NewIdempotencyService(e.db, e.clk), 15*time.Minute)
	ops := staff(opsID, true, nil, "order.create")

	const (
		onlineWorkers = 10
		posBatches    = 5
		perBatch      = 4
	)
	batches := make([][]json.RawMessage, posBatches)
	for b := range posBatches {
		for i := range perBatch {
			seq := int64(b*perBatch + i + 1)
			batches[b] = append(batches[b], signed(t, e.key, e.newTxn(seq, line(sku2ID, 2, priceSKU2), line(sku1ID, 1, priceSKU1))))
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, onlineWorkers+posBatches+1)
	start := make(chan struct{})
	for w := range onlineWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := orders.CreateIdempotent(context.Background(), ops, fmt.Sprintf("conc-%d", w), service.CreateOrderInput{
				Lines: []service.CreateOrderLine{{ProductID: sku1ID, Qty: 2}, {ProductID: sku2ID, Qty: 1}},
			}, func(service.OrderView) any { return nil })
			if err != nil && !errors.Is(err, service.ErrOutOfStock) {
				errs <- fmt.Errorf("order online %d: %w", w, err)
			}
		}()
	}
	for b := range posBatches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := e.device(http.MethodPost, "/api/v1/pos/sync", e.token, map[string]any{"transactions": batches[b]})
			if r.Code != http.StatusOK {
				errs <- fmt.Errorf("sync %d: %d %s", b, r.Code, r.Body)
				return
			}
			var out syncResponse
			if err := json.Unmarshal(r.Body, &out); err != nil {
				errs <- err
				return
			}
			for _, res := range out.Results {
				if res.Status != service.PosStatusAccepted && res.Status != service.PosStatusAcceptedWithConflict {
					errs <- fmt.Errorf("seq %d: %s %s", res.DeviceSeq, res.Status, res.Detail)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for range 5 {
			if _, err := e.pos.MarkStaleLocationsOffline(context.Background()); err != nil {
				errs <- err
			}
		}
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	sold := int64(posBatches * perBatch)
	if got := e.balance(jakselID, sku1ID); got.Physical != 30-sold {
		t.Fatalf("SKU1 fisik %d, ingin %d", got.Physical, 30-sold)
	}
	if got := e.balance(jakselID, sku2ID); got.Physical != 60-2*sold {
		t.Fatalf("SKU2 fisik %d, ingin %d", got.Physical, 60-2*sold)
	}
	// reserved_qty = jumlah reservasi ACTIVE (preempt mengurangi keduanya).
	if n := e.count(`
		SELECT count(*) FROM inventory_balances b
		 WHERE b.location_id = ? AND b.reserved_qty <> COALESCE((SELECT sum(qty) FROM stock_reservations r
		        WHERE r.location_id = b.location_id AND r.product_id = b.product_id AND r.status = 'ACTIVE'), 0)`, jakselID); n != 0 {
		t.Fatal("reserved_qty tidak sama dengan reservasi ACTIVE")
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions WHERE result <> 'REJECTED'`); n != sold {
		t.Fatalf("transaksi POS %d, ingin %d", n, sold)
	}
	e.assertLedgerMatchesBalances()
}
