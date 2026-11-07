package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/clock"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// Kode tipe mutasi (movement_types.code) yang dipakai engine.
const (
	MovementReceipt           = "RECEIPT"
	MovementReserve           = "RESERVE"
	MovementRelease           = "RELEASE"
	MovementPreempt           = "PREEMPT"
	MovementCommitSale        = "COMMIT_SALE"
	MovementDirectSale        = "DIRECT_SALE"
	MovementQuarantineIn      = "QUARANTINE_IN"
	MovementQuarantineOut     = "QUARANTINE_OUT"
	MovementAdjustCount       = "ADJUST_COUNT"
	MovementAdjustDiscrepancy = "ADJUST_DISCREPANCY"
)

const (
	// MaxMovementQty adalah batas atas besaran satu mutasi (validasi boundary).
	MaxMovementQty = 1_000_000
	// invMaxFactKeyLen menyisakan ruang untuk sufiks turunan
	// (":discrepancy") di bawah batas 255 karakter kolom ledger.
	invMaxFactKeyLen = 200

	// Event outbox milik domain stok.
	EventReservationPreempted = "reservation.preempted.v1"
)

// InventoryService adalah engine mutasi stok generik: setiap mutasi dihitung
// dari movement_types, saldo diubah dalam satu statement, dan ledger ditulis
// di transaksi yang sama.
type InventoryService struct {
	db     *gorm.DB
	clock  clock.Clock
	inv    repository.InventoryRepository
	orders repository.OrderRepository
	outbox repository.OutboxRepository
}

func NewInventoryService(db *gorm.DB, clk clock.Clock) *InventoryService {
	if clk == nil {
		clk = clock.Real{}
	}
	return &InventoryService{db: db, clock: clk}
}

// MovementInput adalah satu mutasi stok.
type MovementInput struct {
	LocationID uuid.UUID
	ProductID  uuid.UUID
	// Type adalah movement_types.code.
	Type string
	// Qty adalah besaran mutasi (> 0). Hanya tipe allow_either_sign
	// (ADJUST_COUNT) yang boleh negatif untuk arah keluar.
	Qty            int64
	RefType        string
	RefID          uuid.UUID
	IdempotencyKey string
	Reason         *string
	Actor          LedgerActor
	// OccurredAt kosong = waktu clock service.
	OccurredAt time.Time
	// KeepAvailable hanya untuk tipe janji (is_promise): mutasi ditolak kalau
	// available sesudahnya < KeepAvailable (buffer online toko).
	KeepAvailable int64
}

// MovementResult adalah baris ledger beserta saldo sesudahnya. Duplicate=true
// berarti idempotency_key sudah pernah dipakai dengan isi yang sama: tidak ada
// efek baru, Movement adalah baris lama dan Balance adalah saldo saat ini.
type MovementResult struct {
	Movement  model.InventoryMovement
	Balance   model.InventoryBalance
	Duplicate bool
}

var errInvDuplicateRace = errors.New("idempotency_key dipakai transaksi lain yang baru commit")

// ApplyMovement menerapkan satu mutasi di dalam tx (wajib transaksi).
//
//   - Delta tiap bucket = sign dari movement_types × Qty; saldo di-update dalam
//     satu statement UPDATE ... FROM movement_types.
//   - Tipe janji (RESERVE) memakai conditional WHERE available >= qty (+buffer);
//     tidak ada baris yang berubah → ErrInsufficientStock.
//   - Fakta mengandalkan CHECK di DB; pelanggaran dipetakan ke ErrInsufficientStock.
//   - idempotency_key yang sudah ada → tidak ada efek ganda; hasil lama
//     dikembalikan (Duplicate=true). Key sama dengan isi beda → ErrIdempotencyKeyReused.
//
// Seluruh langkah berjalan di SAVEPOINT: kalau gagal, tx tetap bisa dipakai
// (misal alokasi order mencoba lokasi berikutnya).
func (s *InventoryService) ApplyMovement(ctx context.Context, tx *gorm.DB, in MovementInput) (MovementResult, error) {
	if err := invValidateMovement(in); err != nil {
		return MovementResult{}, err
	}
	now := s.clock.Now()
	occurred := in.OccurredAt
	if occurred.IsZero() {
		occurred = now
	}
	tx = tx.WithContext(ctx)

	var res MovementResult
	err := invSavepoint(tx, "inv_movement", func() error {
		if dup, err := s.duplicateMovement(tx, in); err != nil || dup != nil {
			if dup != nil {
				res = *dup
			}
			return err
		}
		bal, eff, updated, err := s.inv.ApplyBalanceEffect(tx, in.LocationID, in.ProductID, in.Type, in.Qty, in.KeepAvailable, now)
		if err != nil {
			return invMapStockError(err)
		}
		if !updated {
			// Transaksi lain dengan key yang sama mungkin baru saja commit.
			if dup, err := s.duplicateMovement(tx, in); err != nil || dup != nil {
				if dup != nil {
					res = *dup
				}
				return err
			}
			return s.diagnoseNoUpdate(tx, in)
		}
		m := model.InventoryMovement{
			ID: invNewID(), LocationID: in.LocationID, ProductID: in.ProductID, MovementType: in.Type,
			PhysicalDelta: eff.PhysicalDelta, ReservedDelta: eff.ReservedDelta, QuarantineDelta: eff.QuarantineDelta,
			RefType: in.RefType, RefID: in.RefID, IdempotencyKey: in.IdempotencyKey, Reason: in.Reason,
			ActorType: in.Actor.Type, ActorID: in.Actor.ID, OccurredAt: occurred, RecordedAt: now,
		}
		inserted, err := s.inv.InsertMovement(tx, &m)
		if err != nil {
			return invMapStockError(err)
		}
		if !inserted {
			// Kalah balapan dengan key yang sama: batalkan update saldo kita.
			return errInvDuplicateRace
		}
		res = MovementResult{Movement: m, Balance: bal}
		return nil
	})
	if errors.Is(err, errInvDuplicateRace) {
		dup, derr := s.duplicateMovement(tx, in)
		if derr != nil {
			return MovementResult{}, derr
		}
		if dup == nil {
			return MovementResult{}, fmt.Errorf("mutasi %s: key bentrok tetapi baris lama tidak ditemukan", in.IdempotencyKey)
		}
		return *dup, nil
	}
	if err != nil {
		return MovementResult{}, err
	}
	return res, nil
}

