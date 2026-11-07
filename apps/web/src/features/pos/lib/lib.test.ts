// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  cartTotals,
  checkDiscount,
  goTrimSpace,
  quickCashAmounts,
  summarizePayments,
  validateNewPayment,
  validatePayments,
  validateQty,
  type CartLine,
} from "./cart-math";
import { formatClock, formatRupiah, formatTxnNumber } from "./format";
import { classifyEntry, isValidSerialCode, looksLikeBarcode, normalizeSerialCode, parseScan } from "./scan";
import { uuidv7 } from "./uuid";

const line = (over: Partial<CartLine> = {}): CartLine => ({
  productId: "p1",
  name: "Serum",
  sku: "SRM-01",
  qty: 2,
  unitPrice: 75_000,
  discount: 0,
  serials: [],
  ...over,
});

describe("format rupiah dan waktu", () => {
  it("menulis Rp tanpa spasi dengan titik ribuan", () => {
    expect(formatRupiah(150_000)).toBe("Rp150.000");
    expect(formatRupiah(0)).toBe("Rp0");
    expect(formatRupiah(1_234_567_890)).toBe("Rp1.234.567.890");
    expect(formatRupiah(-5_000)).toBe("-Rp5.000");
  });

  it("menampilkan jam di zona toko dengan akhiran zona", () => {
    const at = Date.UTC(2026, 10, 7, 3, 42);
    expect(formatClock(at, "Asia/Jakarta")).toBe("10.42 WIB");
    expect(formatClock(at, "Asia/Makassar")).toBe("11.42 WITA");
    expect(formatClock(at, "Asia/Jayapura")).toBe("12.42 WIT");
    expect(formatClock(at, "Bukan/Zona")).toBe("10.42 WIB");
  });

  it("menulis nomor transaksi enam digit", () => {
    expect(formatTxnNumber(42)).toBe("000042");
  });
});

describe("parser scan", () => {
  it("mengenali URL QR hologram dan menormalkan kodenya seperti backend", () => {
    const app = "https://kasir.example.test";
    expect(parseScan(`${app}/v/7k9m-2p4q-8r1s-0t3v`, app)).toEqual({ kind: "serial-url", code: "7K9M2P4Q8R1S0T3V" });
    // I/L → 1, O → 0, spasi dan strip dibuang, huruf kecil dinaikkan; query diabaikan.
    expect(parseScan("http://localhost:5173/v/ilo0%20abcd-efgh-jkmn?src=qr", "http://localhost:5173")).toEqual({
      kind: "serial-url",
      code: "1100ABCDEFGHJKMN",
    });
    // Path relatif diterima walau origin tidak diketahui.
    expect(parseScan("/v/7K9M2P4Q8R1S0T3V", undefined)).toEqual({ kind: "serial-url", code: "7K9M2P4Q8R1S0T3V" });
  });

  it("menolak URL selain origin aplikasi, protokol selain http(s), dan path yang tidak persis /v/<kode>", () => {
    const app = "https://kasir.example.test";
    expect(parseScan("https://palsu.example/v/7K9M2P4Q8R1S0T3V", app).kind).toBe("invalid-url");
    expect(parseScan("//palsu.example/v/7K9M2P4Q8R1S0T3V", app).kind).toBe("invalid-url");
    expect(parseScan("javascript:alert(1)//v/7K9M2P4Q8R1S0T3V", app).kind).toBe("invalid-url");
    expect(parseScan(`${app}/x/v/7K9M2P4Q8R1S0T3V`, app).kind).toBe("invalid-url");
    expect(parseScan(`${app}/v/7K9M2P4Q8R1S0T3V/extra`, app).kind).toBe("invalid-url");
    expect(parseScan(`${app}/v/PENDEK`, app).kind).toBe("invalid-url");
    expect(parseScan(`${app}/v/%E0%A4%A`, app).kind).toBe("invalid-url");
    // Tanpa skema bukan URL: diperlakukan sebagai kode polos.
    expect(parseScan("mganik.id/v/7K9M2P4Q8R1S0T3V", app).kind).toBe("code");
  });

  it("meneruskan barcode, SKU, dan ketikan sebagai kode polos", () => {
    expect(parseScan(" 8991234567890 ")).toEqual({ kind: "code", value: "8991234567890" });
    expect(parseScan("SRM-01")).toEqual({ kind: "code", value: "SRM-01" });
    expect(parseScan("serum wajah")).toEqual({ kind: "code", value: "serum wajah" });
    expect(parseScan("   ")).toEqual({ kind: "empty" });
  });

  it("menormalkan dan memvalidasi kode serial", () => {
    expect(normalizeSerialCode(" 7k9m-2p4q-8r1s-0t3v ")).toBe("7K9M2P4Q8R1S0T3V");
    expect(isValidSerialCode("7K9M2P4Q8R1S0T3V")).toBe(true);
    // U bukan alfabet Crockford.
    expect(isValidSerialCode("7K9M2P4Q8R1S0T3U")).toBe(false);
    // Input lebih dari 64 byte tidak dipetakan (sama dengan backend).
    const long = "o".repeat(65);
    expect(normalizeSerialCode(long)).toBe(long);
    // Unicode tidak menjadi kode sah.
    expect(isValidSerialCode(normalizeSerialCode("ÉÉÉÉ"))).toBe(false);
  });

  it("mengenali barcode digit", () => {
    expect(looksLikeBarcode("8991234567890")).toBe(true);
    expect(looksLikeBarcode("12345678")).toBe(true);
    expect(looksLikeBarcode("SRM-01")).toBe(false);
    expect(looksLikeBarcode("123")).toBe(false);
  });

  it("membedakan burst scanner dari ketikan manual", () => {
    expect(classifyEntry([0, 8, 15, 22, 30, 37])).toBe("scanner");
    // Satu jeda 30 ms atau lebih berarti manusia.
    expect(classifyEntry([0, 8, 15, 45, 50, 55])).toBe("manual");
    // Enter yang ditekan lama setelah karakter terakhir: manual.
    expect(classifyEntry([0, 5, 10, 15, 400])).toBe("manual");
    // Terlalu pendek untuk dianggap scan.
    expect(classifyEntry([0, 5, 10])).toBe("manual");
  });
});

