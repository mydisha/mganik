import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ALL_PERMISSIONS, meFixture, mockApi, problem, renderApp } from "@/test/render-app";
import { anomalyFixture, emptyPage } from "./test-fixtures";

async function openDetail() {
  fireEvent.click(await screen.findByRole("button", { name: "Lihat detail Di luar wilayah distributor" }));
  return screen.findByRole("dialog");
}

describe("antrean anomali", () => {
  it("dimulai dari status Terbuka dan bisa disaring", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/anomalies": { body: { items: [anomalyFixture()], next_cursor: null } },
    });
    renderApp("/console/anomali");

    expect(await screen.findByRole("table", { name: "Antrean anomali" })).toBeInTheDocument();
    expect(api.calls.at(-1)?.query.get("status")).toBe("OPEN");

    fireEvent.change(screen.getByLabelText("Prioritas"), { target: { value: "CRITICAL" } });
    await waitFor(() => expect(api.calls.at(-1)?.query.get("severity")).toBe("CRITICAL"));

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "SEMUA" } });
    await waitFor(() => expect(api.calls.at(-1)?.query.has("status")).toBe(false));
  });

  it("menampilkan bukti dan mewajibkan catatan untuk Selesai", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/anomalies": { body: { items: [anomalyFixture()], next_cursor: null } },
      "PATCH /api/v1/cc/anomalies/0193a000-0000-7000-8000-00000000c001": (call) => ({
        body: anomalyFixture({ status: "RESOLVED", resolution_note: (call.body as { note: string }).note, version: 2 }),
      }),
    });
    renderApp("/console/anomali");

    const panel = await openDetail();
    expect(within(panel).getByText("Kode serial")).toBeInTheDocument();
    expect(within(panel).getByText("7K3M9QX2TB4H8N6P")).toBeInTheDocument();

    fireEvent.click(within(panel).getByLabelText("Selesai, masalahnya sudah ditangani"));
    fireEvent.click(within(panel).getByRole("button", { name: "Simpan tindak lanjut" }));
    expect(await within(panel).findByText("Tulis catatan sebelum menutup anomali.")).toBeInTheDocument();
    expect(api.find("PATCH", "/api/v1/cc/anomalies/0193a000-0000-7000-8000-00000000c001")).toHaveLength(0);

    // Spasi saja tidak dihitung sebagai catatan.
    fireEvent.change(within(panel).getByLabelText("Catatan"), { target: { value: "   " } });
    fireEvent.click(within(panel).getByRole("button", { name: "Simpan tindak lanjut" }));
    expect(await within(panel).findByText("Tulis catatan sebelum menutup anomali.")).toBeInTheDocument();

    fireEvent.change(within(panel).getByLabelText("Catatan"), { target: { value: "  Sudah dicek: konsumen pindah ke Jakarta 🚚 ✓ " } });
    fireEvent.click(within(panel).getByRole("button", { name: "Simpan tindak lanjut" }));

    await waitFor(() =>
      expect(api.find("PATCH", "/api/v1/cc/anomalies/0193a000-0000-7000-8000-00000000c001")[0]?.body).toEqual({
        status: "RESOLVED",
        note: "Sudah dicek: konsumen pindah ke Jakarta 🚚 ✓",
      }),
    );
    expect(await screen.findByText("Anomali ditutup sebagai selesai.")).toBeInTheDocument();
    expect(await within(panel).findByText("Anomali ini sudah ditutup; tidak ada tindak lanjut lagi.")).toBeInTheDocument();
  });

  it("ACK tanpa catatan boleh dan mengirim note null", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/anomalies": { body: { items: [anomalyFixture()], next_cursor: null } },
      "PATCH /api/v1/cc/anomalies/0193a000-0000-7000-8000-00000000c001": { body: anomalyFixture({ status: "ACK", version: 2 }) },
    });
    renderApp("/console/anomali");

    const panel = await openDetail();
    fireEvent.click(within(panel).getByLabelText("Tandai sedang ditangani"));
    fireEvent.click(within(panel).getByRole("button", { name: "Simpan tindak lanjut" }));

    await waitFor(() =>
      expect(api.find("PATCH", "/api/v1/cc/anomalies/0193a000-0000-7000-8000-00000000c001")[0]?.body).toEqual({ status: "ACK", note: null }),
    );
  });

  it("memilih tindakan wajib", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/anomalies": { body: { items: [anomalyFixture()], next_cursor: null } },
    });
    renderApp("/console/anomali");

    const panel = await openDetail();
    fireEvent.click(within(panel).getByRole("button", { name: "Simpan tindak lanjut" }));
    expect(await within(panel).findByText("Pilih tindakan.")).toBeInTheDocument();
  });

  it("error server (409) ditampilkan apa adanya", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/anomalies": { body: { items: [anomalyFixture()], next_cursor: null } },
      "PATCH /api/v1/cc/anomalies/0193a000-0000-7000-8000-00000000c001": problem(
        409,
        "invalid_transition",
        "Anomali sudah ditutup oleh pengguna lain.",
      ),
    });
    renderApp("/console/anomali");

    const panel = await openDetail();
    fireEvent.click(within(panel).getByLabelText("Bukan masalah (alarm palsu)"));
    fireEvent.change(within(panel).getByLabelText("Catatan"), { target: { value: "Scan dari pameran resmi." } });
    fireEvent.click(within(panel).getByRole("button", { name: "Simpan tindak lanjut" }));

    expect(await within(panel).findByText("Anomali sudah ditutup oleh pengguna lain.")).toBeInTheDocument();
  });

  it("tanpa anomaly.resolve hanya bisa melihat", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture(ALL_PERMISSIONS.filter((p) => p !== "anomaly.resolve")) },
      "GET /api/v1/cc/anomalies": { body: { items: [anomalyFixture()], next_cursor: null } },
    });
    renderApp("/console/anomali");

    const panel = await openDetail();
    expect(within(panel).getByText("Bukti")).toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: "Simpan tindak lanjut" })).not.toBeInTheDocument();
  });

  it("filter kosong menawarkan hapus filter", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/anomalies": { body: emptyPage },
    });
    renderApp("/console/anomali?prioritas=LOW");

    expect(await screen.findByText("Tidak ada data yang cocok dengan filter ini")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Hapus semua filter" }).length).toBeGreaterThan(0);
  });
});
