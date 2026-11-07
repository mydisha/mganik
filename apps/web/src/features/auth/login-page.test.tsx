import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { meFixture, mockApi, problem, renderApp } from "@/test/render-app";

const emptySummary = {
  generated_at: "2026-11-07T03:00:00Z",
  runout: {
    window_days: 28,
    critical_items: 0,
    critical_locations: 0,
    warning_items: 0,
    warning_locations: 0,
    insufficient_data_items: 0,
    earliest_runout_date: null,
  },
  stock: { out_of_stock_items: 0, out_of_stock_locations: 0 },
  anomalies: { open_total: 0, open_by_severity: { LOW: 0, MEDIUM: 0, HIGH: 0, CRITICAL: 0 }, top_party: null },
  conflicts: { open_total: 0 },
  stores: { total: 2, offline: 0 },
};

function fillAndSubmit(email: string, password: string) {
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: email } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: password } });
  fireEvent.click(screen.getByRole("button", { name: "Masuk" }));
}

describe("halaman login", () => {
  it("masuk dengan password unicode lalu membuka Command Center", async () => {
    let signedIn = false;
    const api = mockApi({
      "GET /api/v1/auth/me": () => (signedIn ? { body: meFixture() } : problem(401, "unauthenticated", "Silakan masuk.")),
      "POST /api/v1/auth/login": () => {
        signedIn = true;
        return { body: meFixture() };
      },
      "GET /api/v1/cc/summary": { body: emptySummary },
      "GET /api/v1/cc/runout": { body: { items: [], next_cursor: null, window_days: 28, generated_at: "2026-11-07T03:00:00Z" } },
      "GET /api/v1/cc/anomalies": { body: { items: [], next_cursor: null } },
    });
    const { router } = renderApp("/login");
    await screen.findByLabelText("Email");

    fillAndSubmit("  ops@mganik.id ", "kata sandi ñ 密码 ");

    expect(await screen.findByRole("heading", { level: 1, name: "Beranda" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/console");
    // Email dipangkas; password dikirim apa adanya.
    expect(api.find("POST", "/api/v1/auth/login")[0]?.body).toEqual({ email: "ops@mganik.id", password: "kata sandi ñ 密码 " });
  });

  it("menampilkan pesan generik dari server saat gagal", async () => {
    mockApi({
      "GET /api/v1/auth/me": problem(401, "unauthenticated"),
      "POST /api/v1/auth/login": problem(401, "invalid_credentials", "Email atau password salah."),
    });
    renderApp("/login");
    await screen.findByLabelText("Email");

    fillAndSubmit("ops@mganik.id", "salah");

    expect(await screen.findByRole("alert")).toHaveTextContent("Email atau password salah.");
  });

  it("429 menampilkan menit tunggu dari server", async () => {
    mockApi({
      "GET /api/v1/auth/me": problem(401, "unauthenticated"),
      "POST /api/v1/auth/login": { ...problem(429, "too_many_attempts", "Terlalu banyak percobaan. Coba lagi dalam 12 menit."), headers: { "Retry-After": "700" } },
    });
    renderApp("/login");
    await screen.findByLabelText("Email");

    fillAndSubmit("ops@mganik.id", "rahasia");

    expect(await screen.findByRole("alert")).toHaveTextContent("Coba lagi dalam 12 menit.");
  });

  it("429 tanpa detail menghitung menit dari Retry-After", async () => {
    mockApi({
      "GET /api/v1/auth/me": problem(401, "unauthenticated"),
      "POST /api/v1/auth/login": { ...problem(429, "too_many_attempts"), headers: { "Retry-After": "290" } },
    });
    renderApp("/login");
    await screen.findByLabelText("Email");

    fillAndSubmit("ops@mganik.id", "rahasia");

    expect(await screen.findByRole("alert")).toHaveTextContent("Terlalu banyak percobaan masuk. Coba lagi dalam 5 menit.");
  });

  it("503 server_busy menampilkan detail dari server", async () => {
    mockApi({
      "GET /api/v1/auth/me": problem(401, "unauthenticated"),
      "POST /api/v1/auth/login": problem(503, "server_busy", "Server sedang sibuk. Coba lagi sebentar lagi."),
    });
    renderApp("/login");
    await screen.findByLabelText("Email");

    fillAndSubmit("ops@mganik.id", "rahasia");

    expect(await screen.findByRole("alert")).toHaveTextContent("Server sedang sibuk.");
  });

  it("memvalidasi isian sebelum mengirim", async () => {
    const api = mockApi({ "GET /api/v1/auth/me": problem(401, "unauthenticated") });
    renderApp("/login");
    await screen.findByLabelText("Email");

    fillAndSubmit("bukan-email", "");

    expect(await screen.findByText("Format email belum benar, contoh: nama@mganik.id.")).toBeInTheDocument();
    expect(screen.getByText("Isi password Anda.")).toBeInTheDocument();
    expect(screen.getByLabelText("Email")).toHaveAttribute("aria-invalid", "true");
    expect(api.find("POST", "/api/v1/auth/login")).toHaveLength(0);
  });

  it("kegagalan jaringan memakai pesan frontend", async () => {
    mockApi({ "GET /api/v1/auth/me": problem(401, "unauthenticated") });
    renderApp("/login");
    await screen.findByLabelText("Email");
    globalThis.fetch = () => Promise.reject(new TypeError("Failed to fetch"));

    fillAndSubmit("ops@mganik.id", "rahasia");

    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Tidak bisa terhubung ke server."));
  });

  it("menjelaskan sesi yang berakhir", async () => {
    mockApi({ "GET /api/v1/auth/me": problem(401, "unauthenticated") });
    renderApp("/login?reason=session_expired");

    expect(await screen.findByText("Sesi Anda sudah berakhir. Masuk lagi untuk melanjutkan.")).toBeInTheDocument();
  });

  it("pengguna yang sudah login langsung ke console", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/summary": { body: emptySummary },
      "GET /api/v1/cc/runout": { body: { items: [], next_cursor: null, window_days: 28, generated_at: "2026-11-07T03:00:00Z" } },
      "GET /api/v1/cc/anomalies": { body: { items: [], next_cursor: null } },
    });
    const { router } = renderApp("/login");

    await waitFor(() => expect(router.state.location.pathname).toBe("/console"));
  });
});