func invValidateMovement(in MovementInput) error {
	switch {
	case in.LocationID == uuid.Nil, in.ProductID == uuid.Nil, in.RefID == uuid.Nil,
		in.Type == "", in.RefType == "", len(in.RefType) > 64,
		in.Qty == 0, in.Qty > MaxMovementQty, in.Qty < -MaxMovementQty,
		in.KeepAvailable < 0, !invValidActorType(in.Actor.Type):
		return ErrInvalidMovement
	}
	if n := len(in.IdempotencyKey); n == 0 || n > 255 {
		return ErrInvalidMovement
	}
	return nil
}

// duplicateMovement mengembalikan hasil lama kalau key sudah ada.
func (s *InventoryService) duplicateMovement(tx *gorm.DB, in MovementInput) (*MovementResult, error) {
	m, err := s.inv.MovementByKey(tx, in.IdempotencyKey)
	if err != nil || m == nil {
		return nil, err
	}
	mt, err := s.inv.MovementType(tx, in.Type)
	if err != nil {
		return nil, err
	}
	same := mt != nil && m.LocationID == in.LocationID && m.ProductID == in.ProductID && m.MovementType == in.Type &&
		m.PhysicalDelta == int64(mt.PhysicalSign)*in.Qty &&
		m.ReservedDelta == int64(mt.ReservedSign)*in.Qty &&
		m.QuarantineDelta == int64(mt.QuarantineSign)*in.Qty
	if !same {
		return nil, ErrIdempotencyKeyReused
	}
	bal, err := s.inv.Balance(tx, in.LocationID, in.ProductID)
	if err != nil {
		return nil, err
	}
	res := &MovementResult{Movement: *m, Duplicate: true}
	if bal != nil {
		res.Balance = *bal
	}
	return res, nil
}

// diagnoseNoUpdate menjelaskan kenapa UPDATE saldo tidak mengenai baris.
func (s *InventoryService) diagnoseNoUpdate(tx *gorm.DB, in MovementInput) error {
	mt, err := s.inv.MovementType(tx, in.Type)
	if err != nil {
		return err
	}
	if mt == nil {
		return ErrMovementTypeUnknown
	}
	if !mt.AllowEitherSign && in.Qty <= 0 {
		return ErrInvalidMovement
	}
	bal, err := s.inv.Balance(tx, in.LocationID, in.ProductID)
	if err != nil {
		return err
	}
	if bal == nil {
		return ErrBalanceNotFound
	}
	return ErrInsufficientStock
}

// invMapStockError memetakan pelanggaran constraint stok ke error domain.
func invMapStockError(err error) error {
	name, ok := repository.ConstraintViolation(err)
	if !ok {
		return err
	}
	switch name {
	case "no_oversell", "physical_nonneg", "reserved_nonneg", "quarantine_nonneg":
		return ErrInsufficientStock
	case "movement_balance_fk", "reservation_balance_fk":
		return ErrBalanceNotFound
	case "movement_effect_matches_type", "movement_has_delta":
		return ErrInvalidMovement
	case "movement_idempotent":
		return ErrIdempotencyKeyReused
	}
	return err
}

