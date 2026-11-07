package service

import (
	"context"

	"gorm.io/gorm"

	"mganik/api/internal/apperror"
	"mganik/api/internal/auth"
	"mganik/api/internal/repository"
)

const (
	traceEventLimit = 500
	traceScanLimit  = 50
)

// TraceResult adalah genealogi lengkap satu serial.
type TraceResult struct {
	Unit        repository.TraceUnit
	Ancestors   []repository.TraceNode
	Descendants []repository.DescendantCount
	Events      []repository.TraceEvent
	Shipments   []repository.TraceShipment
	Scans       []repository.TraceScan
}

// Trace membaca genealogi serial: leluhur, ringkasan turunan, event, shipment
// (milik serial dan leluhurnya), custodian, dan scan terakhir. Satu query per
// bagian (tanpa N+1), dalam satu transaksi read-only agar konsisten.
// Principal ber-scope SELECTED hanya boleh melihat serial yang sedang berada
// di lokasinya (aturan serial tanpa lokasi: lihat requireSerialScope).
func (s *SerialService) Trace(ctx context.Context, p *auth.Principal, code string) (TraceResult, error) {
	if p == nil {
		return TraceResult{}, apperror.ErrForbidden
	}
	var res TraceResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET TRANSACTION READ ONLY").Error; err != nil {
			return err
		}
		unit, ok, err := s.trace.Unit(tx, code)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSerialNotFound
		}
		if err := s.requireSerialScope(tx, p, "serial.trace", unit.LocationID); err != nil {
			return err
		}
		res.Unit = unit
		if res.Ancestors, err = s.trace.Ancestors(tx, unit.ID); err != nil {
			return err
		}
		if res.Descendants, err = s.trace.Descendants(tx, unit.ID); err != nil {
			return err
		}
		if res.Events, err = s.trace.Events(tx, unit.ID, traceEventLimit); err != nil {
			return err
		}
		// Shipment dari riwayat event (milik unit dan leluhur historisnya lewat
		// event packing), jadi bukti tetap ada setelah karton/palet dibongkar.
		if res.Shipments, err = s.trace.Shipments(tx, unit.ID, unit.Code); err != nil {
			return err
		}
		res.Scans, err = s.trace.Scans(tx, unit.ID, traceScanLimit)
		return err
	})
	return res, err
}
