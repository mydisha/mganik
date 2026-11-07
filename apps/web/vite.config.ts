/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";
import { VitePWA } from "vite-plugin-pwa";

// Dev server mem-proxy path API ke backend Go. Default mengikuti port API
// di compose (8089); `make api` menjalankan backend lokal di port yang sama.
export default defineConfig(({ mode }) => {
  const env = { ...loadEnv(mode, process.cwd(), "VITE_"), ...process.env };
  const target = env.VITE_API_TARGET || "http://localhost:8089";
  const proxy = Object.fromEntries(
    ["/api", "/healthz", "/readyz"].map((prefix) => [prefix, { target, changeOrigin: true }]),
  );

  return {
    plugins: [
      react(),
      tailwindcss(),
      // PWA untuk kasir offline: app shell (HTML, JS, CSS, font, WASM) di-precache
      // supaya /pos terbuka tanpa internet. Navigasi hanya /pos yang dilayani dari
      // cache; /api, /healthz, dan /readyz tidak pernah di-cache (tanpa
      // runtimeCaching) karena status online dan data transaksi harus dari server.
      // Service worker didaftarkan dari features/pos/pwa.ts, bukan di semua halaman.
      VitePWA({
        injectRegister: false,
        registerType: "prompt",
        manifest: {
          name: "mGanik Kasir",
          short_name: "Kasir",
          description: "Kasir toko mGanik yang tetap berjalan saat offline.",
          lang: "id",
          start_url: "/pos",
          scope: "/",
          display: "standalone",
          orientation: "landscape",
          // Token --background (terang) dan --primary dari app/styles.css.
          background_color: "#f9fafb",
          theme_color: "#1f2635",
          icons: [],
        },
        workbox: {
          globPatterns: ["**/*.{js,css,html,woff2,wasm}"],
          navigateFallback: "/index.html",
          navigateFallbackAllowlist: [/^\/pos(?:\/|$)/],
          navigateFallbackDenylist: [/^\/api\//, /^\/healthz/, /^\/readyz/],
          cleanupOutdatedCaches: true,
          runtimeCaching: [],
        },
        devOptions: { enabled: false },
      }),
    ],
    resolve: {
      alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
    },
    server: { port: 5173, proxy },
    test: {
      environment: "jsdom",
      globals: false,
      setupFiles: ["./src/test/setup.ts"],
      include: ["src/**/*.test.{ts,tsx}"],
      css: false,
      restoreMocks: true,
      unstubGlobals: true,
    },
  };
});
