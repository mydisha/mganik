// @vitest-environment node
import { describe, expect, it } from "vitest";
import { bytesToHex, CanonicalError, canonicalBytes, canonicalize, importSigningKey, signCanonical } from "./canonical";

// Vektor resmi dari apps/api/internal/domain/pos_canonical_test.go (dihasilkan
// kode referensi JS di OpenAPI). Teks JSON mentahnya disalin byte per byte:
// \u0001 dan \" adalah escape JSON, U+2028 dan U+FFFF adalah karakter asli.
const jsVectorRaw =
  '{"b":1,"signature":"zz","a":"x\\u0001\\"/é\\n ","c":[3,{"z":null,"y":true}],' +
  '"€":0,"ä":-0,"😀":"emoji","￿":false}';
const jsVectorCanonicalHex =
  "7b2261223a22785c75303030315c222fc3a95c6ee280a8222c2262223a312c2263223a5b332c7b2279223a747275652c227a223a6e756c6c7d5d2c22c3a4223a302c22e282ac223a302c22f09f9880223a22656d6f6a69222c22efbfbf223a66616c73657d";
const jsVectorKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f";
const jsVectorSignature = "62d9ac8aa45ca99d91fc310b67e46875efda67661870f7d0464a4a5f3767f8dc";
const jsVectorPayloadHash = "cc0c89ab577a35e695a2e5d7e41276099e75941bb7d799c23fb3384ba6d5953f";

const jsLoneRaw = '{"note":"a\\ud83db","signature":"zz","\\udc00k":"😀x\\udfff","b":"/"}';
const jsLoneCanonicalHex =
  "7b2262223a222f222c226e6f7465223a22615c756438336462222c225c75646330306b223a22f09f9880785c7564666666227d";
const jsLoneSignature = "c319caa0e2c10e2b9308d4794c08ece3b4a63a1b131a77a30a733d0b7f8c07fa";

describe("kanonisasi transaksi POS", () => {
  it("menghasilkan byte, HMAC, dan payload hash yang sama dengan vektor backend", async () => {
    const txn = JSON.parse(jsVectorRaw) as Record<string, unknown>;
    const bytes = canonicalBytes(txn);
    expect(bytesToHex(bytes)).toBe(jsVectorCanonicalHex);

    const key = await importSigningKey(jsVectorKeyHex);
    expect(await signCanonical(key, txn)).toBe(jsVectorSignature);

    const hash = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes as Uint8Array<ArrayBuffer>));
    expect(bytesToHex(hash)).toBe(jsVectorPayloadHash);
  });

  it("menulis surrogate tunggal sebagai \\udxxx dan mengurutkan key per unit UTF-16", async () => {
    const txn = JSON.parse(jsLoneRaw) as Record<string, unknown>;
    expect(bytesToHex(canonicalBytes(txn))).toBe(jsLoneCanonicalHex);
    const key = await importSigningKey(jsVectorKeyHex);
    expect(await signCanonical(key, txn)).toBe(jsLoneSignature);
  });

  it("tidak bergantung urutan key dan hanya membuang signature tingkat atas", () => {
    const a = canonicalize({ z: [1, 2], a: { y: "é", x: null } });
    expect(a).toBe('{"a":{"x":null,"y":"é"},"z":[1,2]}');
    expect(new TextDecoder().decode(canonicalBytes({ x: { signature: 1 }, signature: "buang" }))).toBe(
      '{"x":{"signature":1}}',
    );
  });

  it("mengurutkan key menurut unit kode UTF-16, bukan code point", () => {
    // U+1F600 (surrogate D83D) harus sebelum U+FFFF; "B" sebelum "a".
    expect(canonicalize({ "￿": 1, "😀": 2, a: 3, B: 4 })).toBe('{"B":4,"a":3,"😀":2,"￿":1}');
  });

  it("menerima batas bilangan bulat aman dan menolak pecahan, eksponen, atau angka terlalu besar", () => {
    expect(canonicalize({ a: 9007199254740991, b: -9007199254740991, c: -0 })).toBe(
      '{"a":9007199254740991,"b":-9007199254740991,"c":0}',
    );
    expect(() => canonicalize({ a: 9007199254740992 })).toThrow(CanonicalError);
    expect(() => canonicalize({ a: 1.5 })).toThrow(CanonicalError);
    expect(() => canonicalize({ a: Number.NaN })).toThrow(CanonicalError);
    expect(() => canonicalize({ a: undefined })).toThrow(CanonicalError);
  });

  it("menandatangani transaksi nyata secara deterministik dalam hex huruf kecil", async () => {
    const key = await importSigningKey(jsVectorKeyHex);
    const txn = {
      client_txn_id: "01890a5d-ac96-774b-bcce-b302099a8057",
      device_seq: 1,
      cashier_id: "6b0e1a52-2f1d-4cf6-9a35-4b4c2a5a9d11",
      occurred_at: "2026-11-07T03:02:00.000Z",
      price_list_version: 3,
      total: 150000,
      lines: [{ product_id: "1d5c2d64-6f0b-4bde-8e1f-3f1c6e0e6a10", qty: 1, unit_price: 150000, discount: 0 }],
      payments: [{ method: "CASH", amount: 200000 }],
    };
    const sig = await signCanonical(key, txn);
    expect(sig).toMatch(/^[0-9a-f]{64}$/);
    expect(await signCanonical(key, { ...txn, signature: sig })).toBe(sig);
  });
});
