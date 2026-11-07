package serialtest

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/seed"
	"mganik/api/internal/service"
)

// TestSerialTransitions_Table (deliverable wajib): setiap pasangan status
// (from, to) dicoba lewat TransitionSerials terhadap Postgres sungguhan.
// Pasangan yang ada di lifecycle_transitions lolos dan menulis unit_events;
// sisanya 409 invalid_transition dan status tidak berubah.
func TestSerialTransitions_Table(t *testing.T) {
	f := newFx(t)
	var statuses []string
	if err := f.db.Raw(`SELECT code FROM serial_unit_statuses ORDER BY sort_order`).Scan(&statuses).Error; err != nil {
		t.Fatal(err)
	}
	if len(statuses) < 10 {
		t.Fatalf("status serial cuma %d", len(statuses))
	}
	// Sumber kebenaran: tabel di DB, bukan domain.Lifecycle (yang sedang diuji).
	var pairs []struct{ FromCode, ToCode, BizStep string }
	if err := f.db.Raw(`SELECT from_code, to_code, biz_step FROM lifecycle_transitions
		WHERE entity_type = 'SERIAL_UNIT' AND from_code IS NOT NULL`).Scan(&pairs).Error; err != nil {
		t.Fatal(err)
	}
	valid := map[[2]string]string{}
	for _, p := range pairs {
		valid[[2]string{p.FromCode, p.ToCode}] = p.BizStep
	}

	// Satu unit baru per pasangan, langsung di status asal.
	type tc struct {
		from, to, code string
		id             uuid.UUID
	}
	var cases []tc
	var units []model.SerialUnit
	product, batch := seed.ProductID(seed.SKU1), seed.BatchID()
	now := f.clk.Now()
	for _, from := range statuses {
		for _, to := range statuses {
			if from == to {
				continue
			}
			code, err := domain.GenerateSerialCode(cryptoRand{})
			if err != nil {
				t.Fatal(err)
			}
			id := uuid.Must(uuid.NewV7())
			cases = append(cases, tc{from: from, to: to, code: code, id: id})
			units = append(units, model.SerialUnit{
				ID: id, Code: code, LevelCode: domain.LevelUnit, ProductID: &product, BatchID: &batch,
				Status: from, LocationID: &locCentral, CommissionedAt: &now, Attributes: model.JSON(`{}`),
			})
		}
	}
	if err := f.db.CreateInBatches(&units, 200).Error; err != nil {
		t.Fatal(err)
	}

	passed, rejected := 0, 0
	for _, c := range cases {
		t.Run(c.from+"->"+c.to, func(t *testing.T) {
			step, ok := valid[[2]string{c.from, c.to}]
			got, err := f.serials.TransitionSerials(f.ctx, nil, []string{c.code}, c.to, service.TransitionOptions{
				Ref: service.EventRef{Type: "TEST"},
			})
			if ok {
				if err != nil {
					t.Fatalf("transisi sah ditolak: %v", err)
				}
				if len(got) != 1 || got[0].FromStatus != c.from {
					t.Fatalf("hasil %+v", got)
				}
				if s := f.status(c.code); s != c.to {
					t.Fatalf("status %s, ingin %s", s, c.to)
				}
				if n := f.count(`SELECT count(*) FROM unit_events WHERE serial_unit_id = ? AND from_status = ? AND to_status = ? AND biz_step = ?`,
					c.id, c.from, c.to, step); n != 1 {
					t.Fatalf("event transisi %d, ingin 1", n)
				}
				passed++
				return
			}
			requireCode(t, err, "invalid_transition")
			if s := f.status(c.code); s != c.from {
				t.Fatalf("status berubah menjadi %s padahal transisi ditolak", s)
			}
			if n := f.count(`SELECT count(*) FROM unit_events WHERE serial_unit_id = ?`, c.id); n != 0 {
				t.Fatalf("transisi ditolak tetap menulis %d event", n)
			}
			rejected++
		})
	}
	if passed != len(pairs) {
		t.Fatalf("transisi sah yang lolos %d, ingin %d (semua baris lifecycle_transitions)", passed, len(pairs))
	}
	t.Logf("%d transisi sah lolos, %d pasangan lain ditolak", passed, rejected)
}

// Transisi yang butuh permission (kolom lifecycle_transitions.permission)
// ditolak untuk principal tanpa permission itu.
func TestTransitionSerials_PermissionFromData(t *testing.T) {
	f := newFx(t)
	code := unit(0).Code
	noPerm := staffAll("serial.trace")
	_, err := f.serials.TransitionSerials(f.ctx, nil, []string{code}, domain.SerialQuarantined, service.TransitionOptions{Principal: noPerm})
	requireCode(t, err, "forbidden")
	withPerm := staffAll("serial.quarantine")
	if _, err := f.serials.TransitionSerials(f.ctx, nil, []string{code}, domain.SerialQuarantined, service.TransitionOptions{Principal: withPerm}); err != nil {
		t.Fatal(err)
	}
	if s := f.status(code); s != domain.SerialQuarantined {
		t.Fatalf("status %s", s)
	}
}

