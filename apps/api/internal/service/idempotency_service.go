package service

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"mganik/api/internal/clock"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

const (
	// IdempotencyKeyTTL: lama respons disimpan untuk di-replay.
	IdempotencyKeyTTL = 24 * time.Hour
	// IdempotencyStaleAfter: klaim IN_PROGRESS yang tidak disentuh selama ini
	// dianggap ditinggal proses yang mati dan boleh diambil alih.
	IdempotencyStaleAfter = 2 * time.Minute
	// MaxIdempotencyKeyLen sama dengan CHECK idempotency_key_length.
	MaxIdempotencyKeyLen = 255
)

// IdempotencyService menerapkan header Idempotency-Key untuk POST pembuat data.
//
// Alur dua fase:
//  1. Klaim: INSERT (scope, key, request_hash, IN_PROGRESS) ON CONFLICT DO
//     NOTHING, commit sendiri. Kalau key sudah ada: hash beda → 422
//     idempotency_key_reused; DONE → replay respons tersimpan; IN_PROGRESS → 409.
//  2. Kerja: efek bisnis + UPDATE key menjadi DONE beserta respons dalam SATU
//     transaksi, jadi tidak pernah ada order tanpa respons tersimpan.
//
// Kalau kerja gagal, klaim dihapus supaya klien boleh mencoba lagi dengan key
// yang sama (tidak ada efek yang ter-commit).
type IdempotencyService struct {
	db    *gorm.DB
	clock clock.Clock
	repo  repository.IdempotencyRepository
}

func NewIdempotencyService(db *gorm.DB, clk clock.Clock) *IdempotencyService {
	if clk == nil {
		clk = clock.Real{}
	}
	return &IdempotencyService{db: db, clock: clk}
}

// ValidIdempotencyKey: 1-255 karakter ASCII yang terlihat (tanpa spasi/kontrol).
func ValidIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > MaxIdempotencyKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

// Do menjalankan fn sekali per (scope, key). fn mengembalikan status HTTP dan
// body respons (di-encode JSON lalu disimpan).
func (s *IdempotencyService) Do(ctx context.Context, scope, key string, requestHash []byte,
	fn func(tx *gorm.DB) (int, any, error)) (IdempotentResponse, error) {
	if !ValidIdempotencyKey(key) {
		return IdempotentResponse{}, ErrIdempotencyKeyRequired
	}
	db := s.db.WithContext(ctx)
	now := s.clock.Now()
	rec := model.IdempotencyKey{
		ID: invNewID(), Scope: scope, Key: key, RequestHash: requestHash,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(IdempotencyKeyTTL),
	}

	claimed := false
	for attempt := 0; attempt < 3 && !claimed; attempt++ {
		inserted, err := s.repo.TryInsert(db, &rec)
		if err != nil {
			return IdempotentResponse{}, err
		}
		if inserted {
			claimed = true
			break
		}
		existing, err := s.repo.Get(db, scope, key)
		if err != nil {
			return IdempotentResponse{}, err
		}
		if existing == nil {
			continue // dihapus pemiliknya barusan; coba klaim lagi
		}
		if !existing.ExpiresAt.After(now) || (existing.State == model.IdempotencyInProgress &&
			existing.UpdatedAt.Before(now.Add(-IdempotencyStaleAfter))) {
			if _, err := s.repo.DeleteReclaimable(db, existing.ID, now, now.Add(-IdempotencyStaleAfter)); err != nil {
				return IdempotentResponse{}, err
			}
			continue
		}
		if !bytes.Equal(existing.RequestHash, requestHash) {
			return IdempotentResponse{}, ErrIdempotencyKeyReused
		}
		if existing.State == model.IdempotencyDone && existing.ResponseCode != nil {
			return IdempotentResponse{Status: *existing.ResponseCode, Body: existing.ResponseBody, Replayed: true}, nil
		}
		return IdempotentResponse{}, ErrIdempotencyInProgress
	}
	if !claimed {
		return IdempotentResponse{}, ErrIdempotencyInProgress
	}

	var resp IdempotentResponse
	err := invRunTx(ctx, s.db, func(tx *gorm.DB) error {
		code, body, err := fn(tx)
		if err != nil {
			return err
		}
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		stored, err := s.repo.Complete(tx, rec.ID, code, model.JSON(b), s.clock.Now())
		if err != nil {
			return err
		}
		if stored == nil {
			// Klaim diambil alih proses lain (dianggap basi): batalkan efek kita.
			return ErrIdempotencyInProgress
		}
		resp = IdempotentResponse{Status: code, Body: stored}
		return nil
	})
	if err != nil {
		// Context request bisa sudah batal; pelepasan klaim tetap dijalankan.
		_ = s.repo.Release(s.db.WithContext(context.WithoutCancel(ctx)), rec.ID)
		return IdempotentResponse{}, err
	}
	return resp, nil
}
