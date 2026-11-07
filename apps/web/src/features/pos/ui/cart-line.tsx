import { Trash2 } from "lucide-react";
import { cn } from "@/shared/lib/utils";
import { lineSubtotal, lineTotal, type CartLine as CartLineData } from "../lib/cart-math";
import { formatNumber, formatRupiah } from "../lib/format";
import { posStrings as s } from "../strings";
import { PosButton } from "./pos-button";

interface CartLineProps {
  line: CartLineData;
  selected: boolean;
  flash: boolean;
  stockLeft: number | null;
  highDiscount: boolean;
  onSelect: () => void;
  onRemove: () => void;
}

// Satu baris keranjang: qty besar di kiri, nama + SKU + serial terikat, total
// baris di kanan. Seluruh baris bisa disentuh untuk dipilih (numpad lalu
// mengubah qty/diskon baris terpilih); hapus selalu lewat konfirmasi.
export function CartLine({ line, selected, flash, stockLeft, highDiscount, onSelect, onRemove }: CartLineProps) {
  return (
    <li
      className={cn(
        "flex items-stretch gap-2 rounded-lg border bg-card motion-safe:transition-colors motion-safe:duration-700",
        selected && "border-ring ring-2 ring-ring/40",
        flash && "bg-success",
      )}
    >
      <button
        type="button"
        onClick={onSelect}
        onMouseDown={(e) => e.preventDefault()}
        aria-pressed={selected}
        className="flex min-w-0 flex-1 cursor-pointer items-center gap-4 rounded-l-lg px-4 py-3 text-left outline-none hover:bg-accent/60 focus-visible:ring-[3px] focus-visible:ring-ring/50"
      >
        <span className="w-14 shrink-0 text-center text-3xl font-semibold tabular-nums">
          <span className="sr-only">{s.sell.qty} </span>
          {formatNumber(line.qty)}
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="truncate text-base font-medium">{line.name}</span>
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
            <span className="font-mono">{line.sku}</span>
            <span className="tabular-nums">{s.sell.each(line.unitPrice)}</span>
            {stockLeft !== null && <span className="tabular-nums">{s.sell.stockLeft(stockLeft)}</span>}
          </span>
          {line.serials.length > 0 && (
            <span className="flex flex-wrap gap-1" aria-label={s.sell.serialCount(line.serials.length)}>
              {line.serials.map((code) => (
                <span key={code} className="rounded border bg-muted px-1.5 py-0.5 font-mono text-xs break-all">
                  {code}
                </span>
              ))}
            </span>
          )}
          {line.discount > 0 && (
            <span className={cn("text-xs tabular-nums", highDiscount ? "text-warning-foreground" : "text-muted-foreground")}>
              {s.sell.discount} −{formatRupiah(line.discount)}
            </span>
          )}
        </span>
        <span className="flex shrink-0 flex-col items-end">
          <span className="text-lg font-semibold tabular-nums">{formatRupiah(lineTotal(line))}</span>
          {line.discount > 0 && (
            <span className="text-xs text-muted-foreground tabular-nums line-through">{formatRupiah(lineSubtotal(line))}</span>
          )}
        </span>
      </button>
      <PosButton
        posSize="icon"
        variant="ghost"
        className="m-1 self-center text-muted-foreground hover:text-destructive"
        aria-label={`${s.sell.removeLine}: ${line.name}`}
        onClick={onRemove}
      >
        <Trash2 aria-hidden="true" />
      </PosButton>
    </li>
  );
}
