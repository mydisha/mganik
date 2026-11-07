import type { ReactNode } from "react";
import { cn } from "@/shared/lib/utils";

type AlertTone = "danger" | "warning" | "info" | "success";

const toneClasses: Record<AlertTone, string> = {
  danger: "border-danger-border bg-danger text-danger-foreground",
  warning: "border-warning-border bg-warning text-warning-foreground",
  info: "border-info-border bg-info text-info-foreground",
  success: "border-success-border bg-success text-success-foreground",
};

interface AlertProps {
  tone: AlertTone;
  title?: string;
  children?: ReactNode;
  className?: string;
}

/** Pesan di dalam form atau bagian. Galat diumumkan langsung ke screen reader. */
export function Alert({ tone, title, children, className }: AlertProps) {
  return (
    <div
      role={tone === "danger" ? "alert" : "status"}
      className={cn("rounded-lg border px-4 py-3 text-sm", toneClasses[tone], className)}
    >
      {title && <p className="font-semibold">{title}</p>}
      {children && <div className={cn(title && "mt-1")}>{children}</div>}
    </div>
  );
}
