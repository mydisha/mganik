import { CircleAlert, ShieldX } from "lucide-react";
import type { ReactNode } from "react";
import { StatusBadge } from "@/shared/ui/status-badge";
import { defaultSyncMeta, type OutboxRow, type PosDb, type PosSyncConflict, type SyncLogRow } from "../db";
import { formatClock, formatTxnNumber } from "../lib/format";
import { posStrings as s } from "../strings";
import { usePos } from "../use-pos-data";
import { PosButton } from "./pos-button";
import { PosDialog } from "./pos-dialog";

// Panel "perlu perhatian": transaksi REJECTED (alasan dari server + langkah
// lanjut), nomor yang hilang di server, sinkron yang berhenti, dan catatan
// konflik/flag dari hasil sinkron terbaru.

export interface AttentionData {
  revoked: boolean;
  blockedMessage: string | null;
  rejected: OutboxRow[];
  /** Nomor yang belum diterima server dan tidak ada di outbox perangkat. */
  missingElsewhere: number[];
  missingTruncated: boolean;
  recent: SyncLogRow[];
  products: Record<string, string>;
}

export async function loadAttention(db: PosDb): Promise<AttentionData> {
  const sync = (await db.getMeta("sync")) ?? defaultSyncMeta();
  const outbox = await db.outbox.toArray();
  const local = new Set(outbox.map((r) => r.deviceSeq));
  const recent = (await db.syncLog.orderBy("id").reverse().limit(100).toArray())
    .filter((l) => l.conflicts.length > 0 || l.flags.length > 0)
    .slice(0, 20);
  const products = Object.fromEntries((await db.products.toArray()).map((p) => [p.id, p.name]));
  return {
    revoked: sync.authRevoked,
    blockedMessage: sync.blockedMessage,
    rejected: outbox.filter((r) => r.state === "rejected").sort((a, b) => a.deviceSeq - b.deviceSeq),
    missingElsewhere: sync.missingSeqs.filter((n) => !local.has(n)),
    missingTruncated: sync.missingSeqsTruncated,
    recent,
    products,
  };
}

export function attentionCount(d: AttentionData | undefined): number {
  if (!d) return 0;
  return d.rejected.length + (d.missingElsewhere.length > 0 ? 1 : 0) + (d.revoked || d.blockedMessage ? 1 : 0);
}

export function describeConflict(c: PosSyncConflict, products: Record<string, string>): string {
  const product = (c.product_id && products[c.product_id]) || s.unknownProduct;
  switch (c.type) {
    case "OFFLINE_SALE_PREEMPT":
      return s.conflicts.OFFLINE_SALE_PREEMPT(c.qty ?? 1, product);
    case "STOCK_DISCREPANCY":
      return s.conflicts.STOCK_DISCREPANCY(c.qty ?? 0, product);
    case "SERIAL_DOUBLE_SALE":
      return s.conflicts.SERIAL_DOUBLE_SALE(c.code ?? "");
    case "SERIAL_UNREGISTERED":
      return s.conflicts.SERIAL_UNREGISTERED(c.code ?? "");
    case "PAYMENT_REF_DUPLICATE":
      return s.conflicts.PAYMENT_REF_DUPLICATE(c.approval_ref ?? "");
  }
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <h3 className="text-base font-semibold">{title}</h3>
      {children}
    </section>
  );
}

interface AttentionPanelProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  data: AttentionData | undefined;
  timeZone?: string;
  onRecheck: () => void;
  onClosed: () => void;
}

