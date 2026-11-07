package postest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"mganik/api/internal/service"
)

// TestPosSync_DuplicateBatch (deliverable wajib): batch yang sama dikirim dua
// kali → hasil kedua DUPLICATE dengan result dan detail identik byte per
// byte, stok berkurang sekali, order dan pembayaran tercatat sekali.
func TestPosSync_DuplicateBatch(t *testing.T) {
	e := newEnv(t)
	t1 := signed(t, e.key, e.newTxn(1, line(sku2ID, 2, priceSKU2)))
	t2 := signed(t, e.key, e.newTxn(2, line(sku2ID, 1, priceSKU2), line(sku1ID, 1, priceSKU1)))

	first := e.sync(t1, t2)
	if len(first.Results) != 2 || first.AckUpToSeq != 2 || len(first.MissingSeqs) != 0 {
		t.Fatalf("respons pertama: %+v", first)
	}
	for _, r := range first.Results {
		if r.Status != service.PosStatusAccepted || r.Result != service.PosStatusAccepted {
			t.Fatalf("transaksi %d: %s %s %s", r.DeviceSeq, r.Status, r.Result, r.Detail)
		}
	}
	stockAfterFirst := e.balance(jakselID, sku2ID)
	if stockAfterFirst.Physical != 12-3 {
		t.Fatalf("stok SKU2 %d, ingin 9", stockAfterFirst.Physical)
	}

	e.clk.Advance(30_000_000_000) // 30 detik: pengiriman ulang setelah timeout klien
	second := e.sync(t1, t2)
	if second.AckUpToSeq != 2 || len(second.Results) != 2 {
		t.Fatalf("respons kedua: %+v", second)
	}
	for i, r := range second.Results {
		if r.Status != service.PosStatusDuplicate || r.Result != first.Results[i].Result {
			t.Fatalf("pengiriman ulang %d: status %s result %s", r.DeviceSeq, r.Status, r.Result)
		}
		if !bytes.Equal(r.Detail, first.Results[i].Detail) {
			t.Fatalf("detail tidak identik:\n%s\n%s", first.Results[i].Detail, r.Detail)
		}
	}
	if got := e.balance(jakselID, sku2ID); got != stockAfterFirst {
		t.Fatalf("stok berubah saat duplikat: %+v vs %+v", got, stockAfterFirst)
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions WHERE device_id = ?`, deviceID); n != 2 {
		t.Fatalf("pos_transactions %d, ingin 2", n)
	}
	if n := e.count(`SELECT count(*) FROM orders WHERE channel_code = 'POS' AND status = 'COMPLETED'`); n != 2 {
		t.Fatalf("order POS %d, ingin 2", n)
	}
	if n := e.count(`SELECT count(*) FROM payments p JOIN orders o ON o.id = p.order_id WHERE o.channel_code = 'POS'`); n != 2 {
		t.Fatalf("pembayaran %d, ingin 2", n)
	}
	if n := e.count(`SELECT count(*) FROM inventory_movements WHERE movement_type = 'DIRECT_SALE' AND ref_type = 'POS_TRANSACTION'`); n != 3 {
		t.Fatalf("ledger DIRECT_SALE %d, ingin 3 (satu per line)", n)
	}
	// Detail respons = result_detail tersimpan.
	d := first.Results[0].detail(t)
	stored := e.str(`SELECT result_detail::text FROM pos_transactions WHERE id = ?`, d.ServerTxnID)
	var compact bytes.Buffer
	e.must(json.Compact(&compact, []byte(stored)))
	if d.OrderID == nil || !bytes.Equal(compact.Bytes(), first.Results[0].Detail) {
		t.Fatalf("detail respons bukan result_detail tersimpan:\n%s\n%s", stored, first.Results[0].Detail)
	}
	if n := e.count(`SELECT count(*) FROM pos_devices WHERE id = ? AND last_acked_seq = 2`, deviceID); n != 1 {
		t.Fatal("last_acked_seq tidak maju ke 2")
	}
	e.assertLedgerMatchesBalances()
}

// TestPosSync_ConcurrentDuplicate: batch yang sama dari dua goroutine
// bersamaan → setiap transaksi diproses tepat sekali (satu ACCEPTED, satu
// DUPLICATE dengan detail identik), stok berkurang sekali.
func TestPosSync_ConcurrentDuplicate(t *testing.T) {
	e := newEnv(t)
	batch := []json.RawMessage{
		signed(t, e.key, e.newTxn(1, line(sku2ID, 1, priceSKU2))),
		signed(t, e.key, e.newTxn(2, line(sku2ID, 2, priceSKU2))),
		signed(t, e.key, e.newTxn(3, line(sku1ID, 1, priceSKU1))),
	}
	body := map[string]any{"client_time": e.clk.Now(), "transactions": batch}

	const workers = 4
	var wg sync.WaitGroup
	outs := make([]resp, workers)
	start := make(chan struct{})
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outs[w] = e.device(http.MethodPost, "/api/v1/pos/sync", e.token, body)
		}()
	}
	close(start)
	wg.Wait()

	accepted := map[int64]int{}
	details := map[int64][]byte{}
	for _, r := range outs {
		if r.Code != http.StatusOK {
			t.Fatalf("sync %d: %s", r.Code, r.Body)
		}
		var out syncResponse
		r.decode(t, &out)
		for _, res := range out.Results {
			switch res.Status {
			case service.PosStatusAccepted:
				accepted[res.DeviceSeq]++
			case service.PosStatusDuplicate:
			default:
				t.Fatalf("seq %d status %s: %s", res.DeviceSeq, res.Status, res.Detail)
			}
			if prev, ok := details[res.DeviceSeq]; ok && !bytes.Equal(prev, res.Detail) {
				t.Fatalf("detail seq %d berbeda antar respons", res.DeviceSeq)
			}
			details[res.DeviceSeq] = res.Detail
		}
	}
	for seq := int64(1); seq <= 3; seq++ {
		if accepted[seq] != 1 {
			t.Fatalf("seq %d diproses %d kali, ingin tepat 1", seq, accepted[seq])
		}
	}
	if got := e.balance(jakselID, sku2ID); got.Physical != 12-3 {
		t.Fatalf("stok SKU2 %d, ingin 9", got.Physical)
	}
	if got := e.balance(jakselID, sku1ID); got.Physical != 0 {
		t.Fatalf("stok SKU1 %d, ingin 0", got.Physical)
	}
	if n := e.count(`SELECT count(*) FROM orders WHERE channel_code = 'POS'`); n != 3 {
		t.Fatalf("order POS %d, ingin 3", n)
	}
	if n := e.count(`SELECT count(*) FROM inventory_movements WHERE movement_type = 'DIRECT_SALE'
	                  AND ref_type = 'POS_TRANSACTION'`); n != 3 {
		t.Fatalf("ledger DIRECT_SALE %d, ingin 3", n)
	}
	e.assertLedgerMatchesBalances()
}

// TestPosSync_SameIdDifferentPayload: client_txn_id sama dengan isi berbeda
// (bertanda tangan sah) → REJECTED idempotency_key_reused + anomali
// POS_PAYLOAD_MISMATCH; transaksi asli dan stok tidak berubah.
func TestPosSync_SameIdDifferentPayload(t *testing.T) {
	e := newEnv(t)
	orig := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	first := e.sync(signed(t, e.key, orig))
	if first.Results[0].Status != service.PosStatusAccepted {
		t.Fatalf("pertama: %+v", first.Results[0])
	}
	before := e.balance(jakselID, sku2ID)

	tampered := txn{}
	for k, v := range orig {
		tampered[k] = v
	}
	tampered["lines"] = []any{line(sku2ID, 5, priceSKU2)}
	tampered["total"] = 5 * priceSKU2
	tampered["payments"] = []any{cash(5 * priceSKU2)}
	out := e.sync(signed(t, e.key, tampered))
	r := out.Results[0]
	if r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonIdempotencyKeyReused {
		t.Fatalf("isi beda harus REJECTED idempotency_key_reused: %s %s", r.Status, r.Detail)
	}
	if got := e.balance(jakselID, sku2ID); got != before {
		t.Fatalf("stok berubah: %+v vs %+v", got, before)
	}
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'POS_PAYLOAD_MISMATCH' AND severity = 'CRITICAL'
	                  AND subject_id = ?`, deviceID); n != 1 {
		t.Fatalf("anomali POS_PAYLOAD_MISMATCH %d, ingin 1", n)
	}
	// Transaksi asli tetap: kirim ulang yang asli = DUPLICATE ACCEPTED.
	again := e.sync(signed(t, e.key, orig))
	if again.Results[0].Status != service.PosStatusDuplicate || again.Results[0].Result != service.PosStatusAccepted ||
		!bytes.Equal(again.Results[0].Detail, first.Results[0].Detail) {
		t.Fatalf("transaksi asli harus tetap: %+v", again.Results[0])
	}

	// device_seq yang sama dipakai client_txn_id lain → REJECTED device_seq_reused.
	reuse := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	r = e.sync(signed(t, e.key, reuse)).Results[0]
	if r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonDeviceSeqReused {
		t.Fatalf("seq dipakai ulang: %s %s", r.Status, r.Detail)
	}
	if got := e.balance(jakselID, sku2ID); got != before {
		t.Fatal("stok berubah oleh seq dipakai ulang")
	}
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'POS_PAYLOAD_MISMATCH'`); n != 2 {
		t.Fatalf("anomali %d, ingin 2", n)
	}
}
