package postest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"mganik/api/internal/config"
	"mganik/api/internal/domain"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

// F1 (TestQA_SeqSquatting): pemegang token device tanpa signing key mengirim
// lebih dulu (a) seq berikutnya dengan client_txn_id acak dan (b) client_txn_id
// asli dengan payload sama/berubah tetapi signature rusak. Tidak ada yang
// disimpan, jadi kiriman asli tetap ACCEPTED dan stok berkurang.
func TestQA_SeqSquatting(t *testing.T) {
	e := newEnv(t)
	genuine := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	genuineRaw := signed(t, e.key, genuine)

	squatSeq := e.newTxn(1, line(sku2ID, 5, priceSKU2))
	squatSeq["signature"] = strings.Repeat("ab", 32)
	rawSquat, _ := json.Marshal(squatSeq)

	var sameIDBadSig map[string]any
	e.must(json.Unmarshal(genuineRaw, &sameIDBadSig))
	sameIDBadSig["signature"] = strings.Repeat("0", 64)
	rawSameID, _ := json.Marshal(sameIDBadSig)

	changed := map[string]any{}
	for k, v := range sameIDBadSig {
		changed[k] = v
	}
	changed["note"] = "diubah penyerang"
	rawChanged, _ := json.Marshal(changed)

	for _, raw := range []json.RawMessage{rawSquat, rawSameID, rawChanged} {
		r := e.sync(raw).Results[0]
		if r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonInvalidSignature {
			t.Fatalf("serangan: %s %s", r.Status, r.Detail)
		}
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions`); n != 0 {
		t.Fatalf("payload bersignature salah tersimpan (%d baris)", n)
	}

	out := e.sync(genuineRaw)
	if r := out.Results[0]; r.Status != service.PosStatusAccepted || out.AckUpToSeq != 1 {
		t.Fatalf("kiriman asli: %s %s ack=%d", r.Status, r.Detail, out.AckUpToSeq)
	}
	if got := e.balance(jakselID, sku2ID); got.Physical != 11 {
		t.Fatalf("stok %d, ingin 11 (barang terjual)", got.Physical)
	}
	// Setelah yang asli masuk, versi rusak tetap ditolak tanpa mengubah apa pun.
	if r := e.sync(rawSameID).Results[0]; r.Status != service.PosStatusRejected {
		t.Fatalf("ulang rusak: %s", r.Status)
	}
	if r := e.sync(genuineRaw).Results[0]; r.Status != service.PosStatusDuplicate || r.Result != service.PosStatusAccepted {
		t.Fatalf("ulang asli: %s %s", r.Status, r.Result)
	}
}

// F2 (TestQA_AckStuckAfterKeyReuse): client_txn_id dipakai ulang dengan seq
// baru (bersignature sah) → REJECTED idempotency_key_reused, seq baru diisi
// tombstone sehingga ack tidak macet.
func TestQA_AckStuckAfterKeyReuse(t *testing.T) {
	e := newEnv(t)
	first := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	if r := e.sync(signed(t, e.key, first)).Results[0]; r.Status != service.PosStatusAccepted {
		t.Fatalf("pertama: %s", r.Status)
	}
	reuse := e.newTxn(2, line(sku2ID, 2, priceSKU2))
	reuse["client_txn_id"] = first["client_txn_id"]
	reuseRaw := signed(t, e.key, reuse)
	out := e.sync(reuseRaw)
	r := out.Results[0]
	if r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonIdempotencyKeyReused {
		t.Fatalf("pakai ulang: %s %s", r.Status, r.Detail)
	}
	if out.AckUpToSeq != 2 || len(out.MissingSeqs) != 0 {
		t.Fatalf("ack macet: ack=%d missing=%v", out.AckUpToSeq, out.MissingSeqs)
	}
	// Kirim ulang tidak membuat tombstone kedua; seq berikutnya normal.
	e.sync(reuseRaw)
	if n := e.count(`SELECT count(*) FROM pos_transactions WHERE device_seq = 2 AND result = 'REJECTED' AND cashier_id IS NULL
	                  AND result_detail->>'reason' = 'idempotency_key_reused'`); n != 1 {
		t.Fatalf("tombstone seq 2: %d", n)
	}
	out = e.sync(signed(t, e.key, e.newTxn(3, line(sku2ID, 1, priceSKU2))))
	if out.AckUpToSeq != 3 || out.Results[0].Status != service.PosStatusAccepted {
		t.Fatalf("seq 3: ack=%d %s", out.AckUpToSeq, out.Results[0].Status)
	}
	if got := e.balance(jakselID, sku2ID); got.Physical != 10 {
		t.Fatalf("stok %d, ingin 10 (pakai ulang tidak mengurangi stok)", got.Physical)
	}
}

// F2: AdvanceAck memindai per jendela, tidak semua transaksi di atas ack;
// rentang terisi melewati beberapa jendela tetap maju sampai ujungnya.
func TestPosSync_AckAcrossWindows(t *testing.T) {
	e := newEnv(t)
	const n = 25_000
	e.exec(`
		INSERT INTO pos_transactions (id, device_id, client_txn_id, device_seq, location_id, occurred_at, received_at,
		       payload_hash, result, result_detail)
		SELECT gen_random_uuid(), ?, gen_random_uuid(), g, ?, now(), now(), sha256(g::text::bytea), 'REJECTED', '{}'
		  FROM generate_series(1, ?) g WHERE g <> ?`, deviceID, jakselID, n, n-3)
	out := e.sync()
	if out.AckUpToSeq != n-4 || !slices.Equal(out.MissingSeqs, []int64{n - 3}) {
		t.Fatalf("ack=%d missing=%v", out.AckUpToSeq, out.MissingSeqs)
	}
}

// F3 (TestQA_HeartbeatBeforeSyncWindow): toko OFFLINE, kasir menjual unit
// terakhir. Internet kembali dan heartbeat datang sebelum outbox terkirim:
// toko TIDAK boleh ONLINE, jadi order online tidak bisa me-reserve unit yang
// sudah terjual dan sync tidak perlu preempt apa pun.
func TestQA_HeartbeatBeforeSyncWindow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.exec(`UPDATE locations SET online_fulfillment_enabled = false WHERE id IN (?, ?, ?)`, centralID, hubID, bandungID)
	inv := service.NewInventoryService(e.db, e.clk)
	orders := service.NewOrderService(e.db, e.clk, inv, service.NewIdempotencyService(e.db, e.clk), 15*time.Minute)
	ops := staff(opsID, true, nil, "order.create")

	sale := e.newTxn(1, line(sku1ID, 1, priceSKU1)) // terjual saat toko OFFLINE
	e.clk.Advance(time.Minute)

	for _, body := range []map[string]any{{}, {"pending_count": 1}} {
		r := e.device(http.MethodPost, "/api/v1/pos/heartbeat", e.token, body)
		var hb heartbeatResp
		r.decode(t, &hb)
		if hb.LocationSyncState != "OFFLINE" || hb.OnlineBlockedReason == nil {
			t.Fatalf("heartbeat %v sebelum outbox habis: %s", body, r.Body)
		}
	}
	_, err := orders.CreateIdempotent(ctx, ops, "qa-hb-1", service.CreateOrderInput{
		Lines: []service.CreateOrderLine{{ProductID: sku1ID, Qty: 1}},
	}, func(service.OrderView) any { return nil })
	if err == nil {
		t.Fatal("order online tidak boleh dijanjikan dari toko yang outbox-nya belum habis")
	}

	r := e.sync(signed(t, e.key, sale)).Results[0]
	if r.Status != service.PosStatusAccepted || len(r.detail(t).Conflicts) != 0 {
		t.Fatalf("sync tanpa preempt: %s %s", r.Status, r.Detail)
	}
	if n := e.count(`SELECT count(*) FROM allocation_conflicts`); n != 0 {
		t.Fatal("tidak boleh ada konflik yang sebenarnya bisa dihindari")
	}
	if hb := e.heartbeatPending(0); hb.LocationSyncState != "ONLINE" || hb.OnlineBlockedReason != nil {
		t.Fatalf("setelah sync: %+v", hb)
	}

	// Gap seq: device bilang outbox kosong, tapi server melihat seq 3 tanpa 2.
	e.sync(signed(t, e.key, e.newTxn(3, line(sku2ID, 1, priceSKU2))))
	e.clk.Advance(e.pos.Config().HeartbeatTimeout + time.Second)
	if _, err := e.pos.MarkStaleLocationsOffline(ctx); err != nil {
		t.Fatal(err)
	}
	if hb := e.heartbeatPending(0); hb.LocationSyncState != "OFFLINE" || hb.OnlineBlockedReason == nil ||
		*hb.OnlineBlockedReason != service.PosOnlineBlockedMissingSeqs {
		t.Fatalf("heartbeat dengan gap seq: %+v", hb)
	}
}

// F4: normalisasi serial sama dengan bagian lain sistem; format salah jadi
// flag tanpa anomali "barang tidak terdaftar".
func TestPosSync_SerialNormalizationAndInvalidFormat(t *testing.T) {
	e := newEnv(t)
	e.moveUnitsToJaksel(0) // kode unit 0 = MVTW3YW6H7GAJW8S
	x := e.newTxn(1, line(sku1ID, 2, priceSKU1, "mvtw-3yw6-h7ga-jw8s", "BUKAN-SERIAL!"))
	r := e.sync(signed(t, e.key, x)).Results[0]
	d := r.detail(t)
	if r.Status != service.PosStatusAccepted || !slices.Equal(d.SerialsSold, []string{seed.DemoSerialCode("UNIT", 0)}) {
		t.Fatalf("serial: %s %s", r.Status, r.Detail)
	}
	if !slices.Contains(d.Flags, service.PosFlagSerialInvalidFormat) || !slices.Equal(d.Invalid, []string{"BUKAN-SERIAL!"}) {
		t.Fatalf("flag format: %v %v", d.Flags, d.Invalid)
	}
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'POS_UNREGISTERED_SERIAL'`); n != 0 {
		t.Fatal("format salah tidak boleh menjadi anomali barang tidak terdaftar")
	}
	if s := e.str(`SELECT status FROM serial_units WHERE id = ?`, seed.DemoSerialID("UNIT", 0)); s != "SOLD" {
		t.Fatalf("serial ternormalisasi harus SOLD, dapat %s", s)
	}
}

