// @vitest-environment node
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { PosDb, type PosTransaction } from "../db";
import { recordSale, type SaleInput } from "../sale";
import { CASHIER_ID, makeBootstrap, PRODUCT_SERUM, seedDevice, TEST_TOKEN } from "../test-fixtures";
import type { PosSyncResult } from "./api";
import { backoffDelay, BACKOFF_MAX_MS, SyncEngine, type LockLike } from "./engine";

interface Call {
  path: string;
  method: string;
  body: unknown;
  auth: string | null;
}

type Reply = { status: number; body?: unknown } | "network";
type Handler = (call: Call) => Reply | Promise<Reply>;

const okHeartbeat = (pending: number) => ({
  status: 200,
  body: {
    server_time: "2026-11-07T03:00:05.000Z",
    location_sync_state: pending === 0 ? "ONLINE" : "OFFLINE",
    last_acked_seq: 0,
    heartbeat_interval_seconds: 30,
    heartbeat_timeout_seconds: 120,
    clock_skew_ms: 0,
    online_blocked_reason: pending === 0 ? null : "pending_outbox",
  },
});

type ResultFor = (txn: PosTransaction, index: number) => Pick<PosSyncResult, "status" | "result" | "detail">;

function syncReply(body: unknown, resultFor: ResultFor = () => ({ status: "ACCEPTED", result: "ACCEPTED", detail: {} })) {
  const txns = (body as { transactions: PosTransaction[] }).transactions;
  const results = txns.map((t, index) => ({ index, client_txn_id: t.client_txn_id, device_seq: t.device_seq, ...resultFor(t, index) }));
  const stored = results.filter((r) => r.status !== "RETRY").map((r) => r.device_seq);
  return {
    status: 200,
    body: {
      server_time: "2026-11-07T03:00:04.000Z",
      ack_up_to_seq: stored.length ? Math.max(...stored) : 0,
      missing_seqs: [],
      missing_seqs_truncated: false,
      results,
    },
  };
}

function fakeServer(override: Partial<Record<"sync" | "heartbeat" | "healthz" | "bootstrap", Handler>> = {}) {
  const calls: Call[] = [];
  const handler: Handler = (call) => {
    if (call.path === "/healthz") return override.healthz?.(call) ?? { status: 200, body: { status: "ok" } };
    if (call.path === "/api/v1/pos/sync") return override.sync?.(call) ?? syncReply(call.body);
    if (call.path === "/api/v1/pos/heartbeat")
      return override.heartbeat?.(call) ?? okHeartbeat((call.body as { pending_count: number }).pending_count);
    if (call.path === "/api/v1/pos/bootstrap") return override.bootstrap?.(call) ?? { status: 200, body: makeBootstrap() };
    return { status: 404, body: { type: "about:blank", title: "Not found", status: 404, code: "not_found" } };
  };
  const fetchFn = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    const headers = new Headers(init?.headers);
    const call: Call = {
      path: url.pathname,
      method: init?.method ?? "GET",
      body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
      auth: headers.get("Authorization"),
    };
    calls.push(call);
    const reply = await handler(call);
    if (reply === "network") throw new TypeError("Failed to fetch");
    return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), {
      status: reply.status,
      headers: { "Content-Type": reply.status >= 400 ? "application/problem+json" : "application/json" },
    });
  }) as typeof fetch;
  return { fetch: fetchFn, calls, paths: () => calls.map((c) => c.path) };
}

const problem = (status: number, code: string, detail: string) => ({
  status,
  body: { type: "about:blank", title: code, status, code, detail },
});

let db: PosDb;
let dbName: string;
let n = 0;

beforeEach(async () => {
  dbName = `engine-test-${++n}-${Math.random()}`;
  db = new PosDb(dbName);
  await seedDevice(db);
});

afterEach(() => db.close());

const sale = (): SaleInput => ({
  lines: [{ productId: PRODUCT_SERUM, name: "Serum", sku: "SRM-01", qty: 1, unitPrice: 150_000, discount: 0, serials: [] }],
  payments: [{ method: "CASH", amount: 150_000 }],
  cashierId: CASHIER_ID,
  cashierName: "Rina",
  priceListVersion: 3,
});

function engine(server: ReturnType<typeof fakeServer>, extra: Partial<ConstructorParameters<typeof SyncEngine>[0]> = {}) {
  return new SyncEngine({ db, baseUrl: "http://pos.test", fetch: server.fetch, locks: null, random: () => 0, ...extra });
}

async function until(cond: () => boolean | Promise<boolean>, ms = 3_000) {
  const start = Date.now();
  while (!(await cond())) {
    if (Date.now() - start > ms) throw new Error("kondisi tidak tercapai");
    await new Promise((r) => setTimeout(r, 5));
  }
}

