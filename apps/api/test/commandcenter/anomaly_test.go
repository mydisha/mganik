package commandcenter_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"mganik/api/internal/dto"
	"mganik/api/internal/pagination"
)

func patchAnomaly(t *testing.T, e *env, id uuid.UUID, body any) resp {
	t.Helper()
	return call(t, e.engine(opsPrincipal()), http.MethodPatch, "/api/v1/cc/anomalies/"+id.String(), body)
}

func decodeAnomaly(t *testing.T, r resp) dto.CCAnomalyItem {
	t.Helper()
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body)
	}
	var out dto.CCAnomalyItem
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAnomaly_Transitions(t *testing.T) {
	e := newEnv(t)
	id := e.anomaly(anomalyFx{partyID: &sulselPartyID})

	// OPEN → ACK (catatan opsional).
	got := decodeAnomaly(t, patchAnomaly(t, e, id, map[string]any{"status": "ACK"}))
	if got.Status != "ACK" || got.Version != 1 || got.ResolvedAt != nil || got.ResolvedBy != nil {
		t.Fatalf("ACK salah: %+v", got)
	}
	if got.PartyName == nil || !strings.Contains(*got.PartyName, "Sulsel") || got.RuleName == "" {
		t.Fatalf("join party/rule hilang: %+v", got)
	}
	// ACK → ACK tidak sah.
	p := requireProblem(t, patchAnomaly(t, e, id, map[string]any{"status": "ACK"}), http.StatusConflict, "invalid_transition")
	if !strings.Contains(p.Detail, "ACK") {
		t.Fatalf("detail tidak menyebut status: %s", p.Detail)
	}
	// Menutup wajib dengan catatan.
	requireProblem(t, patchAnomaly(t, e, id, map[string]any{"status": "RESOLVED"}), http.StatusUnprocessableEntity, "validation_failed")
	requireProblem(t, patchAnomaly(t, e, id, map[string]any{"status": "RESOLVED", "note": "   "}), http.StatusUnprocessableEntity, "validation_failed")
	// ACK → RESOLVED dengan catatan unicode.
	note := "Sudah dicek ✓ — listing Shopee di-takedown 👍 (gudang Makassar)"
	got = decodeAnomaly(t, patchAnomaly(t, e, id, map[string]any{"status": "RESOLVED", "note": "  " + note + "\n"}))
	if got.Status != "RESOLVED" || got.Version != 2 || got.ResolutionNote == nil || *got.ResolutionNote != note ||
		got.ResolvedBy == nil || *got.ResolvedBy != opsUserID || got.ResolvedAt == nil || !got.ResolvedAt.Equal(testNow) {
		t.Fatalf("RESOLVED salah: %+v", got)
	}
	// Status akhir tidak bisa diubah lagi.
	requireProblem(t, patchAnomaly(t, e, id, map[string]any{"status": "FALSE_POSITIVE", "note": "x"}), http.StatusConflict, "invalid_transition")
	requireProblem(t, patchAnomaly(t, e, id, map[string]any{"status": "ACK"}), http.StatusConflict, "invalid_transition")

	// Audit: satu baris per transisi yang berhasil, dengan before/after dan alasan.
	type auditRow struct {
		Before, After, Reason, RequestID, ActorType string
		ActorID                                     uuid.UUID
	}
	var audits []auditRow
	if err := e.db.Raw(`SELECT before::text AS before, after::text AS after, COALESCE(reason, '') AS reason,
	                           request_id, actor_type, actor_id
	                      FROM audit_logs WHERE entity = 'anomalies' AND entity_id = ? AND action = 'anomaly.status_changed'
	                     ORDER BY at, id`, id).Scan(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 2 {
		t.Fatalf("audit %d baris, ingin 2", len(audits))
	}
	if !strings.Contains(audits[0].Before, `"OPEN"`) || !strings.Contains(audits[0].After, `"ACK"`) ||
		!strings.Contains(audits[1].Before, `"ACK"`) || !strings.Contains(audits[1].After, `"RESOLVED"`) ||
		audits[1].Reason != note || audits[1].RequestID != "req-test-cc-0001" ||
		audits[1].ActorType != "USER" || audits[1].ActorID != opsUserID {
		t.Fatalf("audit salah: %+v", audits)
	}

	// OPEN → FALSE_POSITIVE langsung.
	id2 := e.anomaly(anomalyFx{})
	got = decodeAnomaly(t, patchAnomaly(t, e, id2, map[string]any{"status": "FALSE_POSITIVE", "note": "Konsumen sedang mudik"}))
	if got.Status != "FALSE_POSITIVE" || got.ResolvedAt == nil {
		t.Fatalf("FALSE_POSITIVE salah: %+v", got)
	}
	// OPEN → RESOLVED langsung.
	id3 := e.anomaly(anomalyFx{})
	if got = decodeAnomaly(t, patchAnomaly(t, e, id3, map[string]any{"status": "RESOLVED", "note": "ok"})); got.Status != "RESOLVED" {
		t.Fatalf("RESOLVED langsung gagal: %+v", got)
	}
}

func TestAnomaly_BadRequests(t *testing.T) {
	e := newEnv(t)
	id := e.anomaly(anomalyFx{})
	cases := []struct {
		name   string
		path   string
		body   any
		status int
		code   string
	}{
		{"id tidak ada", "/api/v1/cc/anomalies/" + uuid.Must(uuid.NewV7()).String(), map[string]any{"status": "ACK"}, http.StatusNotFound, "anomaly_not_found"},
		{"id bukan uuid", "/api/v1/cc/anomalies/123", map[string]any{"status": "ACK"}, http.StatusUnprocessableEntity, "validation_failed"},
		{"status OPEN", "/api/v1/cc/anomalies/" + id.String(), map[string]any{"status": "OPEN"}, http.StatusUnprocessableEntity, "validation_failed"},
		{"status kosong", "/api/v1/cc/anomalies/" + id.String(), map[string]any{}, http.StatusUnprocessableEntity, "validation_failed"},
		{"field tak dikenal", "/api/v1/cc/anomalies/" + id.String(), map[string]any{"status": "ACK", "resolved_by": opsUserID}, http.StatusBadRequest, "invalid_request"},
		{"body bukan json", "/api/v1/cc/anomalies/" + id.String(), "{status", http.StatusBadRequest, "invalid_request"},
		{"note terlalu panjang", "/api/v1/cc/anomalies/" + id.String(), map[string]any{"status": "ACK", "note": strings.Repeat("é", 1001)}, http.StatusUnprocessableEntity, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := call(t, e.engine(opsPrincipal()), http.MethodPatch, tc.path, tc.body)
			requireProblem(t, r, tc.status, tc.code)
		})
	}
	// 1000 karakter unicode masih diterima (batas dihitung per karakter).
	decodeAnomaly(t, patchAnomaly(t, e, id, map[string]any{"status": "ACK", "note": strings.Repeat("é", 1000)}))
	if n := e.count(`SELECT count(*) FROM audit_logs WHERE entity_id = ?`, id); n != 1 {
		t.Fatalf("audit %d baris, ingin 1 (request gagal tidak tercatat)", n)
	}

	// Anomali di luar scope pemanggil: 404, bukan bocor keberadaannya.
	other := e.anomaly(anomalyFx{rule: "STOCK_DISCREPANCY", subjectType: "LOCATION", subjectID: &bandungID})
	own := e.anomaly(anomalyFx{rule: "STOCK_DISCREPANCY", subjectType: "LOCATION", subjectID: &jakselID})
	mgr := e.engine(jakselPrincipal("anomaly.resolve"))
	requireProblem(t, call(t, mgr, http.MethodPatch, "/api/v1/cc/anomalies/"+other.String(), map[string]any{"status": "ACK"}),
		http.StatusNotFound, "anomaly_not_found")
	if r := call(t, mgr, http.MethodPatch, "/api/v1/cc/anomalies/"+own.String(), map[string]any{"status": "ACK"}); r.Code != http.StatusOK {
		t.Fatalf("anomali lokasi sendiri: %d %s", r.Code, r.Body)
	}
}

