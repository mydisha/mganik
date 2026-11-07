package migrations_test

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// State machine PLANNING §5.7 (serial unit + order), "" = status awal.
// EXPIRED -> NEEDS_ACTION berasal dari catatan edge case bayar telat di §5.7.
var wantTransitions = []string{
	"SERIAL_UNIT: -> GENERATED",
	"SERIAL_UNIT:GENERATED -> COMMISSIONED",
	"SERIAL_UNIT:GENERATED -> DECOMMISSIONED",
	"SERIAL_UNIT:COMMISSIONED -> IN_STOCK",
	"SERIAL_UNIT:COMMISSIONED -> QUARANTINED",
	"SERIAL_UNIT:IN_STOCK -> IN_TRANSIT",
	"SERIAL_UNIT:IN_STOCK -> SOLD",
	"SERIAL_UNIT:IN_STOCK -> QUARANTINED",
	"SERIAL_UNIT:IN_TRANSIT -> IN_STOCK",
	"SERIAL_UNIT:IN_TRANSIT -> AT_PARTNER",
	"SERIAL_UNIT:IN_TRANSIT -> QUARANTINED",
	"SERIAL_UNIT:AT_PARTNER -> IN_TRANSIT",
	"SERIAL_UNIT:AT_PARTNER -> SOLD",
	"SERIAL_UNIT:AT_PARTNER -> CONSUMER_ACTIVATED",
	"SERIAL_UNIT:AT_PARTNER -> QUARANTINED",
	"SERIAL_UNIT:SOLD -> CONSUMER_ACTIVATED",
	"SERIAL_UNIT:SOLD -> RETURNED",
	"SERIAL_UNIT:CONSUMER_ACTIVATED -> RETURNED",
	// Migrasi 00012: klaim ditolak/dibalik mengembalikan unit ke status asal.
	"SERIAL_UNIT:CONSUMER_ACTIVATED -> SOLD",
	"SERIAL_UNIT:CONSUMER_ACTIVATED -> AT_PARTNER",
	"SERIAL_UNIT:RETURNED -> QUARANTINED",
	"SERIAL_UNIT:QUARANTINED -> IN_STOCK",
	"SERIAL_UNIT:QUARANTINED -> DECOMMISSIONED",

	"ORDER: -> PENDING_PAYMENT",
	"ORDER: -> PAID",
	"ORDER: -> COMPLETED",
	"ORDER:PENDING_PAYMENT -> PAID",
	"ORDER:PENDING_PAYMENT -> EXPIRED",
	"ORDER:PENDING_PAYMENT -> CANCELLED",
	"ORDER:EXPIRED -> PAID",
	"ORDER:EXPIRED -> NEEDS_ACTION",
	"ORDER:PAID -> FULFILLING",
	"ORDER:PAID -> NEEDS_ACTION",
	"ORDER:NEEDS_ACTION -> PAID",
	"ORDER:NEEDS_ACTION -> CANCELLED",
	"ORDER:PAID -> CANCELLED",
	"ORDER:FULFILLING -> SHIPPED",
	"ORDER:SHIPPED -> DELIVERED",
	"ORDER:DELIVERED -> COMPLETED",
	"ORDER:DELIVERED -> RETURNED",
}

