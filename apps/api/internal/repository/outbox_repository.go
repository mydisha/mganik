package repository

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// OutboxRepository menulis dan mengklaim outbox_events.
type OutboxRepository struct{}

// Insert menulis event di transaksi yang sama dengan perubahan datanya.
func (OutboxRepository) Insert(tx *gorm.DB, eventType, aggregateType string, aggregateID uuid.UUID,
	payload model.JSON, now time.Time) error {
	return tx.Create(&model.OutboxEvent{
		ID: uuid.Must(uuid.NewV7()), EventType: eventType, AggregateType: aggregateType,
		AggregateID: aggregateID, Payload: payload, NextAttemptAt: now, CreatedAt: now,
	}).Error
}

// ClaimNext mengunci satu event yang siap diproses (FOR UPDATE SKIP LOCKED):
// beberapa dispatcher paralel tidak pernah mengambil event yang sama. nil
// kalau antrean kosong.
func (OutboxRepository) ClaimNext(tx *gorm.DB, now time.Time) (*model.OutboxEvent, error) {
	var rows []model.OutboxEvent
	err := tx.Raw(`
		SELECT * FROM outbox_events
		 WHERE published_at IS NULL AND dead_at IS NULL AND next_attempt_at <= ?
		 ORDER BY next_attempt_at, id
		 LIMIT 1
		   FOR UPDATE SKIP LOCKED`, now).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// MarkPublished menandai event selesai diproses.
func (OutboxRepository) MarkPublished(tx *gorm.DB, id uuid.UUID, now time.Time) error {
	return tx.Exec(`UPDATE outbox_events SET published_at = ?, last_error = NULL WHERE id = ?`, now, id).Error
}

// MarkFailed mencatat kegagalan dan menjadwalkan percobaan berikutnya. Kalau
// jumlah percobaan mencapai maxAttempts, event ditandai mati (dead_at = now)
// dan tidak diambil lagi. dead=true kalau event baru saja mati.
func (OutboxRepository) MarkFailed(tx *gorm.DB, id uuid.UUID, lastError string, next, now time.Time, maxAttempts int) (bool, error) {
	var rows []struct{ DeadAt *time.Time }
	err := tx.Raw(`
		UPDATE outbox_events
		   SET attempts = attempts + 1, last_error = ?, next_attempt_at = ?,
		       dead_at = CASE WHEN attempts + 1 >= ? THEN CAST(? AS timestamptz) END
		 WHERE id = ?
		RETURNING dead_at`, lastError, next, maxAttempts, now, id).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return false, err
	}
	return rows[0].DeadAt != nil, nil
}

// CountDead menghitung event dead-letter (untuk observabilitas/alert).
func (OutboxRepository) CountDead(db *gorm.DB) (int64, error) {
	var n int64
	err := db.Raw(`SELECT count(*) FROM outbox_events WHERE dead_at IS NOT NULL`).Scan(&n).Error
	return n, err
}
