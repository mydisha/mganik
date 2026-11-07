import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { Link } from "react-router";
import { toast } from "sonner";
import { z } from "zod";
import { useCan } from "@/features/auth/session";
import { errorMessage, fieldErrors } from "@/shared/lib/api";
import { codePointLength, formatNumber } from "@/shared/lib/format";
import { Alert } from "@/shared/ui/alert";
import { Button } from "@/shared/ui/button";
import type { DataColumn } from "@/shared/ui/data-table";
import { FormField } from "@/shared/ui/form-field";
import { Textarea } from "@/shared/ui/input";
import { PageHeader } from "@/shared/ui/page-header";
import { SidePanel } from "@/shared/ui/side-panel";
import { Spinner } from "@/shared/ui/spinner";
import {
  type AnomalyItem,
  type AnomalyQuery,
  type AnomalySeverity,
  type AnomalyStatus,
  ccKeys,
  listAnomalies,
  updateAnomaly,
} from "./api";
import { FilterBar, SelectFilter, useUrlFilters } from "./filters";
import { perm } from "./nav";
import { RecordList } from "./record-list";
import { consoleStrings as t } from "./strings";
import { useCursorList } from "./use-cursor-list";
import { AnomalyStatusBadge, EmptyValue, FactList, ListSection, SeverityBadge, TimeText } from "./ui";

const KEYS = ["status", "prioritas", "aturan"] as const;
const ALL = "SEMUA";
const STATUSES: AnomalyStatus[] = ["OPEN", "ACK", "RESOLVED", "FALSE_POSITIVE"];
const SEVERITIES: AnomalySeverity[] = ["CRITICAL", "HIGH", "MEDIUM", "LOW"];

type Action = "ACK" | "RESOLVED" | "FALSE_POSITIVE";
const NEXT: Record<AnomalyStatus, Action[]> = {
  OPEN: ["ACK", "RESOLVED", "FALSE_POSITIVE"],
  ACK: ["RESOLVED", "FALSE_POSITIVE"],
  RESOLVED: [],
  FALSE_POSITIVE: [],
};

const p = t.anomalies.panel;

const actionSchema = z
  .object({
    action: z.enum(["ACK", "RESOLVED", "FALSE_POSITIVE"], { error: p.actionRequired }),
    note: z.string().refine((v) => codePointLength(v.trim()) <= 1000, p.noteTooLong),
  })
  .superRefine((value, ctx) => {
    if (value.action !== "ACK" && value.note.trim() === "") {
      ctx.addIssue({ code: "custom", path: ["note"], message: p.noteRequired });
    }
  });

type ActionValues = z.input<typeof actionSchema>;

