package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/domain"
	"mganik/api/internal/model"
)

// packableStatus: status tempat agregasi/bongkar boleh dilakukan, yaitu saat
// barang masih di tangan sendiri (line produksi atau gudang/toko sendiri).
var packableStatus = map[string]bool{domain.SerialCommissioned: true, domain.SerialInStock: true}

// AggregateResult merangkum agregasi.
type AggregateResult struct {
	ParentCode      string
	Added           int
	AlreadyInParent int
	ChildCount      int64
	Capacity        int
}

// Aggregate memasukkan anak ke kemasan induk (unit → karton, karton → palet).
//
// Validasi: rank level induk > anak (DB juga menjaga lewat trigger), level
// induk sesuai packaging_specs produk, kapasitas ≤ qty_per_parent, produk
// sama, aturan campuran batch (allow_mixed_batch), anak belum punya induk
// lain, tidak ada siklus, dan induk + anak berstatus sama di lokasi yang sama
// (COMMISSIONED atau IN_STOCK). Induk dan anak dikunci FOR UPDATE (urut id)
// sebelum kapasitas dihitung, jadi agregasi paralel ke induk yang sama tidak
// bisa melampaui kapasitas. Anak yang sudah ada di induk yang sama dilewati
// (aman di-retry).
func (s *SerialService) Aggregate(ctx context.Context, p *auth.Principal, parentCode string, childCodes []string) (AggregateResult, error) {
	if err := RejectDuplicateCodes("child_codes", childCodes); err != nil {
		return AggregateResult{}, err
	}
	actor := serialActorOf(p)
	var res AggregateResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		all := append([]string{parentCode}, childCodes...)
		rows, err := s.repo.LockByCodes(tx, all)
		if err != nil {
			return err
		}
		byCode := make(map[string]model.SerialUnit, len(rows))
		for _, r := range rows {
			byCode[r.Code] = r
		}
		parent, ok := byCode[parentCode]
		if !ok {
			return serialErrWithFields(ErrSerialNotFound, []apperror.FieldError{
				{Field: "parent_code", Code: "serial_not_found", Message: "kode tidak terdaftar"}})
		}
		idx := newSerialCodeIndex("child_codes", childCodes)
		var missing []apperror.FieldError
		for _, c := range childCodes {
			if c == parentCode {
				return serialErrWithFields(ErrSerialCycleDetected, []apperror.FieldError{
					idx.fieldError(c, "cycle_detected", "induk tidak boleh menjadi anaknya sendiri")})
			}
			if _, ok := byCode[c]; !ok {
				missing = append(missing, idx.fieldError(c, "serial_not_found", "kode tidak terdaftar"))
			}
		}
		if len(missing) > 0 {
			return serialErrWithFields(ErrSerialNotFound, missing)
		}
		if err := s.requireSerialScope(tx, p, "serial.aggregate", parent.LocationID); err != nil {
			return err
		}
		if !packableStatus[parent.Status] {
			return serialErrWithDetail(ErrSerialStatusMismatch, "Induk berstatus %s; agregasi hanya untuk COMMISSIONED atau IN_STOCK.", parent.Status)
		}

		ranks, err := s.repo.LevelRanks(tx)
		if err != nil {
			return err
		}
		childLevel := ""
		var newIDs []uuid.UUID
		var levelErrs, statusErrs, productErrs, packedErrs []apperror.FieldError
		for _, c := range childCodes {
			ch := byCode[c]
			if childLevel == "" {
				childLevel = ch.LevelCode
			} else if ch.LevelCode != childLevel {
				return serialErrWithFields(ErrSerialMixedLevels, []apperror.FieldError{
					idx.fieldError(c, "mixed_levels", "level anak harus sama")})
			}
			if ranks[parent.LevelCode] <= ranks[ch.LevelCode] {
				levelErrs = append(levelErrs, idx.fieldError(c, "level_mismatch",
					fmt.Sprintf("%s tidak bisa dimasukkan ke %s", ch.LevelCode, parent.LevelCode)))
				continue
			}
			if ch.Status != parent.Status || !sameUUID(ch.LocationID, parent.LocationID) {
				statusErrs = append(statusErrs, idx.fieldError(c, "status_mismatch",
					"status/lokasi anak harus sama dengan induk"))
				continue
			}
			if !sameUUID(ch.ProductID, parent.ProductID) {
				productErrs = append(productErrs, idx.fieldError(c, "product_mismatch", "produk anak berbeda dengan induk"))
				continue
			}
			if ch.ParentID != nil {
				if *ch.ParentID == parent.ID {
					res.AlreadyInParent++
					continue
				}
				packedErrs = append(packedErrs, idx.fieldError(c, "already_packed", "sudah di kemasan lain"))
				continue
			}
			newIDs = append(newIDs, ch.ID)
		}
		for _, e := range []struct {
			base   *apperror.Error
			fields []apperror.FieldError
		}{
			{ErrSerialLevelMismatch, levelErrs}, {ErrSerialStatusMismatch, statusErrs},
			{ErrSerialProductMismatch, productErrs}, {ErrSerialAlreadyPacked, packedErrs},
		} {
			if len(e.fields) > 0 {
				return serialErrWithFields(e.base, e.fields)
			}
		}

		// Siklus: anak tidak boleh leluhur induk. Dengan aturan rank ini tidak
		// mungkin terjadi, tetapi dicek eksplisit sebagai pertahanan berlapis.
		ancestors, err := s.repo.AncestorIDs(tx, parent.ID)
		if err != nil {
			return err
		}
		anc := make(map[uuid.UUID]bool, len(ancestors))
		for _, a := range ancestors {
			anc[a] = true
		}
		for _, c := range childCodes {
			if anc[byCode[c].ID] {
				return serialErrWithFields(ErrSerialCycleDetected, []apperror.FieldError{
					idx.fieldError(c, "cycle_detected", "anak adalah leluhur induk")})
			}
		}

		if parent.ProductID == nil {
			return ErrSerialPackagingSpecMissing
		}
		spec, ok, err := s.repo.PackagingSpec(tx, *parent.ProductID, childLevel)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSerialPackagingSpecMissing
		}
		if spec.ParentLevel != parent.LevelCode {
			return serialErrWithDetail(ErrSerialLevelMismatch, "%s dikemas ke %s menurut spesifikasi produk, bukan %s.",
				childLevel, spec.ParentLevel, parent.LevelCode)
		}
		existing, err := s.repo.ChildCount(tx, parent.ID)
		if err != nil {
			return err
		}
		if existing+int64(len(newIDs)) > int64(spec.QtyPerParent) {
			return serialErrWithDetail(ErrSerialCapacityExceeded, "Kapasitas %s %d; sudah berisi %d, ditambah %d.",
				parent.LevelCode, spec.QtyPerParent, existing, len(newIDs))
		}
		if !spec.AllowMixedBatch {
			batches := map[uuid.UUID]bool{}
			if parent.BatchID != nil {
				batches[*parent.BatchID] = true
			}
			existingBatches, err := s.repo.ChildBatches(tx, parent.ID)
			if err != nil {
				return err
			}
			for _, b := range existingBatches {
				batches[b] = true
			}
			var mixErrs []apperror.FieldError
			for _, c := range childCodes {
				ch := byCode[c]
				if ch.BatchID == nil {
					continue
				}
				batches[*ch.BatchID] = true
				if len(batches) > 1 {
					mixErrs = append(mixErrs, idx.fieldError(c, "mixed_batch_not_allowed", "batch berbeda dengan isi kemasan"))
				}
			}
			if len(mixErrs) > 0 {
				return serialErrWithFields(ErrSerialMixedBatch, mixErrs)
			}
		}

		if len(newIDs) > 0 {
			n, err := s.repo.SetParent(tx, parent.ID, newIDs,
				s.eventSpec("packing", actor, EventRef{Type: "SERIAL_UNIT", ID: &parent.ID}))
			if err != nil {
				return mapSerialConstraint(err)
			}
			if n != int64(len(newIDs)) {
				return ErrSerialConcurrentChange
			}
		}
		res.ParentCode = parent.Code
		res.Added = len(newIDs)
		res.ChildCount = existing + int64(len(newIDs))
		res.Capacity = spec.QtyPerParent
		return nil
	})
	return res, err
}

