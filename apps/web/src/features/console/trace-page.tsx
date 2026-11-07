import { useQuery } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { useSearchParams } from "react-router";
import { errorMessage, errorStatus } from "@/shared/lib/api";
import { formatDate } from "@/shared/lib/format";
import { Button } from "@/shared/ui/button";
import { CodeText } from "@/shared/ui/copy-button";
import { DataTable, type DataColumn } from "@/shared/ui/data-table";
import { EmptyState } from "@/shared/ui/empty-state";
import { ErrorState } from "@/shared/ui/error-state";
import { FormField } from "@/shared/ui/form-field";
import { Input } from "@/shared/ui/input";
import { PageHeader } from "@/shared/ui/page-header";
import { Skeleton } from "@/shared/ui/skeleton";
import { ccKeys, type Trace, traceSerial } from "./api";
import { consoleStrings as t } from "./strings";
import { EmptyValue, FactList, TimeText } from "./ui";

type TraceEvent = Trace["events"][number];
type TraceShipment = Trace["shipments"][number];
type TraceScan = Trace["scans"][number];

const tr = t.trace;
const status = (code: string | null) => (code ? (t.serialStatus[code] ?? code) : null);
const level = (code: string) => t.packagingLevel[code] ?? code;

function summaryText(trace: Trace): string {
  const s = trace.serial;
  const chain = [...trace.ancestors]
    .sort((a, b) => a.depth - b.depth)
    .map((a) => `${level(a.level_code).toLowerCase()} ${a.code}`)
    .join(", di dalam ");
  return (
    tr.summary.intro(level(s.level_code), status(s.status) ?? s.status) +
    (s.location?.name ? tr.summary.at(s.location.name) : "") +
    (s.custodian?.name ? tr.summary.holder(s.custodian.name) : "") +
    "." +
    (chain ? tr.summary.inside(chain) : tr.summary.top)
  );
}

function SummarySection({ trace }: { trace: Trace }) {
  const s = trace.serial;
  return (
    <section aria-labelledby="trace-summary" className="space-y-4 rounded-lg border bg-card p-5 shadow-xs">
      <h2 id="trace-summary" className="text-base font-semibold">
        {tr.summaryHeading}
      </h2>
      <p className="text-base leading-7">{summaryText(trace)}</p>
      <FactList
        items={[
          { label: tr.facts.code, value: <CodeText value={s.code} label={tr.copyLabel} /> },
          { label: tr.facts.product, value: s.product?.name && `${s.product.name}${s.product.code ? ` (${s.product.code})` : ""}` },
          { label: tr.facts.batch, value: s.batch?.lot_no },
          { label: tr.facts.expiry, value: s.batch?.exp_date && formatDate(s.batch.exp_date) },
          { label: tr.facts.location, value: s.location?.name },
          { label: tr.facts.custodian, value: s.custodian?.name },
          { label: tr.facts.commissioned, value: s.commissioned_at && <TimeText iso={s.commissioned_at} /> },
          { label: tr.facts.verifyCount, value: tr.facts.verifyCountValue(s.verify_count) },
          {
            label: tr.facts.firstVerified,
            value: s.first_verified_at && (
              <>
                <TimeText iso={s.first_verified_at} />
                {s.first_verified_city && `, ${s.first_verified_city}`}
              </>
            ),
          },
        ]}
      />
      <div className="space-y-2">
        <h3 className="text-sm font-semibold">{tr.contents}</h3>
        {trace.descendants.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tr.contentsEmpty}</p>
        ) : (
          <ul className="list-disc space-y-1 pl-5 text-sm">
            {trace.descendants.map((d) => (
              <li key={`${d.level_code}:${d.status}`}>{tr.contentsRow(d.count, level(d.level_code), status(d.status) ?? d.status)}</li>
            ))}
          </ul>
        )}
      </div>
    </section>
  );
}

const eventColumns: DataColumn<TraceEvent>[] = [
  { key: "step", header: tr.eventColumns.step, card: "title", cell: (e) => <span className="font-medium">{t.bizStep[e.biz_step] ?? e.biz_step}</span> },
  { key: "time", header: tr.eventColumns.time, cell: (e) => <TimeText iso={e.occurred_at} /> },
  {
    key: "status",
    header: tr.eventColumns.status,
    cell: (e) =>
      e.from_status || e.to_status ? (
        tr.statusChange(status(e.from_status) ?? tr.statusStart, status(e.to_status) ?? tr.statusSame)
      ) : (
        <EmptyValue />
      ),
  },
  {
    key: "where",
    header: tr.eventColumns.where,
    cell: (e) =>
      e.location_code || e.party_name ? (
        <span>
          {e.location_code && <span className="font-mono text-xs">{e.location_code}</span>}
          {e.location_code && e.party_name && " · "}
          {e.party_name}
        </span>
      ) : (
        <EmptyValue />
      ),
  },
  { key: "actor", header: tr.eventColumns.actor, cell: (e) => tr.actors[e.actor_type] },
];

