import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useForm } from "react-hook-form";
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
import { ccKeys, type ConflictItem, type ConflictQuery, type ConflictResolve, listConflicts, resolveConflict } from "./api";
import { FilterBar, SelectFilter, useUrlFilters } from "./filters";
import { perm } from "./nav";
import { RecordList } from "./record-list";
import { consoleStrings as t } from "./strings";
import { useCursorList } from "./use-cursor-list";
import { ConflictStatusBadge, EmptyValue, FactList, ListSection, LocationCell, ProductCell, TimeText } from "./ui";

const KEYS = ["status", "jenis"] as const;
const ALL = "SEMUA";
type Status = ConflictItem["status"];
type Kind = ConflictItem["kind"];
type Resolution = ConflictResolve["resolution"];
const STATUSES: Status[] = ["OPEN", "AUTO_RESOLVED", "RESOLVED"];
const KINDS: Kind[] = ["OFFLINE_SALE_PREEMPT", "SERIAL_DOUBLE_SALE", "STOCK_DISCREPANCY"];
const RESOLUTIONS: Resolution[] = [
  "CUSTOMER_COMPENSATED",
  "ORDER_CANCELLED",
  "STOCK_RECOUNTED",
  "SERIAL_RECORD_CORRECTED",
  "FALSE_ALARM",
  "OTHER",
];

const p = t.conflicts.panel;

const resolveSchema = z.object({
  resolution: z.enum(RESOLUTIONS as [Resolution, ...Resolution[]], { error: p.chooseRequired }),
  note: z
    .string()
    .refine((v) => codePointLength(v.trim()) >= 3, p.noteTooShort)
    .refine((v) => codePointLength(v.trim()) <= 1000, p.noteTooLong),
});

type ResolveValues = z.input<typeof resolveSchema>;

