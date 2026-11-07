import { cn } from "@/shared/lib/utils";

export type StatusTone = "success" | "warning" | "danger" | "info" | "neutral";

const toneClasses: Record<StatusTone, string> = {
  success: "border-success-border bg-success text-success-foreground",
  warning: "border-warning-border bg-warning text-warning-foreground",
  danger: "border-danger-border bg-danger text-danger-foreground",
  info: "border-info-border bg-info text-info-foreground",
  neutral: "border-neutral-border bg-neutral text-neutral-foreground",
};

interface StatusBadgeProps {
  label: string;
  tone: StatusTone;
  className?: string;
}

/** Status selalu berupa teks; warna hanya penguat. */
export function StatusBadge({ label, tone, className }: StatusBadgeProps) {
  return (
    <span
      data-tone={tone}
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium whitespace-nowrap",
        toneClasses[tone],
        className,
      )}
    >
      <span aria-hidden="true" className="size-1.5 rounded-full bg-current" />
      {label}
    </span>
  );
}
