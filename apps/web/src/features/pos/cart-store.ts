import type { CartState, PosDb, ProductRow } from "./db";
import { MAX_LINES, MAX_QTY, type CartLine } from "./lib/cart-math";
import { isValidSerialCode, looksLikeBarcode, normalizeSerialCode, parseScan } from "./lib/scan";

// Keranjang aktif disimpan di IndexedDB (satu baris "current") supaya selamat
// dari refresh, crash, atau tab tertutup. Semua perubahan lewat mutateCart
// dalam transaksi rw.

export function emptyCart(): CartState {
  return { id: "current", lines: [], pendingSerial: null, updatedAt: 0 };
}

export async function readCart(db: PosDb): Promise<CartState> {
  return (await db.cart.get("current")) ?? emptyCart();
}

export async function mutateCart<T>(db: PosDb, fn: (cart: CartState) => T): Promise<T> {
  return db.transaction("rw", db.cart, async () => {
    const cart = structuredClone(await readCart(db));
    const out = fn(cart);
    cart.updatedAt = Date.now();
    await db.cart.put(cart);
    return out;
  });
}

export type AddResult =
  | { ok: true; lineIndex: number }
  | { ok: false; error: "qty_limit" | "line_limit" | "serial_duplicate" | "serial_limit" };

function totalSerials(cart: CartState): number {
  return cart.lines.reduce((n, l) => n + l.serials.length, 0);
}

// addUnit menambah satu unit produk (dan serialnya bila ada) ke keranjang.
// Setiap scan = satu unit; baris produk yang sama digabung.
export function addUnit(
  cart: CartState,
  product: Pick<ProductRow, "id" | "name" | "sku">,
  unitPrice: number,
  serial: string | null,
  maxSerials: number,
): AddResult {
  if (serial) {
    if (cart.lines.some((l) => l.serials.includes(serial))) return { ok: false, error: "serial_duplicate" };
    if (totalSerials(cart) >= maxSerials) return { ok: false, error: "serial_limit" };
  }
  const idx = cart.lines.findIndex((l) => l.productId === product.id);
  if (idx >= 0) {
    const line = cart.lines[idx]!;
    if (line.qty >= MAX_QTY) return { ok: false, error: "qty_limit" };
    line.qty += 1;
    if (serial) line.serials.push(serial);
    // Baris yang baru disentuh naik ke atas supaya terlihat tanpa menggulung.
    cart.lines.splice(idx, 1);
    cart.lines.unshift(line);
    return { ok: true, lineIndex: 0 };
  }
  if (cart.lines.length >= MAX_LINES) return { ok: false, error: "line_limit" };
  cart.lines.unshift({
    productId: product.id,
    name: product.name,
    sku: product.sku,
    qty: 1,
    unitPrice,
    discount: 0,
    serials: serial ? [serial] : [],
  });
  return { ok: true, lineIndex: 0 };
}

export type ScanOutcome =
  | { type: "added"; productId: string; productName: string; serial: string | null; soldBefore: boolean }
  | { type: "serial-unknown"; code: string }
  | { type: "no-price"; productName: string }
  | { type: "not-found"; value: string }
  | { type: "invalid-url" }
  | { type: "rejected"; error: Exclude<AddResult, { ok: true }>["error"] }
  | { type: "search"; query: string };

export async function findProductByCode(db: PosDb, value: string): Promise<ProductRow | undefined> {
  const v = value.trim();
  if (looksLikeBarcode(v)) {
    const byGtin = await db.products.where("gtin").equals(v).first();
    if (byGtin) return byGtin;
  }
  return db.products.where("skuKey").equals(v.toUpperCase()).first();
}