// TestSerial_ConcurrentSell: banyak goroutine menjual serial yang sama →
// tepat satu menang, sisanya invalid_transition, satu event jual.
func TestSerial_ConcurrentSell(t *testing.T) {
	f := newFx(t)
	code := unit(5).Code
	const workers = 20
	var wg sync.WaitGroup
	results := make([]error, workers)
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = f.serials.SellSerials(f.ctx, nil, []string{code}, locCentral, service.SaleRef{
				RefType: "POS_TRANSACTION", RefID: uuid.Must(uuid.NewV7()),
			})
		}(i)
	}
	close(start)
	wg.Wait()
	wins := 0
	for _, err := range results {
		switch errCode(err) {
		case "":
			wins++
		case "invalid_transition":
		default:
			t.Fatalf("error tak terduga: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("pemenang %d, ingin tepat 1", wins)
	}
	if s := f.status(code); s != domain.SerialSold {
		t.Fatalf("status %s", s)
	}
	if n := f.count(`SELECT count(*) FROM unit_events e JOIN serial_units s ON s.id = e.serial_unit_id
		WHERE s.code = ? AND e.biz_step = 'retail_selling'`, code); n != 1 {
		t.Fatalf("event jual %d, ingin 1", n)
	}
}

func TestSellSerials_AllOrNothingAndLocation(t *testing.T) {
	f := newFx(t)
	ref := service.SaleRef{RefType: "POS_TRANSACTION", RefID: uuid.Must(uuid.NewV7()), OccurredAt: t0.Add(-time.Hour),
		Actor: service.SerialActor{Type: model.ActorDevice, DeviceID: ptr(seed.PosDeviceID())}}

	// Salah lokasi: unit ada di gudang pusat, dijual dari toko.
	_, err := f.serials.SellSerials(f.ctx, nil, []string{unit(0).Code}, locJaksel, ref)
	requireCode(t, err, "serial_wrong_location")

	// Satu kode tidak terdaftar → tidak ada yang terjual.
	unknown, _ := domain.GenerateSerialCode(cryptoRand{})
	_, err = f.serials.SellSerials(f.ctx, nil, []string{unit(0).Code, unknown}, locCentral, ref)
	ae := requireCode(t, err, "serial_not_found")
	if len(ae.Fields) != 1 || ae.Fields[0].Field != "codes[1]" {
		t.Fatalf("field error %+v", ae.Fields)
	}
	if s := f.status(unit(0).Code); s != domain.SerialInStock {
		t.Fatalf("all-or-nothing dilanggar: status %s", s)
	}

	// Sukses: event mencatat device, ref, dan jam transaksi device.
	sold, err := f.serials.SellSerials(f.ctx, nil, []string{unit(0).Code, unit(1).Code}, locCentral, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(sold) != 2 || sold[0].ProductID == nil || *sold[0].ProductID != seed.ProductID(seed.SKU1) {
		t.Fatalf("hasil jual %+v", sold)
	}
	if n := f.count(`SELECT count(*) FROM unit_events WHERE biz_step = 'retail_selling' AND actor_type = 'DEVICE'
		AND device_id = ? AND ref_id = ? AND occurred_at = ?`, seed.PosDeviceID(), ref.RefID, ref.OccurredAt); n != 2 {
		t.Fatalf("event jual dengan device/ref/jam device %d, ingin 2", n)
	}
	// AT_PARTNER → SOLD sah menurut lifecycle, tetapi SellSerials hanya IN_STOCK.
	f.exec(`UPDATE serial_units SET status = 'AT_PARTNER' WHERE code = ?`, unit(2).Code)
	_, err = f.serials.SellSerials(f.ctx, nil, []string{unit(2).Code}, locCentral, ref)
	requireCode(t, err, "invalid_transition")
}

// SellSerials di dalam transaksi pemanggil ikut rollback bersama transaksi itu.
func TestSellSerials_CallerTransactionRollback(t *testing.T) {
	f := newFx(t)
	sentinel := fmt.Errorf("rollback pemanggil")
	err := f.db.Transaction(func(tx *gormDB) error {
		if _, err := f.serials.SellSerials(f.ctx, tx, []string{unit(3).Code}, locCentral, service.SaleRef{
			RefType: "POS_TRANSACTION", RefID: uuid.Must(uuid.NewV7()),
		}); err != nil {
			return err
		}
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("err %v", err)
	}
	if s := f.status(unit(3).Code); s != domain.SerialInStock {
		t.Fatalf("status %s setelah rollback, ingin IN_STOCK", s)
	}
}