function ActionForm({ anomaly, onDone, onPendingChange }: {
  anomaly: AnomalyItem;
  onDone: (updated: AnomalyItem) => void;
  onPendingChange: (pending: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const form = useForm<ActionValues>({
    resolver: zodResolver(actionSchema),
    defaultValues: { action: undefined, note: "" },
  });
  const action = form.watch("action");

  const save = useMutation({
    mutationFn: (values: { status: Action; note: string }) =>
      updateAnomaly(anomaly.id, { status: values.status, note: values.note === "" ? null : values.note }),
    onMutate: () => onPendingChange(true),
    onSettled: () => {
      onPendingChange(false);
      void queryClient.invalidateQueries({ queryKey: ccKeys.all });
    },
    onSuccess: (updated, values) => {
      toast.success(p.done[values.status]);
      onDone(updated);
    },
    onError: (error) => {
      const message = fieldErrors(error).note;
      if (message) form.setError("note", { message });
    },
  });

  const noteRequired = action !== undefined && action !== "ACK";
  const errors = form.formState.errors;

  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      onSubmit={form.handleSubmit((values) => save.mutate({ status: values.action, note: values.note.trim() }))}
    >
      <h3 className="text-sm font-semibold">{p.actionHeading}</h3>
      {save.isError && <Alert tone="danger">{errorMessage(save.error)}</Alert>}
      <fieldset className="flex flex-col gap-1" aria-describedby={errors.action ? "anomaly-action-error" : undefined}>
        <legend className="mb-1 text-sm font-medium">{p.action}</legend>
        {NEXT[anomaly.status].map((option) => (
          <label key={option} className="flex items-center gap-3 rounded-md px-1 py-1.5 text-sm max-md:min-h-11">
            <input type="radio" value={option} className="size-4 accent-primary" disabled={save.isPending} {...form.register("action")} />
            {p.actions[option]}
          </label>
        ))}
        {errors.action && (
          <p id="anomaly-action-error" className="text-xs font-medium text-danger-foreground">
            {errors.action.message}
          </p>
        )}
      </fieldset>
      <FormField
        id="anomaly-note"
        label={p.note}
        hint={noteRequired ? p.noteHintRequired : p.noteHintOptional}
        error={errors.note?.message}
      >
        {(control) => <Textarea {...control} rows={4} disabled={save.isPending} {...form.register("note")} />}
      </FormField>
      <Button type="submit" className="self-start" disabled={save.isPending}>
        {save.isPending && <Spinner />}
        {save.isPending ? p.submitting : p.submit}
      </Button>
    </form>
  );
}

function AnomalyPanel({ anomaly, onClose, onUpdated }: {
  anomaly: AnomalyItem | null;
  onClose: () => void;
  onUpdated: (item: AnomalyItem) => void;
}) {
  const canResolve = useCan(perm.anomalyResolve);
  const [pending, setPending] = useState(false);

  return (
    <SidePanel
      open={anomaly !== null}
      onOpenChange={(open) => !open && onClose()}
      title={anomaly?.rule_name ?? ""}
      description={anomaly ? `${t.anomalies.severities[anomaly.severity]} · ${t.anomalies.statuses[anomaly.status]}` : undefined}
      locked={pending}
    >
      {anomaly && (
        <div className="flex flex-col gap-6">
          <section className="flex flex-col gap-3">
            <div className="flex flex-wrap gap-2">
              <SeverityBadge severity={anomaly.severity} />
              <AnomalyStatusBadge status={anomaly.status} />
            </div>
            <h3 className="text-sm font-semibold">{p.facts}</h3>
            <FactList
              items={[
                { label: p.party, value: anomaly.party_name },
                { label: p.region, value: anomaly.region_name },
                { label: p.hits, value: formatNumber(anomaly.hit_count) },
                { label: p.firstSeen, value: <TimeText iso={anomaly.first_seen_at} /> },
                { label: p.lastSeen, value: <TimeText iso={anomaly.last_seen_at} /> },
                ...(anomaly.resolved_at
                  ? [
                      { label: p.resolvedAt, value: <TimeText iso={anomaly.resolved_at} /> },
                      { label: p.resolution, value: anomaly.resolution_note },
                    ]
                  : []),
              ]}
            />
          </section>
          <section className="flex flex-col gap-3">
            <h3 className="text-sm font-semibold">{p.evidence}</h3>
            <RecordList data={anomaly.evidence} empty={p.evidenceEmpty} />
          </section>
          {canResolve &&
            (NEXT[anomaly.status].length > 0 ? (
              <ActionForm key={`${anomaly.id}:${anomaly.version}`} anomaly={anomaly} onDone={onUpdated} onPendingChange={setPending} />
            ) : (
              <p className="text-sm text-muted-foreground">{p.closedInfo}</p>
            ))}
        </div>
      )}
    </SidePanel>
  );
}

export function AnomaliesPage() {
  const filters = useUrlFilters(KEYS);
  const [selected, setSelected] = useState<AnomalyItem | null>(null);
  const status = filters.values.status === ALL ? undefined : ((filters.values.status || "OPEN") as AnomalyStatus);
  const query: AnomalyQuery = {
    status,
    severity: (SEVERITIES as string[]).includes(filters.values.prioritas) ? (filters.values.prioritas as AnomalySeverity) : undefined,
    rule: filters.values.aturan || undefined,
  };
  const list = useCursorList(ccKeys.anomalies(query), (cursor) => listAnomalies({ ...query, cursor }));
  const ruleName = list.rows.find((row) => row.rule_code === filters.values.aturan)?.rule_name;

  const columns: DataColumn<AnomalyItem>[] = [
    {
      key: "rule",
      header: t.anomalies.columns.rule,
      card: "title",
      cell: (row) => (
        <Link
          to={filters.hrefWith("aturan", row.rule_code)}
          title={t.filters.onlyRule(row.rule_name)}
          className="font-medium underline-offset-4 hover:text-link hover:underline"
        >
          {row.rule_name}
        </Link>
      ),
    },
    { key: "severity", header: t.anomalies.columns.severity, cell: (row) => <SeverityBadge severity={row.severity} /> },
    { key: "status", header: t.anomalies.columns.status, cell: (row) => <AnomalyStatusBadge status={row.status} /> },
    { key: "subject", header: t.anomalies.columns.subject, cell: (row) => row.party_name ?? row.region_name ?? <EmptyValue /> },
    { key: "hits", header: t.anomalies.columns.hits, numeric: true, cell: (row) => formatNumber(row.hit_count) },
    { key: "lastSeen", header: t.anomalies.columns.lastSeen, cell: (row) => <TimeText iso={row.last_seen_at} /> },
    {
      key: "actions",
      header: t.anomalies.columns.actions,
      card: "actions",
      cell: (row) => (
        <Button variant="outline" size="sm" onClick={() => setSelected(row)} aria-label={t.anomalies.detailFor(row.rule_name)}>
          {t.anomalies.detail}
        </Button>
      ),
    },
  ];

  const chips = filters.values.aturan
    ? [{ key: "aturan", label: t.filters.rule(ruleName ?? t.filters.selectedRule), onRemove: () => filters.set("aturan", "") }]
    : [];

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={t.anomalies.title} description={t.anomalies.description} />
      <FilterBar chips={chips} active={filters.active} onClear={filters.clear}>
        <SelectFilter
          label={t.anomalies.status}
          value={filters.values.status || "OPEN"}
          allLabel={t.anomalies.allStatuses}
          allValue={ALL}
          options={STATUSES.map((value) => ({ value, label: t.anomalies.statuses[value] }))}
          onChange={(value) => filters.set("status", value === "OPEN" ? "" : value)}
        />
        <SelectFilter
          label={t.anomalies.severity}
          value={filters.values.prioritas}
          allLabel={t.anomalies.allSeverities}
          options={SEVERITIES.map((value) => ({ value, label: t.anomalies.severities[value] }))}
          onChange={(value) => filters.set("prioritas", value)}
        />
      </FilterBar>
      <ListSection
        query={list.query}
        rows={list.rows}
        caption={t.anomalies.caption}
        columns={columns}
        rowKey={(row) => row.id}
        loadMore={list.loadMore}
        empty={{ title: t.anomalies.empty, description: t.anomalies.emptyHint }}
        onClearFilters={filters.active ? filters.clear : undefined}
      />
      <AnomalyPanel anomaly={selected} onClose={() => setSelected(null)} onUpdated={setSelected} />
    </div>
  );
}
