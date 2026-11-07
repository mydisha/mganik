import { countUnacked, defaultSyncMeta, updateSyncMeta, type OutboxRow, type PosDb, type SyncLogRow } from "../db";
import { applyBootstrap } from "../device";
import { uuidv7 } from "../lib/uuid";
import { signPendingRows } from "../sale";
import { posStrings } from "../strings";
import {
  fetchBootstrapAll,
  ping,
  postHeartbeat,
  postSync,
  type ApiOutcome,
  type PosApiConfig,
  type PosSyncResponse,
} from "./api";

// Sync engine kasir (PLANNING §8.4).
// - Satu runner untuk semua tab: Web Locks API (`mganik-pos-sync`); kalau API
//   itu tidak ada, lease di IndexedDB. Dua runner tetap aman karena server
//   idempotent (DUPLICATE), lease hanya menghemat request.
// - Online = /healthz menjawab, bukan navigator.onLine.
// - Urutan per siklus: tanda tangani baris tertinggal → kirim outbox berurutan
//   device_seq sampai habis → heartbeat dengan pending_count → refresh
//   bootstrap berkala.
// - 401 = device dicabut: berhenti, outbox TIDAK dihapus.
// - Gagal sementara: backoff eksponensial dengan jitter.

export const SYNC_LOCK_NAME = "mganik-pos-sync";
export const SYNC_CHANNEL_NAME = "mganik-pos-sync";
export const BACKOFF_BASE_MS = 2_000;
export const BACKOFF_MAX_MS = 60_000;
export const BOOTSTRAP_REFRESH_MS = 10 * 60_000;
export const LEASE_MS = 60_000;
const SYNC_LOG_KEEP = 200;
const NIL_UUID = "00000000-0000-0000-0000-000000000000";

export type CycleOutcome = "ok" | "retry" | "offline" | "blocked" | "revoked" | "no-device";

export interface LockLike {
  request<T>(name: string, options: { signal?: AbortSignal }, callback: () => Promise<T>): Promise<T>;
}

export interface KickChannel {
  postMessage(message: unknown): void;
  addEventListener(type: "message", listener: () => void): void;
  removeEventListener(type: "message", listener: () => void): void;
  close?(): void;
}

export interface EngineEnv {
  db: PosDb;
  baseUrl: string;
  fetch: typeof fetch;
  locks: LockLike | null;
  channel?: KickChannel | null;
  now?: () => number;
  random?: () => number;
  bootstrapRefreshMs?: number;
  backoffBaseMs?: number;
}

// backoffDelay: eksponensial dengan "equal jitter" (separuh tetap, separuh acak)
// supaya banyak perangkat yang pulih bersamaan tidak menyerbu server serentak.
export function backoffDelay(attempt: number, random: number, base = BACKOFF_BASE_MS, max = BACKOFF_MAX_MS): number {
  const exp = Math.min(max, base * 2 ** Math.max(0, attempt - 1));
  return Math.round(exp / 2 + (random * exp) / 2);
}

function isAcceptedLike(status: string): status is "ACCEPTED" | "ACCEPTED_WITH_CONFLICT" {
  return status === "ACCEPTED" || status === "ACCEPTED_WITH_CONFLICT";
}

export class SyncEngine {
  private readonly env: Required<Omit<EngineEnv, "channel" | "locks">> & Pick<EngineEnv, "channel" | "locks">;
  private running = false;
  private stopping = false;
  private restartAfterStop = false;
  private loopDone: Promise<void> | null = null;
  private abort: AbortController | null = null;
  private wake: (() => void) | null = null;
  private kicked = false;
  private failures = 0;
  private readonly leaseOwner = uuidv7();
  private readonly onChannel = () => this.kick();

  constructor(env: EngineEnv) {
    this.env = {
      now: () => Date.now(),
      random: Math.random,
      bootstrapRefreshMs: BOOTSTRAP_REFRESH_MS,
      backoffBaseMs: BACKOFF_BASE_MS,
      ...env,
    };
  }

  get isRunning(): boolean {
    return this.running;
  }

  start(): void {
    if (this.running) {
      // start() saat stop() masih berjalan (mis. efek React dipasang ulang):
      // jalankan lagi begitu loop lama selesai.
      if (this.stopping) this.restartAfterStop = true;
      return;
    }
    this.running = true;
    this.stopping = false;
    this.abort = new AbortController();
    this.env.channel?.addEventListener("message", this.onChannel);
    this.loopDone = this.loop()
      .catch(() => undefined)
      .finally(() => {
        this.running = false;
        if (this.restartAfterStop) {
          this.restartAfterStop = false;
          this.start();
        }
      });
  }

