import { useLiveQuery } from "dexie-react-hooks";
import { createContext, useContext } from "react";
import { emptyCart } from "./cart-store";
import { countUnacked, defaultSyncMeta, type PosDb } from "./db";
import type { SyncEngine } from "./sync/engine";

// Data layar kasir dibaca lewat liveQuery: setiap perubahan IndexedDB, dari tab
// mana pun, langsung tampil tanpa state global tambahan.

export interface PosContextValue {
  db: PosDb;
  engine: SyncEngine | null;
}

export const PosContext = createContext<PosContextValue | null>(null);

export function usePos(): PosContextValue {
  const ctx = useContext(PosContext);
  if (!ctx) throw new Error("usePos dipakai di luar PosContext");
  return ctx;
}

// `undefined` = masih memuat; `null` = belum ada.
export function useDevice() {
  const { db } = usePos();
  return useLiveQuery(async () => (await db.getMeta("device")) ?? null, [db]);
}

export function useSyncMeta() {
  const { db } = usePos();
  return useLiveQuery(async () => (await db.getMeta("sync")) ?? defaultSyncMeta(), [db]);
}

export function useLimits() {
  const { db } = usePos();
  return useLiveQuery(async () => (await db.getMeta("limits")) ?? null, [db]);
}

export function usePriceList() {
  const { db } = usePos();
  return useLiveQuery(async () => (await db.getMeta("priceList")) ?? null, [db]);
}

export function useSession() {
  const { db } = usePos();
  return useLiveQuery(async () => (await db.getMeta("session")) ?? null, [db]);
}

export function usePrefs() {
  const { db } = usePos();
  return useLiveQuery(async () => (await db.getMeta("prefs")) ?? { key: "prefs" as const, sound: true }, [db]);
}

export function useCart() {
  const { db } = usePos();
  return useLiveQuery(async () => (await db.cart.get("current")) ?? emptyCart(), [db]);
}

export interface OutboxSummary {
  /** Menunggu dikirim (belum dijawab server). */
  waiting: number;
  /** Belum tersimpan di server (pending_count heartbeat). */
  unacked: number;
  /** Ditolak dan belum ditandai ditangani. */
  rejected: number;
  total: number;
}

export function useOutboxSummary() {
  const { db } = usePos();
  return useLiveQuery(async (): Promise<OutboxSummary> => {
    const rows = await db.outbox.toArray();
    return {
      waiting: rows.filter((r) => r.state === "pending" || r.state === "unsigned").length,
      unacked: await countUnacked(db),
      rejected: rows.filter((r) => r.state === "rejected").length,
      total: rows.length,
    };
  }, [db]);
}