// UnpackResult merangkum pembongkaran kemasan.
type UnpackResult struct {
	Code     string
	Unpacked int64
}

// Unpack mengeluarkan semua anak langsung dari kemasan (event unpacking per
// anak). Hanya untuk kemasan di tangan sendiri (COMMISSIONED/IN_STOCK) di
// lokasi dalam scope principal. Kemasan kosong = 0 (aman di-retry).
func (s *SerialService) Unpack(ctx context.Context, p *auth.Principal, code string) (UnpackResult, error) {
	actor := serialActorOf(p)
	var res UnpackResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := s.repo.LockByCodes(tx, []string{code})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrSerialNotFound
		}
		parent := rows[0]
		if err := s.requireSerialScope(tx, p, "serial.aggregate", parent.LocationID); err != nil {
			return err
		}
		if !packableStatus[parent.Status] {
			return serialErrWithDetail(ErrSerialStatusMismatch, "Kemasan berstatus %s; bongkar hanya untuk COMMISSIONED atau IN_STOCK.", parent.Status)
		}
		n, err := s.repo.DetachChildren(tx, parent.ID,
			s.eventSpec("unpacking", actor, EventRef{Type: "SERIAL_UNIT", ID: &parent.ID}))
		if err != nil {
			return err
		}
		res = UnpackResult{Code: parent.Code, Unpacked: n}
		return nil
	})
	return res, err
}
