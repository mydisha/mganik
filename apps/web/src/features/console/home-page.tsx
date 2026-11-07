import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { formatNumber, formatTime } from "@/shared/lib/format";
import { DataTable, type DataColumn } from "@/shared/ui/data-table";
import { EmptyState } from "@/shared/ui/empty-state";
import { ErrorState } from "@/shared/ui/error-state";
import { PageHeader } from "@/shared/ui/page-header";
import { Skeleton } from "@/shared/ui/skeleton";
import { type AnomalyItem, ccKeys, getSummary, listAnomalies, listRunout, type RunoutItem } from "./api";
import { consoleStrings as t } from "./strings";
import { relativeDay, summarySentences } from "./text";
import { AnomalyStatusBadge, EmptyValue, LocationCell, ProductCell, RunoutLevelBadge, SeverityBadge, TimeText } from "./ui";

const TOP = 5;

function SectionHeader({ id, title, to, linkLabel }: { id: string; title: string; to: string; linkLabel: string }) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-2">
      <h2 id={id} className="text-base font-semibold">
        {title}
      </h2>
      <Link to={to} className="text-sm text-link underline-offset-4 hover:underline max-md:py-3">
        {linkLabel}
      </Link>
    </div>
  );
}

function Summary() {
  const summary = useQuery({ queryKey: ccKeys.summary(), queryFn: getSummary });

  if (summary.isPending) {
    return (
      <div className="space-y-2 rounded-lg border bg-card p-5" aria-busy="true">
        <Skeleton className="h-5 w-full" />
        <Skeleton className="h-5 w-4/5" />
        <Skeleton className="h-5 w-2/3" />
      </div>
    );
  }
  if (summary.isError) return <ErrorState error={summary.error} onRetry={() => void summary.refetch()} title={t.home.summaryFailed} />;

  return (
    <section aria-labelledby="ringkasan" className="space-y-3 rounded-lg border bg-card p-5 shadow-xs">
      <h2 id="ringkasan" className="sr-only">
        {t.home.summaryHeading}
      </h2>
      {summarySentences(summary.data).map((paragraph, i) => (
        <p key={i} className="text-base leading-7">
          {paragraph.map((sentence, j) => (
            <span key={j}>
              {j > 0 && " "}
              {sentence.map((part, k) =>
                typeof part === "string" ? (
                  part
                ) : (
                  <Link key={k} to={part.to} className="font-semibold text-link underline underline-offset-4">
                    {part.text}
                  </Link>
                ),
              )}
            </span>
          ))}
        </p>
      ))}
      <p className="text-xs text-muted-foreground">{t.updatedAt(formatTime(summary.data.generated_at))}</p>
    </section>
  );
}

const runoutColumns: DataColumn<RunoutItem>[] = [
  {
    key: "product",
    header: t.runout.columns.product,
    card: "title",
    cell: (row) => <ProductCell name={row.product_name} sku={row.sku} />,
  },
  {
    key: "location",
    header: t.runout.columns.location,
    cell: (row) => <LocationCell name={row.location_name} code={row.location_code} />,
  },
  { key: "level", header: t.runout.columns.level, cell: (row) => <RunoutLevelBadge level={row.level} /> },
  {
    key: "available",
    header: t.runout.columns.available,
    numeric: true,
    cell: (row) => (
      <>
        {formatNumber(row.available_qty)}
        {row.is_estimate && <span className="block text-xs text-muted-foreground">{t.estimate}</span>}
      </>
    ),
  },
  {
    key: "runout",
    header: t.runout.columns.runoutDate,
    cell: (row) => (row.runout_date ? relativeDay(row.runout_date) : <EmptyValue />),
  },
];

function RunoutTop() {
  // Urutan server: kritis, menipis, data belum cukup, aman. Beranda hanya
  // menampilkan yang perlu diisi ulang.
  const runout = useQuery({ queryKey: ccKeys.runout({ limit: TOP }), queryFn: () => listRunout({ limit: TOP }) });
  const rows = runout.data?.items.filter((row) => row.level === "CRITICAL" || row.level === "WARNING") ?? [];

  return (
    <section aria-labelledby="runout-teratas" className="space-y-3">
      <SectionHeader id="runout-teratas" title={t.home.runoutHeading} to="/console/runout" linkLabel={t.home.runoutAll} />
      {runout.isPending ? (
        <DataTable caption={t.home.runoutHeading} columns={runoutColumns} rows={[]} rowKey={() => ""} loading />
      ) : runout.isError ? (
        <ErrorState error={runout.error} onRetry={() => void runout.refetch()} />
      ) : rows.length === 0 ? (
        <EmptyState title={t.home.runoutEmpty} description={t.home.runoutEmptyHint} />
      ) : (
        <DataTable
          caption={t.home.runoutHeading}
          columns={runoutColumns}
          rows={rows}
          rowKey={(row) => `${row.location_id}:${row.product_id}`}
        />
      )}
    </section>
  );
}

const anomalyColumns: DataColumn<AnomalyItem>[] = [
  { key: "rule", header: t.anomalies.columns.rule, card: "title", cell: (row) => <span className="font-medium">{row.rule_name}</span> },
  { key: "severity", header: t.anomalies.columns.severity, cell: (row) => <SeverityBadge severity={row.severity} /> },
  { key: "status", header: t.anomalies.columns.status, cell: (row) => <AnomalyStatusBadge status={row.status} /> },
  {
    key: "subject",
    header: t.anomalies.columns.subject,
    cell: (row) => row.party_name ?? row.region_name ?? <EmptyValue />,
  },
  { key: "lastSeen", header: t.anomalies.columns.lastSeen, cell: (row) => <TimeText iso={row.last_seen_at} /> },
];

function AnomaliesTop() {
  const query = { status: "OPEN" as const, limit: TOP };
  const anomalies = useQuery({ queryKey: ccKeys.anomalies(query), queryFn: () => listAnomalies(query) });

  return (
    <section aria-labelledby="anomali-terbuka" className="space-y-3">
      <SectionHeader id="anomali-terbuka" title={t.home.anomaliesHeading} to="/console/anomali" linkLabel={t.home.anomaliesAll} />
      {anomalies.isPending ? (
        <DataTable caption={t.home.anomaliesHeading} columns={anomalyColumns} rows={[]} rowKey={() => ""} loading />
      ) : anomalies.isError ? (
        <ErrorState error={anomalies.error} onRetry={() => void anomalies.refetch()} />
      ) : anomalies.data.items.length === 0 ? (
        <EmptyState title={t.home.anomaliesEmpty} description={t.home.anomaliesEmptyHint} />
      ) : (
        <DataTable caption={t.home.anomaliesHeading} columns={anomalyColumns} rows={anomalies.data.items} rowKey={(row) => row.id} />
      )}
    </section>
  );
}

/** Beranda: kalimat ringkasan, stok yang akan habis, dan anomali terbuka. */
export function HomePage() {
  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={t.home.title} />
      <Summary />
      <RunoutTop />
      <AnomaliesTop />
    </div>
  );
}
