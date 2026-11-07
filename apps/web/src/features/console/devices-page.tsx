import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";
import { useSession } from "@/features/auth/session";
import { errorMessage, fieldErrors } from "@/shared/lib/api";
import { codePointLength, formatDate } from "@/shared/lib/format";
import { strings } from "@/shared/lib/strings";
import { Alert } from "@/shared/ui/alert";
import { Button } from "@/shared/ui/button";
import { ConfirmDialog } from "@/shared/ui/confirm-dialog";
import type { DataColumn } from "@/shared/ui/data-table";
import { Dialog, DialogContent } from "@/shared/ui/dialog";
import { FormField } from "@/shared/ui/form-field";
import { Input, Textarea } from "@/shared/ui/input";
import { PageHeader } from "@/shared/ui/page-header";
import { SecretRevealDialog } from "@/shared/ui/secret-reveal-dialog";
import { Select } from "@/shared/ui/select";
import { Spinner } from "@/shared/ui/spinner";
import { ccKeys, type Device, type DeviceProvisioned, listDevices, listStock, provisionDevice, revokeDevice } from "./api";
import { perm } from "./nav";
import { consoleStrings as t } from "./strings";
import { useCursorList } from "./use-cursor-list";
import { DeviceStatusBadge, ListSection, LocationCell, TimeText } from "./ui";

const d = t.devices;
const STORE_PAGES = 5;

interface StoreOption {
  id: string;
  name: string;
  code: string;
}

/**
 * Kontrak belum punya daftar lokasi. Toko diambil dari saldo stok bertipe
 * OWN_STORE (butuh cc.read, sudah dibatasi scope), atau dari scope lokasi
 * pengguna. Server tetap menolak lokasi yang bukan toko (422).
 */
function useStoreOptions(enabled: boolean) {
  const me = useSession();
  const canReadStock = me.permissions.includes(perm.ccRead);
  return useQuery({
    queryKey: ccKeys.stores(),
    enabled,
    staleTime: 5 * 60_000,
    queryFn: async (): Promise<StoreOption[]> => {
      const stores = new Map<string, StoreOption>();
      if (canReadStock) {
        let cursor: string | undefined;
        for (let page = 0; page < STORE_PAGES; page++) {
          const result = await listStock({ location_type: "OWN_STORE", limit: 200, cursor });
          for (const row of result.items) {
            stores.set(row.location_id, { id: row.location_id, name: row.location_name, code: row.location_code });
          }
          if (!result.next_cursor) break;
          cursor = result.next_cursor;
        }
      } else if (me.scope.mode === "SELECTED") {
        for (const loc of me.scope.locations) stores.set(loc.id, loc);
      }
      return [...stores.values()].sort((a, b) => a.name.localeCompare(b.name, "id"));
    },
  });
}

const registerSchema = z.object({
  location_id: z.string().min(1, d.form.storeRequired),
  name: z
    .string()
    .trim()
    .min(1, d.form.nameRequired)
    .refine((v) => codePointLength(v) <= 100, d.form.nameTooLong),
});
type RegisterValues = z.input<typeof registerSchema>;

