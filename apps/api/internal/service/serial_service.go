package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
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

// SerialService memegang use case serialisasi: mint, commission, terima hasil
// produksi, agregasi, shipment, trace, recall, dan transisi status generik
// (termasuk SellSerials untuk POS). Semua transisi divalidasi terhadap
// lifecycle_transitions dan dieksekusi dengan conditional update.
type SerialService struct {
	db     *gorm.DB
	repo   *repository.SerialRepository
	ships  *repository.ShipmentRepository
	trace  *repository.TraceRepository
	clock  clock.Clock
	pepper []byte
	rand   io.Reader

	mu        sync.Mutex
	lifecycle *domain.Lifecycle
}

// NewSerialService membuat service. pepper = CLAIM_PEPPER untuk hash PIN.
func NewSerialService(db *gorm.DB, clk clock.Clock, pepper []byte) *SerialService {
	if clk == nil {
		clk = clock.Real{}
	}
	return &SerialService{
		db:     db,
		repo:   repository.NewSerialRepository(db),
		ships:  repository.NewShipmentRepository(),
		trace:  repository.NewTraceRepository(),
		clock:  clk,
		pepper: append([]byte(nil), pepper...),
		rand:   rand.Reader,
	}
}

// Lifecycle mengembalikan state machine serial dari lifecycle_transitions.
// Dimuat sekali lalu di-cache; gagal muat tidak di-cache supaya request
// berikutnya mencoba lagi. Perubahan transisi di DB berlaku setelah restart.
func (s *SerialService) Lifecycle(ctx context.Context) (*domain.Lifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifecycle != nil {
		return s.lifecycle, nil
	}
	ts, err := s.repo.LoadLifecycle(ctx)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, errors.New("lifecycle_transitions untuk SERIAL_UNIT kosong")
	}
	s.lifecycle = domain.NewLifecycle(ts)
	return s.lifecycle, nil
}

// inTx menjalankan fn di tx yang diberikan, atau membuka transaksi baru kalau
// tx nil. Dipakai method yang bisa dipanggil modul lain di transaksinya sendiri.
func (s *SerialService) inTx(ctx context.Context, tx *gorm.DB, fn func(tx *gorm.DB) error) error {
	if tx != nil {
		return fn(tx.WithContext(ctx))
	}
	return s.db.WithContext(ctx).Transaction(fn)
}

// EventRef menjelaskan dokumen pemicu transisi (ref_type/ref_id unit_events).
type EventRef struct {
	Type    string
	ID      *uuid.UUID
	Payload map[string]any
	// At: waktu kejadian; nol = jam server. POS mengisi jam transaksi device.
	At time.Time
}

func (s *SerialService) eventSpec(step string, actor SerialActor, ref EventRef) repository.UnitEventSpec {
	at := ref.At
	if at.IsZero() {
		at = s.clock.Now()
	}
	return repository.UnitEventSpec{
		BizStep: step, ActorType: actor.Type, ActorID: actor.UserID, DeviceID: actor.DeviceID,
		RefType: ref.Type, RefID: ref.ID, Payload: ref.Payload, At: at,
	}
}

// transitionGroups mengelompokkan status asal per biz_step untuk tujuan to,
// supaya setiap event mencatat langkah bisnis yang benar (mis. RETURNED →
// QUARANTINED = inspecting, IN_STOCK → QUARANTINED = holding). from dibatasi
// ke only kalau only tidak kosong.
func transitionGroups(lc *domain.Lifecycle, to string, only []string) map[string][]string {
	allowed := lc.AllowedFrom(to)
	if len(only) > 0 {
		keep := make(map[string]bool, len(only))
		for _, o := range only {
			keep[o] = true
		}
		filtered := allowed[:0]
		for _, f := range allowed {
			if keep[f] {
				filtered = append(filtered, f)
			}
		}
		allowed = filtered
	}
	groups := map[string][]string{}
	for _, from := range allowed {
		t, _ := lc.Lookup(from, to)
		groups[t.BizStep] = append(groups[t.BizStep], from)
	}
	return groups
}