// F5: kasir yang dinonaktifkan setelah menjual offline: penjualan tetap
// fakta (flag + anomali). Kasir tanpa role di lokasi tetap ditolak.
func TestPosSync_InactiveCashier(t *testing.T) {
	e := newEnv(t)
	sale := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	e.exec(`UPDATE users SET status = 'DISABLED' WHERE id = ?`, kasirID)
	r := e.sync(signed(t, e.key, sale)).Results[0]
	d := r.detail(t)
	if r.Status != service.PosStatusAccepted || !slices.Contains(d.Flags, service.PosFlagCashierInactive) {
		t.Fatalf("kasir nonaktif: %s %s", r.Status, r.Detail)
	}
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'POS_CASHIER_INACTIVE' AND subject_id = ?`, kasirID); n != 1 {
		t.Fatal("anomali kasir nonaktif tidak tercatat")
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions WHERE cashier_id = ? AND result = 'ACCEPTED'`, kasirID); n != 1 {
		t.Fatal("kasir harus tercatat di transaksi")
	}
	if got := e.balance(jakselID, sku2ID); got.Physical != 11 {
		t.Fatalf("stok %d, ingin 11", got.Physical)
	}
	// Gudang: tidak punya role pos.sync di lokasi itu → ditolak.
	other := e.newTxn(2, line(sku2ID, 1, priceSKU2))
	other["cashier_id"] = seed.UserID("gudang@mganik.test").String()
	if r := e.sync(signed(t, e.key, other)).Results[0]; r.Status != service.PosStatusRejected ||
		r.detail(t).Reason != service.PosReasonCashierNotAllowed {
		t.Fatalf("tanpa role: %s %s", r.Status, r.Detail)
	}
}

