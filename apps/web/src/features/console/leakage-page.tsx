import { formatDate, formatNumber, formatPercent } from "@/shared/lib/format";
import type { DataColumn } from "@/shared/ui/data-table";
import { PageHeader } from "@/shared/ui/page-header";
import { Select } from "@/shared/ui/select";
import { type StatusTone, StatusBadge } from "@/shared/ui/status-badge";
import { ccKeys, type LeakageItem, type LeakageQuery, listLeakage } from "./api";
import { useUrlFilters } from "./filters";
import { consoleStrings as t } from "./strings";
import { type LeakageStatus, leakageExplanation, leakageStatus } from "./text";
import { useCursorList } from "./use-cursor-list";
import { ListSection } from "./ui";

const KEYS = ["hari"] as const;
const WINDOWS = [7, 30, 90];
const DEFAULT_DAYS = 30;
const statusTone: Record<LeakageStatus, StatusTone> = { alert: "danger", watch: "warning", normal: "success", unjudged: "neutral" };

export function LeakagePage() {
  const filters = useUrlFilters(KEYS);
  const days = WINDOWS.includes(Number(filters.values.hari)) ? Number(filters.values.hari) : DEFAULT_DAYS;
  const query: LeakageQuery = { days };
  const list = useCursorList(ccKeys.leakage(query), (cursor) => listLeakage({ ...query, cursor }));
  const meta = list.firstPage;
  const alerts = list.rows.filter((row) => row.alert).length;

  const columns: DataColumn<LeakageItem>[] = [
    { key: "party", header: t.leakage.columns.party, card: "title", cell: (row) => <span className="font-medium">{row.party_name}</span> },
    {
      key: "status",
      header: t.leakage.columns.status,
      cell: (row) => {
        const status = leakageStatus(row);
        return <StatusBadge tone={statusTone[status]} label={t.leakage.statuses[status]} />;
      },
    },
    {
      key: "ratio",
      header: t.leakage.columns.ratio,
      numeric: true,
      cell: (row) =>
        row.leak_ratio === null || row.out_of_territory_scans === null ? (
          <span className="text-muted-foreground">{t.leakage.ratioNone}</span>
        ) : (
          t.leakage.ratio(formatPercent(row.leak_ratio), row.out_of_territory_scans, row.judged_scans)
        ),
    },
    {
      key: "testBuy",
      header: t.leakage.columns.testBuy,
      numeric: true,
      cell: (row) => (row.test_buy_scans === 0 ? formatNumber(0) : t.leakage.testBuy(row.test_buy_hits, row.test_buy_scans)),
    },
    {
      key: "explanation",
      header: t.leakage.columns.explanation,
      cell: (row) => (
        <ul className="max-w-sm space-y-1 text-sm">
          {meta && leakageExplanation(row, meta.baseline_ratio, meta.min_scans).map((line) => <li key={line}>{line}</li>)}
        </ul>
      ),
    },
  ];

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={t.leakage.title} description={t.leakage.description} />
      <div className="flex min-w-0 flex-col gap-1.5 sm:w-56">
        <label htmlFor="leakage-window" className="text-xs font-medium text-muted-foreground">
          {t.leakage.window}
        </label>
        <Select
          id="leakage-window"
          value={String(days)}
          onChange={(e) => filters.set("hari", e.target.value === String(DEFAULT_DAYS) ? "" : e.target.value)}
        >
          {WINDOWS.map((d) => (
            <option key={d} value={String(d)}>
              {t.leakage.windowOption(d)}
            </option>
          ))}
        </Select>
      </div>
      <section aria-label={t.leakage.caption} className="flex flex-col gap-3">
        {meta && list.rows.length > 0 && (
          <div className="space-y-1 rounded-lg border bg-card p-4 text-sm shadow-xs">
            <p className="text-base font-medium">{t.leakage.summary(list.rows.length, alerts)}</p>
            <p className="text-muted-foreground">
              {t.leakage.period(formatDate(meta.from), formatDate(meta.to))} {t.leakage.method(formatPercent(meta.baseline_ratio), meta.min_scans)}
            </p>
          </div>
        )}
        <ListSection
          query={list.query}
          rows={list.rows}
          caption={t.leakage.caption}
          columns={columns}
          rowKey={(row) => row.party_id}
          loadMore={list.loadMore}
          empty={{ title: t.leakage.empty, description: t.leakage.emptyHint }}
        />
      </section>
    </div>
  );
}
