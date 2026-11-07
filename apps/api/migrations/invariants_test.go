package migrations_test

import (
	"testing"

	"mganik/api/testutil"
)

const sqlStateRestrictViolation = "23001" // dipakai forbid_mutation()

func TestInvariant_NoOversell(t *testing.T) {
	f := newFx(t)
	loc, prod := f.location(), f.product()
	f.balance(loc, prod, 5)

	// Reservasi melebihi fisik.
	err := f.exec(`UPDATE inventory_balances SET reserved_qty = 6 WHERE location_id = $1 AND product_id = $2`, loc, prod)
	testutil.RequireConstraint(t, err, "no_oversell")

	// Karantina + reservasi melebihi fisik.
	f.must(`UPDATE inventory_balances SET reserved_qty = 3 WHERE location_id = $1 AND product_id = $2`, loc, prod)
	err = f.exec(`UPDATE inventory_balances SET quarantine_qty = 3 WHERE location_id = $1 AND product_id = $2`, loc, prod)
	testutil.RequireConstraint(t, err, "no_oversell")

	// Bucket negatif ditolak per bucket.
	err = f.exec(`UPDATE inventory_balances SET reserved_qty = -1 WHERE location_id = $1 AND product_id = $2`, loc, prod)
	testutil.RequireConstraint(t, err, "reserved_nonneg")

	// Batas pas (available = 0) diterima, dan available_qty dihitung DB.
	f.must(`UPDATE inventory_balances SET quarantine_qty = 2 WHERE location_id = $1 AND product_id = $2`, loc, prod)
	if n := f.count(`SELECT available_qty FROM inventory_balances WHERE location_id = $1 AND product_id = $2`, loc, prod); n != 0 {
		t.Fatalf("available_qty %d, ingin 0", n)
	}
	// Kolom generated tidak bisa ditulis langsung.
	if err := f.exec(`UPDATE inventory_balances SET available_qty = 10 WHERE location_id = $1`, loc); err == nil {
		t.Fatal("available_qty seharusnya tidak bisa ditulis")
	}

	// Reservasi atomik §5.6: 0 baris berubah kalau stok tidak cukup.
	res, err := f.db.ExecContext(f.ctx, `
		UPDATE inventory_balances SET reserved_qty = reserved_qty + $3, version = version + 1, updated_at = now()
		 WHERE location_id = $1 AND product_id = $2 AND physical_qty - reserved_qty - quarantine_qty >= $3`, loc, prod, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("reservasi tanpa stok mengubah %d baris", n)
	}
}

func TestInvariant_AppendOnly(t *testing.T) {
	f := newFx(t)
	loc, prod := f.location(), f.product()
	f.balance(loc, prod, 0)
	serial := f.serial(prod)
	consumer := f.consumer()
	orderID, _ := f.order(prod)

	ids := map[string]string{}
	insert := map[string]string{
		"inventory_movements": `INSERT INTO inventory_movements (id, location_id, product_id, movement_type, physical_delta,
			ref_type, ref_id, idempotency_key, actor_type, occurred_at)
			VALUES ($1, '` + loc + `', '` + prod + `', 'RECEIPT', 1, 'TEST', $1, gen_random_uuid()::text, 'SYSTEM', now())`,
		"unit_events": `INSERT INTO unit_events (id, serial_unit_id, biz_step, actor_type, occurred_at)
			VALUES ($1, '` + serial + `', 'receiving', 'SYSTEM', now())`,
		"scan_events": `INSERT INTO scan_events (id, raw_code, context, result)
			VALUES ($1, 'KODEPALSU', 'CONSUMER_VERIFY', 'NOT_FOUND')`,
		"loyalty_ledger": `INSERT INTO loyalty_ledger (id, consumer_id, entry_type, points, ref_type, ref_id, idempotency_key)
			VALUES ($1, '` + consumer + `', 'EARN', 10, 'TEST', $1, gen_random_uuid()::text)`,
		"order_status_history": `INSERT INTO order_status_history (id, order_id, to_status, actor_type)
			VALUES ($1, '` + orderID + `', 'PENDING_PAYMENT', 'SYSTEM')`,
		"audit_logs": `INSERT INTO audit_logs (id, actor_type, action, entity) VALUES ($1, 'SYSTEM', 'test', 'test')`,
	}
	for table, q := range insert {
		id := newID()
		ids[table] = id
		f.must(q, id)
	}
	for table, id := range ids {
		t.Run(table, func(t *testing.T) {
			err := f.exec(`UPDATE `+table+` SET id = id WHERE id = $1`, id)
			testutil.RequireSQLState(t, err, sqlStateRestrictViolation)
			err = f.exec(`DELETE FROM `+table+` WHERE id = $1`, id)
			testutil.RequireSQLState(t, err, sqlStateRestrictViolation)
			err = f.exec(`TRUNCATE ` + table + ` CASCADE`)
			testutil.RequireSQLState(t, err, sqlStateRestrictViolation)
			if n := f.count(`SELECT count(*) FROM `+table+` WHERE id = $1`, id); n != 1 {
				t.Fatalf("baris hilang dari %s", table)
			}
		})
	}
}

