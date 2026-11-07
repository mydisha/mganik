import { useLiveQuery } from "dexie-react-hooks";
import { CircleAlert, CircleCheck, TriangleAlert, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { EmptyState } from "@/shared/ui/empty-state";
import { cn } from "@/shared/lib/utils";
import { playScanSound } from "./audio";
import {
  addProductToCart,
  clearCart,
  mutateCart,
  removeLine,
  resolveScan,
  searchProducts,
  setLineDiscount,
  setLineQty,
  type ScanOutcome,
} from "./cart-store";
import type { OutboxRow, ProductRow } from "./db";
import { cartTotals, checkDiscount, isHighDiscount, validateQty, type PaymentEntry } from "./lib/cart-math";
import { formatRupiah } from "./lib/format";
import { looksLikeBarcode } from "./lib/scan";
import { recordSale, SaleError } from "./sale";
import { posStrings as s } from "./strings";
import { CartLine } from "./ui/cart-line";
import { NumPad } from "./ui/num-pad";
import { PaymentSheet } from "./ui/payment-sheet";
import { ConfirmDialog } from "./ui/pos-dialog";
import { PosButton, ShortcutHint } from "./ui/pos-button";
import { PosHeader } from "./ui/pos-header";
import { ReceiptDialog } from "./ui/receipt-dialog";
import { ScanField } from "./ui/scan-field";
import { useCart, useDevice, useLimits, usePos, usePrefs, usePriceList, useSession } from "./use-pos-data";

type Feedback = { tone: "ok" | "warn" | "error"; text: string } | null;

function isEditable(el: Element | null): boolean {
  if (!el) return false;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || (el as HTMLElement).isContentEditable;
}

// Layar jual: keranjang di kiri (kolom scan selalu siap), total + numpad +
// bayar di kanan. Semua aksi bisa dengan keyboard: F2 bayar, F4 hapus baris,
// Esc batal, + / − qty, panah memilih baris.
export function SellScreen() {
  const { db, engine } = usePos();
  const cart = useCart();
  const limits = useLimits();
  const priceList = usePriceList();
  const session = useSession();
  const prefs = usePrefs();
  const device = useDevice();
  const stockRows = useLiveQuery(() => db.stock.toArray(), [db]);

  const scanRef = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<ProductRow[]>([]);
  const [searchFor, setSearchFor] = useState("");
  const [active, setActive] = useState(-1);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [flashId, setFlashId] = useState<string | null>(null);
  const [feedback, setFeedback] = useState<Feedback>(null);
  const [entry, setEntry] = useState("");
  const [payOpen, setPayOpen] = useState(false);
  const [receipt, setReceipt] = useState<{ row: OutboxRow; names: Record<string, string> } | null>(null);
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);
  const [confirmCancel, setConfirmCancel] = useState(false);
  const [headerDialog, setHeaderDialog] = useState(false);

  const lines = cart?.lines ?? [];
  const totals = cartTotals(lines);
  const maxSerials = limits?.max_serials_per_txn ?? 50;
  const highPct = limits?.high_discount_percent ?? 100;
  const sound = prefs?.sound ?? true;
  const canSell = Boolean(priceList?.version && priceList.version >= 1);
  const dialogOpen = payOpen || Boolean(receipt) || Boolean(confirmRemove) || confirmCancel || headerDialog;
  const selected = lines.find((l) => l.productId === selectedId) ?? null;

  const stockLeft = useMemo(() => {
    const m = new Map<string, number>();
    for (const r of stockRows ?? []) m.set(r.productId, r.availableQty - r.localSold);
    return m;
  }, [stockRows]);

  const focusScan = useCallback(() => {
    requestAnimationFrame(() => scanRef.current?.focus());
  }, []);

  // Pilihan baris mengikuti isi keranjang (baris dihapus, keranjang dikosongkan).
  useEffect(() => {
    if (selectedId && !lines.some((l) => l.productId === selectedId)) setSelectedId(lines[0]?.productId ?? null);
  }, [lines, selectedId]);

  useEffect(() => {
    if (!flashId) return;
    const t = setTimeout(() => setFlashId(null), 700);
    return () => clearTimeout(t);
  }, [flashId]);

  // Pencarian produk dari ketikan (ditunda sedikit supaya burst scanner tidak memicu pencarian).
  useEffect(() => {
    const q = query.trim();
    if (q.length < 2) {
      setResults([]);
      setSearchFor("");
      return;
    }
    const t = setTimeout(() => {
      void searchProducts(db, q).then((r) => {
        setResults(r);
        setSearchFor(q);
        setActive(r.length > 0 ? 0 : -1);
      });
    }, 150);
    return () => clearTimeout(t);
  }, [db, query]);

  const say = useCallback(
    (tone: "ok" | "warn" | "error", text: string) => {
      setFeedback({ tone, text });
      playScanSound(tone === "ok" ? "ok" : "error", sound);
    },
    [sound],
  );

  const applyOutcome = useCallback(
    (o: ScanOutcome | null) => {
      if (!o) return;
      switch (o.type) {
        case "added":
          setSelectedId(o.productId);
          setFlashId(o.productId);
          setQuery("");
          setResults([]);
          if (o.soldBefore && o.serial) say("warn", s.scan.soldBefore(o.serial));
          else say("ok", o.serial ? s.scan.addedSerial(o.productName, o.serial) : s.scan.added(o.productName));
          return;
        case "serial-unknown":
          setQuery("");
          say("warn", s.sell.pendingSerial(o.code));
          return;
        case "no-price":
          setQuery("");
          say("error", s.scan.noPrice(o.productName));
          return;
        case "not-found":
          setQuery("");
          say("error", s.scan.notFound(o.value));
          return;
        case "invalid-url":
          setQuery("");
          say("error", s.scan.invalidUrl);
          return;
        case "rejected": {
          setQuery("");
          const text =
            o.error === "serial_duplicate"
              ? s.scan.serialDuplicate
              : o.error === "serial_limit"
                ? s.scan.serialLimit(maxSerials)
                : o.error === "qty_limit"
                  ? s.scan.qtyLimit
                  : s.scan.lineLimit;
          say("error", text);
          return;
        }
        case "search":
          // Tetap di kolom; daftar hasil yang menjawab.
          setFeedback(null);
          return;
      }
    },
    [maxSerials, say],
  );

  const addProduct = useCallback(
    async (p: ProductRow) => applyOutcome(await addProductToCart(db, p, null, maxSerials)),
    [applyOutcome, db, maxSerials],
  );

  const onScan = useCallback(
    async (value: string, source: "scanner" | "manual") => {
      if (source === "manual" && results.length > 0 && active >= 0 && searchFor === value.trim()) {
        await addProduct(results[active]!);
        return;
      }
      const outcome = await resolveScan(db, value, source, maxSerials);
      if (outcome?.type === "search") {
        const found = await searchProducts(db, outcome.query);
        if (found.length === 1) return addProduct(found[0]!);
        setResults(found);
        setSearchFor(outcome.query.trim());
        setActive(found.length ? 0 : -1);
        if (found.length === 0) {
          const q = outcome.query.trim();
          say("error", looksLikeBarcode(q) ? s.scan.notFound(q) : s.sell.noResults(q));
        }
        return;
      }
      applyOutcome(outcome);
    },
    [active, addProduct, applyOutcome, db, maxSerials, results, say, searchFor],
  );

  // Angka untuk qty/diskon: dari numpad, atau dari kolom scan kalau isinya angka.
  const takeNumber = (): number | null => {
    if (entry) return Number(entry);
    if (/^\d+$/.test(query.trim())) {
      const n = Number(query.trim());
      setQuery("");
      return n;
    }
    return null;
  };

  const applyQty = async (qtyArg?: number) => {
    if (!selected) return say("error", s.sell.selectLineFirst);
    const qty = qtyArg ?? takeNumber();
    if (qty === null) return say("error", s.sell.typeNumberFirst);
    const err = validateQty(qty, selected.serials.length);
    if (err) return say("error", s.numpadErrors.qty[err]);
    await mutateCart(db, (c) => setLineQty(c, selected.productId, qty));
    setEntry("");
    setFeedback(null);
  };

  const applyDiscount = async () => {
    if (!selected) return say("error", s.sell.selectLineFirst);
    const value = takeNumber();
    if (value === null) return say("error", s.sell.typeNumberFirst);
    const check = checkDiscount(selected, value, highPct);
    if (check.error) return say("error", s.numpadErrors.discount[check.error]);
    await mutateCart(db, (c) => setLineDiscount(c, selected.productId, value));
    setEntry("");
    if (check.high) setFeedback({ tone: "warn", text: s.sell.highDiscount(highPct) });
    else setFeedback(null);
  };

  const openPay = useCallback(() => {
    if (lines.length === 0 || !canSell || !session) return;
    setPayOpen(true);
  }, [canSell, lines.length, session]);

  const complete = async (payments: PaymentEntry[]): Promise<string | null> => {
    if (!session || !cart) return s.payment.saleErrors.fallback;
    try {
      const names = Object.fromEntries(cart.lines.map((l) => [l.productId, l.name]));
      const row = await recordSale(db, {
        lines: cart.lines,
        payments,
        cashierId: session.cashierId,
        cashierName: session.cashierName,
        priceListVersion: priceList?.version ?? null,
      });
      engine?.kick(true);
      setPayOpen(false);
      setReceipt({ row, names });
      setFeedback(null);
      setEntry("");
      return null;
    } catch (err) {
      if (err instanceof SaleError) {
        const code = err.code as keyof typeof s.payment.errors;
        return (
          s.payment.errors[code] ??
          (s.payment.saleErrors as Record<string, string>)[err.code] ??
          s.payment.saleErrors.fallback
        );
      }
      return s.payment.saleErrors.fallback;
    }
  };

  // Pintasan keyboard global layar jual.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (dialogOpen) return;
      const inScan = document.activeElement === scanRef.current;
      if (e.key === "F2") {
        e.preventDefault();
        openPay();
        return;
      }
      if (e.key === "F4") {
        e.preventDefault();
        if (selected) setConfirmRemove(selected.productId);
        else say("error", s.sell.selectLineFirst);
        return;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        if (results.length > 0 || query) {
          setQuery("");
          setResults([]);
        } else if (entry) {
          setEntry("");
        } else if (cart?.pendingSerial) {
          void mutateCart(db, (c) => {
            c.pendingSerial = null;
          });
        } else if (lines.length > 0) {
          setConfirmCancel(true);
        }
        focusScan();
        return;
      }
      if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        const dir = e.key === "ArrowDown" ? 1 : -1;
        if (results.length > 0) {
          e.preventDefault();
          setActive((a) => (a + dir + results.length) % results.length);
        } else if (!query && lines.length > 0) {
          e.preventDefault();
          const idx = lines.findIndex((l) => l.productId === selectedId);
          const next = lines[Math.min(lines.length - 1, Math.max(0, idx + dir))];
          if (next) setSelectedId(next.productId);
        }
        return;
      }
      if ((e.key === "+" || e.key === "-") && !query && selected && (inScan || !isEditable(document.activeElement))) {
        e.preventDefault();
        void applyQty(Math.max(1, selected.qty + (e.key === "+" ? 1 : -1)));
        return;
      }
      // Ketikan/scan saat fokus di luar kolom input: arahkan ke kolom scan.
      if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey && !isEditable(document.activeElement)) {
        scanRef.current?.focus();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  useEffect(() => {
    focusScan();
  }, [focusScan]);

  const listId = "pos-search-results";
  const showResults = results.length > 0 && query.trim().length >= 2;

  return (
    <div className="flex min-h-dvh flex-col lg:h-dvh">
      <PosHeader onDialogChange={setHeaderDialog} onFocusScan={focusScan} />
      <main className="grid flex-1 gap-4 p-4 lg:min-h-0 lg:grid-cols-[minmax(0,1fr)_23rem] xl:grid-cols-[minmax(0,1fr)_26rem]">
        <section aria-labelledby="pos-cart-title" className="flex min-w-0 flex-col gap-3 lg:min-h-0">
          <h1 className="sr-only">{s.title}</h1>
          {!canSell && priceList !== undefined && (
            <p role="alert" className="flex items-start gap-2 rounded-md border border-danger-border bg-danger p-3 text-sm text-danger-foreground">
              <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
              {s.sell.noPriceList}
            </p>
          )}
          <div className="relative">
            <ScanField
              ref={scanRef}
              id="pos-scan"
              label={s.sell.scanLabel}
              hint={s.sell.scanHint}
              placeholder={s.sell.scanPlaceholder}
              value={query}
              onValueChange={setQuery}
              onSubmit={(v, src) => void onScan(v, src)}
              role="combobox"
              aria-expanded={showResults}
              aria-controls={listId}
              aria-autocomplete="list"
              aria-activedescendant={showResults && active >= 0 ? `${listId}-${active}` : undefined}
            />
            {showResults && (
              <ul
                id={listId}
                role="listbox"
                aria-label={s.sell.searchResults}
                className="absolute top-[5.5rem] right-0 left-0 z-30 max-h-80 overflow-y-auto rounded-lg border bg-popover p-1 shadow-md"
              >
                {results.map((p, i) => (
                  <li
                    key={p.id}
                    id={`${listId}-${i}`}
                    role="option"
                    aria-selected={i === active}
                    onMouseDown={(e) => e.preventDefault()}
                    onClick={() => void addProduct(p)}
                    className={cn(
                      "flex min-h-12 cursor-pointer items-center justify-between gap-3 rounded-md px-3 py-2",
                      i === active ? "bg-accent text-accent-foreground" : "hover:bg-accent/60",
                    )}
                  >
                    <span className="min-w-0">
                      <span className="block truncate text-base">{p.name}</span>
                      <span className="font-mono text-xs text-muted-foreground">
                        {p.sku}
                        {p.gtin ? ` · ${p.gtin}` : ""}
                      </span>
                    </span>
                    <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                      {s.sell.stockLeft(stockLeft.get(p.id) ?? 0)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div aria-live="polite" className="min-h-6">
            {feedback && (
              <p
                className={cn(
                  "flex items-start gap-2 text-sm",
                  feedback.tone === "ok" && "text-success-foreground",
                  feedback.tone === "warn" && "text-warning-foreground",
                  feedback.tone === "error" && "text-danger-foreground",
                )}
              >
                {feedback.tone === "ok" ? (
                  <CircleCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
                ) : feedback.tone === "warn" ? (
                  <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
                ) : (
                  <CircleAlert aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
                )}
                {feedback.text}
              </p>
            )}
          </div>

          {cart?.pendingSerial && (
            <div className="flex flex-wrap items-center gap-3 rounded-md border border-warning-border bg-warning p-3 text-sm text-warning-foreground">
              <span className="min-w-0 flex-1 break-words">{s.sell.pendingSerial(cart.pendingSerial)}</span>
              <PosButton
                posSize="touch"
                variant="outline"
                onClick={() =>
                  void mutateCart(db, (c) => {
                    c.pendingSerial = null;
                  }).then(focusScan)
                }
              >
                <X aria-hidden="true" />
                {s.sell.cancelPendingSerial}
              </PosButton>
            </div>
          )}

          <div className="flex items-baseline justify-between gap-3">
            <h2 id="pos-cart-title" className="text-base font-semibold">
              {s.sell.cartTitle}
            </h2>
            {lines.length > 0 && (
              <span className="text-sm text-muted-foreground tabular-nums">{s.sell.items(lines.length, totals.units)}</span>
            )}
          </div>

          <div className="flex-1 lg:min-h-0 lg:overflow-y-auto">
            {cart === undefined ? (
              <p role="status" className="text-sm text-muted-foreground">
                {s.boot.loading}
              </p>
            ) : lines.length === 0 ? (
              <EmptyState title={s.sell.cartEmptyTitle} description={s.sell.cartEmptyBody} className="py-10" />
            ) : (
              <ul className="flex flex-col gap-2 pb-1">
                {lines.map((l) => (
                  <CartLine
                    key={l.productId}
                    line={l}
                    selected={l.productId === selectedId}
                    flash={l.productId === flashId}
                    stockLeft={stockRows ? (stockLeft.get(l.productId) ?? 0) : null}
                    highDiscount={isHighDiscount(l, highPct)}
                    onSelect={() => setSelectedId(l.productId)}
                    onRemove={() => setConfirmRemove(l.productId)}
                  />
                ))}
              </ul>
            )}
          </div>

          <div className="flex flex-wrap gap-3">
            <PosButton
              posSize="pos"
              variant="outline"
              disabled={!selected}
              onClick={() => selected && setConfirmRemove(selected.productId)}
            >
              {s.sell.removeLine} <ShortcutHint keys="F4" />
            </PosButton>
            <PosButton posSize="pos" variant="outline" disabled={lines.length === 0} onClick={() => setConfirmCancel(true)}>
              {s.sell.cancelSale} <ShortcutHint keys="Esc" />
            </PosButton>
          </div>
        </section>

        <aside aria-label={s.sell.total} className="flex flex-col gap-3 lg:min-h-0">
          <div className="rounded-lg border bg-card p-4">
            <div className="flex items-baseline justify-between gap-3">
              <span className="text-base font-medium">{s.sell.total}</span>
              <span className="text-4xl font-semibold tabular-nums" aria-live="polite">
                {formatRupiah(totals.total)}
              </span>
            </div>
            <dl className="mt-2 grid grid-cols-2 gap-y-0.5 text-sm text-muted-foreground">
              <dt>{s.sell.subtotal}</dt>
              <dd className="text-right tabular-nums">{formatRupiah(totals.subtotal)}</dd>
              <dt>{s.sell.discount}</dt>
              <dd className="text-right tabular-nums">{formatRupiah(totals.discount)}</dd>
            </dl>
          </div>

          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between gap-3 rounded-md border border-input bg-card px-4 py-2">
              <span className="text-sm text-muted-foreground">{s.sell.entryLabel}</span>
              <output aria-live="polite" className="text-2xl font-semibold tabular-nums">
                {entry || "0"}
              </output>
            </div>
            <div className="grid grid-cols-2 gap-2">
              <PosButton posSize="pos" variant="secondary" disabled={!selected} onMouseDown={(e) => e.preventDefault()} onClick={() => void applyQty()}>
                {s.sell.setQty}
              </PosButton>
              <PosButton posSize="pos" variant="secondary" disabled={!selected} onMouseDown={(e) => e.preventDefault()} onClick={() => void applyDiscount()}>
                {s.sell.setDiscount}
              </PosButton>
            </div>
          </div>

          <NumPad
            onDigit={(d) => setEntry((v) => (v + d).replace(/^0+(?=\d)/, "").slice(0, 12))}
            onBackspace={() => setEntry((v) => v.slice(0, -1))}
            onClear={() => setEntry("")}
          />

          <PosButton
            posSize="primary"
            className="mt-auto w-full"
            disabled={lines.length === 0 || !canSell || !session}
            onClick={openPay}
          >
            {s.sell.pay} {formatRupiah(totals.total)} <ShortcutHint keys="F2" />
          </PosButton>
        </aside>
      </main>

      <PaymentSheet
        open={payOpen}
        onOpenChange={setPayOpen}
        total={totals.total}
        offlineValueLimit={limits?.offline_value_limit ?? null}
        onComplete={complete}
        onClosed={focusScan}
      />
      <ReceiptDialog sale={receipt} timeZone={device?.timezone} onDone={() => setReceipt(null)} onClosed={focusScan} />
      <ConfirmDialog
        open={Boolean(confirmRemove)}
        onOpenChange={(o) => !o && setConfirmRemove(null)}
        title={s.sell.confirmRemoveTitle}
        body={(() => {
          const l = lines.find((x) => x.productId === confirmRemove);
          return l ? s.sell.confirmRemoveBody(l.name, l.qty) : "";
        })()}
        confirmLabel={s.sell.confirmRemove}
        cancelLabel={s.sell.keep}
        onConfirm={() => {
          const id = confirmRemove;
          if (id) void mutateCart(db, (c) => removeLine(c, id));
        }}
        onClosed={focusScan}
      />
      <ConfirmDialog
        open={confirmCancel}
        onOpenChange={setConfirmCancel}
        title={s.sell.confirmCancelTitle}
        body={s.sell.confirmCancelBody}
        confirmLabel={s.sell.confirmCancel}
        cancelLabel={s.sell.keep}
        onConfirm={() => {
          void mutateCart(db, clearCart);
          setEntry("");
          setFeedback(null);
        }}
        onClosed={focusScan}
      />
    </div>
  );
}
