package postest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

// receive menambah stok fisik lewat ledger (RECEIPT), supaya test serial dan
// pembayaran tidak tercampur konflik selisih stok.
func (e *env) receive(loc, prod uuid.UUID, qty int64) {
	e.t.Helper()
	inv := service.NewInventoryService(e.db, e.clk)
	e.must(e.db.Transaction(func(tx *gorm.DB) error {
		_, err := inv.ApplyMovement(context.Background(), tx, service.MovementInput{
			LocationID: loc, ProductID: prod, Type: service.MovementReceipt, Qty: qty, RefType: "TEST",
			RefID: uuid.New(), IdempotencyKey: "test:receipt:" + uuid.NewString(), Actor: service.SystemLedgerActor,
		})
		return err
	}))
}

func statuses(rs []syncResult) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Status
	}
	return out
}

func TestPosSync_SeqGapAndOrdering(t *testing.T) {
	e := newEnv(t)
	tx := func(seq int64) json.RawMessage { return signed(t, e.key, e.newTxn(seq, line(sku2ID, 1, priceSKU2))) }

	// Urutan acak di batch: diproses dan dijawab urut device_seq.
	out := e.sync(tx(4), tx(1), tx(2))
	seqs := []int64{out.Results[0].DeviceSeq, out.Results[1].DeviceSeq, out.Results[2].DeviceSeq}
	if !slices.Equal(seqs, []int64{1, 2, 4}) {
		t.Fatalf("hasil harus urut seq, dapat %v", seqs)
	}
	if out.AckUpToSeq != 2 || !slices.Equal(out.MissingSeqs, []int64{3}) || out.MissingSeqsTruncated {
		t.Fatalf("gap: ack=%d missing=%v", out.AckUpToSeq, out.MissingSeqs)
	}
	// Ledger mengikuti urutan seq (waktu terima sama, id ledger UUIDv7 naik).
	var refs []string
	e.must(e.db.Raw(`SELECT pt.device_seq::text FROM inventory_movements m JOIN pos_transactions pt ON pt.id = m.ref_id
	                  WHERE m.movement_type = 'DIRECT_SALE' ORDER BY m.id`).Scan(&refs).Error)
	if !slices.Equal(refs, []string{"1", "2", "4"}) {
		t.Fatalf("urutan pemrosesan %v", refs)
	}

	// Gap terisi: ack maju melewati 4.
	out = e.sync(tx(3))
	if out.AckUpToSeq != 4 || len(out.MissingSeqs) != 0 {
		t.Fatalf("setelah gap terisi: ack=%d missing=%v", out.AckUpToSeq, out.MissingSeqs)
	}

	// Batch kosong: probe status ack tanpa efek.
	out = e.sync()
	if out.AckUpToSeq != 4 || len(out.Results) != 0 || out.MissingSeqs == nil {
		t.Fatalf("batch kosong: %+v", out)
	}

	// seq di atas 2^31-1 (bigint end-to-end): gap besar dilaporkan terpotong.
	const far = int64(3_000_000_000)
	out = e.sync(tx(far))
	if out.Results[0].Status != service.PosStatusAccepted || out.AckUpToSeq != 4 || !out.MissingSeqsTruncated ||
		len(out.MissingSeqs) != 500 || out.MissingSeqs[0] != 5 {
		t.Fatalf("seq besar: ack=%d missing=%d truncated=%v", out.AckUpToSeq, len(out.MissingSeqs), out.MissingSeqsTruncated)
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions WHERE device_seq = ?`, far); n != 1 {
		t.Fatal("seq besar tidak tersimpan")
	}
	// transactions wajib ada (boleh kosong).
	r := e.device(http.MethodPost, "/api/v1/pos/sync", e.token, map[string]any{})
	if r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("tanpa transactions: %d", r.Code)
	}
}

func TestPosSync_BatchLimits(t *testing.T) {
	e := newEnv(t)
	max := e.pos.Config().SyncMaxBatch
	big := make([]json.RawMessage, max+1)
	for i := range big {
		big[i] = json.RawMessage(fmt.Sprintf(`{"client_txn_id":%q,"device_seq":%d}`, uuid.NewString(), i+1))
	}
	r := e.device(http.MethodPost, "/api/v1/pos/sync", e.token, map[string]any{"transactions": big})
	if r.Code != http.StatusUnprocessableEntity || r.problem(t).Code != "batch_too_large" {
		t.Fatalf("batch > %d: %d %s", max, r.Code, r.Body)
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions`); n != 0 {
		t.Fatal("batch ditolak tidak boleh menyimpan apa pun")
	}

	// Struktur batch rusak → 400/422 untuk seluruh batch.
	for name, body := range map[string]string{
		"field asing":         `{"transactions":[],"extra":1}`,
		"client_time ng":      `{"transactions":[],"client_time":"kemarin"}`,
		"transactions objek":  `{"transactions":{}}`,
		"bukan json":          `{"transactions":[`,
		"transactions absent": `{}`,
	} {
		if r := e.device(http.MethodPost, "/api/v1/pos/sync", e.token, body); r.Code != http.StatusUnprocessableEntity &&
			r.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions`); n != 0 {
		t.Fatal("batch ditolak tidak boleh menyimpan apa pun")
	}

	// Body sync boleh melebihi HTTP_MAX_BODY_BYTES (1 MiB) sampai 5 MiB.
	pad := strings.Repeat(" ", 2<<20)
	if r := e.device(http.MethodPost, "/api/v1/pos/sync", e.token, pad+`{"transactions":[]}`); r.Code != http.StatusOK {
		t.Fatalf("body 2 MiB ke /pos/sync: %d %s", r.Code, r.Body)
	}
	pad = strings.Repeat(" ", 6<<20)
	if r := e.device(http.MethodPost, "/api/v1/pos/sync", e.token, pad+`{"transactions":[]}`); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body 6 MiB: %d", r.Code)
	}
	// Route lain tetap 1 MiB.
	pad = strings.Repeat(" ", 2<<20)
	if r := e.device(http.MethodPost, "/api/v1/pos/heartbeat", e.token, pad+`{}`); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("heartbeat 2 MiB harus 413, dapat %d", r.Code)
	}
}

func TestPosSync_InvalidSignature(t *testing.T) {
	e := newEnv(t)
	before := e.balance(jakselID, sku2ID)
	wrongKey := make([]byte, 32)
	x := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	bad := signed(t, wrongKey, x)

	// Isi diubah setelah ditandatangani.
	y := e.newTxn(2, line(sku2ID, 1, priceSKU2))
	var tampered map[string]any
	e.must(json.Unmarshal(signed(t, e.key, y), &tampered))
	tampered["total"] = 1
	tamperedRaw, _ := json.Marshal(tampered)

	// Tanpa signature sama sekali.
	z := e.newTxn(3, line(sku2ID, 1, priceSKU2))
	unsigned, _ := json.Marshal(z)

	out := e.sync(bad, tamperedRaw, unsigned)
	for _, r := range out.Results {
		if r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonInvalidSignature {
			t.Fatalf("seq %d: %s %s", r.DeviceSeq, r.Status, r.Detail)
		}
	}
	if got := e.balance(jakselID, sku2ID); got != before {
		t.Fatal("signature salah tidak boleh memengaruhi stok")
	}
	if n := e.count(`SELECT count(*) FROM orders WHERE channel_code = 'POS'`); n != 0 {
		t.Fatal("signature salah tidak boleh membuat order")
	}
	// TIDAK disimpan: tidak menempati client_txn_id maupun device_seq.
	if n := e.count(`SELECT count(*) FROM pos_transactions`); n != 0 || out.AckUpToSeq != 0 {
		t.Fatalf("signature salah tidak boleh disimpan: n=%d ack=%d", n, out.AckUpToSeq)
	}
	// Satu anomali per device per hari; hit_count menghitung setiap kejadian,
	// evidence memuat seq/client_txn_id/hash kejadian terakhir.
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'POS_PAYLOAD_MISMATCH' AND hit_count = 3
	                  AND evidence->>'device_seq' IS NOT NULL AND evidence->>'client_txn_id' IS NOT NULL
	                  AND evidence->>'payload_hash' IS NOT NULL`); n != 1 {
		t.Fatal("anomali signature harus satu baris dengan hit_count 3 dan evidence lengkap")
	}
	// Kirim ulang tetap REJECTED (bukan DUPLICATE, karena tidak pernah disimpan).
	if r := e.sync(bad).Results[0]; r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonInvalidSignature {
		t.Fatalf("ulang: %+v", r)
	}
	// Versi sah transaksi yang sama tetap bisa masuk.
	if r := e.sync(signed(t, e.key, x)).Results[0]; r.Status != service.PosStatusAccepted {
		t.Fatalf("versi sah setelah signature salah: %s %s", r.Status, r.Detail)
	}
}

