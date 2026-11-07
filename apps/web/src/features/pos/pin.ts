import type { PinLockMeta, PosDb } from "./db";

// Login kasir offline: PIN 6 digit diverifikasi lokal terhadap pos_pin_hash
// (argon2id PHC) dari bootstrap. Setelah 5 kali salah, login di perangkat ini
// dikunci beberapa menit. Penghitung disimpan di IndexedDB (bukan memori) dan
// berlaku untuk semua kasir, supaya refresh atau ganti nama kasir tidak
// mereset batas.

export const PIN_LENGTH = 6;
export const PIN_MAX_ATTEMPTS = 5;
export const PIN_LOCK_MS = 5 * 60_000;

// Format PHC dari backend (domain.HashPassword): $argon2id$v=19$m=..,t=..,p=..$salt$hash.
const PHC_ARGON2ID = /^\$argon2id\$v=19\$m=\d+,t=\d+,p=\d+\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$/;

export type PinVerifier = (pin: string, hash: string) => Promise<boolean>;

export const argon2Verifier: PinVerifier = async (pin, hash) => {
  // Dimuat saat dibutuhkan supaya WASM argon2 tidak ikut bundel awal.
  const { argon2Verify } = await import("hash-wasm");
  return argon2Verify({ password: pin, hash });
};

export type PinResult =
  | { ok: true; cashierId: string; cashierName: string }
  | { ok: false; reason: "format" }
  | { ok: false; reason: "unknown_cashier" }
  | { ok: false; reason: "hash_invalid" }
  | { ok: false; reason: "engine_unavailable" }
  | { ok: false; reason: "wrong"; remaining: number }
  | { ok: false; reason: "locked"; until: number };

export function isPinFormat(pin: string): boolean {
  return new RegExp(`^\\d{${PIN_LENGTH}}$`).test(pin);
}

async function readLock(db: PosDb): Promise<PinLockMeta> {
  return (await db.getMeta("pinLock")) ?? { key: "pinLock", failures: 0, lockedUntil: null };
}

export async function pinLockedUntil(db: PosDb, now: number = Date.now()): Promise<number | null> {
  const lock = await readLock(db);
  return lock.lockedUntil && lock.lockedUntil > now ? lock.lockedUntil : null;
}

export async function verifyCashierPin(
  db: PosDb,
  cashierId: string,
  pin: string,
  opts: { now?: number; verify?: PinVerifier } = {},
): Promise<PinResult> {
  const now = opts.now ?? Date.now();
  const verify = opts.verify ?? argon2Verifier;
  if (!isPinFormat(pin)) return { ok: false, reason: "format" };

  const until = await pinLockedUntil(db, now);
  if (until) return { ok: false, reason: "locked", until };

  const cashier = await db.cashiers.get(cashierId);
  if (!cashier) return { ok: false, reason: "unknown_cashier" };
  if (!PHC_ARGON2ID.test(cashier.pinHash)) return { ok: false, reason: "hash_invalid" };

  let match: boolean;
  try {
    match = await verify(pin, cashier.pinHash);
  } catch {
    // Hash formatnya sah, jadi kegagalan di sini berasal dari mesin argon2
    // (mis. WebAssembly diblokir CSP), bukan dari PIN. Tidak dihitung salah.
    return { ok: false, reason: "engine_unavailable" };
  }

  return db.transaction("rw", db.meta, async (): Promise<PinResult> => {
    const lock = await readLock(db);
    // Tab lain bisa mengunci selama argon2 berjalan: kunci aktif selalu menang,
    // PIN benar pun tidak membukanya.
    if (lock.lockedUntil && lock.lockedUntil > now) return { ok: false, reason: "locked", until: lock.lockedUntil };
    // Kunci yang sudah lewat waktunya memulai hitungan baru.
    const failures = lock.lockedUntil && lock.lockedUntil <= now ? 0 : lock.failures;
    if (match) {
      await db.meta.put({ key: "pinLock", failures: 0, lockedUntil: null });
      await db.meta.put({ key: "session", cashierId: cashier.id, cashierName: cashier.name, since: now });
      return { ok: true, cashierId: cashier.id, cashierName: cashier.name };
    }
    const next = failures + 1;
    if (next >= PIN_MAX_ATTEMPTS) {
      const lockedUntil = now + PIN_LOCK_MS;
      await db.meta.put({ key: "pinLock", failures: 0, lockedUntil });
      return { ok: false, reason: "locked", until: lockedUntil };
    }
    await db.meta.put({ key: "pinLock", failures: next, lockedUntil: null });
    return { ok: false, reason: "wrong", remaining: PIN_MAX_ATTEMPTS - next };
  });
}

export async function endSession(db: PosDb): Promise<void> {
  await db.meta.delete("session");
}