func TestInvariant_MovementLedger(t *testing.T) {
	f := newFx(t)
	loc, prod := f.location(), f.product()
	f.balance(loc, prod, 0)
	move := `INSERT INTO inventory_movements (id, location_id, product_id, movement_type, physical_delta, reserved_delta,
		ref_type, ref_id, idempotency_key, actor_type, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'TEST', $1, $7, 'SYSTEM', now())`

	f.must(move, newID(), loc, prod, "RECEIPT", 5, 0, "key-1")
	// Idempotency key sama = mutasi yang sama, ditolak.
	testutil.RequireConstraint(t, f.exec(move, newID(), loc, prod, "RECEIPT", 5, 0, "key-1"), "movement_idempotent")
	// Tanda delta harus cocok dengan movement_types.
	testutil.RequireConstraint(t, f.exec(move, newID(), loc, prod, "RECEIPT", -5, 0, "key-2"), "movement_effect_matches_type")
	testutil.RequireConstraint(t, f.exec(move, newID(), loc, prod, "RESERVE", 1, 1, "key-3"), "movement_effect_matches_type")
	testutil.RequireConstraint(t, f.exec(move, newID(), loc, prod, "COMMIT_SALE", -1, 0, "key-4"), "movement_effect_matches_type")
	// Tipe multi-bucket satu arah: besaran harus sama di semua bucket.
	testutil.RequireConstraint(t, f.exec(move, newID(), loc, prod, "COMMIT_SALE", -5, -1, "key-4b"), "movement_effect_matches_type")
	testutil.RequireConstraint(t, f.exec(move, newID(), loc, prod, "COMMIT_SALE", -1, -5, "key-4c"), "movement_effect_matches_type")
	f.must(move, newID(), loc, prod, "COMMIT_SALE", -1, -1, "key-4d")
	// ADJUST_COUNT (opname) boleh dua arah.
	f.must(move, newID(), loc, prod, "ADJUST_COUNT", -2, 0, "key-5")
	f.must(move, newID(), loc, prod, "ADJUST_COUNT", 1, 0, "key-6")
	// Baris tanpa efek ditolak (trigger efek berjalan sebelum CHECK movement_has_delta).
	testutil.RequireConstraint(t, f.exec(move, newID(), loc, prod, "ADJUST_COUNT", 0, 0, "key-7"), "movement_effect_matches_type")
	// Ledger hanya untuk (lokasi, produk) yang punya baris saldo.
	testutil.RequireConstraint(t, f.exec(move, newID(), f.location(), prod, "RECEIPT", 1, 0, "key-8"), "movement_balance_fk")
}