describe("sync engine: hasil per transaksi", () => {
  it("memproses setiap status, lalu heartbeat dengan pending_count yang benar", async () => {
    for (let i = 0; i < 6; i++) await recordSale(db, sale());
    const plan: Pick<PosSyncResult, "status" | "result" | "detail">[] = [
      { status: "ACCEPTED", result: "ACCEPTED", detail: { server_txn_id: "a", flags: ["PRICE_STALE"] } },
      {
        status: "ACCEPTED_WITH_CONFLICT",
        result: "ACCEPTED_WITH_CONFLICT",
        detail: { server_txn_id: "b", conflicts: [{ type: "OFFLINE_SALE_PREEMPT", product_id: PRODUCT_SERUM, qty: 1 }] },
      },
      { status: "DUPLICATE", result: "ACCEPTED", detail: { server_txn_id: "c" } },
      {
        status: "REJECTED",
        result: "REJECTED",
        detail: { server_txn_id: "d", reason: "cashier_not_allowed", message: "Kasir tidak dikenal." },
      },
      { status: "RETRY", result: "", detail: { reason: "stock_contended", message: "Coba lagi." } },
      { status: "DUPLICATE", result: "REJECTED", detail: { server_txn_id: "f", reason: "total_mismatch", message: "Total salah." } },
    ];
    const server = fakeServer({ sync: (c) => syncReply(c.body, (_t, i) => plan[i]!) });
    const e = engine(server);

    expect(await e.cycle()).toBe("retry");
    expect(server.paths()).toEqual(["/healthz", "/api/v1/pos/sync", "/api/v1/pos/heartbeat"]);
    const sent = (server.calls[1]!.body as { transactions: PosTransaction[] }).transactions;
    expect(sent.map((t) => t.device_seq)).toEqual([1, 2, 3, 4, 5, 6]);
    expect(sent.every((t) => /^[0-9a-f]{64}$/.test(t.signature))).toBe(true);
    expect(server.calls[1]!.auth).toBe(`Bearer ${TEST_TOKEN}`);

    const rows = await db.outbox.orderBy("deviceSeq").toArray();
    expect(rows.map((r) => [r.deviceSeq, r.state])).toEqual([
      [4, "rejected"],
      [5, "pending"],
      [6, "rejected"],
    ]);
    expect(rows[0]!.rejection).toMatchObject({ reason: "cashier_not_allowed", message: "Kasir tidak dikenal.", stored: true });
    expect(rows[1]!.attempts).toBe(1);
    expect(rows[2]!.rejection).toMatchObject({ reason: "total_mismatch", stored: true });

    const log = (await db.syncLog.toArray()).sort((x, y) => x.deviceSeq - y.deviceSeq);
    expect(log.map((l) => [l.deviceSeq, l.status])).toEqual([
      [1, "ACCEPTED"],
      [2, "ACCEPTED_WITH_CONFLICT"],
      [3, "DUPLICATE"],
    ]);
    expect(log[0]!.flags).toEqual(["PRICE_STALE"]);
    expect(log[1]!.conflicts[0]).toMatchObject({ type: "OFFLINE_SALE_PREEMPT" });

    // Hanya transaksi RETRY yang belum tersimpan di server.
    expect(server.calls[2]!.body).toMatchObject({ pending_count: 1 });
    const sync = await db.getMeta("sync");
    expect(sync).toMatchObject({ lastAckedSeq: 6, onlineBlockedReason: "pending_outbox", online: true, syncing: false });
  });

  it("menghitung REJECTED yang tidak disimpan server sebagai belum ter-ack", async () => {
    await recordSale(db, sale());
    await recordSale(db, sale());
    const server = fakeServer({
      sync: (c) =>
        syncReply(c.body, (_t, i) =>
          i === 0
            ? { status: "REJECTED", result: "REJECTED", detail: { reason: "invalid_signature", message: "Signature salah." } }
            : { status: "ACCEPTED", result: "ACCEPTED", detail: { server_txn_id: "x" } },
        ),
    });
    expect(await engine(server).cycle()).toBe("ok");
    expect((await db.outbox.toArray())[0]!.rejection).toMatchObject({ stored: false });
    expect(server.calls.at(-1)!.body).toMatchObject({ pending_count: 1 });
  });

  it("heartbeat dengan pending_count 0 setelah outbox habis", async () => {
    await recordSale(db, sale());
    const server = fakeServer();
    expect(await engine(server).cycle()).toBe("ok");
    expect(await db.outbox.count()).toBe(0);
    expect(server.paths()).toEqual(["/healthz", "/api/v1/pos/sync", "/api/v1/pos/heartbeat"]);
    expect(server.calls[2]!.body).toMatchObject({ pending_count: 0 });
    expect((await db.getMeta("sync"))?.locationSyncState).toBe("ONLINE");
  });

  it("mengirim batch berurutan sesuai sync_max_batch dan membelah batch saat 413", async () => {
    for (let i = 0; i < 5; i++) await recordSale(db, sale());
    await db.meta.put({ ...(await db.getMeta("limits"))!, sync_max_batch: 4 });
    const server = fakeServer({
      sync: (c) => {
        const t = (c.body as { transactions: unknown[] }).transactions;
        return t.length > 2 ? problem(413, "payload_too_large", "Batch terlalu besar.") : syncReply(c.body);
      },
    });
    expect(await engine(server).cycle()).toBe("ok");
    const sizes = server.calls
      .filter((c) => c.path === "/api/v1/pos/sync")
      .map((c) => (c.body as { transactions: PosTransaction[] }).transactions.map((t) => t.device_seq));
    expect(sizes).toEqual([[1, 2, 3, 4], [1, 2], [3, 4], [5]]);
    expect(await db.outbox.count()).toBe(0);
  });
});

