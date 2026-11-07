package serialtest

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/domain"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

// #7: SellSerials hanya untuk UNIT, dan unit dilepas dari kartonnya dalam
// transaksi yang sama (event unpacking ber-alasan sale).
func TestSellSerials_UnitOnlyAndDetachesFromCarton(t *testing.T) {
	f := newFx(t)
	ref := service.SaleRef{RefType: "POS_TRANSACTION", RefID: uuid.Must(uuid.NewV7())}
	_, err := f.serials.SellSerials(f.ctx, nil, []string{cartonCode(0)}, locCentral, ref)
	ae := requireCode(t, err, "serial_not_unit")
	if len(ae.Fields) != 1 || ae.Fields[0].Field != "codes[0]" {
		t.Fatalf("field %+v", ae.Fields)
	}
	// Campuran unit + palet: all-or-nothing.
	_, err = f.serials.SellSerials(f.ctx, nil, []string{unit(0).Code, palletCode()}, locCentral, ref)
	requireCode(t, err, "serial_not_unit")
	if s := f.status(unit(0).Code); s != domain.SerialInStock {
		t.Fatalf("status %s", s)
	}

	if _, err := f.serials.SellSerials(f.ctx, nil, []string{unit(0).Code}, locCentral, ref); err != nil {
		t.Fatal(err)
	}
	if n := f.count(`SELECT count(*) FROM serial_units WHERE code = ? AND parent_id IS NULL AND status = 'SOLD'`, unit(0).Code); n != 1 {
		t.Fatal("unit terjual masih di dalam karton")
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE serial_unit_id = ? AND biz_step = 'unpacking'
		AND parent_id = ? AND payload->>'reason' = 'sale' AND ref_id = ?`, unit(0).ID, seed.DemoSerialID("CARTON", 0), ref.RefID); n != 1 {
		t.Fatal("event unpacking alasan sale tidak tercatat")
	}
	if n := f.count(`SELECT count(*) FROM serial_units WHERE parent_id = ?`, seed.DemoSerialID("CARTON", 0)); n != 11 {
		t.Fatalf("isi karton %d, ingin 11", n)
	}
	// Karton sisa tetap bisa dikirim (tidak terblokir unit SOLD).
	if _, err := f.serials.Unpack(f.ctx, f.admin(), palletCode()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.serials.CreateShipment(f.ctx, f.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locJaksel, Codes: []string{cartonCode(0)},
	}); err != nil {
		t.Fatalf("karton sisa tidak bisa dikirim: %v", err)
	}
	// Rollback pemanggil ikut membatalkan pelepasan dari karton.
	sentinel := errSentinel("batal")
	_ = f.db.Transaction(func(tx *gormDB) error {
		if _, err := f.serials.SellSerials(f.ctx, tx, []string{unit(13).Code}, locCentral, ref); err != nil {
			t.Fatal(err)
		}
		return sentinel
	})
	if n := f.count(`SELECT count(*) FROM serial_units WHERE code = ? AND parent_id IS NOT NULL AND status = 'IN_STOCK'`, unit(13).Code); n != 1 {
		t.Fatal("rollback tidak mengembalikan unit ke kartonnya")
	}
}

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// #8: kode duplikat dalam satu panggilan service → 422 duplicate_code dengan
// field kemunculan kedua, bukan 404.
func TestService_DuplicateCodes(t *testing.T) {
	f := newFx(t)
	ref := service.SaleRef{RefType: "POS_TRANSACTION", RefID: uuid.Must(uuid.NewV7())}
	codes := []string{unit(0).Code, unit(1).Code, unit(0).Code}
	check := func(name string, err error) {
		t.Helper()
		ae := requireCode(t, err, "duplicate_code")
		if ae.Status != http.StatusUnprocessableEntity || len(ae.Fields) != 1 || ae.Fields[0].Code != "duplicate_code" ||
			ae.Fields[0].Field[len(ae.Fields[0].Field)-3:] != "[2]" {
			t.Fatalf("%s: %+v", name, ae.Fields)
		}
	}
	_, err := f.serials.SellSerials(f.ctx, nil, codes, locCentral, ref)
	check("SellSerials", err)
	_, err = f.serials.TransitionSerials(f.ctx, nil, codes, domain.SerialQuarantined, service.TransitionOptions{})
	check("TransitionSerials", err)
	_, err = f.serials.Commission(f.ctx, f.admin(), seed.BatchID(), codes)
	check("Commission", err)
	_, err = f.serials.ReceiveProduction(f.ctx, f.admin(), locCentral, codes)
	check("ReceiveProduction", err)
	_, err = f.serials.CreateShipment(f.ctx, f.admin(), service.CreateShipmentInput{FromLocationID: locCentral, ToLocationID: locJaksel, Codes: codes})
	check("CreateShipment", err)
	_, err = f.serials.Aggregate(f.ctx, f.admin(), cartonCode(1), codes)
	check("Aggregate", err)
	if s := f.status(unit(0).Code); s != domain.SerialInStock {
		t.Fatalf("status berubah %s", s)
	}
}

// #6: trace tetap menunjukkan shipment setelah palet & karton dibongkar.
func TestTrace_ShipmentSurvivesUnpack(t *testing.T) {
	f := newFx(t)
	program := "SUBSIDI_SULSEL_Q4"
	sh := f.shipPalletTo(locJaksel, &program) // toko sendiri: IN_STOCK, bisa dibongkar
	if _, err := f.serials.Unpack(f.ctx, f.admin(), palletCode()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.serials.Unpack(f.ctx, f.admin(), cartonCode(0)); err != nil {
		t.Fatal(err)
	}
	res, err := f.serials.Trace(f.ctx, staffAll("serial.trace"), unit(0).Code)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ancestors) != 0 {
		t.Fatalf("unit sudah lepas, leluhur saat ini %+v", res.Ancestors)
	}
	if len(res.Shipments) != 1 || res.Shipments[0].ID != sh.Shipment.ID || res.Shipments[0].ViaCode != palletCode() ||
		res.Shipments[0].ProgramCode == nil || *res.Shipments[0].ProgramCode != program {
		t.Fatalf("shipment hilang setelah unpack: %+v", res.Shipments)
	}
}

// Kecil: to_party_id ke lokasi sendiri wajib pemilik lokasi itu; scope serial
// tanpa lokasi (COMMISSIONED) = ALL atau scope gudang pusat.
func TestShipmentToPartyAndUnlocatedScope(t *testing.T) {
	f := newFx(t)
	_, err := f.serials.CreateShipment(f.ctx, f.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locJaksel, ToPartyID: &partySul, Codes: []string{palletCode()},
	})
	requireCode(t, err, "to_party_mismatch")
	// Ke lokasi mitra, party lain diizinkan (mis. titipan).
	if _, err := f.serials.CreateShipment(f.ctx, f.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locJatim, ToPartyID: &partySul, Codes: []string{palletCode()},
	}); err != nil {
		t.Fatal(err)
	}

	carton := f.commissioned(domain.LevelCarton, 1, seed.BatchID())[0]
	units := f.commissioned(domain.LevelUnit, 2, seed.BatchID())
	_, err = f.serials.Aggregate(f.ctx, staffAt([]uuid.UUID{locJaksel}, "serial.aggregate"), carton, units[:1])
	requireCode(t, err, "forbidden")
	if _, err := f.serials.Aggregate(f.ctx, staffAt([]uuid.UUID{locCentral}, "serial.aggregate"), carton, units[:1]); err != nil {
		t.Fatalf("operator gudang pusat: %v", err)
	}
	_, err = f.serials.Unpack(f.ctx, staffAt([]uuid.UUID{locHub}, "serial.aggregate"), carton)
	requireCode(t, err, "forbidden")
	if _, err := f.serials.Unpack(f.ctx, staffAll("serial.aggregate"), carton); err != nil {
		t.Fatal(err)
	}
	// Permission tetap dicek di helper scope (bukan hanya middleware).
	_, err = f.serials.Aggregate(f.ctx, staffAll("serial.trace"), carton, units[1:])
	requireCode(t, err, "forbidden")
}

// #5: klaim dibatasi per akun konsumen (selain per IP), dan IPv6 per /64.
func TestHTTP_ClaimRateLimitPerConsumerAndIPv6(t *testing.T) {
	f := newFx(t)
	cfg := f.cfg
	cfg.Serialization.ClaimRatePerConsumerPerHour = 2
	cfg.Serialization.VerifyRatePerMinute = 2
	h, _ := f.httpEngine(cfg)
	ip := func(addr string) func(*http.Request) { return func(r *http.Request) { r.RemoteAddr = addr } }
	body := map[string]any{"code": unit(0).Code, "pin": "0000000000"}
	for i, addr := range []string{"192.0.2.1:1", "192.0.2.2:1"} {
		if r := do(h, http.MethodPost, "/api/v1/public/claims", "consumer", body, ip(addr)); r.Code == http.StatusTooManyRequests {
			t.Fatalf("klaim ke-%d sudah dibatasi", i+1)
		}
	}
	r := do(h, http.MethodPost, "/api/v1/public/claims", "consumer", body, ip("192.0.2.3:1"))
	if r.Code != http.StatusTooManyRequests || r.problem(t).Code != "rate_limited" {
		t.Fatalf("akun yang berpindah IP harus tetap dibatasi: %d %s", r.Code, r.Body)
	}
	if r := do(h, http.MethodPost, "/api/v1/public/claims", "consumer2", body, ip("192.0.2.3:1")); r.Code == http.StatusTooManyRequests {
		t.Fatal("akun lain ikut dibatasi")
	}
	f.clk.Advance(time.Hour)
	if r := do(h, http.MethodPost, "/api/v1/public/claims", "consumer", body, ip("192.0.2.4:1")); r.Code == http.StatusTooManyRequests {
		t.Fatal("jendela per jam tidak berganti")
	}

	// IPv6: berganti alamat di /64 yang sama tetap satu kunci.
	v := map[string]any{"code": "kode-ngawur"}
	for _, addr := range []string{"[2001:db8:1:2::1]:1", "[2001:db8:1:2::2]:1"} {
		if r := do(h, http.MethodPost, "/api/v1/public/verify", "", v, ip(addr)); r.Code != http.StatusOK {
			t.Fatalf("%s: %d", addr, r.Code)
		}
	}
	if r := do(h, http.MethodPost, "/api/v1/public/verify", "", v, ip("[2001:db8:1:2:ffff::9]:1")); r.Code != http.StatusTooManyRequests {
		t.Fatalf("alamat lain di /64 yang sama: %d", r.Code)
	}
	if r := do(h, http.MethodPost, "/api/v1/public/verify", "", v, ip("[2001:db8:1:3::1]:1")); r.Code != http.StatusOK {
		t.Fatalf("/64 lain: %d", r.Code)
	}
}
