// @vitest-environment node
import "fake-indexeddb/auto";
import { argon2id } from "hash-wasm";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { addProductToCart, addUnit, mutateCart, readCart, resolveScan, searchProducts } from "./cart-store";
import { countUnacked, PosDb } from "./db";
import { applyBootstrap, extractCredentials, isSigningKeyFormat, isTokenFormat, wipeDevice } from "./device";
import { importSigningKey, signCanonical } from "./lib/canonical";
import { PIN_LOCK_MS, verifyCashierPin } from "./pin";
import { recordSale, SaleError, signPendingRows, type SaleInput } from "./sale";
import {
  CASHIER_ID,
  makeBootstrap,
  PRODUCT_SERUM,
  PRODUCT_TONER,
  SERIAL_A,
  SERIAL_B,
  seedDevice,
  TEST_KEY_HEX,
} from "./test-fixtures";

let dbName: string;
let db: PosDb;
let counter = 0;

beforeEach(async () => {
  dbName = `pos-test-${++counter}-${Math.random()}`;
  db = new PosDb(dbName);
  await seedDevice(db);
});

afterEach(async () => {
  db.close();
});

const sale = (over: Partial<SaleInput> = {}): SaleInput => ({
  lines: [
    { productId: PRODUCT_SERUM, name: "Serum", sku: "SRM-01", qty: 1, unitPrice: 150_000, discount: 0, serials: [] },
  ],
  payments: [{ method: "CASH", amount: 200_000 }],
  cashierId: CASHIER_ID,
  cashierName: "Rina",
  priceListVersion: 3,
  ...over,
});

