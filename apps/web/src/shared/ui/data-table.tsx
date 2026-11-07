import type { ReactNode } from "react";
import { strings } from "@/shared/lib/strings";
import { useIsMobile } from "@/shared/lib/use-media-query";
import { cn } from "@/shared/lib/utils";
import { Button } from "./button";
import { Skeleton } from "./skeleton";
import { Spinner } from "./spinner";

export interface DataColumn<T> {
  key: string;
  header: string;
  cell: (row: T) => ReactNode;
  /** Angka rata kanan dengan tabular-nums. */
  numeric?: boolean;
  /** Peran kolom di kartu mobile: judul kartu atau baris aksi di bawah. */
  card?: "title" | "actions";
}

interface LoadMore {
  hasMore: boolean;
  loading: boolean;
  onLoadMore: () => void;
}

interface DataTableProps<T> {
  /** Nama tabel untuk screen reader. */
  caption: string;
  columns: DataColumn<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  loading?: boolean;
  loadMore?: LoadMore;
}

const SKELETON_ROWS = 5;

/**
 * Tabel padat (baris 40 px) di layar lebar; di bawah 768 px setiap baris
 * menjadi kartu supaya halaman tidak pernah menggulung ke samping.
 */
export function DataTable<T>({ caption, columns, rows, rowKey, loading = false, loadMore }: DataTableProps<T>) {
  const mobile = useIsMobile();

  return (
    <div className="flex flex-col gap-3">
      {mobile ? (
        <CardList caption={caption} columns={columns} rows={rows} rowKey={rowKey} loading={loading} />
      ) : (
        <Table caption={caption} columns={columns} rows={rows} rowKey={rowKey} loading={loading} />
      )}
      {loadMore?.hasMore && (
        <Button variant="outline" className="self-center" onClick={loadMore.onLoadMore} disabled={loadMore.loading}>
          {loadMore.loading && <Spinner />}
          {loadMore.loading ? strings.common.loadingMore : strings.common.loadMore}
        </Button>
      )}
    </div>
  );
}

type ViewProps<T> = Omit<DataTableProps<T>, "loadMore">;

function Table<T>({ caption, columns, rows, rowKey, loading }: ViewProps<T>) {
  return (
    <div className="overflow-x-auto rounded-lg border bg-card shadow-xs">
      <table className="w-full text-sm">
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr className="border-b">
            {columns.map((col) => (
              <th
                key={col.key}
                scope="col"
                className={cn(
                  "h-10 px-3 text-xs font-medium whitespace-nowrap text-muted-foreground",
                  col.numeric ? "text-right" : "text-left",
                )}
              >
                {col.card === "actions" ? <span className="sr-only">{col.header}</span> : col.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {loading
            ? Array.from({ length: SKELETON_ROWS }, (_, i) => (
                <tr key={i} className="border-b last:border-0">
                  {columns.map((col) => (
                    <td key={col.key} className="h-10 px-3">
                      <Skeleton className={cn("h-4", col.numeric ? "ml-auto w-12" : "w-3/4")} />
                    </td>
                  ))}
                </tr>
              ))
            : rows.map((row) => (
                <tr key={rowKey(row)} className="border-b align-top last:border-0">
                  {columns.map((col) => (
                    <td
                      key={col.key}
                      className={cn(
                        "px-3 py-2.5",
                        col.numeric && "text-right tabular-nums",
                        col.card === "actions" && "w-px text-right whitespace-nowrap",
                      )}
                    >
                      {col.cell(row)}
                    </td>
                  ))}
                </tr>
              ))}
        </tbody>
      </table>
    </div>
  );
}

function CardList<T>({ caption, columns, rows, rowKey, loading }: ViewProps<T>) {
  const title = columns.find((col) => col.card === "title");
  const actions = columns.find((col) => col.card === "actions");
  const fields = columns.filter((col) => col !== title && col !== actions);

  if (loading) {
    return (
      <div className="flex flex-col gap-3" aria-hidden="true">
        {Array.from({ length: 3 }, (_, i) => (
          <div key={i} className="space-y-3 rounded-lg border bg-card p-4">
            <Skeleton className="h-5 w-2/3" />
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-1/2" />
          </div>
        ))}
      </div>
    );
  }

  return (
    <ul aria-label={caption} className="flex flex-col gap-3">
      {rows.map((row) => (
        <li key={rowKey(row)} className="rounded-lg border bg-card p-4 shadow-xs">
          {title && <div className="mb-3 font-medium">{title.cell(row)}</div>}
          <dl className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-x-4 gap-y-2 text-sm">
            {fields.map((col) => (
              <div key={col.key} className="contents">
                <dt className="text-xs text-muted-foreground">{col.header}</dt>
                <dd className={cn("min-w-0 break-words", col.numeric && "tabular-nums")}>{col.cell(row)}</dd>
              </div>
            ))}
          </dl>
          {actions && <div className="mt-3 flex flex-wrap gap-2">{actions.cell(row)}</div>}
        </li>
      ))}
    </ul>
  );
}
