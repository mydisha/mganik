// UUIDv7 (RFC 9562): 48 bit milidetik Unix + versi 7 + varian 10 + 74 bit acak.
// Dipakai untuk client_txn_id supaya urut waktu dan unik tanpa server.
export function uuidv7(now: number = Date.now(), random?: Uint8Array): string {
  const rand = random ?? globalThis.crypto.getRandomValues(new Uint8Array(10));
  if (rand.length < 10) throw new Error("uuidv7 butuh 10 byte acak");
  const b = new Uint8Array(16);
  let ts = Math.max(0, Math.floor(now));
  for (let i = 5; i >= 0; i--) {
    b[i] = ts % 256;
    ts = Math.floor(ts / 256);
  }
  b.set(rand.subarray(0, 10), 6);
  b[6] = 0x70 | (b[6]! & 0x0f);
  b[8] = 0x80 | (b[8]! & 0x3f);
  const hex = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}
