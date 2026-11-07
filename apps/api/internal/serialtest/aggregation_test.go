package serialtest

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/seed"
)

// commissioned mint + commission n kode level ke batch.
func (f *fx) commissioned(level string, n int, batch uuid.UUID) []string {
	f.t.Helper()
	codes := codesOf(f.mint(level, n))
	if _, err := f.serials.Commission(f.ctx, f.admin(), batch, codes); err != nil {
		f.t.Fatal(err)
	}
	return codes
}

func (f *fx) secondBatch() uuid.UUID {
	f.t.Helper()
	b := model.Batch{
		ID: uuid.Must(uuid.NewV7()), ProductID: seed.ProductID(seed.SKU1), LotNo: "L2611B",
		MfgDate: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), ExpDate: time.Date(2028, 11, 1, 0, 0, 0, 0, time.UTC),
		Status: "ACTIVE",
	}
	if err := f.db.Create(&b).Error; err != nil {
		f.t.Fatal(err)
	}
	return b.ID
}

func TestAggregate_CapacityAndIdempotent(t *testing.T) {
	f := newFx(t)
	carton := f.commissioned(domain.LevelCarton, 1, seed.BatchID())[0]
	units := f.commissioned(domain.LevelUnit, 13, seed.BatchID())

	res, err := f.serials.Aggregate(f.ctx, f.admin(), carton, units[:12])
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 12 || res.ChildCount != 12 || res.Capacity != seed.UnitsPerCarton {
		t.Fatalf("agregasi %+v", res)
	}
	if n := f.count(`SELECT count(*) FROM unit_events e JOIN serial_units p ON p.id = e.parent_id
		WHERE p.code = ? AND e.biz_step = 'packing'`, carton); n != 12 {
		t.Fatalf("event packing %d", n)
	}
	// Unit ke-13 melampaui kapasitas karton (12).
	_, err = f.serials.Aggregate(f.ctx, f.admin(), carton, units[12:])
	requireCode(t, err, "capacity_exceeded")
	// Retry anak yang sudah di induk yang sama: aman, tidak menambah.
	again, err := f.serials.Aggregate(f.ctx, f.admin(), carton, units[:2])
	if err != nil || again.Added != 0 || again.AlreadyInParent != 2 {
		t.Fatalf("retry %+v %v", again, err)
	}
}

func TestAggregate_Rejections(t *testing.T) {
	f := newFx(t)
	cartons := f.commissioned(domain.LevelCarton, 2, seed.BatchID())
	units := f.commissioned(domain.LevelUnit, 3, seed.BatchID())
	pallet := f.commissioned(domain.LevelPallet, 1, seed.BatchID())[0]

	if _, err := f.serials.Aggregate(f.ctx, f.admin(), cartons[0], units[:1]); err != nil {
		t.Fatal(err)
	}
	// Anak sudah punya induk lain.
	_, err := f.serials.Aggregate(f.ctx, f.admin(), cartons[1], units[:1])
	requireCode(t, err, "already_packed")
	// Rank: karton ke karton, karton ke unit.
	_, err = f.serials.Aggregate(f.ctx, f.admin(), cartons[1], cartons[:1])
	requireCode(t, err, "level_mismatch")
	_, err = f.serials.Aggregate(f.ctx, f.admin(), units[1], cartons[1:])
	requireCode(t, err, "level_mismatch")
	// Siklus: induk menjadi anaknya sendiri.
	_, err = f.serials.Aggregate(f.ctx, f.admin(), cartons[1], []string{cartons[1]})
	requireCode(t, err, "cycle_detected")
	// Unit langsung ke palet: rank lolos, tetapi spesifikasi kemasan UNIT → CARTON.
	_, err = f.serials.Aggregate(f.ctx, f.admin(), pallet, units[1:2])
	requireCode(t, err, "level_mismatch")
	// Level anak campur.
	_, err = f.serials.Aggregate(f.ctx, f.admin(), pallet, []string{cartons[1], units[2]})
	requireCode(t, err, "mixed_levels")
	// Status: label GENERATED belum bisa dikemas.
	gen := codesOf(f.mint(domain.LevelUnit, 1))
	_, err = f.serials.Aggregate(f.ctx, f.admin(), cartons[1], gen)
	requireCode(t, err, "status_mismatch")
	// Produk lain.
	other := seed.ProductID(seed.SKU2)
	f.exec(`UPDATE serial_units SET product_id = ? WHERE code = ?`, other, units[2])
	_, err = f.serials.Aggregate(f.ctx, f.admin(), cartons[1], units[2:3])
	requireCode(t, err, "product_mismatch")
	// Kode tidak terdaftar menunjuk field-nya.
	unknown, _ := domain.GenerateSerialCode(cryptoRand{})
	_, err = f.serials.Aggregate(f.ctx, f.admin(), cartons[1], []string{units[1], unknown})
	ae := requireCode(t, err, "serial_not_found")
	if len(ae.Fields) != 1 || ae.Fields[0].Field != "child_codes[1]" {
		t.Fatalf("field %+v", ae.Fields)
	}
	// Scope lokasi: karton IN_STOCK di gudang pusat, staff hanya hub.
	_, err = f.serials.Aggregate(f.ctx, staffAt([]uuid.UUID{locHub}, "serial.aggregate"), cartonCode(0), []string{unit(0).Code})
	requireCode(t, err, "forbidden")
}