func TestInvariant_OneActiveReservationPerLine(t *testing.T) {
	f := newFx(t)
	loc, prod := f.location(), f.product()
	f.balance(loc, prod, 10)
	orderID, lineID := f.order(prod)
	reserve := `INSERT INTO stock_reservations (id, order_id, order_line_id, location_id, product_id, qty, status, expires_at)
		VALUES ($1, $2, $3, $4, $5, 1, 'ACTIVE', now() + interval '15 minutes')`

	first := newID()
	f.must(reserve, first, orderID, lineID, loc, prod)
	testutil.RequireConstraint(t, f.exec(reserve, newID(), orderID, lineID, loc, prod), "one_active_reservation_per_line")

	// Status penutup wajib punya closed_at.
	testutil.RequireConstraint(t, f.exec(`UPDATE stock_reservations SET status = 'RELEASED' WHERE id = $1`, first), "reservation_closed_consistent")
	f.must(`UPDATE stock_reservations SET status = 'RELEASED', closed_at = now(), close_reason = 'test' WHERE id = $1`, first)
	// Setelah dilepas, line yang sama boleh reservasi lagi.
	f.must(reserve, newID(), orderID, lineID, loc, prod)

	// Line harus milik order yang sama.
	_, otherLine := f.order(prod)
	testutil.RequireConstraint(t, f.exec(reserve, newID(), orderID, otherLine, loc, prod), "reservation_line_fk")
}

func TestInvariant_OneActiveClaimPerUnit(t *testing.T) {
	f := newFx(t)
	prod := f.product()
	unit := f.serial(prod)
	claim := `INSERT INTO loyalty_claims (id, serial_unit_id, consumer_id, status, points, risk_score, available_at)
		VALUES ($1, $2, $3, $4, 10, 0, now())`

	fraud := newID()
	f.must(claim, fraud, unit, f.consumer(), "PENDING")
	testutil.RequireConstraint(t, f.exec(claim, newID(), unit, f.consumer(), "PENDING"), "one_active_claim_per_unit")
	testutil.RequireConstraint(t, f.exec(claim, newID(), unit, f.consumer(), "APPROVED"), "one_active_claim_per_unit")

	// Klaim fraud ditolak → pembeli asli tetap bisa klaim unit yang sama.
	f.must(`UPDATE loyalty_claims SET status = 'REJECTED' WHERE id = $1`, fraud)
	f.must(claim, newID(), unit, f.consumer(), "PENDING")
	// Klaim ditolak lain tidak mengganggu.
	f.must(claim, newID(), unit, f.consumer(), "REJECTED")
	if n := f.count(`SELECT count(*) FROM loyalty_claims WHERE serial_unit_id = $1`, unit); n != 3 {
		t.Fatalf("jumlah klaim %d, ingin 3", n)
	}
}

func TestInvariant_PosTransactionOnce(t *testing.T) {
	f := newFx(t)
	loc := f.location()
	device, cashier := f.posDevice(loc), f.user()
	txn := `INSERT INTO pos_transactions (id, device_id, client_txn_id, device_seq, location_id, cashier_id,
		occurred_at, payload_hash, result) VALUES ($1, $2, $3, $4, $5, $6, now(), $7, 'ACCEPTED')`

	client := newID()
	f.must(txn, newID(), device, client, 1, loc, cashier, randomBytes(32))
	// Batch yang sama dikirim ulang.
	testutil.RequireConstraint(t, f.exec(txn, newID(), device, client, 2, loc, cashier, randomBytes(32)), "pos_txn_once")
	// device_seq dipakai ulang oleh transaksi lain.
	testutil.RequireConstraint(t, f.exec(txn, newID(), device, newID(), 1, loc, cashier, randomBytes(32)), "pos_seq_once")
	testutil.RequireConstraint(t, f.exec(txn, newID(), device, newID(), 0, loc, cashier, randomBytes(32)), "pos_device_seq_positive")
	// client_txn_id sama dari device lain bukan duplikat.
	f.must(txn, newID(), f.posDevice(loc), client, 1, loc, cashier, randomBytes(32))
}

