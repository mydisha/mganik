package inventorytest_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"mganik/api/internal/auth"
	"mganik/api/internal/controller"
	"mganik/api/internal/dto"
	"mganik/api/internal/inventorytest"
	"mganik/api/testutil"
)

type authPrincipal = auth.Principal

func TestBalances_ListScopeAndPagination(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	for _, loc := range []uuid.UUID{f.Central, f.StoreA, f.StoreB} {
		for _, prod := range []uuid.UUID{f.ProductA, f.ProductB} {
			f.Receive(loc, prod, 5)
		}
	}
	f.SetBuffer(f.StoreA, f.ProductA, 2)
	list := func(p *authPrincipal, q url.Values) (int, dto.BalanceListResponse, []byte) {
		rec := f.Do(p, http.MethodGet, "/api/v1/inventory/balances?"+q.Encode(), nil, nil)
		var r dto.BalanceListResponse
		if rec.Code == 200 {
			inventorytest.Decode(t, rec, &r)
		}
		return rec.Code, r, rec.Body.Bytes()
	}
	reader := inventorytest.Staff("inventory.read")

	t.Run("pagination keyset mengembalikan semua baris tepat sekali", func(t *testing.T) {
		seen := map[string]bool{}
		q := url.Values{"limit": {"2"}}
		pages := 0
		for {
			code, r, body := list(reader, q)
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			pages++
			for _, it := range r.Items {
				k := it.LocationID.String() + it.ProductID.String()
				if seen[k] {
					t.Fatalf("baris ganda %s", k)
				}
				seen[k] = true
			}
			if r.NextCursor == nil {
				break
			}
			q.Set("cursor", *r.NextCursor)
		}
		if len(seen) != 6 || pages != 3 {
			t.Fatalf("baris %d halaman %d, ingin 6 dan 3", len(seen), pages)
		}
	})
	t.Run("ATP online: buffer dan OFFLINE", func(t *testing.T) {
		_, r, _ := list(reader, url.Values{"product_id": {f.ProductA.String()}})
		got := map[uuid.UUID]int64{}
		for _, it := range r.Items {
			got[it.LocationID] = it.OnlineATP
		}
		if got[f.Central] != 5 || got[f.StoreA] != 3 || got[f.StoreB] != 0 {
			t.Fatalf("online_atp %v", got)
		}
	})
	t.Run("scope lokasi principal", func(t *testing.T) {
		store := inventorytest.StaffAt([]uuid.UUID{f.StoreA}, "inventory.read")
		code, r, _ := list(store, url.Values{})
		if code != 200 || len(r.Items) != 2 {
			t.Fatalf("principal toko A melihat %d baris", len(r.Items))
		}
		for _, it := range r.Items {
			if it.LocationID != f.StoreA {
				t.Fatal("bocor lokasi di luar scope")
			}
		}
		if code, _, _ := list(store, url.Values{"location_id": {f.Central.String()}}); code != 403 {
			t.Fatalf("filter lokasi luar scope: %d", code)
		}
		noScope := inventorytest.StaffAt(nil, "inventory.read")
		if code, r, _ := list(noScope, url.Values{}); code != 200 || len(r.Items) != 0 {
			t.Fatal("scope SELECTED tanpa lokasi tidak boleh melihat apa pun")
		}
	})
	t.Run("autentikasi, permission, dan validasi query", func(t *testing.T) {
		if code, _, _ := list(nil, url.Values{}); code != 401 {
			t.Fatalf("anonim %d", code)
		}
		if code, _, _ := list(inventorytest.Staff("order.read"), url.Values{}); code != 403 {
			t.Fatalf("tanpa inventory.read %d", code)
		}
		for _, q := range []url.Values{{"limit": {"0"}}, {"limit": {"201"}}, {"location_id": {"x"}}, {"cursor": {"!!"}}} {
			if code, _, body := list(reader, q); code != 422 {
				t.Fatalf("query %v: %d %s", q, code, body)
			}
		}
	})
}