func TestLookup_LifecycleTransitionsMatchStateMachine(t *testing.T) {
	f := newFx(t)
	rows, err := f.db.QueryContext(f.ctx, `SELECT entity_type || ':' || coalesce(from_code, '') || ' -> ' || to_code FROM lifecycle_transitions`)
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
	want := append([]string(nil), wantTransitions...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("transisi tidak sama dengan state machine.\ndapat:\n%s\ningin:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Setiap from/to di lifecycle_transitions ada di tabel status entitasnya, dan
// setiap status non-terminal punya jalan keluar.
func TestLookup_TransitionStatusesExist(t *testing.T) {
	f := newFx(t)
	missing := f.count(`
		SELECT count(*) FROM lifecycle_transitions lt, unnest(ARRAY[lt.from_code, lt.to_code]) AS c(code)
		 WHERE c.code IS NOT NULL AND NOT (
		   (lt.entity_type = 'SERIAL_UNIT' AND EXISTS (SELECT 1 FROM serial_unit_statuses s WHERE s.code = c.code)) OR
		   (lt.entity_type = 'ORDER'       AND EXISTS (SELECT 1 FROM order_statuses s WHERE s.code = c.code)))`)
	if missing != 0 {
		t.Fatalf("%d kode status di lifecycle_transitions tidak ada di tabel status", missing)
	}
	deadEnds := f.count(`
		SELECT count(*) FROM (
		  SELECT 'SERIAL_UNIT' AS e, code, is_terminal FROM serial_unit_statuses
		  UNION ALL SELECT 'ORDER', code, is_terminal FROM order_statuses) s
		 WHERE NOT s.is_terminal
		   AND NOT EXISTS (SELECT 1 FROM lifecycle_transitions lt WHERE lt.entity_type = s.e AND lt.from_code = s.code)`)
	if deadEnds != 0 {
		t.Fatalf("%d status non-terminal tanpa transisi keluar", deadEnds)
	}
	unreachable := f.count(`
		SELECT count(*) FROM (
		  SELECT 'SERIAL_UNIT' AS e, code FROM serial_unit_statuses
		  UNION ALL SELECT 'ORDER', code FROM order_statuses) s
		 WHERE NOT EXISTS (SELECT 1 FROM lifecycle_transitions lt WHERE lt.entity_type = s.e AND lt.to_code = s.code)`)
	if unreachable != 0 {
		t.Fatalf("%d status tidak pernah bisa dicapai", unreachable)
	}
	// Permission transisi harus terdaftar (FK) dan dimiliki minimal satu role selain ADMIN.
	orphan := f.count(`
		SELECT count(DISTINCT lt.permission) FROM lifecycle_transitions lt
		 WHERE lt.permission IS NOT NULL AND NOT EXISTS (
		   SELECT 1 FROM role_permissions rp WHERE rp.permission_code = lt.permission AND rp.role_code <> 'ADMIN')`)
	if orphan != 0 {
		t.Fatalf("%d permission transisi hanya dimiliki ADMIN", orphan)
	}
}

func TestLookup_MovementTypes(t *testing.T) {
	f := newFx(t)
	// code: physical reserved quarantine promise either (PLANNING §5.5).
	want := map[string]string{
		"RECEIPT":            "1 0 0 false false",
		"RESERVE":            "0 1 0 true false",
		"RELEASE":            "0 -1 0 false false",
		"PREEMPT":            "0 -1 0 false false",
		"COMMIT_SALE":        "-1 -1 0 false false",
		"DIRECT_SALE":        "-1 0 0 false false",
		"QUARANTINE_IN":      "0 0 1 false false",
		"QUARANTINE_OUT":     "0 0 -1 false false",
		"WRITE_OFF":          "-1 0 -1 false false",
		"TRANSFER_OUT":       "-1 0 0 false false",
		"TRANSFER_IN":        "1 0 0 false false",
		"ADJUST_COUNT":       "1 0 0 false true",
		"ADJUST_DISCREPANCY": "1 0 0 false false",
	}
	rows, err := f.db.QueryContext(f.ctx, `SELECT code, concat_ws(' ', physical_sign, reserved_sign, quarantine_sign, is_promise::text, allow_either_sign::text) FROM movement_types`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var code, effect string
		if err := rows.Scan(&code, &effect); err != nil {
			t.Fatal(err)
		}
		got[code] = effect
	}
	if len(got) != len(want) {
		t.Fatalf("jumlah movement_types %d, ingin %d", len(got), len(want))
	}
	for code, w := range want {
		if got[code] != w {
			t.Errorf("%s: efek %q, ingin %q", code, got[code], w)
		}
	}
}

func TestLookup_AnomalyRules(t *testing.T) {
	f := newFx(t)
	params := func(code string) map[string]any {
		var raw []byte
		if err := f.db.QueryRowContext(f.ctx, `SELECT params FROM anomaly_rules WHERE code = $1 AND enabled`, code).Scan(&raw); err != nil {
			t.Fatalf("aturan %s: %v", code, err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	travel := params("IMPOSSIBLE_TRAVEL")
	if travel["max_speed_kmh"] != 900.0 || travel["min_distance_km_gps"] != 100.0 || travel["min_distance_km_ip"] != 500.0 {
		t.Fatalf("IMPOSSIBLE_TRAVEL params %v", travel)
	}
	velocity := params("SCAN_VELOCITY")
	if velocity["max_fingerprints"] != 5.0 || velocity["window_hours"] != 24.0 {
		t.Fatalf("SCAN_VELOCITY params %v", velocity)
	}
	for _, code := range []string{"UNKNOWN_CODE", "NON_SELLABLE_SCAN", "OUT_OF_TERRITORY", "BATCH_RECALLED"} {
		params(code)
	}
	// Luar wilayah tidak boleh mengubah hasil yang dilihat konsumen.
	if n := f.count(`SELECT count(*) FROM anomaly_rules WHERE code = 'OUT_OF_TERRITORY' AND consumer_effect = 'NONE'`); n != 1 {
		t.Fatal("OUT_OF_TERRITORY harus internal saja (consumer_effect NONE)")
	}
}

func TestLookup_RBAC(t *testing.T) {
	f := newFx(t)
	for _, role := range []string{"ADMIN", "OPS_MANAGER", "PRODUCTION_OPERATOR", "WAREHOUSE_STAFF", "STORE_MANAGER", "CASHIER", "MARKETING_ANALYST"} {
		if n := f.count(`SELECT count(*) FROM role_permissions WHERE role_code = $1`, role); n == 0 {
			t.Errorf("role %s tanpa permission", role)
		}
	}
	if f.count(`SELECT count(*) FROM permissions`) != f.count(`SELECT count(*) FROM role_permissions WHERE role_code = 'ADMIN'`) {
		t.Error("ADMIN harus memegang semua permission")
	}
	// Permission yang disebut PLANNING §6.3.
	for _, p := range []string{"serial.commission", "shipment.create", "inventory.adjust", "pos.sync", "order.cancel", "cc.read", "anomaly.resolve", "conflict.resolve"} {
		if f.count(`SELECT count(*) FROM permissions WHERE code = $1`, p) != 1 {
			t.Errorf("permission %s tidak ada", p)
		}
	}
	// Kasir tidak boleh menyesuaikan stok atau menyelesaikan konflik.
	if n := f.count(`SELECT count(*) FROM role_permissions WHERE role_code = 'CASHIER' AND permission_code IN ('inventory.adjust', 'conflict.resolve', 'user.manage')`); n != 0 {
		t.Error("kasir memegang permission sensitif")
	}
}
