import Dexie, { type EntityTable, type Table } from "dexie";
import type { components } from "@/shared/api/schema";
import type { CartLine } from "./lib/cart-math";

// Penyimpanan lokal kasir (IndexedDB lewat Dexie). Satu database per origin,
// dipakai bersama semua tab. Data referensi (produk, harga, stok, serial,
// kasir) diganti utuh setiap bootstrap; outbox adalah sumber kebenaran
// penjualan yang belum diterima server dan TIDAK PERNAH dihapus kecuali server
// menjawab ACCEPTED / ACCEPTED_WITH_CONFLICT / DUPLICATE, atau perangkat
// dikeluarkan dengan konfirmasi.

export type PosBootstrap = components["schemas"]["PosBootstrap"];
export type PosLimits = PosBootstrap["limits"];
export type PosTransaction = components["schemas"]["PosTransaction"];
export type PosSyncDetail = components["schemas"]["PosSyncDetail"];
export type PosSyncConflict = components["schemas"]["PosSyncConflict"];
export type PosSyncFlag = NonNullable<PosSyncDetail["flags"]>[number];
export type LocationSyncState = PosBootstrap["location"]["sync_state"];

export interface ProductRow {
  id: string;
  sku: string;
  /** SKU huruf besar untuk pencarian tanpa membedakan kapital. */
  skuKey: string;
  gtin: string | null;
  name: string;
  uom: string;
}

export interface PriceRow {
  productId: string;
  price: number;
}

export interface StockRow {
  productId: string;
  physicalQty: number;
  reservedQty: number;
  availableQty: number;
  /** Unit yang terjual di perangkat ini sejak snapshot terakhir dan belum masuk snapshot. */
  localSold: number;
}

export interface SerialRow {
  code: string;
  productId: string;
  /** Sudah terjual di perangkat ini (belum tentu sudah tersinkron). */
  soldLocally?: boolean;
}

export interface CashierRow {
  id: string;
  name: string;
  pinHash: string;
}

export interface CartState {
  id: "current";
  lines: CartLine[];
  /** Serial yang dipindai tetapi belum terdaftar: menunggu produk dipilih. */
  pendingSerial: string | null;
  updatedAt: number;
}

export type OutboxState = "unsigned" | "pending" | "rejected" | "resolved";

export interface RejectionInfo {
  reason: string;
  message: string;
  errors: { field: string; message: string }[];
  /** Server menyimpan transaksi ini (seq terisi, kirim ulang = DUPLICATE). */
  stored: boolean;
  at: number;
}

export interface OutboxRow {
  clientTxnId: string;
  deviceSeq: number;
  /** Transaksi persis seperti yang ditandatangani dan dikirim (tanpa signature). */
  payload: Omit<PosTransaction, "signature">;
  signature: string | null;
  state: OutboxState;
  createdAt: number;
  cashierName: string;
  attempts: number;
  lastAttemptAt: number | null;
  rejection: RejectionInfo | null;
}

export interface SyncLogRow {
  id?: number;
  clientTxnId: string;
  deviceSeq: number;
  status: string;
  result: string;
  flags: PosSyncFlag[];
  conflicts: PosSyncConflict[];
  occurredAt: string;
  total: number;
  at: number;
}

export interface DeviceMeta {
  key: "device";
  token: string;
  /** CryptoKey HMAC yang tidak bisa diekspor; hex kunci tidak disimpan. */
  signingKey: CryptoKey;
  deviceId: string;
  deviceName: string;
  locationId: string;
  locationCode: string;
  locationName: string;
  timezone: string;
  provisionedAt: number;
  /** Hasil navigator.storage.persist(); null kalau API tidak ada. */
  persisted: boolean | null;
}

export interface SeqMeta {
  key: "seq";
  value: number;
}

export interface LimitsMeta extends PosLimits {
  key: "limits";
}

export interface PriceListMeta {
  key: "priceList";
  id: string | null;
  code: string | null;
  version: number | null;
  validTo: string | null;
}

