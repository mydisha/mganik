import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { meFixture, mockApi, problem, renderApp } from "@/test/render-app";
import { deviceFixture, emptyPage, stockFixture } from "./test-fixtures";

const TOKEN = "pos_9f2c7e1a4b8d6e3f0a5c2b7d9e1f4a6c";
const KEY = "a".repeat(64);

function provisioned(name: string) {
  return { status: 201, body: { device: deviceFixture({ name }), token: TOKEN, signing_key: KEY } };
}

describe("perangkat kasir", () => {
  it("mendaftarkan perangkat lalu menampilkan token sekali", async () => {
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/pos/devices": { body: emptyPage },
      "GET /api/v1/cc/stock": { body: { items: [stockFixture()], next_cursor: null } },
      "POST /api/v1/pos/devices": (call) => provisioned((call.body as { name: string }).name),
    });
    renderApp("/console/perangkat");

    expect(await screen.findByText("Belum ada perangkat kasir")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Daftarkan perangkat" }));
    const form = await screen.findByRole("dialog", { name: "Daftarkan perangkat kasir" });

    const store = await within(form).findByRole("option", { name: "Toko Jakarta Selatan (ST-JKS)" });
    expect(api.find("GET", "/api/v1/cc/stock")[0]?.query.get("location_type")).toBe("OWN_STORE");

    fireEvent.click(within(form).getByRole("button", { name: "Daftarkan" }));
    expect(await within(form).findByText("Pilih toko tempat perangkat dipakai.")).toBeInTheDocument();
    expect(within(form).getByText("Isi nama perangkat.")).toBeInTheDocument();

    fireEvent.change(within(form).getByLabelText("Toko"), { target: { value: (store as HTMLOptionElement).value } });
    fireEvent.change(within(form).getByLabelText("Nama perangkat"), { target: { value: "  Kasir Café №2 ☕ " } });
    fireEvent.click(within(form).getByRole("button", { name: "Daftarkan" }));

    const reveal = await screen.findByRole("dialog", { name: "Kasir Café №2 ☕ sudah terdaftar" });
    expect(api.find("POST", "/api/v1/pos/devices")[0]?.body).toEqual({
      location_id: "0193a000-0000-7000-8000-00000000a001",
      name: "Kasir Café №2 ☕",
    });
    expect(within(reveal).getByText(TOKEN)).toBeInTheDocument();
    expect(within(reveal).getByText(KEY)).toBeInTheDocument();

    // Tidak bisa ditutup sebelum dicentang: tombol nonaktif dan Escape diabaikan.
    const done = within(reveal).getByRole("button", { name: "Selesai" });
    expect(done).toBeDisabled();
    fireEvent.keyDown(reveal, { key: "Escape" });
    expect(screen.getByRole("dialog", { name: "Kasir Café №2 ☕ sudah terdaftar" })).toBeInTheDocument();

    fireEvent.click(within(reveal).getByLabelText("Saya sudah menyimpan token dan signing key di perangkat kasir."));
    expect(done).toBeEnabled();
    fireEvent.click(done);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByText(TOKEN)).not.toBeInTheDocument();
  });

  it("error server saat mendaftar ditampilkan di form", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/pos/devices": { body: emptyPage },
      "GET /api/v1/cc/stock": { body: { items: [stockFixture()], next_cursor: null } },
      "POST /api/v1/pos/devices": problem(422, "location_not_store", "Lokasi ini bukan toko sendiri yang aktif."),
    });
    renderApp("/console/perangkat");

    fireEvent.click(await screen.findByRole("button", { name: "Daftarkan perangkat" }));
    const form = await screen.findByRole("dialog");
    const store = await within(form).findByRole("option", { name: "Toko Jakarta Selatan (ST-JKS)" });
    fireEvent.change(within(form).getByLabelText("Toko"), { target: { value: (store as HTMLOptionElement).value } });
    fireEvent.change(within(form).getByLabelText("Nama perangkat"), { target: { value: "Kasir 1" } });
    fireEvent.click(within(form).getByRole("button", { name: "Daftarkan" }));

    expect(await within(form).findByRole("alert")).toHaveTextContent("Lokasi ini bukan toko sendiri yang aktif.");
  });

  it("mencabut perangkat butuh nama yang diketik persis (unicode)", async () => {
    const name = "Kasir Café 2";
    const api = mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/pos/devices": { body: { items: [deviceFixture({ name })], next_cursor: null } },
      "POST /api/v1/pos/devices/0193a000-0000-7000-8000-0000000d0001/revoke": {
        body: deviceFixture({ name, status: "REVOKED", revoked_at: "2026-11-07T04:00:00Z" }),
      },
    });
    renderApp("/console/perangkat");

    fireEvent.click(await screen.findByRole("button", { name: `Cabut ${name}` }));
    const dialog = await screen.findByRole("dialog", { name: `Cabut ${name}?` });
    const confirm = within(dialog).getByRole("button", { name: "Cabut perangkat" });
    const typed = within(dialog).getByLabelText(`Ketik ${name} untuk mengonfirmasi`);

    fireEvent.change(typed, { target: { value: "kasir café 2" } });
    expect(confirm).toBeDisabled();
    // Bentuk dekomposisi (e + aksen gabungan) dianggap sama.
    fireEvent.change(typed, { target: { value: "Kasir Café 2" } });
    expect(confirm).toBeEnabled();

    fireEvent.change(within(dialog).getByLabelText("Alasan"), { target: { value: "Tablet hilang" } });
    fireEvent.click(confirm);

    await waitFor(() =>
      expect(api.find("POST", "/api/v1/pos/devices/0193a000-0000-7000-8000-0000000d0001/revoke")[0]?.body).toEqual({
        reason: "Tablet hilang",
      }),
    );
    expect(await screen.findByText(`${name} dicabut.`)).toBeInTheDocument();
  });
});
