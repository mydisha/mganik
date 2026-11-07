import { ScanLine } from "lucide-react";
import { forwardRef, useRef, type InputHTMLAttributes, type KeyboardEvent } from "react";
import { cn } from "@/shared/lib/utils";
import { classifyEntry } from "../lib/scan";

// Kolom scan kasir. Scanner HID mengetik seperti keyboard; ScanField mencatat
// waktu setiap karakter dan, saat Enter, melaporkan apakah isian datang dari
// scanner (jeda < 30 ms) atau diketik orang. Halaman yang memakainya
// bertanggung jawab mengembalikan fokus ke sini setelah dialog ditutup.

interface ScanFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "onSubmit" | "onChange" | "value"> {
  id: string;
  label: string;
  hint: string;
  value: string;
  onValueChange: (value: string) => void;
  onSubmit: (value: string, source: "scanner" | "manual") => void;
}

export const ScanField = forwardRef<HTMLInputElement, ScanFieldProps>(function ScanField(
  { id, label, hint, value, onValueChange, onSubmit, onKeyDown, className, ...rest },
  ref,
) {
  const times = useRef<number[]>([]);

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    onKeyDown?.(e);
    if (e.defaultPrevented) return;
    if (e.key === "Enter") {
      e.preventDefault();
      const source = classifyEntry([...times.current, e.timeStamp || performance.now()]);
      times.current = [];
      onSubmit(e.currentTarget.value, source);
      return;
    }
    if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      // Isian kosong = awal scan baru.
      if (e.currentTarget.value === "" || e.currentTarget.selectionStart === 0) times.current = [];
      times.current.push(e.timeStamp || performance.now());
    } else if (e.key === "Backspace" || e.key === "Delete") {
      // Hapus manual: pasti bukan scanner.
      times.current = [];
    }
  };

  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      <div className="relative">
        <ScanLine aria-hidden="true" className="pointer-events-none absolute top-1/2 left-4 size-6 -translate-y-1/2 text-muted-foreground" />
        <input
          ref={ref}
          id={id}
          value={value}
          onChange={(e) => onValueChange(e.target.value)}
          onKeyDown={handleKeyDown}
          onPaste={() => {
            times.current = [];
          }}
          autoComplete="off"
          autoCorrect="off"
          autoCapitalize="off"
          spellCheck={false}
          enterKeyHint="enter"
          aria-describedby={`${id}-hint`}
          className="h-14 w-full rounded-md border border-input bg-card pr-4 pl-13 text-lg shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50"
          {...rest}
        />
      </div>
      <p id={`${id}-hint`} className="text-xs text-muted-foreground">
        {hint}
      </p>
    </div>
  );
});
