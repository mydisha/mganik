// Aritmetika keranjang dan pembayaran. Semua uang dalam rupiah utuh (integer).
// Aturan mengikuti PosTransaction di kontrak: total = Σ(qty × unit_price −
// discount), discount <= qty × unit_price, pembayaran >= total, non-tunai tidak
// boleh melebihi total (kembalian hanya dari tunai), maksimal satu CASH,
// approval_ref wajib untuk EDC/QRIS, maksimal 10 pembayaran.

export const MAX_QTY = 1000;
export const MAX_LINES = 200;
export const MAX_PAYMENTS = 10;
export const MAX_UNIT_PRICE = 100_000_000_000;
export const MAX_PAYMENT_AMOUNT = 100_000_000_000_000;
export const MAX_APPROVAL_REF = 64;

export interface CartLine {
  productId: string;
  name: string;
  sku: string;
  qty: number;
  unitPrice: number;
  discount: number;
  serials: string[];
}

export type PaymentMethod = "CASH" | "EDC" | "QRIS";

export interface PaymentEntry {
  method: PaymentMethod;
  amount: number;
  approvalRef?: string;
}

export function lineSubtotal(line: Pick<CartLine, "qty" | "unitPrice">): number {
  return line.qty * line.unitPrice;
}

export function lineTotal(line: Pick<CartLine, "qty" | "unitPrice" | "discount">): number {
  return lineSubtotal(line) - line.discount;
}

export interface CartTotals {
  subtotal: number;
  discount: number;
  total: number;
  units: number;
}

export function cartTotals(lines: readonly CartLine[]): CartTotals {
  let subtotal = 0;
  let discount = 0;
  let units = 0;
  for (const l of lines) {
    subtotal += lineSubtotal(l);
    discount += l.discount;
    units += l.qty;
  }
  return { subtotal, discount, total: subtotal - discount, units };
}

export type QtyError = "not_integer" | "below_min" | "above_max" | "below_serials";

export function validateQty(qty: number, serialCount = 0): QtyError | null {
  if (!Number.isInteger(qty)) return "not_integer";
  if (qty < 1) return "below_min";
  if (qty > MAX_QTY) return "above_max";
  if (qty < serialCount) return "below_serials";
  return null;
}

export type DiscountError = "not_integer" | "negative" | "exceeds_subtotal";

export interface DiscountCheck {
  error: DiscountError | null;
  /** Di atas high_discount_percent: tetap boleh, tetapi server memberi flag HIGH_DISCOUNT. */
  high: boolean;
}

export function checkDiscount(
  line: Pick<CartLine, "qty" | "unitPrice">,
  discount: number,
  highDiscountPercent: number,
): DiscountCheck {
  if (!Number.isInteger(discount)) return { error: "not_integer", high: false };
  if (discount < 0) return { error: "negative", high: false };
  const sub = lineSubtotal(line);
  if (discount > sub) return { error: "exceeds_subtotal", high: false };
  // Bandingkan dalam integer: discount/sub > pct/100  ⇔  discount*100 > sub*pct.
  return { error: null, high: sub > 0 && discount * 100 > sub * highDiscountPercent };
}

export function isHighDiscount(line: CartLine, highDiscountPercent: number): boolean {
  return checkDiscount(line, line.discount, highDiscountPercent).high;
}

export type PaymentError =
  | "amount_invalid"
  | "approval_ref_required"
  | "approval_ref_invalid"
  | "approval_ref_duplicate"
  | "multiple_cash"
  | "non_cash_exceeds_total"
  | "cash_change_too_large"
  | "too_many";

// Kembalian tunai di atas ini hampir pasti salah ketik atau isi scan yang
// nyasar ke kolom nominal (Rp200.000.000 untuk belanja Rp150.000).
export const MAX_CASH_CHANGE = 1_000_000;

// Sama dengan server (pos_sync_service.go + invCheckText): kontrol C0, DEL,
// dan C1 ditolak; surrogate tunggal bukan UTF-8 sah.
const CONTROL_CHARS = /[\u0000-\u001f\u007f-\u009f]/;
const LONE_SURROGATE = /[\ud800-\udbff](?![\udc00-\udfff])|(?<![\ud800-\udbff])[\udc00-\udfff]/;
// Set whitespace Go unicode.IsSpace (dipakai strings.TrimSpace). Berbeda dari
// String.prototype.trim: U+0085 termasuk, U+FEFF tidak.
const GO_SPACE = "\\t\\n\\v\\f\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000";
const GO_TRIM = new RegExp(`^[${GO_SPACE}]+|[${GO_SPACE}]+$`, "g");