func TestPosSync_RevokedDeviceAndCashierScope(t *testing.T) {
	e := newEnv(t)
	// Kasir toko lain (Bandung) yang sah.
	bdgCashier := uuid.New()
	e.exec(`INSERT INTO users (id, email, name, password_hash, status) VALUES (?, 'kasir.bdg@mganik.test', 'Kasir Bandung', '$argon2id$x', 'ACTIVE')`, bdgCashier)
	ur := uuid.New()
	e.exec(`INSERT INTO user_roles (id, user_id, role_code, scope_mode) VALUES (?, ?, 'CASHIER', 'SELECTED')`, ur, bdgCashier)
	e.exec(`INSERT INTO user_role_locations (user_role_id, location_id) VALUES (?, ?)`, ur, bandungID)

	// Per transaksi: kasir lokasi lain → REJECTED cashier_not_allowed (bukan
	// 403 batch, supaya transaksi lain di batch tetap diproses).
	other := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	other["cashier_id"] = bdgCashier.String()
	ghost := e.newTxn(2, line(sku2ID, 1, priceSKU2))
	ghost["cashier_id"] = uuid.NewString() // user tidak ada
	manager := e.newTxn(3, line(sku2ID, 1, priceSKU2))
	manager["cashier_id"] = managerID.String() // kepala toko punya pos.sync di Jaksel
	out := e.sync(signed(t, e.key, other), signed(t, e.key, ghost), signed(t, e.key, manager))
	if got := statuses(out.Results); !slices.Equal(got, []string{"REJECTED", "REJECTED", "ACCEPTED"}) {
		t.Fatalf("status %v", got)
	}
	for _, r := range out.Results[:2] {
		if r.detail(t).Reason != service.PosReasonCashierNotAllowed {
			t.Fatalf("seq %d: %s", r.DeviceSeq, r.Detail)
		}
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions WHERE device_seq = 1 AND cashier_id = ?`, bdgCashier); n != 1 {
		t.Fatal("kasir yang ada tetap dicatat di transaksi yang ditolak")
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions WHERE device_seq = 2 AND cashier_id IS NULL`); n != 1 {
		t.Fatal("kasir yang tidak ada dicatat NULL")
	}

	// Header X-Cashier-Id lokasi lain: resolver menolak seluruh request 403.
	r := serve(e.engine, http.MethodPost, "/api/v1/pos/heartbeat", nil,
		map[string]string{"Authorization": "Bearer " + e.token, "X-Cashier-Id": bdgCashier.String()})
	if r.Code != http.StatusForbidden || r.problem(t).Code != "cashier_not_allowed" {
		t.Fatalf("header kasir lokasi lain: %d %s", r.Code, r.Body)
	}

	// Device dicabut → 401 di semua route device.
	mgr := staff(managerID, false, []uuid.UUID{jakselID}, service.PermPosDeviceManage)
	if r := serve(e.staffEngine(mgr), http.MethodPost, "/api/v1/pos/devices/"+deviceID.String()+"/revoke", nil, nil); r.Code != http.StatusOK {
		t.Fatalf("revoke %d: %s", r.Code, r.Body)
	}
	for _, path := range []string{"/api/v1/pos/sync", "/api/v1/pos/heartbeat"} {
		r := e.device(http.MethodPost, path, e.token, map[string]any{"transactions": []any{}})
		if r.Code != http.StatusUnauthorized || r.problem(t).Code != "invalid_token" {
			t.Fatalf("%s dengan device dicabut: %d %s", path, r.Code, r.Body)
		}
	}
	if r := e.device(http.MethodGet, "/api/v1/pos/bootstrap", e.token, nil); r.Code != http.StatusUnauthorized {
		t.Fatalf("bootstrap device dicabut: %d", r.Code)
	}
	// Staff tidak boleh memakai route device.
	if r := serve(e.staffEngine(staff(opsID, true, nil, service.PermPosSync)), http.MethodPost, "/api/v1/pos/heartbeat", nil, nil); r.Code != http.StatusForbidden {
		t.Fatalf("staff ke route device: %d", r.Code)
	}
}

