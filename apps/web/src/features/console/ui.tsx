import type { ReactNode } from "react";
import { Link } from "react-router";
import { formatDateTime } from "@/shared/lib/format";
import { strings } from "@/shared/lib/strings";
import { Button } from "@/shared/ui/button";
import { DataTable, type DataColumn } from "@/shared/ui/data-table";
import { EmptyState } from "@/shared/ui/empty-state";
import { ErrorState } from "@/shared/ui/error-state";
import { type StatusTone, StatusBadge } from "@/shared/ui/status-badge";
import type {
  AnomalySeverity,
  AnomalyStatus,
  ConflictItem,
  Device,
  RunoutLevel,
} from "./api";
import { consoleStrings as t } from "./strings";

/** Nilai kosong: tanda pisah untuk mata, "tidak ada" untuk screen reader. */
export function EmptyValue() {
  return (
    <span className="text-muted-foreground">
      <span aria-hidden="true">—</span>
      <span className="sr-only">{strings.common.emptyValue}</span>
    </span>
  );
}

export function TimeText({ iso }: { iso: string | null | undefined }) {
  if (!iso) return <EmptyValue />;
  return (
    <time dateTime={iso} className="whitespace-nowrap">
      {formatDateTime(iso)}
    </time>
  );
}

const filterLinkClasses = "font-medium text-foreground underline-offset-4 hover:text-link hover:underline";

interface LocationCellProps {
  name: string;
  code: string;
  type?: string;
  /** Alamat daftar yang disaring ke lokasi ini. */
  filterTo?: string;
  extra?: ReactNode;
}

export function LocationCell({ name, code, type, filterTo, extra }: LocationCellProps) {
  return (
    <div className="min-w-0 space-y-0.5">
      {filterTo ? (
        <Link to={filterTo} className={filterLinkClasses} title={t.filters.onlyLocation(name)}>
          {name}
        </Link>
      ) : (
        <p className="font-medium">{name}</p>
      )}
      <p className="text-xs text-muted-foreground">
        <span className="font-mono">{code}</span>
        {type && <> · {t.locationTypes[type] ?? type}</>}
      </p>
      {extra}
    </div>
  );
}

export function ProductCell({ name, sku, filterTo }: { name: string; sku: string; filterTo?: string }) {
  return (
    <div className="min-w-0 space-y-0.5">
      {filterTo ? (
        <Link to={filterTo} className={filterLinkClasses} title={t.filters.onlyProduct(name)}>
          {name}
        </Link>
      ) : (
        <p className="font-medium">{name}</p>
      )}
      <p className="font-mono text-xs text-muted-foreground">{sku}</p>
    </div>
  );
}

const levelTone: Record<RunoutLevel, StatusTone> = {
  CRITICAL: "danger",
  WARNING: "warning",
  INSUFFICIENT_DATA: "neutral",
  OK: "success",
};
export function RunoutLevelBadge({ level }: { level: RunoutLevel }) {
  return <StatusBadge tone={levelTone[level]} label={t.runout.levels[level]} />;
}

const severityTone: Record<AnomalySeverity, StatusTone> = { LOW: "neutral", MEDIUM: "warning", HIGH: "danger", CRITICAL: "danger" };
export function SeverityBadge({ severity }: { severity: AnomalySeverity }) {
  return <StatusBadge tone={severityTone[severity]} label={t.anomalies.severities[severity]} />;
}

const anomalyStatusTone: Record<AnomalyStatus, StatusTone> = { OPEN: "warning", ACK: "info", RESOLVED: "success", FALSE_POSITIVE: "neutral" };
export function AnomalyStatusBadge({ status }: { status: AnomalyStatus }) {
  return <StatusBadge tone={anomalyStatusTone[status]} label={t.anomalies.statuses[status]} />;
}

const conflictTone: Record<ConflictItem["status"], StatusTone> = { OPEN: "danger", AUTO_RESOLVED: "success", RESOLVED: "success" };
export function ConflictStatusBadge({ status }: { status: ConflictItem["status"] }) {
  return <StatusBadge tone={conflictTone[status]} label={t.conflicts.statuses[status]} />;
}

export function DeviceStatusBadge({ status }: { status: Device["status"] }) {
  return <StatusBadge tone={status === "ACTIVE" ? "success" : "neutral"} label={t.devices.statuses[status]} />;
}

interface QueryLike {
  isPending: boolean;
  isError: boolean;
  error: unknown;
  data?: unknown;
  refetch: () => unknown;
}

interface ListSectionProps<T> {
  query: QueryLike;
  rows: T[];
  caption: string;
  columns: DataColumn<T>[];
  rowKey: (row: T) => string;
  loadMore?: { hasMore: boolean; loading: boolean; onLoadMore: () => void };
  empty: { title: string; description: string; action?: ReactNode };
  /** Ada filter aktif: daftar kosong berarti filter terlalu sempit. */
  onClearFilters?: () => void;
}

/** Loading (skeleton baris), error dengan coba lagi, kosong, lalu tabel. */
export function ListSection<T>({ query, rows, caption, columns, rowKey, loadMore, empty, onClearFilters }: ListSectionProps<T>) {
  if (query.isPending) return <DataTable caption={caption} columns={columns} rows={[]} rowKey={rowKey} loading />;
  if (query.isError && !query.data) return <ErrorState error={query.error} onRetry={() => void query.refetch()} />;
  if (rows.length === 0) {
    return onClearFilters ? (
      <EmptyState
        title={t.filters.emptyFiltered}
        description={t.filters.emptyFilteredHint}
        action={
          <Button variant="outline" onClick={onClearFilters}>
            {strings.common.clearFilters}
          </Button>
        }
      />
    ) : (
      <EmptyState title={empty.title} description={empty.description} action={empty.action} />
    );
  }
  return <DataTable caption={caption} columns={columns} rows={rows} rowKey={rowKey} loadMore={loadMore} />;
}

/** Daftar fakta label/nilai di panel detail. */
export function FactList({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-x-4 gap-y-2 text-sm">
      {items.map((item) => (
        <div key={item.label} className="contents">
          <dt className="text-muted-foreground">{item.label}</dt>
          <dd className="min-w-0 break-words">{item.value ?? <EmptyValue />}</dd>
        </div>
      ))}
    </dl>
  );
}
