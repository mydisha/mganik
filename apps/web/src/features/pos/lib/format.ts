// Format angka dan waktu kasir sesuai DESIGN.md: `Rp150.000` (tanpa spasi,
// titik ribuan), waktu id-ID di zona toko dengan akhiran WIB/WITA/WIT.

const groupFmt = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 0 });

export function formatRupiah(amount: number): string {
  const sign = amount < 0 ? "-" : "";
  return `${sign}Rp${groupFmt.format(Math.abs(Math.trunc(amount)))}`;
}

export function formatNumber(n: number): string {
  return groupFmt.format(n);
}

const zoneSuffix: Record<string, string> = {
  "Asia/Jakarta": "WIB",
  "Asia/Pontianak": "WIB",
  "Asia/Makassar": "WITA",
  "Asia/Jayapura": "WIT",
};

export const DEFAULT_TIMEZONE = "Asia/Jakarta";

function safeZone(timeZone: string | undefined): string {
  if (!timeZone) return DEFAULT_TIMEZONE;
  try {
    new Intl.DateTimeFormat("id-ID", { timeZone });
    return timeZone;
  } catch {
    return DEFAULT_TIMEZONE;
  }
}

// formatClock: "10.42 WIB".
export function formatClock(at: number | string | Date, timeZone?: string): string {
  const zone = safeZone(timeZone);
  const text = new Intl.DateTimeFormat("id-ID", { hour: "2-digit", minute: "2-digit", timeZone: zone }).format(
    new Date(at),
  );
  const suffix = zoneSuffix[zone];
  return suffix ? `${text} ${suffix}` : text;
}

// formatDateTime: "7 Nov 2026 10.42 WIB".
export function formatDateTime(at: number | string | Date, timeZone?: string): string {
  const zone = safeZone(timeZone);
  const date = new Intl.DateTimeFormat("id-ID", { day: "numeric", month: "short", year: "numeric", timeZone: zone })
    .format(new Date(at))
    .replace(/\.$/, "");
  return `${date} ${formatClock(at, zone)}`;
}

// Nomor transaksi perangkat: device_seq dengan nol di depan supaya mudah dibaca.
export function formatTxnNumber(seq: number): string {
  return String(seq).padStart(6, "0");
}
