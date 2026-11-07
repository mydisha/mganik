import { render } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { vi } from "vitest";
import { AppProviders, createQueryClient } from "@/app/providers";
import { AppShell } from "@/app/app-shell";
import type { Me } from "@/features/auth/api";
import { authRoutes } from "@/features/auth/routes";
import { consoleRoutes } from "@/features/console/routes";
import { verifyRoutes } from "@/features/verify/routes";

// Helper test untuk halaman auth, Command Center, dan verifikasi: route asli,
// QueryClient asli (termasuk pengalihan sesi), dan fetch tiruan per endpoint.

export interface MockResponse {
  status?: number;
  body?: unknown;
  headers?: Record<string, string>;
}

export type MockHandler = MockResponse | ((call: ApiCall) => MockResponse);

export interface ApiCall {
  method: string;
  path: string;
  query: URLSearchParams;
  body: unknown;
  headers: Headers;
}

export function problem(status: number, code: string, detail?: string) {
  return { status, body: { type: "about:blank", title: code, status, code, ...(detail ? { detail } : {}) } };
}

/** Mengganti fetch: kunci "METHOD /path". Endpoint yang tidak dikenal menjawab 501. */
export function mockApi(routes: Record<string, MockHandler>) {
  const calls: ApiCall[] = [];
  const fetchMock = vi.fn(async (request: Request) => {
    const url = new URL(request.url);
    const text = await request.clone().text();
    const call: ApiCall = {
      method: request.method,
      path: url.pathname,
      query: url.searchParams,
      body: text ? JSON.parse(text) : undefined,
      headers: request.headers,
    };
    calls.push(call);
    const handler = routes[`${request.method} ${url.pathname}`];
    const res = typeof handler === "function" ? handler(call) : (handler ?? problem(501, "not_mocked", url.pathname));
    const status = res.status ?? 200;
    const isProblem = status >= 400;
    return new Response(status === 204 || res.body === undefined ? null : JSON.stringify(res.body), {
      status,
      headers: { "Content-Type": isProblem ? "application/problem+json" : "application/json", ...res.headers },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return { calls, fetchMock, find: (method: string, path: string) => calls.filter((c) => c.method === method && c.path === path) };
}

export const ALL_PERMISSIONS = ["cc.read", "anomaly.resolve", "conflict.resolve", "serial.trace", "pos.device.manage"];

export function meFixture(permissions: string[] = ALL_PERMISSIONS): Me {
  return {
    user: { id: "0193a000-0000-7000-8000-000000000001", email: "ops@mganik.id", name: "Sari Ops" },
    roles: [{ code: "OPS_MANAGER", name: "Manajer operasional", scope_mode: "ALL", locations: [] }],
    permissions,
    scope: { mode: "ALL", locations: [] },
  };
}

/** Lebar ponsel (< 768 px): tabel tampil sebagai kartu. */
export function stubMobile(mobile: boolean) {
  vi.stubGlobal(
    "matchMedia",
    (query: string) =>
      ({
        matches: query.includes("max-width") ? mobile : false,
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
      }) as unknown as MediaQueryList,
  );
}

// jsdom tidak punya matchMedia (dipakai sonner dan tabel/kartu): default desktop, tema terang.
if (typeof window.matchMedia !== "function") {
  window.matchMedia = (query: string) =>
    ({ matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} }) as unknown as MediaQueryList;
}

// Radix Popper (menu akun) memakai ResizeObserver, yang tidak ada di jsdom.
if (!("ResizeObserver" in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

export function renderApp(path: string) {
  const router = createMemoryRouter(
    [
      ...authRoutes,
      { path: "/console", element: <AppShell />, children: consoleRoutes },
      { path: "/v", children: verifyRoutes },
    ],
    { initialEntries: [path] },
  );
  const client = createQueryClient(router);
  client.setDefaultOptions({ queries: { ...client.getDefaultOptions().queries, retry: false } });
  const view = render(
    <AppProviders client={client}>
      <RouterProvider router={router} />
    </AppProviders>,
  );
  return { ...view, router, client };
}
