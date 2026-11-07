import { useEffect, useState } from "react";

// Service worker (dist/sw.js, dibuat vite-plugin-pwa mode generateSW) hanya
// didaftarkan dari layar kasir: perangkat toko butuh app shell /pos yang
// terbuka tanpa internet; halaman lain tidak. Versi baru tidak dipasang
// diam-diam di tengah transaksi: SW baru menunggu (registerType "prompt"),
// kasir memilih kapan memuat ulang, lalu SW diminta SKIP_WAITING.
// Didaftarkan manual (tanpa virtual:pwa-register) karena workbox-window tidak
// menjadi dependency app ini.

const SW_URL = "/sw.js";
let waiting: ServiceWorker | null = null;
let started = false;
const listeners = new Set<() => void>();

function announce(sw: ServiceWorker) {
  waiting = sw;
  listeners.forEach((l) => l());
}

function ensureRegistered() {
  if (started || !import.meta.env.PROD || typeof navigator === "undefined" || !("serviceWorker" in navigator)) return;
  started = true;
  void navigator.serviceWorker
    .register(SW_URL, { scope: "/" })
    .then((reg) => {
      // SW baru yang sudah terpasang tetapi menunggu, hanya kalau sudah ada
      // SW lama yang mengontrol halaman (pemasangan pertama tidak perlu prompt).
      if (reg.waiting && navigator.serviceWorker.controller) announce(reg.waiting);
      reg.addEventListener("updatefound", () => {
        const sw = reg.installing;
        sw?.addEventListener("statechange", () => {
          if (sw.state === "installed" && navigator.serviceWorker.controller) announce(sw);
        });
      });
    })
    .catch(() => undefined);
  let reloading = false;
  navigator.serviceWorker.addEventListener("controllerchange", () => {
    if (reloading || !waiting) return;
    reloading = true;
    window.location.reload();
  });
}

export function usePwaUpdate(): (() => void) | null {
  const [ready, setReady] = useState(() => waiting !== null);
  useEffect(() => {
    ensureRegistered();
    const l = () => setReady(true);
    listeners.add(l);
    return () => {
      listeners.delete(l);
    };
  }, []);
  return ready ? () => waiting?.postMessage({ type: "SKIP_WAITING" }) : null;
}
