package service

import (
	"context"
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

// CreateShipmentInput adalah permintaan kirim palet/karton/unit.
type CreateShipmentInput struct {
	FromLocationID uuid.UUID
	ToLocationID   uuid.UUID
	ToPartyID      *uuid.UUID
	ProgramCode    *string
	Codes          []string
}

// ShipmentResult merangkum shipment.
type ShipmentResult struct {
	Shipment model.Shipment
	Items    []model.ShipmentItem
	// Serials: jumlah serial (semua level) yang berubah status.
	Serials int64
	// Units: jumlah serial level UNIT di dalam shipment.
	Units int64
	// Skipped: serial di pohon yang tidak ikut diterima (mis. dikarantina saat
	// di jalan karena recall).
	Skipped         int64
	AlreadyReceived bool
}

// CreateShipment mengirim serial (dan seluruh turunannya) dari lokasi asal:
// IN_STOCK/AT_PARTNER → IN_TRANSIT, custodian berpindah ke party tujuan.
// Propagasi ke ribuan unit di bawah palet terjadi dalam SATU statement
// (recursive CTE + UPDATE ... FROM + INSERT ... SELECT event per unit).
// Principal wajib punya scope lokasi asal (BOLA).
func (s *SerialService) CreateShipment(ctx context.Context, p *auth.Principal, in CreateShipmentInput) (ShipmentResult, error) {
	if in.FromLocationID == in.ToLocationID {
		return ShipmentResult{}, ErrShipmentSameLocation
	}
	if err := RejectDuplicateCodes("codes", in.Codes); err != nil {
		return ShipmentResult{}, err
	}
	if !serialCanAt(p, "shipment.create", in.FromLocationID) {
		return ShipmentResult{}, apperror.ErrForbidden
	}
	lc, err := s.Lifecycle(ctx)
	if err != nil {
		return ShipmentResult{}, err
	}
	actor := serialActorOf(p)
	now := s.clock.Now()
	var res ShipmentResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		from, err := s.activeLocation(tx, in.FromLocationID, "from_location_id")
		if err != nil {
			return err
		}
		to, err := s.activeLocation(tx, in.ToLocationID, "to_location_id")
		if err != nil {
			return err
		}
		toParty := to.PartyID
		if in.ToPartyID != nil {
			ok, err := s.repo.PartyExists(tx, *in.ToPartyID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrSerialPartyNotFound
			}
			// Ke lokasi sendiri, custodian harus pemilik lokasi itu; party lain
			// hanya masuk akal untuk lokasi mitra (mis. gudang titipan).
			if to.IsOwn && to.PartyID != nil && *to.PartyID != *in.ToPartyID {
				return ErrShipmentToPartyMismatch
			}
			toParty = in.ToPartyID
		}
		if toParty == nil {
			return ErrShipmentToPartyRequired
		}

		rows, err := s.repo.LockByCodes(tx, in.Codes)
		if err != nil {
			return err
		}
		idx := newSerialCodeIndex("codes", in.Codes)
		if err := missingCodes(idx, in.Codes, rows); err != nil {
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
		var locErrs, transErrs []apperror.FieldError
		for _, n := range tree {
			if n.LocationID == nil || *n.LocationID != from.ID {
				locErrs = append(locErrs, apperror.FieldError{Field: "codes", Code: "serial_wrong_location",
					Message: fmt.Sprintf("serial %s tidak berada di lokasi asal", n.Code)})
				continue
			}
			if !lc.CanTransition(n.Status, domain.SerialInTransit) {
				transErrs = append(transErrs, apperror.FieldError{Field: "codes", Code: "invalid_transition",
					Message: fmt.Sprintf("serial %s berstatus %s, tidak bisa dikirim", n.Code, n.Status)})
			}
		}
		if len(locErrs) > 0 {
			return serialErrWithFields(ErrSerialWrongLocation, locErrs)
		}
		if len(transErrs) > 0 {
			return serialErrWithFields(ErrSerialInvalidTransition, transErrs)
		}

		shID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		shCode, err := s.shipmentCode(now)
		if err != nil {
			return err
		}
		res.Shipment = model.Shipment{
			ID: shID, Code: shCode, FromLocationID: from.ID, ToLocationID: to.ID, ToPartyID: toParty,
			ProgramCode: in.ProgramCode, Status: "SHIPPED", ShippedAt: &now, CreatedBy: actor.UserID,
		}
		units := unitsPerRoot(tree)
		for _, r := range roots {
			if r.ProductID == nil {
				return serialErrWithFields(ErrSerialProductNotFound, []apperror.FieldError{
					idx.fieldError(r.Code, "product_not_found", "serial belum terikat produk")})
			}
			if units[r.ID] == 0 {
				return serialErrWithFields(ErrSerialEmptyContainer, []apperror.FieldError{
					idx.fieldError(r.Code, "empty_container", "kemasan tidak berisi unit")})
			}
			itemID, err := uuid.NewV7()
			if err != nil {
				return err
			}
			res.Items = append(res.Items, model.ShipmentItem{
				ID: itemID, ShipmentID: shID, SerialUnitID: r.ID, ProductID: *r.ProductID, Qty: int(units[r.ID]),
			})
			res.Units += units[r.ID]
		}
		if err := s.ships.Create(tx, &res.Shipment, res.Items); err != nil {
			return err
		}

		payload := map[string]any{"shipment_code": shCode}
		if in.ProgramCode != nil {
			payload["program_code"] = *in.ProgramCode
		}
		n, err := s.bulkTransition(tx, lc, repository.SerialTransitionSpec{
			Roots: serialIDs(roots), Recursive: true, To: domain.SerialInTransit,
			RequireLocation: &from.ID, SetCustodian: toParty,
		}, nil, actor, EventRef{Type: "SHIPMENT", ID: &shID, At: now, Payload: payload})
		if err != nil {
			return err
		}
		if n != int64(len(tree)) {
			return ErrSerialConcurrentChange
		}
		res.Serials = n
		return nil
	})
	return res, err
}

