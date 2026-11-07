package service

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// MaxMintQty adalah jumlah kode maksimal per job cetak (satu request).
const MaxMintQty = 5000

// mintChunk: baris per statement INSERT saat mint.
const mintChunk = 1000

// MintInput adalah permintaan job cetak kode.
type MintInput struct {
	LevelCode     string
	Qty           int
	ProductID     *uuid.UUID
	PrinterVendor string
	Notes         *string
}

// MintedCode adalah satu kode hasil mint. PIN hanya untuk level UNIT dan
// hanya ada di respons pembuatan ini (bahan file cetak); DB hanya menyimpan
// HMAC-nya.
type MintedCode struct {
	Code string
	PIN  string
}

// MintResult adalah job cetak beserta kode plaintext-nya.
type MintResult struct {
	CodeBatch model.CodeBatch
	Codes     []MintedCode
}

// Mint membuat job cetak dan qty kode acak 80-bit (crypto/rand). Untuk level
// UNIT, setiap kode mendapat PIN 50-bit yang disimpan sebagai
// HMAC-SHA256(CLAIM_PEPPER, id ‖ PIN). Kode dan PIN plaintext hanya
// dikembalikan sekali di sini; tidak pernah ditulis ke log atau DB.
func (s *SerialService) Mint(ctx context.Context, p *auth.Principal, in MintInput) (MintResult, error) {
	if in.Qty <= 0 || in.Qty > MaxMintQty {
		return MintResult{}, apperror.Validation(apperror.FieldError{
			Field: "qty", Code: "max", Message: "harus 1 sampai " + apperror.FormatNumber(MaxMintQty)})
	}
	if in.LevelCode == domain.LevelUnit && len(s.pepper) == 0 {
		return MintResult{}, errors.New("CLAIM_PEPPER kosong: PIN klaim tidak bisa di-hash")
	}
	actor := serialActorOf(p)
	now := s.clock.Now()
	var res MintResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ok, err := s.repo.LevelExists(tx, in.LevelCode)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSerialLevelNotFound
		}
		if in.ProductID != nil {
			ok, err := s.repo.ProductExists(tx, *in.ProductID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrSerialProductNotFound
			}
		}
		cbID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		res.CodeBatch = model.CodeBatch{
			ID: cbID, LevelCode: in.LevelCode, ProductID: in.ProductID, Qty: in.Qty,
			PrinterVendor: in.PrinterVendor, IssuedAt: now, Notes: in.Notes, CreatedBy: actor.UserID,
		}
		if err := s.repo.CreateCodeBatch(tx, &res.CodeBatch); err != nil {
			return fmt.Errorf("menyimpan code_batches: %w", err)
		}

		codes, err := s.mintCodes(tx, cbID, in.LevelCode, in.Qty, actor, now)
		if err != nil {
			return err
		}
		res.Codes = codes
		return nil
	})
	if err != nil {
		return MintResult{}, err
	}
	return res, nil
}

// mintCodes membuat qty kode, menyisipkannya per potongan, lalu membuat ulang
// kode yang bertabrakan dengan kode lama (ON CONFLICT) sampai semua masuk.
func (s *SerialService) mintCodes(tx *gorm.DB, codeBatchID uuid.UUID, level string, qty int, actor SerialActor, now time.Time) ([]MintedCode, error) {
	withPIN := level == domain.LevelUnit
	type pending struct {
		id   uuid.UUID
		code string
		pin  string
	}
	seen := make(map[string]bool, qty)
	gen := func(id uuid.UUID) (pending, error) {
		for {
			code, err := domain.GenerateSerialCode(s.rand)
			if err != nil {
				return pending{}, err
			}
			if seen[code] {
				continue // tabrakan di dalam job yang sama
			}
			seen[code] = true
			pd := pending{id: id, code: code}
			if withPIN {
				if pd.pin, err = domain.GenerateClaimPIN(s.rand); err != nil {
					return pending{}, err
				}
			}
			return pd, nil
		}
	}

	all := make([]pending, qty)
	for i := range all {
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		if all[i], err = gen(id); err != nil {
			return nil, err
		}
	}

	ev := s.eventSpec("encoding", actor, EventRef{Type: "CODE_BATCH", ID: &codeBatchID, At: now})
	todo := make([]int, qty)
	for i := range todo {
		todo[i] = i
	}
	for attempt := 0; len(todo) > 0; attempt++ {
		if attempt >= 5 {
			return nil, fmt.Errorf("mint: %d kode masih bertabrakan setelah %d percobaan", len(todo), attempt)
		}
		var retry []int
		for start := 0; start < len(todo); start += mintChunk {
			chunk := todo[start:min(start+mintChunk, len(todo))]
			rows := make([]repository.MintRow, len(chunk))
			for j, i := range chunk {
				rows[j] = repository.MintRow{ID: all[i].id, Code: all[i].code}
				if withPIN {
					rows[j].HashHex = hex.EncodeToString(domain.ClaimSecretHash(s.pepper, all[i].id, all[i].pin))
				}
			}
			inserted, err := s.repo.InsertMinted(tx, codeBatchID, level, rows, ev)
			if err != nil {
				return nil, err
			}
			ok := make(map[uuid.UUID]bool, len(inserted))
			for _, id := range inserted {
				ok[id] = true
			}
			for _, i := range chunk {
				if !ok[all[i].id] {
					retry = append(retry, i)
				}
			}
		}
		for _, i := range retry {
			pd, err := gen(all[i].id)
			if err != nil {
				return nil, err
			}
			all[i] = pd
		}
		todo = retry
	}

	out := make([]MintedCode, qty)
	for i, pd := range all {
		out[i] = MintedCode{Code: pd.code, PIN: pd.pin}
	}
	return out, nil
}

