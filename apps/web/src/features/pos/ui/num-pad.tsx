import { Delete } from "lucide-react";
import type { MouseEvent } from "react";
import { posStrings as s } from "../strings";
import { PosButton } from "./pos-button";

interface NumPadProps {
  onDigit: (digit: string) => void;
  onBackspace: () => void;
  /** Tombol kiri bawah: hapus semua (default) atau "000" untuk nominal uang. */
  leftKey?: { label: string; ariaLabel: string; onPress: () => void };
  onClear?: () => void;
  disabled?: boolean;
  className?: string;
}

// Tombol numpad tidak mengambil fokus saat diklik/disentuh, supaya kursor tetap
// di kolom scan atau kolom nominal dan scanner tetap bisa mengetik.
const keepFocus = (e: MouseEvent) => e.preventDefault();

export function NumPad({ onDigit, onBackspace, leftKey, onClear, disabled, className }: NumPadProps) {
  const left = leftKey ?? (onClear ? { label: "C", ariaLabel: s.sell.clearEntry, onPress: onClear } : null);
  return (
    <div role="group" aria-label="Numpad" className={className}>
      <div className="grid grid-cols-3 justify-items-stretch gap-2">
        {["1", "2", "3", "4", "5", "6", "7", "8", "9"].map((d) => (
          <PosButton
            key={d}
            posSize="key"
            variant="outline"
            className="w-full"
            disabled={disabled}
            onMouseDown={keepFocus}
            onClick={() => onDigit(d)}
          >
            {d}
          </PosButton>
        ))}
        {left ? (
          <PosButton
            posSize="key"
            variant="secondary"
            className="w-full text-xl"
            aria-label={left.ariaLabel}
            disabled={disabled}
            onMouseDown={keepFocus}
            onClick={left.onPress}
          >
            {left.label}
          </PosButton>
        ) : (
          <span aria-hidden="true" />
        )}
        <PosButton
          posSize="key"
          variant="outline"
          className="w-full"
          disabled={disabled}
          onMouseDown={keepFocus}
          onClick={() => onDigit("0")}
        >
          0
        </PosButton>
        <PosButton
          posSize="key"
          variant="secondary"
          className="w-full"
          aria-label={s.sell.backspace}
          disabled={disabled}
          onMouseDown={keepFocus}
          onClick={onBackspace}
        >
          <Delete aria-hidden="true" />
        </PosButton>
      </div>
    </div>
  );
}