describe("aritmetika keranjang", () => {
  it("menghitung subtotal, diskon, total, dan jumlah unit", () => {
    expect(cartTotals([line(), line({ productId: "p2", qty: 1, unitPrice: 20_000, discount: 5_000 })])).toEqual({
      subtotal: 170_000,
      discount: 5_000,
      total: 165_000,
      units: 3,
    });
    expect(cartTotals([])).toEqual({ subtotal: 0, discount: 0, total: 0, units: 0 });
  });

  it("membatasi diskon sampai subtotal baris dan menandai diskon tinggi", () => {
    expect(checkDiscount(line(), 150_000, 20)).toEqual({ error: null, high: true });
    expect(checkDiscount(line(), 150_001, 20).error).toBe("exceeds_subtotal");
    expect(checkDiscount(line(), -1, 20).error).toBe("negative");
    expect(checkDiscount(line(), 10.5, 20).error).toBe("not_integer");
    // Tepat di batas 20% belum tinggi; satu rupiah di atasnya sudah.
    expect(checkDiscount(line(), 30_000, 20)).toEqual({ error: null, high: false });
    expect(checkDiscount(line(), 30_001, 20)).toEqual({ error: null, high: true });
    // Harga nol: diskon nol sah, tidak dianggap tinggi.
    expect(checkDiscount(line({ unitPrice: 0 }), 0, 20)).toEqual({ error: null, high: false });
  });

  it("memvalidasi qty terhadap batas dan jumlah serial", () => {
    expect(validateQty(1)).toBeNull();
    expect(validateQty(0)).toBe("below_min");
    expect(validateQty(1001)).toBe("above_max");
    expect(validateQty(1.5)).toBe("not_integer");
    expect(validateQty(1, 2)).toBe("below_serials");
  });
});

