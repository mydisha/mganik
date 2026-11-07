package postest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/config"
	"mganik/api/internal/pii"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

type provisioned struct {
	Device struct {
		ID         uuid.UUID `json:"id"`
		LocationID uuid.UUID `json:"location_id"`
		Status     string    `json:"status"`
	} `json:"device"`
	Token      string `json:"token"`
	SigningKey string `json:"signing_key"`
}

// TestPosDevice_ProvisionSecretsOnce: token dan signing key hanya muncul di
// respons provisioning (no-store); DB menyimpan hash token dan key
// terenkripsi; device baru langsung bisa bootstrap dan sync bertanda tangan.
func TestPosDevice_ProvisionSecretsOnce(t *testing.T) {
	e := newEnv(t)
	mgr := e.staffEngine(staff(managerID, false, []uuid.UUID{jakselID}, service.PermPosDeviceManage))
	r := serve(mgr, http.MethodPost, "/api/v1/pos/devices", map[string]any{"location_id": jakselID, "name": "Kasir 2 – Jaksel ☕"}, nil)
	if r.Code != http.StatusCreated {
		t.Fatalf("provisioning %d: %s", r.Code, r.Body)
	}
	if cc := r.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q", cc)
	}
	var p provisioned
	r.decode(t, &p)
	key, err := hex.DecodeString(p.SigningKey)
	if err != nil || len(key) != 32 || !strings.HasPrefix(p.Token, "pos_") || len(p.Token) < 40 {
		t.Fatalf("rahasia tidak valid: token=%d key=%d", len(p.Token), len(key))
	}

	// DB: SHA-256 token, signing key terenkripsi (AAD = id device).
	var row struct {
		TokenHash     []byte
		SigningKeyEnc []byte
	}
	e.must(e.db.Raw(`SELECT token_hash, signing_key_enc FROM pos_devices WHERE id = ?`, p.Device.ID).Scan(&row).Error)
	sum := sha256.Sum256([]byte(p.Token))
	if !bytes.Equal(row.TokenHash, sum[:]) || bytes.Contains(row.SigningKeyEnc, key) {
		t.Fatal("token_hash bukan SHA-256 atau key tersimpan polos")
	}
	c, _ := pii.NewSigningKeyCipher([]byte(config.DemoPIIEncKey))
	if got, err := c.Open(row.SigningKeyEnc, p.Device.ID[:]); err != nil || !bytes.Equal(got, key) {
		t.Fatal("signing_key_enc tidak terbuka menjadi key yang dikembalikan")
	}
	// Token/key tidak tersimpan polos di tabel mana pun (termasuk audit).
	var tables []string
	e.must(e.db.Raw(`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`).Scan(&tables).Error)
	for _, tbl := range tables {
		if n := e.count(`SELECT count(*) FROM "`+tbl+`" x WHERE strpos(x::text, ?) > 0 OR strpos(x::text, ?) > 0`,
			p.Token, p.SigningKey); n != 0 {
			t.Fatalf("rahasia device tersimpan polos di %s", tbl)
		}
	}
	if n := e.count(`SELECT count(*) FROM audit_logs WHERE action = 'pos_device.provisioned' AND entity_id = ?`, p.Device.ID); n != 1 {
		t.Fatal("provisioning tidak diaudit")
	}

	// List tidak pernah memuat rahasia.
	list := serve(mgr, http.MethodGet, "/api/v1/pos/devices", nil, nil)
	if list.Code != http.StatusOK || bytes.Contains(list.Body, []byte(p.Token)) || bytes.Contains(list.Body, []byte(p.SigningKey)) ||
		!bytes.Contains(list.Body, []byte(p.Device.ID.String())) {
		t.Fatalf("list device: %d %s", list.Code, list.Body)
	}

	// Device baru bekerja: bootstrap + sync dengan key-nya sendiri.
	if b := e.device(http.MethodGet, "/api/v1/pos/bootstrap", p.Token, nil); b.Code != http.StatusOK {
		t.Fatalf("bootstrap device baru: %d %s", b.Code, b.Body)
	}
	out := e.syncWith(p.Token, map[string]any{"transactions": []json.RawMessage{signed(t, key, e.newTxn(1, line(sku2ID, 1, priceSKU2)))}})
	if out.Results[0].Status != service.PosStatusAccepted {
		t.Fatalf("sync device baru: %+v", out.Results[0])
	}
	// Signing key device lain (demo) ditolak untuk device ini.
	out = e.syncWith(p.Token, map[string]any{"transactions": []json.RawMessage{signed(t, e.key, e.newTxn(2, line(sku2ID, 1, priceSKU2)))}})
	if out.Results[0].Status != service.PosStatusRejected {
		t.Fatal("signature dengan key device lain harus ditolak")
	}
}