// bulkTransition menjalankan spec untuk setiap kelompok biz_step dan
// mengembalikan total baris yang berubah.
func (s *SerialService) bulkTransition(tx *gorm.DB, lc *domain.Lifecycle, spec repository.SerialTransitionSpec, only []string, actor SerialActor, ref EventRef) (int64, error) {
	groups := transitionGroups(lc, spec.To, only)
	steps := make([]string, 0, len(groups))
	for step := range groups {
		steps = append(steps, step)
	}
	sort.Strings(steps)
	var total int64
	for _, step := range steps {
		g := spec
		g.AllowedFrom = groups[step]
		g.Event = s.eventSpec(step, actor, ref)
		n, err := s.repo.BulkTransition(tx, g)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// TransitionOptions mengatur TransitionSerials.
type TransitionOptions struct {
	// Principal: kalau diisi, permission tiap transisi (kolom
	// lifecycle_transitions.permission) dicek terhadap principal ini. nil =
	// pemanggil adalah modul lain yang sudah mengotorisasi aksinya sendiri.
	Principal *auth.Principal
	// Actor: pelaku di unit_events. Kosong = diturunkan dari Principal.
	Actor SerialActor
	// RequireLocation: semua serial wajib sedang berada di lokasi ini.
	RequireLocation *uuid.UUID
	// OnlyFrom: batasi status asal (irisan dengan lifecycle). Kosong = semua
	// status asal yang sah menurut lifecycle_transitions.
	OnlyFrom []string
	// OnlyLevel: kalau diisi, semua serial wajib berlevel ini (422 serial_not_unit
	// untuk UNIT).
	OnlyLevel string
	// DetachFromParent: lepas serial dari kemasan induknya (event unpacking
	// dengan payload alasan) di transaksi yang sama, sebelum transisi.
	DetachFromParent bool
	DetachReason     string
	Ref              EventRef
}

// TransitionedSerial adalah serial yang berhasil dipindah statusnya.
type TransitionedSerial struct {
	ID         uuid.UUID
	Code       string
	FromStatus string
	ProductID  *uuid.UUID
	BatchID    *uuid.UUID
	LocationID *uuid.UUID
}

// TransitionSerials memindahkan serial (berdasarkan kode, tanpa turunannya)
// ke status `to`. All-or-nothing: kalau satu kode tidak ada, salah lokasi,
// atau transisinya tidak sah, tidak ada yang berubah.
//
// Konkurensi: baris dikunci FOR UPDATE (urut id), lalu UPDATE bersyarat
// `WHERE status = ANY(allowed_from)`. Dua transaksi yang memindahkan serial
// yang sama berurutan di lock; yang kedua melihat status baru dan mendapat
// 409 invalid_transition.
//
// tx boleh nil (transaksi sendiri) atau transaksi pemanggil (mis. sync POS),
// sehingga perubahan serial ikut commit/rollback bersama perubahan stok.
func (s *SerialService) TransitionSerials(ctx context.Context, tx *gorm.DB, codes []string, to string, opt TransitionOptions) ([]TransitionedSerial, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	if err := RejectDuplicateCodes("codes", codes); err != nil {
		return nil, err
	}
	lc, err := s.Lifecycle(ctx)
	if err != nil {
		return nil, err
	}
	actor := opt.Actor
	if actor.Type == "" {
		actor = serialActorOf(opt.Principal)
	}
	var out []TransitionedSerial
	err = s.inTx(ctx, tx, func(tx *gorm.DB) error {
		rows, err := s.repo.LockByCodes(tx, codes)
		if err != nil {
			return err
		}
		idx := newSerialCodeIndex("codes", codes)
		if err := missingCodes(idx, codes, rows); err != nil {
			return err
		}
		only := map[string]bool{}
		for _, o := range opt.OnlyFrom {
			only[o] = true
		}
		var levelErrs, locErrs, transErrs []apperror.FieldError
		for _, r := range rows {
			if opt.OnlyLevel != "" && r.LevelCode != opt.OnlyLevel {
				levelErrs = append(levelErrs, idx.fieldError(r.Code, "serial_not_unit",
					fmt.Sprintf("serial berlevel %s, bukan %s", r.LevelCode, opt.OnlyLevel)))
				continue
			}
			if opt.RequireLocation != nil && (r.LocationID == nil || *r.LocationID != *opt.RequireLocation) {
				locErrs = append(locErrs, idx.fieldError(r.Code, "serial_wrong_location", "tercatat di lokasi lain"))
				continue
			}
			t, ok := lc.Lookup(r.Status, to)
			if !ok || r.Status == to || (len(only) > 0 && !only[r.Status]) {
				transErrs = append(transErrs, idx.fieldError(r.Code, "invalid_transition",
					fmt.Sprintf("berstatus %s, tidak bisa diubah menjadi %s", r.Status, to)))
				continue
			}
			if opt.Principal != nil && t.Permission != "" && !opt.Principal.Can(t.Permission) {
				return apperror.ErrForbidden
			}
		}
		if len(levelErrs) > 0 {
			return serialErrWithFields(ErrSerialNotUnit, levelErrs)
		}
		if len(locErrs) > 0 {
			return serialErrWithFields(ErrSerialWrongLocation, locErrs)
		}
		if len(transErrs) > 0 {
			return serialErrWithFields(ErrSerialInvalidTransition, transErrs)
		}
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		if opt.DetachFromParent {
			ref := opt.Ref
			ref.Payload = map[string]any{"reason": opt.DetachReason}
			if _, err := s.repo.DetachForSale(tx, ids, s.eventSpec("unpacking", actor, ref)); err != nil {
				return err
			}
		}
		n, err := s.bulkTransition(tx, lc, repository.SerialTransitionSpec{
			Roots: ids, To: to, RequireLocation: opt.RequireLocation,
		}, opt.OnlyFrom, actor, opt.Ref)
		if err != nil {
			return err
		}
		if n != int64(len(rows)) {
			return ErrSerialConcurrentChange
		}
		out = make([]TransitionedSerial, len(rows))
		for i, r := range rows {
			out[i] = TransitionedSerial{
				ID: r.ID, Code: r.Code, FromStatus: r.Status,
				ProductID: r.ProductID, BatchID: r.BatchID, LocationID: r.LocationID,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SaleRef menjelaskan penjualan yang memicu SellSerials.
type SaleRef struct {
	// RefType/RefID: dokumen penjualan, mis. "POS_TRANSACTION" + id transaksi
	// server, atau "ORDER" + id order.
	RefType string
	RefID   uuid.UUID
	// Actor: pelaku (device POS + kasir, atau user). Kosong = SYSTEM.
	Actor SerialActor
	// OccurredAt: waktu penjualan sebenarnya (jam device untuk POS offline).
	// Nol = jam server.
	OccurredAt time.Time
}

// SellSerials menandai serial terjual: IN_STOCK → SOLD, hanya untuk level
// UNIT yang sedang berada di locationID. Dipakai POS (Fase 4) dan
// fulfillment order.
//
// Kontrak:
//   - tx: transaksi pemanggil (wajib untuk POS supaya deduksi stok dan status
//     serial atomik), atau nil untuk transaksi sendiri.
//   - All-or-nothing. Error yang mungkin (semua *apperror.Error):
//     422 duplicate_code (kode sama dua kali), 404 serial_not_found,
//     422 serial_not_unit (karton/palet), 409 serial_wrong_location,
//     409 invalid_transition (bukan IN_STOCK, termasuk sudah SOLD),
//     409 concurrent_change.
//   - Dua pemanggil bersamaan untuk serial yang sama: tepat satu sukses.
//   - Unit dilepas dari karton induknya (parent_id NULL + event unpacking
//     dengan payload reason "sale") di transaksi yang sama, supaya karton
//     yang tersisa tetap bisa dikirim/dibongkar.
//   - Tidak mengecek permission (pemanggil sudah mengotorisasi device/user dan
//     lokasi).
//   - Mengembalikan product_id per serial untuk mutasi stok pemanggil.
func (s *SerialService) SellSerials(ctx context.Context, tx *gorm.DB, codes []string, locationID uuid.UUID, ref SaleRef) ([]TransitionedSerial, error) {
	actor := ref.Actor
	if actor.Type == "" {
		actor = SerialSystemActor
	}
	refID := ref.RefID
	return s.TransitionSerials(ctx, tx, codes, domain.SerialSold, TransitionOptions{
		Actor:            actor,
		RequireLocation:  &locationID,
		OnlyFrom:         []string{domain.SerialInStock},
		OnlyLevel:        domain.LevelUnit,
		DetachFromParent: true,
		DetachReason:     "sale",
		Ref:              EventRef{Type: ref.RefType, ID: &refID, At: ref.OccurredAt},
	})
}

// missingCodes mengembalikan 404 serial_not_found berisi kode yang tidak ada.
func missingCodes(idx serialCodeIndex, codes []string, rows []model.SerialUnit) error {
	if len(rows) == len(codes) {
		return nil
	}
	found := make(map[string]bool, len(rows))
	for _, r := range rows {
		found[r.Code] = true
	}
	var fields []apperror.FieldError
	for _, c := range codes {
		if !found[c] {
			fields = append(fields, idx.fieldError(c, "serial_not_found", "kode tidak terdaftar"))
		}
	}
	return serialErrWithFields(ErrSerialNotFound, fields)
}

// rootsOf memastikan setiap serial yang diminta adalah akar pohon: tidak
// punya induk, atau induknya juga ada di permintaan (anak itu lalu dilewati
// karena ikut lewat induknya). Serial dengan induk di luar permintaan
// ditolak: kirim/terima induknya, atau bongkar dulu.
func rootsOf(idx serialCodeIndex, rows []model.SerialUnit) ([]model.SerialUnit, error) {
	inSet := make(map[uuid.UUID]bool, len(rows))
	for _, r := range rows {
		inSet[r.ID] = true
	}
	roots := make([]model.SerialUnit, 0, len(rows))
	var fields []apperror.FieldError
	for _, r := range rows {
		switch {
		case r.ParentID == nil:
			roots = append(roots, r)
		case inSet[*r.ParentID]:
			// ikut lewat induknya
		default:
			fields = append(fields, idx.fieldError(r.Code, "packed_in_parent", "masih di dalam kemasan induk"))
		}
	}
	if len(fields) > 0 {
		return nil, serialErrWithFields(ErrSerialPackedInParent, fields)
	}
	return roots, nil
}

func serialIDs(rows []model.SerialUnit) []uuid.UUID {
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func sameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// mapSerialConstraint memetakan pelanggaran constraint DB serialisasi ke
// error domain (lapis kedua di belakang validasi service).
func mapSerialConstraint(err error) error {
	name, ok := repository.ConstraintViolation(err)
	if !ok {
		return err
	}
	switch name {
	case "serial_parent_level_higher":
		return ErrSerialLevelMismatch
	case "serial_not_own_parent":
		return ErrSerialCycleDetected
	case "one_active_claim_per_unit":
		return ErrClaimAlreadyClaimed
	}
	return err
}