export function AttentionPanel({ open, onOpenChange, data, timeZone, onRecheck, onClosed }: AttentionPanelProps) {
  const { db } = usePos();

  const resolve = async (clientTxnId: string) => {
    // Tetap disimpan (cadangan/ekspor); hanya disembunyikan dari daftar.
    await db.outbox.update(clientTxnId, { state: "resolved" });
  };

  return (
    <PosDialog
      open={open}
      onOpenChange={onOpenChange}
      title={s.attention.title}
      description={s.attention.description}
      size="lg"
      onClosed={onClosed}
      footer={
        <PosButton posSize="pos" variant="outline" onClick={() => onOpenChange(false)}>
          {s.attention.close}
        </PosButton>
      }
    >
      {!data ? (
        <p className="text-sm text-muted-foreground">{s.boot.loading}</p>
      ) : (
        <div className="flex flex-col gap-6">
          {data.revoked && (
            <div role="alert" className="flex gap-3 rounded-lg border border-danger-border bg-danger p-4 text-danger-foreground">
              <ShieldX aria-hidden="true" className="mt-0.5 size-5 shrink-0" />
              <div className="flex flex-col gap-2">
                <p className="font-semibold">{s.attention.revokedTitle}</p>
                <p className="text-sm">{s.sync.revokedBody}</p>
                {data.blockedMessage && (
                  <p className="text-sm">
                    {s.attention.serverSays}: {data.blockedMessage}
                  </p>
                )}
                <PosButton posSize="touch" variant="outline" className="self-start" onClick={onRecheck}>
                  {s.sync.recheck}
                </PosButton>
              </div>
            </div>
          )}
          {!data.revoked && data.blockedMessage && (
            <div role="alert" className="flex gap-3 rounded-lg border border-danger-border bg-danger p-4 text-danger-foreground">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-5 shrink-0" />
              <div>
                <p className="font-semibold">{s.attention.blockedTitle}</p>
                <p className="text-sm">
                  {s.attention.serverSays}: {data.blockedMessage}
                </p>
              </div>
            </div>
          )}

          <Section title={s.attention.rejectedTitle}>
            {data.rejected.length === 0 ? (
              <p className="text-sm text-muted-foreground">{s.attention.rejectedEmpty}</p>
            ) : (
              <ul className="flex flex-col gap-3">
                {data.rejected.map((r) => (
                  <li key={r.clientTxnId} className="flex flex-col gap-2 rounded-lg border bg-card p-4">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="text-sm font-medium tabular-nums">
                        {s.attention.txnLine(r.deviceSeq, r.createdAt, r.payload.total, timeZone)} · {r.cashierName}
                      </span>
                      <StatusBadge label={r.rejection?.reason ?? "rejected"} tone="danger" className="font-mono" />
                    </div>
                    <p className="text-sm">
                      <span className="font-medium">{s.attention.serverSays}: </span>
                      {r.rejection?.message || r.rejection?.reason}
                    </p>
                    {r.rejection?.errors.length ? (
                      <ul className="list-inside list-disc text-xs text-muted-foreground">
                        {r.rejection.errors.map((e, i) => (
                          <li key={i}>
                            <span className="font-mono">{e.field}</span>: {e.message}
                          </li>
                        ))}
                      </ul>
                    ) : null}
                    <p className="text-sm">
                      <span className="font-medium">{s.attention.whatToDo}: </span>
                      {s.attention.actions[r.rejection?.reason ?? ""] ?? s.attention.actions.fallback}
                    </p>
                    <PosButton posSize="touch" variant="outline" className="self-start" onClick={() => void resolve(r.clientTxnId)}>
                      {s.attention.resolve}
                    </PosButton>
                  </li>
                ))}
              </ul>
            )}
          </Section>

          {data.missingElsewhere.length > 0 && (
            <Section title={s.attention.missingTitle}>
              <p className="text-sm">
                {s.attention.missingBody(data.missingElsewhere.slice(0, 20).map(formatTxnNumber).join(", "), data.missingTruncated || data.missingElsewhere.length > 20)}
              </p>
            </Section>
          )}

          <Section title={s.attention.recentTitle}>
            {data.recent.length === 0 ? (
              <p className="text-sm text-muted-foreground">{s.attention.recentEmpty}</p>
            ) : (
              <ul className="flex flex-col gap-3">
                {data.recent.map((l) => (
                  <li key={l.id} className="flex flex-col gap-1 rounded-lg border bg-card p-4">
                    <span className="text-xs text-muted-foreground tabular-nums">
                      No. {formatTxnNumber(l.deviceSeq)} · {formatClock(l.occurredAt, timeZone)}
                    </span>
                    {l.conflicts.map((c, i) => (
                      <p key={`c${i}`} className="text-sm">
                        {describeConflict(c, data.products)}
                      </p>
                    ))}
                    {l.flags.map((f) => (
                      <p key={f} className="text-sm text-muted-foreground">
                        {s.flags[f] ?? f}
                      </p>
                    ))}
                  </li>
                ))}
              </ul>
            )}
          </Section>
        </div>
      )}
    </PosDialog>
  );
}