describe("sync engine: kegagalan", () => {
  it("401 menghentikan sync tanpa menghapus outbox", async () => {
    await recordSale(db, sale());
    await recordSale(db, sale());
    const server = fakeServer({ sync: () => problem(401, "invalid_token", "Token perangkat tidak valid atau sudah dicabut.") });
    const e = engine(server);
    expect(await e.cycle()).toBe("revoked");
    expect(await db.outbox.count()).toBe(2);
    expect(await db.getMeta("sync")).toMatchObject({
      authRevoked: true,
      blockedMessage: "Token perangkat tidak valid atau sudah dicabut.",
    });
    expect(server.paths()).not.toContain("/api/v1/pos/heartbeat");

    // Siklus berikutnya tidak menyentuh jaringan sama sekali.
    const before = server.calls.length;
    expect(await e.cycle()).toBe("revoked");
    expect(server.calls.length).toBe(before);
    expect(await db.outbox.count()).toBe(2);
  });

  it("offline kalau /healthz tidak terjangkau, tanpa mengirim apa pun", async () => {
    await recordSale(db, sale());
    const server = fakeServer({ healthz: () => "network" });
    expect(await engine(server).cycle()).toBe("offline");
    expect(server.paths()).toEqual(["/healthz"]);
    expect((await db.getMeta("sync"))?.online).toBe(false);
    expect(await db.outbox.count()).toBe(1);
  });

  it("jaringan putus di tengah sync dianggap offline, outbox utuh", async () => {
    await recordSale(db, sale());
    const server = fakeServer({ sync: () => "network" });
    expect(await engine(server).cycle()).toBe("offline");
    expect(await db.outbox.count()).toBe(1);
  });

  it("403 menahan sync dengan pesan server", async () => {
    await recordSale(db, sale());
    const server = fakeServer({ sync: () => problem(403, "forbidden", "Device tidak diizinkan di lokasi ini.") });
    expect(await engine(server).cycle()).toBe("blocked");
    expect((await db.getMeta("sync"))?.blockedMessage).toBe("Device tidak diizinkan di lokasi ini.");
    expect(await db.outbox.count()).toBe(1);
  });

  it("422 batch_too_large dibelah seperti 413; blok tanpa problem+json memakai pesan cadangan lalu hilang setelah pulih", async () => {
    for (let i = 0; i < 3; i++) await recordSale(db, sale());
    let mode: "split" | "proxy" | "ok" = "split";
    const server = fakeServer({
      sync: (c) => {
        const t = (c.body as { transactions: unknown[] }).transactions;
        if (mode === "split" && t.length > 1) return problem(422, "batch_too_large", "Batch terlalu besar.");
        if (mode === "proxy") return { status: 403 };
        return syncReply(c.body);
      },
    });
    const e = engine(server);
    expect(await e.cycle()).toBe("ok");
    expect(await db.outbox.count()).toBe(0);

    await recordSale(db, sale());
    mode = "proxy";
    expect(await e.cycle()).toBe("blocked");
    expect((await db.getMeta("sync"))?.blockedMessage).toMatch(/tanpa keterangan/);
    // Bootstrap tetap diambil ulang saat sync tertahan.
    expect(server.paths().slice(-2)).toEqual(["/api/v1/pos/heartbeat", "/api/v1/pos/bootstrap"]);

    mode = "ok";
    expect(await e.cycle()).toBe("ok");
    expect((await db.getMeta("sync"))?.blockedMessage).toBeNull();
  });

  it("mencoba lagi dengan backoff sampai server pulih (loop berjalan)", async () => {
    await recordSale(db, sale());
    let fails = 2;
    const server = fakeServer({
      sync: (c) => (fails-- > 0 ? problem(503, "unavailable", "Server sibuk.") : syncReply(c.body)),
    });
    const e = engine(server, { backoffBaseMs: 5 });
    e.start();
    await until(async () => (await db.outbox.count()) === 0);
    await e.stop();
    expect(server.calls.filter((c) => c.path === "/api/v1/pos/sync").length).toBe(3);
  });

  it("start() saat stop() belum selesai tetap menjalankan runner (efek React dipasang ulang)", async () => {
    const server = fakeServer();
    const e = engine(server);
    e.start();
    void e.stop();
    e.start();
    await recordSale(db, sale());
    e.kick();
    await until(async () => (await db.outbox.count()) === 0);
    expect(e.isRunning).toBe(true);
    await e.stop();
    expect(e.isRunning).toBe(false);
  });

  it("backoff eksponensial dengan jitter dan batas atas", () => {
    expect(backoffDelay(1, 0)).toBe(1_000);
    expect(backoffDelay(1, 1)).toBe(2_000);
    expect(backoffDelay(3, 0.5)).toBe(6_000);
    for (const r of [0, 0.3, 1]) {
      const d = backoffDelay(30, r);
      expect(d).toBeGreaterThanOrEqual(BACKOFF_MAX_MS / 2);
      expect(d).toBeLessThanOrEqual(BACKOFF_MAX_MS);
    }
  });

  it("refresh bootstrap berkala saat online", async () => {
    const server = fakeServer();
    const e = engine(server, { bootstrapRefreshMs: 0 });
    expect(await e.cycle()).toBe("ok");
    expect(server.paths()).toEqual(["/healthz", "/api/v1/pos/heartbeat", "/api/v1/pos/bootstrap"]);
  });
});