// CommissionResult merangkum commission.
type CommissionResult struct {
	BatchID   uuid.UUID
	ProductID uuid.UUID
	Count     int
}

// Commission mengikat label GENERATED ke batch produksi (→ COMMISSIONED) dan
// mengisi produk/batch. Batch harus ACTIVE dan produk job cetak (kalau ada)
// harus sama dengan produk batch.
func (s *SerialService) Commission(ctx context.Context, p *auth.Principal, batchID uuid.UUID, codes []string) (CommissionResult, error) {
	if err := RejectDuplicateCodes("codes", codes); err != nil {
		return CommissionResult{}, err
	}
	lc, err := s.Lifecycle(ctx)
	if err != nil {
		return CommissionResult{}, err
	}
	actor := serialActorOf(p)
	var res CommissionResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock batch: commission tidak boleh lolos bersamaan dengan recall.
		batch, ok, err := s.repo.FindBatch(tx, batchID, true)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSerialBatchNotFound
		}
		if batch.Status != "ACTIVE" {
			return ErrSerialBatchNotActive
		}
		rows, err := s.repo.LockByCodes(tx, codes)
		if err != nil {
			return err
		}
		idx := newSerialCodeIndex("codes", codes)
		if err := missingCodes(idx, codes, rows); err != nil {
			return err
		}
		cbIDs := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			if r.CodeBatchID != nil {
				cbIDs = append(cbIDs, *r.CodeBatchID)
			}
		}
		cbs, err := s.repo.FindCodeBatches(tx, cbIDs)
		if err != nil {
			return err
		}
		var transErrs, prodErrs []apperror.FieldError
		for _, r := range rows {
			if !lc.CanTransition(r.Status, domain.SerialCommissioned) {
				transErrs = append(transErrs, idx.fieldError(r.Code, "invalid_transition",
					fmt.Sprintf("berstatus %s, jadi labelnya tidak bisa diikat ke batch", r.Status)))
				continue
			}
			if r.CodeBatchID != nil {
				if cb, ok := cbs[*r.CodeBatchID]; ok && cb.ProductID != nil && *cb.ProductID != batch.ProductID {
					prodErrs = append(prodErrs, idx.fieldError(r.Code, "product_mismatch", "label dicetak untuk produk lain"))
				}
			}
		}
		if len(transErrs) > 0 {
			return serialErrWithFields(ErrSerialInvalidTransition, transErrs)
		}
		if len(prodErrs) > 0 {
			return serialErrWithFields(ErrSerialProductMismatch, prodErrs)
		}
		n, err := s.bulkTransition(tx, lc, repository.SerialTransitionSpec{
			Roots: serialIDs(rows), To: domain.SerialCommissioned,
			SetProduct: &batch.ProductID, SetBatch: &batch.ID, SetCommissioned: true,
		}, []string{domain.SerialGenerated}, actor, EventRef{Type: "BATCH", ID: &batch.ID})
		if err != nil {
			return err
		}
		if n != int64(len(rows)) {
			return ErrSerialConcurrentChange
		}
		res = CommissionResult{BatchID: batch.ID, ProductID: batch.ProductID, Count: len(rows)}
		return nil
	})
	return res, err
}

// ReceiveResult merangkum penerimaan.
type ReceiveResult struct {
	LocationID uuid.UUID
	Roots      int
	Serials    int64
}