func TestInvariant_SerialUnits(t *testing.T) {
	f := newFx(t)
	prod := f.product()
	batch := f.batch(prod)
	insert := `INSERT INTO serial_units (id, code, level_code, product_id, batch_id, status, commissioned_at)
		VALUES ($1, $2, 'UNIT', $3, $4, $5, $6)`

	// Label belum di-commission boleh tanpa produk/batch.
	f.must(insert, newID(), serialCode(), nil, nil, "GENERATED", nil)
	// Label di-void sebelum commission juga boleh.
	f.must(insert, newID(), serialCode(), nil, nil, "DECOMMISSIONED", nil)

	// Status selain GENERATED wajib produk dan batch.
	testutil.RequireConstraint(t, f.exec(insert, newID(), serialCode(), prod, nil, "IN_STOCK", "2026-10-20"), "serial_commissioned_has_product")
	testutil.RequireConstraint(t, f.exec(insert, newID(), serialCode(), nil, batch, "COMMISSIONED", "2026-10-20"), "serial_commissioned_has_product")
	// Unit yang pernah di-commission tetap wajib produk/batch walau dimusnahkan.
	testutil.RequireConstraint(t, f.exec(insert, newID(), serialCode(), nil, nil, "DECOMMISSIONED", "2026-10-20"), "serial_commissioned_has_product")

	code := serialCode()
	id := newID()
	f.must(insert, id, code, prod, batch, "IN_STOCK", "2026-10-20")
	testutil.RequireConstraint(t, f.exec(insert, newID(), code, prod, batch, "IN_STOCK", "2026-10-20"), "serial_code_unique")
	testutil.RequireConstraint(t, f.exec(`UPDATE serial_units SET parent_id = id WHERE id = $1`, id), "serial_not_own_parent")
	testutil.RequireConstraint(t, f.exec(insert, newID(), "kode-tidak-valid", prod, batch, "IN_STOCK", "2026-10-20"), "serial_code_format")
	testutil.RequireConstraint(t, f.exec(`UPDATE serial_units SET claim_secret_hash = $2 WHERE id = $1`, id, randomBytes(16)), "serial_claim_hash_sha256")
	// Status di luar lookup ditolak FK.
	testutil.RequireConstraint(t, f.exec(insert, newID(), serialCode(), prod, batch, "HILANG", "2026-10-20"), "serial_units_status_fkey")
}

func TestInvariant_PackagingHierarchyAndMixedBatch(t *testing.T) {
	f := newFx(t)
	prod := f.product()
	batch := f.batch(prod)
	insert := `INSERT INTO serial_units (id, code, level_code, product_id, batch_id, parent_id, status, commissioned_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'IN_STOCK', now())`

	// Karton/palet campuran batch: batch_id boleh NULL, product_id tetap wajib.
	pallet, carton, unit := newID(), newID(), newID()
	f.must(insert, pallet, serialCode(), "PALLET", prod, nil, nil)
	f.must(insert, carton, serialCode(), "CARTON", prod, nil, pallet)
	testutil.RequireConstraint(t, f.exec(insert, newID(), serialCode(), "CARTON", nil, nil, nil), "serial_commissioned_has_product")
	// Unit tetap wajib batch.
	testutil.RequireConstraint(t, f.exec(insert, newID(), serialCode(), "UNIT", prod, nil, carton), "serial_commissioned_has_product")
	f.must(insert, unit, serialCode(), "UNIT", prod, batch, carton)
	// Unit langsung di palet (melewati karton) tetap naik level: diterima.
	f.must(insert, newID(), serialCode(), "UNIT", prod, batch, pallet)

	// Induk harus berlevel lebih tinggi.
	testutil.RequireConstraint(t, f.exec(insert, newID(), serialCode(), "PALLET", prod, nil, unit), "serial_parent_level_higher")
	testutil.RequireConstraint(t, f.exec(insert, newID(), serialCode(), "CARTON", prod, nil, carton), "serial_parent_level_higher")
	testutil.RequireConstraint(t, f.exec(insert, newID(), serialCode(), "UNIT", prod, batch, unit), "serial_parent_level_higher")
	// UPDATE juga dicek: memindahkan palet ke bawah unit, atau mengganti level anak.
	testutil.RequireConstraint(t, f.exec(`UPDATE serial_units SET parent_id = $2 WHERE id = $1`, pallet, unit), "serial_parent_level_higher")
	testutil.RequireConstraint(t, f.exec(`UPDATE serial_units SET level_code = 'PALLET' WHERE id = $1`, carton), "serial_parent_level_higher")
}