// ------------------------------------------------------------------ fakta menang atas janji

// FactSaleInput adalah penjualan fisik yang sudah terjadi (POS, termasuk
// transaksi offline yang baru tersinkron).
type FactSaleInput struct {
	LocationID uuid.UUID
	ProductID  uuid.UUID
	Qty        int64
	// RefType/RefID menunjuk dokumen asal, misal POS_TRANSACTION + id.
	RefType string
	RefID   uuid.UUID
	// IdempotencyKey mutasi DIRECT_SALE (maks. 200 karakter); turunan
	// ":discrepancy" dipakai untuk penyesuaian selisih.
	IdempotencyKey string
	Actor          LedgerActor
	OccurredAt     time.Time
	// PosTransactionID opsional; diisi ke allocation_conflicts.
	PosTransactionID *uuid.UUID
}

// PreemptedReservation adalah reservasi online yang dikalahkan fakta penjualan.
type PreemptedReservation struct {
	ReservationID uuid.UUID
	OrderID       uuid.UUID
	OrderLineID   uuid.UUID
	OrderStatus   string
	Qty           int64
	ConflictID    uuid.UUID
}

// FactSaleResult adalah hasil RecordFactSale.
type FactSaleResult struct {
	// Sale adalah mutasi DIRECT_SALE.
	Sale      MovementResult
	Preempted []PreemptedReservation
	// DiscrepancyQty > 0 kalau stok fisik tercatat kurang dan ditambah lewat
	// ADJUST_DISCREPANCY sebelum penjualan dicatat.
	DiscrepancyQty        int64
	DiscrepancyConflictID *uuid.UUID
	// Duplicate=true: key DIRECT_SALE sudah pernah dicatat, tidak ada efek baru.
	Duplicate bool
}

// invFactSaleAttempts: berapa kali satu penjualan fakta dicoba ulang (dengan
// melepas savepoint) saat sebagian reservasi di lokasi itu masih dipegang
// transaksi lain, sebelum menyerah dengan ErrStockContended.
const invFactSaleAttempts = 20

var errInvFactSaleRetry = errors.New("reservasi sedang dipegang transaksi lain; coba lagi")

// ErrStockContended: reservasi di lokasi itu terus dipegang transaksi lain.
// Bisa di-retry dengan transaksi baru; TIDAK berarti penjualan ditolak karena stok.
var ErrStockContended = apperror.New(http.StatusServiceUnavailable, "stock_contended",
	"Stok sedang diproses", "Reservasi di lokasi ini sedang diubah transaksi lain. Coba lagi beberapa saat lagi.")

func invValidateFactSale(in FactSaleInput) error {
	if in.Qty <= 0 || in.Qty > MaxMovementQty || in.LocationID == uuid.Nil || in.ProductID == uuid.Nil ||
		in.RefID == uuid.Nil || in.RefType == "" || in.IdempotencyKey == "" || len(in.IdempotencyKey) > invMaxFactKeyLen ||
		!invValidActorType(in.Actor.Type) {
		return ErrInvalidMovement
	}
	return nil
}

// RecordFactSale mencatat penjualan fisik di dalam tx milik pemanggil
// (Fase 4: sync POS). Prinsip "fakta menang atas janji" (PLANNING §7.4):
//
//  1. Kunci (MENUNGGU) semua reservasi ACTIVE di (lokasi, produk), lalu baris
//     saldo (FOR NO KEY UPDATE). Urutan ini sama dengan jalur batal,
//     kedaluwarsa, dan fulfill (reservasi → saldo), jadi tidak deadlock, dan
//     transaksi yang sedang melepas reservasi ditunggu sampai selesai.
//  2. Kalau available < qty, preempt reservasi dengan urutan PENDING_PAYMENT
//     terbaru lalu PAID terbaru; di dalam tiap kelompok, reservasi terkecil
//     yang cukup menutup kekurangan didahulukan (sesedikit mungkin pelanggan
//     terdampak). Efeknya: movement PREEMPT, reservasi PREEMPTED,
//     allocation_conflicts OFFLINE_SALE_PREEMPT, outbox reservation.preempted.v1.
//  3. ADJUST_DISCREPANCY (+ konflik dan anomali STOCK_DISCREPANCY) hanya kalau
//     stok fisik benar-benar kurang setelah SEMUA reservasi di lokasi itu
//     di-preempt. Selama reserved_qty saldo lebih besar dari reservasi yang
//     bisa dikunci (masih dipegang transaksi lain), langkah dibatalkan ke
//     savepoint dan dicoba lagi; setelah invFactSaleAttempts kali hasilnya
//     ErrStockContended (bisa di-retry), tidak pernah selisih palsu.
//  4. Catat DIRECT_SALE.
//
// Kontrak untuk pemanggil (Fase 4):
//   - tx wajib transaksi; kalau error, rollback seluruh tx.
//   - Aman dipanggil beberapa kali untuk (lokasi, produk) yang SAMA dalam satu
//     tx (reservasi dan saldonya sudah kita pegang sejak panggilan pertama).
//   - Untuk BEBERAPA produk dalam satu tx, pakai RecordFactSales: memanggil
//     RecordFactSale berurutan untuk produk berbeda bisa menunggu reservasi
//     produk kedua sambil memegang saldo produk pertama (deadlock).
//   - IdempotencyKey sama → Duplicate=true tanpa efek baru.
//   - Penjualan fakta TIDAK PERNAH ditolak karena stok. Error hanya untuk
//     input tidak valid, ErrStockContended (retry), atau kegagalan sistem.
func (s *InventoryService) RecordFactSale(ctx context.Context, tx *gorm.DB, in FactSaleInput) (FactSaleResult, error) {
	if err := invValidateFactSale(in); err != nil {
		return FactSaleResult{}, err
	}
	return s.recordFactSale(ctx, tx.WithContext(ctx), in, true)
}

