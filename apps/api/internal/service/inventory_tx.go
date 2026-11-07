package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// Helper bersama untuk domain stok/order. Identifier unexported diberi awalan
// inv supaya tidak bertabrakan dengan file domain lain di package service.

// LedgerActor adalah pelaku mutasi untuk ledger stok, riwayat order, dan audit.
type LedgerActor struct {
	Type string // USER, DEVICE, SYSTEM, CONSUMER
	ID   *uuid.UUID
}

// SystemLedgerActor dipakai worker dan webhook.
var SystemLedgerActor = LedgerActor{Type: model.ActorSystem}

// LedgerActorFromPrincipal memetakan principal ke pelaku: staff = user itu
// sendiri, device = device POS (kasir dicatat di level transaksi POS).
func LedgerActorFromPrincipal(p *auth.Principal) LedgerActor {
	if p == nil {
		return SystemLedgerActor
	}
	id := p.ID
	switch p.Kind {
	case auth.KindDevice:
		return LedgerActor{Type: model.ActorDevice, ID: &id}
	case auth.KindConsumer:
		return LedgerActor{Type: model.ActorConsumer, ID: &id}
	default:
		return LedgerActor{Type: model.ActorUser, ID: &id}
	}
}

// invScopeAllows adalah SATU-SATUNYA tempat cek scope lokasi domain stok &
// order (BOLA). nil principal → 401. Scope ALL → boleh. Selain itu principal
// wajib berwenang di SEMUA lokasi yang disebut; tanpa lokasi sama sekali
// hanya scope ALL yang boleh. (Setelah merge IAM, ganti isinya dengan
// auth.Principal.CanAt bila semantiknya berubah.)
func invScopeAllows(p *auth.Principal, locs ...uuid.UUID) error {
	if p == nil {
		return apperror.ErrUnauthenticated
	}
	if p.ScopeAll {
		return nil
	}
	if len(locs) == 0 {
		return apperror.ErrForbidden
	}
	for _, l := range locs {
		if !p.CanAccessLocation(l) {
			return apperror.ErrForbidden
		}
	}
	return nil
}

func invValidActorType(t string) bool {
	switch t {
	case model.ActorUser, model.ActorDevice, model.ActorSystem, model.ActorConsumer:
		return true
	}
	return false
}

// invMaxTxAttempts: transaksi yang gagal karena deadlock/serialization failure
// diulang dengan transaksi baru. Dengan urutan lock yang konsisten ini
// seharusnya tidak pernah terjadi; jumlahnya dipantau test lewat TxRetryCount.
const invMaxTxAttempts = 3

var invRetryCount atomic.Int64

// TxRetryCount mengembalikan jumlah transaksi stok/order yang diulang karena
// deadlock atau serialization failure sejak proses mulai.
func TxRetryCount() int64 { return invRetryCount.Load() }

// invRunTx menjalankan fn dalam satu transaksi dan mengulangnya (maks.
// invMaxTxAttempts) kalau Postgres membatalkannya karena deadlock/serialization.
func invRunTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	var err error
	for attempt := 1; attempt <= invMaxTxAttempts; attempt++ {
		err = db.WithContext(ctx).Transaction(fn)
		if err == nil || !repository.IsSerializationFailure(err) {
			return err
		}
		invRetryCount.Add(1)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt*10) * time.Millisecond):
		}
	}
	return err
}

// invSavepoint menjalankan fn di dalam SAVEPOINT. Kalau fn gagal, perubahan fn
// dibatalkan (ROLLBACK TO SAVEPOINT) dan transaksi luar tetap bisa dipakai.
// tx wajib transaksi (db.Transaction); di luar transaksi Postgres menolak SAVEPOINT.
func invSavepoint(tx *gorm.DB, name string, fn func() error) error {
	if err := tx.SavePoint(name).Error; err != nil {
		return fmt.Errorf("savepoint %s: %w", name, err)
	}
	if err := fn(); err != nil {
		if rbErr := tx.RollbackTo(name).Error; rbErr != nil {
			return errors.Join(err, fmt.Errorf("rollback ke savepoint %s: %w", name, rbErr))
		}
		return err
	}
	if err := tx.Exec("RELEASE SAVEPOINT " + name).Error; err != nil {
		return fmt.Errorf("release savepoint %s: %w", name, err)
	}
	return nil
}

func invNewID() uuid.UUID { return uuid.Must(uuid.NewV7()) }

func invPtr[T any](v T) *T { return &v }

// invCheckText memeriksa teks bebas dari klien (alasan, catatan, referensi
// eksternal, SKU, ...): UTF-8 valid dan tanpa karakter kontrol selain \n dan
// \t. NUL ditolak Postgres sebagai error 500, jadi harus berhenti di sini
// sebagai 422 invalid_characters. Argumen berpasangan: nama field, nilai.
func invCheckText(fieldValues ...string) error {
	var fields []apperror.FieldError
	for i := 0; i+1 < len(fieldValues); i += 2 {
		name, v := fieldValues[i], fieldValues[i+1]
		bad := !utf8.ValidString(v)
		for _, r := range v {
			if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				bad = true
				break
			}
		}
		if bad {
			fields = append(fields, apperror.FieldError{
				Field: name, Code: "invalid_characters", Message: "memuat karakter tak terlihat yang tidak diizinkan",
			})
		}
	}
	if len(fields) == 0 {
		return nil
	}
	e := apperror.New(http.StatusUnprocessableEntity, "invalid_characters",
		"Karakter tidak valid", "Teks memuat karakter tak terlihat (karakter kontrol) yang tidak diizinkan. Karakter ini sering ikut saat teks ditempel dari aplikasi lain; ketik ulang teksnya, lalu kirim lagi.")
	e.Fields = fields
	return e
}

// invTrimmed merapikan teks bebas (alasan, catatan) dan mengembalikan
// panjangnya dalam karakter (rune), bukan byte, supaya teks unicode adil.
func invTrimmed(s string) (string, int) {
	s = strings.TrimSpace(s)
	return s, len([]rune(s))
}