describe("pembayaran", () => {
  it("menghitung kembalian hanya dari tunai", () => {
    const s = summarizePayments(165_000, [
      { method: "QRIS", amount: 65_000, approvalRef: "QR-1" },
      { method: "CASH", amount: 200_000 },
    ]);
    expect(s).toMatchObject({ paid: 265_000, remaining: 0, change: 100_000, complete: true });
    expect(summarizePayments(165_000, [{ method: "CASH", amount: 100_000 }])).toMatchObject({
      remaining: 65_000,
      change: 0,
      complete: false,
    });
  });

  it("menolak non-tunai yang melebihi total di klien", () => {
    expect(validateNewPayment(100_000, [], { method: "EDC", amount: 100_001, approvalRef: "A1" })).toBe(
      "non_cash_exceeds_total",
    );
    expect(
      validateNewPayment(100_000, [{ method: "QRIS", amount: 60_000, approvalRef: "Q" }], {
        method: "EDC",
        amount: 50_000,
        approvalRef: "E",
      }),
    ).toBe("non_cash_exceeds_total");
    expect(validateNewPayment(100_000, [], { method: "EDC", amount: 100_000, approvalRef: "A1" })).toBeNull();
  });

  it("mewajibkan approval_ref untuk EDC/QRIS dan menolak karakter kontrol", () => {
    expect(validateNewPayment(10_000, [], { method: "EDC", amount: 10_000 })).toBe("approval_ref_required");
    expect(validateNewPayment(10_000, [], { method: "QRIS", amount: 10_000, approvalRef: "   " })).toBe(
      "approval_ref_required",
    );
    expect(validateNewPayment(10_000, [], { method: "QRIS", amount: 10_000, approvalRef: "a\nb" })).toBe(
      "approval_ref_invalid",
    );
    expect(validateNewPayment(10_000, [], { method: "QRIS", amount: 10_000, approvalRef: "x".repeat(65) })).toBe(
      "approval_ref_invalid",
    );
  });

  it("menyamakan validasi approval_ref dengan server: byte UTF-8, C0/C1, trim Go, duplikat", () => {
    const edc = (approvalRef: string, amount = 1_000) => ({ method: "EDC" as const, amount, approvalRef });
    // 1) Panjang dalam byte: 32 × "é" = 64 byte lolos, 33 × "é" = 66 byte ditolak walau hanya 33 karakter.
    expect(validateNewPayment(10_000, [], edc("é".repeat(32)))).toBeNull();
    expect(validateNewPayment(10_000, [], edc("é".repeat(33)))).toBe("approval_ref_invalid");
    // 2) Kontrol C0, DEL, dan C1 ditolak.
    for (const bad of ["A\u0001B", "A\u007fB", "A\u0085B", "A\u009fB"]) {
      expect(validateNewPayment(10_000, [], edc(bad))).toBe("approval_ref_invalid");
    }
    // 3) Trim memakai set whitespace Go (NBSP, ideographic space) dan hasilnya yang dikirim.
    expect(goTrimSpace(" 　REF-1  ")).toBe("REF-1");
    expect(goTrimSpace("﻿REF")).toBe("﻿REF");
    expect(validateNewPayment(10_000, [], edc("  　"))).toBe("approval_ref_required");
    // 4) Surrogate tunggal bukan UTF-8 sah.
    expect(validateNewPayment(10_000, [], edc("A\ud800"))).toBe("approval_ref_invalid");
    // 5) Pasangan (metode, ref) yang sama dua kali ditolak; metode lain dengan ref sama boleh.
    expect(validateNewPayment(10_000, [edc("R1")], edc(" R1 "))).toBe("approval_ref_duplicate");
    expect(validateNewPayment(10_000, [edc("R1")], { method: "QRIS", amount: 1_000, approvalRef: "R1" })).toBeNull();
  });

  it("menolak tunai dengan kembalian di atas Rp1.000.000", () => {
    expect(validateNewPayment(150_000, [], { method: "CASH", amount: 1_150_000 })).toBeNull();
    expect(validateNewPayment(150_000, [], { method: "CASH", amount: 1_150_001 })).toBe("cash_change_too_large");
    // Isi scan barcode yang nyasar ke kolom nominal.
    expect(validateNewPayment(150_000, [], { method: "CASH", amount: 8_991_234_567_890 })).toBe("cash_change_too_large");
    // Sisa setelah non-tunai yang dihitung.
    expect(
      validateNewPayment(150_000, [{ method: "QRIS", amount: 100_000, approvalRef: "Q" }], { method: "CASH", amount: 1_050_001 }),
    ).toBe("cash_change_too_large");
  });

  it("hanya mengizinkan satu pembayaran tunai dan maksimal sepuluh pembayaran", () => {
    expect(validateNewPayment(10_000, [{ method: "CASH", amount: 5_000 }], { method: "CASH", amount: 5_000 })).toBe(
      "multiple_cash",
    );
    const ten = Array.from({ length: 10 }, (_, i) => ({ method: "EDC" as const, amount: 1, approvalRef: `R${i}` }));
    expect(validateNewPayment(100, ten, { method: "CASH", amount: 1 })).toBe("too_many");
    expect(validateNewPayment(100, [], { method: "CASH", amount: 0 })).toBe("amount_invalid");
  });

  it("menolak daftar pembayaran yang kurang dan menerima total nol tanpa pembayaran", () => {
    expect(validatePayments(50_000, [{ method: "CASH", amount: 20_000 }])).toBe("insufficient");
    expect(validatePayments(0, [])).toBeNull();
  });

  it("menyarankan uang pas dan pembulatan ke atas", () => {
    expect(quickCashAmounts(137_500)).toEqual([137_500, 140_000, 150_000, 200_000]);
    expect(quickCashAmounts(100_000)).toEqual([100_000]);
    expect(quickCashAmounts(0)).toEqual([]);
  });
});

describe("uuidv7", () => {
  it("menyandikan waktu, versi 7, dan varian RFC", () => {
    const id = uuidv7(Date.UTC(2026, 10, 7), new Uint8Array(10).fill(0xff));
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(Number.parseInt(id.replace(/-/g, "").slice(0, 12), 16)).toBe(Date.UTC(2026, 10, 7));
    expect(uuidv7()).not.toBe(uuidv7());
  });
});
