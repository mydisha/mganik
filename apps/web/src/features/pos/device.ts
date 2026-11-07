import { defaultSyncMeta, type DeviceMeta, type PosBootstrap, type PosDb, type SerialRow } from "./db";
import { importSigningKey } from "./lib/canonical";
import { fetchBootstrapAll, type ApiOutcome, type PosApiConfig, type PosSerialPage } from "./sync/api";

// Pendaftaran perangkat: token bearer + signing key dari Command Center
// ditempel di sini, divalidasi formatnya, lalu bootstrap pertama diambil.
// Kunci HMAC disimpan sebagai CryptoKey yang tidak bisa diekspor.

// Format token sengaja longgar (ASCII tercetak tanpa spasi, 20-200 karakter):
// token produksi `pos_<base64url>` dan token demo seed `demo-pos-<hex>` sama-sama
// lolos; sah atau tidaknya diputuskan server (401).
const TOKEN_PATTERN = /^[\x21-\x7e]{20,200}$/;
const KEY_PATTERN = /^[0-9a-f]{64}$/;

export function normalizeToken(raw: string): string {
  return raw.trim();
}

export function normalizeSigningKey(raw: string): string {
  return raw.replace(/\s+/g, "").toLowerCase();
}

export function isTokenFormat(token: string): boolean {
  return TOKEN_PATTERN.test(token);
}

export function isSigningKeyFormat(key: string): boolean {
  return KEY_PATTERN.test(key);
}

