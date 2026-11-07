import type { RouteObject } from "react-router";

// Halaman publik konsumen dari QR hologram. Path relatif terhadap /v.
// Dimuat terpisah supaya ponsel konsumen tidak mengunduh kode console.
export const verifyRoutes: RouteObject[] = [
  { path: ":code", lazy: async () => ({ Component: (await import("./verify-page")).VerifyPage }) },
];
