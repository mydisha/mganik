package serialtest

import (
	"testing"

	"github.com/google/uuid"

	"mganik/api/internal/domain"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

// Shipment palet: status dan custodian SELURUH turunan (1 palet + 4 karton +
// 48 unit) berubah dalam satu statement, dengan satu event per serial.
func TestShipment_PropagatesToWholePallet(t *testing.T) {
	f := newFx(t)
	program := "SUBSIDI_SULSEL_Q4"
	res, err := f.serials.CreateShipment(f.ctx, f.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locSulsel, ProgramCode: &program, Codes: []string{palletCode()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Serials != 53 || res.Units != 48 || len(res.Items) != 1 || res.Items[0].Qty != 48 {
		t.Fatalf("shipment %+v", res)
	}
	if res.Shipment.ToPartyID == nil || *res.Shipment.ToPartyID != partySul || res.Shipment.Status != "SHIPPED" {
		t.Fatalf("shipment %+v", res.Shipment)
	}
	if n := f.count(`SELECT count(*) FROM serial_units WHERE batch_id = ? AND status = 'IN_TRANSIT' AND custodian_party_id = ?`,
		seed.BatchID(), partySul); n != 53 {
		t.Fatalf("serial IN_TRANSIT milik Sulsel %d, ingin 53", n)
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE biz_step = 'shipping' AND ref_type = 'SHIPMENT' AND ref_id = ?
		AND from_status = 'IN_STOCK' AND to_status = 'IN_TRANSIT' AND party_id = ? AND payload->>'program_code' = ?`,
		res.Shipment.ID, partySul, program); n != 53 {
		t.Fatalf("event shipping %d, ingin 53 (satu per serial)", n)
	}
	if n := f.count(`SELECT count(DISTINCT serial_unit_id) FROM unit_events WHERE ref_id = ? AND biz_step = 'shipping'`, res.Shipment.ID); n != 53 {
		t.Fatalf("event per serial unik %d", n)
	}

	// Penerima di luar scope lokasi tujuan → 403.
	_, err = f.serials.ReceiveShipment(f.ctx, staffAt([]uuid.UUID{locJaksel}, "shipment.receive"), res.Shipment.ID)
	requireCode(t, err, "forbidden")

	rec, err := f.serials.ReceiveShipment(f.ctx, staffAt([]uuid.UUID{locSulsel}, "shipment.receive"), res.Shipment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Serials != 53 || rec.Shipment.Status != "RECEIVED" || rec.AlreadyReceived {
		t.Fatalf("receive %+v", rec)
	}
	if n := f.count(`SELECT count(*) FROM serial_units WHERE batch_id = ? AND status = 'AT_PARTNER' AND location_id = ?`,
		seed.BatchID(), locSulsel); n != 53 {
		t.Fatalf("AT_PARTNER di gudang Sulsel %d", n)
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE biz_step = 'receiving' AND ref_id = ? AND to_status = 'AT_PARTNER'`, res.Shipment.ID); n != 53 {
		t.Fatalf("event receiving %d", n)
	}
	// Idempotent: terima ulang tidak mengubah apa pun.
	again, err := f.serials.ReceiveShipment(f.ctx, f.admin(), res.Shipment.ID)
	if err != nil || !again.AlreadyReceived || again.Serials != 0 {
		t.Fatalf("terima ulang %+v %v", again, err)
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE biz_step = 'receiving' AND ref_id = ?`, res.Shipment.ID); n != 53 {
		t.Fatalf("event receiving setelah retry %d", n)
	}
}

func TestShipment_Rejections(t *testing.T) {
	f := newFx(t)
	admin := f.admin()
	// Karton masih di dalam palet.
	_, err := f.serials.CreateShipment(f.ctx, admin, service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locJaksel, Codes: []string{cartonCode(0)},
	})
	requireCode(t, err, "packed_in_parent")
	// Palet + kartonnya sekaligus: karton ikut lewat palet (tidak ganda).
	res, err := f.serials.CreateShipment(f.ctx, admin, service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locJatim, Codes: []string{palletCode(), cartonCode(0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Serials != 53 {
		t.Fatalf("shipment %+v", res)
	}
	// Sudah IN_TRANSIT: tidak bisa dikirim lagi.
	_, err = f.serials.CreateShipment(f.ctx, admin, service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locJaksel, Codes: []string{palletCode()},
	})
	requireCode(t, err, "invalid_transition")

	f2 := newFx(t)
	// Lokasi asal salah.
	_, err = f2.serials.CreateShipment(f2.ctx, f2.admin(), service.CreateShipmentInput{
		FromLocationID: locHub, ToLocationID: locJaksel, Codes: []string{palletCode()},
	})
	requireCode(t, err, "serial_wrong_location")
	// Asal = tujuan.
	_, err = f2.serials.CreateShipment(f2.ctx, f2.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locCentral, Codes: []string{palletCode()},
	})
	requireCode(t, err, "same_location")
	// Lokasi virtual sebagai tujuan.
	_, err = f2.serials.CreateShipment(f2.ctx, f2.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: seed.LocationID(seed.InTransitCode), Codes: []string{palletCode()},
	})
	requireCode(t, err, "location_virtual")
	// Scope lokasi asal (BOLA).
	_, err = f2.serials.CreateShipment(f2.ctx, staffAt([]uuid.UUID{locHub}, "shipment.create"), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locJaksel, Codes: []string{palletCode()},
	})
	requireCode(t, err, "forbidden")
	// Kemasan kosong tidak bisa dikirim.
	if _, err := f2.serials.Unpack(f2.ctx, f2.admin(), cartonCode(3)); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.serials.Unpack(f2.ctx, f2.admin(), palletCode()); err != nil {
		t.Fatal(err)
	}
	_, err = f2.serials.CreateShipment(f2.ctx, f2.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locJaksel, Codes: []string{palletCode()},
	})
	requireCode(t, err, "empty_container")
	// Unit dari karton yang sudah dibongkar bisa dikirim satuan ke toko → IN_STOCK.
	// Toko sudah punya unit dari seed (seed.DemoStoreUnit), jadi hitung selisihnya.
	inStoreQ := `SELECT count(*) FROM serial_units WHERE location_id = ? AND status = 'IN_STOCK' AND custodian_party_id = ?`
	inStoreBefore := f2.count(inStoreQ, locJaksel, partyCo)
	one, err := f2.serials.CreateShipment(f2.ctx, f2.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locJaksel, Codes: []string{unit(36).Code, cartonCode(0)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if one.Units != 13 || one.Serials != 14 {
		t.Fatalf("shipment satuan %+v", one)
	}
	if _, err := f2.serials.ReceiveShipment(f2.ctx, f2.admin(), one.Shipment.ID); err != nil {
		t.Fatal(err)
	}
	if s := f2.status(unit(36).Code); s != domain.SerialInStock {
		t.Fatalf("diterima toko sendiri: %s, ingin IN_STOCK", s)
	}
	if n := f2.count(inStoreQ, locJaksel, partyCo) - inStoreBefore; n != 14 {
		t.Fatalf("serial baru di toko %d, ingin 14", n)
	}
	// Shipment tidak ada / bukan SHIPPED.
	_, err = f2.serials.ReceiveShipment(f2.ctx, f2.admin(), uuid.Must(uuid.NewV7()))
	requireCode(t, err, "shipment_not_found")
}

// Recall saat barang di jalan: unit yang dikarantina dilewati saat diterima.
func TestShipment_ReceiveSkipsQuarantinedInTransit(t *testing.T) {
	f := newFx(t)
	res, err := f.serials.CreateShipment(f.ctx, f.admin(), service.CreateShipmentInput{
		FromLocationID: locCentral, ToLocationID: locSulsel, Codes: []string{palletCode()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.serials.TransitionSerials(f.ctx, nil, []string{unit(0).Code, unit(1).Code}, domain.SerialQuarantined, service.TransitionOptions{}); err != nil {
		t.Fatal(err)
	}
	rec, err := f.serials.ReceiveShipment(f.ctx, f.admin(), res.Shipment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Serials != 51 || rec.Skipped != 2 {
		t.Fatalf("receive %+v", rec)
	}
}

func TestTrace_Genealogy(t *testing.T) {
	f := newFx(t)
	program := "SUBSIDI_SULSEL_Q4"
	sh := f.shipPalletTo(locSulsel, &program)
	u := unit(13) // karton 1
	res, err := f.serials.Trace(f.ctx, staffAll("serial.trace"), u.Code)
	if err != nil {
		t.Fatal(err)
	}
	if res.Unit.Status != domain.SerialAtPartner || res.Unit.CustodianName == nil || res.Unit.LocationCode == nil ||
		*res.Unit.LocationCode != seed.PartnerSulselCode {
		t.Fatalf("unit %+v", res.Unit)
	}
	if len(res.Ancestors) != 2 || res.Ancestors[0].Code != cartonCode(1) || res.Ancestors[1].Code != palletCode() {
		t.Fatalf("leluhur %+v", res.Ancestors)
	}
	if len(res.Shipments) != 1 || res.Shipments[0].ID != sh.Shipment.ID || res.Shipments[0].ViaCode != palletCode() ||
		res.Shipments[0].ProgramCode == nil || *res.Shipments[0].ProgramCode != program {
		t.Fatalf("shipment %+v", res.Shipments)
	}
	steps := map[string]int{}
	for _, e := range res.Events {
		steps[e.BizStep]++
	}
	for _, s := range []string{"commissioning", "packing", "shipping"} {
		if steps[s] != 1 {
			t.Fatalf("event %s = %d, semua %v", s, steps[s], steps)
		}
	}
	if steps["receiving"] != 2 {
		t.Fatalf("event receiving %d (gudang pusat + mitra)", steps["receiving"])
	}
	// Ringkasan turunan palet.
	p, err := f.serials.Trace(f.ctx, staffAll("serial.trace"), palletCode())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, d := range p.Descendants {
		got[d.LevelCode+":"+d.Status] = d.Count
	}
	if got["UNIT:AT_PARTNER"] != 48 || got["CARTON:AT_PARTNER"] != 4 || len(got) != 2 {
		t.Fatalf("turunan %v", got)
	}
	// Scope: staff gudang pusat tidak melihat unit di gudang mitra.
	_, err = f.serials.Trace(f.ctx, staffAt([]uuid.UUID{locCentral}, "serial.trace"), u.Code)
	requireCode(t, err, "forbidden")
	unknown, _ := domain.GenerateSerialCode(cryptoRand{})
	_, err = f.serials.Trace(f.ctx, staffAll("serial.trace"), unknown)
	requireCode(t, err, "serial_not_found")
}