describe("device_seq dan outbox", () => {
  it("monoton dan unik saat dua tab mencatat penjualan bersamaan", async () => {
    // Dua instance Dexie ke database yang sama = dua tab.
    const tabA = new PosDb(dbName);
    const tabB = new PosDb(dbName);
    const jobs: Promise<unknown>[] = [];
    for (let i = 0; i < 15; i++) {
      jobs.push(recordSale(tabA, sale()));
      jobs.push(recordSale(tabB, sale()));
    }
    await Promise.all(jobs);
    const rows = await db.outbox.orderBy("deviceSeq").toArray();
    expect(rows.map((r) => r.deviceSeq)).toEqual(Array.from({ length: 30 }, (_, i) => i + 1));
    expect(new Set(rows.map((r) => r.clientTxnId)).size).toBe(30);
    expect((await db.getMeta("seq"))?.value).toBe(30);
    // Setiap baris sudah ditandatangani dengan kunci perangkat.
    const key = await importSigningKey(TEST_KEY_HEX);
    for (const r of rows) {
      expect(r.state).toBe("pending");
      expect(r.signature).toBe(await signCanonical(key, r.payload as unknown as Record<string, unknown>));
      expect(r.payload.device_seq).toBe(r.deviceSeq);
    }
    tabA.close();
    tabB.close();
  });

  it("mencatat payload sesuai kontrak, mengurangi stok lokal, menandai serial, dan mengosongkan keranjang", async () => {
    await mutateCart(db, (c) => addUnit(c, { id: PRODUCT_SERUM, name: "Serum", sku: "SRM-01" }, 150_000, SERIAL_A, 50));
    const cart = await readCart(db);
    const row = await recordSale(
      db,
      sale({
        lines: cart.lines.map((l) => ({ ...l, discount: 10_000 })),
        payments: [
          { method: "QRIS", amount: 40_000, approvalRef: "  QR-778 " },
          { method: "CASH", amount: 100_000 },
        ],
      }),
      Date.UTC(2026, 10, 7, 3, 2),
    );
    expect(row.payload).toEqual({
      client_txn_id: row.clientTxnId,
      device_seq: 1,
      cashier_id: CASHIER_ID,
      occurred_at: "2026-11-07T03:02:00.000Z",
      price_list_version: 3,
      total: 140_000,
      lines: [{ product_id: PRODUCT_SERUM, qty: 1, unit_price: 150_000, discount: 10_000, serials: [SERIAL_A] }],
      payments: [
        { method: "QRIS", amount: 40_000, approval_ref: "QR-778" },
        { method: "CASH", amount: 100_000 },
      ],
    });
    expect((await db.stock.get(PRODUCT_SERUM))?.localSold).toBe(1);
    expect((await db.serials.get(SERIAL_A))?.soldLocally).toBe(true);
    expect((await readCart(db)).lines).toEqual([]);
  });

  it("menolak penjualan yang melanggar aturan kontrak sebelum mengambil nomor urut", async () => {
    await expect(recordSale(db, sale({ priceListVersion: null }))).rejects.toThrow(SaleError);
    await expect(
      recordSale(db, sale({ payments: [{ method: "EDC", amount: 150_001, approvalRef: "E1" }] })),
    ).rejects.toMatchObject({ code: "non_cash_exceeds_total" });
    await expect(recordSale(db, sale({ payments: [{ method: "CASH", amount: 1_000 }] }))).rejects.toMatchObject({
      code: "insufficient",
    });
    await expect(recordSale(db, sale({ lines: [] }))).rejects.toMatchObject({ code: "empty_cart" });
    expect(await db.outbox.count()).toBe(0);
    expect((await db.getMeta("seq"))?.value).toBe(0);
  });

  it("menandatangani baris yang tertinggal tanpa signature (tab mati sebelum sempat)", async () => {
    const row = await recordSale(db, sale());
    await db.outbox.put({ ...row, signature: null, state: "unsigned" });
    expect(await signPendingRows(db)).toBe(1);
    expect(await signPendingRows(db)).toBe(0);
    expect((await db.outbox.get(row.clientTxnId))?.state).toBe("pending");
  });

  it("menghitung pending_count: semua yang belum tersimpan di server", async () => {
    const a = await recordSale(db, sale());
    const b = await recordSale(db, sale());
    const c = await recordSale(db, sale());
    await db.outbox.put({
      ...b,
      state: "rejected",
      rejection: { reason: "cashier_not_allowed", message: "x", errors: [], stored: true, at: 0 },
    });
    await db.outbox.put({
      ...c,
      state: "rejected",
      rejection: { reason: "invalid_signature", message: "x", errors: [], stored: false, at: 0 },
    });
    expect(a.state).toBe("pending");
    expect(await countUnacked(db)).toBe(2);
  });

  it("refresh bootstrap mempertahankan pengurangan stok untuk penjualan yang belum terkirim", async () => {
    await recordSale(db, sale({ lines: [{ ...sale().lines[0]!, qty: 1 }] }));
    await applyBootstrap(db, makeBootstrap(), [], Date.now());
    const stock = await db.stock.get(PRODUCT_SERUM);
    expect(stock).toMatchObject({ availableQty: 4, localSold: 1 });
  });

  it("menghapus semua data lokal saat perangkat dikeluarkan", async () => {
    await recordSale(db, sale());
    await wipeDevice(db);
    expect(await db.outbox.count()).toBe(0);
    expect(await db.getMeta("device")).toBeUndefined();
  });
});