func TestAdjustmentAndQuarantine(t *testing.T) {
	f := inventorytest.New(t, testutil.NewDB(t))
	f.Receive(f.Central, f.ProductA, 10)
	ops := inventorytest.Staff(inventorytest.AllInventoryPerms...)
	post := func(p *authPrincipal, path, key string, body map[string]any) (int, dto.StockChangeResponse, []byte) {
		h := map[string]string{}
		if key != "" {
			h[controller.IdempotencyKeyHeader] = key
		}
		rec := f.Do(p, http.MethodPost, "/api/v1/inventory/"+path, body, h)
		var r dto.StockChangeResponse
		if rec.Code < 300 {
			inventorytest.Decode(t, rec, &r)
		}
		return rec.Code, r, rec.Body.Bytes()
	}
	adjust := func(loc, prod uuid.UUID, delta int64, reason string) map[string]any {
		return map[string]any{"location_id": loc, "product_id": prod, "qty_delta": delta, "reason": reason}
	}

	t.Run("adjustment diaudit, alasan unicode utuh, replay aman", func(t *testing.T) {
		reason := "Selisih opname ✓ rak B-2 (σ=3) — dicek Bu Sri"
		key := uuid.NewString()
		code, r, body := post(ops, "adjustments", key, adjust(f.Central, f.ProductA, -3, reason))
		if code != 201 || r.Balance.PhysicalQty != 7 || r.Movement.MovementType != "ADJUST_COUNT" || *r.Movement.Reason != reason {
			t.Fatalf("%d %s", code, body)
		}
		var auditReason string
		if err := f.DB.Raw(`SELECT reason FROM audit_logs WHERE action = 'inventory.adjust' AND entity_id = ?`, r.Movement.ID).Scan(&auditReason).Error; err != nil || auditReason != reason {
			t.Fatalf("audit %q %v", auditReason, err)
		}
		code, again, _ := post(ops, "adjustments", key, adjust(f.Central, f.ProductA, -3, reason))
		if code != 200 || !again.Duplicate || again.Movement.ID != r.Movement.ID {
			t.Fatalf("replay %d %+v", code, again)
		}
		if n := f.Count(`SELECT count(*) FROM audit_logs WHERE action = 'inventory.adjust'`); n != 1 {
			t.Fatalf("audit ganda %d", n)
		}
		if code, _, body := post(ops, "adjustments", key, adjust(f.Central, f.ProductA, -4, reason)); code != 422 || problemCode(t, body) != "idempotency_key_reused" {
			t.Fatalf("key sama isi beda %d %s", code, body)
		}
	})
	t.Run("adjustment positif membuat saldo baru", func(t *testing.T) {
		code, r, body := post(ops, "adjustments", uuid.NewString(), adjust(f.Hub, f.ProductC, 4, "Temuan opname"))
		if code != 201 || r.Balance.PhysicalQty != 4 {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("validasi adjustment", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body map[string]any
			key  string
			want int
			code string
		}{
			{"delta 0", adjust(f.Central, f.ProductA, 0, "alasan sah"), "k", 422, "validation_failed"},
			{"alasan kosong", adjust(f.Central, f.ProductA, 1, "   "), "k", 422, "validation_failed"},
			{"alasan terlalu pendek", adjust(f.Central, f.ProductA, 1, "ok"), "k", 422, "validation_failed"},
			{"di luar batas", adjust(f.Central, f.ProductA, 1_000_001, "alasan sah"), "k", 422, "validation_failed"},
			{"melebihi available", adjust(f.Central, f.ProductA, -100, "alasan sah"), uuid.NewString(), 409, "insufficient_stock"},
			{"lokasi tidak ada", adjust(uuid.New(), f.ProductA, 1, "alasan sah"), uuid.NewString(), 403, "forbidden"},
			{"produk tidak ada", adjust(f.Central, uuid.New(), 1, "alasan sah"), uuid.NewString(), 422, "product_not_found"},
			{"tanpa Idempotency-Key", adjust(f.Central, f.ProductA, 1, "alasan sah"), "", 400, "idempotency_key_required"},
		} {
			p := ops
			if tc.name == "lokasi tidak ada" {
				p = inventorytest.StaffAt([]uuid.UUID{f.Central}, "inventory.adjust")
			}
			code, _, body := post(p, "adjustments", tc.key, tc.body)
			if code != tc.want || problemCode(t, body) != tc.code {
				t.Errorf("%s: %d %s", tc.name, code, body)
			}
		}
	})
	t.Run("karantina masuk/keluar dengan audit", func(t *testing.T) {
		q := func(dir string, qty int64) map[string]any {
			return map[string]any{"location_id": f.Central, "product_id": f.ProductA, "qty": qty, "direction": dir, "reason": "Kemasan penyok"}
		}
		before := f.Balance(f.Central, f.ProductA)
		code, r, body := post(ops, "quarantine", uuid.NewString(), q("IN", 3))
		if code != 201 || r.Balance.QuarantineQty != before.QuarantineQty+3 || r.Balance.AvailableQty != before.AvailableQty-3 {
			t.Fatalf("%d %s", code, body)
		}
		if code, r, _ := post(ops, "quarantine", uuid.NewString(), q("OUT", 1)); code != 201 || r.Balance.QuarantineQty != before.QuarantineQty+2 {
			t.Fatal("rilis karantina gagal")
		}
		if code, _, body := post(ops, "quarantine", uuid.NewString(), q("OUT", 50)); code != 409 || problemCode(t, body) != "insufficient_stock" {
			t.Fatalf("rilis melebihi karantina %d %s", code, body)
		}
		if code, _, body := post(ops, "quarantine", uuid.NewString(), q("IN", 500)); code != 409 {
			t.Fatalf("karantina melebihi available %d %s", code, body)
		}
		if code, _, _ := post(ops, "quarantine", uuid.NewString(), q("SAMPING", 1)); code != 422 {
			t.Fatal("direction tidak valid harus 422")
		}
		if n := f.Count(`SELECT count(*) FROM audit_logs WHERE action LIKE 'inventory.quarantine%'`); n != 2 {
			t.Fatalf("audit karantina %d", n)
		}
	})
	t.Run("authz", func(t *testing.T) {
		store := inventorytest.StaffAt([]uuid.UUID{f.StoreA}, inventorytest.AllInventoryPerms...)
		if code, _, _ := post(store, "adjustments", uuid.NewString(), adjust(f.Central, f.ProductA, 1, "alasan sah")); code != 403 {
			t.Fatalf("adjust lokasi luar scope %d", code)
		}
		readOnly := inventorytest.Staff("inventory.read")
		if code, _, _ := post(readOnly, "adjustments", uuid.NewString(), adjust(f.Central, f.ProductA, 1, "alasan sah")); code != 403 {
			t.Fatalf("adjust tanpa permission %d", code)
		}
		if code, _, _ := post(readOnly, "quarantine", uuid.NewString(), map[string]any{
			"location_id": f.Central, "product_id": f.ProductA, "qty": 1, "direction": "IN", "reason": "alasan sah",
		}); code != 403 {
			t.Fatalf("karantina tanpa permission %d", code)
		}
		if code, _, _ := post(nil, "adjustments", uuid.NewString(), adjust(f.Central, f.ProductA, 1, "alasan sah")); code != 401 {
			t.Fatalf("anonim %d", code)
		}
	})
	f.AssertInvariants()
}
