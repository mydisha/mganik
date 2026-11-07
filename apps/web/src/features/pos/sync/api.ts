import type { components } from "@/shared/api/schema";
import { isProblem } from "@/shared/api/problem";
import type { PosBootstrap } from "../db";

// Klien HTTP route device POS. Sengaja memakai fetch langsung (bukan
// openapi-fetch) supaya hasilnya dipetakan ke satu union yang mudah diuji:
// sync engine memutuskan retry/berhenti dari `kind`, dan pesan untuk orang
// selalu `detail` dari server (problem+json), bukan teks karangan klien.

export type PosSerialPage = components["schemas"]["PosSerialPage"];
export type PosHeartbeatRequest = components["schemas"]["PosHeartbeatRequest"];
export type PosHeartbeatResponse = components["schemas"]["PosHeartbeatResponse"];
export type PosSyncRequest = components["schemas"]["PosSyncRequest"];
export type PosSyncResponse = components["schemas"]["PosSyncResponse"];
export type PosSyncResult = components["schemas"]["PosSyncResult"];

export type ApiOutcome<T> =
  | { kind: "ok"; data: T }
  /** 401: token device dicabut atau tidak dikenal. */
  | { kind: "unauthorized"; message: string | null }
  /** 403: device/kasir tidak diizinkan di lokasi ini. */
  | { kind: "forbidden"; message: string | null }
  /** 413: batch terlalu besar. */
  | { kind: "too_large"; message: string | null }
  /** 400/422: struktur request ditolak; mengirim ulang yang sama tidak membantu. */
  | { kind: "invalid"; message: string | null }
  /** 409, 429, 5xx, atau respons tak terduga: coba lagi nanti. */
  | { kind: "retry"; status: number; message: string | null }
  /** Server tidak terjangkau atau waktu habis. */
  | { kind: "network" };

export interface PosApiConfig {
  baseUrl: string;
  token: string;
  fetch: typeof fetch;
  timeoutMs?: number;
}

const DEFAULT_TIMEOUT_MS = 20_000;

async function withTimeout(
  fetchFn: typeof fetch,
  url: string,
  init: RequestInit,
  timeoutMs: number,
): Promise<Response> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    return await fetchFn(url, { ...init, signal: ctrl.signal });
  } finally {
    clearTimeout(timer);
  }
}

async function readProblem(res: Response): Promise<{ message: string | null; code: string | null }> {
  try {
    const body: unknown = await res.json();
    if (isProblem(body)) return { message: body.detail ?? body.title ?? null, code: body.code };
  } catch {
    // Badan bukan JSON (mis. halaman error proxy).
  }
  return { message: null, code: null };
}

async function request<T>(cfg: PosApiConfig, method: "GET" | "POST", path: string, body?: unknown): Promise<ApiOutcome<T>> {
  let res: Response;
  try {
    res = await withTimeout(
      cfg.fetch,
      cfg.baseUrl + path,
      {
        method,
        // Device memakai Bearer, bukan cookie staff yang kebetulan ada di browser ini.
        credentials: "omit",
        cache: "no-store",
        headers: {
          Accept: "application/json",
          Authorization: `Bearer ${cfg.token}`,
          ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
        },
        body: body !== undefined ? JSON.stringify(body) : undefined,
      },
      cfg.timeoutMs ?? DEFAULT_TIMEOUT_MS,
    );
  } catch {
    return { kind: "network" };
  }
  if (res.ok) {
    try {
      return { kind: "ok", data: (await res.json()) as T };
    } catch {
      return { kind: "retry", status: res.status, message: null };
    }
  }
  const { message, code } = await readProblem(res);
  // 422 batch_too_large (lebih dari sync_max_batch) diperlakukan seperti 413.
  if (code === "batch_too_large") return { kind: "too_large", message };
  switch (res.status) {
    case 401:
      return { kind: "unauthorized", message };
    case 403:
      return { kind: "forbidden", message };
    case 413:
      return { kind: "too_large", message };
    case 400:
    case 422:
      return { kind: "invalid", message };
    default:
      return { kind: "retry", status: res.status, message };
  }
}

export function getBootstrap(cfg: PosApiConfig): Promise<ApiOutcome<PosBootstrap>> {
  return request(cfg, "GET", "/api/v1/pos/bootstrap");
}

export function getSerialPage(cfg: PosApiConfig, cursor: string): Promise<ApiOutcome<PosSerialPage>> {
  return request(cfg, "GET", `/api/v1/pos/bootstrap/serials?cursor=${encodeURIComponent(cursor)}`);
}

export function postHeartbeat(cfg: PosApiConfig, body: PosHeartbeatRequest): Promise<ApiOutcome<PosHeartbeatResponse>> {
  return request(cfg, "POST", "/api/v1/pos/heartbeat", body);
}

export function postSync(cfg: PosApiConfig, body: PosSyncRequest): Promise<ApiOutcome<PosSyncResponse>> {
  return request(cfg, "POST", "/api/v1/pos/sync", body);
}

// ping: status online ditentukan dari /healthz yang benar-benar menjawab, bukan
// navigator.onLine (yang tetap true di Wi-Fi tanpa internet atau captive portal).
export async function ping(baseUrl: string, fetchFn: typeof fetch, timeoutMs = 5_000): Promise<boolean> {
  try {
    const res = await withTimeout(fetchFn, `${baseUrl}/healthz`, { method: "GET", cache: "no-store", credentials: "omit" }, timeoutMs);
    return res.ok;
  } catch {
    return false;
  }
}

// fetchBootstrapAll mengambil bootstrap lalu semua halaman serial.
export async function fetchBootstrapAll(
  cfg: PosApiConfig,
): Promise<ApiOutcome<{ bootstrap: PosBootstrap; serials: PosSerialPage["items"] }>> {
  const first = await getBootstrap(cfg);
  if (first.kind !== "ok") return first;
  const serials = [...first.data.serials.items];
  let cursor = first.data.serials.next_cursor;
  // Batas pengaman terhadap cursor yang tidak pernah habis.
  for (let page = 0; cursor && page < 10_000; page++) {
    const next = await getSerialPage(cfg, cursor);
    if (next.kind !== "ok") return next;
    serials.push(...next.data.items);
    cursor = next.data.next_cursor;
  }
  return { kind: "ok", data: { bootstrap: first.data, serials } };
}
