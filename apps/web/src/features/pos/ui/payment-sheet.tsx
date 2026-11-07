import { CircleAlert, X } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { cn } from "@/shared/lib/utils";
import {
  quickCashAmounts,
  summarizePayments,
  validateNewPayment,
  validatePayments,
  type PaymentEntry,
  type PaymentError,
  type PaymentMethod,
} from "../lib/cart-math";
import { formatRupiah } from "../lib/format";
import { classifyEntry } from "../lib/scan";
import { posStrings as s } from "../strings";
import { NumPad } from "./num-pad";
import { PosButton, ShortcutHint } from "./pos-button";
import { PosDialog } from "./pos-dialog";

interface PaymentSheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  total: number;
  offlineValueLimit: number | null;
  onComplete: (payments: PaymentEntry[]) => Promise<string | null>;
  onClosed: () => void;
}

const METHODS: PaymentMethod[] = ["CASH", "EDC", "QRIS"];

function parseAmount(raw: string): number {
  const digits = raw.replace(/\D/g, "");
  return digits ? Number(digits) : 0;
}

// Pembayaran: tunai (kembalian), EDC/QRIS dengan kode approval wajib, atau
// campuran. Aturan kontrak dicek di klien sebelum transaksi dicatat: non-tunai
// tidak boleh melebihi total, hanya satu tunai.
export function PaymentSheet({ open, onOpenChange, total, offlineValueLimit, onComplete, onClosed }: PaymentSheetProps) {
  const [payments, setPayments] = useState<PaymentEntry[]>([]);
  const [method, setMethod] = useState<PaymentMethod>("CASH");
  const [amount, setAmount] = useState("");
  const [ref, setRef] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const amountRef = useRef<HTMLInputElement>(null);
  const refRef = useRef<HTMLInputElement>(null);
  const completeRef = useRef<HTMLButtonElement>(null);
  const amountTimes = useRef<number[]>([]);
  const ids = useId();

  useEffect(() => {
    if (open) {
      setPayments([]);
      setMethod("CASH");
      setAmount("");
      setRef("");
      setError(null);
      setSaving(false);
    }
  }, [open]);

  const summary = summarizePayments(total, payments);
  const ready = validatePayments(total, payments) === null;
  const hasCash = payments.some((p) => p.method === "CASH");

  const pickMethod = (m: PaymentMethod) => {
    setMethod(m);
    setError(null);
    // Non-tunai biasanya pas sebesar sisa; tunai diketik sesuai uang yang diterima.
    setAmount(m === "CASH" ? "" : summary.remaining > 0 ? String(summary.remaining) : "");
    setRef("");
    requestAnimationFrame(() => (m === "CASH" ? amountRef.current : refRef.current)?.focus());
  };

  const tryAdd = (value = parseAmount(amount)): PaymentEntry[] | null => {
    const entry: PaymentEntry = method === "CASH" ? { method, amount: value } : { method, amount: value, approvalRef: ref };
    const err = validateNewPayment(total, payments, entry);
    if (err) {
      setError(s.payment.errors[err]);
      return null;
    }
    const next = [...payments, { ...entry, approvalRef: entry.approvalRef?.trim() }];
    setPayments(next);
    setAmount("");
    setRef("");
    setError(null);
    const after = summarizePayments(total, next);
    if (after.complete) {
      requestAnimationFrame(() => completeRef.current?.focus());
    } else {
      // Sisa dibayar dengan metode lain: tunai sudah tercatat → pindah ke EDC.
      const nextMethod = method === "CASH" || next.some((p) => p.method === "CASH") ? "EDC" : "CASH";
      setMethod(nextMethod);
      if (nextMethod !== "CASH") setAmount(String(after.remaining));
      requestAnimationFrame(() => amountRef.current?.focus());
    }
    return next;
  };

  const complete = async () => {
    let list = payments;
    // Nominal yang sudah diketik tetapi belum ditambahkan ikut dicatat.
    if (amount && !summary.complete) {
      const added = tryAdd();
      if (!added) return;
      list = added;
    }
    const err: PaymentError | "insufficient" | null = validatePayments(total, list);
    if (err) {
      setError(s.payment.errors[err]);
      return;
    }
    setSaving(true);
    const failure = await onComplete(list);
    setSaving(false);
    if (failure) setError(failure);
  };

  const onDigit = (d: string) => setAmount((a) => (a + d).replace(/^0+(?=\d)/, "").slice(0, 15));

  return (
    <PosDialog
      open={open}
      onOpenChange={(o) => !saving && onOpenChange(o)}
      title={s.payment.title}
      size="lg"
      onClosed={onClosed}
      initialFocus={amountRef}
      footer={
        <>
          <PosButton posSize="pos" variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>
            {s.payment.cancel} <ShortcutHint keys="Esc" />
          </PosButton>
          <PosButton
            ref={completeRef}
            posSize="primary"
            onClick={() => void complete()}
            disabled={saving || (!ready && !amount)}
            className="min-w-64"
          >
            {saving ? s.payment.completing : s.payment.complete}
          </PosButton>
        </>
      }
    >
      <div className="grid gap-5 md:grid-cols-[minmax(0,1fr)_auto]">
        <div className="flex min-w-0 flex-col gap-4">
          <dl className="grid grid-cols-2 gap-x-4 gap-y-1 rounded-lg border bg-card p-4">
            <dt className="text-sm text-muted-foreground">{s.payment.totalDue}</dt>
            <dd className="text-right text-2xl font-semibold tabular-nums">{formatRupiah(total)}</dd>
            <dt className="text-sm text-muted-foreground">{s.payment.paid}</dt>
            <dd className="text-right text-base tabular-nums">{formatRupiah(summary.paid)}</dd>
            {summary.change > 0 || summary.complete ? (
              <>
                <dt className="text-base font-medium">{s.payment.change}</dt>
                <dd className="text-right text-3xl font-semibold tabular-nums" aria-live="polite">
                  {formatRupiah(summary.change)}
                </dd>
              </>
            ) : (
              <>
                <dt className="text-base font-medium">{s.payment.remaining}</dt>
                <dd className="text-right text-3xl font-semibold tabular-nums" aria-live="polite">
                  {formatRupiah(summary.remaining)}
                </dd>
              </>
            )}
          </dl>

          {total === 0 && <p className="rounded-md border border-warning-border bg-warning p-3 text-sm text-warning-foreground">{s.payment.zeroTotal}</p>}
          {offlineValueLimit !== null && total > offlineValueLimit && (
            <p className="rounded-md border border-warning-border bg-warning p-3 text-sm text-warning-foreground">
              {s.payment.valueLimit(offlineValueLimit)}
            </p>
          )}

          {!summary.complete && (
            <fieldset className="flex flex-col gap-3">
              <legend className="mb-2 text-sm font-medium">{s.payment.method}</legend>
              <div className="flex flex-wrap gap-2">
                {METHODS.map((m) => (
                  <PosButton
                    key={m}
                    posSize="touch"
                    variant={method === m ? "default" : "outline"}
                    aria-pressed={method === m}
                    disabled={m === "CASH" && hasCash}
                    onClick={() => pickMethod(m)}
                    className="min-w-28"
                  >
                    {s.payment.methods[m]}
                  </PosButton>
                ))}
              </div>
              <div className="flex flex-col gap-1.5">
                <label htmlFor={`${ids}-amount`} className="text-sm font-medium">
                  {s.payment.amount} ({s.payment.methods[method]})
                </label>
                <input
                  ref={amountRef}
                  id={`${ids}-amount`}
                  inputMode="numeric"
                  autoComplete="off"
                  value={amount}
                  onChange={(e) => setAmount(e.target.value.replace(/\D/g, "").slice(0, 15))}
                  onKeyDown={(e) => {
                    const t = e.timeStamp || performance.now();
                    if (e.key.length === 1) {
                      if (!e.currentTarget.value) amountTimes.current = [];
                      amountTimes.current.push(t);
                    } else if (e.key === "Backspace" || e.key === "Delete") {
                      amountTimes.current = [];
                    }
                    if (e.key === "Enter") {
                      e.preventDefault();
                      const burst = classifyEntry([...amountTimes.current, t]) === "scanner";
                      amountTimes.current = [];
                      if (burst) {
                        // Scanner menembak ke kolom nominal: buang, jangan jadi pembayaran.
                        setAmount("");
                        setError(s.payment.errors.scanIgnored);
                        return;
                      }
                      if (method !== "CASH" && !ref.trim()) refRef.current?.focus();
                      else tryAdd();
                    }
                  }}
                  aria-describedby={`${ids}-amount-preview`}
                  className="h-14 w-full rounded-md border border-input bg-card px-4 text-right text-2xl tabular-nums shadow-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50"
                />
                <span id={`${ids}-amount-preview`} className="text-right text-sm text-muted-foreground tabular-nums">
                  {formatRupiah(parseAmount(amount))}
                </span>
              </div>
              {method === "CASH" ? (
                <div className="flex flex-wrap gap-2">
                  {quickCashAmounts(summary.remaining).map((v, i) => (
                    <PosButton key={v} posSize="touch" variant="secondary" className="tabular-nums" onClick={() => tryAdd(v)}>
                      {i === 0 ? `${s.payment.exact} ${formatRupiah(v)}` : formatRupiah(v)}
                    </PosButton>
                  ))}
                </div>
              ) : (
                <div className="flex flex-col gap-1.5">
                  <label htmlFor={`${ids}-ref`} className="text-sm font-medium">
                    {s.payment.approvalRef[method]}
                  </label>
                  <input
                    ref={refRef}
                    id={`${ids}-ref`}
                    autoComplete="off"
                    value={ref}
                    onChange={(e) => setRef(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        e.preventDefault();
                        tryAdd();
                      }
                    }}
                    aria-describedby={`${ids}-ref-hint`}
                    className="h-12 w-full rounded-md border border-input bg-card px-4 font-mono text-base shadow-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 max-md:text-base"
                  />
                  <span id={`${ids}-ref-hint`} className="text-xs text-muted-foreground">
                    {s.payment.approvalHint}
                  </span>
                </div>
              )}
              <PosButton posSize="touch" variant="outline" onClick={() => tryAdd()} className="self-start">
                {s.payment.add}
              </PosButton>
            </fieldset>
          )}

          {error && (
            <p role="alert" className="flex items-start gap-2 rounded-md border border-danger-border bg-danger p-3 text-sm text-danger-foreground">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
              {error}
            </p>
          )}

          {payments.length > 0 && (
            <section aria-labelledby={`${ids}-list`}>
              <h3 id={`${ids}-list`} className="mb-2 text-sm font-medium">
                {s.payment.list}
              </h3>
              <ul className="flex flex-col gap-2">
                {payments.map((p, i) => {
                  const label = `${s.payment.methods[p.method]} ${formatRupiah(p.amount)}`;
                  return (
                    <li key={i} className="flex items-center gap-3 rounded-md border bg-card py-1 pr-1 pl-3">
                      <span className="flex-1 text-base">
                        {s.payment.methods[p.method]}
                        {p.approvalRef && <span className="ml-2 font-mono text-xs text-muted-foreground">{p.approvalRef}</span>}
                      </span>
                      <span className="text-base font-medium tabular-nums">{formatRupiah(p.amount)}</span>
                      <PosButton
                        posSize="icon"
                        variant="ghost"
                        aria-label={s.payment.remove(label)}
                        onClick={() => setPayments(payments.filter((_, j) => j !== i))}
                        disabled={saving}
                      >
                        <X aria-hidden="true" />
                      </PosButton>
                    </li>
                  );
                })}
              </ul>
            </section>
          )}
        </div>

        <NumPad
          className={cn("md:w-[13.5rem]", summary.complete && "hidden")}
          onDigit={onDigit}
          onBackspace={() => setAmount((a) => a.slice(0, -1))}
          leftKey={{ label: "000", ariaLabel: s.payment.threeZeros, onPress: () => onDigit("000") }}
          disabled={saving || summary.complete}
        />
      </div>
    </PosDialog>
  );
}