  async stop(): Promise<void> {
    this.restartAfterStop = false;
    if (!this.running) return;
    this.stopping = true;
    this.abort?.abort();
    this.wake?.();
    this.env.channel?.removeEventListener("message", this.onChannel);
    await this.loopDone;
  }

  /** Bangunkan runner sekarang (mis. setelah penjualan). Juga memberi tahu tab lain. */
  kick(broadcast = false): void {
    this.kicked = true;
    this.wake?.();
    if (broadcast) this.env.channel?.postMessage({ type: "kick" });
  }

  /** Setelah 401: kasir/kepala toko meminta cek ulang token. */
  async resume(): Promise<void> {
    await updateSyncMeta(this.env.db, { authRevoked: false, blockedMessage: null });
    this.kick(true);
  }

  private async loop(): Promise<void> {
    const body = async () => {
      while (!this.stopping) {
        if (!this.env.locks && !(await this.acquireLease())) {
          await this.waitForKick(LEASE_MS / 2);
          continue;
        }
        let outcome: CycleOutcome;
        try {
          outcome = await this.cycle();
        } catch {
          outcome = "retry";
        }
        if (this.stopping) break;
        await this.waitForKick(await this.nextDelay(outcome));
      }
      if (!this.env.locks) await this.releaseLease();
    };
    if (!this.env.locks) return body();
    try {
      await this.env.locks.request(SYNC_LOCK_NAME, { signal: this.abort!.signal }, body);
    } catch (err) {
      // AbortError: dihentikan saat masih menunggu giliran lock.
      if (!(err instanceof DOMException && err.name === "AbortError") && !this.stopping) throw err;
    }
  }

  private waitForKick(ms: number): Promise<void> {
    if (this.kicked || this.stopping) {
      this.kicked = false;
      return Promise.resolve();
    }
    return new Promise((resolve) => {
      const timer = Number.isFinite(ms) ? setTimeout(() => done(), ms) : null;
      const done = () => {
        if (timer) clearTimeout(timer);
        this.wake = null;
        this.kicked = false;
        resolve();
      };
      this.wake = done;
    });
  }

  private async nextDelay(outcome: CycleOutcome): Promise<number> {
    switch (outcome) {
      case "ok": {
        this.failures = 0;
        const sync = await this.env.db.getMeta("sync");
        return sync?.heartbeatIntervalMs ?? 30_000;
      }
      case "revoked":
      case "no-device":
        // Menunggu kick (setup ulang, "periksa lagi").
        this.failures = 0;
        return Number.POSITIVE_INFINITY;
      case "blocked":
        // 403/400/422: mengulang cepat tidak mengubah jawaban server.
        this.failures++;
        return Math.max(
          this.env.backoffBaseMs * 15,
          backoffDelay(this.failures, this.env.random(), this.env.backoffBaseMs, BACKOFF_MAX_MS),
        );
      default:
        this.failures++;
        return backoffDelay(this.failures, this.env.random(), this.env.backoffBaseMs, BACKOFF_MAX_MS);
    }
  }

  private async acquireLease(): Promise<boolean> {
    const { db } = this.env;
    const now = this.env.now();
    return db.transaction("rw", db.meta, async () => {
      const lease = await db.getMeta("syncLease");
      if (lease && lease.owner !== this.leaseOwner && lease.expiresAt > now) return false;
      await db.meta.put({ key: "syncLease", owner: this.leaseOwner, expiresAt: now + LEASE_MS });
      return true;
    });
  }

  private async releaseLease(): Promise<void> {
    const { db } = this.env;
    await db.transaction("rw", db.meta, async () => {
      const lease = await db.getMeta("syncLease");
      if (lease?.owner === this.leaseOwner) await db.meta.delete("syncLease");
    });
  }

