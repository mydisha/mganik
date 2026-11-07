import { describe, expect, it } from "vitest";
import { relativeDay, runoutReasons, type Sentence, summarySentences, leakageExplanation, leakageStatus } from "./text";
import { runoutFixture, summaryFixture } from "./test-fixtures";

const now = new Date("2026-11-07T03:00:00Z"); // 10.00 WIB, 7 Nov

function text(paragraphs: Sentence[][]) {
  return paragraphs.map((p) => p.map((s) => s.map((part) => (typeof part === "string" ? part : part.text)).join("")).join(" "));
}

describe("summarySentences", () => {
  it("kasus nol menjadi satu kalimat tenang", () => {
    expect(text(summarySentences(summaryFixture(), now))).toEqual([
      "Belum ada yang perlu ditindaklanjuti: stok di lokasi sendiri aman, tidak ada anomali terbuka, dan tidak ada konflik alokasi.",
    ]);
  });

  it("menyusun kalimat stok, anomali, konflik, dan toko offline", () => {
    const summary = summaryFixture({
      runout: {
        window_days: 28,
        critical_items: 1,
        critical_locations: 1,
        warning_items: 1234,
        warning_locations: 3,
        insufficient_data_items: 2,
        earliest_runout_date: "2026-11-08",
      },
      stock: { out_of_stock_items: 2, out_of_stock_locations: 1 },
      anomalies: {
        open_total: 5,
        open_by_severity: { LOW: 1, MEDIUM: 1, HIGH: 2, CRITICAL: 1 },
        top_party: { party_id: "p1", party_name: "Distributor Sulsel", open_count: 3 },
      },
      conflicts: { open_total: 2 },
      stores: { total: 4, offline: 1 },
    });

    const paragraphs = summarySentences(summary, now);

    expect(text(paragraphs)).toEqual([
      "Stok 1 SKU di 1 lokasi kritis: sudah atau akan habis sebelum kiriman ulang tiba, paling cepat habis besok. 1.234 SKU lain di 3 lokasi mulai menipis. 2 SKU sudah habis di 1 lokasi. 2 SKU belum punya riwayat penjualan, jadi perkiraannya belum dihitung.",
      "5 anomali masih terbuka, 3 di antaranya berprioritas tinggi atau kritis; terbanyak dari Distributor Sulsel (3). 2 konflik alokasi menunggu keputusan. 1 dari 4 toko sedang offline, jadi stok toko itu tidak dijual online.",
    ]);
    expect(paragraphs[1]?.[0]?.[0]).toEqual({ text: "5 anomali", to: "/console/anomali" });
    expect(paragraphs[1]?.[1]?.[0]).toEqual({ text: "2 konflik alokasi", to: "/console/konflik" });
  });

  it("hanya stok menipis, tanpa anomali", () => {
    const summary = summaryFixture({
      runout: { ...summaryFixture().runout, warning_items: 3, warning_locations: 2, earliest_runout_date: "2026-11-12" },
    });
    expect(text(summarySentences(summary, now))).toEqual([
      "Stok 3 SKU di 2 lokasi mulai menipis, paling cepat habis dalam 5 hari (12 Nov 2026).",
      "Tidak ada anomali terbuka.",
    ]);
  });
});

describe("relativeDay", () => {
  it("memakai hari kalender WIB", () => {
    expect(relativeDay("2026-11-07", now)).toBe("hari ini");
    expect(relativeDay("2026-11-06", now)).toBe("hari ini");
    expect(relativeDay("2026-11-08", now)).toBe("besok");
    // 23.30 WIB masih tanggal 7.
    expect(relativeDay("2026-11-08", new Date("2026-11-07T16:30:00Z"))).toBe("besok");
    expect(relativeDay("2026-11-08", new Date("2026-11-07T17:30:00Z"))).toBe("hari ini");
  });
});

describe("runoutReasons", () => {
  it("menjelaskan data yang belum cukup tanpa angka palsu", () => {
    const item = runoutFixture({
      level: "INSUFFICIENT_DATA",
      reasons: ["NO_SALES_HISTORY"],
      days_of_inventory: null,
      avg_daily_sales: null,
      runout_date: null,
      history_days: null,
    });
    expect(runoutReasons(item, 28)).toEqual(["Belum ada penjualan dalam 28 hari terakhir, jadi tanggal habis tidak dikarang."]);
  });

  it("menjelaskan riwayat pendek dan batas aman", () => {
    const item = runoutFixture({ level: "WARNING", reasons: ["BELOW_SAFETY_DAYS", "SHORT_HISTORY"], history_days: 4 });
    expect(runoutReasons(item, 28)).toEqual([
      "Di bawah batas aman 5 hari (waktu kirim ditambah cadangan).",
      "Riwayat penjualan baru 4 hari, perkiraannya masih kasar.",
    ]);
  });

  it("level aman tanpa alasan", () => {
    expect(runoutReasons(runoutFixture({ level: "OK", reasons: [] }), 28)).toEqual(["Stok cukup sampai kiriman berikutnya."]);
  });
});

describe("leakage", () => {
  const base = {
    party_id: "p1",
    party_name: "Distributor Sulsel",
    coverage_mode: "SELECTED" as const,
    total_scans: 40,
    located_scans: 38,
    unlocated_scans: 2,
    judged_scans: 30,
    unjudged_scans: 8,
    out_of_territory_scans: 6,
    leak_ratio: 0.2,
    sample_too_small: false,
    test_buy_scans: 1,
    test_buy_hits: 1,
    above_baseline: true,
    alert: true,
  };

  it("test-buy di luar wilayah dan rasio tinggi", () => {
    expect(leakageStatus(base)).toBe("alert");
    expect(leakageExplanation(base, 0.05, 20)).toEqual([
      "1 test-buy ditemukan di luar wilayah. Ini sinyal kuat penjualan keluar wilayah.",
      "Di atas batas normal 5%.",
      "8 scan tidak bisa dinilai karena lokasinya kurang rinci.",
      "2 scan tanpa lokasi.",
    ]);
  });

  it("sampel kecil dan distributor tanpa wilayah", () => {
    const small = { ...base, alert: false, above_baseline: false, sample_too_small: true, judged_scans: 7, test_buy_hits: 0, unjudged_scans: 0, unlocated_scans: 0 };
    expect(leakageStatus(small)).toBe("watch");
    expect(leakageExplanation(small, 0.05, 20)).toEqual(["Sampel masih kecil: baru 7 dari minimal 20 scan yang bisa dinilai."]);

    const none = { ...small, coverage_mode: null, sample_too_small: false, leak_ratio: null, out_of_territory_scans: null };
    expect(leakageStatus(none)).toBe("unjudged");
    expect(leakageExplanation(none, 0.05, 20)).toEqual(["Belum punya wilayah resmi yang berlaku, jadi rasio tidak dihitung."]);
  });
});