describe("keranjang", () => {
  it("bertahan setelah database dibuka ulang (refresh/crash)", async () => {
    await mutateCart(db, (c) => addUnit(c, { id: PRODUCT_SERUM, name: "Serum", sku: "SRM-01" }, 150_000, null, 50));
    await mutateCart(db, (c) => addUnit(c, { id: PRODUCT_TONER, name: "Toner", sku: "TNR-02" }, 85_000, null, 50));
    db.close();
    const reopened = new PosDb(dbName);
    const cart = await readCart(reopened);
    expect(cart.lines.map((l) => [l.productId, l.qty])).toEqual([
      [PRODUCT_TONER, 1],
      [PRODUCT_SERUM, 1],
    ]);
    reopened.close();
    db = new PosDb(dbName);
  });

  it("menggabungkan scan produk yang sama dan menolak serial ganda", async () => {
    expect(await resolveScan(db, "8991234567890", "scanner", 50)).toMatchObject({ type: "added", serial: null });
    expect(await resolveScan(db, "srm-01", "manual", 50)).toMatchObject({ type: "added" });
    expect(await resolveScan(db, "/v/7k9m-2p4q-8r1s-0t3v", "scanner", 50)).toMatchObject({
      type: "added",
      serial: SERIAL_A,
    });
    expect(await resolveScan(db, `/v/${SERIAL_A}`, "scanner", 50)).toEqual({
      type: "rejected",
      error: "serial_duplicate",
    });
    const cart = await readCart(db);
    expect(cart.lines).toHaveLength(1);
    expect(cart.lines[0]).toMatchObject({ qty: 3, serials: [SERIAL_A] });
  });

  it("menahan serial yang belum terdaftar sampai produknya dipilih", async () => {
    expect(await resolveScan(db, `/v/${SERIAL_B}`, "scanner", 50)).toEqual({
      type: "serial-unknown",
      code: SERIAL_B,
    });
    expect((await readCart(db)).pendingSerial).toBe(SERIAL_B);
    const toner = (await db.products.get(PRODUCT_TONER))!;
    expect(await addProductToCart(db, toner, null, 50)).toMatchObject({ type: "added", serial: SERIAL_B });
    const cart = await readCart(db);
    expect(cart.pendingSerial).toBeNull();
    expect(cart.lines[0]).toMatchObject({ productId: PRODUCT_TONER, serials: [SERIAL_B] });
  });

  it("membedakan kode tak dikenal, produk tanpa harga, dan ketikan untuk pencarian", async () => {
    expect(await resolveScan(db, "0000000000000", "scanner", 50)).toEqual({ type: "not-found", value: "0000000000000" });
    expect(await resolveScan(db, "8990000000001", "scanner", 50)).toMatchObject({ type: "no-price" });
    expect(await resolveScan(db, "serum", "manual", 50)).toEqual({ type: "search", query: "serum" });
    expect(await resolveScan(db, "/v/XX", "scanner", 50)).toEqual({ type: "invalid-url" });
    expect((await searchProducts(db, "toner")).map((p) => p.id)).toEqual([PRODUCT_TONER]);
    expect((await searchProducts(db, "899123")).map((p) => p.id)).toEqual([PRODUCT_SERUM]);
  });

  it("membatasi jumlah serial per transaksi", async () => {
    expect(await resolveScan(db, `/v/${SERIAL_A}`, "scanner", 0)).toEqual({ type: "rejected", error: "serial_limit" });
  });
});

describe("setup perangkat", () => {
  it("memvalidasi format token dan signing key, dan mengambil keduanya dari satu tempelan", () => {
    const token = "pos_" + "aZ09_-".repeat(7) + "x";
    expect(isTokenFormat(token)).toBe(true);
    expect(isTokenFormat("pos_pendek")).toBe(false);
    expect(isSigningKeyFormat(TEST_KEY_HEX)).toBe(true);
    expect(isSigningKeyFormat(TEST_KEY_HEX.slice(1))).toBe(false);
    expect(extractCredentials(`Token: ${token}\nSigning key: ${TEST_KEY_HEX.toUpperCase()}`)).toEqual({
      token,
      signingKey: TEST_KEY_HEX,
    });
    expect(extractCredentials("tidak ada apa-apa")).toEqual({ token: null, signingKey: null });
    // Token demo seed (fixture.go DemoDeviceToken): demo-pos-<64 hex>.
    const demo = "demo-pos-" + "ab".repeat(32);
    expect(isTokenFormat(demo)).toBe(true);
    expect(extractCredentials(`${TEST_KEY_HEX}\n${demo}`)).toEqual({ token: demo, signingKey: TEST_KEY_HEX });
    expect(isTokenFormat("token dengan spasi di dalamnya")).toBe(false);
    expect(isTokenFormat("x".repeat(201))).toBe(false);
    expect(isTokenFormat("pendek")).toBe(false);
  });
});

