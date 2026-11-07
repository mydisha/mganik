import { daysFromToday, formatCalendarDate, formatPercent } from "@/shared/lib/format";
import type { LeakageItem, RunoutItem, Summary } from "./api";
import { consoleStrings as t } from "./strings";

/** Bagian kalimat: teks biasa, atau frasa yang menjadi tautan. */
export type SentencePart = string | { text: string; to: string };
export type Sentence = SentencePart[];

/** "hari ini", "besok", atau "dalam 5 hari (12 Nov 2026)". */
export function relativeDay(day: string, now: Date = new Date()): string {
  const diff = daysFromToday(day, now);
  if (diff <= 0) return t.relativeDay.today;
  if (diff === 1) return t.relativeDay.tomorrow;
  return t.relativeDay.inDays(diff, formatCalendarDate(day));
}

/**
 * Kalimat ringkasan Beranda dari /cc/summary. Paragraf pertama tentang stok
 * lokasi sendiri, paragraf kedua tentang anomali, konflik, dan toko offline.
 */
export function summarySentences(s: Summary, now: Date = new Date()): Sentence[][] {
  const r = s.runout;
  const earliest = r.earliest_runout_date ? t.home.runoutEarliest(relativeDay(r.earliest_runout_date, now)) : ".";
  const severe = s.anomalies.open_by_severity.HIGH + s.anomalies.open_by_severity.CRITICAL;
  const quiet =
    r.critical_items === 0 &&
    r.warning_items === 0 &&
    s.stock.out_of_stock_items === 0 &&
    s.anomalies.open_total === 0 &&
    s.conflicts.open_total === 0;

  const stock: Sentence[] = [];
  const ops: Sentence[] = [];

  if (quiet) {
    stock.push([t.home.allClear]);
  } else {
    if (r.critical_items > 0) {
      stock.push([t.home.runoutCritical(r.critical_items, r.critical_locations) + earliest]);
      if (r.warning_items > 0) stock.push([t.home.runoutWarningAfterCritical(r.warning_items, r.warning_locations)]);
    } else if (r.warning_items > 0) {
      stock.push([t.home.runoutWarningOnly(r.warning_items, r.warning_locations) + earliest]);
    } else {
      stock.push([t.home.runoutNone(r.window_days)]);
    }
    if (s.stock.out_of_stock_items > 0) {
      stock.push([t.home.outOfStock(s.stock.out_of_stock_items, s.stock.out_of_stock_locations)]);
    }

    if (s.anomalies.open_total > 0) {
      const top = s.anomalies.top_party;
      ops.push([
        { text: t.home.anomaliesLink(s.anomalies.open_total), to: "/console/anomali" },
        t.home.anomaliesOpen +
          (severe > 0 ? t.home.anomaliesSevere(severe) : "") +
          (top ? t.home.anomaliesTopParty(top.party_name, top.open_count) : "") +
          ".",
      ]);
    } else {
      ops.push([t.home.anomaliesNone]);
    }
    if (s.conflicts.open_total > 0) {
      ops.push([{ text: t.home.conflictsLink(s.conflicts.open_total), to: "/console/konflik" }, t.home.conflictsOpen]);
    }
  }

  if (r.insufficient_data_items > 0) stock.push([t.home.insufficient(r.insufficient_data_items)]);
  if (s.stores.offline > 0) ops.push([t.home.storesOffline(s.stores.offline, s.stores.total)]);

  return [stock, ops].filter((paragraph) => paragraph.length > 0);
}

/** Alasan level runout dalam kalimat biasa, satu kalimat per alasan. */
export function runoutReasons(item: RunoutItem, windowDays: number): string[] {
  const reasons = item.reasons.map((reason) => {
    switch (reason) {
      case "STOCK_OUT":
        return t.runout.reasons.STOCK_OUT;
      case "BELOW_LEAD_TIME":
        return t.runout.reasons.BELOW_LEAD_TIME(item.lead_time_days);
      case "BELOW_SAFETY_DAYS":
        return t.runout.reasons.BELOW_SAFETY_DAYS(
          item.lead_time_days !== null && item.safety_days !== null ? item.lead_time_days + item.safety_days : null,
        );
      case "BELOW_MIN_QTY":
        return t.runout.reasons.BELOW_MIN_QTY(item.available_qty, item.min_qty);
      case "NO_SALES_HISTORY":
        return t.runout.reasons.NO_SALES_HISTORY(windowDays);
      case "SHORT_HISTORY":
        return t.runout.reasons.SHORT_HISTORY(item.history_days);
      case "NO_POLICY":
        return t.runout.reasons.NO_POLICY;
    }
  });
  return reasons.length > 0 ? reasons : [t.runout.reasons.none];
}

export type LeakageStatus = keyof typeof t.leakage.statuses;

export function leakageStatus(item: LeakageItem): LeakageStatus {
  if (item.alert) return "alert";
  if (item.coverage_mode === null) return "unjudged";
  if (item.above_baseline || item.sample_too_small) return "watch";
  return "normal";
}

/** Penjelasan penilaian satu distributor dalam kalimat. */
export function leakageExplanation(item: LeakageItem, baseline: number, minScans: number): string[] {
  const lines: string[] = [];
  const base = formatPercent(baseline);
  if (item.test_buy_hits > 0) lines.push(t.leakage.explain.testBuyHit(item.test_buy_hits));
  if (item.coverage_mode === null) {
    lines.push(t.leakage.explain.noTerritory);
  } else if (item.coverage_mode === "NATIONAL") {
    lines.push(t.leakage.explain.national);
  } else if (item.sample_too_small) {
    lines.push(t.leakage.explain.smallSample(item.judged_scans, minScans));
  } else if (item.above_baseline) {
    lines.push(t.leakage.explain.above(base));
  } else {
    lines.push(t.leakage.explain.within(base));
  }
  if (item.unjudged_scans > 0) lines.push(t.leakage.explain.unjudged(item.unjudged_scans));
  if (item.unlocated_scans > 0) lines.push(t.leakage.explain.unlocated(item.unlocated_scans));
  return lines;
}