// TestAnomaly_ConcurrentUpdatesOneWins: banyak permintaan menutup anomali yang
// sama bersamaan → tepat satu 200, sisanya 409, satu baris audit.
func TestAnomaly_ConcurrentUpdatesOneWins(t *testing.T) {
	e := newEnv(t)
	id := e.anomaly(anomalyFx{})
	h := e.engine(opsPrincipal())
	const n = 12
	codes := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			status := "RESOLVED"
			if i%2 == 1 {
				status = "FALSE_POSITIVE"
			}
			codes[i] = call(t, h, http.MethodPatch, "/api/v1/cc/anomalies/"+id.String(),
				map[string]any{"status": status, "note": "paralel"}).Code
		}(i)
	}
	close(start)
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("ingin 1 sukses dan %d konflik, dapat %v", n-1, codes)
	}
	if v := e.count(`SELECT version FROM anomalies WHERE id = ?`, id); v != 1 {
		t.Fatalf("version %d, ingin 1", v)
	}
	if a := e.count(`SELECT count(*) FROM audit_logs WHERE entity_id = ?`, id); a != 1 {
		t.Fatalf("audit %d baris, ingin 1", a)
	}
}

// TestAnomaly_ListFiltersAndKeyset: filter status/severity/rule/party dan
// halaman berurutan saat banyak baris punya created_at yang sama.
func TestAnomaly_ListFiltersAndKeyset(t *testing.T) {
	e := newEnv(t)
	same := testNow.Add(-2 * time.Hour)
	var all []uuid.UUID
	for i := range 7 {
		all = append(all, e.anomaly(anomalyFx{createdAt: same, severity: []string{"LOW", "HIGH"}[i%2], partyID: &jatimPartyID}))
	}
	newer := e.anomaly(anomalyFx{createdAt: testNow.Add(-time.Minute), rule: "IMPOSSIBLE_TRAVEL", severity: "HIGH", partyID: &sulselPartyID})
	older := e.anomaly(anomalyFx{createdAt: testNow.Add(-48 * time.Hour), status: "ACK"})
	h := e.engine(opsPrincipal())

	// Urutan yang benar: created_at DESC, id DESC.
	slices.SortFunc(all, func(a, b uuid.UUID) int { return strings.Compare(b.String(), a.String()) })
	want := append(append([]uuid.UUID{newer}, all...), older)

	var seen []uuid.UUID
	cursor := ""
	for range 20 {
		path := "/api/v1/cc/anomalies?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var page pagination.Page[dto.CCAnomalyItem]
		getJSON(t, h, path, &page)
		for _, it := range page.Items {
			seen = append(seen, it.ID)
			if string(it.Evidence) != `{"sumber": "test"}` && string(it.Evidence) != `{"sumber":"test"}` {
				t.Fatalf("evidence salah: %s", it.Evidence)
			}
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("halaman salah:\n dapat %v\n ingin %v", seen, want)
	}

	countOf := func(path string) int {
		var page pagination.Page[dto.CCAnomalyItem]
		getJSON(t, h, path, &page)
		return len(page.Items)
	}
	for path, n := range map[string]int{
		"/api/v1/cc/anomalies?status=ACK":                                     1,
		"/api/v1/cc/anomalies?status=OPEN":                                    8,
		"/api/v1/cc/anomalies?severity=HIGH":                                  4,
		"/api/v1/cc/anomalies?rule=IMPOSSIBLE_TRAVEL":                         1,
		"/api/v1/cc/anomalies?party_id=" + jatimPartyID.String():              7,
		"/api/v1/cc/anomalies?party_id=" + sulselPartyID.String():             1,
		"/api/v1/cc/anomalies?status=RESOLVED":                                0,
		"/api/v1/cc/anomalies?rule=TIDAK_ADA":                                 0,
		"/api/v1/cc/anomalies?status=OPEN&severity=LOW&rule=OUT_OF_TERRITORY": 4,
	} {
		if got := countOf(path); got != n {
			t.Errorf("%s: %d item, ingin %d", path, got, n)
		}
	}
}

func TestConflicts_KeysetWithSameCreatedAt(t *testing.T) {
	e := newEnv(t)
	same := testNow.Add(-time.Hour)
	var ids []uuid.UUID
	for range 5 {
		ids = append(ids, e.conflict(jakselID, sku1ID, "OPEN", same))
	}
	resolved := e.conflict(bandungID, sku2ID, "RESOLVED", testNow.Add(-3*time.Hour))
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(b.String(), a.String()) })
	want := append(ids, resolved)

	h := e.engine(opsPrincipal())
	var seen []uuid.UUID
	cursor := ""
	for range 10 {
		path := "/api/v1/cc/conflicts?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var page pagination.Page[dto.CCConflictItem]
		getJSON(t, h, path, &page)
		for _, it := range page.Items {
			seen = append(seen, it.ID)
			if it.SKU == "" || it.LocationCode == "" || string(it.Details) != "{}" {
				t.Fatalf("join/details salah: %+v", it)
			}
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("halaman salah:\n dapat %v\n ingin %v", seen, want)
	}

	var open pagination.Page[dto.CCConflictItem]
	getJSON(t, h, "/api/v1/cc/conflicts?status=OPEN&kind=OFFLINE_SALE_PREEMPT&location_id="+jakselID.String(), &open)
	if len(open.Items) != 5 {
		t.Fatalf("filter konflik %d, ingin 5", len(open.Items))
	}
}

