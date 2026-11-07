import { formatNumber, formatDateTime } from "@/shared/lib/format";
import type { DataColumn } from "@/shared/ui/data-table";
import { PageHeader } from "@/shared/ui/page-header";
import { StatusBadge } from "@/shared/ui/status-badge";
import { ccKeys, listStock, type StockItem, type StockQuery } from "./api";
import { FilterBar, locationTypeOptions, SelectFilter, useUrlFilters } from "./filters";
import { consoleStrings as t } from "./strings";
import { useCursorList } from "./use-cursor-list";
import { ListSection, LocationCell, ProductCell, TimeText } from "./ui";

const KEYS = ["tipe", "lokasi", "produk"] as const;

function SyncNote({ row }: { row: StockItem }) {
  if (row.sync_state !== "OFFLINE") return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5 pt-1">
      <StatusBadge tone="warning" label={t.syncState.OFFLINE} />
      <span className="text-xs text-muted-foreground">
        {row.last_heartbeat_at ? t.syncState.offlineSince(formatDateTime(row.last_heartbeat_at)) : t.syncState.neverSeen}
      </span>
    </div>
  );
}

export function StockPage() {
  const filters = useUrlFilters(KEYS);
  const query: StockQuery = {
    location_type: filters.values.tipe || undefined,
    location_id: filters.values.lokasi || undefined,
    product_id: filters.values.produk || undefined,
  };
  const list = useCursorList(ccKeys.stock(query), (cursor) => listStock({ ...query, cursor }));
  const first = list.rows[0];

  const columns: DataColumn<StockItem>[] = [
    {
      key: "location",
      header: t.stock.columns.location,
      card: "title",
      cell: (row) => (
        <LocationCell
          name={row.location_name}
          code={row.location_code}
          type={row.location_type}
          filterTo={filters.hrefWith("lokasi", row.location_id)}
          extra={<SyncNote row={row} />}
        />
      ),
    },
    {
      key: "product",
      header: t.stock.columns.product,
      cell: (row) => <ProductCell name={row.product_name} sku={row.sku} filterTo={filters.hrefWith("produk", row.product_id)} />,
    },
    { key: "physical", header: t.stock.columns.physical, numeric: true, cell: (row) => formatNumber(row.physical_qty) },
    { key: "reserved", header: t.stock.columns.reserved, numeric: true, cell: (row) => formatNumber(row.reserved_qty) },
    { key: "quarantine", header: t.stock.columns.quarantine, numeric: true, cell: (row) => formatNumber(row.quarantine_qty) },
    {
      key: "available",
      header: t.stock.columns.available,
      numeric: true,
      cell: (row) => (
        <span className="inline-flex flex-col items-end gap-1 max-md:items-start">
          <span className="font-semibold">{formatNumber(row.available_qty)}</span>
          {row.is_estimate && <StatusBadge tone="neutral" label={t.estimate} />}
        </span>
      ),
    },
    { key: "updated", header: t.stock.columns.updated, cell: (row) => <TimeText iso={row.updated_at} /> },
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
      <PageHeader title={t.stock.title} description={t.stock.description} />
      <FilterBar chips={chips} active={filters.active} onClear={filters.clear}>
        <SelectFilter
          label={t.filters.locationType}
          value={filters.values.tipe}
          allLabel={t.filters.allLocationTypes}
          options={locationTypeOptions}
          onChange={(value) => filters.set("tipe", value)}
        />
      </FilterBar>
      <section className="flex flex-col gap-3" aria-label={t.stock.caption}>
        {list.rows.some((row) => row.is_estimate) && <p className="text-sm text-muted-foreground">{t.stock.estimateNote}</p>}
        <ListSection
          query={list.query}
          rows={list.rows}
          caption={t.stock.caption}
          columns={columns}
          rowKey={(row) => `${row.location_id}:${row.product_id}`}
          loadMore={list.loadMore}
          empty={{ title: t.stock.empty, description: t.stock.emptyHint }}
          onClearFilters={filters.active ? filters.clear : undefined}
        />
      </section>
    </div>
  );
}
