import { useLiveQuery } from "dexie-react-hooks";
import { useRef } from "react";
import { StatusBadge, type StatusTone } from "@/shared/ui/status-badge";
import type { OutboxRow } from "../db";
import { summarizePayments } from "../lib/cart-math";
import { formatDateTime, formatNumber, formatRupiah } from "../lib/format";
import { posStrings as s } from "../strings";
import { usePos } from "../use-pos-data";
import { PosButton, ShortcutHint } from "./pos-button";
import { PosDialog } from "./pos-dialog";

interface ReceiptDialogProps {
  sale: { row: OutboxRow; names: Record<string, string> } | null;
  timeZone?: string;
  onDone: () => void;
  onClosed: () => void;
}

// Struk ringkas di layar setelah transaksi tercatat. Status sinkron diperbarui
// langsung dari IndexedDB: kasir bisa melihat transaksi ini diterima server
// tanpa membuka menu lain.
export function ReceiptDialog({ sale, timeZone, onDone, onClosed }: ReceiptDialogProps) {
  const { db } = usePos();
  const doneRef = useRef<HTMLButtonElement>(null);
  const id = sale?.row.clientTxnId;
  const status = useLiveQuery(async (): Promise<{ label: string; tone: StatusTone }> => {
    if (!id) return { label: s.receipt.statusPending, tone: "warning" };
    const row = await db.outbox.get(id);
    if (row?.state === "rejected" || row?.state === "resolved") return { label: s.receipt.statusRejected, tone: "danger" };
    if (row) return { label: s.receipt.statusPending, tone: "warning" };
    const log = await db.syncLog.where("clientTxnId").equals(id).last();
    const noted = Boolean(log && (log.conflicts.length > 0 || log.flags.length > 0));
    return noted ? { label: s.receipt.statusConflict, tone: "info" } : { label: s.receipt.statusSynced, tone: "success" };
  }, [db, id]);

  const p = sale?.row.payload;
  const pay = p
    ? summarizePayments(
        p.total,
        p.payments.map((x) => ({ method: x.method, amount: x.amount })),
      )
    : null;

  return (
    <PosDialog
      open={Boolean(sale)}
      onOpenChange={(o) => !o && onDone()}
      title={s.receipt.title}
      description={sale ? `${s.receipt.number(sale.row.deviceSeq)} · ${formatDateTime(sale.row.createdAt, timeZone)} · ${sale.row.cashierName}` : undefined}
      onClosed={onClosed}
      initialFocus={doneRef}
      footer={
        <PosButton ref={doneRef} posSize="primary" onClick={onDone} className="min-w-64">
          {s.receipt.next} <ShortcutHint keys="Enter" />
        </PosButton>
      }
    >
      {sale && p && pay && (
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-baseline justify-between gap-2 rounded-lg border bg-card p-4">
            <span className="text-base font-medium">{s.receipt.change}</span>
            <span className="text-4xl font-semibold tabular-nums">{formatRupiah(pay.change)}</span>
          </div>
          {status && <StatusBadge label={status.label} tone={status.tone} className="self-start text-sm" />}
          <ul className="flex flex-col divide-y rounded-lg border bg-card">
            {p.lines.map((l) => (
              <li key={l.product_id} className="flex items-start justify-between gap-3 px-4 py-2 text-sm">
                <span className="min-w-0">
                  <span className="tabular-nums">{formatNumber(l.qty)} × </span>
                  {sale.names[l.product_id] ?? s.unknownProduct}
                  {l.serials?.length ? (
                    <span className="block font-mono text-xs break-all text-muted-foreground">{l.serials.join(", ")}</span>
                  ) : null}
                </span>
                <span className="shrink-0 tabular-nums">{formatRupiah(l.qty * l.unit_price - l.discount)}</span>
              </li>
            ))}
          </ul>
          <dl className="grid grid-cols-2 gap-y-1 text-sm">
            <dt className="font-medium">{s.receipt.total}</dt>
            <dd className="text-right text-base font-semibold tabular-nums">{formatRupiah(p.total)}</dd>
            {p.payments.map((x, i) => (
              <div key={i} className="contents">
                <dt className="text-muted-foreground">
                  {s.receipt.paidWith} {s.payment.methods[x.method]}
                  {x.approval_ref && <span className="ml-1 font-mono text-xs">{x.approval_ref}</span>}
                </dt>
                <dd className="text-right tabular-nums">{formatRupiah(x.amount)}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}
    </PosDialog>
  );
}
