import { formatCalendarDate, formatDecimal, formatNumber } from "@/shared/lib/format";
import type { DataColumn } from "@/shared/ui/data-table";
import { PageHeader } from "@/shared/ui/page-header";
import { StatusBadge } from "@/shared/ui/status-badge";
import { ccKeys, listRunout, type RunoutItem, type RunoutLevel, type RunoutQuery } from "./api";
import { FilterBar, locationTypeOptions, SelectFilter, useUrlFilters } from "./filters";
import { consoleStrings as t } from "./strings";
import { relativeDay, runoutReasons } from "./text";
import { useCursorList } from "./use-cursor-list";
import { ListSection, LocationCell, ProductCell, RunoutLevelBadge } from "./ui";

const KEYS = ["level", "tipe", "lokasi", "produk"] as const;
const LEVELS: RunoutLevel[] = ["CRITICAL", "WARNING", "INSUFFICIENT_DATA", "OK"];
const levelOptions = LEVELS.map((value) => ({ value, label: t.runout.levels[value] }));

function isLevel(value: string): value is RunoutLevel {
  return (LEVELS as string[]).includes(value);
}

function DoiCell({ row }: { row: RunoutItem }) {
  if (row.days_of_inventory === null) return <span className="text-muted-foreground">{t.runout.doiUnknown}</span>;
  return (
    <span className="inline-flex flex-col items-end max-md:items-start">
      <span>{t.runout.doi(formatDecimal(row.days_of_inventory))}</span>
      {row.avg_daily_sales !== null && (
        <span className="text-xs text-muted-foreground">{t.runout.avgSales(formatDecimal(row.avg_daily_sales))}</span>
      )}
    </span>
  );
}

function RunoutDateCell({ row }: { row: RunoutItem }) {
  if (row.runout_date) {
    return (
      <span className="inline-flex flex-col">
        <span>{relativeDay(row.runout_date)}</span>
        <span className="text-xs text-muted-foreground">{formatCalendarDate(row.runout_date)}</span>
      </span>
    );
  }
  return <span className="text-muted-foreground">{row.days_of_inventory === null ? t.runout.runoutUnknown : t.runout.runoutFar}</span>;
}

export function RunoutPage() {
  const filters = useUrlFilters(KEYS);
  const query: RunoutQuery = {
    level: isLevel(filters.values.level) ? filters.values.level : undefined,
    location_type: filters.values.tipe || undefined,
    location_id: filters.values.lokasi || undefined,
    product_id: filters.values.produk || undefined,
  };
  const list = useCursorList(ccKeys.runout(query), (cursor) => listRunout({ ...query, cursor }));
  const windowDays = list.firstPage?.window_days;
  const first = list.rows[0];

  const columns: DataColumn<RunoutItem>[] = [
    {
      key: "product",
      header: t.runout.columns.product,
      card: "title",
      cell: (row) => <ProductCell name={row.product_name} sku={row.sku} filterTo={filters.hrefWith("produk", row.product_id)} />,
    },
    {
      key: "location",
      header: t.runout.columns.location,
      cell: (row) => (
        <LocationCell
          name={row.location_name}
          code={row.location_code}
          type={row.location_type}
          filterTo={filters.hrefWith("lokasi", row.location_id)}
        />
      ),
    },
    { key: "level", header: t.runout.columns.level, cell: (row) => <RunoutLevelBadge level={row.level} /> },
    {
      key: "available",
      header: t.runout.columns.available,
      numeric: true,
      cell: (row) => (
        <span className="inline-flex flex-col items-end gap-1 max-md:items-start">
          {formatNumber(row.available_qty)}
          {row.is_estimate && <StatusBadge tone="neutral" label={t.estimate} />}
        </span>
      ),
    },
    { key: "doi", header: t.runout.columns.doi, numeric: true, cell: (row) => <DoiCell row={row} /> },
    { key: "date", header: t.runout.columns.runoutDate, cell: (row) => <RunoutDateCell row={row} /> },
    {
      key: "reason",
      header: t.runout.columns.reason,
      cell: (row) => (
        <ul className="max-w-xs space-y-1 text-sm">
          {runoutReasons(row, windowDays ?? 28).map((reason) => (
            <li key={reason}>{reason}</li>
          ))}
        </ul>
      ),
    },
  ];

  const chips = [
    filters.values.lokasi && {
      key: "lokasi",
      label: t.filters.location(first?.location_id === filters.values.lokasi ? first.location_name : t.filters.selectedLocation),
      onRemove: () => filters.set("lokasi", ""),
    },
    filters.values.produk && {
      key: "produk",
      label: t.filters.product(first?.product_id === filters.values.produk ? first.product_name : t.filters.selectedProduct),
      onRemove: () => filters.set("produk", ""),
    },
  ].filter((chip) => !!chip);

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={t.runout.title} description={windowDays ? t.runout.description(windowDays) : t.runout.descriptionLoading} />
      <FilterBar chips={chips} active={filters.active} onClear={filters.clear}>
        <SelectFilter
          label={t.runout.level}
          value={filters.values.level}
          allLabel={t.runout.allLevels}
          options={levelOptions}
          onChange={(value) => filters.set("level", value)}
        />
        <SelectFilter
          label={t.filters.locationType}
          value={filters.values.tipe}
          allLabel={t.filters.allLocationTypes}
          options={locationTypeOptions}
          onChange={(value) => filters.set("tipe", value)}
        />
      </FilterBar>
      <ListSection
        query={list.query}
        rows={list.rows}
        caption={t.runout.caption}
        columns={columns}
        rowKey={(row) => `${row.location_id}:${row.product_id}`}
        loadMore={list.loadMore}
        empty={{ title: t.runout.empty, description: t.runout.emptyHint }}
        onClearFilters={filters.active ? filters.clear : undefined}
      />
    </div>
  );
}