// F6: flag uang. Semua tetap diterima (fakta).
func TestPosSync_MoneyFlags(t *testing.T) {
	e := newEnv(t)
	e.receive(jakselID, sku2ID, 1000)
	discount := func(x txn, d int64) txn {
		l := x["lines"].([]map[string]any)[0]
		l["discount"] = d
		total := l["qty"].(int64)*l["unit_price"].(int64) - d
		x["total"], x["payments"] = total, []any{cash(max(total, 1))}
		return x
	}
	mismatch := e.newTxn(1, line(sku2ID, 1, 30000))
	high := discount(e.newTxn(2, line(sku2ID, 1, priceSKU2)), priceSKU2*60/100)
	edge := discount(e.newTxn(3, line(sku2ID, 1, priceSKU2)), priceSKU2/2) // tepat 50% → tanpa flag
	zero := e.newTxn(4, line(sku2ID, 1, 0))
	zero["payments"] = []any{}
	big := e.newTxn(5, line(sku2ID, 400, priceSKU2)) // 13,2 juta > 10 juta

	out := e.sync(signed(t, e.key, mismatch), signed(t, e.key, high), signed(t, e.key, edge),
		signed(t, e.key, zero), signed(t, e.key, big))
	want := [][]string{
		{"PRICE_MISMATCH"},
		{"HIGH_DISCOUNT"},
		nil,
		{"PRICE_MISMATCH", "ZERO_TOTAL"},
		{"OFFLINE_VALUE_LIMIT"},
	}
	for i, r := range out.Results {
		if r.Status != service.PosStatusAccepted {
			t.Fatalf("seq %d: %s %s", r.DeviceSeq, r.Status, r.Detail)
		}
		if f := r.detail(t).Flags; !slices.Equal(f, want[i]) {
			t.Fatalf("seq %d flags %v, ingin %v", r.DeviceSeq, f, want[i])
		}
	}
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'POS_OFFLINE_VALUE_LIMIT'`); n != 1 {
		t.Fatal("anomali nilai di atas batas tidak tercatat")
	}

	// Ambang dari config.
	e2 := newEnvWith(t, config.POSConfig{HighDiscountPercent: 10, OfflineValueLimit: 100_000})
	x := discount(e2.newTxn(1, line(sku2ID, 4, priceSKU2)), 20000) // 15% diskon, total 112.000
	f := e2.sync(signed(t, e2.key, x)).Results[0].detail(t).Flags
	if !slices.Equal(f, []string{"HIGH_DISCOUNT", "OFFLINE_VALUE_LIMIT"}) {
		t.Fatalf("ambang config: %v", f)
	}
}

// F7: surrogate UTF-16 tunggal → REJECTED invalid_payload (bersignature
// sah, disimpan, ack maju), tanpa anomali "dimanipulasi".
func TestPosSync_LoneSurrogateRejected(t *testing.T) {
	e := newEnv(t)
	x := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	x["note"] = "SURROGATE"
	raw, _ := json.Marshal(x)
	raw = bytes.Replace(raw, []byte(`"SURROGATE"`), []byte(`"catatan \ud83d"`), 1)
	canon, info, err := domain.CanonicalPosJSONInfo(raw)
	if err != nil || !info.LoneSurrogate {
		t.Fatalf("kanonis: %v %+v", err, info)
	}
	var obj map[string]json.RawMessage
	e.must(json.Unmarshal(raw, &obj))
	obj["signature"], _ = json.Marshal(domain.NewPosSigner(e.key).Sign(canon))
	// Marshal ulang map RawMessage mempertahankan escape \ud83d apa adanya.
	signedRaw, _ := json.Marshal(obj)
	if !bytes.Contains(signedRaw, []byte(`\ud83d`)) {
		t.Fatalf("escape hilang: %s", signedRaw)
	}
	out := e.sync(signedRaw)
	r := out.Results[0]
	if r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonInvalidPayload {
		t.Fatalf("surrogate: %s %s", r.Status, r.Detail)
	}
	if out.AckUpToSeq != 1 {
		t.Fatalf("ack %d, ingin 1 (disimpan sebagai REJECTED)", out.AckUpToSeq)
	}
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'POS_PAYLOAD_MISMATCH'`); n != 0 {
		t.Fatal("surrogate tunggal bukan manipulasi; tidak boleh ada anomali")
	}
	if got := e.balance(jakselID, sku2ID); got.Physical != 12 {
		t.Fatal("transaksi ditolak tidak boleh mengubah stok")
	}
}

