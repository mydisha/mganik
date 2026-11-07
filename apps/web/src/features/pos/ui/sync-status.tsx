import { CircleAlert, RefreshCw, ShieldX, Wifi, WifiOff } from "lucide-react";
import type { StatusTone } from "@/shared/ui/status-badge";
import { cn } from "@/shared/lib/utils";
import type { SyncMeta } from "../db";
import { posStrings as s } from "../strings";

// Pill status sinkron yang selalu terlihat di header kasir. Status dibawa
// ikon + teks, warna hanya penguat (DESIGN.md ## Kasir).

export type SyncIcon = "online" | "offline" | "syncing" | "revoked" | "blocked" | "checking";

export interface SyncView {
  tone: StatusTone;
  icon: SyncIcon;
  /** Teks lengkap, mis. "Offline · 3 transaksi menunggu · sinkron terakhir 10.42 WIB". */
  text: string;
  offlineTooLong: boolean;
}

export function describeSync(input: {
  sync: SyncMeta;
  waiting: number;
  now: number;
  offlineLimitSeconds?: number;
  timeZone?: string;
}): SyncView {
  // `waiting` = transaksi yang belum tersimpan di server (pending_count), bukan
  // hanya yang menunggu dikirim.
  const { sync, waiting, now, timeZone } = input;
  // "Semua tersinkron" hanya kalau server juga tidak menahan toko (heartbeat
  // terakhir tanpa online_blocked_reason).
  const serverHolds = waiting === 0 && sync.onlineBlockedReason !== null && sync.lastHeartbeatAt !== null;
  const queue =
    waiting > 0
      ? s.sync.waiting(waiting)
      : serverHolds
        ? sync.onlineBlockedReason === "missing_seqs"
          ? s.sync.missingSeqs
          : s.sync.waitingServer
        : s.sync.synced;
  // Kontak terakhir yang berhasil: sync atau heartbeat.
  const lastContact = Math.max(sync.lastSyncAt ?? 0, sync.lastHeartbeatAt ?? 0) || null;
  const last = s.sync.lastSync(lastContact, timeZone);
  const offlineTooLong = Boolean(
    input.offlineLimitSeconds &&
      waiting > 0 &&
      sync.online === false &&
      lastContact &&
      now - lastContact > input.offlineLimitSeconds * 1000,
  );
  const join = (...parts: string[]) => parts.join(" · ");

  if (sync.authRevoked) return { tone: "danger", icon: "revoked", text: join(s.sync.revoked, queue), offlineTooLong };
  if (sync.blockedMessage && sync.online !== false)
    return { tone: "danger", icon: "blocked", text: join(s.sync.blocked, queue, last), offlineTooLong };
  if (sync.online === null) return { tone: "neutral", icon: "checking", text: join(s.sync.checking, queue), offlineTooLong };
  if (sync.online === false) return { tone: "warning", icon: "offline", text: join(s.sync.offline, queue, last), offlineTooLong };
  if (serverHolds && sync.onlineBlockedReason === "missing_seqs")
    return { tone: "warning", icon: "blocked", text: join(s.sync.online, queue, last), offlineTooLong };
  if (waiting > 0 || sync.syncing || serverHolds)
    return { tone: "info", icon: "syncing", text: join(s.sync.syncing, queue, last), offlineTooLong };
  return { tone: "success", icon: "online", text: join(s.sync.online, queue, last), offlineTooLong };
}

const toneClasses: Record<StatusTone, string> = {
  success: "border-success-border bg-success text-success-foreground",
  warning: "border-warning-border bg-warning text-warning-foreground",
  danger: "border-danger-border bg-danger text-danger-foreground",
  info: "border-info-border bg-info text-info-foreground",
  neutral: "border-neutral-border bg-neutral text-neutral-foreground",
};

const icons = {
  online: Wifi,
  offline: WifiOff,
  syncing: RefreshCw,
  revoked: ShieldX,
  blocked: CircleAlert,
  checking: Wifi,
} as const;

export function SyncStatus({ view, className }: { view: SyncView; className?: string }) {
  const Icon = icons[view.icon];
  return (
    <div
      role="status"
      data-tone={view.tone}
      title={view.offlineTooLong ? `${view.text}. ${s.sync.offlineTooLong}` : view.text}
      className={cn(
        "inline-flex h-10 min-w-0 items-center gap-2 rounded-full border px-3 text-sm font-medium",
        toneClasses[view.tone],
        className,
      )}
    >
      <Icon aria-hidden="true" className="size-4 shrink-0" />
      <span className="truncate tabular-nums">{view.text}</span>
      {view.offlineTooLong && <span className="sr-only">{s.sync.offlineTooLong}</span>}
    </div>
  );
}
