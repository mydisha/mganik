// Parser input kolom scan: URL QR hologram (/v/<kode>), kode serial polos,
// barcode/SKU produk, atau ketikan bebas untuk pencarian. Normalisasi kode
// serial mengikuti backend (apps/api/internal/domain/serial_mint.go
// NormalizeCrockford): trim, huruf besar, buang spasi dan strip, I/L → 1, O → 0.

const SERIAL_PATTERN = /^[0-9A-HJKMNP-TV-Z]{16}$/;
const MAX_NORMALIZE_BYTES = 64;

export function normalizeSerialCode(raw: string): string {
  const s = raw.trim();
  // Backend tidak memetakan input lebih dari 64 byte (pasti tidak sah).
  if (new TextEncoder().encode(s).length > MAX_NORMALIZE_BYTES) return s;
  let out = "";
  for (const ch of s.toUpperCase()) {
    if (ch === " " || ch === "-") continue;
    if (ch === "I" || ch === "L") out += "1";
    else if (ch === "O") out += "0";
    else out += ch;
  }
  return out;
}

export function isValidSerialCode(code: string): boolean {
  return SERIAL_PATTERN.test(code);
}

export type ScanInput =
  | { kind: "empty" }
  /** URL hologram dengan kode yang formatnya sah. */
  | { kind: "serial-url"; code: string }
  /** URL hologram, tetapi kodenya bukan format serial. */
  | { kind: "invalid-url"; raw: string }
  /** Kode polos: barcode, SKU, atau kode serial tanpa URL. */
  | { kind: "code"; value: string };

// Path persis /v/<kode> (query/hash diabaikan). Kontrak dan kode belum punya
// daftar host QR resmi, jadi yang diterima: URL http(s) di origin aplikasi ini
// dan path relatif `/v/<kode>`.
const VERIFY_PATH = /^\/v\/([^/]+)$/;

function looksLikeUrl(s: string): boolean {
  // SKU boleh memuat ":" (mis. "ABC:12"), jadi hanya skema yang jelas URL.
  return /^[a-z][a-z0-9+.-]*:\/\//i.test(s) || /^(?:https?|javascript|data):/i.test(s) || s.startsWith("/");
}

export function parseScan(raw: string, appOrigin: string | undefined = globalThis.location?.origin): ScanInput {
  const text = raw.trim();
  if (!text) return { kind: "empty" };
  if (looksLikeUrl(text)) {
    let url: URL;
    try {
      url = new URL(text, appOrigin ?? "http://relative.invalid");
    } catch {
      return { kind: "invalid-url", raw: text };
    }
    const relative = text.startsWith("/") && !text.startsWith("//");
    const sameOrigin = appOrigin !== undefined && url.origin === appOrigin;
    if ((url.protocol !== "http:" && url.protocol !== "https:") || !(relative || sameOrigin)) {
      return { kind: "invalid-url", raw: text };
    }
    const m = VERIFY_PATH.exec(url.pathname);
    if (!m) return { kind: "invalid-url", raw: text };
    let segment = m[1]!;
    try {
      segment = decodeURIComponent(segment);
    } catch {
      return { kind: "invalid-url", raw: text };
    }
    const code = normalizeSerialCode(segment);
    return isValidSerialCode(code) ? { kind: "serial-url", code } : { kind: "invalid-url", raw: text };
  }
  return { kind: "code", value: text };
}

// Barcode produk (EAN-8, UPC-A, EAN-13, GTIN-14) hanya digit.
export function looksLikeBarcode(value: string): boolean {
  return /^\d{8}$|^\d{12,14}$/.test(value);
}

// Scanner HID mengetik seperti keyboard, tetapi sangat cepat: jeda antar
// karakter (termasuk Enter penutup) di bawah 30 ms. Ketikan manusia jauh lebih
// lambat. `times` adalah timestamp keydown setiap karakter lalu Enter.
export const SCANNER_MAX_GAP_MS = 30;
export const SCANNER_MIN_CHARS = 4;

export function classifyEntry(times: readonly number[], maxGapMs = SCANNER_MAX_GAP_MS): "scanner" | "manual" {
  // times = [t(char1), ..., t(charN), t(Enter)]
  if (times.length < SCANNER_MIN_CHARS + 1) return "manual";
  for (let i = 1; i < times.length; i++) {
    if (times[i]! - times[i - 1]! >= maxGapMs) return "manual";
  }
  return "scanner";
}
