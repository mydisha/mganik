import { CircleAlert, ShieldCheck, TriangleAlert } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { updateSyncMeta } from "./db";
import {
  exportOutbox,
  extractCredentials,
  isSigningKeyFormat,
  isTokenFormat,
  normalizeSigningKey,
  normalizeToken,
  setupDevice,
  wipeDevice,
} from "./device";
import { formatDateTime } from "./lib/format";
import { posStrings as s } from "./strings";
import { ConfirmDialog } from "./ui/pos-dialog";
import { PosButton } from "./ui/pos-button";
import { useDevice, useOutboxSummary, usePos, useSyncMeta } from "./use-pos-data";

const fieldClass =
  "w-full rounded-md border border-input bg-card px-4 py-3 font-mono text-base shadow-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 aria-invalid:border-danger-foreground";

function Notice({ tone, children }: { tone: "info" | "warning" | "danger"; children: ReactNode }) {
  const cls = {
    info: "border-info-border bg-info text-info-foreground",
    warning: "border-warning-border bg-warning text-warning-foreground",
    danger: "border-danger-border bg-danger text-danger-foreground",
  }[tone];
  const Icon = tone === "info" ? ShieldCheck : tone === "warning" ? TriangleAlert : CircleAlert;
  return (
    <div role={tone === "danger" ? "alert" : undefined} className={`flex items-start gap-2 rounded-md border p-3 text-sm ${cls}`}>
      <Icon aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
      <div>{children}</div>
    </div>
  );
}

function persistText(p: boolean | null): { tone: "info" | "warning"; text: string } {
  if (p === true) return { tone: "info", text: s.setup.persistGranted };
  if (p === false) return { tone: "warning", text: s.setup.persistDenied };
  return { tone: "warning", text: s.setup.persistUnknown };
}

function SetupForm() {
  const { db } = usePos();
  const navigate = useNavigate();
  const [token, setToken] = useState("");
  const [key, setKey] = useState("");
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const tokenOk = isTokenFormat(normalizeToken(token));
  const keyOk = isSigningKeyFormat(normalizeSigningKey(key));

  const onPasteBoth = (text: string) => {
    const found = extractCredentials(text);
    if (found.token && found.signingKey) {
      setToken(found.token);
      setKey(found.signingKey);
      return true;
    }
    return false;
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    setError(null);
    if (!tokenOk || !keyOk) return;
    setBusy(true);
    const res = await setupDevice(
      db,
      { token, signingKey: key },
      { baseUrl: globalThis.location.origin, fetch: (i, init) => globalThis.fetch(i, init) },
    );
    setBusy(false);
    if (res.ok) {
      navigate("/pos", { replace: true });
      return;
    }
    switch (res.error) {
      case "token_format":
        return setError(s.setup.tokenFormat);
      case "key_format":
        return setError(s.setup.keyFormat);
      case "network":
        return setError(s.setup.network);
      case "outbox_not_empty":
        return setError(s.setup.outboxNotEmpty);
      case "server":
        return setError(res.message ?? s.setup.network);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)} noValidate className="flex flex-col gap-5">
      <p className="text-base text-muted-foreground">{s.setup.intro}</p>
      <div className="flex flex-col gap-1.5">
        <label htmlFor="pos-token" className="text-sm font-medium">
          {s.setup.tokenLabel}
        </label>
        <input
          id="pos-token"
          className={fieldClass}
          value={token}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setToken(e.target.value)}
          onPaste={(e) => {
            if (onPasteBoth(e.clipboardData.getData("text"))) e.preventDefault();
          }}
          aria-invalid={touched && !tokenOk}
          aria-describedby="pos-token-hint pos-token-error"
        />
        <span id="pos-token-hint" className="text-xs text-muted-foreground">
          {s.setup.tokenHint}
        </span>
        {touched && !tokenOk && (
          <span id="pos-token-error" className="text-sm text-danger-foreground">
            {s.setup.tokenFormat}
          </span>
        )}
      </div>
      <div className="flex flex-col gap-1.5">
        <label htmlFor="pos-key" className="text-sm font-medium">
          {s.setup.keyLabel}
        </label>
        <input
          id="pos-key"
          className={fieldClass}
          value={key}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setKey(e.target.value)}
          onPaste={(e) => {
            if (onPasteBoth(e.clipboardData.getData("text"))) e.preventDefault();
          }}
          aria-invalid={touched && !keyOk}
          aria-describedby="pos-key-hint pos-key-error"
        />
        <span id="pos-key-hint" className="text-xs text-muted-foreground">
          {s.setup.keyHint}
        </span>
        {touched && !keyOk && (
          <span id="pos-key-error" className="text-sm text-danger-foreground">
            {s.setup.keyFormat}
          </span>
        )}
      </div>
      {error && <Notice tone="danger">{error}</Notice>}
      <PosButton type="submit" posSize="primary" disabled={busy} className="self-start">
        {busy ? s.setup.submitting : s.setup.submit}
      </PosButton>
      <section aria-labelledby="pos-key-storage" className="flex flex-col gap-1 border-t pt-4">
        <h2 id="pos-key-storage" className="text-base font-semibold">
          {s.setup.keyStorageTitle}
        </h2>
        <p className="text-sm text-muted-foreground">{s.setup.keyStorage}</p>
      </section>
    </form>
  );
}

