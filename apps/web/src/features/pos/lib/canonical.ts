// Kanonisasi transaksi POS dan signature HMAC-SHA256, persis seperti kode
// referensi di packages/contracts/openapi.yaml (POST /pos/sync) dan vektor uji
// backend (apps/api/internal/domain/pos_canonical_test.go). Jangan "merapikan"
// fungsi ini: setiap byte yang berbeda membuat transaksi ditolak
// invalid_signature.

export class CanonicalError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "CanonicalError";
  }
}

// canonicalize: key diurutkan rekursif menurut unit kode UTF-16 (sort bawaan),
// tanpa spasi, string di-escape oleh JSON.stringify (surrogate tunggal menjadi
// \udxxx). Angka wajib bilangan bulat aman; selain itu ditolak sebelum
// ditandatangani karena server juga menolaknya.
export function canonicalize(value: unknown): string {
  if (Array.isArray(value)) return "[" + value.map(canonicalize).join(",") + "]";
  if (value !== null && typeof value === "object") {
    const obj = value as Record<string, unknown>;
    return (
      "{" +
      Object.keys(obj)
        .sort()
        .map((k) => JSON.stringify(k) + ":" + canonicalize(obj[k]))
        .join(",") +
      "}"
    );
  }
  if (typeof value === "number" && !Number.isSafeInteger(value)) {
    throw new CanonicalError(`angka ${value} bukan bilangan bulat aman`);
  }
  if (value === null || typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
    return JSON.stringify(value);
  }
  throw new CanonicalError(`tipe ${typeof value} tidak bisa dikanonisasi`);
}

// canonicalBytes membuang member tingkat atas `signature` (yang bersarang tetap).
export function canonicalBytes(txn: Record<string, unknown>): Uint8Array {
  const rest: Record<string, unknown> = { ...txn };
  delete rest.signature;
  return new TextEncoder().encode(canonicalize(rest));
}

export function bytesToHex(bytes: Uint8Array): string {
  let out = "";
  for (const b of bytes) out += b.toString(16).padStart(2, "0");
  return out;
}

export function hexToBytes(hex: string): Uint8Array<ArrayBuffer> {
  if (!/^(?:[0-9a-fA-F]{2})*$/.test(hex)) throw new CanonicalError("hex tidak valid");
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}

// importSigningKey membuat CryptoKey HMAC yang tidak bisa diekspor. Kunci ini
// yang disimpan di IndexedDB, bukan hex mentahnya.
export function importSigningKey(hex: string, subtle: SubtleCrypto = globalThis.crypto.subtle): Promise<CryptoKey> {
  return subtle.importKey("raw", hexToBytes(hex), { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
}

export async function signCanonical(
  key: CryptoKey,
  txn: Record<string, unknown>,
  subtle: SubtleCrypto = globalThis.crypto.subtle,
): Promise<string> {
  const bytes = canonicalBytes(txn);
  const sig = await subtle.sign("HMAC", key, bytes as Uint8Array<ArrayBuffer>);
  return bytesToHex(new Uint8Array(sig));
}
