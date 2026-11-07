import type { OutboxRow, PosDb, PosTransaction } from "./db";
import { signCanonical } from "./lib/canonical";
import {
  cartTotals,
  checkDiscount,
  MAX_LINES,
  normalizeApprovalRef,
  validatePayments,
  validateQty,
  type CartLine,
  type PaymentEntry,
} from "./lib/cart-math";
import { uuidv7 } from "./lib/uuid";

export class SaleError extends Error {
  readonly code: string;
  constructor(code: string) {
    super(code);
    this.name = "SaleError";
    this.code = code;
  }
}

export interface SaleInput {
  lines: CartLine[];
  payments: PaymentEntry[];
  cashierId: string;
  cashierName: string;
  priceListVersion: number | null;
}

type UnsignedTxn = Omit<PosTransaction, "signature">;

export function buildPayload(
  input: SaleInput,
  ids: { clientTxnId: string; deviceSeq: number; occurredAt: string },
): UnsignedTxn {
  const totals = cartTotals(input.lines);
  return {
    client_txn_id: ids.clientTxnId,
    device_seq: ids.deviceSeq,
    cashier_id: input.cashierId,
    occurred_at: ids.occurredAt,
    price_list_version: input.priceListVersion!,
    total: totals.total,
    lines: input.lines.map((l) => ({
      product_id: l.productId,
      qty: l.qty,
      unit_price: l.unitPrice,
      discount: l.discount,
      ...(l.serials.length ? { serials: [...l.serials] } : {}),
    })),
    payments: input.payments.map((p) =>
      p.method === "CASH"
        ? { method: p.method, amount: p.amount }
        : { method: p.method, amount: p.amount, approval_ref: normalizeApprovalRef(p.approvalRef) },
    ),
  };
}

function validateSale(input: SaleInput): void {
  if (!input.priceListVersion || input.priceListVersion < 1) throw new SaleError("no_price_list");
  if (input.lines.length === 0) throw new SaleError("empty_cart");
  if (input.lines.length > MAX_LINES) throw new SaleError("too_many_lines");
  for (const l of input.lines) {
    if (validateQty(l.qty, l.serials.length)) throw new SaleError("invalid_qty");
    if (checkDiscount(l, l.discount, 100).error) throw new SaleError("invalid_discount");
  }
  const total = cartTotals(input.lines).total;
  const payErr = validatePayments(total, input.payments);
  if (payErr) throw new SaleError(payErr);
}

// recordSale mencatat penjualan sebagai fakta di perangkat:
// 1. Dalam SATU transaksi IndexedDB: ambil device_seq berikutnya, simpan
//    transaksi ke outbox (state "unsigned"), kurangi stok lokal, tandai serial
//    terjual, kosongkan keranjang. IndexedDB menjalankan transaksi rw yang
//    tumpang tindih secara berurutan, juga antar tab, jadi device_seq monoton
//    dan unik tanpa lock tambahan.
// 2. Tanda tangani di luar transaksi (WebCrypto async akan menutup transaksi
//    IndexedDB). Kalau tab mati di antara langkah 1 dan 2, baris "unsigned"
//    tetap ada dan ditandatangani sync engine; seq tidak pernah bolong.
export async function recordSale(
  db: PosDb,
  input: SaleInput,
  now: number = Date.now(),
  clientTxnId: string = uuidv7(now),
): Promise<OutboxRow> {
  validateSale(input);
  const row = await db.transaction("rw", [db.meta, db.outbox, db.stock, db.serials, db.cart], async () => {
    const seq = ((await db.getMeta("seq"))?.value ?? 0) + 1;
    await db.meta.put({ key: "seq", value: seq });
    const payload = buildPayload(input, {
      clientTxnId,
      deviceSeq: seq,
      occurredAt: new Date(now).toISOString(),
    });
    const outRow: OutboxRow = {
      clientTxnId,
      deviceSeq: seq,
      payload,
      signature: null,
      state: "unsigned",
      createdAt: now,
      cashierName: input.cashierName,
      attempts: 0,
      lastAttemptAt: null,
      rejection: null,
    };
    await db.outbox.add(outRow);
    for (const l of input.lines) {
      const stock = await db.stock.get(l.productId);
      await db.stock.put(
        stock
          ? { ...stock, localSold: stock.localSold + l.qty }
          : { productId: l.productId, physicalQty: 0, reservedQty: 0, availableQty: 0, localSold: l.qty },
      );
      for (const code of l.serials) {
        const s = await db.serials.get(code);
        if (s) await db.serials.put({ ...s, soldLocally: true });
      }
    }
    await db.cart.put({ id: "current", lines: [], pendingSerial: null, updatedAt: now });
    return outRow;
  });
  await signPendingRows(db);
  return (await db.outbox.get(row.clientTxnId)) ?? row;
}

// signPendingRows menandatangani setiap baris outbox "unsigned" dengan kunci
// perangkat. Aman dipanggil berulang dan dari banyak tab: signature
// deterministik, dan baris hanya berubah kalau masih "unsigned".
export async function signPendingRows(db: PosDb): Promise<number> {
  const device = await db.getMeta("device");
  if (!device) return 0;
  const rows = await db.outbox.where("state").equals("unsigned").toArray();
  let signed = 0;
  for (const r of rows) {
    const signature = await signCanonical(device.signingKey, r.payload as unknown as Record<string, unknown>);
    await db.transaction("rw", db.outbox, async () => {
      const cur = await db.outbox.get(r.clientTxnId);
      if (cur?.state === "unsigned") {
        await db.outbox.put({ ...cur, signature, state: "pending" });
        signed++;
      }
    });
  }
  return signed;
}
