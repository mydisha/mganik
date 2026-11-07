import type { GeoPoint } from "./api";

/**
 * Menormalkan nomor seluler Indonesia ke E.164 (+628…), sama dengan aturan
 * server: 08…, 628…, +628…, atau 8…, dengan spasi, titik, tanda hubung, atau
 * kurung sebagai pemisah; 9-12 digit setelah 62 dan diawali 8. Hanya digit
 * ASCII yang diterima. null kalau tidak sah.
 */
export function normalizePhone(raw: string): string | null {
  const value = raw.trim();
  if (value === "" || value.length > 32) return null;
  const plus = value.startsWith("+");
  const body = plus ? value.slice(1) : value;
  if (!/^[0-9 .()-]+$/.test(body)) return null;
  let digits = body.replace(/[ .()-]/g, "");
  if (plus && !digits.startsWith("62")) return null;
  if (digits.startsWith("62")) digits = digits.slice(2);
  else if (digits.startsWith("0")) digits = digits.slice(1);
  if (digits.length < 9 || digits.length > 12 || !digits.startsWith("8")) return null;
  return `+62${digits}`;
}

/** PIN klaim: spasi dan tanda hubung dibuang; sisanya harus 10 karakter. */
export function normalizePin(raw: string): string | null {
  const pin = raw.replace(/[\s-]/g, "").toUpperCase();
  return [...pin].length === 10 ? pin : null;
}

const GEO_TIMEOUT_MS = 10_000;

/**
 * Meminta lokasi sekali, setelah pengguna menekan tombol. Ditolak, tidak
 * didukung, atau terlalu lama: null, dan verifikasi tetap jalan.
 */
export function requestLocation(): Promise<GeoPoint | null> {
  return new Promise((resolve) => {
    if (!("geolocation" in navigator)) {
      resolve(null);
      return;
    }
    // Opsi `timeout` tidak menghitung waktu menunggu jawaban izin, dan
    // sebagian browser tidak memanggil callback apa pun saat prompt ditutup.
    // Timer sendiri menjamin halaman tidak macet.
    const guard = setTimeout(() => resolve(null), GEO_TIMEOUT_MS + 2_000);
    const done = (point: GeoPoint | null) => {
      clearTimeout(guard);
      resolve(point);
    };
    navigator.geolocation.getCurrentPosition(
      (pos) =>
        done({
          lat: pos.coords.latitude,
          lng: pos.coords.longitude,
          accuracy_m: Math.min(100_000, Math.max(0, Math.round(pos.coords.accuracy))),
        }),
      () => done(null),
      { enableHighAccuracy: false, timeout: GEO_TIMEOUT_MS, maximumAge: 60_000 },
    );
  });
}
