// Data uji bersama untuk test kasir (tidak diimpor kode produksi).
import { defaultSyncMeta, type PosBootstrap, type PosDb } from "./db";
import { applyBootstrap } from "./device";
import { importSigningKey } from "./lib/canonical";

export const TEST_KEY_HEX = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f";
export const TEST_TOKEN = "pos_" + "A".repeat(43);

export const PRODUCT_SERUM = "1d5c2d64-6f0b-4bde-8e1f-3f1c6e0e6a10";
export const PRODUCT_TONER = "2e6d3e75-7a1c-4cef-9f20-401d7f1f7b21";
export const PRODUCT_NOPRICE = "3f7e4f86-8b2d-4d00-a031-512e80208c32";
export const CASHIER_ID = "6b0e1a52-2f1d-4cf6-9a35-4b4c2a5a9d11";
export const SERIAL_A = "7K9M2P4Q8R1S0T3V";
export const SERIAL_B = "8K9M2P4Q8R1S0T3V";

export function makeBootstrap(over: Partial<PosBootstrap> = {}): PosBootstrap {
  return {
    server_time: "2026-11-07T03:00:00.000Z",
    device: {
      id: "9a0f2c11-1111-4a4a-8b8b-0c0c0c0c0c0c",
      location_id: "5b5b5b5b-2222-4c4c-9d9d-1e1e1e1e1e1e",
      location_code: "TKO-JKT-01",
      location_name: "Toko Kemang",
      name: "Kasir depan",
      status: "ACTIVE",
      last_seen_at: null,
      last_acked_seq: 0,
      provisioned_by: null,
      revoked_at: null,
      created_at: "2026-11-01T00:00:00.000Z",
    },
    location: {
      id: "5b5b5b5b-2222-4c4c-9d9d-1e1e1e1e1e1e",
      code: "TKO-JKT-01",
      name: "Toko Kemang",
      timezone: "Asia/Jakarta",
      sync_state: "ONLINE",
    },
    limits: {
      offline_limit_seconds: 86_400,
      heartbeat_interval_seconds: 30,
      heartbeat_timeout_seconds: 120,
      clock_skew_tolerance_seconds: 300,
      sync_max_batch: 500,
      sync_max_body_bytes: 5 * 1024 * 1024,
      max_serials_per_txn: 50,
      high_discount_percent: 20,
      offline_value_limit: 5_000_000,
    },
    products: [
      { id: PRODUCT_SERUM, sku: "SRM-01", gtin: "8991234567890", name: "Serum Niacinamide 30 ml", uom: "pcs" },
      { id: PRODUCT_TONER, sku: "TNR-02", gtin: null, name: "Toner Centella 100 ml", uom: "pcs" },
      { id: PRODUCT_NOPRICE, sku: "BLM-03", gtin: "8990000000001", name: "Produk tanpa harga", uom: "pcs" },
    ],
    price_list: {
      id: "7c7c7c7c-3333-4d4d-8e8e-2f2f2f2f2f2f",
      code: "POS-DEFAULT",
      version: 3,
      location_id: null,
      currency: "IDR",
      valid_from: "2026-11-01T00:00:00.000Z",
      valid_to: null,
      items: [
        { product_id: PRODUCT_SERUM, price: 150_000 },
        { product_id: PRODUCT_TONER, price: 85_000 },
      ],
    },
    stock: [
      { product_id: PRODUCT_SERUM, physical_qty: 5, reserved_qty: 1, quarantine_qty: 0, available_qty: 4 },
      { product_id: PRODUCT_TONER, physical_qty: 2, reserved_qty: 0, quarantine_qty: 0, available_qty: 2 },
    ],
    cashiers: [{ id: CASHIER_ID, name: "Rina", pos_pin_hash: "$argon2id$v=19$m=8,t=1,p=1$c2FsdHNhbHQ$AAAAAAAAAAAAAAAAAAAAAA" }],
    serials: { items: [{ code: SERIAL_A, product_id: PRODUCT_SERUM }], next_cursor: null },
    ...over,
  };
}

export async function seedDevice(db: PosDb, bootstrap: PosBootstrap = makeBootstrap(), now = Date.now()): Promise<void> {
  const signingKey = await importSigningKey(TEST_KEY_HEX);
  await db.meta.bulkPut([
    {
      key: "device",
      token: TEST_TOKEN,
      signingKey,
      deviceId: bootstrap.device.id,
      deviceName: bootstrap.device.name,
      locationId: bootstrap.location.id,
      locationCode: bootstrap.location.code,
      locationName: bootstrap.location.name,
      timezone: bootstrap.location.timezone,
      provisionedAt: now,
      persisted: true,
    },
    { key: "seq", value: 0 },
    defaultSyncMeta(),
  ]);
  await applyBootstrap(db, bootstrap, bootstrap.serials.items, now);
}