// extractCredentials: kalau kasir menempel satu blok teks berisi token dan
// kunci sekaligus (mis. disalin dari Command Center), ambil keduanya.
// Kunci = token 64 hex yang berdiri sendiri; token = token tercetak lain yang
// formatnya sah (mis. `pos_…` atau `demo-pos-<hex>`, yang memuat hex tetapi
// tidak berdiri sendiri). Label seperti "Token:" diabaikan.
export function extractCredentials(text: string): { token: string | null; signingKey: string | null } {
  const words = text
    .split(/\s+/)
    .map((w) => w.replace(/^["'`]+|["'`,;]+$/g, ""))
    .filter(Boolean);
  const keyWord = words.find((w) => /^[0-9a-fA-F]{64}$/.test(w)) ?? null;
  const token =
    words.find((w) => w !== keyWord && !w.endsWith(":") && isTokenFormat(w) && !/^[0-9a-fA-F]{64}$/.test(w)) ?? null;
  return { token, signingKey: keyWord?.toLowerCase() ?? null };
}

// applyBootstrap mengganti data referensi dalam satu transaksi. Stok lokal =
// snapshot server, dengan `localSold` diisi ulang dari outbox yang belum
// terkirim (snapshot server belum memuat penjualan itu).
export async function applyBootstrap(
  db: PosDb,
  b: PosBootstrap,
  serials: PosSerialPage["items"],
  now: number,
): Promise<void> {
  await db.transaction(
    "rw",
    [db.products, db.prices, db.stock, db.serials, db.cashiers, db.meta, db.outbox],
    async () => {
      const unsent = (await db.outbox.toArray()).filter((r) => r.state === "unsigned" || r.state === "pending");
      const pendingQty = new Map<string, number>();
      const pendingSerials = new Set<string>();
      for (const r of unsent) {
        for (const l of r.payload.lines) {
          pendingQty.set(l.product_id, (pendingQty.get(l.product_id) ?? 0) + l.qty);
          for (const s of l.serials ?? []) pendingSerials.add(s);
        }
      }

      await db.products.clear();
      await db.products.bulkPut(
        b.products.map((p) => ({ id: p.id, sku: p.sku, skuKey: p.sku.toUpperCase(), gtin: p.gtin, name: p.name, uom: p.uom })),
      );
      await db.prices.clear();
      await db.prices.bulkPut((b.price_list?.items ?? []).map((i) => ({ productId: i.product_id, price: i.price })));
      await db.stock.clear();
      await db.stock.bulkPut(
        b.stock.map((s) => ({
          productId: s.product_id,
          physicalQty: s.physical_qty,
          reservedQty: s.reserved_qty,
          availableQty: s.available_qty,
          localSold: pendingQty.get(s.product_id) ?? 0,
        })),
      );
      await db.serials.clear();
      await db.serials.bulkPut(
        serials.map((s): SerialRow => ({
          code: s.code,
          productId: s.product_id,
          ...(pendingSerials.has(s.code) ? { soldLocally: true } : {}),
        })),
      );
      await db.cashiers.clear();
      await db.cashiers.bulkPut(b.cashiers.map((c) => ({ id: c.id, name: c.name, pinHash: c.pos_pin_hash })));

      await db.meta.put({ key: "limits", ...b.limits });
      await db.meta.put({
        key: "priceList",
        id: b.price_list?.id ?? null,
        code: b.price_list?.code ?? null,
        version: b.price_list?.version ?? null,
        validTo: b.price_list?.valid_to ?? null,
      });
      const device = await db.getMeta("device");
      if (device) {
        await db.meta.put({
          ...device,
          deviceName: b.device.name,
          locationId: b.location.id,
          locationCode: b.location.code,
          locationName: b.location.name,
          timezone: b.location.timezone,
        });
      }
      const sync = (await db.getMeta("sync")) ?? defaultSyncMeta();
      const serverTime = Date.parse(b.server_time);
      await db.meta.put({
        ...sync,
        lastBootstrapAt: now,
        locationSyncState: b.location.sync_state,
        lastAckedSeq: Math.max(sync.lastAckedSeq, b.device.last_acked_seq),
        serverOffsetMs: Number.isFinite(serverTime) ? serverTime - now : sync.serverOffsetMs,
        heartbeatIntervalMs: b.limits.heartbeat_interval_seconds * 1000,
      });
      // Kasir yang tidak lagi diizinkan di toko ini keluar; keranjang tetap.
      const session = await db.getMeta("session");
      if (session && !b.cashiers.some((c) => c.id === session.cashierId)) await db.meta.delete("session");
    },
  );
}

export type SetupResult =
  | { ok: true; persisted: boolean | null }
  | { ok: false; error: "token_format" | "key_format" | "network" | "outbox_not_empty" }
  | { ok: false; error: "server"; message: string | null };

export async function requestPersistentStorage(): Promise<boolean | null> {
  try {
    if (!navigator.storage?.persist) return null;
    if (await navigator.storage.persisted?.()) return true;
    return await navigator.storage.persist();
  } catch {
    return null;
  }
}

export async function setupDevice(
  db: PosDb,
  input: { token: string; signingKey: string },
  env: { baseUrl: string; fetch: typeof fetch; now?: number; persist?: () => Promise<boolean | null> },
): Promise<SetupResult> {
  const token = normalizeToken(input.token);
  const keyHex = normalizeSigningKey(input.signingKey);
  if (!isTokenFormat(token)) return { ok: false, error: "token_format" };
  if (!isSigningKeyFormat(keyHex)) return { ok: false, error: "key_format" };
  // Outbox dari perangkat lama ditandatangani kunci lama dan memakai nomor
  // urut lama; mencampurnya dengan device baru membuat transaksi ditolak.
  if ((await db.outbox.count()) > 0) return { ok: false, error: "outbox_not_empty" };

  const persisted = await (env.persist ?? requestPersistentStorage)();
  const cfg: PosApiConfig = { baseUrl: env.baseUrl, token, fetch: env.fetch, timeoutMs: 60_000 };
  const res: ApiOutcome<{ bootstrap: PosBootstrap; serials: PosSerialPage["items"] }> = await fetchBootstrapAll(cfg);
  if (res.kind === "network") return { ok: false, error: "network" };
  if (res.kind !== "ok") return { ok: false, error: "server", message: res.message };

  const now = env.now ?? Date.now();
  const signingKey = await importSigningKey(keyHex);
  const b = res.data.bootstrap;
  const device: DeviceMeta = {
    key: "device",
    token,
    signingKey,
    deviceId: b.device.id,
    deviceName: b.device.name,
    locationId: b.location.id,
    locationCode: b.location.code,
    locationName: b.location.name,
    timezone: b.location.timezone,
    provisionedAt: now,
    persisted,
  };
  await db.transaction("rw", db.meta, async () => {
    await db.meta.put(device);
    // Nomor urut melanjutkan yang sudah diterima server untuk device ini.
    await db.meta.put({ key: "seq", value: b.device.last_acked_seq });
    await db.meta.put({ ...defaultSyncMeta(), lastAckedSeq: b.device.last_acked_seq });
    await db.meta.delete("session");
  });
  await applyBootstrap(db, b, res.data.serials, now);
  return { ok: true, persisted };
}

// wipeDevice menghapus semua data lokal kasir, termasuk outbox. Pemanggil
// wajib sudah meminta konfirmasi (dan menawarkan ekspor kalau outbox berisi).
export async function wipeDevice(db: PosDb): Promise<void> {
  await db.transaction("rw", db.tables, async () => {
    await Promise.all(db.tables.map((t) => t.clear()));
  });
}

// exportOutbox: cadangan manual transaksi yang belum diterima server (mis.
// perangkat sudah dicabut). Berisi transaksi bertanda tangan apa adanya.
export async function exportOutbox(db: PosDb, now: number = Date.now()): Promise<Blob> {
  const device = await db.getMeta("device");
  const rows = await db.outbox.orderBy("deviceSeq").toArray();
  const doc = {
    exported_at: new Date(now).toISOString(),
    device_id: device?.deviceId ?? null,
    location_code: device?.locationCode ?? null,
    transactions: rows.map((r) => ({
      state: r.state,
      rejection: r.rejection,
      transaction: { ...r.payload, ...(r.signature ? { signature: r.signature } : {}) },
    })),
  };
  return new Blob([JSON.stringify(doc, null, 2)], { type: "application/json" });
}
