package seed_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"gorm.io/gorm"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/seed"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

const testPassword = "kata-sandi-demo-test"

var testPepper = []byte("pepper-test-32-byte-xxxxxxxxxxxx")

// Parameter argon2 kecil supaya test cepat; satu test memakai parameter OWASP.
var fastParams = domain.Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

func options(log *slog.Logger) seed.Options {
	return seed.Options{Enabled: true, Pepper: testPepper, DemoPassword: testPassword, PasswordParams: fastParams, Log: log}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

func seeded(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.NewDB(t)
	if _, err := seed.Run(context.Background(), db, options(quietLog())); err != nil {
		t.Fatal(err)
	}
	return db
}

func count(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("%v\nquery: %s", err, query)
	}
	return n
}

// tableDigests mengembalikan "jumlah:md5(isi)" setiap tabel publik. Isi tiap
// baris (termasuk created_at/updated_at) ikut di-hash, jadi perubahan nilai
// apa pun, bukan hanya jumlah baris, terdeteksi.
func tableDigests(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	var tables []string
	if err := db.Raw(`SELECT table_name FROM information_schema.tables
	                   WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY 1`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("tidak ada tabel")
	}
	out := map[string]string{}
	for _, tbl := range tables {
		var digest string
		q := `SELECT count(*)::text || ':' || md5(coalesce(string_agg(t::text, E'\n' ORDER BY t::text), '')) FROM "` + tbl + `" t`
		if err := db.Raw(q).Scan(&digest).Error; err != nil {
			t.Fatalf("digest %s: %v", tbl, err)
		}
		out[tbl] = digest
	}
	return out
}

func requireSameDigests(t *testing.T, before, after map[string]string) {
	t.Helper()
	for tbl, d := range before {
		if after[tbl] != d {
			t.Errorf("isi %s berubah: %s → %s", tbl, d, after[tbl])
		}
	}
}

func scanString(t *testing.T, db *gorm.DB, query string, args ...any) string {
	t.Helper()
	var s string
	if err := db.Raw(query, args...).Scan(&s).Error; err != nil {
		t.Fatalf("%v\nquery: %s", err, query)
	}
	return s
}

func TestRun_RefusesWithoutSeedDemo(t *testing.T) {
	db := testutil.NewDB(t)
	before := tableDigests(t, db)
	opts := options(quietLog())
	opts.Enabled = false
	if _, err := seed.Run(context.Background(), db, opts); !errors.Is(err, seed.ErrDisabled) {
		t.Fatalf("ingin ErrDisabled, dapat %v", err)
	}
	requireSameDigests(t, before, tableDigests(t, db))
}

func TestRun_RejectsMissingSecrets(t *testing.T) {
	db := testutil.NewDB(t)
	opts := options(quietLog())
	opts.Pepper = nil
	if _, err := seed.Run(context.Background(), db, opts); err == nil {
		t.Fatal("seed tanpa pepper seharusnya gagal")
	}
	opts = options(quietLog())
	opts.DemoPassword = ""
	if _, err := seed.Run(context.Background(), db, opts); err == nil {
		t.Fatal("seed tanpa password demo seharusnya gagal")
	}
}