// Tidak ada FK yang menunjuk ke tabel event (siap partisi, PLANNING §5.8).
func TestSchema_NoForeignKeysIntoEventTables(t *testing.T) {
	f := newFx(t)
	if n := f.count(`SELECT count(*) FROM pg_constraint WHERE contype = 'f'
	                  AND confrelid IN ('scan_events'::regclass, 'unit_events'::regclass)`); n != 0 {
		t.Fatalf("%d FK menunjuk ke scan_events/unit_events", n)
	}
	// Kolom referensinya tetap ada.
	if n := f.count(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'loyalty_claims' AND column_name = 'scan_event_id'`); n != 1 {
		t.Fatal("loyalty_claims.scan_event_id hilang")
	}
}

func TestInvariant_UniqueBusinessKeys(t *testing.T) {
	f := newFx(t)
	prod := f.product()

	t.Run("orders channel_code+external_ref", func(t *testing.T) {
		q := `INSERT INTO orders (id, channel_code, external_ref, status, subtotal, total, placed_at)
			VALUES ($1, $2, $3, 'PAID', 1000, 1000, now())`
		f.must(q, newID(), "SHOPEE", "SHP-001")
		testutil.RequireConstraint(t, f.exec(q, newID(), "SHOPEE", "SHP-001"), "order_external_ref_once")
		f.must(q, newID(), "TOKOPEDIA", "SHP-001") // channel lain boleh
		// Order D2C tanpa external_ref tidak saling bentrok.
		f.must(q, newID(), "D2C_WEB", nil)
		f.must(q, newID(), "D2C_WEB", nil)
	})

	t.Run("payments provider+provider_ref", func(t *testing.T) {
		orderID, _ := f.order(prod)
		q := `INSERT INTO payments (id, order_id, provider, provider_ref, amount, status) VALUES ($1, $2, 'MOCKPAY', $3, 10000, 'SUCCEEDED')`
		f.must(q, newID(), orderID, "EVT-1")
		testutil.RequireConstraint(t, f.exec(q, newID(), orderID, "EVT-1"), "payment_provider_ref_once")
	})

	t.Run("order_line_serials serial sekali terjual", func(t *testing.T) {
		unit := f.serial(prod)
		_, line1 := f.order(prod)
		_, line2 := f.order(prod)
		q := `INSERT INTO order_line_serials (order_line_id, serial_unit_id) VALUES ($1, $2)`
		f.must(q, line1, unit)
		testutil.RequireConstraint(t, f.exec(q, line2, unit), "serial_sold_once")
	})

	t.Run("anomalies dedupe_key", func(t *testing.T) {
		q := `INSERT INTO anomalies (id, rule_code, severity, subject_type, dedupe_key, first_seen_at, last_seen_at)
			VALUES ($1, 'SCAN_VELOCITY', 'MEDIUM', 'SERIAL_UNIT', 'SCAN_VELOCITY:x:2026-11-07', now(), now())`
		f.must(q, newID())
		testutil.RequireConstraint(t, f.exec(q, newID()), "anomaly_dedupe_once")
	})

	t.Run("loyalty_ledger idempotency_key dan tanda poin", func(t *testing.T) {
		consumer := f.consumer()
		q := `INSERT INTO loyalty_ledger (id, consumer_id, entry_type, points, ref_type, ref_id, idempotency_key)
			VALUES ($1, $2, $3, $4, 'CLAIM', $1, $5)`
		f.must(q, newID(), consumer, "EARN", 10, "claim:1")
		testutil.RequireConstraint(t, f.exec(q, newID(), consumer, "EARN", 10, "claim:1"), "loyalty_ledger_idempotent")
		testutil.RequireConstraint(t, f.exec(q, newID(), consumer, "EARN", -10, "claim:2"), "loyalty_points_sign")
		testutil.RequireConstraint(t, f.exec(q, newID(), consumer, "REDEEM", 10, "claim:3"), "loyalty_points_sign")
	})

	t.Run("idempotency_keys scope+key", func(t *testing.T) {
		q := `INSERT INTO idempotency_keys (id, scope, key, request_hash, expires_at) VALUES ($1, $2, 'k-1', $3, now() + interval '1 day')`
		f.must(q, newID(), "user:a:orders.create", randomBytes(32))
		testutil.RequireConstraint(t, f.exec(q, newID(), "user:a:orders.create", randomBytes(32)), "idempotency_key_once")
		f.must(q, newID(), "user:b:orders.create", randomBytes(32)) // scope lain boleh
	})

	t.Run("users email case-insensitive", func(t *testing.T) {
		q := `INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'U', '$argon2id$v=19$m=8,t=1,p=1$c2FsdA$aGFzaA')`
		f.must(q, newID(), "Kasir@Toko.test")
		testutil.RequireConstraint(t, f.exec(q, newID(), "kasir@toko.TEST"), "users_email_unique")
	})
}

func TestInvariant_ExplicitScopeModes(t *testing.T) {
	f := newFx(t)
	user := f.user()
	q := `INSERT INTO user_roles (id, user_id, role_code, scope_mode) VALUES ($1, $2, $3, $4)`
	testutil.RequireConstraint(t, f.exec(q, newID(), user, "CASHIER", "SOME"), "user_role_scope_mode_valid")
	testutil.RequireSQLState(t, f.exec(q, newID(), user, "CASHIER", nil), "23502") // NOT NULL: mode wajib eksplisit
	f.must(q, newID(), user, "CASHIER", "SELECTED")

	party := newID()
	f.must(`INSERT INTO parties (id, kind, name) VALUES ($1, 'ORG', 'Distributor')`, party)
	territory := newID()
	f.must(`INSERT INTO territories (id, code, name) VALUES ($1, 'TER-X', 'X')`, territory)
	pt := `INSERT INTO partner_territories (id, party_id, territory_id, coverage_mode) VALUES ($1, $2, $3, $4)`
	testutil.RequireConstraint(t, f.exec(pt, newID(), party, nil, "SELECTED"), "partner_coverage_consistent")
	testutil.RequireConstraint(t, f.exec(pt, newID(), party, territory, "NATIONAL"), "partner_coverage_consistent")
	testutil.RequireSQLState(t, f.exec(pt, newID(), party, territory, "SEMUA"), "23514") // check_violation
	f.must(pt, newID(), party, territory, "SELECTED")
}

func TestInvariant_LifecycleTransitionCodesExist(t *testing.T) {
	f := newFx(t)
	q := `INSERT INTO lifecycle_transitions (entity_type, from_code, to_code, biz_step) VALUES ($1, $2, $3, 'test')`
	testutil.RequireConstraint(t, f.exec(q, "SERIAL_UNIT", "SOLD", "HILANG"), "lifecycle_transition_codes_exist")
	// Status order tidak berlaku untuk serial, dan sebaliknya.
	testutil.RequireConstraint(t, f.exec(q, "SERIAL_UNIT", "IN_STOCK", "PAID"), "lifecycle_transition_codes_exist")
	testutil.RequireConstraint(t, f.exec(q, "ORDER", "PAID", "SOLD"), "lifecycle_transition_codes_exist")
	testutil.RequireConstraint(t, f.exec(q, "SERIAL_UNIT", "IN_STOCK", "SOLD"), "lifecycle_transition_once")
	f.must(q, "SERIAL_UNIT", "SOLD", "QUARANTINED")
}
