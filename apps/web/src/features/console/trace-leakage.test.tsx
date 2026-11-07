import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { LeakageItem, Trace } from "./api";
import { meFixture, mockApi, problem, renderApp } from "@/test/render-app";
import { NOW } from "./test-fixtures";

const trace: Trace = {
  serial: {
    id: "s1",
    code: "7K3M9QX2TB4H8N6P",
    level_code: "UNIT",
    status: "AT_PARTNER",
    parent_id: "s2",
    product: { id: "p1", code: "MG-BRS-5KG", name: "Beras Organik 5 kg" },
    batch: { id: "b1", lot_no: "L2410A", status: "ACTIVE", exp_date: "2027-10-01T00:00:00Z" },
    location: { id: "l1", code: "PW-MKS", name: "Gudang Mitra Makassar" },
    custodian: { id: "d1", code: "DST-SULSEL", name: "Distributor Sulsel" },
    commissioned_at: NOW,
    verify_count: 2,
    first_verified_at: NOW,
    first_verified_city: "Jakarta Selatan",
  },
  ancestors: [
    { id: "s3", code: "PLT0000000000001", level_code: "PALLET", status: "AT_PARTNER", depth: 2 },
    { id: "s2", code: "CTN0000000000001", level_code: "CARTON", status: "AT_PARTNER", depth: 1 },
  ],
  descendants: [],
  events: [
    {
      id: "e2",
      biz_step: "shipping",
      from_status: "IN_STOCK",
      to_status: "IN_TRANSIT",
      parent_code: null,
      location_code: "CWH-JKT",
      party_name: null,
      actor_type: "USER",
      ref_type: null,
      ref_id: null,
      occurred_at: "2026-10-02T03:00:00Z",
    },
  ],
  shipments: [
    {
      id: "sh1",
      code: "SH-123",
      status: "RECEIVED",
      program_code: "SUBSIDI_SULSEL_Q4",
      from_location_code: "CWH-JKT",
      to_location_code: "PW-MKS",
      to_party_name: "Distributor Sulsel",
      via_code: "PLT0000000000001",
      shipped_at: "2026-10-02T03:00:00Z",
      received_at: "2026-10-05T03:00:00Z",
    },
  ],
  scans: [],
};

describe("telusur serial", () => {
  it("menelusuri kode dan menyusun kalimat ringkasan", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/serials/7K3M9QX2TB4H8N6P/trace": { body: trace },
    });
    const { router } = renderApp("/console/serial");

    expect(await screen.findByText("Masukkan kode serial untuk melihat jejaknya")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Telusuri" }));
    expect(await screen.findByText("Isi kode serial.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Kode serial"), { target: { value: " 7K3M9QX2TB4H8N6P " } });
    fireEvent.click(screen.getByRole("button", { name: "Telusuri" }));

    expect(
      await screen.findByText(
        "Unit ini berstatus di mitra di Gudang Mitra Makassar, dipegang Distributor Sulsel. Berada di dalam karton CTN0000000000001, di dalam palet PLT0000000000001.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("SUBSIDI_SULSEL_Q4")).toBeInTheDocument();
    expect(screen.getByText("Belum ada scan konsumen.")).toBeInTheDocument();
    expect(router.state.location.search).toBe("?kode=7K3M9QX2TB4H8N6P");
    expect(api.find("GET", "/api/v1/serials/7K3M9QX2TB4H8N6P/trace")).toHaveLength(1);
  });

  it("kode tidak dikenal menampilkan pesan server, bukan error", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/serials/%C3%9CNBEKANNT/trace": problem(404, "serial_not_found", "Kode serial tidak terdaftar."),
    });
    renderApp("/console/serial?kode=%C3%9CNBEKANNT");

    expect(await screen.findByText("Serial tidak ditemukan")).toBeInTheDocument();
    expect(screen.getByText("Kode serial tidak terdaftar.")).toBeInTheDocument();
  });
});

describe("kebocoran distribusi", () => {
  const item: LeakageItem = {
    party_id: "d1",
    party_name: "Distributor Sulsel",
    coverage_mode: "SELECTED",
    total_scans: 40,
    located_scans: 40,
    unlocated_scans: 0,
    judged_scans: 32,
    unjudged_scans: 8,
    out_of_territory_scans: 8,
    leak_ratio: 0.25,
    sample_too_small: false,
    test_buy_scans: 0,
    test_buy_hits: 0,
    above_baseline: true,
    alert: true,
  };

  it("menjelaskan rasio, batas normal, dan rentang waktu", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/distribution-leakage": {
        body: { items: [item], next_cursor: null, window_days: 30, from: "2026-10-08T03:00:00Z", to: NOW, min_scans: 20, baseline_ratio: 0.05 },
      },
    });
    renderApp("/console/distribusi");

    expect(await screen.findByText("Dari 1 distributor yang tampil, 1 perlu ditindaklanjuti.")).toBeInTheDocument();
    expect(screen.getByText("25% (8 dari 32 scan)")).toBeInTheDocument();
    expect(screen.getByText("Di atas batas normal 5%.")).toBeInTheDocument();
    expect(screen.getByText("Perlu tindakan")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Rentang waktu"), { target: { value: "90" } });
    await waitFor(() => expect(api.calls.at(-1)?.query.get("days")).toBe("90"));
  });
});