// F8b: batas serial per transaksi dari config (selain <= qty per line).
func TestPosSync_MaxSerialsPerTxn(t *testing.T) {
	e := newEnvWith(t, config.POSConfig{MaxSerialsPerTxn: 3})
	codes := e.moveUnitsToJaksel(0, 1, 2, 3)
	x := e.newTxn(1, line(sku1ID, 2, priceSKU1, codes[0], codes[1]), line(sku1ID, 2, priceSKU1, codes[2], codes[3]))
	r := e.sync(signed(t, e.key, x)).Results[0]
	d := r.detail(t)
	if r.Status != service.PosStatusRejected || d.Reason != service.PosReasonValidationFailed ||
		!slices.ContainsFunc(d.Errors, func(f fieldErr) bool { return f.Code == "max_serials" }) {
		t.Fatalf("batas serial: %s %s", r.Status, r.Detail)
	}
	// Tepat di batas: bulk insert order_line_serials untuk dua line.
	y := e.newTxn(2, line(sku1ID, 2, priceSKU1, codes[0], codes[1]), line(sku1ID, 1, priceSKU1, codes[2]))
	d = e.sync(signed(t, e.key, y)).Results[0].detail(t)
	if len(d.SerialsSold) != 3 || e.count(`SELECT count(*) FROM order_line_serials ols JOIN order_lines ol ON ol.id = ols.order_line_id
	                  WHERE ol.order_id = ?`, *d.OrderID) != 3 {
		t.Fatalf("serial terjual %v", d.SerialsSold)
	}
}