// ReceiveProduction menerima hasil produksi (COMMISSIONED → IN_STOCK) ke
// lokasi fisik milik sendiri, beserta seluruh turunan kemasan. custodian =
// party pemilik lokasi. Principal wajib punya scope lokasi tujuan.
func (s *SerialService) ReceiveProduction(ctx context.Context, p *auth.Principal, locationID uuid.UUID, codes []string) (ReceiveResult, error) {
	lc, err := s.Lifecycle(ctx)
	if err != nil {
		return ReceiveResult{}, err
	}
	if err := RejectDuplicateCodes("codes", codes); err != nil {
		return ReceiveResult{}, err
	}
	if !serialCanAt(p, "serial.receive", locationID) {
		return ReceiveResult{}, apperror.ErrForbidden
	}
	actor := serialActorOf(p)
	var res ReceiveResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loc, ok, err := s.repo.FindLocation(tx, locationID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSerialLocationNotFound
		}
		if !loc.IsOwn || loc.IsVirtual {
			return ErrSerialLocationNotOwn
		}
		rows, err := s.repo.LockByCodes(tx, codes)
		if err != nil {
			return err
		}
		idx := newSerialCodeIndex("codes", codes)
		if err := missingCodes(idx, codes, rows); err != nil {
			return err
		}
		roots, err := rootsOf(idx, rows)
		if err != nil {
			return err
		}
		tree, err := s.repo.LockTree(tx, serialIDs(roots))
		if err != nil {
			return err
		}
		var fields []apperror.FieldError
		for _, n := range tree {
			if n.Status != domain.SerialCommissioned || !lc.CanTransition(n.Status, domain.SerialInStock) {
				fields = append(fields, apperror.FieldError{Field: "codes", Code: "invalid_transition",
					Message: fmt.Sprintf("serial %s berstatus %s; hanya serial COMMISSIONED yang bisa diterima", n.Code, n.Status)})
			}
		}
		if len(fields) > 0 {
			return serialErrWithFields(ErrSerialInvalidTransition, fields)
		}
		n, err := s.bulkTransition(tx, lc, repository.SerialTransitionSpec{
			Roots: serialIDs(roots), Recursive: true, To: domain.SerialInStock,
			SetLocation: &loc.ID, SetCustodian: loc.PartyID,
		}, []string{domain.SerialCommissioned}, actor, EventRef{Type: "LOCATION", ID: &loc.ID})
		if err != nil {
			return err
		}
		if n != int64(len(tree)) {
			return ErrSerialConcurrentChange
		}
		res = ReceiveResult{LocationID: loc.ID, Roots: len(roots), Serials: n}
		return nil
	})
	return res, err
}

// RecallResult merangkum recall batch.
type RecallResult struct {
	BatchID         uuid.UUID
	RecalledAt      time.Time
	Quarantined     int64
	AlreadyRecalled bool
}

// RecallBatch menandai batch RECALLED dan memindahkan massal semua serial
// batch itu yang masih di rantai pasok ke QUARANTINED (status asal dibaca dari
// lifecycle_transitions). Unit di tangan konsumen (SOLD, CONSUMER_ACTIVATED)
// tetap; verifikasi menampilkan RECALLED dari status batch. Idempotent:
// recall kedua tidak mengubah apa pun.
func (s *SerialService) RecallBatch(ctx context.Context, p *auth.Principal, batchID uuid.UUID, reason string) (RecallResult, error) {
	lc, err := s.Lifecycle(ctx)
	if err != nil {
		return RecallResult{}, err
	}
	actor := serialActorOf(p)
	now := s.clock.Now()
	var res RecallResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		batch, ok, err := s.repo.FindBatch(tx, batchID, true)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSerialBatchNotFound
		}
		if batch.Status == "RECALLED" {
			res = RecallResult{BatchID: batch.ID, AlreadyRecalled: true}
			if batch.RecalledAt != nil {
				res.RecalledAt = *batch.RecalledAt
			}
			return nil
		}
		if _, err := s.repo.MarkBatchRecalled(tx, batch.ID, reason, now); err != nil {
			return fmt.Errorf("menandai batch recall: %w", err)
		}
		n, err := s.bulkTransition(tx, lc, repository.SerialTransitionSpec{
			BatchID: &batch.ID, To: domain.SerialQuarantined,
		}, nil, actor, EventRef{Type: "BATCH", ID: &batch.ID, At: now, Payload: map[string]any{"reason": "recall"}})
		if err != nil {
			return err
		}
		auditID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if err := s.repo.InsertAudit(tx, model.AuditLog{
			ID: auditID, ActorType: actor.Type, ActorID: actor.UserID, Action: "batch.recall",
			Entity: "batches", EntityID: &batch.ID,
			Before: model.MustJSON(map[string]any{"status": batch.Status}),
			After:  model.MustJSON(map[string]any{"status": "RECALLED", "quarantined": n}),
			Reason: &reason, At: now,
		}); err != nil {
			return err
		}
		res = RecallResult{BatchID: batch.ID, RecalledAt: now, Quarantined: n}
		return nil
	})
	return res, err
}
