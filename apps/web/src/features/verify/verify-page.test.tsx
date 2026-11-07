import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { mockApi, problem, renderApp } from "@/test/render-app";

const CODE = "7K3M9QX2TB4H8N6P";
const product = { name: "Beras Organik 5 kg", lot_no: "L2410A", exp_date: "2027-10-01T00:00:00Z" };

function stubGeolocation(result: "granted" | "denied") {
  const getCurrentPosition = vi.fn((ok: PositionCallback, fail: PositionErrorCallback) => {
    if (result === "granted") {
      ok({ coords: { latitude: -6.26, longitude: 106.81, accuracy: 35.4 } } as GeolocationPosition);
    } else {
      fail({ code: 1, message: "User denied Geolocation" } as GeolocationPositionError);
    }
  });
  Object.defineProperty(navigator, "geolocation", { value: { getCurrentPosition }, configurable: true });
  return getCurrentPosition;
}

afterEach(() => {
  Reflect.deleteProperty(navigator, "geolocation");
});

function consumerSignedOut() {
  return { "GET /api/v1/public/consumer/me": problem(401, "unauthenticated") };
}

describe("verifikasi konsumen", () => {
  it("tidak meminta lokasi atau memverifikasi sebelum pengguna menekan tombol", async () => {
    const geo = stubGeolocation("granted");
    const api = mockApi({});
    renderApp(`/v/${CODE}`);

    expect(await screen.findByRole("heading", { level: 1, name: "Cek keaslian produk" })).toBeInTheDocument();
    expect(screen.getByText(/Boleh kami pakai lokasi Anda\?/)).toBeInTheDocument();
    expect(geo).not.toHaveBeenCalled();
    expect(api.calls).toHaveLength(0);
  });

  it("GENUINE_FIRST dengan lokasi mengirim koordinat", async () => {
    stubGeolocation("granted");
    const api = mockApi({
      "POST /api/v1/public/verify": { body: { result: "GENUINE_FIRST", product } },
      ...consumerSignedOut(),
    });
    renderApp(`/v/${CODE}`);

    fireEvent.click(await screen.findByRole("button", { name: "Cek dengan lokasi" }));

    expect(await screen.findByRole("heading", { name: "Produk asli" })).toBeInTheDocument();
    expect(screen.getByText("Kode ini terdaftar di mGanik dan baru pertama kali dicek.")).toBeInTheDocument();
    expect(screen.getByText("L2410A")).toBeInTheDocument();
    expect(screen.getByText("1 Oktober 2027")).toBeInTheDocument();
    expect(api.find("POST", "/api/v1/public/verify")[0]?.body).toEqual({ code: CODE, lat: -6.26, lng: 106.81, accuracy_m: 35 });
    expect(await screen.findByRole("heading", { name: "Klaim poin" })).toBeInTheDocument();
  });

  it("izin lokasi ditolak: verifikasi tetap jalan tanpa koordinat", async () => {
    stubGeolocation("denied");
    const api = mockApi({
      "POST /api/v1/public/verify": {
        body: { result: "GENUINE_REPEAT", product, first_verified_at: "2026-11-01T05:00:00Z", first_verified_city: "Makassar" },
      },
      ...consumerSignedOut(),
    });
    renderApp(`/v/${CODE}`);

    fireEvent.click(await screen.findByRole("button", { name: "Cek dengan lokasi" }));

    expect(await screen.findByRole("heading", { name: "Produk asli, kode ini pernah dicek" })).toBeInTheDocument();
    expect(screen.getByText("Pertama kali dicek pada 1 November 2026 di Makassar.")).toBeInTheDocument();
    expect(screen.getByText(/pastikan segel kemasan masih utuh/)).toBeInTheDocument();
    expect(screen.getByText(/Lokasi tidak dipakai karena izinnya tidak diberikan/)).toBeInTheDocument();
    expect(api.find("POST", "/api/v1/public/verify")[0]?.body).toEqual({ code: CODE });
  });

  it("SUSPICIOUS menjelaskan langkah dan tidak menawarkan klaim", async () => {
    mockApi({ "POST /api/v1/public/verify": { body: { result: "SUSPICIOUS" } } });
    renderApp(`/v/${CODE}`);

    fireEvent.click(await screen.findByRole("button", { name: "Cek tanpa lokasi" }));

    expect(await screen.findByRole("heading", { name: "Produk ini perlu dicek lebih lanjut" })).toBeInTheDocument();
    expect(screen.getByText("Jangan dikonsumsi dulu.")).toBeInTheDocument();
    expect(screen.getByText("Hubungi layanan pelanggan mGanik dan sebutkan kode di hologram.")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Klaim poin" })).not.toBeInTheDocument();
  });

  it("NOT_FOUND untuk kode berhuruf unicode", async () => {
    const api = mockApi({ "POST /api/v1/public/verify": { body: { result: "NOT_FOUND" } } });
    renderApp("/v/%E2%9C%93-%C3%A9");

    expect(await screen.findByText("✓-é")).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Cek tanpa lokasi" }));

    expect(await screen.findByRole("heading", { name: "Kode tidak terdaftar" })).toBeInTheDocument();
    expect(api.find("POST", "/api/v1/public/verify")[0]?.body).toEqual({ code: "✓-é" });
  });

  it("RECALLED menyebut lot dan apa yang harus dilakukan", async () => {
    mockApi({ "POST /api/v1/public/verify": { body: { result: "RECALLED", product } } });
    renderApp(`/v/${CODE}`);

    fireEvent.click(await screen.findByRole("button", { name: "Cek tanpa lokasi" }));

    expect(await screen.findByRole("heading", { name: "Produk ini sedang ditarik" })).toBeInTheDocument();
    expect(screen.getByText("mGanik menarik lot L2410A dari peredaran.")).toBeInTheDocument();
    expect(screen.getByText("Jangan dikonsumsi.")).toBeInTheDocument();
  });

  it("tidak pernah menampilkan status internal serial", async () => {
    mockApi({ "POST /api/v1/public/verify": { body: { result: "GENUINE_FIRST", product } }, ...consumerSignedOut() });
    renderApp(`/v/${CODE}`);

    fireEvent.click(await screen.findByRole("button", { name: "Cek tanpa lokasi" }));
    await screen.findByRole("heading", { name: "Produk asli" });

    expect(document.body.textContent).not.toMatch(/GENUINE|AT_PARTNER|SOLD|IN_STOCK|QUARANTINED/);
  });

  it("429 menampilkan pesan server dan bisa dicoba lagi", async () => {
    let calls = 0;
    mockApi({
      "POST /api/v1/public/verify": () => {
        calls += 1;
        return calls === 1
          ? problem(429, "rate_limited", "Terlalu banyak pengecekan dari jaringan ini. Coba lagi dalam 1 menit.")
          : { body: { result: "GENUINE_FIRST", product } };
      },
      ...consumerSignedOut(),
    });
    renderApp(`/v/${CODE}`);

    fireEvent.click(await screen.findByRole("button", { name: "Cek tanpa lokasi" }));
    expect(await screen.findByText("Terlalu banyak pengecekan dari jaringan ini. Coba lagi dalam 1 menit.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Coba cek lagi" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Produk asli" })).toBeInTheDocument());
  });
});
