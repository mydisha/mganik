import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ALL_PERMISSIONS, meFixture, mockApi, problem, renderApp } from "@/test/render-app";
import { conflictFixture } from "./test-fixtures";

const ID = "0193a000-0000-7000-8000-00000000e001";
const RESOLVE = `/api/v1/cc/conflicts/${ID}/resolve`;

describe("konflik alokasi", () => {
  it("validasi catatan wajib, lalu mengirim keputusan", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/conflicts": { body: { items: [conflictFixture()], next_cursor: null } },
      [`POST ${RESOLVE}`]: { body: conflictFixture({ status: "RESOLVED", resolution: "CUSTOMER_COMPENSATED", resolved_at: "2026-11-07T04:00:00Z" }) },
    });
    renderApp("/console/konflik");

    fireEvent.click(await screen.findByRole("button", { name: "Selesaikan konflik Beras Organik 5 kg" }));
    const panel = await screen.findByRole("dialog");

    fireEvent.click(within(panel).getByRole("button", { name: "Simpan keputusan" }));
    expect(await within(panel).findByText("Pilih cara penyelesaiannya.")).toBeInTheDocument();
    expect(within(panel).getByText("Tulis catatan minimal 3 karakter.")).toBeInTheDocument();

    fireEvent.click(within(panel).getByLabelText("Pelanggan online sudah diberi kompensasi"));
    fireEvent.change(within(panel).getByLabelText("Catatan"), { target: { value: " ok " } });
    fireEvent.click(within(panel).getByRole("button", { name: "Simpan keputusan" }));
    expect(await within(panel).findByText("Tulis catatan minimal 3 karakter.")).toBeInTheDocument();
    expect(api.find("POST", RESOLVE)).toHaveLength(0);

    fireEvent.change(within(panel).getByLabelText("Catatan"), { target: { value: "Voucher Rp50.000 untuk pelanggan, café dikabari 👍" } });
    fireEvent.click(within(panel).getByRole("button", { name: "Simpan keputusan" }));

    await waitFor(() =>
      expect(api.find("POST", RESOLVE)[0]?.body).toEqual({
        resolution: "CUSTOMER_COMPENSATED",
        note: "Voucher Rp50.000 untuk pelanggan, café dikabari 👍",
      }),
    );
    expect(await within(panel).findByText("Konflik ini sudah selesai.")).toBeInTheDocument();
  });

  it("menampilkan error server (409 resolution_not_applicable)", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/conflicts": { body: { items: [conflictFixture()], next_cursor: null } },
      [`POST ${RESOLVE}`]: problem(409, "resolution_not_applicable", "Pesanan terkait belum dibatalkan."),
    });
    renderApp("/console/konflik");

    fireEvent.click(await screen.findByRole("button", { name: "Selesaikan konflik Beras Organik 5 kg" }));
    const panel = await screen.findByRole("dialog");
    fireEvent.click(within(panel).getByLabelText(/Pesanan online dibatalkan/));
    fireEvent.change(within(panel).getByLabelText("Catatan"), { target: { value: "Pesanan dibatalkan." } });
    fireEvent.click(within(panel).getByRole("button", { name: "Simpan keputusan" }));

    expect(await within(panel).findByRole("alert")).toHaveTextContent("Pesanan terkait belum dibatalkan.");
  });

  it("pilihan pesanan dibatalkan tidak muncul untuk konflik tanpa pesanan", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/conflicts": { body: { items: [conflictFixture({ kind: "STOCK_DISCREPANCY", order_id: null })], next_cursor: null } },
    });
    renderApp("/console/konflik");

    fireEvent.click(await screen.findByRole("button", { name: "Selesaikan konflik Beras Organik 5 kg" }));
    const panel = await screen.findByRole("dialog");
    expect(within(panel).queryByLabelText(/Pesanan online dibatalkan/)).not.toBeInTheDocument();
    expect(within(panel).getByLabelText("Stok sudah dihitung ulang")).toBeInTheDocument();
  });

  it("tanpa conflict.resolve tombolnya menjadi lihat detail", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture(ALL_PERMISSIONS.filter((p) => p !== "conflict.resolve")) },
      "GET /api/v1/cc/conflicts": { body: { items: [conflictFixture()], next_cursor: null } },
    });
    renderApp("/console/konflik");

    fireEvent.click(await screen.findByRole("button", { name: "Lihat detail konflik Beras Organik 5 kg" }));
    const panel = await screen.findByRole("dialog");
    expect(within(panel).getByText("Peran Anda bisa melihat konflik ini, tetapi tidak bisa menyelesaikannya.")).toBeInTheDocument();
  });
});