export interface SyncMeta {
  key: "sync";
  /** null = belum pernah dicek. */
  online: boolean | null;
  syncing: boolean;
  lastCheckAt: number | null;
  lastSyncAt: number | null;
  lastAckedSeq: number;
  missingSeqs: number[];
  missingSeqsTruncated: boolean;
  /** server_time − jam perangkat, ms. */
  serverOffsetMs: number | null;
  /** 401 dari server: token dicabut atau tidak dikenal. Sync berhenti. */
  authRevoked: boolean;
  /** Pesan server terakhir yang menghentikan sync (403, batch ditolak). */
  blockedMessage: string | null;
  locationSyncState: LocationSyncState | null;
  onlineBlockedReason: string | null;
  lastHeartbeatAt: number | null;
  lastBootstrapAt: number | null;
  /** Diisi tab mana pun yang meminta "perbarui data toko"; runner menjalankannya. */
  bootstrapRequestedAt?: number | null;
  heartbeatIntervalMs: number;
}

export interface PinLockMeta {
  key: "pinLock";
  failures: number;
  lockedUntil: number | null;
}

export interface SessionMeta {
  key: "session";
  cashierId: string;
  cashierName: string;
  since: number;
}

export interface PrefsMeta {
  key: "prefs";
  sound: boolean;
}

export interface LeaseMeta {
  key: "syncLease";
  owner: string;
  expiresAt: number;
}

export type MetaRow =
  | DeviceMeta
  | SeqMeta
  | LimitsMeta
  | PriceListMeta
  | SyncMeta
  | PinLockMeta
  | SessionMeta
  | PrefsMeta
  | LeaseMeta;

export type MetaKey = MetaRow["key"];
export type MetaOf<K extends MetaKey> = Extract<MetaRow, { key: K }>;

export const POS_DB_NAME = "mganik-pos";

export class PosDb extends Dexie {
  products!: EntityTable<ProductRow, "id">;
  prices!: EntityTable<PriceRow, "productId">;
  stock!: EntityTable<StockRow, "productId">;
  serials!: EntityTable<SerialRow, "code">;
  cashiers!: EntityTable<CashierRow, "id">;
  cart!: EntityTable<CartState, "id">;
  outbox!: EntityTable<OutboxRow, "clientTxnId">;
  syncLog!: EntityTable<SyncLogRow, "id">;
  meta!: Table<MetaRow, MetaKey>;

  constructor(name: string = POS_DB_NAME) {
    super(name);
    this.version(1).stores({
      products: "id, skuKey, gtin",
      prices: "productId",
      stock: "productId",
      serials: "code, productId",
      cashiers: "id",
      cart: "id",
      // &deviceSeq: indeks unik, jaminan terakhir bahwa dua tab tidak pernah
      // memakai nomor yang sama.
      outbox: "clientTxnId, &deviceSeq, state",
      syncLog: "++id, clientTxnId, at",
      meta: "key",
    });
  }

  async getMeta<K extends MetaKey>(key: K): Promise<MetaOf<K> | undefined> {
    return (await this.meta.get(key)) as MetaOf<K> | undefined;
  }
}

let shared: PosDb | null = null;

export function getPosDb(): PosDb {
  shared ??= new PosDb();
  return shared;
}

export function defaultSyncMeta(): SyncMeta {
  return {
    key: "sync",
    online: null,
    syncing: false,
    lastCheckAt: null,
    lastSyncAt: null,
    lastAckedSeq: 0,
    missingSeqs: [],
    missingSeqsTruncated: false,
    serverOffsetMs: null,
    authRevoked: false,
    blockedMessage: null,
    locationSyncState: null,
    onlineBlockedReason: null,
    lastHeartbeatAt: null,
    lastBootstrapAt: null,
    heartbeatIntervalMs: 30_000,
  };
}

// updateSyncMeta menggabungkan perubahan ke baris status sync dalam satu
// transaksi supaya dua penulis tidak saling menimpa field lain.
export async function updateSyncMeta(db: PosDb, patch: Partial<Omit<SyncMeta, "key">>): Promise<void> {
  await db.transaction("rw", db.meta, async () => {
    const cur = (await db.getMeta("sync")) ?? defaultSyncMeta();
    await db.meta.put({ ...cur, ...patch, key: "sync" });
  });
}

// countUnacked: transaksi yang belum tersimpan di server. Dipakai sebagai
// pending_count heartbeat. REJECTED yang disimpan server sudah ter-ack;
// REJECTED yang tidak disimpan (signature salah, seq terpakai) belum.
export async function countUnacked(db: PosDb): Promise<number> {
  const rows = await db.outbox.toArray();
  return rows.filter((r) => r.state === "unsigned" || r.state === "pending" || !r.rejection?.stored).length;
}
