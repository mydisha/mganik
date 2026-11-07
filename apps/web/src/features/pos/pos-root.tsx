import { useLiveQuery } from "dexie-react-hooks";
import { useEffect, useMemo, useState } from "react";
import { Navigate, Outlet, useLocation } from "react-router";
import { getPosDb } from "./db";
import { usePwaUpdate } from "./pwa";
import { posStrings as s } from "./strings";
import { SYNC_CHANNEL_NAME, SyncEngine, type KickChannel, type LockLike } from "./sync/engine";
import { PosButton } from "./ui/pos-button";
import { PosContext } from "./use-pos-data";

function browserLocks(): LockLike | null {
  const locks = typeof navigator !== "undefined" ? navigator.locks : undefined;
  if (!locks?.request) return null;
  return {
    request: <T,>(name: string, options: { signal?: AbortSignal }, cb: () => Promise<T>) =>
      locks.request(name, options, () => cb()) as Promise<T>,
  };
}

function browserChannel(): KickChannel | null {
  if (typeof BroadcastChannel === "undefined") return null;
  const ch = new BroadcastChannel(SYNC_CHANNEL_NAME);
  return {
    postMessage: (m) => ch.postMessage(m),
    addEventListener: (t, l) => ch.addEventListener(t, l),
    removeEventListener: (t, l) => ch.removeEventListener(t, l),
    close: () => ch.close(),
  };
}

// Layout semua route /pos: membuka IndexedDB, menjalankan satu SyncEngine per
// tab (hanya satu yang benar-benar bekerja berkat Web Locks), mendaftarkan
// service worker, dan mengarahkan perangkat yang belum terdaftar ke setup.
export function PosRoot() {
  const db = useMemo(getPosDb, []);
  const [open, setOpen] = useState<"opening" | "ready" | "error">("opening");
  const [attempt, setAttempt] = useState(0);
  const location = useLocation();
  const pwaReload = usePwaUpdate();

  useEffect(() => {
    let alive = true;
    setOpen("opening");
    db.open()
      .then(() => alive && setOpen("ready"))
      .catch(() => alive && setOpen("error"));
    return () => {
      alive = false;
    };
  }, [db, attempt]);

  const engine = useMemo(
    () =>
      new SyncEngine({
        db,
        baseUrl: globalThis.location.origin,
        fetch: (input, init) => globalThis.fetch(input, init),
        locks: browserLocks(),
        channel: browserChannel(),
      }),
    [db],
  );

  const device = useLiveQuery(async () => (open === "ready" ? ((await db.getMeta("device")) ?? null) : undefined), [db, open]);
  const hasDevice = Boolean(device);

  useEffect(() => {
    if (!hasDevice) return;
    engine.start();
    engine.kick();
    return () => {
      void engine.stop();
    };
  }, [engine, hasDevice]);

  useEffect(() => {
    document.title = `${s.title} · mGanik`;
  }, []);

  if (open === "error") {
    return (
      <main className="mx-auto flex min-h-dvh max-w-lg flex-col justify-center gap-4 px-4">
        <h1 className="text-xl font-semibold">{s.boot.storageErrorTitle}</h1>
        <p className="text-base text-muted-foreground">{s.boot.storageErrorBody}</p>
        <PosButton posSize="pos" className="self-start" onClick={() => setAttempt((n) => n + 1)}>
          {s.boot.retry}
        </PosButton>
      </main>
    );
  }

  if (open === "opening" || device === undefined) {
    return (
      <main className="flex min-h-dvh items-center justify-center px-4" aria-busy="true">
        <p role="status" className="text-base text-muted-foreground">
          {s.boot.loading}
        </p>
      </main>
    );
  }

  const onSetup = location.pathname.replace(/\/+$/, "").endsWith("/setup");
  if (!device && !onSetup) return <Navigate to="/pos/setup" replace />;

  return (
    <PosContext.Provider value={{ db, engine }}>
      {pwaReload && (
        <div role="status" className="flex flex-wrap items-center justify-center gap-3 border-b border-info-border bg-info px-4 py-2 text-sm text-info-foreground">
          <span>
            {s.pwa.updateReady} {s.pwa.reloadHint}
          </span>
          <PosButton posSize="touch" variant="outline" onClick={pwaReload}>
            {s.pwa.reload}
          </PosButton>
        </div>
      )}
      <Outlet />
    </PosContext.Provider>
  );
}
