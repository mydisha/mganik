import { ChevronDown } from "lucide-react";
import type { ComponentProps } from "react";
import { cn } from "@/shared/lib/utils";
import { controlClasses } from "./input";

/**
 * Select native: pemilih bawaan perangkat di ponsel, bisa dipakai penuh dengan
 * keyboard, dan tetap terbaca screen reader tanpa ARIA tambahan.
 */
export function Select({ className, children, ...props }: ComponentProps<"select">) {
  return (
    <div className={cn("relative", className)}>
      <select data-slot="select" className={cn(controlClasses, "h-9 appearance-none pr-9 pl-3 max-md:min-h-11")} {...props}>
        {children}
      </select>
      <ChevronDown
        aria-hidden="true"
        className="pointer-events-none absolute top-1/2 right-3 size-4 -translate-y-1/2 text-muted-foreground"
      />
    </div>
  );
}
