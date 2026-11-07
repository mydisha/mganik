package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/model"
	"mganik/api/internal/repository"
)

// PosConflictResolutions: cara penyelesaian konflik alokasi yang diterima
// (allocation_conflicts.resolution). Catatan wajib untuk semuanya.
var PosConflictResolutions = []string{
	"CUSTOMER_COMPENSATED",    // pelanggan online diberi kompensasi (voucher/refund)
	"ORDER_CANCELLED",         // order online dibatalkan + refund
	"STOCK_RECOUNTED",         // selisih stok sudah di-opname
	"SERIAL_RECORD_CORRECTED", // data serial diperbaiki (salah lokasi/status)
	"FALSE_ALARM",             // ternyata bukan masalah
	"OTHER",
}

const (
	posMinResolveNote = 3
	posMaxResolveNote = 1000
)

// ConflictResolveInput adalah permintaan POST /cc/conflicts/{id}/resolve.
type ConflictResolveInput struct {
	ID         uuid.UUID
	Resolution string
	Note       string
	RequestID  string
}

// ResolveConflict menutup konflik OPEN → RESOLVED (permission
// conflict.resolve di lokasi konflik, dicek dengan CanAt). Baris dikunci lalu
// di-UPDATE bersyarat `WHERE status = 'OPEN'`: dari dua permintaan bersamaan
// hanya satu yang menang, yang lain 409. Konflik di luar scope dijawab 404
// (sama dengan /cc/anomalies/{id}: keberadaan objek tidak dibocorkan).
// Perubahan dan audit_logs ditulis dalam satu transaksi.
func (s *PosService) ResolveConflict(ctx context.Context, p *auth.Principal, in ConflictResolveInput) (repository.CCConflictRow, error) {
	var zero repository.CCConflictRow
	if p == nil {
		return zero, apperror.ErrUnauthenticated
	}
	if p.Kind != auth.KindStaff || !p.Can(PermConflictResolve) {
		return zero, apperror.ErrForbidden
	}
	var fields []apperror.FieldError
	if !slices.Contains(PosConflictResolutions, in.Resolution) {
		fields = append(fields, apperror.FieldError{Field: "resolution", Code: "oneof",
			Message: "harus salah satu dari: " + strings.Join(PosConflictResolutions, ", ")})
	}
	note, n := invTrimmed(in.Note)
	if n < posMinResolveNote || n > posMaxResolveNote {
		fields = append(fields, apperror.FieldError{Field: "note", Code: "length",
			Message: fmt.Sprintf("wajib diisi, %d sampai %s karakter", posMinResolveNote, apperror.FormatNumber(posMaxResolveNote))})
	}
	if len(fields) > 0 {
		return zero, apperror.Validation(fields...)
	}
	if err := invCheckText("note", note); err != nil {
		return zero, err
	}

	var out repository.CCConflictRow
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cur, err := s.repo.LockConflict(tx, in.ID)
		if err != nil {
			return err
		}
		if cur == nil || !p.CanAt(PermConflictResolve, cur.LocationID) {
			return ErrPosConflictNotFound
		}
		if cur.Status != "OPEN" {
			return ErrPosConflictNotOpen
		}
		// ORDER_CANCELLED hanya sah kalau order terkait memang sudah batal
		// atau kedaluwarsa; kalau tidak, catatan resolusi akan berbohong.
		if in.Resolution == "ORDER_CANCELLED" &&
			(cur.OrderStatus == nil || (*cur.OrderStatus != OrderStatusCancelled && *cur.OrderStatus != OrderStatusExpired)) {
			return ErrPosResolutionNotApplicable
		}
		now := s.clock.Now()
		actor := p.ID
		patch := model.MustJSON(map[string]any{"resolution_note": note})
		changed, err := s.repo.ResolveConflict(tx, in.ID, in.Resolution, patch, &actor, now)
		if err != nil {
			return err
		}
		if !changed {
			return ErrPosConflictNotOpen // tidak terjadi selama baris terkunci
		}
		if err := s.invRepo.InsertAudit(tx, posAudit(p, "allocation_conflict.resolved", "allocation_conflicts", in.ID,
			map[string]any{"status": cur.Status, "resolution": cur.Resolution, "kind": cur.Kind},
			map[string]any{"status": "RESOLVED", "resolution": in.Resolution}, &note, in.RequestID, now)); err != nil {
			return err
		}
		view, err := s.repo.ConflictView(tx, in.ID)
		if err != nil {
			return err
		}
		if view == nil {
			return ErrPosConflictNotFound
		}
		out = *view
		return nil
	})
	return out, err
}
