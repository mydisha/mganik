import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { emptyPage, emptyRunout, summaryFixture } from "@/features/console/test-fixtures";
import { meFixture, mockApi, problem, renderApp } from "@/test/render-app";

const homeApi = {
  "GET /api/v1/cc/summary": { body: summaryFixture() },
  "GET /api/v1/cc/runout": { body: emptyRunout },
  "GET /api/v1/cc/anomalies": { body: emptyPage },
};

describe("penjaga sesi console", () => {
  it("menampilkan kerangka shell selama profil dimuat", async () => {
    mockApi({ "GET /api/v1/auth/me": { body: meFixture() }, ...homeApi });
    renderApp("/console");

    expect(await screen.findByText("Memuat Command Center…")).toBeInTheDocument();
    expect(await screen.findByRole("heading", { level: 1, name: "Beranda" })).toBeInTheDocument();
  });

  it("401 session_expired mengirim ke /login dengan pesan", async () => {
    mockApi({ "GET /api/v1/auth/me": problem(401, "session_expired", "Sesi berakhir.") });
    const { router } = renderApp("/console/stok");

    expect(await screen.findByText("Sesi Anda sudah berakhir. Masuk lagi untuk melanjutkan.")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/login");
    expect(router.state.location.search).toBe("?reason=session_expired");
  });

  it("401 lain mengirim ke /login tanpa pesan sesi berakhir", async () => {
    mockApi({ "GET /api/v1/auth/me": problem(401, "unauthenticated") });
    const { router } = renderApp("/console");

    await waitFor(() => expect(router.state.location.pathname).toBe("/login"));
    expect(router.state.location.search).toBe("");
    expect(screen.queryByText(/Sesi Anda sudah berakhir/)).not.toBeInTheDocument();
  });

  it("401 session_expired dari request halaman juga mengirim ke /login", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/stock": problem(401, "session_expired", "Sesi berakhir."),
    });
    const { router } = renderApp("/console/stok");

    await waitFor(() => expect(router.state.location.pathname).toBe("/login"));
    expect(router.state.location.search).toBe("?reason=session_expired");
  });

  it("403 dari API menampilkan ForbiddenState di tempat konten", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "GET /api/v1/cc/stock": problem(403, "forbidden", "Tidak boleh."),
    });
    const { router } = renderApp("/console/stok");

    expect(await screen.findByRole("heading", { name: "Anda tidak punya akses ke halaman ini" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Stok" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/console/stok");
  });

  it("peran tanpa permission tidak melihat menunya dan halamannya forbidden", async () => {
    const api = mockApi({ "GET /api/v1/auth/me": { body: meFixture(["cc.read"]) } });
    renderApp("/console/perangkat");

    expect(await screen.findByRole("heading", { name: "Anda tidak punya akses ke halaman ini" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Navigasi Command Center" });
    expect(within(nav).queryByRole("link", { name: "Perangkat kasir" })).not.toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Stok" })).toBeInTheDocument();
    expect(api.find("GET", "/api/v1/pos/devices")).toHaveLength(0);
  });

  it("profil gagal dimuat karena jaringan menampilkan coba lagi", async () => {
    mockApi({});
    globalThis.fetch = () => Promise.reject(new TypeError("Failed to fetch"));
    renderApp("/console");

    expect(await screen.findByText(/Tidak bisa terhubung ke server/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Coba lagi" })).toBeInTheDocument();
  });

  it("keluar memanggil logout dengan Content-Type JSON lalu ke /login", async () => {
    let signedIn = true;
    const api = mockApi({
      "GET /api/v1/auth/me": () => (signedIn ? { body: meFixture() } : problem(401, "unauthenticated")),
      "POST /api/v1/auth/logout": () => {
        signedIn = false;
        return { status: 204 };
      },
      ...homeApi,
    });
    const { router } = renderApp("/console");

    const trigger = await screen.findByRole("button", { name: "Menu akun: Sari Ops" });
    fireEvent.keyDown(trigger, { key: "Enter" });
    expect(await screen.findByText("ops@mganik.id")).toBeInTheDocument();
    expect(screen.getByText("Manajer operasional")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitem", { name: "Keluar" }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/login"));
    const call = api.find("POST", "/api/v1/auth/logout")[0];
    expect(call?.headers.get("Content-Type")).toBe("application/json");
  });

  it("logout gagal tetap di console dan memberi tahu", async () => {
    mockApi({
      "GET /api/v1/auth/me": { body: meFixture() },
      "POST /api/v1/auth/logout": problem(403, "csrf_rejected", "Permintaan ditolak karena asal tidak dikenal."),
      ...homeApi,
    });
    const { router } = renderApp("/console");

    fireEvent.keyDown(await screen.findByRole("button", { name: "Menu akun: Sari Ops" }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: "Keluar" }));

    expect(await screen.findByText("Permintaan ditolak karena asal tidak dikenal.")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/console");
  });
});