// TestRun_Idempotent: seed dua kali → isi setiap tabel identik (jumlah dan
// nilai), tanpa error, dan password/hash tidak diganti di run kedua.
func TestRun_Idempotent(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	opts := options(quietLog())
	opts.PasswordParams = domain.OWASPArgon2id // jalur default produksi

	first, err := seed.Run(ctx, db, opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Total() == 0 {
		t.Fatal("seed pertama tidak menulis apa pun")
	}
	digestsAfterFirst := tableDigests(t, db)
	hashBefore := scanString(t, db, `SELECT password_hash FROM users WHERE email = 'admin@mganik.test'`)
	if !strings.HasPrefix(hashBefore, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash bukan argon2id OWASP: %s", hashBefore)
	}

	second, err := seed.Run(ctx, db, opts)
	if err != nil {
		t.Fatalf("seed kedua: %v", err)
	}
	if second.Total() != 0 || second.ClaimHashesUpdated != 0 {
		t.Fatalf("seed kedua menulis %d baris baru, %d hash: %v", second.Total(), second.ClaimHashesUpdated, second.Inserted)
	}
	requireSameDigests(t, digestsAfterFirst, tableDigests(t, db))
	if hashAfter := scanString(t, db, `SELECT password_hash FROM users WHERE email = 'admin@mganik.test'`); hashAfter != hashBefore {
		t.Fatal("seed kedua mengganti password hash")
	}

	// Jumlah kunci fixture.
	for q, want := range map[string]int64{
		`SELECT count(*) FROM serial_units WHERE level_code = 'UNIT'`:   seed.DemoUnitCount + 1, // + unit toko
		`SELECT count(*) FROM serial_units WHERE level_code = 'CARTON'`: 4,
		`SELECT count(*) FROM serial_units WHERE level_code = 'PALLET'`: 1,
		`SELECT count(*) FROM products`:                                 2,
		`SELECT count(*) FROM users`:                                    int64(len(seed.DemoAccounts)),
		`SELECT count(*) FROM pos_devices`:                              1,
		`SELECT count(*) FROM regions WHERE level = 'PROVINCE'`:         7,
		`SELECT count(*) FROM regions WHERE level <> 'PROVINCE'`:        12,
	} {
		if got := count(t, db, q); got != want {
			t.Errorf("%s = %d, ingin %d", q, got, want)
		}
	}
}

func TestSeed_LedgerMatchesBalances(t *testing.T) {
	db := seeded(t)
	// Setiap (lokasi, produk) di saldo maupun ledger: SUM(delta) = saldo per bucket.
	drift := count(t, db, `
		WITH ledger AS (
		  SELECT location_id, product_id, sum(physical_delta) AS p, sum(reserved_delta) AS r, sum(quarantine_delta) AS q
		    FROM inventory_movements GROUP BY 1, 2)
		SELECT count(*) FROM inventory_balances b
		  FULL JOIN ledger l USING (location_id, product_id)
		 WHERE coalesce(b.physical_qty, 0)   <> coalesce(l.p, 0)
		    OR coalesce(b.reserved_qty, 0)   <> coalesce(l.r, 0)
		    OR coalesce(b.quarantine_qty, 0) <> coalesce(l.q, 0)`)
	if drift != 0 {
		t.Fatalf("%d (lokasi, produk) dengan ledger tidak sama dengan saldo", drift)
	}
	if n := count(t, db, `SELECT count(*) FROM inventory_balances WHERE physical_qty > 0`); n < 6 {
		t.Fatalf("hanya %d saldo terisi", n)
	}
	// Ledger seed hanya berisi RECEIPT (saldo awal + stok masuk sebelum
	// riwayat penjualan) dan DIRECT_SALE riwayat penjualan demo untuk DOI/runout
	// Command Center; tidak ada tipe mutasi lain.
	if n := count(t, db, `SELECT count(*) FROM inventory_movements
	                       WHERE movement_type <> 'RECEIPT'
	                         AND NOT (movement_type = 'DIRECT_SALE' AND ref_type = 'SEED_SALES_HISTORY')`); n != 0 {
		t.Fatalf("seed hanya boleh menulis RECEIPT dan riwayat DIRECT_SALE demo, ada %d mutasi lain", n)
	}
	// Saldo serial di gudang pusat = jumlah unit serial IN_STOCK di sana.
	units := count(t, db, `SELECT count(*) FROM serial_units WHERE level_code = 'UNIT' AND status = 'IN_STOCK' AND location_id = ?`, seed.LocationID(seed.CentralWHCode))
	bal := count(t, db, `SELECT physical_qty FROM inventory_balances WHERE location_id = ? AND product_id = ?`, seed.LocationID(seed.CentralWHCode), seed.ProductID(seed.SKU1))
	if units != bal || bal != seed.DemoUnitCount {
		t.Fatalf("unit serial %d, saldo %d, ingin %d", units, bal, seed.DemoUnitCount)
	}
	// Kasus 4: satu SKU di toko Jakarta Selatan dengan stok tepat 1 dan buffer online 0.
	if n := count(t, db, `SELECT count(*) FROM inventory_balances b JOIN stock_policies p USING (location_id, product_id)
	                       WHERE b.location_id = ? AND b.available_qty = 1 AND p.online_buffer_qty = 0`, seed.LocationID(seed.StoreJakselCode)); n != 1 {
		t.Fatal("tidak ada SKU stok 1 untuk demo Kasus 4")
	}
	if n := count(t, db, `SELECT count(*) FROM stock_policies sp JOIN locations l ON l.id = sp.location_id WHERE l.type_code = 'OWN_STORE'`); n != 4 {
		t.Fatalf("stock_policies toko %d, ingin 4", n)
	}
	// Gudang pusat juga punya kebijakan stok (demo DOI gudang) untuk kedua SKU.
	if n := count(t, db, `SELECT count(*) FROM stock_policies WHERE location_id = ? AND lead_time_days > 0`, seed.LocationID(seed.CentralWHCode)); n != 2 {
		t.Fatalf("stock_policies gudang pusat %d, ingin 2", n)
	}
}

func TestSeed_PackagingHierarchy(t *testing.T) {
	db := seeded(t)
	// Unit toko (DemoStoreUnit) sengaja lepas di luar palet; dicek di TestSeed_StoreUnit.
	storeID := seed.DemoStoreUnit().ID.String()
	checks := map[string]string{
		"unit tanpa karton induk": `SELECT count(*) FROM serial_units u LEFT JOIN serial_units p ON p.id = u.parent_id
			WHERE u.level_code = 'UNIT' AND (p.id IS NULL OR p.level_code <> 'CARTON') AND u.id <> '` + storeID + `'`,
		"karton tanpa palet induk": `SELECT count(*) FROM serial_units c LEFT JOIN serial_units p ON p.id = c.parent_id
			WHERE c.level_code = 'CARTON' AND (p.id IS NULL OR p.level_code <> 'PALLET')`,
		"palet punya induk": `SELECT count(*) FROM serial_units WHERE level_code = 'PALLET' AND parent_id IS NOT NULL`,
		"karton tidak berisi 12 unit": `SELECT count(*) FROM serial_units c
			WHERE c.level_code = 'CARTON' AND (SELECT count(*) FROM serial_units u WHERE u.parent_id = c.id) <> 12`,
		"palet tidak berisi 4 karton": `SELECT count(*) FROM serial_units p
			WHERE p.level_code = 'PALLET' AND (SELECT count(*) FROM serial_units c WHERE c.parent_id = p.id) <> 4`,
		"serial tidak IN_STOCK di gudang pusat": `SELECT count(*) FROM serial_units WHERE (status <> 'IN_STOCK' OR location_id <> '` +
			seed.LocationID(seed.CentralWHCode).String() + `' OR batch_id IS NULL) AND id <> '` + storeID + `'`,
		// Riwayat event hanya memakai transisi yang ada di state machine.
		"event dengan transisi tidak terdaftar": `SELECT count(*) FROM unit_events e
			WHERE e.to_status IS NOT NULL AND NOT EXISTS (SELECT 1 FROM lifecycle_transitions lt
			  WHERE lt.entity_type = 'SERIAL_UNIT' AND lt.from_code = e.from_status AND lt.to_code = e.to_status)`,
		"status serial tidak sama dengan event terakhir": `SELECT count(*) FROM serial_units s
			WHERE s.status <> (SELECT e.to_status FROM unit_events e WHERE e.serial_unit_id = s.id AND e.to_status IS NOT NULL
			                   ORDER BY e.occurred_at DESC LIMIT 1)`,
	}
	for name, q := range checks {
		if n := count(t, db, q); n != 0 {
			t.Errorf("%s: %d baris", name, n)
		}
	}
}

// Unit terakhir SKU1 di Toko Jakarta Selatan (Kasus 4) adalah unit serial lepas
// yang bisa dipindai kasir: saldo toko = jumlah serial IN_STOCK di toko, dan
// riwayatnya berakhir di toko dengan custodian party toko.
func TestSeed_StoreUnit(t *testing.T) {
	db := seeded(t)
	store := seed.DemoStoreUnit()
	jaksel, sku1 := seed.LocationID(seed.StoreJakselCode), seed.ProductID(seed.SKU1)

	var su model.SerialUnit
	if err := db.First(&su, "id = ?", store.ID).Error; err != nil {
		t.Fatal(err)
	}
	if su.Code != store.Code || su.LevelCode != "UNIT" || su.Status != "IN_STOCK" || su.ParentID != nil ||
		su.LocationID == nil || *su.LocationID != jaksel || su.BatchID == nil || *su.BatchID != seed.BatchID() ||
		su.ProductID == nil || *su.ProductID != sku1 {
		t.Fatalf("unit toko: %+v", su)
	}
	if party := scanString(t, db, `SELECT party_id::text FROM locations WHERE id = ?`, jaksel); su.CustodianPartyID == nil || su.CustodianPartyID.String() != party {
		t.Fatalf("custodian unit toko %v, ingin party toko %s", su.CustodianPartyID, party)
	}
	if !domain.VerifyClaimPIN(testPepper, store.ID, store.PIN, su.ClaimSecretHash) {
		t.Fatal("PIN klaim unit toko tidak terverifikasi")
	}

	serials := count(t, db, `SELECT count(*) FROM serial_units WHERE level_code = 'UNIT' AND status = 'IN_STOCK'
	                          AND location_id = ? AND product_id = ?`, jaksel, sku1)
	bal := count(t, db, `SELECT physical_qty FROM inventory_balances WHERE location_id = ? AND product_id = ?`, jaksel, sku1)
	if serials != 1 || bal != 1 {
		t.Fatalf("toko Jakarta Selatan: %d serial SKU1, saldo %d, ingin 1 dan 1", serials, bal)
	}
	// Event terakhir yang mengubah status terjadi di toko, dengan custodian yang sama.
	if n := count(t, db, `SELECT count(*) FROM (
	                        SELECT location_id, party_id FROM unit_events WHERE serial_unit_id = ? AND to_status IS NOT NULL
	                         ORDER BY occurred_at DESC LIMIT 1) e
	                       WHERE e.location_id = ? AND e.party_id = ?`, store.ID, jaksel, *su.CustodianPartyID); n != 1 {
		t.Fatal("event terakhir unit toko bukan penerimaan di toko")
	}
	// Code batch UNIT palet tetap berisi tepat 48 kode; kode unit toko di code batch sendiri.
	for name, want := range map[string]int64{
		"code_batch:UNIT":                         seed.DemoUnitCount,
		"code_batch:UNIT:" + seed.StoreJakselCode: 1,
	} {
		if n := count(t, db, `SELECT count(*) FROM serial_units s JOIN code_batches cb ON cb.id = s.code_batch_id
		                       WHERE cb.id = ? AND cb.qty = ?`, seed.ID(name), want); n != want {
			t.Errorf("%s: %d kode, ingin %d (sama dengan qty)", name, n, want)
		}
	}
}

// Database yang di-seed sebelum unit toko punya serial (saldo toko 1 tanpa
// serial) mendapat unit toko saat seed ulang, tanpa mengubah saldo maupun ledger.
func TestRun_AddsStoreUnitToOlderSeed(t *testing.T) {
	db := seeded(t)
	store := seed.DemoStoreUnit()
	// Buang jejak unit toko untuk meniru database versi lama. unit_events
	// append-only, jadi trigger dimatikan hanya di transaksi simulasi ini.
	err := db.Transaction(func(tx *gorm.DB) error {
		for _, q := range []string{
			`SET LOCAL session_replication_role = replica`,
			`DELETE FROM unit_events WHERE serial_unit_id = '` + store.ID.String() + `'`,
			`DELETE FROM serial_units WHERE id = '` + store.ID.String() + `'`,
			`DELETE FROM code_batches WHERE id = '` + seed.ID("code_batch:UNIT:"+seed.StoreJakselCode).String() + `'`,
		} {
			if err := tx.Exec(q).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := tableDigests(t, db)

	res, err := seed.Run(context.Background(), db, options(quietLog()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted["serial_units"] != 1 || res.Inserted["code_batches"] != 1 {
		t.Fatalf("baris baru %v, ingin 1 serial dan 1 code batch", res.Inserted)
	}
	// Jumlah event dicek di tabel, bukan dari Result.Inserted: untuk INSERT ...
	// ON CONFLICT DO NOTHING RETURNING, GORM menghitung seluruh batch sebagai
	// baru begitu ada satu baris masuk kalau kolom ber-default (recorded_at,
	// payload) diisi eksplisit.
	if n := count(t, db, `SELECT count(*) FROM unit_events WHERE serial_unit_id = ?`, store.ID); n != 2 {
		t.Fatalf("event unit toko %d, ingin 2 (commission, diterima toko)", n)
	}
	after := tableDigests(t, db)
	for _, tbl := range []string{"inventory_balances", "inventory_movements"} {
		if after[tbl] != before[tbl] {
			t.Errorf("isi %s berubah saat unit toko ditambahkan", tbl)
		}
	}
	if s := scanString(t, db, `SELECT status FROM serial_units WHERE id = ? AND location_id = ?`,
		store.ID, seed.LocationID(seed.StoreJakselCode)); s != "IN_STOCK" {
		t.Fatalf("unit toko setelah seed ulang: %q", s)
	}
}

func TestSeed_DeterministicCodesAndClaimHash(t *testing.T) {
	db := seeded(t)
	var units []model.SerialUnit
	if err := db.Where("level_code = 'UNIT'").Find(&units).Error; err != nil {
		t.Fatal(err)
	}
	byID := map[string]model.SerialUnit{}
	for _, u := range units {
		byID[u.ID.String()] = u
	}
	seen := map[string]bool{}
	for _, want := range seed.DemoUnits() {
		got, ok := byID[want.ID.String()]
		if !ok {
			t.Fatalf("unit %d tidak ada", want.Index)
		}
		if got.Code != want.Code || !domain.ValidSerialCode(got.Code) {
			t.Fatalf("unit %d kode %s, ingin %s", want.Index, got.Code, want.Code)
		}
		if len(want.PIN) != domain.ClaimPINLen || seen[want.PIN] {
			t.Fatalf("PIN unit %d tidak valid atau duplikat", want.Index)
		}
		seen[want.PIN] = true
		if !domain.VerifyClaimPIN(testPepper, got.ID, want.PIN, got.ClaimSecretHash) {
			t.Fatalf("claim hash unit %d tidak cocok dengan PIN deterministik", want.Index)
		}
		// PIN unit lain atau pepper lain tidak cocok.
		other := seed.DemoClaimPIN((want.Index + 1) % seed.DemoUnitCount)
		if domain.VerifyClaimPIN(testPepper, got.ID, other, got.ClaimSecretHash) {
			t.Fatalf("PIN unit lain cocok untuk unit %d", want.Index)
		}
		if domain.VerifyClaimPIN([]byte("pepper-lain-pepper-lain-pepper-lain"), got.ID, want.PIN, got.ClaimSecretHash) {
			t.Fatal("pepper berbeda tetap cocok")
		}
	}
	// Karton/palet tidak punya PIN klaim.
	if n := count(t, db, `SELECT count(*) FROM serial_units WHERE level_code <> 'UNIT' AND claim_secret_hash IS NOT NULL`); n != 0 {
		t.Fatalf("%d karton/palet punya claim hash", n)
	}
	// Fixture stabil antar pemanggilan (dipakai test dan skrip demo).
	if seed.DemoSerialCode("UNIT", 0) != seed.DemoUnits()[0].Code || seed.DemoClaimPIN(5) != seed.DemoClaimPIN(5) {
		t.Fatal("fixture tidak deterministik")
	}
}

func TestSeed_UsersRolesAndScopes(t *testing.T) {
	db := seeded(t)
	for _, a := range seed.DemoAccounts {
		var u model.User
		if err := db.First(&u, "id = ?", seed.UserID(a.Email)).Error; err != nil {
			t.Fatalf("akun %s: %v", a.Email, err)
		}
		ok, err := domain.VerifyPassword(testPassword, u.PasswordHash)
		if err != nil || !ok {
			t.Fatalf("password demo %s tidak cocok: %v", a.Email, err)
		}
		var ur model.UserRole
		if err := db.First(&ur, "user_id = ? AND role_code = ?", u.ID, a.Role).Error; err != nil {
			t.Fatalf("role %s untuk %s: %v", a.Role, a.Email, err)
		}
		if ur.ScopeMode != a.ScopeMode {
			t.Fatalf("%s scope %s, ingin %s", a.Email, ur.ScopeMode, a.ScopeMode)
		}
		locs := count(t, db, `SELECT count(*) FROM user_role_locations WHERE user_role_id = ?`, ur.ID)
		if ur.ScopeMode == model.ScopeSelected && locs == 0 {
			t.Fatalf("%s SELECTED tanpa lokasi", a.Email)
		}
		if ur.ScopeMode == model.ScopeAll && locs != 0 {
			t.Fatalf("%s ALL tetapi punya %d lokasi", a.Email, locs)
		}
		if a.Role == "CASHIER" {
			if u.PosPINHash == nil {
				t.Fatal("kasir tanpa pos_pin_hash")
			}
			if ok, _ := domain.VerifyPassword(seed.DemoCashierPIN, *u.PosPINHash); !ok {
				t.Fatal("PIN POS kasir tidak cocok")
			}
		} else if u.PosPINHash != nil {
			t.Fatalf("%s bukan kasir tetapi punya pos_pin_hash", a.Email)
		}
	}
	// Kasir dan kepala toko hanya di toko Jakarta Selatan; admin & ops ALL.
	for email, want := range map[string]string{"kasir.jaksel@mganik.test": seed.StoreJakselCode, "kepala.jaksel@mganik.test": seed.StoreJakselCode} {
		var code string
		if err := db.Raw(`SELECT l.code FROM user_roles ur JOIN user_role_locations url ON url.user_role_id = ur.id
		         JOIN locations l ON l.id = url.location_id WHERE ur.user_id = ?`, seed.UserID(email)).Scan(&code).Error; err != nil {
			t.Fatal(err)
		}
		if code != want {
			t.Fatalf("%s scope %q, ingin %s", email, code, want)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM user_roles WHERE role_code IN ('ADMIN','OPS_MANAGER') AND scope_mode = 'ALL'`); n != 2 {
		t.Fatalf("ADMIN/OPS dengan scope ALL: %d", n)
	}
}

func TestSeed_DistributionAndLocations(t *testing.T) {
	db := seeded(t)
	regionsOf := func(partyKey string) string {
		var codes []string
		if err := db.Raw(`SELECT tr.region_code FROM partner_territories pt
		         JOIN territory_regions tr ON tr.territory_id = pt.territory_id
		        WHERE pt.party_id = ? AND pt.coverage_mode = 'SELECTED' ORDER BY 1`, seed.ID(partyKey)).Scan(&codes).Error; err != nil {
			t.Fatal(err)
		}
		return strings.Join(codes, ",")
	}
	if got := regionsOf(seed.JatimPartyKey); got != "35.15,35.73,35.78" {
		t.Fatalf("wilayah Distributor Jatim %s", got)
	}
	if got := regionsOf(seed.SulselPartyKey); got != "73.06,73.71" {
		t.Fatalf("wilayah Distributor Sulsel %s", got)
	}
	if n := count(t, db, `SELECT count(*) FROM partner_territories WHERE party_id = ? AND coverage_mode = 'NATIONAL' AND territory_id IS NULL`, seed.ID(seed.CompanyPartyKey)); n != 1 {
		t.Fatal("pemilik brand harus NATIONAL eksplisit")
	}
	for code, typ := range map[string]string{
		seed.CentralWHCode: "CENTRAL_WH", seed.HubSurabayaCode: "HUB", seed.StoreJakselCode: "OWN_STORE",
		seed.StoreBandungCode: "OWN_STORE", seed.PartnerJatimCode: "PARTNER_WH", seed.PartnerSulselCode: "PARTNER_WH",
		seed.InTransitCode: "IN_TRANSIT",
	} {
		if n := count(t, db, `SELECT count(*) FROM locations WHERE code = ? AND type_code = ?`, code, typ); n != 1 {
			t.Errorf("lokasi %s bertipe %s tidak ada", code, typ)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM locations WHERE code = ? AND region_code = '32.75'`, seed.CentralWHCode); n != 1 {
		t.Error("gudang pusat harus di Kota Bekasi (32.75)")
	}
	// Setiap kota punya induk provinsi dan centroid.
	if n := count(t, db, `SELECT count(*) FROM regions r WHERE r.level <> 'PROVINCE' AND (r.centroid_lat IS NULL OR NOT EXISTS
	                       (SELECT 1 FROM regions p WHERE p.code = r.parent_code AND p.level = 'PROVINCE'))`); n != 0 {
		t.Errorf("%d kota tanpa provinsi/centroid", n)
	}
	// Price list per channel dengan versi.
	if n := count(t, db, `SELECT count(DISTINCT channel_code) FROM price_lists WHERE channel_code IN ('POS','D2C_WEB')`); n != 2 {
		t.Error("price list POS dan D2C_WEB wajib ada")
	}
	if n := count(t, db, `SELECT max(version) FROM price_lists WHERE channel_code = 'POS'`); n < 2 {
		t.Error("price list POS harus punya lebih dari satu versi")
	}
	attrs := scanString(t, db, `SELECT attributes::text FROM products WHERE sku = ?`, seed.SKU1)
	if !strings.Contains(attrs, `"bpom_no"`) || !strings.Contains(attrs, `"halal": true`) {
		t.Errorf("atribut produk %s", attrs)
	}
	spec := count(t, db, `SELECT qty_per_parent FROM packaging_specs WHERE product_id = ? AND child_level = 'UNIT' AND parent_level = 'CARTON'`, seed.ProductID(seed.SKU1))
	if spec != 12 {
		t.Errorf("unit per karton %d", spec)
	}
}

func TestSeed_PosDeviceTokenHashed(t *testing.T) {
	db := seeded(t)
	var dev model.PosDevice
	if err := db.First(&dev, "id = ?", seed.PosDeviceID()).Error; err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(seed.DemoDeviceToken()))
	if !bytes.Equal(dev.TokenHash, sum[:]) {
		t.Fatal("token_hash bukan SHA-256 token demo")
	}
	if dev.LocationID != seed.LocationID(seed.StoreJakselCode) || dev.Status != "ACTIVE" {
		t.Fatal("device tidak terpasang di toko Jakarta Selatan atau tidak aktif")
	}
	// Token plaintext tidak boleh tersimpan di kolom mana pun, di tabel mana pun.
	// row::text memuat semua kolom (bytea sebagai hex, jadi juga dicek dalam
	// bentuk hex).
	token := seed.DemoDeviceToken()
	tokenHex := hex.EncodeToString([]byte(token))
	var tables []string
	if err := db.Raw(`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE'`).Scan(&tables).Error; err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		if n := count(t, db, `SELECT count(*) FROM "`+tbl+`" r WHERE strpos(r::text, ?) > 0 OR strpos(r::text, ?) > 0`, token, tokenHex); n != 0 {
			t.Fatalf("token device plaintext tersimpan di %s", tbl)
		}
	}
}

// Seed ulang dengan pepper lain memperbaiki claim_secret_hash fixture demo,
// tanpa menyentuh kolom/tabel lain.
func TestRun_RehashesClaimsWhenPepperChanges(t *testing.T) {
	db := testutil.NewDB(t)
	ctx := context.Background()
	pepperA := []byte("pepper-A-xxxxxxxxxxxxxxxxxxxxxxxxxxx")
	pepperB := []byte("pepper-B-yyyyyyyyyyyyyyyyyyyyyyyyyyy")

	opts := options(quietLog())
	opts.Pepper = pepperA
	if _, err := seed.Run(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	before := tableDigests(t, db)

	opts.Pepper = pepperB
	res, err := seed.Run(ctx, db, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Semua unit demo: 48 unit palet + unit toko.
	units := append(seed.DemoUnits(), seed.DemoStoreUnit())
	if res.ClaimHashesUpdated != int64(len(units)) || res.Total() != 0 {
		t.Fatalf("hash diperbarui %d (ingin %d), baris baru %d (ingin 0)", res.ClaimHashesUpdated, len(units), res.Total())
	}
	for _, u := range units {
		var su model.SerialUnit
		if err := db.First(&su, "id = ?", u.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !domain.VerifyClaimPIN(pepperB, u.ID, u.PIN, su.ClaimSecretHash) {
			t.Fatalf("unit %d tidak terverifikasi dengan pepper baru", u.Index)
		}
		if domain.VerifyClaimPIN(pepperA, u.ID, u.PIN, su.ClaimSecretHash) {
			t.Fatalf("unit %d masih memakai hash pepper lama", u.Index)
		}
	}
	// Hanya serial_units yang berubah.
	after := tableDigests(t, db)
	for tbl, d := range before {
		if tbl != "serial_units" && after[tbl] != d {
			t.Errorf("isi %s berubah saat pepper diganti", tbl)
		}
	}
	// Pepper sama lagi: no-op.
	res, err = seed.Run(ctx, db, opts)
	if err != nil || res.ClaimHashesUpdated != 0 || res.Total() != 0 {
		t.Fatalf("seed ulang pepper sama: err=%v hash=%d baru=%d", err, res.ClaimHashesUpdated, res.Total())
	}
}

// TestSeed_LogsHaveNoSecrets: PIN, token, password tidak boleh muncul di log.
func TestSeed_LogsHaveNoSecrets(t *testing.T) {
	db := testutil.NewDB(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := seed.Run(context.Background(), db, options(log)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if out == "" {
		t.Fatal("seed tidak menulis log ringkasan")
	}
	secrets := []string{testPassword, string(testPepper), seed.DemoCashierPIN, seed.DemoDeviceToken()}
	for _, u := range seed.DemoUnits() {
		secrets = append(secrets, u.PIN)
	}
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Fatalf("log memuat rahasia demo: %q", s)
		}
	}
}

func TestPrintDemo(t *testing.T) {
	var buf bytes.Buffer
	if err := seed.PrintDemo(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, u := range seed.DemoUnits() {
		if !strings.Contains(out, u.Code) || !strings.Contains(out, u.PIN) {
			t.Fatalf("unit %d tidak tercetak", u.Index)
		}
	}
	for _, want := range []string{seed.DemoDeviceToken(), seed.DemoCashierPIN, "admin@mganik.test", "kasir.jaksel@mganik.test"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output tidak memuat %q", want)
		}
	}
	// Password dari env tidak pernah dicetak.
	if strings.Contains(out, testPassword) || strings.Contains(out, "mganik-demo-2026") {
		t.Fatal("print-demo mencetak password")
	}
}