export function goTrimSpace(s: string): string {
  return s.replace(GO_TRIM, "");
}

export function normalizeApprovalRef(raw: string | undefined): string {
  return goTrimSpace(raw ?? "");
}

export function utf8Length(s: string): number {
  return new TextEncoder().encode(s).length;
}

function entryError(entry: PaymentEntry): PaymentError | null {
  if (!Number.isInteger(entry.amount) || entry.amount < 1 || entry.amount > MAX_PAYMENT_AMOUNT) {
    return "amount_invalid";
  }
  if (entry.method !== "CASH") {
    const ref = normalizeApprovalRef(entry.approvalRef);
    if (!ref) return "approval_ref_required";
    // Server menghitung panjang dalam byte UTF-8, bukan karakter.
    if (utf8Length(ref) > MAX_APPROVAL_REF || CONTROL_CHARS.test(ref) || LONE_SURROGATE.test(ref)) {
      return "approval_ref_invalid";
    }
  }
  return null;
}

export interface PaymentSummary {
  total: number;
  paid: number;
  cash: number;
  nonCash: number;
  /** Sisa yang belum dibayar (0 kalau sudah cukup). */
  remaining: number;
  /** Kembalian tunai. */
  change: number;
  complete: boolean;
}

export function summarizePayments(total: number, payments: readonly PaymentEntry[]): PaymentSummary {
  let cash = 0;
  let nonCash = 0;
  for (const p of payments) {
    if (p.method === "CASH") cash += p.amount;
    else nonCash += p.amount;
  }
  const paid = cash + nonCash;
  return {
    total,
    paid,
    cash,
    nonCash,
    remaining: Math.max(0, total - paid),
    change: Math.max(0, paid - total),
    complete: paid >= total && nonCash <= total,
  };
}

// validateNewPayment memeriksa satu pembayaran baru terhadap yang sudah ada.
export function validateNewPayment(
  total: number,
  existing: readonly PaymentEntry[],
  entry: PaymentEntry,
): PaymentError | null {
  if (existing.length >= MAX_PAYMENTS) return "too_many";
  const err = entryError(entry);
  if (err) return err;
  const nonCash = existing.filter((p) => p.method !== "CASH").reduce((s, p) => s + p.amount, 0);
  if (entry.method === "CASH") {
    if (existing.some((p) => p.method === "CASH")) return "multiple_cash";
    if (entry.amount - Math.max(0, total - nonCash) > MAX_CASH_CHANGE) return "cash_change_too_large";
    return null;
  }
  const ref = normalizeApprovalRef(entry.approvalRef);
  if (existing.some((p) => p.method === entry.method && normalizeApprovalRef(p.approvalRef) === ref)) {
    return "approval_ref_duplicate";
  }
  if (nonCash + entry.amount > total) return "non_cash_exceeds_total";
  return null;
}

// validatePayments memeriksa daftar lengkap sebelum transaksi dicatat.
export function validatePayments(total: number, payments: readonly PaymentEntry[]): PaymentError | "insufficient" | null {
  const accepted: PaymentEntry[] = [];
  for (const p of payments) {
    const err = validateNewPayment(total, accepted, p);
    if (err) return err;
    accepted.push(p);
  }
  return summarizePayments(total, payments).paid >= total ? null : "insufficient";
}

// Pecahan uang tunai yang umum, untuk tombol cepat "uang pas" dan pembulatan ke atas.
const CASH_STEPS = [5_000, 10_000, 20_000, 50_000, 100_000];

export function quickCashAmounts(remaining: number): number[] {
  if (remaining <= 0) return [];
  const out = new Set<number>([remaining]);
  for (const step of CASH_STEPS) {
    const rounded = Math.ceil(remaining / step) * step;
    if (rounded > remaining) out.add(rounded);
    if (out.size >= 4) break;
  }
  return [...out].sort((a, b) => a - b);
}
