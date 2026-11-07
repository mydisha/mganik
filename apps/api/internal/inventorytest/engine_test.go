package inventorytest_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/inventorytest"
	"mganik/api/internal/service"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

func reserveInput(f *inventorytest.Fixture, key string, qty int64) service.MovementInput {
	return service.MovementInput{
		LocationID: f.Central, ProductID: f.ProductA, Type: service.MovementReserve, Qty: qty,
		RefType: "TEST", RefID: uuid.New(), IdempotencyKey: key, Actor: service.SystemLedgerActor,
	}
}

// Deliverable wajib: stok 10, 100 goroutine me-reserve 1 unit bersamaan.
// Conditional UPDATE + re-evaluasi WHERE di READ COMMITTED menjamin tepat 10
// menang tanpa SERIALIZABLE dan tanpa retry.
func TestReserve_ConcurrentLastUnits(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 10)
	retriesBefore := service.TxRetryCount()

	const workers = 100
	var ok, insufficient atomic.Int64
	var mu sync.Mutex
	var others []error
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			<-start
			err := f.DB.Transaction(func(tx *gorm.DB) error {
				_, err := f.Inv.ApplyMovement(context.Background(), tx, reserveInput(f, fmt.Sprintf("test:reserve:%d", i), 1))
				return err
			})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, service.ErrInsufficientStock):
				insufficient.Add(1)
			default:
				mu.Lock()
				others = append(others, err)
				mu.Unlock()
			}
		})
	}
	close(start)
	wg.Wait()

	if len(others) > 0 {
		t.Fatalf("%d error selain ErrInsufficientStock, contoh: %v", len(others), others[0])
	}
	if ok.Load() != 10 || insufficient.Load() != 90 {
		t.Fatalf("sukses %d gagal-stok %d, ingin 10 dan 90", ok.Load(), insufficient.Load())
	}
	b := f.Balance(f.Central, f.ProductA)
	if b.PhysicalQty != 10 || b.ReservedQty != 10 || b.AvailableQty != 0 {
		t.Fatalf("saldo fisik %d reserved %d available %d, ingin 10/10/0", b.PhysicalQty, b.ReservedQty, b.AvailableQty)
	}
	if n := f.Count(`SELECT count(*) FROM inventory_movements WHERE movement_type = 'RESERVE'`); n != 10 {
		t.Fatalf("baris ledger RESERVE %d, ingin 10", n)
	}
	if service.TxRetryCount() != retriesBefore {
		t.Fatal("ada transaksi yang diulang karena deadlock/serialization")
	}
	f.AssertLedgerInvariant()
}