  /** Satu siklus penuh. Dipanggil loop; juga dipanggil langsung oleh test. */
  async cycle(): Promise<CycleOutcome> {
    const { db } = this.env;
    const device = await db.getMeta("device");
    if (!device) return "no-device";
    const sync = (await db.getMeta("sync")) ?? defaultSyncMeta();
    if (sync.authRevoked) return "revoked";

    const online = await ping(this.env.baseUrl, this.env.fetch);
    await updateSyncMeta(db, { online, lastCheckAt: this.env.now() });
    if (!online) return "offline";

    const cfg: PosApiConfig = { baseUrl: this.env.baseUrl, token: device.token, fetch: this.env.fetch };
    await updateSyncMeta(db, { syncing: true });
    try {
      await signPendingRows(db);
      const drained = await this.drain(cfg);
      if (drained !== "ok" && drained !== "retry" && drained !== "blocked") return drained;
      const blocked = drained === "blocked";

      // Kontrak: kirim outbox sampai habis, LALU heartbeat dengan pending_count.
      const hb = await this.heartbeat(cfg, blocked);
      if (hb !== "ok") return hb;

      const after = (await db.getMeta("sync")) ?? defaultSyncMeta();
      if (
        // Sync tertahan (403/400/422/batch terlalu besar): limits atau izin di
        // server mungkin sudah berubah, jadi bootstrap tetap diambil ulang.
        blocked ||
        !after.lastBootstrapAt ||
        this.env.now() - after.lastBootstrapAt >= this.env.bootstrapRefreshMs ||
        (after.bootstrapRequestedAt ?? 0) > after.lastBootstrapAt
      ) {
        const b = await this.refreshBootstrap(cfg, blocked);
        if (b !== "ok") return b;
      }
      return drained;
    } finally {
      await updateSyncMeta(db, { syncing: false });
    }
  }

  private async handleFailure(res: Exclude<ApiOutcome<unknown>, { kind: "ok" }>): Promise<CycleOutcome> {
    const { db } = this.env;
    switch (res.kind) {
      case "unauthorized":
        await updateSyncMeta(db, { authRevoked: true, blockedMessage: res.message });
        return "revoked";
      case "network":
        await updateSyncMeta(db, { online: false });
        return "offline";
      case "forbidden":
      case "invalid":
      case "too_large":
        // Tanpa problem+json (mis. proxy) tetap ada pesan, supaya status "tertahan" tidak kosong.
        await updateSyncMeta(db, { blockedMessage: res.message ?? posStrings.sync.blockedFallback });
        return "blocked";
      case "retry":
        return "retry";
    }
  }

  private async drain(cfg: PosApiConfig): Promise<CycleOutcome> {
    const { db } = this.env;
    const limits = await db.getMeta("limits");
    let batchLimit = Math.max(1, limits?.sync_max_batch ?? 500);
    const maxBytes = Math.floor((limits?.sync_max_body_bytes ?? 5 * 1024 * 1024) * 0.9);

    // Transaksi yang dijawab RETRY tidak dikirim ulang di siklus yang sama;
    // siklus berikutnya (setelah backoff) mencobanya lagi.
    const deferred = new Set<string>();
    for (let round = 0; round < 1_000; round++) {
      const pending = (await db.outbox.where("state").equals("pending").sortBy("deviceSeq")).filter(
        (r) => !deferred.has(r.clientTxnId),
      );
      const sync = (await db.getMeta("sync")) ?? defaultSyncMeta();
      if (pending.length === 0) {
        if (deferred.size > 0) return "retry";
        // Outbox kosong tetapi server melaporkan nomor yang hilang: batch kosong
        // memperbarui ack dan daftar missing_seqs untuk panel perhatian.
        if (sync.missingSeqs.length === 0 && sync.onlineBlockedReason !== "missing_seqs") return "ok";
        const res = await postSync(cfg, { client_time: new Date(this.env.now()).toISOString(), transactions: [] });
        if (res.kind !== "ok") return this.handleFailure(res);
        await this.applyResults([], res.data, deferred);
        return "ok";
      }

      const batch: OutboxRow[] = [];
      let bytes = 64;
      for (const row of pending) {
        if (batch.length >= batchLimit) break;
        const size = JSON.stringify(row.payload).length + 90;
        if (batch.length > 0 && bytes + size > maxBytes) break;
        batch.push(row);
        bytes += size;
      }
      const res = await postSync(cfg, {
        client_time: new Date(this.env.now()).toISOString(),
        transactions: batch.map((r) => ({ ...r.payload, signature: r.signature! })),
      });
      if (res.kind === "too_large" && batch.length > 1) {
        batchLimit = Math.max(1, Math.floor(batch.length / 2));
        continue;
      }
      if (res.kind !== "ok") return this.handleFailure(res);
      const progressed = await this.applyResults(batch, res.data, deferred);
      if (!progressed) {
        // Server tidak menjawab satu pun transaksi batch ini: jangan berputar.
        for (const r of batch) deferred.add(r.clientTxnId);
      }
    }
    return "retry";
  }