// TestAnomaly_NoteControlCharacters: NUL dan karakter kontrol lain → 422
// (bukan 500); baris baru dan tab diterima.
func TestAnomaly_NoteControlCharacters(t *testing.T) {
	e := newEnv(t)
	id := e.anomaly(anomalyFx{})
	for name, note := range map[string]string{
		"NUL":    "cek\x00gudang",
		"ESC":    "cek \x1b[31m merah",
		"CR":     "baris\rtimpa",
		"DEL":    "hapus\x7f",
		"C1 NEL": "baris\u0085baru",
	} {
		t.Run(name, func(t *testing.T) {
			p := requireProblem(t, patchAnomaly(t, e, id, map[string]any{"status": "RESOLVED", "note": note}),
				http.StatusUnprocessableEntity, "validation_failed")
			if len(p.Errors) != 1 || p.Errors[0].Field != "note" || p.Errors[0].Code != "invalid_characters" {
				t.Fatalf("error salah: %+v", p.Errors)
			}
		})
	}
	if n := e.count(`SELECT count(*) FROM audit_logs WHERE entity_id = ?`, id); n != 0 {
		t.Fatalf("request ditolak tapi audit tertulis %d", n)
	}
	note := "Langkah:\n\t1. takedown listing\n\t2. surati distributor"
	got := decodeAnomaly(t, patchAnomaly(t, e, id, map[string]any{"status": "RESOLVED", "note": note}))
	if got.ResolutionNote == nil || *got.ResolutionNote != note {
		t.Fatalf("baris baru/tab harus dipertahankan: %+v", got.ResolutionNote)
	}
}

// TestAnomaly_AckNoteOnlyInAudit: catatan pada ACK diterima, tidak disimpan di
// anomali, hanya di audit_logs.reason.
func TestAnomaly_AckNoteOnlyInAudit(t *testing.T) {
	e := newEnv(t)
	id := e.anomaly(anomalyFx{})
	got := decodeAnomaly(t, patchAnomaly(t, e, id, map[string]any{"status": "ACK", "note": "Sedang dicek tim area"}))
	if got.Status != "ACK" || got.ResolutionNote != nil {
		t.Fatalf("ACK tidak boleh mengisi resolution_note: %+v", got)
	}
	var reason, after string
	if err := e.db.Raw(`SELECT reason, after::text FROM audit_logs WHERE entity_id = ?`, id).Row().Scan(&reason, &after); err != nil {
		t.Fatal(err)
	}
	if reason != "Sedang dicek tim area" || strings.Contains(after, "resolution_note") {
		t.Fatalf("audit ACK salah: reason=%q after=%s", reason, after)
	}
}
