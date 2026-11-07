-- +goose Up
-- Index untuk read model Command Center (Pilar D). Tidak ada tabel baru:
-- Command Center hanya membaca.

-- Antrean anomali dan konflik: pagination keyset `(created_at, id)` terbaru
-- dulu, dengan dan tanpa filter status. created_at dipakai (bukan last_seen_at)
-- karena tidak pernah berubah, jadi urutan halaman stabil walau hit_count naik.
CREATE INDEX anomalies_created_keyset ON anomalies (created_at DESC, id DESC);
CREATE INDEX anomalies_status_created_keyset ON anomalies (status, created_at DESC, id DESC);
CREATE INDEX allocation_conflicts_created_keyset ON allocation_conflicts (created_at DESC, id DESC);

-- Penjualan 28 hari untuk DOI/runout: partial + INCLUDE supaya agregasi
-- dibaca dari index saja (index-only scan, tanpa heap fetch). Diukur pada
-- 180 ribu baris ledger: buffer agregasi 2.189 → 317, waktu 19,5 → 12,6 ms
-- dibanding bitmap scan index inventory_movements_type_time.
-- Indeks kebocoran wilayah memakai index scan_events yang sudah ada (BRIN
-- occurred_at); index tambahan belum terbukti dipakai planner, jadi tidak dibuat.
CREATE INDEX inventory_movements_sales_window
  ON inventory_movements (occurred_at, location_id, product_id)
  INCLUDE (physical_delta)
  WHERE movement_type IN ('DIRECT_SALE', 'COMMIT_SALE');

-- +goose Down
DROP INDEX IF EXISTS inventory_movements_sales_window;
DROP INDEX IF EXISTS allocation_conflicts_created_keyset;
DROP INDEX IF EXISTS anomalies_status_created_keyset;
DROP INDEX IF EXISTS anomalies_created_keyset;