function RegisterDialog({ open, onOpenChange, onProvisioned }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onProvisioned: (result: DeviceProvisioned) => void;
}) {
  const queryClient = useQueryClient();
  const stores = useStoreOptions(open);
  const form = useForm<RegisterValues>({ resolver: zodResolver(registerSchema), defaultValues: { location_id: "", name: "" } });
  const save = useMutation({
    mutationFn: provisionDevice,
    // Token dan signing key hanya boleh hidup di dialog reveal: jangan
    // disimpan di MutationCache setelah diserahkan.
    gcTime: 0,
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: ccKeys.devices() });
      form.reset();
      onProvisioned(result);
      queueMicrotask(() => save.reset());
    },
    onError: (error) => {
      const fields = fieldErrors(error);
      if (fields.name) form.setError("name", { message: fields.name });
      if (fields.location_id) form.setError("location_id", { message: fields.location_id });
    },
  });
  const errors = form.formState.errors;
  const pending = save.isPending;

  function change(next: boolean) {
    if (pending) return;
    if (!next) {
      form.reset();
      save.reset();
    }
    onOpenChange(next);
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogContent
        title={d.form.title}
        description={d.form.description}
        footer={
          <>
            <Button variant="outline" onClick={() => change(false)} disabled={pending}>
              {strings.common.cancel}
            </Button>
            <Button type="submit" form="register-device" disabled={pending || !stores.data?.length}>
              {pending && <Spinner />}
              {pending ? d.form.submitting : d.form.submit}
            </Button>
          </>
        }
      >
        <form
          id="register-device"
          noValidate
          className="flex flex-col gap-4"
          onSubmit={form.handleSubmit((values) => save.mutate({ location_id: values.location_id, name: values.name.trim() }))}
        >
          {save.isError && <Alert tone="danger">{errorMessage(save.error)}</Alert>}
          {stores.isError && <Alert tone="danger">{`${d.form.storesFailed} ${errorMessage(stores.error)}`}</Alert>}
          {stores.isSuccess && stores.data.length === 0 && <Alert tone="warning">{d.form.storesEmpty}</Alert>}
          <FormField
            id="device-store"
            label={d.form.store}
            hint={stores.isPending ? d.form.storesLoading : d.form.storeHint}
            error={errors.location_id?.message}
          >
            {(control) => (
              <Select {...control} disabled={pending || !stores.isSuccess} {...form.register("location_id")}>
                <option value="">{d.form.storePlaceholder}</option>
                {stores.data?.map((store) => (
                  <option key={store.id} value={store.id}>
                    {store.name} ({store.code})
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="device-name" label={d.form.name} hint={d.form.nameHint} error={errors.name?.message}>
            {(control) => <Input {...control} autoComplete="off" disabled={pending} {...form.register("name")} />}
          </FormField>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function RevokeDialog({ device, onClose }: { device: Device | null; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [reason, setReason] = useState("");
  const tooLong = codePointLength(reason.trim()) > 500;
  const revoke = useMutation({
    mutationFn: (target: Device) => revokeDevice(target.id, reason.trim()),
    onSuccess: (updated) => {
      toast.success(d.revokeDialog.done(updated.name));
      void queryClient.invalidateQueries({ queryKey: ccKeys.devices() });
      close();
    },
  });

  function close() {
    setReason("");
    revoke.reset();
    onClose();
  }

  if (!device) return null;
  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => !open && close()}
      title={d.revokeDialog.title(device.name)}
      description={d.revokeDialog.description}
      confirmText={device.name}
      confirmTextLabel={d.revokeDialog.confirmTextLabel(device.name)}
      confirmLabel={d.revokeDialog.confirm}
      pendingLabel={d.revokeDialog.pending}
      pending={revoke.isPending}
      error={revoke.isError ? errorMessage(revoke.error) : null}
      onConfirm={() => !tooLong && revoke.mutate(device)}
    >
      <FormField
        id="revoke-reason"
        label={d.revokeDialog.reason}
        hint={d.revokeDialog.reasonHint}
        error={tooLong ? d.revokeDialog.reasonTooLong : undefined}
      >
        {(control) => (
          <Textarea {...control} rows={3} value={reason} onChange={(e) => setReason(e.target.value)} disabled={revoke.isPending} />
        )}
      </FormField>
    </ConfirmDialog>
  );
}

export function DevicesPage() {
  const [registerOpen, setRegisterOpen] = useState(false);
  const [provisioned, setProvisioned] = useState<DeviceProvisioned | null>(null);
  const [revoking, setRevoking] = useState<Device | null>(null);
  const list = useCursorList(ccKeys.devices(), (cursor) => listDevices({ cursor }));

  const columns: DataColumn<Device>[] = [
    { key: "name", header: d.columns.name, card: "title", cell: (row) => <span className="font-medium break-words">{row.name}</span> },
    { key: "store", header: d.columns.store, cell: (row) => <LocationCell name={row.location_name} code={row.location_code} /> },
    {
      key: "status",
      header: d.columns.status,
      cell: (row) => (
        <span className="inline-flex flex-col gap-1">
          <DeviceStatusBadge status={row.status} />
          {row.revoked_at && <span className="text-xs text-muted-foreground">{d.revokedAt(formatDate(row.revoked_at))}</span>}
        </span>
      ),
    },
    {
      key: "lastSeen",
      header: d.columns.lastSeen,
      cell: (row) => (row.last_seen_at ? <TimeText iso={row.last_seen_at} /> : <span className="text-muted-foreground">{d.neverSeen}</span>),
    },
    { key: "created", header: d.columns.created, cell: (row) => <TimeText iso={row.created_at} /> },
    {
      key: "actions",
      header: d.columns.actions,
      card: "actions",
      cell: (row) =>
        row.status === "ACTIVE" ? (
          <Button variant="outline" size="sm" onClick={() => setRevoking(row)} aria-label={d.revokeFor(row.name)}>
            {d.revoke}
          </Button>
        ) : null,
    },
  ];

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={d.title} description={d.description} actions={<Button onClick={() => setRegisterOpen(true)}>{d.register}</Button>} />
      <ListSection
        query={list.query}
        rows={list.rows}
        caption={d.caption}
        columns={columns}
        rowKey={(row) => row.id}
        loadMore={list.loadMore}
        empty={{ title: d.empty, description: d.emptyHint }}
      />
      <RegisterDialog
        open={registerOpen}
        onOpenChange={setRegisterOpen}
        onProvisioned={(result) => {
          setRegisterOpen(false);
          setProvisioned(result);
        }}
      />
      {provisioned && (
        <SecretRevealDialog
          open
          title={d.reveal.title(provisioned.device.name)}
          description={d.reveal.description(provisioned.device.location_name)}
          warning={d.reveal.warning}
          secrets={[
            { label: d.reveal.token, value: provisioned.token },
            { label: d.reveal.signingKey, value: provisioned.signing_key },
          ]}
          acknowledgeLabel={d.reveal.acknowledge}
          closeLabel={d.reveal.close}
          onClose={() => setProvisioned(null)}
        />
      )}
      <RevokeDialog device={revoking} onClose={() => setRevoking(null)} />
    </div>
  );
}
