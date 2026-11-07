package migrations_test

import (
	"regexp"
	"testing"
)

// Teks lookup yang tampil di UI mengikuti glosarium (migrasi 00016): tanpa
// istilah internal/Inggris yang sudah punya padanan, tanpa garis miring
// sebagai pengganti "atau", dan tanpa tanda pisah panjang.
func TestLookup_DisplayTextFollowsGlossary(t *testing.T) {
	f := newFx(t)
	rows, err := f.db.QueryContext(f.ctx, `
		SELECT 'serial_unit_statuses ' || code, name FROM serial_unit_statuses
		UNION ALL SELECT 'order_statuses ' || code, name FROM order_statuses
		UNION ALL SELECT 'movement_types ' || code, name FROM movement_types
		UNION ALL SELECT 'roles.name ' || code, name FROM roles
		UNION ALL SELECT 'roles.description ' || code, coalesce(description, '') FROM roles
		UNION ALL SELECT 'permissions ' || code, description FROM permissions
		UNION ALL SELECT 'lifecycle_transitions ' || entity_type || ':' || coalesce(from_code, '') || '->' || to_code,
		                 coalesce(description, '') FROM lifecycle_transitions
		UNION ALL SELECT 'anomaly_rules.name ' || code, name FROM anomaly_rules
		UNION ALL SELECT 'anomaly_rules.description ' || code, description FROM anomaly_rules`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	banned := regexp.MustCompile(`(?i)\bdevice\b|\bshipment\b|di-commission|\bcommission\b|re-reserve|preempt|realokasi|\brelease\b|\bvoid\b|\bdb\b|—|\S/\S`)
	n := 0
	for rows.Next() {
		var key, text string
		if err := rows.Scan(&key, &text); err != nil {
			t.Fatal(err)
		}
		n++
		if m := banned.FindString(text); m != "" {
			t.Errorf("%s memuat %q: %s", key, m, text)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 100 {
		t.Fatalf("hanya %d baris lookup terbaca", n)
	}
}