  // applyResults memproses hasil per transaksi dalam satu transaksi IndexedDB.
  // Mengembalikan true kalau minimal satu transaksi selesai (bukan RETRY);
  // transaksi RETRY dicatat di `deferred`.
  private async applyResults(batch: OutboxRow[], data: PosSyncResponse, deferred: Set<string>): Promise<boolean> {
    const { db } = this.env;
    const now = this.env.now();
    let progressed = false;
    await db.transaction("rw", [db.outbox, db.syncLog, db.meta], async () => {
      for (const r of data.results) {
        const row = batch[r.index];
        if (!row) continue;
        // Identitas yang tidak terbaca server dijawab dengan nil UUID: cocokkan lewat index.
        if (r.client_txn_id !== row.clientTxnId && r.client_txn_id !== NIL_UUID) continue;
        // DUPLICATE membawa hasil asli: perlakukan sesuai hasil itu.
        const effective = r.status === "DUPLICATE" ? r.result || "ACCEPTED" : r.status;
        const detail = r.detail ?? {};
        if (isAcceptedLike(effective)) {
          progressed = true;
          await db.outbox.delete(row.clientTxnId);
          const log: SyncLogRow = {
            clientTxnId: row.clientTxnId,
            deviceSeq: row.deviceSeq,
            status: r.status,
            result: effective,
            flags: detail.flags ?? [],
            conflicts: detail.conflicts ?? [],
            occurredAt: row.payload.occurred_at,
            total: row.payload.total,
            at: now,
          };
          await db.syncLog.add(log);
        } else if (effective === "REJECTED") {
          progressed = true;
          await db.outbox.put({
            ...row,
            state: "rejected",
            attempts: row.attempts + 1,
            lastAttemptAt: now,
            rejection: {
              reason: detail.reason ?? "unknown",
              message: detail.message ?? "",
              errors: (detail.errors ?? []).map((e) => ({ field: e.field, message: e.message })),
              stored: r.status === "DUPLICATE" || Boolean(detail.server_txn_id),
              at: now,
            },
          });
        } else {
          deferred.add(row.clientTxnId);
          await db.outbox.put({ ...row, attempts: row.attempts + 1, lastAttemptAt: now });
        }
      }
      const serverTime = Date.parse(data.server_time);
      const sync = (await db.getMeta("sync")) ?? defaultSyncMeta();
      await db.meta.put({
        ...sync,
        lastSyncAt: now,
        lastAckedSeq: data.ack_up_to_seq,
        missingSeqs: data.missing_seqs,
        missingSeqsTruncated: data.missing_seqs_truncated,
        serverOffsetMs: Number.isFinite(serverTime) ? serverTime - now : sync.serverOffsetMs,
        blockedMessage: null,
      });
      const count = await db.syncLog.count();
      if (count > SYNC_LOG_KEEP) {
        const old = await db.syncLog.orderBy("id").limit(count - SYNC_LOG_KEEP).primaryKeys();
        await db.syncLog.bulkDelete(old);
      }
    });
    return progressed;
  }

  private async heartbeat(cfg: PosApiConfig, keepBlocked = false): Promise<CycleOutcome> {
    const { db } = this.env;
    const pending = await countUnacked(db);
    const now = this.env.now();
    const res = await postHeartbeat(cfg, { client_time: new Date(now).toISOString(), pending_count: pending });
    if (res.kind !== "ok") return this.handleFailure(res);
    const serverTime = Date.parse(res.data.server_time);
    const sync = (await db.getMeta("sync")) ?? defaultSyncMeta();
    await updateSyncMeta(db, {
      lastHeartbeatAt: now,
      locationSyncState: res.data.location_sync_state,
      onlineBlockedReason: res.data.online_blocked_reason,
      lastAckedSeq: Math.max(sync.lastAckedSeq, res.data.last_acked_seq),
      heartbeatIntervalMs: Math.max(5, res.data.heartbeat_interval_seconds) * 1000,
      serverOffsetMs: Number.isFinite(serverTime) ? serverTime - now : sync.serverOffsetMs,
      ...(keepBlocked ? {} : { blockedMessage: null }),
    });
    return "ok";
  }

  private async refreshBootstrap(cfg: PosApiConfig, keepBlocked = false): Promise<CycleOutcome> {
    const res = await fetchBootstrapAll({ ...cfg, timeoutMs: 60_000 });
    if (res.kind !== "ok") return this.handleFailure(res);
    await applyBootstrap(this.env.db, res.data.bootstrap, res.data.serials, this.env.now());
    if (!keepBlocked) await updateSyncMeta(this.env.db, { blockedMessage: null });
    return "ok";
  }
}
