import { X } from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import type { ReactNode } from "react";
import { strings } from "@/shared/lib/strings";
import { cn } from "@/shared/lib/utils";
import { Button } from "./button";

interface SidePanelProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: ReactNode;
  footer?: ReactNode;
  /** Kanan untuk detail record, kiri untuk menu navigasi di layar kecil. */
  side?: "left" | "right";
  /** Menahan panel tetap terbuka selama request berjalan. */
  locked?: boolean;
}

/**
 * Panel yang meluncur dari tepi layar: penuh di ponsel, 36rem mulai 640 px.
 * Header dan footer tetap, isinya menggulung.
 */
export function SidePanel({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  side = "right",
  locked = false,
}: SidePanelProps) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(next) => (!locked || next) && onOpenChange(next)}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-overlay" />
        <DialogPrimitive.Content
          className={cn(
            "fixed inset-y-0 z-50 flex w-full flex-col bg-popover text-popover-foreground shadow-lg",
            side === "right" ? "right-0 border-l sm:max-w-xl" : "left-0 border-r sm:max-w-xs",
          )}
        >
          <div className="flex items-start justify-between gap-3 border-b px-4 py-3 md:px-5">
            <div className="min-w-0 space-y-1">
              <DialogPrimitive.Title className="text-base font-semibold">{title}</DialogPrimitive.Title>
              {description ? (
                <DialogPrimitive.Description className="text-sm text-muted-foreground">{description}</DialogPrimitive.Description>
              ) : (
                <DialogPrimitive.Description className="sr-only">{title}</DialogPrimitive.Description>
              )}
            </div>
            <DialogPrimitive.Close asChild>
              <Button variant="ghost" size="sm" aria-label={strings.common.close} disabled={locked}>
                <X aria-hidden="true" />
              </Button>
            </DialogPrimitive.Close>
          </div>
          <div className="flex-1 overflow-y-auto px-4 py-4 md:px-5">{children}</div>
          {footer && <div className="flex flex-col-reverse gap-2 border-t px-4 py-3 sm:flex-row sm:justify-end md:px-5">{footer}</div>}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