describe("PIN kasir", () => {
  async function setPin(pin: string) {
    const hash = await argon2id({
      password: pin,
      salt: crypto.getRandomValues(new Uint8Array(16)),
      iterations: 1,
      memorySize: 64,
      parallelism: 1,
      hashLength: 32,
      outputType: "encoded",
    });
    await db.cashiers.put({ id: CASHIER_ID, name: "Rina", pinHash: hash });
  }

  it("memverifikasi PIN lokal dengan argon2id dan membuka sesi", async () => {
    await setPin("135790");
    expect(await verifyCashierPin(db, CASHIER_ID, "135790")).toEqual({
      ok: true,
      cashierId: CASHIER_ID,
      cashierName: "Rina",
    });
    expect((await db.getMeta("session"))?.cashierId).toBe(CASHIER_ID);
  });

  it("mengunci login setelah 5 kali salah, bertahan setelah refresh, lalu terbuka lagi", async () => {
    await setPin("135790");
    const t0 = 1_000_000;
    for (let i = 1; i <= 4; i++) {
      expect(await verifyCashierPin(db, CASHIER_ID, "000000", { now: t0 + i })).toEqual({
        ok: false,
        reason: "wrong",
        remaining: 5 - i,
      });
    }
    expect(await verifyCashierPin(db, CASHIER_ID, "000000", { now: t0 + 5 })).toEqual({
      ok: false,
      reason: "locked",
      until: t0 + 5 + PIN_LOCK_MS,
    });
    // PIN benar pun ditolak selama terkunci, juga dari "tab" lain.
    const other = new PosDb(dbName);
    expect(await verifyCashierPin(other, CASHIER_ID, "135790", { now: t0 + 60_000 })).toMatchObject({
      ok: false,
      reason: "locked",
    });
    other.close();
    expect(await verifyCashierPin(db, CASHIER_ID, "135790", { now: t0 + 5 + PIN_LOCK_MS })).toMatchObject({ ok: true });
    // Hitungan mulai dari nol lagi.
    expect(await verifyCashierPin(db, CASHIER_ID, "000000", { now: t0 + PIN_LOCK_MS + 10 })).toMatchObject({
      remaining: 4,
    });
  });

  it("PIN benar tidak membuka kunci yang dipasang tab lain selama verifikasi berjalan", async () => {
    await setPin("135790");
    const now = 5_000_000;
    const lockDuring = async () => {
      await db.meta.put({ key: "pinLock", failures: 0, lockedUntil: now + 60_000 });
      return true;
    };
    expect(await verifyCashierPin(db, CASHIER_ID, "135790", { now, verify: lockDuring })).toEqual({
      ok: false,
      reason: "locked",
      until: now + 60_000,
    });
    expect(await db.getMeta("session")).toBeUndefined();
  });

  it("menolak format PIN yang salah tanpa menghitung percobaan, dan hash rusak", async () => {
    expect(await verifyCashierPin(db, CASHIER_ID, "12345")).toEqual({ ok: false, reason: "format" });
    expect(await verifyCashierPin(db, CASHIER_ID, "12345a")).toEqual({ ok: false, reason: "format" });
    expect(await verifyCashierPin(db, "tidak-ada", "123456")).toEqual({ ok: false, reason: "unknown_cashier" });
    await db.cashiers.put({ id: CASHIER_ID, name: "Rina", pinHash: "bukan-hash" });
    expect(await verifyCashierPin(db, CASHIER_ID, "123456")).toEqual({ ok: false, reason: "hash_invalid" });
    expect((await db.getMeta("pinLock"))?.failures ?? 0).toBe(0);
  });

  it("tidak menghitung percobaan kalau mesin argon2 gagal jalan (mis. WebAssembly diblokir CSP)", async () => {
    await setPin("135790");
    const broken = async () => {
      throw new WebAssembly.CompileError("blocked by CSP");
    };
    expect(await verifyCashierPin(db, CASHIER_ID, "135790", { verify: broken })).toEqual({
      ok: false,
      reason: "engine_unavailable",
    });
    expect((await db.getMeta("pinLock"))?.failures ?? 0).toBe(0);
  });
});
