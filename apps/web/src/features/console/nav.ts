import { consoleStrings as t } from "./strings";

// Permission dari GET /auth/me yang dipakai UI untuk menyembunyikan halaman
// dan aksi. Server tetap memeriksa setiap request.
export const perm = {
  ccRead: "cc.read",
  anomalyResolve: "anomaly.resolve",
  conflictResolve: "conflict.resolve",
  serialTrace: "serial.trace",
  deviceManage: "pos.device.manage",
} as const;

export interface NavItem {
  to: string;
  label: string;
  permission: string;
  end?: boolean;
}

export const consoleNav: NavItem[] = [
  { to: "/console", label: t.nav.home, permission: perm.ccRead, end: true },
  { to: "/console/stok", label: t.nav.stock, permission: perm.ccRead },
  { to: "/console/runout", label: t.nav.runout, permission: perm.ccRead },
  { to: "/console/anomali", label: t.nav.anomalies, permission: perm.ccRead },
  { to: "/console/konflik", label: t.nav.conflicts, permission: perm.ccRead },
  { to: "/console/distribusi", label: t.nav.leakage, permission: perm.ccRead },
  { to: "/console/serial", label: t.nav.trace, permission: perm.serialTrace },
  { to: "/console/perangkat", label: t.nav.devices, permission: perm.deviceManage },
];
