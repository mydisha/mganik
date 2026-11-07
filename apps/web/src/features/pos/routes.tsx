import type { RouteObject } from "react-router";

// Route kasir. Berdiri sendiri (tanpa AppShell console): layar penuh untuk
// perangkat toko. Path relatif terhadap /pos. Kode kasir (Dexie, argon2, sync)
// dimuat terpisah supaya tidak membebani console dan halaman verifikasi.
export const posRoutes: RouteObject[] = [
  {
    lazy: async () => ({ Component: (await import("./pos-root")).PosRoot }),
    children: [
      { index: true, lazy: async () => ({ Component: (await import("./pos-home")).PosHome }) },
      { path: "setup", lazy: async () => ({ Component: (await import("./setup-screen")).SetupScreen }) },
    ],
  },
];
