// Format angka dan waktu untuk semua permukaan (id-ID). Waktu dari API selalu
// UTC; ditampilkan di WIB karena kontrak belum mengirim zona waktu lokasi.
const TIME_ZONE = "Asia/Jakarta";
const ZONE_SUFFIX = "WIB";

const integerFormat = new Intl.NumberFormat("id-ID");
const decimalFormat = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 1 });
const percentFormat = new Intl.NumberFormat("id-ID", { style: "percent", maximumFractionDigits: 1 });
const dateTimeFormat = new Intl.DateTimeFormat("id-ID", {
  timeZone: TIME_ZONE,
  day: "numeric",
  month: "short",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});
const timeFormat = new Intl.DateTimeFormat("id-ID", { timeZone: TIME_ZONE, hour: "2-digit", minute: "2-digit" });
const dateFormat = new Intl.DateTimeFormat("id-ID", { timeZone: TIME_ZONE, day: "numeric", month: "long", year: "numeric" });
// Tanggal kalender (YYYY-MM-DD) sudah dalam zona lokasi; diformat sebagai UTC
// supaya harinya tidak bergeser.
const calendarFormat = new Intl.DateTimeFormat("id-ID", { timeZone: "UTC", day: "numeric", month: "short", year: "numeric" });
const isoDayFormat = new Intl.DateTimeFormat("en-CA", { timeZone: TIME_ZONE });

export function formatNumber(value: number): string {
  return integerFormat.format(value);
}

export function formatDecimal(value: number): string {
  return decimalFormat.format(value);
}

export function formatPercent(ratio: number): string {
  return percentFormat.format(ratio);
}

/** "7 Nov 2026, 10.32 WIB" */
export function formatDateTime(iso: string): string {
  return `${dateTimeFormat.format(new Date(iso))} ${ZONE_SUFFIX}`;
}

/** "10.32 WIB" */
export function formatTime(iso: string): string {
  return `${timeFormat.format(new Date(iso))} ${ZONE_SUFFIX}`;
}

/** "7 November 2026" (tanggal di WIB dari timestamp UTC) */
export function formatDate(iso: string): string {
  return dateFormat.format(new Date(iso));
}

/** "8 Nov 2026" dari tanggal kalender "2026-11-08" */
export function formatCalendarDate(day: string): string {
  return calendarFormat.format(new Date(`${day}T00:00:00Z`));
}

/** Selisih hari kalender dari hari ini (WIB) ke `day` (YYYY-MM-DD). */
export function daysFromToday(day: string, now: Date = new Date()): number {
  const today = Date.parse(`${isoDayFormat.format(now)}T00:00:00Z`);
  return Math.round((Date.parse(`${day}T00:00:00Z`) - today) / 86_400_000);
}

/** Banyak karakter menurut code point, sama dengan cara server menghitung maxLength. */
export function codePointLength(value: string): number {
  return [...value].length;
}
