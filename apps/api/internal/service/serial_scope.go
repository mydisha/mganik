package service

import (
	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
)

// serialCanAt adalah SATU-SATUNYA titik cek "permission perm di lokasi loc"
// (BOLA) untuk Fase 2. Setelah IAM dengan auth.Principal.CanAt masuk, isi
// fungsi ini cukup diganti `return p.CanAt(perm, loc)`.
func serialCanAt(p *auth.Principal, perm string, loc uuid.UUID) bool {
	return p != nil && p.Can(perm) && p.CanAccessLocation(loc)
}

// requireSerialScope mengembalikan 403 kalau principal tidak boleh memakai
// perm terhadap serial di lokasi loc. Serial tanpa lokasi (label GENERATED/
// COMMISSIONED yang belum diterima gudang) dianggap masih di line produksi
// gudang pusat: boleh untuk principal ber-scope ALL atau ber-scope ke salah
// satu lokasi bertipe CENTRAL_WH (tujuan penerimaan hasil produksi).
func (s *SerialService) requireSerialScope(tx *gorm.DB, p *auth.Principal, perm string, loc *uuid.UUID) error {
	if p == nil || !p.Can(perm) {
		return apperror.ErrForbidden
	}
	if loc != nil {
		if !serialCanAt(p, perm, *loc) {
			return apperror.ErrForbidden
		}
		return nil
	}
	if p.ScopeAll {
		return nil
	}
	ok, err := s.repo.AnyLocationOfType(tx, p.LocationIDs, "CENTRAL_WH")
	if err != nil {
		return err
	}
	if !ok {
		return apperror.ErrForbidden
	}
	return nil
}