// RecordFactSales mencatat beberapa penjualan fakta (misal satu transaksi POS
// multi-produk) dalam satu tx. Reservasi SEMUA (lokasi, produk) dikunci lebih
// dulu urut (lokasi, produk), lalu tiap penjualan diproses urut (lokasi,
// produk) sehingga saldo juga dikunci berurutan. Hasil dikembalikan sesuai
// urutan input.
func (s *InventoryService) RecordFactSales(ctx context.Context, tx *gorm.DB, ins []FactSaleInput) ([]FactSaleResult, error) {
	for _, in := range ins {
		if err := invValidateFactSale(in); err != nil {
			return nil, err
		}
	}
	tx = tx.WithContext(ctx)
	order := make([]int, len(ins))
	for i := range order {
		order[i] = i
	}
	cmp := func(a, b FactSaleInput) int {
		if c := bytes.Compare(a.LocationID[:], b.LocationID[:]); c != 0 {
			return c
		}
		return bytes.Compare(a.ProductID[:], b.ProductID[:])
	}
	slices.SortStableFunc(order, func(i, j int) int { return cmp(ins[i], ins[j]) })
	for k, idx := range order {
		if k > 0 && cmp(ins[order[k-1]], ins[idx]) == 0 {
			continue
		}
		if err := s.inv.LockActiveReservationsAt(tx, ins[idx].LocationID, ins[idx].ProductID); err != nil {
			return nil, err
		}
	}
	out := make([]FactSaleResult, len(ins))
	for _, idx := range order {
		// Reservasi sudah terkunci; jangan menunggu lagi sambil memegang saldo
		// produk sebelumnya.
		res, err := s.recordFactSale(ctx, tx, ins[idx], false)
		if err != nil {
			return nil, err
		}
		out[idx] = res
	}
	return out, nil
}

func (s *InventoryService) recordFactSale(ctx context.Context, tx *gorm.DB, in FactSaleInput, waitReservations bool) (FactSaleResult, error) {
	for attempt := 1; attempt <= invFactSaleAttempts; attempt++ {
		var out FactSaleResult
		err := invSavepoint(tx, "inv_fact_sale", func() error {
			var err error
			out, err = s.recordFactSaleOnce(ctx, tx, in, waitReservations)
			return err
		})
		if !errors.Is(err, errInvFactSaleRetry) {
			return out, err
		}
		select {
		case <-ctx.Done():
			return FactSaleResult{}, ctx.Err()
		case <-time.After(time.Duration(min(attempt, 10)) * 5 * time.Millisecond):
		}
	}
	return FactSaleResult{}, ErrStockContended
}