func TestPosDevice_ProvisionAuthorization(t *testing.T) {
	e := newEnv(t)
	jaksel := e.staffEngine(staff(managerID, false, []uuid.UUID{jakselID}, service.PermPosDeviceManage))
	ops := e.staffEngine(staff(opsID, true, nil, service.PermPosDeviceManage))
	noPerm := e.staffEngine(staff(kasirID, false, []uuid.UUID{jakselID}, service.PermPosSync))

	cases := []struct {
		name string
		h    http.Handler
		body map[string]any
		code int
		want string
	}{
		{"kepala Jaksel di Bandung", jaksel, map[string]any{"location_id": bandungID, "name": "X"}, 403, "forbidden"},
		{"tanpa permission", noPerm, map[string]any{"location_id": jakselID, "name": "X"}, 403, "forbidden"},
		{"bukan toko", ops, map[string]any{"location_id": centralID, "name": "X"}, 422, "location_not_store"},
		{"lokasi tidak ada", ops, map[string]any{"location_id": uuid.New(), "name": "X"}, 422, "location_not_found"},
		{"nama kosong", ops, map[string]any{"location_id": jakselID, "name": "  "}, 422, "validation_failed"},
		{"nama NUL", ops, map[string]any{"location_id": jakselID, "name": "a\u0000b"}, 422, "invalid_characters"},
		{"field asing", ops, map[string]any{"location_id": jakselID, "name": "X", "status": "ACTIVE"}, 400, "invalid_request"},
	}
	for _, c := range cases {
		r := serve(c.h, http.MethodPost, "/api/v1/pos/devices", c.body, nil)
		if r.Code != c.code || r.problem(t).Code != c.want {
			t.Errorf("%s: %d %s", c.name, r.Code, r.Body)
		}
	}
	if r := serve(e.staffEngine(nil), http.MethodPost, "/api/v1/pos/devices", map[string]any{"location_id": jakselID, "name": "X"}, nil); r.Code != 401 {
		t.Fatalf("anonim: %d", r.Code)
	}
	if n := e.count(`SELECT count(*) FROM pos_devices`); n != 1 {
		t.Fatalf("permintaan ditolak tidak boleh membuat device (%d)", n)
	}
	// Ops (ALL) boleh di Bandung; Bandung jadi dilacak (OFFLINE sampai heartbeat).
	e.exec(`UPDATE locations SET sync_state = 'NOT_TRACKED' WHERE id = ?`, bandungID)
	if r := serve(ops, http.MethodPost, "/api/v1/pos/devices", map[string]any{"location_id": bandungID, "name": "Kasir BDG"}, nil); r.Code != 201 {
		t.Fatalf("ops di Bandung: %d %s", r.Code, r.Body)
	}
	if s := e.str(`SELECT sync_state FROM locations WHERE id = ?`, bandungID); s != "OFFLINE" {
		t.Fatalf("lokasi dengan device pertama harus OFFLINE, dapat %s", s)
	}

	// List mengikuti scope permission: kepala Jaksel tidak melihat device Bandung.
	var page struct {
		Items []struct {
			LocationID uuid.UUID `json:"location_id"`
		} `json:"items"`
	}
	serve(jaksel, http.MethodGet, "/api/v1/pos/devices", nil, nil).decode(t, &page)
	if len(page.Items) != 1 || page.Items[0].LocationID != jakselID {
		t.Fatalf("list scope Jaksel: %+v", page.Items)
	}
	var paged struct {
		Items []struct {
			ID uuid.UUID `json:"id"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	serve(ops, http.MethodGet, "/api/v1/pos/devices?limit=1", nil, nil).decode(t, &paged)
	if len(paged.Items) != 1 || paged.NextCursor == nil {
		t.Fatalf("limit tidak dihormati: %+v", paged)
	}
	first := paged.Items[0].ID
	serve(ops, http.MethodGet, "/api/v1/pos/devices?limit=1&cursor="+*paged.NextCursor, nil, nil).decode(t, &paged)
	if len(paged.Items) != 1 || paged.Items[0].ID == first || paged.NextCursor != nil {
		t.Fatalf("halaman kedua: %+v", paged)
	}
	if r := serve(jaksel, http.MethodGet, "/api/v1/pos/devices?location_id="+bandungID.String(), nil, nil); r.Code != 403 {
		t.Fatalf("filter lokasi di luar scope: %d", r.Code)
	}

	// Revoke: di luar scope 403, tidak ada 404, dua kali idempotent.
	if r := serve(jaksel, http.MethodPost, "/api/v1/pos/devices/"+uuid.NewString()+"/revoke", nil, nil); r.Code != 404 {
		t.Fatalf("revoke device tidak ada: %d", r.Code)
	}
	bdgDevice := uuid.MustParse(e.str(`SELECT id::text FROM pos_devices WHERE location_id = ?`, bandungID))
	if r := serve(jaksel, http.MethodPost, "/api/v1/pos/devices/"+bdgDevice.String()+"/revoke", nil, nil); r.Code != 403 {
		t.Fatalf("revoke di luar scope: %d", r.Code)
	}
	for i := range 2 {
		r := serve(jaksel, http.MethodPost, "/api/v1/pos/devices/"+deviceID.String()+"/revoke", map[string]any{"reason": "Tablet hilang"}, nil)
		if r.Code != 200 || !bytes.Contains(r.Body, []byte(`"status":"REVOKED"`)) {
			t.Fatalf("revoke ke-%d: %d %s", i+1, r.Code, r.Body)
		}
	}
	if n := e.count(`SELECT count(*) FROM audit_logs WHERE action = 'pos_device.revoked' AND entity_id = ?`, deviceID); n != 1 {
		t.Fatalf("audit revoke %d, ingin 1 (revoke kedua no-op)", n)
	}
}

type bootstrapBody struct {
	Device    struct{ ID uuid.UUID } `json:"device"`
	PriceList *struct {
		Version int64 `json:"version"`
		Items   []struct {
			ProductID uuid.UUID `json:"product_id"`
			Price     int64     `json:"price"`
		} `json:"items"`
	} `json:"price_list"`
	Stock []struct {
		ProductID    uuid.UUID `json:"product_id"`
		AvailableQty int64     `json:"available_qty"`
	} `json:"stock"`
	Cashiers []struct {
		ID         uuid.UUID `json:"id"`
		PosPinHash string    `json:"pos_pin_hash"`
	} `json:"cashiers"`
	Products []struct{ ID uuid.UUID } `json:"products"`
	Serials  struct {
		Items      []struct{ Code string } `json:"items"`
		NextCursor *string                 `json:"next_cursor"`
	} `json:"serials"`
	Limits struct {
		OfflineLimitSeconds int64 `json:"offline_limit_seconds"`
		SyncMaxBatch        int   `json:"sync_max_batch"`
	} `json:"limits"`
}

func TestPosBootstrap(t *testing.T) {
	e := newEnv(t)
	// 2.003 serial UNIT di toko (satu SOLD) ditambah unit toko dari seed
	// (seed.DemoStoreUnit): 2.003 IN_STOCK, lebih dari satu halaman.
	e.exec(`
		INSERT INTO serial_units (id, code, level_code, product_id, batch_id, status, location_id)
		SELECT gen_random_uuid(), 'T' || lpad(g::text, 15, '0'), 'UNIT', ?, ?, 'IN_STOCK', ?
		  FROM generate_series(1, ?) g`, sku1ID, seed.BatchID(), jakselID, service.PosSerialPageSize+3)
	e.exec(`UPDATE serial_units SET status = 'SOLD' WHERE code = 'T000000000000001'`)

	r := e.device(http.MethodGet, "/api/v1/pos/bootstrap", e.token, nil)
	if r.Code != http.StatusOK || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("bootstrap %d %q: %s", r.Code, r.Header.Get("Cache-Control"), r.Body)
	}
	var b bootstrapBody
	r.decode(t, &b)
	if b.Device.ID != deviceID || b.PriceList == nil || b.PriceList.Version != 2 || len(b.PriceList.Items) != 2 {
		t.Fatalf("device/price list: %+v", b.PriceList)
	}
	if len(b.Products) != 2 || len(b.Stock) != 2 || b.Limits.OfflineLimitSeconds != 86400 || b.Limits.SyncMaxBatch != 500 {
		t.Fatalf("katalog/stok/limit: %d %d %+v", len(b.Products), len(b.Stock), b.Limits)
	}
	// Hanya kasir ber-scope pos.sync di Jaksel yang punya PIN (kepala toko tanpa PIN tidak ikut).
	if len(b.Cashiers) != 1 || b.Cashiers[0].ID != kasirID || !strings.HasPrefix(b.Cashiers[0].PosPinHash, "$argon2id$") {
		t.Fatalf("kasir: %+v", b.Cashiers)
	}
	if len(b.Serials.Items) != service.PosSerialPageSize || b.Serials.NextCursor == nil {
		t.Fatalf("halaman serial pertama %d, cursor %v", len(b.Serials.Items), b.Serials.NextCursor)
	}
	r = e.device(http.MethodGet, "/api/v1/pos/bootstrap/serials?cursor="+*b.Serials.NextCursor, e.token, nil)
	var next struct {
		Items      []struct{ Code string } `json:"items"`
		NextCursor *string                 `json:"next_cursor"`
	}
	r.decode(t, &next)
	if len(next.Items) != 3 || next.NextCursor != nil {
		t.Fatalf("halaman kedua: %d %v", len(next.Items), next.NextCursor)
	}
	var codes []string
	for _, s := range append(b.Serials.Items, next.Items...) {
		codes = append(codes, s.Code)
	}
	if !slices.IsSorted(codes) {
		t.Fatal("serial bootstrap tidak urut kode (cursor keyset)")
	}
	if slices.Contains(codes, "T000000000000001") {
		t.Fatal("serial SOLD tidak boleh ikut")
	}
	if !slices.Contains(codes, seed.DemoStoreUnit().Code) {
		t.Fatal("unit toko dari seed tidak ikut bootstrap")
	}
	if r := e.device(http.MethodGet, "/api/v1/pos/bootstrap/serials?cursor=rusak", e.token, nil); r.Code != 422 {
		t.Fatalf("cursor rusak: %d", r.Code)
	}
	// Staff tidak bisa bootstrap.
	if r := serve(e.staffEngine(staff(opsID, true, nil, service.PermPosSync)), http.MethodGet, "/api/v1/pos/bootstrap", nil, nil); r.Code != 403 {
		t.Fatalf("staff bootstrap: %d", r.Code)
	}
}

// TestPosHeartbeat_MarkOfflineThreshold: ambang heartbeat dengan clock.Fake.
func TestPosHeartbeat_MarkOfflineThreshold(t *testing.T) {
	e := newEnv(t)
	r := e.device(http.MethodPost, "/api/v1/pos/heartbeat", e.token,
		map[string]any{"client_time": e.clk.Now().Add(-3 * time.Second), "pending_count": 0})
	var hb struct {
		LocationSyncState string `json:"location_sync_state"`
		ClockSkewMs       *int64 `json:"clock_skew_ms"`
	}
	r.decode(t, &hb)
	if r.Code != 200 || hb.LocationSyncState != "ONLINE" || hb.ClockSkewMs == nil || *hb.ClockSkewMs != 3000 {
		t.Fatalf("heartbeat: %d %s", r.Code, r.Body)
	}
	if n := e.count(`SELECT count(*) FROM pos_devices WHERE id = ? AND last_seen_at = ?`, deviceID, testNow); n != 1 {
		t.Fatal("last_seen_at tidak diperbarui")
	}
	ctx := context.Background()
	timeout := e.pos.Config().HeartbeatTimeout
	// Toko Bandung seed OFFLINE tanpa device ACTIVE → langsung NOT_TRACKED.
	if n, err := e.pos.MarkStaleLocationsOffline(ctx); err != nil || n != 1 {
		t.Fatalf("lokasi tanpa device: n=%d err=%v", n, err)
	}
	if s := e.str(`SELECT sync_state FROM locations WHERE id = ?`, bandungID); s != "NOT_TRACKED" {
		t.Fatalf("Bandung %s", s)
	}

	e.clk.Advance(timeout - time.Second)
	if n, err := e.pos.MarkStaleLocationsOffline(ctx); err != nil || n != 0 {
		t.Fatalf("sebelum ambang: n=%d err=%v", n, err)
	}
	e.clk.Advance(2 * time.Second)
	if n, err := e.pos.MarkStaleLocationsOffline(ctx); err != nil || n != 1 {
		t.Fatalf("lewat ambang: n=%d err=%v", n, err)
	}
	if s := e.str(`SELECT sync_state FROM locations WHERE id = ?`, jakselID); s != "OFFLINE" {
		t.Fatal("toko harus OFFLINE")
	}
	// Lokasi tanpa POS (NOT_TRACKED) tidak pernah disentuh.
	if s := e.str(`SELECT sync_state FROM locations WHERE id = ?`, centralID); s != "NOT_TRACKED" {
		t.Fatal("gudang pusat tidak boleh berubah")
	}
	// Heartbeat terlambat (jam mundur) tidak memundurkan last_heartbeat_at.
	e.heartbeat()
	latest := e.clk.Now()
	e.clk.Advance(-time.Minute)
	e.heartbeat()
	if n := e.count(`SELECT count(*) FROM locations WHERE id = ? AND last_heartbeat_at = ? AND sync_state = 'ONLINE'`, jakselID, latest); n != 1 {
		t.Fatal("last_heartbeat_at harus monoton")
	}
	// Semua device toko dicabut: setelah timeout toko kembali NOT_TRACKED,
	// bukan OFFLINE selamanya.
	e.exec(`UPDATE pos_devices SET status = 'REVOKED', revoked_at = now() WHERE location_id = ?`, jakselID)
	e.clk.Advance(2 * timeout)
	if n, err := e.pos.MarkStaleLocationsOffline(ctx); err != nil || n != 1 {
		t.Fatalf("toko tanpa device: n=%d err=%v", n, err)
	}
	if s := e.str(`SELECT sync_state FROM locations WHERE id = ?`, jakselID); s != "NOT_TRACKED" {
		t.Fatalf("toko tanpa device ACTIVE harus NOT_TRACKED, dapat %s", s)
	}
}

// TestPosHeartbeat_TwoJobInstances: dua instance job bersamaan dengan
// heartbeat berjalan → setiap lokasi basi ditandai tepat sekali, lokasi yang
// heartbeat-nya baru tetap ONLINE, tanpa error.
func TestPosHeartbeat_TwoJobInstances(t *testing.T) {
	e := newEnv(t)
	const stale = 40
	for i := range stale {
		e.exec(`INSERT INTO locations (id, code, name, type_code, sync_state, last_heartbeat_at)
		        VALUES (?, ?, ?, 'OWN_STORE', 'ONLINE', ?)`, uuid.New(), "T-STALE-"+uuidShort(i), "Toko basi", testNow.Add(-time.Hour))
	}
	e.heartbeat() // Jaksel segar
	e.clk.Advance(time.Minute)

	var wg sync.WaitGroup
	counts := make([]int, 2)
	errs := make([]error, 3)
	start := make(chan struct{})
	for w := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			counts[w], errs[w] = e.pos.MarkStaleLocationsOffline(context.Background())
		}()
	}
	wg.Add(1)
	go func() { // heartbeat bersamaan dari device demo
		defer wg.Done()
		<-start
		for range 10 {
			if r := e.device(http.MethodPost, "/api/v1/pos/heartbeat", e.token, map[string]any{"pending_count": 0}); r.Code != 200 {
				errs[2] = &httpErr{r.Code}
				return
			}
		}
	}()
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// +1: toko Bandung seed (OFFLINE tanpa device ACTIVE) kembali NOT_TRACKED.
	if counts[0]+counts[1] != stale+1 {
		t.Fatalf("lokasi diubah %d+%d, ingin tepat %d", counts[0], counts[1], stale+1)
	}
	// Lokasi uji tidak punya device ACTIVE → NOT_TRACKED (bukan OFFLINE selamanya).
	if n := e.count(`SELECT count(*) FROM locations WHERE code LIKE 'T-STALE-%' AND sync_state = 'NOT_TRACKED'`); n != stale {
		t.Fatalf("NOT_TRACKED %d", n)
	}
	if s := e.str(`SELECT sync_state FROM locations WHERE id = ?`, bandungID); s != "NOT_TRACKED" {
		t.Fatalf("Bandung tanpa device harus NOT_TRACKED, dapat %s", s)
	}
	if s := e.str(`SELECT sync_state FROM locations WHERE id = ?`, jakselID); s != "ONLINE" {
		t.Fatal("toko dengan heartbeat baru tidak boleh OFFLINE")
	}
}

type httpErr struct{ code int }

func (h *httpErr) Error() string { return http.StatusText(h.code) }

func uuidShort(i int) string { return fmt.Sprintf("%03d", i) }
