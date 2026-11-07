import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { meFixture, mockApi, problem, renderApp } from "@/test/render-app";
import { emptyRunout, runoutFixture, stockFixture } from "./test-fixtures";

describe("stok", () => {
  it("menandai estimasi, toko offline, dan memuat halaman berikutnya", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/stock": (call) =>
        call.query.get("cursor") === "c2"
          ? { body: { items: [stockFixture({ product_id: "p2", product_name: "Gula Aren 1 kg", sku: "MG-GLA-1KG" })], next_cursor: null } }
          : {
              body: {
                items: [
                  stockFixture({ sync_state: "OFFLINE" }),
                  stockFixture({
                    location_id: "l2",
                    location_name: "Gudang Mitra Makassar",
                    location_code: "PW-MKS",
                    location_type: "PARTNER_WH",
                    is_estimate: true,
                  }),
                ],
                next_cursor: "c2",
              },
            },
    });
    renderApp("/console/stok");

    await screen.findByText("Gudang Mitra Makassar");
    const table = screen.getByRole("table", { name: "Saldo stok per lokasi dan produk" });
    expect(within(table).getByText("Estimasi")).toBeInTheDocument();
    expect(within(table).getByText("Offline")).toBeInTheDocument();
    expect(screen.getByText(/adalah estimasi dari sell-in/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Muat lebih banyak" }));
    expect(await within(table).findByText("Gula Aren 1 kg")).toBeInTheDocument();
    expect(api.find("GET", "/api/v1/cc/stock").at(-1)?.query.get("cursor")).toBe("c2");
    expect(screen.queryByRole("button", { name: "Muat lebih banyak" })).not.toBeInTheDocument();
  });

  it("menyaring lewat tautan lokasi dan menampilkan chip yang bisa dilepas", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/stock": { body: { items: [stockFixture()], next_cursor: null } },
    });
    renderApp("/console/stok");

    fireEvent.click(await screen.findByRole("link", { name: "Toko Jakarta Selatan" }));
    await waitFor(() => expect(api.calls.at(-1)?.query.get("location_id")).toBe("0193a000-0000-7000-8000-00000000a001"));
    const chip = await screen.findByRole("button", { name: "Hapus filter Lokasi: Toko Jakarta Selatan" });

    fireEvent.click(chip);
    await waitFor(() => expect(api.calls.at(-1)?.query.has("location_id")).toBe(false));
  });

  it("gagal memuat halaman pertama menampilkan error dan coba lagi", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/stock": problem(422, "validation_failed", "Filter lokasi tidak valid."),
    });
    renderApp("/console/stok?tipe=OWN_STORE");

    expect(await screen.findByText("Filter lokasi tidak valid.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Coba lagi" })).toBeInTheDocument();
  });
});

describe("runout", () => {
  it("menjelaskan data belum cukup tanpa angka palsu", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/runout": {
        body: {
          ...emptyRunout,
          items: [
            runoutFixture(),
            runoutFixture({
              product_id: "p2",
              product_name: "Madu Hutan",
              level: "INSUFFICIENT_DATA",
              reasons: ["NO_SALES_HISTORY"],
              days_of_inventory: null,
              avg_daily_sales: null,
              runout_date: null,
              history_days: null,
            }),
          ],
        },
      },
    });
    renderApp("/console/runout");

    await screen.findByText("Madu Hutan");
    const table = screen.getByRole("table", { name: "Perkiraan habis stok per lokasi dan produk" });
    const rows = within(table).getAllByRole("row");
    const madu = rows.find((row) => within(row).queryByText("Madu Hutan"));
    expect(madu).toBeDefined();
    expect(within(madu!).getByText("Data belum cukup")).toBeInTheDocument();
    expect(within(madu!).getAllByText("Belum bisa dihitung")).toHaveLength(2);
    expect(within(madu!).getByText(/tanggal habis tidak dikarang/)).toBeInTheDocument();
    expect(screen.getByText("Perkiraan habis stok dari penjualan 28 hari terakhir, paling kritis di atas.")).toBeInTheDocument();
  });
});