// addProductToCart: dipakai setelah barcode/SKU dikenali atau produk dipilih
// dari pencarian. Serial yang menunggu (belum terdaftar) ikut terpasang.
export async function addProductToCart(
  db: PosDb,
  product: ProductRow,
  serial: string | null,
  maxSerials: number,
): Promise<ScanOutcome> {
  const price = await db.prices.get(product.id);
  if (!price) return { type: "no-price", productName: product.name };
  const soldBefore = serial ? Boolean((await db.serials.get(serial))?.soldLocally) : false;
  const res = await mutateCart(db, (cart) => {
    const attach = serial ?? cart.pendingSerial;
    const r = addUnit(cart, product, price.price, attach, maxSerials);
    if (r.ok && !serial) cart.pendingSerial = null;
    return { r, attach };
  });
  if (!res.r.ok) return { type: "rejected", error: res.r.error };
  return { type: "added", productId: product.id, productName: product.name, serial: res.attach, soldBefore };
}

async function addSerial(db: PosDb, code: string, maxSerials: number): Promise<ScanOutcome> {
  const row = await db.serials.get(code);
  if (!row) {
    // Serial tidak ada di daftar toko: tetap boleh dijual (server memberi
    // konflik SERIAL_UNREGISTERED), tetapi produknya harus dipilih kasir.
    await mutateCart(db, (cart) => {
      cart.pendingSerial = code;
    });
    return { type: "serial-unknown", code };
  }
  const product = await db.products.get(row.productId);
  if (!product) return { type: "not-found", value: code };
  return addProductToCart(db, product, code, maxSerials);
}

// resolveScan menerjemahkan isi kolom scan menjadi aksi keranjang.
// source "manual" yang tidak cocok persis menjadi pencarian produk.
export async function resolveScan(
  db: PosDb,
  raw: string,
  source: "scanner" | "manual",
  maxSerials: number,
): Promise<ScanOutcome | null> {
  const parsed = parseScan(raw);
  switch (parsed.kind) {
    case "empty":
      return null;
    case "invalid-url":
      return { type: "invalid-url" };
    case "serial-url":
      return addSerial(db, parsed.code, maxSerials);
    case "code": {
      const product = await findProductByCode(db, parsed.value);
      if (product) return addProductToCart(db, product, null, maxSerials);
      const code = normalizeSerialCode(parsed.value);
      if (isValidSerialCode(code)) {
        const known = await db.serials.get(code);
        if (known || source === "scanner") return addSerial(db, code, maxSerials);
      }
      return source === "manual" ? { type: "search", query: parsed.value } : { type: "not-found", value: parsed.value };
    }
  }
}

export async function searchProducts(db: PosDb, query: string, limit = 8): Promise<ProductRow[]> {
  const q = query.trim().toLocaleLowerCase("id-ID");
  if (!q) return [];
  const all = await db.products.toArray();
  const scored: { p: ProductRow; score: number }[] = [];
  for (const p of all) {
    const name = p.name.toLocaleLowerCase("id-ID");
    const sku = p.skuKey.toLocaleLowerCase("id-ID");
    let score = -1;
    if (sku === q || p.gtin === query.trim()) score = 0;
    else if (sku.startsWith(q) || name.startsWith(q)) score = 1;
    else if (name.includes(q) || sku.includes(q) || (p.gtin ?? "").startsWith(q)) score = 2;
    if (score >= 0) scored.push({ p, score });
  }
  scored.sort((a, b) => a.score - b.score || a.p.name.localeCompare(b.p.name, "id-ID"));
  return scored.slice(0, limit).map((s) => s.p);
}

export function setLineQty(cart: CartState, productId: string, qty: number): void {
  const line = cart.lines.find((l) => l.productId === productId);
  if (!line) return;
  line.qty = qty;
  // Diskon tidak boleh melebihi subtotal baru.
  line.discount = Math.min(line.discount, line.qty * line.unitPrice);
}

export function setLineDiscount(cart: CartState, productId: string, discount: number): void {
  const line = cart.lines.find((l) => l.productId === productId);
  if (line) line.discount = discount;
}

export function removeLine(cart: CartState, productId: string): CartLine | undefined {
  const idx = cart.lines.findIndex((l) => l.productId === productId);
  if (idx < 0) return undefined;
  return cart.lines.splice(idx, 1)[0];
}

export function clearCart(cart: CartState): void {
  cart.lines = [];
  cart.pendingSerial = null;
}
