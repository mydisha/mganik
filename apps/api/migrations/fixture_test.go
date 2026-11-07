package migrations_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"testing"

	"github.com/google/uuid"

	"mganik/api/testutil"
)

// fx membuat baris minimal lewat SQL mentah (*sql.DB, bukan GORM) supaya error
// Postgres asli beserta nama constraint-nya bisa diperiksa.
type fx struct {
	t   *testing.T
	ctx context.Context
	db  *sql.DB
}

func newFx(t *testing.T) *fx {
	t.Helper()
	sqlDB, err := testutil.NewDB(t).DB()
	if err != nil {
		t.Fatal(err)
	}
	return &fx{t: t, ctx: context.Background(), db: sqlDB}
}

func (f *fx) exec(query string, args ...any) error {
	_, err := f.db.ExecContext(f.ctx, query, args...)
	return err
}

func (f *fx) must(query string, args ...any) {
	f.t.Helper()
	if err := f.exec(query, args...); err != nil {
		f.t.Fatalf("%v\nquery: %s", err, query)
	}
}

func (f *fx) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRowContext(f.ctx, query, args...).Scan(&n); err != nil {
		f.t.Fatalf("%v\nquery: %s", err, query)
	}
	return n
}

func newID() string { return uuid.NewString() }

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func (f *fx) product() string {
	brand, prod := newID(), newID()
	f.must(`INSERT INTO brands (id, code, name) VALUES ($1, $2, 'Brand')`, brand, "B-"+brand[:8])
	f.must(`INSERT INTO products (id, brand_id, sku, name) VALUES ($1, $2, $3, 'Produk')`, prod, brand, "SKU-"+prod[:8])
	return prod
}

func (f *fx) location() string {
	id := newID()
	f.must(`INSERT INTO locations (id, code, name, type_code) VALUES ($1, $2, 'Lokasi', 'OWN_STORE')`, id, "L-"+id[:8])
	return id
}

func (f *fx) balance(loc, prod string, physical int64) {
	f.must(`INSERT INTO inventory_balances (location_id, product_id, physical_qty) VALUES ($1, $2, $3)`, loc, prod, physical)
}

func (f *fx) user() string {
	id := newID()
	f.must(`INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'User', '$argon2id$v=19$m=8,t=1,p=1$c2FsdA$aGFzaA')`,
		id, "u-"+id[:8]+"@test.local")
	return id
}

func (f *fx) consumer() string {
	id := newID()
	f.must(`INSERT INTO consumers (id, phone_enc, phone_bidx) VALUES ($1, $2, $3)`, id, randomBytes(32), randomBytes(32))
	return id
}

func (f *fx) batch(prod string) string {
	id := newID()
	f.must(`INSERT INTO batches (id, product_id, lot_no, mfg_date, exp_date) VALUES ($1, $2, $3, '2026-10-01', '2028-10-01')`,
		id, prod, "LOT-"+id[:8])
	return id
}

// serialCode: 16 karakter Crockford acak (hanya untuk fixture test).
func serialCode() string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	b := randomBytes(16)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// serial membuat unit IN_STOCK yang terikat produk dan batch.
func (f *fx) serial(prod string) string {
	id := newID()
	f.must(`INSERT INTO serial_units (id, code, level_code, product_id, batch_id, status, commissioned_at)
	        VALUES ($1, $2, 'UNIT', $3, $4, 'IN_STOCK', now())`, id, serialCode(), prod, f.batch(prod))
	return id
}

// order membuat order D2C dengan satu line.
func (f *fx) order(prod string) (orderID, lineID string) {
	orderID, lineID = newID(), newID()
	f.must(`INSERT INTO orders (id, channel_code, status, subtotal, total, placed_at)
	        VALUES ($1, 'D2C_WEB', 'PENDING_PAYMENT', 10000, 10000, now())`, orderID)
	f.must(`INSERT INTO order_lines (id, order_id, product_id, qty, unit_price) VALUES ($1, $2, $3, 1, 10000)`, lineID, orderID, prod)
	return orderID, lineID
}

func (f *fx) posDevice(loc string) string {
	id := newID()
	f.must(`INSERT INTO pos_devices (id, location_id, name, token_hash) VALUES ($1, $2, 'POS', $3)`, id, loc, randomBytes(32))
	return id
}
