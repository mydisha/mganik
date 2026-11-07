package migrations_test

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"

	"mganik/api/internal/database"
	"mganik/api/testutil"
)

func TestMain(m *testing.M) { testutil.Main(m) }

// TestMigrate_UpDownUp: migrasi dari DB kosong sukses, Down sampai versi 0
// membersihkan semua objek, lalu Up lagi sukses dengan isi lookup yang sama.
func TestMigrate_UpDownUp(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := testutil.NewEmptyDB(t).DB()
	if err != nil {
		t.Fatal(err)
	}
	provider, err := database.NewMigrationProvider(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	sources := provider.ListSources()
	latest := sources[len(sources)-1].Version
	if latest < 10 {
		t.Fatalf("versi migrasi terakhir %d, ingin >= 10", latest)
	}

	snapshot := func() string {
		rows, err := sqlDB.QueryContext(ctx, `
			SELECT 'role_permissions', count(*) FROM role_permissions
			UNION ALL SELECT 'lifecycle_transitions', count(*) FROM lifecycle_transitions
			UNION ALL SELECT 'movement_types', count(*) FROM movement_types
			UNION ALL SELECT 'anomaly_rules', count(*) FROM anomaly_rules
			UNION ALL SELECT 'tables', count(*) FROM information_schema.tables WHERE table_schema = 'public'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var parts []string
		for rows.Next() {
			var name string
			var n int
			if err := rows.Scan(&name, &n); err != nil {
				t.Fatal(err)
			}
			parts = append(parts, name+"="+strconv.Itoa(n))
		}
		return strings.Join(parts, ",")
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up dari nol: %v", err)
	}
	if v, _ := provider.GetDBVersion(ctx); v != latest {
		t.Fatalf("versi setelah up %d, ingin %d", v, latest)
	}
	first := snapshot()

	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("down ke 0: %v", err)
	}
	if v, _ := provider.GetDBVersion(ctx); v != 0 {
		t.Fatalf("versi setelah down %d, ingin 0", v)
	}
	var leftovers []string
	// Semua relasi (tabel, index, sequence, view), fungsi, trigger, dan type di
	// schema public selain milik goose sendiri.
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT 'relkind ' || c.relkind::text || ' ' || c.relname FROM pg_class c
		 WHERE c.relnamespace = 'public'::regnamespace AND c.relname NOT LIKE 'goose\_db\_version%'
		UNION ALL
		SELECT 'function ' || p.proname FROM pg_proc p WHERE p.pronamespace = 'public'::regnamespace
		UNION ALL
		SELECT 'trigger ' || tg.tgname FROM pg_trigger tg JOIN pg_class c ON c.oid = tg.tgrelid
		 WHERE c.relnamespace = 'public'::regnamespace AND NOT tg.tgisinternal
		UNION ALL
		SELECT 'type ' || t.typname FROM pg_type t
		 WHERE t.typnamespace = 'public'::regnamespace AND t.typname NOT LIKE '%goose\_db\_version%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		leftovers = append(leftovers, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(leftovers) > 0 {
		t.Fatalf("objek tersisa setelah down ke 0: %v", leftovers)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up kedua: %v", err)
	}
	if second := snapshot(); second != first {
		t.Fatalf("isi setelah up kedua berbeda:\n%s\n%s", first, second)
	}
}

// TestSchema_NoEnumTypes: status/tipe memakai lookup table atau text + CHECK.
func TestSchema_NoEnumTypes(t *testing.T) {
	f := newFx(t)
	if n := f.count(`SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace
	                  WHERE t.typtype = 'e' AND n.nspname = 'public'`); n != 0 {
		t.Fatalf("ada %d tipe ENUM", n)
	}
}

// TestSchema_NamedConstraints: setiap constraint/index bernama dari PLANNING
// §5.6 ada di tabel yang benar.
func TestSchema_NamedConstraints(t *testing.T) {
	f := newFx(t)
	want := map[string]string{
		// constraint
		"no_oversell":                     "inventory_balances",
		"physical_nonneg":                 "inventory_balances",
		"reserved_nonneg":                 "inventory_balances",
		"quarantine_nonneg":               "inventory_balances",
		"movement_idempotent":             "inventory_movements",
		"pos_txn_once":                    "pos_transactions",
		"pos_seq_once":                    "pos_transactions",
		"order_external_ref_once":         "orders",
		"payment_provider_ref_once":       "payments",
		"serial_sold_once":                "order_line_serials",
		"serial_code_unique":              "serial_units",
		"serial_not_own_parent":           "serial_units",
		"serial_commissioned_has_product": "serial_units",
		"anomaly_dedupe_once":             "anomalies",
		"loyalty_ledger_idempotent":       "loyalty_ledger",
		"idempotency_key_once":            "idempotency_keys",
		"user_role_scope_mode_valid":      "user_roles",
		"partner_coverage_consistent":     "partner_territories",
		// index
		"one_active_reservation_per_line":   "stock_reservations",
		"reservations_due":                  "stock_reservations",
		"one_active_claim_per_unit":         "loyalty_claims",
		"inventory_movements_loc_prod_time": "inventory_movements",
		"scan_events_unit_time":             "scan_events",
		"scan_events_occurred_brin":         "scan_events",
		"outbox_events_pending":             "outbox_events",
		"users_email_unique":                "users",
	}
	rows, err := f.db.QueryContext(f.ctx, `
		SELECT conname, conrelid::regclass::text FROM pg_constraint
		 WHERE connamespace = 'public'::regnamespace
		UNION ALL
		SELECT indexname, tablename FROM pg_indexes WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var name, table string
		if err := rows.Scan(&name, &table); err != nil {
			t.Fatal(err)
		}
		got[name] = table
	}
	for name, table := range want {
		if got[name] != table {
			t.Errorf("constraint/index %s: ingin di %s, dapat %q", name, table, got[name])
		}
	}

	// Detail yang tidak terlihat dari nama saja.
	checks := map[string]string{
		"one_active_claim_per_unit partial PENDING/APPROVED": `SELECT count(*) FROM pg_indexes WHERE indexname = 'one_active_claim_per_unit'
			AND indexdef LIKE 'CREATE UNIQUE INDEX%' AND indexdef LIKE '%PENDING%' AND indexdef LIKE '%APPROVED%'`,
		"one_active_reservation_per_line unique partial": `SELECT count(*) FROM pg_indexes WHERE indexname = 'one_active_reservation_per_line'
			AND indexdef LIKE 'CREATE UNIQUE INDEX%' AND indexdef LIKE '%ACTIVE%'`,
		"scan_events BRIN": `SELECT count(*) FROM pg_indexes WHERE indexname = 'scan_events_occurred_brin' AND indexdef LIKE '%USING brin%'`,
		"scan_events DESC": `SELECT count(*) FROM pg_indexes WHERE indexname = 'scan_events_unit_time' AND indexdef LIKE '%occurred_at DESC%'`,
		"outbox partial":   `SELECT count(*) FROM pg_indexes WHERE indexname = 'outbox_events_pending' AND indexdef LIKE '%published_at IS NULL%'`,
		"available_qty generated stored": `SELECT count(*) FROM information_schema.columns
			WHERE table_name = 'inventory_balances' AND column_name = 'available_qty' AND is_generated = 'ALWAYS'`,
	}
	for name, q := range checks {
		if f.count(q) != 1 {
			t.Errorf("%s tidak terpenuhi", name)
		}
	}
}

// TestSchema_AppendOnlyTriggers: enam tabel ledger/event punya trigger
// forbid_mutation untuk UPDATE/DELETE dan TRUNCATE.
func TestSchema_AppendOnlyTriggers(t *testing.T) {
	f := newFx(t)
	rows, err := f.db.QueryContext(f.ctx, `
		SELECT DISTINCT c.relname FROM pg_trigger tg
		  JOIN pg_class c ON c.oid = tg.tgrelid
		  JOIN pg_proc p ON p.oid = tg.tgfoid
		 WHERE p.proname = 'forbid_mutation' AND NOT tg.tgisinternal
		 ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	want := []string{"audit_logs", "inventory_movements", "loyalty_ledger", "order_status_history", "scan_events", "unit_events"}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tabel append-only %v, ingin %v", got, want)
	}
}