function download(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1_000);
}

function DevicePanel() {
  const { db, engine } = usePos();
  const device = useDevice();
  const sync = useSyncMeta();
  const outbox = useOutboxSummary();
  const navigate = useNavigate();
  const [wipeOpen, setWipeOpen] = useState(false);
  const [requested, setRequested] = useState<number | null>(null);
  if (!device) return null;

  const tz = device.timezone;
  const unsent = outbox?.unacked ?? 0;
  const persist = persistText(device.persisted);
  const refreshing = requested !== null && (sync?.lastBootstrapAt ?? 0) < requested;

  const refresh = async () => {
    const at = Date.now();
    setRequested(at);
    await updateSyncMeta(db, { bootstrapRequestedAt: at });
    engine?.kick(true);
  };

  const doExport = async () => {
    download(await exportOutbox(db), `kasir-${device.locationCode}-transaksi-tertunda.json`);
  };

  const rows: [string, string][] = [
    [s.device.name, device.deviceName],
    [s.device.store, device.locationName],
    [s.device.storeCode, device.locationCode],
    [s.device.registered, formatDateTime(device.provisionedAt, tz)],
    [s.device.dataUpdated, sync?.lastBootstrapAt ? formatDateTime(sync.lastBootstrapAt, tz) : s.device.never],
  ];

  return (
    <div className="flex flex-col gap-6">
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-2 rounded-lg border bg-card p-4 text-sm">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-muted-foreground">{k}</dt>
            <dd className="break-words">{v}</dd>
          </div>
        ))}
      </dl>
      <Notice tone={persist.tone}>{persist.text}</Notice>
      <div className="flex flex-col gap-2">
        <PosButton posSize="pos" variant="outline" className="self-start" disabled={refreshing} onClick={() => void refresh()}>
          {refreshing ? s.device.refreshing : s.device.refresh}
        </PosButton>
        {sync?.online === false && <p className="text-sm text-muted-foreground">{s.device.refreshNeedsOnline}</p>}
      </div>
      <div className="flex flex-col gap-2">
        <PosButton posSize="pos" variant="outline" className="self-start" disabled={(outbox?.total ?? 0) === 0} onClick={() => void doExport()}>
          {s.device.export}
        </PosButton>
        <p className="text-sm text-muted-foreground">{s.device.exportHint(unsent)}</p>
      </div>
      <PosButton posSize="pos" variant="destructive" className="self-start" onClick={() => setWipeOpen(true)}>
        {s.device.wipe}
      </PosButton>
      <ConfirmDialog
        open={wipeOpen}
        onOpenChange={setWipeOpen}
        title={s.device.wipeTitle}
        body={s.device.wipeBody}
        confirmLabel={s.device.wipeConfirm}
        cancelLabel={s.device.cancel}
        onConfirm={() => {
          void (async () => {
            await engine?.stop();
            await wipeDevice(db);
            navigate("/pos/setup", { replace: true });
          })();
        }}
      >
        {unsent > 0 && (
          <div className="mt-3 flex flex-col gap-2">
            <Notice tone="danger">{s.device.wipeOutboxWarning(unsent)}</Notice>
            <PosButton posSize="touch" variant="outline" className="self-start" onClick={() => void doExport()}>
              {s.device.export}
            </PosButton>
          </div>
        )}
      </ConfirmDialog>
    </div>
  );
}

// /pos/setup: daftarkan perangkat (kalau belum) atau kelola perangkat ini.
export function SetupScreen() {
  const device = useDevice();
  return (
    <main className="mx-auto flex min-h-dvh w-full max-w-2xl flex-col gap-6 px-4 py-8 md:px-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <p className="text-sm font-semibold">{s.brand}</p>
          <h1 className="text-xl font-semibold">{device ? s.device.title : s.setup.title}</h1>
        </div>
        {device && (
          <PosButton posSize="touch" variant="outline" asChild>
            <Link to="/pos">{s.device.back}</Link>
          </PosButton>
        )}
      </div>
      {device === undefined ? null : device ? <DevicePanel /> : <SetupForm />}
    </main>
  );
}
