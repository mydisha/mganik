import { useLiveQuery } from "dexie-react-hooks";
import { History, Lock, Settings, TriangleAlert, Volume2, VolumeX } from "lucide-react";
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { cn } from "@/shared/lib/utils";
import { endSession } from "../pin";
import { posStrings as s } from "../strings";
import { useDevice, useLimits, useOutboxSummary, usePos, usePrefs, useSession, useSyncMeta } from "../use-pos-data";
import { attentionCount, AttentionPanel, loadAttention } from "./attention-panel";
import { PosButton } from "./pos-button";
import { describeSync, SyncStatus } from "./sync-status";

// Header kasir: toko, status sinkron (selalu terlihat), akses ke panel
// perhatian, bunyi scan, kasir yang login, dan pengaturan perangkat.
export function PosHeader({ onDialogChange, onFocusScan }: { onDialogChange?: (open: boolean) => void; onFocusScan?: () => void }) {
  const { db, engine } = usePos();
  const device = useDevice();
  const sync = useSyncMeta();
  const outbox = useOutboxSummary();
  const limits = useLimits();
  const session = useSession();
  const prefs = usePrefs();
  const attention = useLiveQuery(() => loadAttention(db), [db]);
  const [panelOpen, setPanelOpen] = useState(false);
  const [now, setNow] = useState(() => Date.now());

  // Jam "sinkron terakhir" dan peringatan offline lama diperbarui tiap menit.
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(t);
  }, []);

  const tz = device?.timezone;
  const view = sync
    ? describeSync({
        sync,
        waiting: outbox?.unacked ?? 0,
        now,
        offlineLimitSeconds: limits?.offline_limit_seconds,
        timeZone: tz,
      })
    : null;
  const count = attentionCount(attention);

  const setPanel = (open: boolean) => {
    setPanelOpen(open);
    onDialogChange?.(open);
  };

  const toggleSound = () => void db.meta.put({ key: "prefs", sound: !(prefs?.sound ?? true) });

  return (
    <header className="flex min-h-14 flex-wrap items-center gap-x-3 gap-y-2 border-b bg-card px-4 py-2">
      <div className="flex min-w-0 flex-col leading-tight">
        <span className="text-base font-semibold">{s.brand}</span>
        {device && <span className="truncate text-xs text-muted-foreground">{device.locationName}</span>}
      </div>
      <div className="flex min-w-0 flex-1 items-center gap-2">
        {view && <SyncStatus view={view} className="max-w-full" />}
        <PosButton
          posSize="touch"
          variant={count > 0 ? "destructive" : "ghost"}
          onClick={() => setPanel(true)}
          aria-label={s.header.attention(count)}
          className={cn("shrink-0", count === 0 && "w-12 px-0")}
        >
          {count > 0 ? <TriangleAlert aria-hidden="true" /> : <History aria-hidden="true" />}
          {count > 0 && <span className="max-xl:sr-only">{s.header.attention(count)}</span>}
          {count > 0 && <span className="tabular-nums xl:hidden">{count}</span>}
        </PosButton>
      </div>
      <div className="flex items-center gap-1">
        <PosButton
          posSize="icon"
          variant="ghost"
          aria-pressed={prefs?.sound ?? true}
          aria-label={prefs?.sound === false ? s.header.soundOff : s.header.soundOn}
          title={prefs?.sound === false ? s.header.soundOff : s.header.soundOn}
          onClick={toggleSound}
        >
          {prefs?.sound === false ? <VolumeX aria-hidden="true" /> : <Volume2 aria-hidden="true" />}
        </PosButton>
        {session && (
          <>
            <span className="max-w-40 truncate px-2 text-sm" title={s.header.cashier(session.cashierName)}>
              <span className="sr-only">{s.header.cashier("")}</span>
              {session.cashierName}
            </span>
            <PosButton posSize="touch" variant="outline" onClick={() => void endSession(db)}>
              <Lock aria-hidden="true" />
              <span className="max-lg:sr-only">{s.header.lock}</span>
            </PosButton>
          </>
        )}
        <PosButton posSize="icon" variant="ghost" asChild>
          <Link to="/pos/setup" aria-label={s.header.device} title={s.header.device}>
            <Settings aria-hidden="true" />
          </Link>
        </PosButton>
      </div>
      <AttentionPanel
        open={panelOpen}
        onOpenChange={setPanel}
        data={attention}
        timeZone={tz}
        onRecheck={() => void engine?.resume()}
        onClosed={() => onFocusScan?.()}
      />
    </header>
  );
}