function ResolveForm({ conflict, onDone, onPendingChange }: {
  conflict: ConflictItem;
  onDone: (updated: ConflictItem) => void;
  onPendingChange: (pending: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const form = useForm<ResolveValues>({ resolver: zodResolver(resolveSchema), defaultValues: { resolution: undefined, note: "" } });
  // ORDER_CANCELLED hanya sah untuk konflik yang punya pesanan online.
  const options = RESOLUTIONS.filter((r) => r !== "ORDER_CANCELLED" || conflict.order_id !== null);

  const save = useMutation({
    mutationFn: (body: ConflictResolve) => resolveConflict(conflict.id, body),
    onMutate: () => onPendingChange(true),
    onSettled: () => {
      onPendingChange(false);
      void queryClient.invalidateQueries({ queryKey: ccKeys.all });
    },
    onSuccess: (updated) => {
      toast.success(p.done);
      onDone(updated);
    },
    onError: (error) => {
      const message = fieldErrors(error).note;
      if (message) form.setError("note", { message });
    },
  });

  const errors = form.formState.errors;

  return (
    <form
      noValidate
      className="flex flex-col gap-4"
      onSubmit={form.handleSubmit((values) => save.mutate({ resolution: values.resolution, note: values.note.trim() }))}
    >
      <h3 className="text-sm font-semibold">{p.resolveHeading}</h3>
      {save.isError && <Alert tone="danger">{errorMessage(save.error)}</Alert>}
      <fieldset className="flex flex-col gap-1" aria-describedby={errors.resolution ? "conflict-resolution-error" : undefined}>
        <legend className="mb-1 text-sm font-medium">{p.choose}</legend>
        {options.map((option) => (
          <label key={option} className="flex items-start gap-3 rounded-md px-1 py-1.5 text-sm max-md:min-h-11">
            <input
              type="radio"
              value={option}
              className="mt-0.5 size-4 accent-primary"
              disabled={save.isPending}
              {...form.register("resolution")}
            />
            <span>
              {t.conflicts.resolutions[option]}
              {option === "ORDER_CANCELLED" && <span className="block text-xs text-muted-foreground">{p.orderCancelledHint}</span>}
            </span>
          </label>
        ))}
        {errors.resolution && (
          <p id="conflict-resolution-error" className="text-xs font-medium text-danger-foreground">
            {errors.resolution.message}
          </p>
        )}
      </fieldset>
      <FormField id="conflict-note" label={p.note} hint={p.noteHint} error={errors.note?.message}>
        {(control) => <Textarea {...control} rows={4} disabled={save.isPending} {...form.register("note")} />}
      </FormField>
      <Button type="submit" className="self-start" disabled={save.isPending}>
        {save.isPending && <Spinner />}
        {save.isPending ? p.submitting : p.submit}
      </Button>
    </form>
  );
}

function ConflictPanel({ conflict, onClose, onUpdated }: {
  conflict: ConflictItem | null;
  onClose: () => void;
  onUpdated: (item: ConflictItem) => void;
}) {
  const canResolve = useCan(perm.conflictResolve);
  const [pending, setPending] = useState(false);

  return (
    <SidePanel
      open={conflict !== null}
      onOpenChange={(open) => !open && onClose()}
      title={conflict ? t.conflicts.kinds[conflict.kind] : ""}
      description={conflict ? `${conflict.product_name} · ${conflict.location_name}` : undefined}
      locked={pending}
    >
      {conflict && (
        <div className="flex flex-col gap-6">
          <section className="flex flex-col gap-3">
            <div>
              <ConflictStatusBadge status={conflict.status} />
            </div>
            <h3 className="text-sm font-semibold">{p.facts}</h3>
            <FactList
              items={[
                { label: t.conflicts.columns.location, value: `${conflict.location_name} (${conflict.location_code})` },
                { label: t.conflicts.columns.product, value: `${conflict.product_name} (${conflict.sku})` },
                { label: t.conflicts.columns.qty, value: conflict.qty === null ? null : formatNumber(conflict.qty) },
                { label: t.conflicts.columns.created, value: <TimeText iso={conflict.created_at} /> },
                { label: p.order, value: conflict.order_id && <span className="font-mono text-xs break-all">{conflict.order_id}</span> },
                {
                  label: p.transaction,
                  value: conflict.pos_transaction_id && <span className="font-mono text-xs break-all">{conflict.pos_transaction_id}</span>,
                },
                ...(conflict.resolved_at
                  ? [
                      { label: p.resolvedAt, value: <TimeText iso={conflict.resolved_at} /> },
                      { label: p.resolution, value: conflict.resolution && (t.conflicts.resolutions[conflict.resolution] ?? conflict.resolution) },
                    ]
                  : []),
              ]}
            />
          </section>
          <section className="flex flex-col gap-3">
            <h3 className="text-sm font-semibold">{p.details}</h3>
            <RecordList data={conflict.details} empty={p.detailsEmpty} />
          </section>
          {conflict.status !== "OPEN" ? (
            <p className="text-sm text-muted-foreground">{p.closedInfo}</p>
          ) : canResolve ? (
            <ResolveForm key={conflict.id} conflict={conflict} onDone={onUpdated} onPendingChange={setPending} />
          ) : (
            <p className="text-sm text-muted-foreground">{p.noPermission}</p>
          )}
        </div>
      )}
    </SidePanel>
  );
}

export function ConflictsPage() {
  const filters = useUrlFilters(KEYS);
  const canResolve = useCan(perm.conflictResolve);
  const [selected, setSelected] = useState<ConflictItem | null>(null);
  const status = filters.values.status === ALL ? undefined : ((filters.values.status || "OPEN") as Status);
  const query: ConflictQuery = {
    status,
    kind: (KINDS as string[]).includes(filters.values.jenis) ? (filters.values.jenis as Kind) : undefined,
  };
  const list = useCursorList(ccKeys.conflicts(query), (cursor) => listConflicts({ ...query, cursor }));

  const columns: DataColumn<ConflictItem>[] = [
    { key: "kind", header: t.conflicts.columns.kind, card: "title", cell: (row) => <span className="font-medium">{t.conflicts.kinds[row.kind]}</span> },
    { key: "location", header: t.conflicts.columns.location, cell: (row) => <LocationCell name={row.location_name} code={row.location_code} /> },
    { key: "product", header: t.conflicts.columns.product, cell: (row) => <ProductCell name={row.product_name} sku={row.sku} /> },
    { key: "qty", header: t.conflicts.columns.qty, numeric: true, cell: (row) => (row.qty === null ? <EmptyValue /> : formatNumber(row.qty)) },
    { key: "status", header: t.conflicts.columns.status, cell: (row) => <ConflictStatusBadge status={row.status} /> },
    { key: "created", header: t.conflicts.columns.created, cell: (row) => <TimeText iso={row.created_at} /> },
    {
      key: "actions",
      header: t.conflicts.columns.actions,
      card: "actions",
      cell: (row) => {
        const resolvable = row.status === "OPEN" && canResolve;
        return (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setSelected(row)}
            aria-label={resolvable ? t.conflicts.resolveFor(row.product_name) : t.conflicts.viewFor(row.product_name)}
          >
            {resolvable ? t.conflicts.resolve : t.conflicts.view}
          </Button>
        );
      },
    },
  ];

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={t.conflicts.title} description={t.conflicts.description} />
      <FilterBar chips={[]} active={filters.active} onClear={filters.clear}>
        <SelectFilter
          label={t.conflicts.status}
          value={filters.values.status || "OPEN"}
          allLabel={t.conflicts.allStatuses}
          allValue={ALL}
          options={STATUSES.map((value) => ({ value, label: t.conflicts.statuses[value] }))}
          onChange={(value) => filters.set("status", value === "OPEN" ? "" : value)}
        />
        <SelectFilter
          label={t.conflicts.kind}
          value={filters.values.jenis}
          allLabel={t.conflicts.allKinds}
          options={KINDS.map((value) => ({ value, label: t.conflicts.kinds[value] }))}
          onChange={(value) => filters.set("jenis", value)}
        />
      </FilterBar>
      <ListSection
        query={list.query}
        rows={list.rows}
        caption={t.conflicts.caption}
        columns={columns}
        rowKey={(row) => row.id}
        loadMore={list.loadMore}
        empty={{ title: t.conflicts.empty, description: t.conflicts.emptyHint }}
        onClearFilters={filters.active ? filters.clear : undefined}
      />
      <ConflictPanel conflict={selected} onClose={() => setSelected(null)} onUpdated={setSelected} />
    </div>
  );
}
