import type { ComponentType } from "react";
import type { RouteObject } from "react-router";
import { perm } from "./nav";
import { RequirePermission } from "./require-permission";
import { consoleStrings as t } from "./strings";

// Halaman dimuat saat dibuka (code splitting) supaya halaman verifikasi
// konsumen di ponsel tidak ikut mengunduh Command Center.
function page(permission: string, title: string, load: () => Promise<ComponentType>): Pick<RouteObject, "lazy"> {
  return {
    lazy: async () => {
      const Page = await load();
      return {
        element: (
          <RequirePermission permission={permission} title={title}>
            <Page />
          </RequirePermission>
        ),
      };
    },
  };
}

// Route Command Center (staff). Dipasang di bawah AppShell oleh app/routes.tsx,
// yang sudah memastikan pengguna login. Path relatif terhadap /console.
export const consoleRoutes: RouteObject[] = [
  { index: true, ...page(perm.ccRead, t.home.title, async () => (await import("./home-page")).HomePage) },
  { path: "stok", ...page(perm.ccRead, t.stock.title, async () => (await import("./stock-page")).StockPage) },
  { path: "runout", ...page(perm.ccRead, t.runout.title, async () => (await import("./runout-page")).RunoutPage) },
  { path: "anomali", ...page(perm.ccRead, t.anomalies.title, async () => (await import("./anomalies-page")).AnomaliesPage) },
  { path: "konflik", ...page(perm.ccRead, t.conflicts.title, async () => (await import("./conflicts-page")).ConflictsPage) },
  { path: "distribusi", ...page(perm.ccRead, t.leakage.title, async () => (await import("./leakage-page")).LeakagePage) },
  { path: "serial", ...page(perm.serialTrace, t.trace.title, async () => (await import("./trace-page")).TracePage) },
  { path: "perangkat", ...page(perm.deviceManage, t.devices.title, async () => (await import("./devices-page")).DevicesPage) },
];
