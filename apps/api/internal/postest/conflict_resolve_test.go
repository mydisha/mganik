package postest

import (
	"bytes"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"mganik/api/internal/service"
)

// openDiscrepancyConflict membuat konflik STOCK_DISCREPANCY lewat sync
// (POS menjual lebih dari stok tercatat Jaksel SKU2 = 12).
func (e *env) openDiscrepancyConflict(t *testing.T, seq int64) uuid.UUID {
	t.Helper()
	d := e.sync(signed(t, e.key, e.newTxn(seq, line(sku2ID, 13, priceSKU2)))).Results[0].detail(t)
	if len(d.Conflicts) != 1 || d.Conflicts[0].ConflictID == nil {
		t.Fatalf("konflik tidak terbentuk: %+v", d)
	}
	return *d.Conflicts[0].ConflictID
}

func TestConflictResolve(t *testing.T) {
	e := newEnv(t)
	id := e.openDiscrepancyConflict(t, 1)
	path := "/api/v1/cc/conflicts/" + id.String() + "/resolve"
	body := map[string]any{"resolution": "STOCK_RECOUNTED", "note": "Opname ulang: 1 unit penerimaan belum dicatat ✔"}

	bdg := e.staffEngine(staff(opsID, false, []uuid.UUID{bandungID}, service.PermConflictResolve))
	readOnly := e.staffEngine(staff(opsID, true, nil, "cc.read"))
	ops := e.staffEngine(staff(opsID, true, nil, service.PermConflictResolve))

	cases := []struct {
		name string
		h    http.Handler
		path string
		body any
		code int
		want string
	}{
		{"anonim", e.staffEngine(nil), path, body, 401, "unauthenticated"},
		{"tanpa permission", readOnly, path, body, 403, "forbidden"},
		{"lokasi lain", bdg, path, body, 404, "conflict_not_found"},
		{"tidak ada", ops, "/api/v1/cc/conflicts/" + uuid.NewString() + "/resolve", body, 404, "conflict_not_found"},
		{"id rusak", ops, "/api/v1/cc/conflicts/abc/resolve", body, 422, "validation_failed"},
		{"resolution asing", ops, path, map[string]any{"resolution": "DIHAPUS", "note": "xxx"}, 422, "validation_failed"},
		{"catatan pendek", ops, path, map[string]any{"resolution": "OTHER", "note": "x"}, 422, "validation_failed"},
		{"catatan NUL", ops, path, map[string]any{"resolution": "OTHER", "note": "ab\u0000c"}, 422, "invalid_characters"},
	}
	for _, c := range cases {
		r := serve(c.h, http.MethodPost, c.path, c.body, nil)
		if r.Code != c.code || r.problem(t).Code != c.want {
			t.Errorf("%s: %d %s", c.name, r.Code, r.Body)
		}
	}
	if s := e.str(`SELECT status FROM allocation_conflicts WHERE id = ?`, id); s != "OPEN" {
		t.Fatal("permintaan ditolak tidak boleh mengubah konflik")
	}

	// Dua resolve bersamaan: tepat satu 200, sisanya 409.
	jaksel := e.staffEngine(staff(managerID, false, []uuid.UUID{jakselID}, service.PermConflictResolve))
	var wg sync.WaitGroup
	codes := make([]int, 4)
	bodies := make([][]byte, 4)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := serve(jaksel, http.MethodPost, path, body, nil)
			codes[i], bodies[i] = r.Code, r.Body
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for i, c := range codes {
		switch c {
		case 200:
			ok++
			if !bytes.Contains(bodies[i], []byte(`"status":"RESOLVED"`)) || !bytes.Contains(bodies[i], []byte(`"resolution":"STOCK_RECOUNTED"`)) {
				t.Fatalf("respons resolve: %s", bodies[i])
			}
		case 409:
			conflict++
		default:
			t.Fatalf("status tak terduga %d: %s", c, bodies[i])
		}
	}
	if ok != 1 || conflict != 3 {
		t.Fatalf("resolve bersamaan: %d sukses, %d konflik", ok, conflict)
	}
	if n := e.count(`SELECT count(*) FROM allocation_conflicts WHERE id = ? AND status = 'RESOLVED' AND resolved_by = ?
	                  AND resolved_at IS NOT NULL AND details->>'resolution_note' LIKE 'Opname ulang%'`, id, managerID); n != 1 {
		t.Fatal("konflik tidak tertutup dengan benar")
	}
	if n := e.count(`SELECT count(*) FROM audit_logs WHERE action = 'allocation_conflict.resolved' AND entity_id = ?`, id); n != 1 {
		t.Fatal("resolve harus diaudit tepat sekali")
	}
	// Konflik AUTO_RESOLVED/RESOLVED tidak bisa diselesaikan lagi.
	if r := serve(ops, http.MethodPost, path, body, nil); r.Code != 409 || r.problem(t).Code != "invalid_transition" {
		t.Fatalf("resolve ulang: %d %s", r.Code, r.Body)
	}
}
