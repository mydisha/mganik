package serialtest

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

func TestMint_UnitCodesAndPINs(t *testing.T) {
	f := newFx(t)
	product := seed.ProductID(seed.SKU1)
	note := "Gulungan #7 — label hologram 🏷️ (Bekasi)"
	res, err := f.serials.Mint(f.ctx, f.admin(), service.MintInput{
		LevelCode: domain.LevelUnit, Qty: 25, ProductID: &product, PrinterVendor: "PT Cetak Aman", Notes: &note,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Codes) != 25 || res.CodeBatch.Qty != 25 {
		t.Fatalf("kode %d, qty %d", len(res.Codes), res.CodeBatch.Qty)
	}
	seen := map[string]bool{}
	for _, c := range res.Codes {
		if !domain.ValidSerialCode(c.Code) || seen[c.Code] {
			t.Fatalf("kode %q tidak valid/duplikat", c.Code)
		}
		seen[c.Code] = true
		if _, ok := domain.NormalizeClaimPIN(c.PIN); !ok {
			t.Fatalf("PIN %q tidak valid", c.PIN)
		}
		var su model.SerialUnit
		if err := f.db.Where("code = ?", c.Code).Take(&su).Error; err != nil {
			t.Fatal(err)
		}
		if su.Status != domain.SerialGenerated || su.CodeBatchID == nil || *su.CodeBatchID != res.CodeBatch.ID {
			t.Fatalf("serial %+v", su)
		}
		if !domain.VerifyClaimPIN(testPepper, su.ID, c.PIN, su.ClaimSecretHash) {
			t.Fatal("claim_secret_hash tidak cocok dengan HMAC(pepper, id, PIN)")
		}
		// PIN plaintext tidak boleh ada di DB dalam bentuk apa pun yang mudah dicari.
		if n := f.count(`SELECT count(*) FROM serial_units s WHERE s.id = ? AND (s::text LIKE '%' || ? || '%')`, su.ID, c.PIN); n != 0 {
			t.Fatal("PIN plaintext tersimpan di serial_units")
		}
	}
	if n := f.count(`SELECT count(*) FROM unit_events e JOIN serial_units s ON s.id = e.serial_unit_id
		WHERE s.code_batch_id = ? AND e.biz_step = 'encoding' AND e.to_status = 'GENERATED'`, res.CodeBatch.ID); n != 25 {
		t.Fatalf("event encoding %d, ingin 25", n)
	}
	var stored string
	if err := f.db.Raw(`SELECT notes FROM code_batches WHERE id = ?`, res.CodeBatch.ID).Scan(&stored).Error; err != nil || stored != note {
		t.Fatalf("catatan unicode %q (err %v)", stored, err)
	}
	pins := f.count(`SELECT count(*) FROM unit_events WHERE payload::text LIKE '%' || ? || '%'`, res.Codes[0].PIN)
	if pins != 0 {
		t.Fatal("PIN muncul di payload event")
	}
}

// Batas atas satu job (5000) melewati beberapa potongan INSERT (1000/statement).
func TestMint_MaxQtyAcrossChunks(t *testing.T) {
	f := newFx(t)
	start := time.Now()
	res, err := f.serials.Mint(f.ctx, f.admin(), service.MintInput{LevelCode: domain.LevelUnit, Qty: service.MaxMintQty, PrinterVendor: "V"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Codes) != service.MaxMintQty {
		t.Fatalf("kode %d", len(res.Codes))
	}
	if n := f.count(`SELECT count(DISTINCT code) FROM serial_units WHERE code_batch_id = ? AND claim_secret_hash IS NOT NULL`, res.CodeBatch.ID); n != service.MaxMintQty {
		t.Fatalf("serial tersimpan %d", n)
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE biz_step = 'encoding' AND ref_id = ?`, res.CodeBatch.ID); n != service.MaxMintQty {
		t.Fatalf("event %d", n)
	}
	t.Logf("mint %d kode + PIN: %s", service.MaxMintQty, time.Since(start))
}

func TestMint_ContainerHasNoPINAndValidation(t *testing.T) {
	f := newFx(t)
	res, err := f.serials.Mint(f.ctx, f.admin(), service.MintInput{LevelCode: domain.LevelCarton, Qty: 3, PrinterVendor: "V"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Codes {
		if c.PIN != "" {
			t.Fatal("karton tidak boleh punya PIN")
		}
	}
	if n := f.count(`SELECT count(*) FROM serial_units WHERE code_batch_id = ? AND claim_secret_hash IS NULL`, res.CodeBatch.ID); n != 3 {
		t.Fatalf("karton tanpa hash %d", n)
	}
	_, err = f.serials.Mint(f.ctx, f.admin(), service.MintInput{LevelCode: "BOX_AJAIB", Qty: 1, PrinterVendor: "V"})
	requireCode(t, err, "level_not_found")
	_, err = f.serials.Mint(f.ctx, f.admin(), service.MintInput{LevelCode: domain.LevelUnit, Qty: service.MaxMintQty + 1, PrinterVendor: "V"})
	requireCode(t, err, "validation_failed")
	ghost := uuid.Must(uuid.NewV7())
	_, err = f.serials.Mint(f.ctx, f.admin(), service.MintInput{LevelCode: domain.LevelUnit, Qty: 1, ProductID: &ghost, PrinterVendor: "V"})
	requireCode(t, err, "product_not_found")
}

func TestCommissionAndReceive(t *testing.T) {
	f := newFx(t)
	minted := f.mint(domain.LevelUnit, 4)
	codes := codesOf(minted)

	res, err := f.serials.Commission(f.ctx, f.admin(), seed.BatchID(), codes)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 4 || res.ProductID != seed.ProductID(seed.SKU1) {
		t.Fatalf("commission %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM serial_units WHERE code = ANY(?) AND status = 'COMMISSIONED'
		AND batch_id = ? AND product_id = ? AND commissioned_at IS NOT NULL`, model.StringArray(codes), seed.BatchID(), seed.ProductID(seed.SKU1)); n != 4 {
		t.Fatalf("commissioned %d", n)
	}
	// Commission kedua: sudah bukan GENERATED.
	_, err = f.serials.Commission(f.ctx, f.admin(), seed.BatchID(), codes[:1])
	requireCode(t, err, "invalid_transition")
	// Kode tidak terdaftar dan batch tidak ada.
	unknown, _ := domain.GenerateSerialCode(cryptoRand{})
	_, err = f.serials.Commission(f.ctx, f.admin(), seed.BatchID(), []string{unknown})
	requireCode(t, err, "serial_not_found")
	_, err = f.serials.Commission(f.ctx, f.admin(), uuid.Must(uuid.NewV7()), codes)
	requireCode(t, err, "batch_not_found")

	// Label dicetak untuk produk lain.
	other := seed.ProductID(seed.SKU2)
	m2, err := f.serials.Mint(f.ctx, f.admin(), service.MintInput{LevelCode: domain.LevelUnit, Qty: 1, ProductID: &other, PrinterVendor: "V"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.serials.Commission(f.ctx, f.admin(), seed.BatchID(), codesOf(m2.Codes))
	requireCode(t, err, "product_mismatch")

	// Terima: lokasi mitra ditolak, scope lokasi lain 403, gudang pusat sukses.
	_, err = f.serials.ReceiveProduction(f.ctx, f.admin(), locSulsel, codes)
	requireCode(t, err, "location_not_own")
	_, err = f.serials.ReceiveProduction(f.ctx, staffAt([]uuid.UUID{locHub}, "serial.receive"), locCentral, codes)
	requireCode(t, err, "forbidden")
	rec, err := f.serials.ReceiveProduction(f.ctx, staffAt([]uuid.UUID{locCentral}, "serial.receive"), locCentral, codes)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Serials != 4 {
		t.Fatalf("diterima %d", rec.Serials)
	}
	if n := f.count(`SELECT count(*) FROM serial_units WHERE code = ANY(?) AND status = 'IN_STOCK'
		AND location_id = ? AND custodian_party_id = ?`, model.StringArray(codes), locCentral, partyCo); n != 4 {
		t.Fatalf("IN_STOCK di gudang pusat %d", n)
	}

	// Batch yang di-hold tidak bisa menerima commission.
	f.exec(`UPDATE batches SET status = 'ON_HOLD' WHERE id = ?`, seed.BatchID())
	m3 := f.mint(domain.LevelUnit, 1)
	_, err = f.serials.Commission(f.ctx, f.admin(), seed.BatchID(), codesOf(m3))
	requireCode(t, err, "batch_not_active")
}

// Recall: unit di rantai pasok → QUARANTINED massal, unit di tangan konsumen
// tetap; verifikasi unit terjual menampilkan RECALLED. Idempotent.
func TestRecallBatch(t *testing.T) {
	f := newFx(t)
	// Satu unit sudah terjual (di tangan konsumen) sebelum recall.
	if _, err := f.serials.SellSerials(f.ctx, nil, []string{unit(47).Code}, locCentral, service.SaleRef{
		RefType: "ORDER", RefID: uuid.Must(uuid.NewV7()),
	}); err != nil {
		t.Fatal(err)
	}
	reason := "Kontaminasi mikroba — uji lab №42"
	res, err := f.serials.RecallBatch(f.ctx, f.admin(), seed.BatchID(), reason)
	if err != nil {
		t.Fatal(err)
	}
	// 1 palet + 4 karton + 47 unit IN_STOCK.
	if res.Quarantined != 52 || res.AlreadyRecalled {
		t.Fatalf("recall %+v", res)
	}
	if s := f.status(unit(47).Code); s != domain.SerialSold {
		t.Fatalf("unit terjual berubah menjadi %s", s)
	}
	if n := f.count(`SELECT count(*) FROM serial_units WHERE batch_id = ? AND status = 'QUARANTINED'`, seed.BatchID()); n != 52 {
		t.Fatalf("QUARANTINED %d", n)
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE biz_step = 'holding' AND to_status = 'QUARANTINED' AND ref_id = ?`, seed.BatchID()); n != 52 {
		t.Fatalf("event holding %d", n)
	}
	var stored string
	if err := f.db.Raw(`SELECT recall_reason FROM batches WHERE id = ? AND status = 'RECALLED'`, seed.BatchID()).Scan(&stored).Error; err != nil || stored != reason {
		t.Fatalf("recall_reason %q", stored)
	}
	if n := f.count(`SELECT count(*) FROM audit_logs WHERE action = 'batch.recall' AND entity_id = ? AND reason = ?`, seed.BatchID(), reason); n != 1 {
		t.Fatalf("audit recall %d", n)
	}
	again, err := f.serials.RecallBatch(f.ctx, f.admin(), seed.BatchID(), reason)
	if err != nil || !again.AlreadyRecalled || again.Quarantined != 0 {
		t.Fatalf("recall kedua %+v %v", again, err)
	}
	v, err := f.verify.Verify(f.ctx, service.VerifyInput{RawCode: unit(47).Code, AnonID: "a1", ClientIP: "198.51.100.7"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Result != domain.ResultRecalled || v.LotNo == nil || *v.LotNo != seed.DemoLotNo {
		t.Fatalf("verifikasi unit recall %+v", v)
	}
	_, err = f.serials.RecallBatch(f.ctx, f.admin(), uuid.Must(uuid.NewV7()), reason)
	requireCode(t, err, "batch_not_found")
}
