package repository

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// ShipmentRepository mengakses shipments dan shipment_items.
type ShipmentRepository struct{}

func NewShipmentRepository() *ShipmentRepository { return &ShipmentRepository{} }

// Create menyimpan shipment beserta item (akar palet/karton/unit).
func (r *ShipmentRepository) Create(tx *gorm.DB, sh *model.Shipment, items []model.ShipmentItem) error {
	if err := tx.Create(sh).Error; err != nil {
		return fmt.Errorf("menyimpan shipment: %w", err)
	}
	if len(items) > 0 {
		if err := tx.CreateInBatches(&items, 500).Error; err != nil {
			return fmt.Errorf("menyimpan shipment_items: %w", err)
		}
	}
	return nil
}

// Find membaca shipment; lock=true menguncinya FOR UPDATE.
func (r *ShipmentRepository) Find(tx *gorm.DB, id uuid.UUID, lock bool) (model.Shipment, bool, error) {
	var sh model.Shipment
	q := tx
	if lock {
		q = q.Clauses(serialLocking)
	}
	err := q.Where("id = ?", id).Take(&sh).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sh, false, nil
	}
	return sh, err == nil, err
}

// Items membaca item shipment, urut waktu dibuat.
func (r *ShipmentRepository) Items(tx *gorm.DB, shipmentID uuid.UUID) ([]model.ShipmentItem, error) {
	var items []model.ShipmentItem
	err := tx.Where("shipment_id = ?", shipmentID).Order("created_at, id").Find(&items).Error
	return items, err
}

// MarkReceived mengubah shipment SHIPPED → RECEIVED (conditional).
func (r *ShipmentRepository) MarkReceived(tx *gorm.DB, id uuid.UUID, at time.Time) (bool, error) {
	res := tx.Model(&model.Shipment{}).
		Where("id = ? AND status = 'SHIPPED'", id).
		Updates(map[string]any{
			"status": "RECEIVED", "received_at": at,
			"version": gorm.Expr("version + 1"), "updated_at": at,
		})
	return res.RowsAffected == 1, res.Error
}