const shipmentColumns: DataColumn<TraceShipment>[] = [
  { key: "code", header: tr.shipmentColumns.code, card: "title", cell: (s) => <span className="font-mono font-medium">{s.code}</span> },
  {
    key: "route",
    header: tr.shipmentColumns.route,
    cell: (s) => (
      <span>
        {tr.route(s.from_location_code, s.to_location_code)}
        {s.to_party_name && <span className="block text-xs text-muted-foreground">{s.to_party_name}</span>}
      </span>
    ),
  },
  { key: "program", header: tr.shipmentColumns.program, cell: (s) => (s.program_code ? <span className="font-mono text-xs">{s.program_code}</span> : <EmptyValue />) },
  { key: "via", header: tr.shipmentColumns.via, cell: (s) => <span className="font-mono text-xs break-all">{s.via_code}</span> },
  { key: "shipped", header: tr.shipmentColumns.shipped, cell: (s) => <TimeText iso={s.shipped_at} /> },
  { key: "received", header: tr.shipmentColumns.received, cell: (s) => <TimeText iso={s.received_at} /> },
];

const scanColumns: DataColumn<TraceScan>[] = [
  { key: "time", header: tr.scanColumns.time, card: "title", cell: (s) => <TimeText iso={s.occurred_at} /> },
  { key: "result", header: tr.scanColumns.result, cell: (s) => t.verifyResult[s.result] },
  { key: "region", header: tr.scanColumns.region, cell: (s) => s.region_name ?? <EmptyValue /> },
  { key: "source", header: tr.scanColumns.source, cell: (s) => tr.geoSource[s.geo_source] },
  {
    key: "rules",
    header: tr.scanColumns.rules,
    cell: (s) => (s.rule_hits.length > 0 ? <span className="font-mono text-xs break-all">{s.rule_hits.join(", ")}</span> : <EmptyValue />),
  },
];

function TraceResult({ code }: { code: string }) {
  const trace = useQuery({ queryKey: ccKeys.trace(code), queryFn: () => traceSerial(code) });

  if (trace.isPending) {
    return (
      <div className="space-y-4" aria-busy="true">
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
    );
  }
  if (trace.isError) {
    if (errorStatus(trace.error) === 404) return <EmptyState title={tr.notFound} description={errorMessage(trace.error)} />;
    return <ErrorState error={trace.error} onRetry={() => void trace.refetch()} />;
  }

  const data = trace.data;
  const events = [...data.events].sort((a, b) => a.occurred_at.localeCompare(b.occurred_at));

  return (
    <div className="flex flex-col gap-6">
      <SummarySection trace={data} />
      <section aria-labelledby="trace-history" className="space-y-3">
        <h2 id="trace-history" className="text-base font-semibold">
          {tr.historyHeading}
        </h2>
        {events.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tr.historyEmpty}</p>
        ) : (
          <DataTable caption={tr.historyCaption} columns={eventColumns} rows={events} rowKey={(e) => e.id} />
        )}
      </section>
      <section aria-labelledby="trace-shipments" className="space-y-3">
        <h2 id="trace-shipments" className="text-base font-semibold">
          {tr.shipmentsHeading}
        </h2>
        {data.shipments.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tr.shipmentsEmpty}</p>
        ) : (
          <DataTable caption={tr.shipmentsCaption} columns={shipmentColumns} rows={data.shipments} rowKey={(s) => `${s.id}:${s.via_code}`} />
        )}
        <h3 className="pt-2 text-sm font-semibold">{tr.scansHeading}</h3>
        {data.scans.length === 0 ? (
          <p className="text-sm text-muted-foreground">{tr.scansEmpty}</p>
        ) : (
          <DataTable caption={tr.scansCaption} columns={scanColumns} rows={data.scans} rowKey={(s) => s.id} />
        )}
      </section>
    </div>
  );
}

export function TracePage() {
  const [params, setParams] = useSearchParams();
  const code = params.get("kode")?.trim() ?? "";
  const [input, setInput] = useState(code);
  const [error, setError] = useState<string | null>(null);

  function submit(event: FormEvent) {
    event.preventDefault();
    const value = input.trim();
    if (!value) {
      setError(tr.codeRequired);
      return;
    }
    setError(null);
    setParams({ kode: value });
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={tr.title} description={tr.description} />
      <form noValidate onSubmit={submit} className="flex flex-col gap-3 sm:flex-row sm:items-start">
        <FormField id="trace-code" label={tr.codeLabel} hint={tr.codeHint} error={error ?? undefined} className="sm:w-96">
          {(control) => (
            <Input
              {...control}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              autoComplete="off"
              autoCapitalize="characters"
              spellCheck={false}
              maxLength={64}
              className="font-mono"
            />
          )}
        </FormField>
        <Button type="submit" className="sm:mt-6.5">
          {tr.submit}
        </Button>
      </form>
      {code ? <TraceResult key={code} code={code} /> : <EmptyState title={tr.idle} description={tr.idleHint} />}
    </div>
  );
}