// ReceiveShipment menerima shipment di lokasi tujuan: serial IN_TRANSIT di
// pohon item → AT_PARTNER (lokasi mitra) atau IN_STOCK (lokasi sendiri),
// location_id = tujuan. Principal wajib punya scope lokasi tujuan. Idempotent:
// shipment yang sudah RECEIVED dikembalikan apa adanya.
func (s *SerialService) ReceiveShipment(ctx context.Context, p *auth.Principal, shipmentID uuid.UUID) (ShipmentResult, error) {
	lc, err := s.Lifecycle(ctx)
	if err != nil {
		return ShipmentResult{}, err
	}
	actor := serialActorOf(p)
	now := s.clock.Now()
	var res ShipmentResult
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sh, ok, err := s.ships.Find(tx, shipmentID, true)
		if err != nil {
			return err
		}
		if !ok {
			return ErrShipmentNotFound
		}
		if !serialCanAt(p, "shipment.receive", sh.ToLocationID) {
			return apperror.ErrForbidden
		}
		items, err := s.ships.Items(tx, sh.ID)
		if err != nil {
			return err
		}
		res.Shipment, res.Items = sh, items
		for _, it := range items {
			res.Units += int64(it.Qty)
		}
		switch sh.Status {
		case "RECEIVED":
			res.AlreadyReceived = true
			return nil
		case "SHIPPED":
		default:
			return ErrShipmentNotShipped
		}
		to, ok, err := s.repo.FindLocation(tx, sh.ToLocationID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSerialLocationNotFound
		}
		target := domain.SerialAtPartner
		if to.IsOwn {
			target = domain.SerialInStock
		}
		custodian := sh.ToPartyID
		if custodian == nil {
			custodian = to.PartyID
		}
		roots := make([]uuid.UUID, len(items))
		for i, it := range items {
			roots[i] = it.SerialUnitID
		}
		tree, err := s.repo.LockTree(tx, roots)
		if err != nil {
			return err
		}
		n, err := s.bulkTransition(tx, lc, repository.SerialTransitionSpec{
			Roots: roots, Recursive: true, To: target,
			SetLocation: &to.ID, SetCustodian: custodian,
		}, []string{domain.SerialInTransit}, actor, EventRef{Type: "SHIPMENT", ID: &sh.ID, At: now,
			Payload: map[string]any{"shipment_code": sh.Code}})
		if err != nil {
			return err
		}
		if _, err := s.ships.MarkReceived(tx, sh.ID, now); err != nil {
			return fmt.Errorf("menandai shipment diterima: %w", err)
		}
		res.Shipment.Status, res.Shipment.ReceivedAt = "RECEIVED", &now
		res.Serials = n
		res.Skipped = int64(len(tree)) - n
		return nil
	})
	return res, err
}

func (s *SerialService) activeLocation(tx *gorm.DB, id uuid.UUID, field string) (repository.SerialLocation, error) {
	loc, ok, err := s.repo.FindLocation(tx, id)
	if err != nil {
		return loc, err
	}
	if !ok {
		return loc, serialErrWithFields(ErrSerialLocationNotFound, []apperror.FieldError{
			{Field: field, Code: "location_not_found", Message: "lokasi tidak ditemukan"}})
	}
	if loc.IsVirtual {
		return loc, serialErrWithFields(ErrSerialLocationVirtual, []apperror.FieldError{
			{Field: field, Code: "location_virtual", Message: "lokasi virtual"}})
	}
	return loc, nil
}

// shipmentCode membuat kode surat jalan yang mudah dibaca: SH-YYYYMMDD-XXXXXXXX
// (40 bit acak per hari; UNIQUE di DB sebagai pengaman).
func (s *SerialService) shipmentCode(now time.Time) (string, error) {
	var b [5]byte
	if _, err := s.rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("membuat kode shipment: %w", err)
	}
	suffix, err := domain.EncodeCrockford(b[:], 8)
	if err != nil {
		return "", err
	}
	return "SH-" + now.Format("20060102") + "-" + suffix, nil
}

// unitsPerRoot menghitung serial level UNIT di bawah setiap akar pohon
// (termasuk akar itu sendiri kalau UNIT).
func unitsPerRoot(tree []repository.SerialTreeNode) map[uuid.UUID]int64 {
	parent := make(map[uuid.UUID]*uuid.UUID, len(tree))
	for _, n := range tree {
		parent[n.ID] = n.ParentID
	}
	out := map[uuid.UUID]int64{}
	for _, n := range tree {
		if n.LevelCode != domain.LevelUnit {
			continue
		}
		// Naik ke akar: node tanpa induk di dalam pohon ini.
		cur := n.ID
		for depth := 0; depth < 16; depth++ {
			p, ok := parent[cur]
			if !ok || p == nil {
				break
			}
			if _, inTree := parent[*p]; !inTree {
				break
			}
			cur = *p
		}
		out[cur]++
	}
	return out
}
