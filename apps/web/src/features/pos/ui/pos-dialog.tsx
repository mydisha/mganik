import { AlertDialog, Dialog } from "radix-ui";
import type { ReactNode, RefObject } from "react";
import { cn } from "@/shared/lib/utils";
import { PosButton, ShortcutHint } from "./pos-button";

// Dialog kasir di atas Radix: fokus terkunci di dalam, Esc menutup, dan saat
// ditutup fokus dikembalikan ke kolom scan lewat `onClosed` (DESIGN.md: kolom
// scan selalu mendapat fokus kembali setelah dialog ditutup).

const overlay = "fixed inset-0 z-40 bg-overlay motion-safe:data-[state=open]:animate-in motion-safe:data-[state=open]:fade-in-0";
const panel =
  "fixed left-1/2 top-1/2 z-50 flex max-h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] -translate-x-1/2 -translate-y-1/2 flex-col rounded-lg border bg-popover text-popover-foreground shadow-lg outline-none";

interface PosDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: ReactNode;
  children: ReactNode;
  footer?: ReactNode;
  size?: "md" | "lg";
  onClosed?: () => void;
  initialFocus?: RefObject<HTMLElement | null>;
}

export function PosDialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
  size = "md",
  onClosed,
  initialFocus,
}: PosDialogProps) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className={overlay} />
        <Dialog.Content
          className={cn(panel, size === "md" ? "max-w-xl" : "max-w-3xl")}
          onOpenAutoFocus={(e) => {
            if (initialFocus?.current) {
              e.preventDefault();
              initialFocus.current.focus();
            }
          }}
          onCloseAutoFocus={(e) => {
            if (onClosed) {
              e.preventDefault();
              onClosed();
            }
          }}
          {...(description ? {} : { "aria-describedby": undefined })}
        >
          <div className="border-b px-5 py-4">
            <Dialog.Title className="text-lg font-semibold">{title}</Dialog.Title>
            {description && (
              <Dialog.Description className="mt-1 text-sm text-muted-foreground">{description}</Dialog.Description>
            )}
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
          {footer && <div className="flex flex-wrap justify-end gap-3 border-t px-5 py-4">{footer}</div>}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

interface ConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  body: ReactNode;
  confirmLabel: string;
  cancelLabel: string;
  onConfirm: () => void;
  destructive?: boolean;
  onClosed?: () => void;
  children?: ReactNode;
}

// Konfirmasi aksi yang membuang data (hapus baris, batalkan transaksi,
// keluarkan perangkat). Tombol batal mendapat fokus awal: Enter tidak langsung
// menghapus.
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  body,
  confirmLabel,
  cancelLabel,
  onConfirm,
  destructive = true,
  onClosed,
  children,
}: ConfirmDialogProps) {
  return (
    <AlertDialog.Root open={open} onOpenChange={onOpenChange}>
      <AlertDialog.Portal>
        <AlertDialog.Overlay className={overlay} />
        <AlertDialog.Content
          className={cn(panel, "max-w-md p-5")}
          onCloseAutoFocus={(e) => {
            if (onClosed) {
              e.preventDefault();
              onClosed();
            }
          }}
        >
          <AlertDialog.Title className="text-lg font-semibold">{title}</AlertDialog.Title>
          <AlertDialog.Description className="mt-2 text-base text-muted-foreground">{body}</AlertDialog.Description>
          {children}
          <div className="mt-5 flex flex-wrap justify-end gap-3">
            <AlertDialog.Cancel asChild>
              <PosButton posSize="pos" variant="outline">
                {cancelLabel} <ShortcutHint keys="Esc" />
              </PosButton>
            </AlertDialog.Cancel>
            <AlertDialog.Action asChild>
              <PosButton posSize="pos" variant={destructive ? "destructive" : "default"} onClick={onConfirm}>
                {confirmLabel}
              </PosButton>
            </AlertDialog.Action>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  );
}
