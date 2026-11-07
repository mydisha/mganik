/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";

// Dev server mem-proxy path API ke backend Go. Default mengikuti port API
// di compose (8089); `make api` menjalankan backend lokal di port yang sama.
export default defineConfig(({ mode }) => {
  const env = { ...loadEnv(mode, process.cwd(), "VITE_"), ...process.env };
  const target = env.VITE_API_TARGET || "http://localhost:8089";
  const proxy = Object.fromEntries(
    ["/api", "/healthz", "/readyz"].map((prefix) => [prefix, { target, changeOrigin: true }]),
  );

  return {
    plugins: [react(), tailwindcss()],
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
