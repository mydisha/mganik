package repository

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"mganik/api/internal/model"
)

// IdempotencyRepository mengelola idempotency_keys (Idempotency-Key HTTP).
type IdempotencyRepository struct{}

// TryInsert mencoba mengklaim (scope, key) dengan state IN_PROGRESS.
// inserted=false berarti key sudah ada.
func (IdempotencyRepository) TryInsert(db *gorm.DB, k *model.IdempotencyKey) (bool, error) {
	res := db.Exec(`
		INSERT INTO idempotency_keys (id, scope, key, request_hash, state, created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?, 'IN_PROGRESS', ?, ?, ?)
		ON CONFLICT ON CONSTRAINT idempotency_key_once DO NOTHING`,
		k.ID, k.Scope, k.Key, k.RequestHash, k.CreatedAt, k.UpdatedAt, k.ExpiresAt)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// Get membaca key (nil kalau tidak ada).
func (IdempotencyRepository) Get(db *gorm.DB, scope, key string) (*model.IdempotencyKey, error) {
	var k model.IdempotencyKey
	err := db.Where("scope = ? AND key = ?", scope, key).Take(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// DeleteReclaimable menghapus key yang sudah kedaluwarsa, atau masih
// IN_PROGRESS tetapi tidak disentuh sejak staleBefore (proses pemiliknya mati).
func (IdempotencyRepository) DeleteReclaimable(db *gorm.DB, id uuid.UUID, now, staleBefore time.Time) (bool, error) {
	res := db.Exec(`
		DELETE FROM idempotency_keys
		 WHERE id = ? AND (expires_at <= ? OR (state = 'IN_PROGRESS' AND updated_at < ?))`, id, now, staleBefore)
	return res.RowsAffected == 1, res.Error
}

// Release menghapus klaim IN_PROGRESS milik pemanggil setelah pemrosesan gagal,
// supaya klien boleh mencoba lagi dengan key yang sama.
func (IdempotencyRepository) Release(db *gorm.DB, id uuid.UUID) error {
	return db.Exec(`DELETE FROM idempotency_keys WHERE id = ? AND state = 'IN_PROGRESS'`, id).Error
}

// Complete menyimpan respons dan menandai DONE, di transaksi yang sama dengan
// efek bisnisnya. Mengembalikan body seperti yang tersimpan (jsonb
// menormalkan urutan key), supaya respons pertama dan replay identik byte per
// byte. stored=nil berarti klaim sudah diambil alih proses lain.
func (IdempotencyRepository) Complete(tx *gorm.DB, id uuid.UUID, code int, body model.JSON, now time.Time) (model.JSON, error) {
	var rows []struct{ ResponseBody model.JSON }
	err := tx.Raw(`
		UPDATE idempotency_keys
		   SET state = 'DONE', response_code = ?, response_body = ?, updated_at = ?
		 WHERE id = ? AND state = 'IN_PROGRESS'
		RETURNING response_body`, code, body, now, id).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0].ResponseBody, nil
}