// F9a: resolusi ORDER_CANCELLED hanya sah kalau order terkait sudah batal
// atau kedaluwarsa.
func TestConflictResolve_OrderCancelledRequiresCancelledOrder(t *testing.T) {
	k := setupKasus4(t)
	e := k.e
	e.exec(`UPDATE locations SET online_fulfillment_enabled = true WHERE id = ?`, centralID)
	conflictID := k.assertPreempted(t, k.sellOffline(t))
	ops := e.staffEngine(staff(opsID, true, nil, service.PermConflictResolve))
	path := "/api/v1/cc/conflicts/" + conflictID.String() + "/resolve"
	body := map[string]any{"resolution": "ORDER_CANCELLED", "note": "Order dibatalkan dan direfund"}

	r := serve(ops, http.MethodPost, path, body, nil)
	if r.Code != http.StatusConflict || r.problem(t).Code != "resolution_not_applicable" {
		t.Fatalf("order belum batal: %d %s", r.Code, r.Body)
	}
	cancel := staff(opsID, true, nil, "order.cancel")
	inv := service.NewInventoryService(e.db, e.clk)
	orders := service.NewOrderService(e.db, e.clk, inv, service.NewIdempotencyService(e.db, e.clk), 15*time.Minute)
	if _, err := orders.Cancel(context.Background(), cancel, k.orderID, "pelanggan minta batal"); err != nil {
		t.Fatal(err)
	}
	if r := serve(ops, http.MethodPost, path, body, nil); r.Code != http.StatusOK {
		t.Fatalf("order sudah batal: %d %s", r.Code, r.Body)
	}

	// Konflik tanpa order (selisih stok) tidak bisa ORDER_CANCELLED.
	disc := e.openDiscrepancyConflict(t, 2)
	r = serve(ops, http.MethodPost, "/api/v1/cc/conflicts/"+disc.String()+"/resolve", body, nil)
	if r.Code != http.StatusConflict {
		t.Fatalf("konflik tanpa order: %d %s", r.Code, r.Body)
	}
}
