import { Dialog as DialogPrimitive } from "radix-ui";
import type { ComponentProps, ReactNode } from "react";
import { cn } from "@/shared/lib/utils";

export const Dialog = DialogPrimitive.Root;

const overlayClasses = "fixed inset-0 z-50 bg-overlay";

type ContentProps = ComponentProps<typeof DialogPrimitive.Content> & {
  title: string;
  description: ReactNode;
  footer: ReactNode;
};

/**
 * Dialog untuk tugas singkat yang fokus. Selalu punya judul, deskripsi, dan
 * footer aksi; tutup lewat tombol di footer atau Escape (kecuali dicegah).
 * Muat di layar 375 px dan isinya menggulung di dalam.
 */
export function DialogContent({ title, description, footer, children, className, ...props }: ContentProps) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className={overlayClasses} />
      <DialogPrimitive.Content
        className={cn(
          "fixed top-1/2 left-1/2 z-50 flex max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-lg -translate-x-1/2 -translate-y-1/2 flex-col rounded-lg border bg-popover text-popover-foreground shadow-lg",
          className,
        )}
        {...props}
      >
        <div className="space-y-1 border-b px-5 py-4">
          <DialogPrimitive.Title className="text-base font-semibold">{title}</DialogPrimitive.Title>
          <DialogPrimitive.Description className="text-sm text-muted-foreground">{description}</DialogPrimitive.Description>
        </div>
        <div className="flex-1 overflow-y-auto px-5 py-4">{children}</div>
        <div className="flex flex-col-reverse gap-2 border-t px-5 py-4 sm:flex-row sm:justify-end">{footer}</div>
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}