// moveUnitsToJaksel memindahkan unit demo ke toko (fixture), dan stok toko
// ditambah supaya test serial tidak tercampur selisih stok.
func (e *env) moveUnitsToJaksel(idx ...int) []string {
	e.t.Helper()
	var codes []string
	for _, i := range idx {
		e.exec(`UPDATE serial_units SET location_id = ? WHERE id = ?`, jakselID, seed.DemoSerialID("UNIT", i))
		codes = append(codes, seed.DemoSerialCode("UNIT", i))
	}
	e.receive(jakselID, sku1ID, 20)
	return codes
}

func TestPosSync_Serials(t *testing.T) {
	e := newEnv(t)
	codes := e.moveUnitsToJaksel(0, 1, 2)
	atCentral := seed.DemoSerialCode("UNIT", 10) // masih di gudang pusat
	unknown := "ZZZZZZZZZZZZZZZZ"

	// Satu transaksi: dua serial sah + satu tidak terdaftar + satu salah lokasi.
	x := e.newTxn(1, line(sku1ID, 4, priceSKU1, strings.ToLower(codes[0]), codes[1], unknown, atCentral))
	r := e.sync(signed(t, e.key, x)).Results[0]
	if r.Status != service.PosStatusAcceptedWithConflict {
		t.Fatalf("serial bermasalah tidak boleh menolak penjualan: %s %s", r.Status, r.Detail)
	}
	d := r.detail(t)
	if !slices.Equal(d.SerialsSold, []string{codes[0], codes[1]}) {
		t.Fatalf("serial terjual %v", d.SerialsSold)
	}
	types := map[string]string{}
	for _, c := range d.Conflicts {
		types[c.Code] = c.Type + "/" + c.Reason
	}
	if types[unknown] != "SERIAL_UNREGISTERED/not_registered" || types[atCentral] != "SERIAL_DOUBLE_SALE/wrong_location" {
		t.Fatalf("konflik serial %v", types)
	}
	for _, c := range codes[:2] {
		if s := e.str(`SELECT status FROM serial_units WHERE code = ?`, c); s != "SOLD" {
			t.Fatalf("serial sah %s harus SOLD, dapat %s", c, s)
		}
	}
	if s := e.str(`SELECT status FROM serial_units WHERE code = ?`, atCentral); s != "IN_STOCK" {
		t.Fatal("serial salah lokasi tidak boleh diubah")
	}
	if n := e.count(`SELECT count(*) FROM order_line_serials ols JOIN order_lines ol ON ol.id = ols.order_line_id
	                  WHERE ol.order_id = ?`, *d.OrderID); n != 2 {
		t.Fatalf("order_line_serials %d, ingin 2", n)
	}
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'POS_UNREGISTERED_SERIAL' AND severity = 'CRITICAL'
	                  AND subject_id = ?`, jakselID); n != 1 {
		t.Fatal("anomali POS_UNREGISTERED_SERIAL tidak tercatat")
	}
	if n := e.count(`SELECT count(*) FROM allocation_conflicts WHERE kind = 'SERIAL_DOUBLE_SALE' AND status = 'OPEN'
	                  AND serial_unit_id = ? AND details->>'reason' = 'wrong_location'`, seed.DemoSerialID("UNIT", 10)); n != 1 {
		t.Fatal("konflik serial salah lokasi tidak tercatat")
	}
	if n := e.count(`SELECT count(*) FROM unit_events WHERE serial_unit_id = ? AND biz_step = 'retail_selling'`,
		seed.DemoSerialID("UNIT", 0)); n != 1 {
		t.Fatal("event penjualan serial tidak tercatat")
	}

	// Serial yang sudah SOLD dijual lagi (kloning label?) → konflik already_sold.
	y := e.newTxn(2, line(sku1ID, 1, priceSKU1, codes[0]))
	r = e.sync(signed(t, e.key, y)).Results[0]
	d = r.detail(t)
	if r.Status != service.PosStatusAcceptedWithConflict || len(d.Conflicts) != 1 || d.Conflicts[0].Reason != "already_sold" {
		t.Fatalf("serial dijual ulang: %s %s", r.Status, r.Detail)
	}
	// Karton (bukan unit) dan serial produk lain.
	z := e.newTxn(3, line(sku2ID, 2, priceSKU2, seed.DemoSerialCode("CARTON", 3), codes[2]))
	d = e.sync(signed(t, e.key, z)).Results[0].detail(t)
	reasons := []string{d.Conflicts[0].Reason, d.Conflicts[1].Reason}
	slices.Sort(reasons)
	if !slices.Equal(reasons, []string{"not_unit", "product_mismatch"}) {
		t.Fatalf("alasan %v", reasons)
	}
	if s := e.str(`SELECT status FROM serial_units WHERE code = ?`, codes[2]); s != "IN_STOCK" {
		t.Fatal("serial produk lain tidak boleh dijual di line produk berbeda")
	}
	e.assertLedgerMatchesBalances()
}

func TestPosSync_FlagsAndValidation(t *testing.T) {
	e := newEnv(t)
	e.receive(jakselID, sku2ID, 100)

	stale := e.newTxn(1, line(sku2ID, 1, 32000))
	stale["price_list_version"] = 1
	stale["total"], stale["payments"] = int64(32000), []any{cash(32000)}

	old := e.newTxn(2, line(sku2ID, 1, priceSKU2))
	old["occurred_at"] = e.clk.Now().Add(-25 * time.Hour).Format(time.RFC3339)

	unicode := e.newTxn(3, line(sku2ID, 1, priceSKU2))
	unicode["note"] = "Pembeli Ñoño 🇮🇩 漢字\nbaris kedua"
	unicode["payments"] = []any{cash(50000)} // kembalian 17.000

	nul := e.newTxn(4, line(sku2ID, 1, priceSKU2))
	nul["note"] = "ada\u0000nul"

	short := e.newTxn(5, line(sku2ID, 2, priceSKU2))
	short["payments"] = []any{cash(priceSKU2)}

	mismatch := e.newTxn(6, line(sku2ID, 1, priceSKU2))
	mismatch["total"] = int64(1)

	zero := e.newTxn(7, line(sku2ID, 0, priceSKU2))

	// Nilai di atas 2^31-1 (lesson: uang/qty di atas batas int32).
	big := e.newTxn(8, line(sku2ID, 2, 3_000_000_000))

	unknownProduct := e.newTxn(9, line(uuid.New(), 1, 1000))

	out := e.sync(
		signed(t, e.key, stale), signed(t, e.key, old), signed(t, e.key, unicode), signed(t, e.key, nul),
		signed(t, e.key, short), signed(t, e.key, mismatch), signed(t, e.key, zero), signed(t, e.key, big),
		signed(t, e.key, unknownProduct),
	)
	want := []string{"ACCEPTED", "ACCEPTED", "ACCEPTED", "REJECTED", "REJECTED", "REJECTED", "REJECTED", "ACCEPTED", "REJECTED"}
	if got := statuses(out.Results); !slices.Equal(got, want) {
		t.Fatalf("status %v", got)
	}
	// Harga v1 (32.000) ≠ harga berlaku v2 (33.000): basi sekaligus beda harga.
	if f := out.Results[0].detail(t).Flags; !slices.Equal(f, []string{"PRICE_MISMATCH", "PRICE_STALE"}) {
		t.Fatalf("harga basi: flags %v", f)
	}
	if f := out.Results[1].detail(t).Flags; !slices.Equal(f, []string{"OFFLINE_TOO_LONG"}) {
		t.Fatalf("offline > 24 jam: flags %v", f)
	}
	d := out.Results[2].detail(t)
	if d.Totals.ChangeDue != 17000 || len(d.Flags) != 0 {
		t.Fatalf("kembalian: %+v", d)
	}
	if note := e.str(`SELECT metadata->>'note' FROM orders WHERE id = ?`, *d.OrderID); note != "Pembeli Ñoño 🇮🇩 漢字\nbaris kedua" {
		t.Fatalf("catatan unicode berubah: %q", note)
	}
	if amt := e.count(`SELECT amount FROM payments WHERE order_id = ? AND method = 'CASH'`, *d.OrderID); amt != priceSKU2 {
		t.Fatalf("tunai dicatat bersih (dibayar − kembalian), dapat %d", amt)
	}
	reasons := []string{"invalid_characters", "payment_insufficient", "total_mismatch", "validation_failed"}
	for i, r := range out.Results[3:7] {
		if got := r.detail(t).Reason; got != reasons[i] {
			t.Fatalf("seq %d alasan %s, ingin %s", r.DeviceSeq, got, reasons[i])
		}
	}
	if got := out.Results[8].detail(t).Reason; got != service.PosReasonUnknownProduct {
		t.Fatalf("produk tidak dikenal: %s", got)
	}
	bigOrder := out.Results[7].detail(t).OrderID
	if total := e.count(`SELECT total FROM orders WHERE id = ?`, *bigOrder); total != 6_000_000_000 {
		t.Fatalf("total besar %d", total)
	}
	// Transaksi ditolak tidak memengaruhi stok: 100 + 12 − (1+1+1+2) terjual.
	if got := e.balance(jakselID, sku2ID); got.Physical != 112-5 {
		t.Fatalf("stok %d, ingin 107", got.Physical)
	}
	e.assertLedgerMatchesBalances()
}

func TestPosSync_ClockSkewAndDiscrepancy(t *testing.T) {
	e := newEnv(t)
	// Jam device mundur 2 jam: client_time dan occurred_at memakai jam device.
	deviceNow := e.clk.Now().Add(-2 * time.Hour)
	x := e.newTxn(1, line(sku2ID, 20, priceSKU2)) // stok hanya 12 → selisih 8
	x["occurred_at"] = deviceNow.Add(-time.Minute).Format(time.RFC3339)
	out := e.syncWith(e.token, map[string]any{"client_time": deviceNow, "transactions": []json.RawMessage{signed(t, e.key, x)}})
	r := out.Results[0]
	d := r.detail(t)
	if r.Status != service.PosStatusAcceptedWithConflict || !slices.Equal(d.Flags, []string{"CLOCK_SKEW"}) {
		t.Fatalf("skew: %s %s", r.Status, r.Detail)
	}
	if d.ClockSkewMs == nil || *d.ClockSkewMs != (2*time.Hour).Milliseconds() {
		t.Fatalf("clock_skew_ms %v", d.ClockSkewMs)
	}
	// Ledger memakai waktu terkoreksi (jam server − 1 menit), device apa adanya.
	if !d.OccurredAt.Equal(e.clk.Now().Add(-time.Minute)) {
		t.Fatalf("occurred terkoreksi %s", d.OccurredAt)
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions WHERE id = ? AND occurred_at = ? AND clock_skew_ms = ?`,
		d.ServerTxnID, deviceNow.Add(-time.Minute), (2 * time.Hour).Milliseconds()); n != 1 {
		t.Fatal("occurred_at device dan clock_skew_ms harus tersimpan apa adanya")
	}
	if len(d.Conflicts) != 1 || d.Conflicts[0].Type != service.PosConflictDiscrepancy || *d.Conflicts[0].Qty != 8 {
		t.Fatalf("selisih stok: %+v", d.Conflicts)
	}
	if got := e.balance(jakselID, sku2ID); got.Physical != 0 {
		t.Fatalf("stok setelah selisih %+v", got)
	}
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'STOCK_DISCREPANCY'`); n != 1 {
		t.Fatal("anomali selisih stok tidak tercatat")
	}
	e.assertLedgerMatchesBalances()
}

func TestPosSync_PaymentsAndApprovalRef(t *testing.T) {
	e := newEnv(t)
	qris := func(seq int64, ref string) txn {
		x := e.newTxn(seq, line(sku2ID, 1, priceSKU2))
		x["payments"] = []any{map[string]any{"method": "QRIS", "amount": priceSKU2, "approval_ref": ref}}
		return x
	}
	split := e.newTxn(3, line(sku2ID, 2, priceSKU2))
	split["payments"] = []any{
		map[string]any{"method": "EDC", "amount": int64(50000), "approval_ref": "EDC-778899"},
		cash(20000), // 66.000 total: 50.000 kartu + 20.000 tunai, kembalian 4.000
	}
	dupInTxn := e.newTxn(4, line(sku2ID, 1, priceSKU2))
	dupInTxn["payments"] = []any{
		map[string]any{"method": "QRIS", "amount": int64(20000), "approval_ref": "Q-1"},
		map[string]any{"method": "QRIS", "amount": int64(13000), "approval_ref": "Q-1"},
	}
	overCard := e.newTxn(5, line(sku2ID, 1, priceSKU2))
	overCard["payments"] = []any{map[string]any{"method": "EDC", "amount": int64(40000), "approval_ref": "EDC-1"}}

	out := e.sync(signed(t, e.key, qris(1, "QR-REF-001")), signed(t, e.key, qris(2, "QR-REF-001")),
		signed(t, e.key, split), signed(t, e.key, dupInTxn), signed(t, e.key, overCard))
	if got := statuses(out.Results); !slices.Equal(got, []string{"ACCEPTED", "ACCEPTED_WITH_CONFLICT", "ACCEPTED", "REJECTED", "REJECTED"}) {
		t.Fatalf("status %v", got)
	}
	d := out.Results[1].detail(t)
	if d.Conflicts[0].Type != service.PosConflictPaymentDuplicate || d.Conflicts[0].AnomalyRule != "POS_PAYMENT_REF_REUSED" {
		t.Fatalf("approval_ref dobel: %+v", d.Conflicts)
	}
	var p model.Payment
	e.must(e.db.Where("order_id = ?", *d.OrderID).Take(&p).Error)
	if p.Status != "PENDING" || p.ProviderRef == "QR-REF-001" {
		t.Fatalf("pembayaran duplikat harus PENDING dengan ref turunan: %+v", p)
	}
	if n := e.count(`SELECT count(*) FROM payments WHERE provider = 'QRIS' AND provider_ref = 'QR-REF-001'`); n != 1 {
		t.Fatal("(provider, provider_ref) asli harus tetap satu")
	}
	if n := e.count(`SELECT count(*) FROM anomalies WHERE rule_code = 'POS_PAYMENT_REF_REUSED'`); n != 1 {
		t.Fatal("anomali approval dobel tidak tercatat")
	}
	d = out.Results[2].detail(t)
	if d.Totals.ChangeDue != 4000 || e.count(`SELECT sum(amount) FROM payments WHERE order_id = ?`, *d.OrderID) != 66000 {
		t.Fatalf("pembayaran campuran: %+v", d.Totals)
	}
}

func TestPosSync_InvalidPayloadShapes(t *testing.T) {
	e := newEnv(t)
	x := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	x["unexpected"] = "field asing ikut ditandatangani"
	y := e.newTxn(2, line(sku2ID, 1, priceSKU2))
	y["lines"] = "bukan array"
	z := json.RawMessage(fmt.Sprintf(`{"client_txn_id":%q,"device_seq":3,"total":1.5}`, uuid.NewString()))
	out := e.sync(signed(t, e.key, x), signed(t, e.key, y), z)
	for i, r := range out.Results {
		if r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonInvalidPayload {
			if i == 2 {
				t.Fatalf("angka pecahan: %s %s", r.Status, r.Detail)
			}
			t.Fatalf("seq %d: %s %s", r.DeviceSeq, r.Status, r.Detail)
		}
	}
	// Bersignature sah (x, y) disimpan dan memajukan ack; z tidak bisa
	// dikanonisasi sehingga signature tidak bisa diverifikasi → tidak disimpan.
	if out.AckUpToSeq != 2 || !slices.Equal(out.MissingSeqs, []int64{}) {
		t.Fatalf("ack %d missing %v", out.AckUpToSeq, out.MissingSeqs)
	}
	if n := e.count(`SELECT count(*) FROM pos_transactions WHERE device_seq = 3`); n != 0 {
		t.Fatal("payload tanpa signature terverifikasi tidak boleh disimpan")
	}
}

// F8a: satu item dengan identitas rusak tidak menolak seluruh batch; id/seq
// ganda di dalam batch ditangani database (DUPLICATE / device_seq_reused).
func TestPosSync_ItemLevelIdentityErrors(t *testing.T) {
	e := newEnv(t)
	good := e.newTxn(1, line(sku2ID, 1, priceSKU2))
	goodRaw := signed(t, e.key, good)
	other := e.newTxn(2, line(sku2ID, 1, priceSKU2))
	otherSameSeq := e.newTxn(2, line(sku2ID, 3, priceSKU2))
	raws := []json.RawMessage{
		json.RawMessage(`{"client_txn_id":"abc","device_seq":5}`),
		goodRaw,
		json.RawMessage(`5`),
		json.RawMessage(fmt.Sprintf(`{"client_txn_id":%q,"device_seq":1.5}`, uuid.NewString())),
		goodRaw, // id ganda di batch
		signed(t, e.key, other),
		signed(t, e.key, otherSameSeq), // seq ganda di batch
	}
	out := e.sync(raws...)
	if len(out.Results) != len(raws) {
		t.Fatalf("hasil %d, ingin %d", len(out.Results), len(raws))
	}
	byIndex := map[int]syncResult{}
	for _, r := range out.Results {
		byIndex[r.Index] = r
	}
	for _, i := range []int{0, 2, 3} {
		r := byIndex[i]
		if r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonInvalidPayload {
			t.Fatalf("item %d: %s %s", i, r.Status, r.Detail)
		}
	}
	if byIndex[1].Status != service.PosStatusAccepted || byIndex[4].Status != service.PosStatusDuplicate ||
		byIndex[5].Status != service.PosStatusAccepted {
		t.Fatalf("item sah: %s %s %s", byIndex[1].Status, byIndex[4].Status, byIndex[5].Status)
	}
	if r := byIndex[6]; r.Status != service.PosStatusRejected || r.detail(t).Reason != service.PosReasonDeviceSeqReused {
		t.Fatalf("seq ganda: %s %s", r.Status, r.Detail)
	}
	if out.AckUpToSeq != 2 || e.count(`SELECT count(*) FROM pos_transactions`) != 2 {
		t.Fatalf("ack %d", out.AckUpToSeq)
	}
	if got := e.balance(jakselID, sku2ID); got.Physical != 10 {
		t.Fatalf("stok %d, ingin 10", got.Physical)
	}
}
