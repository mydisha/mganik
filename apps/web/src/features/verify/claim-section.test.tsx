import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { type MockHandler, mockApi, problem, renderApp } from "@/test/render-app";

const CODE = "7K3M9QX2TB4H8N6P";
const me = { id: "0193a000-0000-7000-8000-0000000c0001", phone_masked: "+62812*****890" };

async function showGenuine(routes: Record<string, MockHandler>) {
  const api = mockApi({ "POST /api/v1/public/verify": { body: { result: "GENUINE_FIRST" } }, ...routes });
  renderApp(`/v/${CODE}`);
  fireEvent.click(await screen.findByRole("button", { name: "Cek tanpa lokasi" }));
  await screen.findByRole("heading", { name: "Klaim poin" });
  return api;
}

const signedIn = {
  "GET /api/v1/public/consumer/me": { body: me },
  "GET /api/v1/public/loyalty/balance": { body: { available: 1200, pending: 50 } },
};

async function submitPin(pin: string) {
  fireEvent.change(await screen.findByLabelText("PIN di dalam kemasan"), { target: { value: pin } });
  fireEvent.click(screen.getByRole("button", { name: "Klaim poin" }));
}

describe("klaim poin", () => {
  it("login OTP: nomor dinormalkan, kode salah menampilkan pesan server", async () => {
    let loggedIn = false;
    const api = await showGenuine({
      "GET /api/v1/public/consumer/me": () => (loggedIn ? { body: me } : problem(401, "unauthenticated")),
      "POST /api/v1/public/otp/request": { status: 202, body: { expires_at: "2026-11-07T03:05:00Z", expires_in_seconds: 300 } },
      "POST /api/v1/public/otp/verify": (call) => {
        if ((call.body as { code: string }).code !== "123456") return problem(401, "otp_invalid", "Kode salah atau sudah kedaluwarsa.");
        loggedIn = true;
        return { body: me };
      },
      "GET /api/v1/public/loyalty/balance": { body: { available: 0, pending: 0 } },
    });

    fireEvent.change(await screen.findByLabelText("Nomor HP"), { target: { value: "０８１２" } });
    fireEvent.click(screen.getByRole("button", { name: "Kirim kode" }));
    expect(await screen.findByText(/Nomor HP belum benar/)).toBeInTheDocument();
    expect(api.find("POST", "/api/v1/public/otp/request")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Nomor HP"), { target: { value: "0812-3456-7890" } });
    fireEvent.click(screen.getByRole("button", { name: "Kirim kode" }));
    expect(await screen.findByText("Kode dikirim ke +6281234567890. Berlaku 5 menit.")).toBeInTheDocument();
    expect(api.find("POST", "/api/v1/public/otp/request")[0]?.body).toEqual({ phone: "+6281234567890" });

    fireEvent.change(screen.getByLabelText("Kode masuk"), { target: { value: "12a" } });
    fireEvent.click(screen.getByRole("button", { name: "Masuk" }));
    expect(await screen.findByText("Kode masuk terdiri dari 6 angka.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Kode masuk"), { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: "Masuk" }));
    expect(await screen.findByText("Kode salah atau sudah kedaluwarsa.")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Kode masuk"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Masuk" }));
    expect(await screen.findByText("Masuk sebagai +62812*****890.")).toBeInTheDocument();
    expect(await screen.findByText("Saldo Anda 0 poin.")).toBeInTheDocument();
  });

  it("PIN salah tampil di kolom PIN", async () => {
    const api = await showGenuine({
      ...signedIn,
      "POST /api/v1/public/claims": problem(422, "invalid_code_or_pin", "Periksa kembali kode produk dan PIN di dalam kemasan."),
    });
    expect(await screen.findByText("Saldo Anda 1.200 poin, ditambah 50 poin yang masih tertahan.")).toBeInTheDocument();

    await submitPin("ABC");
    expect(await screen.findByText("PIN terdiri dari 10 karakter huruf dan angka.")).toBeInTheDocument();
    expect(api.find("POST", "/api/v1/public/claims")).toHaveLength(0);

    await submitPin("abcd-efgh-jk");
    expect(await screen.findByText("Periksa kembali kode produk dan PIN di dalam kemasan.")).toBeInTheDocument();
    expect(screen.getByLabelText("PIN di dalam kemasan")).toHaveAttribute("aria-invalid", "true");
    expect(api.find("POST", "/api/v1/public/claims")[0]?.body).toEqual({ code: CODE, pin: "ABCDEFGHJK" });
  });

  it("PIN unicode dihitung per karakter", async () => {
    const api = await showGenuine({ ...signedIn, "POST /api/v1/public/claims": problem(422, "invalid_code_or_pin", "PIN salah.") });
    await submitPin("ÄÖÜ1234567");
    await waitFor(() => expect(api.find("POST", "/api/v1/public/claims")[0]?.body).toEqual({ code: CODE, pin: "ÄÖÜ1234567" }));
  });

  it("klaim terkunci menonaktifkan form", async () => {
    await showGenuine({
      ...signedIn,
      "POST /api/v1/public/claims": problem(423, "claim_locked", "Terlalu banyak percobaan PIN salah untuk produk ini. Hubungi layanan pelanggan."),
    });

    await submitPin("ABCDEFGHJK");

    expect(await screen.findByRole("alert")).toHaveTextContent("Terlalu banyak percobaan PIN salah");
    expect(screen.getByRole("button", { name: "Klaim poin" })).toBeDisabled();
    expect(screen.getByLabelText("PIN di dalam kemasan")).toBeDisabled();
  });

  it("sukses dengan masa tunggu menampilkan tanggal poin tersedia", async () => {
    const api = await showGenuine({
      ...signedIn,
      "POST /api/v1/public/claims": {
        status: 201,
        body: { claim_id: "c1", status: "PENDING", points: 100, available_at: "2099-11-14T03:00:00Z", replayed: false },
      },
    });

    await submitPin("ABCDEFGHJK");

    expect(await screen.findByText("100 poin tercatat dan bisa dipakai mulai 14 November 2099.")).toBeInTheDocument();
    await waitFor(() => expect(api.find("GET", "/api/v1/public/loyalty/balance").length).toBeGreaterThan(1));
  });

  it("sukses langsung, review, sudah diklaim, dan ditolak", async () => {
    const outcomes = [
      [{ status: 201, body: { claim_id: "c1", status: "APPROVED", points: 100, available_at: "2026-11-07T03:00:00Z", replayed: false } }, "100 poin masuk ke saldo Anda."],
      [{ status: 201, body: { claim_id: "c1", status: "PENDING", points: 100, available_at: "2020-01-01T00:00:00Z", replayed: false } }, "Klaim Anda sedang diperiksa tim kami. 100 poin masuk setelah klaim disetujui."],
      [{ status: 200, body: { claim_id: "c1", status: "REJECTED", points: 100, available_at: "2020-01-01T00:00:00Z", replayed: true } }, "Klaim ini sudah tercatat sebelumnya. Klaim ini ditolak. Hubungi layanan pelanggan mGanik kalau menurut Anda keputusan ini keliru."],
      [problem(409, "already_claimed", "Poin untuk produk ini sudah diklaim."), "Poin untuk produk ini sudah diklaim."],
    ] as const;

    for (const [response, text] of outcomes) {
      await showGenuine({ ...signedIn, "POST /api/v1/public/claims": response });
      await submitPin("ABCDEFGHJK");
      expect(await screen.findByText(text)).toBeInTheDocument();
      cleanup();
    }
  });
});
