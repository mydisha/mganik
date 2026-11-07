import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { meFixture, mockApi, renderApp, stubMobile } from "@/test/render-app";
import { anomalyFixture, emptyPage, emptyRunout, runoutFixture, summaryFixture } from "./test-fixtures";

describe("Beranda", () => {
  it("menyusun kalimat ringkasan, runout teratas, dan anomali terbuka", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/summary": {
        body: summaryFixture({
          runout: { ...summaryFixture().runout, critical_items: 1, critical_locations: 1 },
          anomalies: { open_total: 1, open_by_severity: { LOW: 0, MEDIUM: 0, HIGH: 1, CRITICAL: 0 }, top_party: null },
        }),
      },
      "GET /api/v1/cc/runout": {
        body: { ...emptyRunout, items: [runoutFixture(), runoutFixture({ level: "OK", product_name: "Gula Aren", product_id: "x" })] },
      },
      "GET /api/v1/cc/anomalies": { body: { items: [anomalyFixture()], next_cursor: null } },
    });
    renderApp("/console");

    expect(await screen.findByText(/Stok 1 SKU di 1 lokasi kritis/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "1 anomali" })).toHaveAttribute("href", "/console/anomali");

    const runout = await screen.findByRole("table", { name: "Akan habis paling cepat" });
    expect(within(runout).getByText("Beras Organik 5 kg")).toBeInTheDocument();
    // Baris berlevel aman tidak tampil di Beranda.
    expect(within(runout).queryByText("Gula Aren")).not.toBeInTheDocument();

    const anomalies = await screen.findByRole("table", { name: "Anomali terbuka terbaru" });
    expect(within(anomalies).getByText("Di luar wilayah distributor")).toBeInTheDocument();
    expect(api.find("GET", "/api/v1/cc/anomalies")[0]?.query.get("status")).toBe("OPEN");
  });

  it("kasus nol: kalimat tenang dan empty state yang menjelaskan", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/summary": { body: summaryFixture() },
      "GET /api/v1/cc/runout": { body: emptyRunout },
      "GET /api/v1/cc/anomalies": { body: emptyPage },
    });
    renderApp("/console");

    expect(await screen.findByText(/Belum ada yang perlu ditindaklanjuti/)).toBeInTheDocument();
    expect(await screen.findByText("Tidak ada stok yang kritis atau menipis")).toBeInTheDocument();
    expect(await screen.findByText("Tidak ada anomali terbuka")).toBeInTheDocument();
  });

  it("ringkasan gagal dimuat menampilkan detail server dan coba lagi", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/summary": { status: 500, body: { type: "about:blank", title: "Galat", status: 500, code: "internal_error", detail: "Terjadi kesalahan di server.", request_id: "req-123" } },
      "GET /api/v1/cc/runout": { body: emptyRunout },
      "GET /api/v1/cc/anomalies": { body: emptyPage },
    });
    renderApp("/console");

    expect(await screen.findByText("Ringkasan tidak bisa dimuat")).toBeInTheDocument();
    expect(screen.getByText("Terjadi kesalahan di server.")).toBeInTheDocument();
    expect(screen.getByText("req-123")).toBeInTheDocument();
  });

  it("di bawah 768 px tabel menjadi kartu", async () => {
    stubMobile(true);
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/summary": { body: summaryFixture() },
      "GET /api/v1/cc/runout": { body: { ...emptyRunout, items: [runoutFixture()] } },
      "GET /api/v1/cc/anomalies": { body: emptyPage },
    });
    renderApp("/console");

    const list = await screen.findByRole("list", { name: "Akan habis paling cepat" });
    expect(within(list).getByText("Beras Organik 5 kg")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});