func TestAggregate_MixedBatchRule(t *testing.T) {
	f := newFx(t)
	b2 := f.secondBatch()
	carton := f.commissioned(domain.LevelCarton, 1, seed.BatchID())[0]
	a := f.commissioned(domain.LevelUnit, 2, seed.BatchID())
	b := f.commissioned(domain.LevelUnit, 1, b2)

	_, err := f.serials.Aggregate(f.ctx, f.admin(), carton, append(a[:1], b...))
	ae := requireCode(t, err, "mixed_batch_not_allowed")
	if len(ae.Fields) != 1 || ae.Fields[0].Field != "child_codes[1]" {
		t.Fatalf("field %+v", ae.Fields)
	}
	// Isi karton sudah batch 1, unit batch 2 ditolak walau dikirim sendiri.
	if _, err := f.serials.Aggregate(f.ctx, f.admin(), carton, a[:1]); err != nil {
		t.Fatal(err)
	}
	_, err = f.serials.Aggregate(f.ctx, f.admin(), carton, b)
	requireCode(t, err, "mixed_batch_not_allowed")
	// Spesifikasi mengizinkan campuran.
	f.exec(`UPDATE packaging_specs SET allow_mixed_batch = true WHERE product_id = ? AND child_level = 'UNIT'`, seed.ProductID(seed.SKU1))
	if _, err := f.serials.Aggregate(f.ctx, f.admin(), carton, b); err != nil {
		t.Fatalf("campuran batch yang diizinkan ditolak: %v", err)
	}
}

// Dua agregasi paralel ke karton yang sama (8 + 8 > 12): induk dikunci
// sebelum kapasitas dihitung, jadi tepat satu sukses.
func TestAggregate_ConcurrentCapacity(t *testing.T) {
	f := newFx(t)
	carton := f.commissioned(domain.LevelCarton, 1, seed.BatchID())[0]
	units := f.commissioned(domain.LevelUnit, 16, seed.BatchID())
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = f.serials.Aggregate(f.ctx, f.admin(), carton, units[i*8:(i+1)*8])
		}(i)
	}
	close(start)
	wg.Wait()
	ok, full := 0, 0
	for _, err := range errs {
		switch errCode(err) {
		case "":
			ok++
		case "capacity_exceeded":
			full++
		default:
			t.Fatalf("error tak terduga %v", err)
		}
	}
	if ok != 1 || full != 1 {
		t.Fatalf("sukses %d, penuh %d", ok, full)
	}
	if n := f.count(`SELECT count(*) FROM serial_units c JOIN serial_units p ON p.id = c.parent_id WHERE p.code = ?`, carton); n != 8 {
		t.Fatalf("isi karton %d, ingin 8", n)
	}
}

func TestUnpack(t *testing.T) {
	f := newFx(t)
	res, err := f.serials.Unpack(f.ctx, f.admin(), cartonCode(0))
	if err != nil {
		t.Fatal(err)
	}
	if res.Unpacked != 12 {
		t.Fatalf("dibongkar %d", res.Unpacked)
	}
	if n := f.count(`SELECT count(*) FROM unit_events e WHERE e.biz_step = 'unpacking' AND e.parent_id = ?`, seed.DemoSerialID("CARTON", 0)); n != 12 {
		t.Fatalf("event unpacking %d", n)
	}
	again, err := f.serials.Unpack(f.ctx, f.admin(), cartonCode(0))
	if err != nil || again.Unpacked != 0 {
		t.Fatalf("bongkar ulang %+v %v", again, err)
	}
	// Unit yang sudah dibongkar bisa dikemas ke karton lain.
	if _, err := f.serials.Aggregate(f.ctx, f.admin(), cartonCode(1), []string{unit(0).Code}); err == nil {
		t.Fatal("karton 1 sudah penuh (12), harus capacity_exceeded")
	}
	unknown, _ := domain.GenerateSerialCode(cryptoRand{})
	_, err = f.serials.Unpack(f.ctx, f.admin(), unknown)
	requireCode(t, err, "serial_not_found")
}