func TestApplyMovement_Rules(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 5)
	ctx := context.Background()
	apply := func(in service.MovementInput) (service.MovementResult, error) {
		var res service.MovementResult
		err := f.DB.Transaction(func(tx *gorm.DB) error {
			var err error
			res, err = f.Inv.ApplyMovement(ctx, tx, in)
			return err
		})
		return res, err
	}
	base := func(typ string, qty int64) service.MovementInput {
		return service.MovementInput{LocationID: f.Central, ProductID: f.ProductA, Type: typ, Qty: qty,
			RefType: "TEST", RefID: uuid.New(), IdempotencyKey: "rules:" + uuid.NewString(), Actor: service.SystemLedgerActor}
	}

	t.Run("tipe tidak dikenal", func(t *testing.T) {
		if _, err := apply(base("TITIP_JUAL", 1)); !errors.Is(err, service.ErrMovementTypeUnknown) {
			t.Fatalf("dapat %v", err)
		}
	})
	t.Run("qty negatif untuk tipe satu arah", func(t *testing.T) {
		if _, err := apply(base(service.MovementReserve, -1)); !errors.Is(err, service.ErrInvalidMovement) {
			t.Fatalf("dapat %v", err)
		}
	})
	t.Run("qty nol dan melewati batas", func(t *testing.T) {
		for _, q := range []int64{0, service.MaxMovementQty + 1, -service.MaxMovementQty - 1} {
			if _, err := apply(base(service.MovementAdjustCount, q)); !errors.Is(err, service.ErrInvalidMovement) {
				t.Fatalf("qty %d: dapat %v", q, err)
			}
		}
	})
	t.Run("saldo belum ada", func(t *testing.T) {
		in := base(service.MovementReserve, 1)
		in.ProductID = f.ProductC
		if _, err := apply(in); !errors.Is(err, service.ErrBalanceNotFound) {
			t.Fatalf("dapat %v", err)
		}
	})
	t.Run("fakta melebihi fisik ditolak CHECK, tx tetap bisa dipakai", func(t *testing.T) {
		err := f.DB.Transaction(func(tx *gorm.DB) error {
			if _, err := f.Inv.ApplyMovement(ctx, tx, base(service.MovementDirectSale, 6)); !errors.Is(err, service.ErrInsufficientStock) {
				t.Fatalf("ingin ErrInsufficientStock, dapat %v", err)
			}
			// Savepoint: transaksi luar masih hidup.
			_, err := f.Inv.ApplyMovement(ctx, tx, base(service.MovementDirectSale, 1))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if b := f.Balance(f.Central, f.ProductA); b.PhysicalQty != 4 {
			t.Fatalf("fisik %d, ingin 4", b.PhysicalQty)
		}
	})
	t.Run("ADJUST_COUNT dua arah", func(t *testing.T) {
		if _, err := apply(base(service.MovementAdjustCount, -2)); err != nil {
			t.Fatal(err)
		}
		if _, err := apply(base(service.MovementAdjustCount, 3)); err != nil {
			t.Fatal(err)
		}
		if b := f.Balance(f.Central, f.ProductA); b.PhysicalQty != 5 {
			t.Fatalf("fisik %d, ingin 5", b.PhysicalQty)
		}
	})
	t.Run("buffer online menahan janji", func(t *testing.T) {
		in := base(service.MovementReserve, 4)
		in.KeepAvailable = 2
		if _, err := apply(in); !errors.Is(err, service.ErrInsufficientStock) {
			t.Fatalf("available 5 - 4 < buffer 2 harus ditolak, dapat %v", err)
		}
		in = base(service.MovementReserve, 3)
		in.KeepAvailable = 2
		if _, err := apply(in); err != nil {
			t.Fatal(err)
		}
		release := base(service.MovementRelease, 3)
		if _, err := apply(release); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("idempotency key: sama = tanpa efek ganda, beda isi = ditolak", func(t *testing.T) {
		in := base(service.MovementQuarantineIn, 2)
		first, err := apply(in)
		if err != nil || first.Duplicate {
			t.Fatalf("pertama: %v dup=%v", err, first.Duplicate)
		}
		again, err := apply(in)
		if err != nil || !again.Duplicate || again.Movement.ID != first.Movement.ID {
			t.Fatalf("ulang: %v dup=%v id sama=%v", err, again.Duplicate, again.Movement.ID == first.Movement.ID)
		}
		if b := f.Balance(f.Central, f.ProductA); b.QuarantineQty != 2 {
			t.Fatalf("karantina %d, ingin 2 (tanpa efek ganda)", b.QuarantineQty)
		}
		changed := in
		changed.Qty = 3
		if _, err := apply(changed); !errors.Is(err, service.ErrIdempotencyKeyReused) {
			t.Fatalf("key sama isi beda: dapat %v", err)
		}
	})
	t.Run("idempotency key bersamaan: satu efek", func(t *testing.T) {
		in := base(service.MovementQuarantineOut, 1)
		var wg sync.WaitGroup
		var dup, fresh atomic.Int64
		for range 10 {
			wg.Go(func() {
				res, err := apply(in)
				if err != nil {
					t.Errorf("apply: %v", err)
					return
				}
				if res.Duplicate {
					dup.Add(1)
				} else {
					fresh.Add(1)
				}
			})
		}
		wg.Wait()
		if fresh.Load() != 1 || dup.Load() != 9 {
			t.Fatalf("baru %d duplikat %d, ingin 1 dan 9", fresh.Load(), dup.Load())
		}
		if b := f.Balance(f.Central, f.ProductA); b.QuarantineQty != 1 {
			t.Fatalf("karantina %d, ingin 1", b.QuarantineQty)
		}
	})
	t.Run("ledger append-only", func(t *testing.T) {
		err := f.DB.Exec(`UPDATE inventory_movements SET physical_delta = 999 WHERE movement_type = 'RECEIPT'`).Error
		if err == nil {
			t.Fatal("UPDATE ledger harus ditolak trigger")
		}
	})
	f.AssertLedgerInvariant()
}
