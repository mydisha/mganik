import type { ComponentProps } from "react";
import { cn } from "@/shared/lib/utils";

// 16 px di bawah 768 px supaya iOS tidak memperbesar halaman saat fokus.
export const controlClasses =
  "w-full min-w-0 rounded-md border border-input bg-card text-base shadow-xs transition-colors placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-danger-foreground md:text-sm";

export function Input({ className, ...props }: ComponentProps<"input">) {
  return <input data-slot="input" className={cn(controlClasses, "h-9 px-3 py-1 max-md:min-h-11", className)} {...props} />;
}

export function Textarea({ className, ...props }: ComponentProps<"textarea">) {
  return <textarea data-slot="textarea" className={cn(controlClasses, "min-h-20 px-3 py-2", className)} {...props} />;
}