// LockManager tiruan dengan semantik eksklusif yang sama dengan Web Locks.
class FakeLocks implements LockLike {
  private tail: Promise<unknown> = Promise.resolve();
  async request<T>(_name: string, options: { signal?: AbortSignal }, cb: () => Promise<T>): Promise<T> {
    const prev = this.tail;
    let release!: () => void;
    this.tail = new Promise<void>((r) => (release = r));
    try {
      await new Promise<void>((resolve, reject) => {
        options.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
        void prev.then(() => resolve());
      });
      return await cb();
    } finally {
      release();
    }
  }
}

describe("sync engine: satu runner untuk banyak tab", () => {
  it("dengan Web Locks hanya tab pemegang lock yang mengirim; tab lain mengambil alih setelahnya", async () => {
    const locks = new FakeLocks();
    const serverA = fakeServer();
    const serverB = fakeServer();
    const dbB = new PosDb(dbName);
    const a = new SyncEngine({ db, baseUrl: "http://pos.test", fetch: serverA.fetch, locks, random: () => 0 });
    const b = new SyncEngine({ db: dbB, baseUrl: "http://pos.test", fetch: serverB.fetch, locks, random: () => 0 });
    a.start();
    b.start();
    await recordSale(db, sale());
    a.kick();
    b.kick();
    await until(async () => (await db.outbox.count()) === 0);
    expect(serverA.calls.length).toBeGreaterThan(0);
    expect(serverB.calls.length).toBe(0);

    await a.stop();
    await recordSale(dbB, sale());
    b.kick();
    await until(async () => (await dbB.outbox.count()) === 0);
    expect(serverB.paths()).toContain("/api/v1/pos/sync");
    await b.stop();
    dbB.close();
  });

  it("tanpa Web Locks memakai lease IndexedDB: tab kedua menunggu", async () => {
    const serverA = fakeServer();
    const serverB = fakeServer();
    const dbB = new PosDb(dbName);
    const a = new SyncEngine({ db, baseUrl: "http://pos.test", fetch: serverA.fetch, locks: null, random: () => 0 });
    const b = new SyncEngine({ db: dbB, baseUrl: "http://pos.test", fetch: serverB.fetch, locks: null, random: () => 0 });
    a.start();
    await until(() => serverA.calls.length > 0);
    b.start();
    await recordSale(db, sale());
    a.kick();
    b.kick();
    await until(async () => (await db.outbox.count()) === 0);
    expect(serverB.calls.length).toBe(0);
    await a.stop();
    await b.stop();
    // Lease dilepas saat runner berhenti.
    expect(await db.getMeta("syncLease")).toBeUndefined();
    dbB.close();
  });
});