func (s *InventoryService) recordFactSaleOnce(ctx context.Context, tx *gorm.DB, in FactSaleInput, waitReservations bool) (FactSaleResult, error) {
	if waitReservations {
		if err := s.inv.LockActiveReservationsAt(tx, in.LocationID, in.ProductID); err != nil {
			return FactSaleResult{}, err
		}
	}
	now := s.clock.Now() // setelah lock
	occurred := in.OccurredAt
	if occurred.IsZero() {
		occurred = now
	}
	snapshot, err := s.inv.Balance(tx, in.LocationID, in.ProductID)
	if err != nil {
		return FactSaleResult{}, err
	}
	if snapshot == nil {
		if err := s.inv.EnsureBalance(tx, in.LocationID, in.ProductID, now); err != nil {
			if name, ok := repository.ConstraintViolation(err); ok && strings.HasPrefix(name, "inventory_balances_") {
				if strings.Contains(name, "product") {
					return FactSaleResult{}, ErrStockProductNotFound
				}
				return FactSaleResult{}, ErrStockLocationNotFound
			}
			return FactSaleResult{}, err
		}
	}
	bal, err := s.inv.LockBalance(tx, in.LocationID, in.ProductID)
	if err != nil {
		return FactSaleResult{}, err
	}
	if bal == nil {
		return FactSaleResult{}, ErrBalanceNotFound
	}

	// Dicek setelah saldo terkunci: transaksi lain dengan key sama sudah commit.
	if m, err := s.inv.MovementByKey(tx, in.IdempotencyKey); err != nil {
		return FactSaleResult{}, err
	} else if m != nil {
		if m.MovementType != MovementDirectSale || m.LocationID != in.LocationID || m.ProductID != in.ProductID ||
			m.PhysicalDelta != -in.Qty {
			return FactSaleResult{}, ErrIdempotencyKeyReused
		}
		return FactSaleResult{Sale: MovementResult{Movement: *m, Balance: *bal, Duplicate: true}, Duplicate: true}, nil
	}

	var out FactSaleResult
	available := bal.PhysicalQty - bal.ReservedQty - bal.QuarantineQty
	if available < in.Qty {
		// Urut prioritas preempt. SKIP LOCKED karena saldo sudah kita pegang:
		// reservasi yang kita kunci di langkah 1 tetap ikut (lock milik sendiri).
		cands, err := s.inv.LockPreemptCandidates(tx, in.LocationID, in.ProductID)
		if err != nil {
			return FactSaleResult{}, err
		}
		var lockable int64
		for _, c := range cands {
			lockable += c.Qty
		}
		// reserved_qty = SUM reservasi ACTIVE (invariant). Selisihnya adalah
		// reservasi yang dipegang transaksi lain (dibuat/dikunci setelah
		// langkah 1). Selama itu terjadi dan stok yang terlihat tidak cukup,
		// jangan pernah mencatat selisih: lepas savepoint dan coba lagi.
		if available+lockable < in.Qty && bal.ReservedQty > lockable {
			return FactSaleResult{}, errInvFactSaleRetry
		}
		for _, c := range invPickPreempt(cands, in.Qty-available) {
			p, err := s.preemptReservation(ctx, tx, c, in, now)
			if err != nil {
				return FactSaleResult{}, err
			}
			out.Preempted = append(out.Preempted, p)
			available += c.Qty
		}
	}

	if available < in.Qty {
		d := in.Qty - available
		reason := fmt.Sprintf("Penjualan fakta %s melebihi stok fisik tercatat sebanyak %d", in.RefType, d)
		if _, err := s.ApplyMovement(ctx, tx, MovementInput{
			LocationID: in.LocationID, ProductID: in.ProductID, Type: MovementAdjustDiscrepancy, Qty: d,
			RefType: in.RefType, RefID: in.RefID, IdempotencyKey: in.IdempotencyKey + ":discrepancy",
			Reason: &reason, Actor: in.Actor, OccurredAt: occurred,
		}); err != nil {
			return FactSaleResult{}, err
		}
		conflictID := invNewID()
		details := model.MustJSON(map[string]any{"fact_ref_type": in.RefType, "fact_ref_id": in.RefID, "discrepancy_qty": d})
		if err := s.inv.InsertConflict(tx, &model.AllocationConflict{
			ID: conflictID, Kind: "STOCK_DISCREPANCY", PosTransactionID: in.PosTransactionID,
			LocationID: in.LocationID, ProductID: in.ProductID, Qty: &d, Status: "OPEN", Details: details,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return FactSaleResult{}, err
		}
		dedupe := fmt.Sprintf("STOCK_DISCREPANCY:%s:%s:%s", in.LocationID, in.ProductID, now.Format("2006-01-02"))
		evidence := model.MustJSON(map[string]any{
			"location_id": in.LocationID, "product_id": in.ProductID, "discrepancy_qty": d,
			"fact_ref_type": in.RefType, "fact_ref_id": in.RefID, "conflict_id": conflictID,
		})
		if err := s.inv.UpsertAnomaly(tx, "STOCK_DISCREPANCY", "LOCATION", in.LocationID, dedupe, evidence, now); err != nil {
			return FactSaleResult{}, err
		}
		out.DiscrepancyQty = d
		out.DiscrepancyConflictID = &conflictID
	}

	sale, err := s.ApplyMovement(ctx, tx, MovementInput{
		LocationID: in.LocationID, ProductID: in.ProductID, Type: MovementDirectSale, Qty: in.Qty,
		RefType: in.RefType, RefID: in.RefID, IdempotencyKey: in.IdempotencyKey, Actor: in.Actor, OccurredAt: occurred,
	})
	if err != nil {
		return FactSaleResult{}, err
	}
	out.Sale = sale
	return out, nil
}

// invPreemptRank: kelompok prioritas preempt (lebih kecil = dikalahkan lebih dulu).
func invPreemptRank(orderStatus string) int {
	switch orderStatus {
	case "PENDING_PAYMENT":
		return 0
	case "PAID":
		return 1
	}
	return 2
}

// invPickPreempt memilih reservasi yang di-preempt untuk menutup need.
// cands sudah urut prioritas (PENDING_PAYMENT terbaru, lalu PAID terbaru, lalu
// lainnya). Di dalam satu kelompok, reservasi TERKECIL yang cukup menutup sisa
// kekurangan dipilih (seri: yang lebih baru); kalau tidak ada yang cukup
// sendirian, yang terbaru diambil lalu sisa kekurangan dihitung ulang.
// Kelompok berikutnya baru disentuh kalau kelompok sebelumnya habis.
func invPickPreempt(cands []repository.PreemptCandidate, need int64) []repository.PreemptCandidate {
	var picked []repository.PreemptCandidate
	for start := 0; start < len(cands) && need > 0; {
		end := start
		for end < len(cands) && invPreemptRank(cands[end].OrderStatus) == invPreemptRank(cands[start].OrderStatus) {
			end++
		}
		pool := slices.Clone(cands[start:end])
		for need > 0 && len(pool) > 0 {
			best := -1
			for i, c := range pool {
				if c.Qty >= need && (best < 0 || c.Qty < pool[best].Qty) {
					best = i
				}
			}
			if best < 0 {
				best = 0
			}
			picked = append(picked, pool[best])
			need -= pool[best].Qty
			pool = slices.Delete(pool, best, best+1)
		}
		start = end
	}
	return picked
}

// preemptReservation mengalahkan satu reservasi demi penjualan fakta.
func (s *InventoryService) preemptReservation(ctx context.Context, tx *gorm.DB, c repository.PreemptCandidate,
	in FactSaleInput, now time.Time) (PreemptedReservation, error) {
	reason := fmt.Sprintf("Dikalahkan penjualan fakta %s %s", in.RefType, in.RefID)
	if _, err := s.ApplyMovement(ctx, tx, MovementInput{
		LocationID: c.LocationID, ProductID: c.ProductID, Type: MovementPreempt, Qty: c.Qty,
		RefType: "ORDER", RefID: c.OrderID, IdempotencyKey: "reservation:" + c.ID.String() + ":preempt",
		Reason: &reason, Actor: in.Actor,
	}); err != nil {
		return PreemptedReservation{}, err
	}
	closed, err := s.orders.CloseReservation(tx, c.ID, model.ReservationPreempted, "preempted_by_fact_sale", now)
	if err != nil {
		return PreemptedReservation{}, err
	}
	if !closed {
		return PreemptedReservation{}, fmt.Errorf("reservasi %s berubah status saat dikunci", c.ID)
	}
	conflictID := invNewID()
	qty := c.Qty
	details := model.MustJSON(map[string]any{
		"fact_ref_type": in.RefType, "fact_ref_id": in.RefID, "order_status": c.OrderStatus, "order_line_id": c.OrderLineID,
	})
	if err := s.inv.InsertConflict(tx, &model.AllocationConflict{
		ID: conflictID, Kind: "OFFLINE_SALE_PREEMPT", PosTransactionID: in.PosTransactionID, ReservationID: &c.ID,
		OrderID: &c.OrderID, LocationID: c.LocationID, ProductID: c.ProductID, Qty: &qty, Status: "OPEN",
		Details: details, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return PreemptedReservation{}, err
	}
	payload := model.MustJSON(PreemptedEvent{
		ReservationID: c.ID, OrderID: c.OrderID, OrderLineID: c.OrderLineID, LocationID: c.LocationID,
		ProductID: c.ProductID, Qty: c.Qty, ConflictID: conflictID, FactRefType: in.RefType, FactRefID: in.RefID,
		PreemptedAt: now,
	})
	if err := s.outbox.Insert(tx, EventReservationPreempted, "ORDER", c.OrderID, payload, now); err != nil {
		return PreemptedReservation{}, err
	}
	return PreemptedReservation{
		ReservationID: c.ID, OrderID: c.OrderID, OrderLineID: c.OrderLineID, OrderStatus: c.OrderStatus,
		Qty: c.Qty, ConflictID: conflictID,
	}, nil
}

// PreemptedEvent adalah payload reservation.preempted.v1.
type PreemptedEvent struct {
	ReservationID uuid.UUID `json:"reservation_id"`
	OrderID       uuid.UUID `json:"order_id"`
	OrderLineID   uuid.UUID `json:"order_line_id"`
	LocationID    uuid.UUID `json:"location_id"`
	ProductID     uuid.UUID `json:"product_id"`
	Qty           int64     `json:"qty"`
	ConflictID    uuid.UUID `json:"conflict_id"`
	FactRefType   string    `json:"fact_ref_type"`
	FactRefID     uuid.UUID `json:"fact_ref_id"`
	PreemptedAt   time.Time `json:"preempted_at"`
}

// ------------------------------------------------------------------ saldo untuk staff

// BalanceQuery adalah filter GET /inventory/balances.
type BalanceQuery struct {
	LocationID *uuid.UUID
	ProductID  *uuid.UUID
	Cursor     string
	Limit      int
}

// BalanceView adalah saldo satu (lokasi, produk) beserta ATP online.
type BalanceView struct {
	repository.BalanceRow
	OnlineATP int64
}

type BalancePage struct {
	Items      []BalanceView
	NextCursor string
}

const (
	DefaultBalancePageSize = 50
	MaxBalancePageSize     = 200
)

// ListBalances membaca saldo dengan pagination keyset dan menghormati scope
// lokasi principal: principal SELECTED hanya melihat lokasinya, dan filter
// lokasi di luar scope ditolak 403.
func (s *InventoryService) ListBalances(ctx context.Context, p *auth.Principal, q BalanceQuery) (BalancePage, error) {
	if p == nil {
		return BalancePage{}, apperror.ErrUnauthenticated
	}
	if q.LocationID != nil {
		if err := invScopeAllows(p, *q.LocationID); err != nil {
			return BalancePage{}, err
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultBalancePageSize
	}
	if limit > MaxBalancePageSize {
		limit = MaxBalancePageSize
	}
	f := repository.BalanceFilter{
		LocationID: q.LocationID, ProductID: q.ProductID, ScopeAll: p.ScopeAll,
		LocationIDs: p.LocationIDs, Limit: limit + 1,
	}
	if q.Cursor != "" {
		loc, prod, err := DecodeBalanceCursor(q.Cursor)
		if err != nil {
			return BalancePage{}, apperror.Validation(apperror.FieldError{
				Field: "cursor", Code: "invalid_cursor", Message: "cursor tidak valid",
			})
		}
		f.AfterLocationID, f.AfterProductID = &loc, &prod
	}
	rows, err := s.inv.ListBalances(s.db.WithContext(ctx), f)
	if err != nil {
		return BalancePage{}, err
	}
	page := BalancePage{Items: make([]BalanceView, 0, min(len(rows), limit))}
	for i, r := range rows {
		if i == limit {
			last := rows[limit-1]
			page.NextCursor = EncodeBalanceCursor(last.LocationID, last.ProductID)
			break
		}
		page.Items = append(page.Items, BalanceView{
			BalanceRow: r,
			OnlineATP:  domain.OnlineATP(r.AvailableQty, r.OnlineBufferQty, r.SyncState),
		})
	}
	return page, nil
}

// EncodeBalanceCursor membuat cursor opaque dari kunci keyset terakhir.
func EncodeBalanceCursor(loc, prod uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(loc.String() + "|" + prod.String()))
}

// DecodeBalanceCursor membaca cursor dari EncodeBalanceCursor.
func DecodeBalanceCursor(c string) (uuid.UUID, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	a, b, ok := strings.Cut(string(raw), "|")
	if !ok {
		return uuid.Nil, uuid.Nil, errors.New("format cursor salah")
	}
	loc, err := uuid.Parse(a)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	prod, err := uuid.Parse(b)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return loc, prod, nil
}

// ------------------------------------------------------------------ adjustment & karantina

// StockChangeInput adalah perubahan stok manual oleh staff.
type StockChangeInput struct {
	LocationID uuid.UUID
	ProductID  uuid.UUID
	// Qty: untuk adjustment = delta bertanda (≠ 0); untuk karantina = besaran > 0.
	Qty    int64
	Reason string
	// IdempotencyKey dari header; ledger memakai turunannya per principal.
	IdempotencyKey string
	RequestID      string
}

// StockChangeResult adalah hasil adjustment/karantina.
type StockChangeResult struct {
	Movement  model.InventoryMovement
	Balance   model.InventoryBalance
	Duplicate bool
}

const (
	invMinReasonLen = 3
	invMaxReasonLen = 500
)

// Adjust menerapkan stock opname (ADJUST_COUNT, ±) dengan alasan wajib dan audit log.
func (s *InventoryService) Adjust(ctx context.Context, p *auth.Principal, in StockChangeInput) (StockChangeResult, error) {
	return s.staffChange(ctx, p, in, MovementAdjustCount, "inventory.adjust")
}

// Quarantine memindahkan stok ke karantina (in=true) atau melepasnya.
func (s *InventoryService) Quarantine(ctx context.Context, p *auth.Principal, in StockChangeInput, toQuarantine bool) (StockChangeResult, error) {
	if in.Qty <= 0 {
		return StockChangeResult{}, apperror.Validation(apperror.FieldError{Field: "qty", Code: "gt", Message: "harus lebih besar dari 0"})
	}
	if toQuarantine {
		return s.staffChange(ctx, p, in, MovementQuarantineIn, "inventory.quarantine_in")
	}
	return s.staffChange(ctx, p, in, MovementQuarantineOut, "inventory.quarantine_out")
}

func (s *InventoryService) staffChange(ctx context.Context, p *auth.Principal, in StockChangeInput,
	movementType, action string) (StockChangeResult, error) {
	if p == nil {
		return StockChangeResult{}, apperror.ErrUnauthenticated
	}
	// BOLA: lokasi wajib di dalam scope principal.
	if err := invScopeAllows(p, in.LocationID); err != nil {
		return StockChangeResult{}, err
	}
	if err := invCheckText("reason", in.Reason); err != nil {
		return StockChangeResult{}, err
	}
	reason, n := invTrimmed(in.Reason)
	if n < invMinReasonLen || n > invMaxReasonLen {
		return StockChangeResult{}, apperror.Validation(apperror.FieldError{
			Field: "reason", Code: "length", Message: fmt.Sprintf("wajib diisi %d-%d karakter", invMinReasonLen, invMaxReasonLen),
		})
	}
	if in.Qty == 0 || in.Qty > MaxMovementQty || in.Qty < -MaxMovementQty {
		return StockChangeResult{}, apperror.Validation(apperror.FieldError{
			Field: "qty", Code: "range", Message: fmt.Sprintf("tidak boleh 0 dan maksimal %d", MaxMovementQty),
		})
	}
	if in.IdempotencyKey == "" {
		return StockChangeResult{}, ErrIdempotencyKeyRequired
	}
	actor := LedgerActorFromPrincipal(p)
	// Key ledger diturunkan dari principal + hash key klien: panjang tetap dan
	// key klien yang sama dari principal berbeda tidak bertabrakan.
	sum := sha256.Sum256([]byte(in.IdempotencyKey))
	ledgerKey := fmt.Sprintf("%s:%s:%s", action, p.ID, hex.EncodeToString(sum[:]))

	var out StockChangeResult
	err := invRunTx(ctx, s.db, func(tx *gorm.DB) error {
		now := s.clock.Now()
		if ok, err := s.inv.LocationExists(tx, in.LocationID); err != nil {
			return err
		} else if !ok {
			return ErrStockLocationNotFound
		}
		if ok, err := s.inv.ProductExists(tx, in.ProductID); err != nil {
			return err
		} else if !ok {
			return ErrStockProductNotFound
		}
		if in.Qty > 0 && movementType == MovementAdjustCount {
			if err := s.inv.EnsureBalance(tx, in.LocationID, in.ProductID, now); err != nil {
				return err
			}
		}
		before, err := s.inv.Balance(tx, in.LocationID, in.ProductID)
		if err != nil {
			return err
		}
		res, err := s.ApplyMovement(ctx, tx, MovementInput{
			LocationID: in.LocationID, ProductID: in.ProductID, Type: movementType, Qty: in.Qty,
			RefType: "STOCK_CHANGE", RefID: invNewID(), IdempotencyKey: ledgerKey, Reason: &reason, Actor: actor,
		})
		if err != nil {
			return err
		}
		out = StockChangeResult{Movement: res.Movement, Balance: res.Balance, Duplicate: res.Duplicate}
		if res.Duplicate {
			return nil
		}
		var reqID *string
		if in.RequestID != "" {
			reqID = &in.RequestID
		}
		return s.inv.InsertAudit(tx, &model.AuditLog{
			ID: invNewID(), ActorType: actor.Type, ActorID: actor.ID, Action: action,
			Entity: "inventory_movement", EntityID: &res.Movement.ID,
			Before: invBalanceJSON(before), After: invBalanceJSON(&res.Balance),
			Reason: &reason, RequestID: reqID, At: now,
		})
	})
	return out, err
}

func invBalanceJSON(b *model.InventoryBalance) model.JSON {
	if b == nil {
		return model.MustJSON(map[string]int64{"physical_qty": 0, "reserved_qty": 0, "quarantine_qty": 0})
	}
	return model.MustJSON(map[string]any{
		"location_id": b.LocationID, "product_id": b.ProductID, "physical_qty": b.PhysicalQty,
		"reserved_qty": b.ReservedQty, "quarantine_qty": b.QuarantineQty,
	})
}
