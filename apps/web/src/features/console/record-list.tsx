import { consoleStrings as t } from "./strings";
import { EmptyValue } from "./ui";

function Value({ value }: { value: unknown }) {
  if (value === null || value === undefined || value === "") return <EmptyValue />;
  if (typeof value === "boolean") return <>{value ? t.yes : t.no}</>;
  if (typeof value === "string" || typeof value === "number") return <span className="break-all">{String(value)}</span>;
  return (
    <pre className="max-h-48 overflow-auto rounded-md bg-muted p-2 font-mono text-xs whitespace-pre-wrap break-all">
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}

/**
 * Bukti anomali atau rincian konflik (objek bebas dari server) sebagai daftar
 * label/nilai. Kunci yang dikenal diberi label; sisanya tampil apa adanya.
 */
export function RecordList({ data, empty }: { data: Record<string, unknown>; empty: string }) {
  const entries = Object.entries(data);
  if (entries.length === 0) return <p className="text-sm text-muted-foreground">{empty}</p>;
  return (
    <dl className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-x-4 gap-y-2 text-sm">
      {entries.map(([key, value]) => (
        <div key={key} className="contents">
          <dt className="text-muted-foreground">{t.recordKeys[key] ?? <span className="font-mono text-xs">{key}</span>}</dt>
          <dd className="min-w-0">
            <Value value={value} />
          </dd>
        </div>
      ))}
    </dl>
  );
}
