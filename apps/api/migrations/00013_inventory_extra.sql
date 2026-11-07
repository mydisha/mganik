-- +goose Up
-- Index tambahan untuk engine stok & order (Fase 3).

-- Sweeper order kedaluwarsa: hanya order yang masih menunggu pembayaran yang
-- dipindai, urut batas bayar. Dipakai bersama FOR NO KEY UPDATE SKIP LOCKED
-- supaya dua instance worker tidak memproses order yang sama.
CREATE INDEX orders_payment_due ON orders (payment_expires_at) WHERE status = 'PENDING_PAYMENT';

-- Dead-letter outbox: event yang gagal diproses N kali (OUTBOX_MAX_ATTEMPTS)
-- ditandai dead_at, tidak diambil dispatcher lagi, dan dihitung untuk
-- observabilitas. last_error menyimpan kegagalan terakhir.
ALTER TABLE outbox_events ADD COLUMN dead_at timestamptz;
ALTER TABLE outbox_events ADD CONSTRAINT outbox_dead_not_published CHECK (dead_at IS NULL OR published_at IS NULL);
-- Antrean dispatcher: belum terbit dan belum mati.
CREATE INDEX outbox_events_ready ON outbox_events (next_attempt_at) WHERE published_at IS NULL AND dead_at IS NULL;
CREATE INDEX outbox_events_dead ON outbox_events (dead_at) WHERE dead_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS outbox_events_dead;
DROP INDEX IF EXISTS outbox_events_ready;
ALTER TABLE outbox_events DROP CONSTRAINT IF EXISTS outbox_dead_not_published;
ALTER TABLE outbox_events DROP COLUMN IF EXISTS dead_at;
DROP INDEX IF EXISTS orders_payment_due;
