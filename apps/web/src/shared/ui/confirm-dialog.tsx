import { type ReactNode, useId, useState } from "react";
import { strings } from "@/shared/lib/strings";
import { Alert } from "./alert";
import { Button } from "./button";
import { Dialog, DialogContent } from "./dialog";
import { FormField } from "./form-field";
import { Input } from "./input";
import { Spinner } from "./spinner";

interface ConfirmDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: ReactNode;
  /** Teks yang wajib diketik persis (peka huruf besar) sebelum aksi bisa dijalankan. */
  confirmText: string;
  confirmTextLabel: string;
  confirmLabel: string;
  pendingLabel: string;
  pending: boolean;
  error: string | null;
  onConfirm: () => void;
  /** Input tambahan di atas kolom ketik-konfirmasi. */
  children?: ReactNode;
}

function same(a: string, b: string) {
  return a.normalize("NFC") === b.normalize("NFC");
}

/** Konfirmasi aksi merusak yang langsung memutus sesuatu yang sedang dipakai. */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmText,
  confirmTextLabel,
  confirmLabel,
  pendingLabel,
  pending,
  error,
  onConfirm,
  children,
}: ConfirmDialogProps) {
  const [typed, setTyped] = useState("");
  const inputId = useId();
  const matches = same(typed, confirmText);

  function change(next: boolean) {
    if (pending) return;
    if (!next) setTyped("");
    onOpenChange(next);
  }

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogContent
        title={title}
        description={description}
        footer={
          <>
            <Button variant="outline" onClick={() => change(false)} disabled={pending}>
              {strings.common.cancel}
            </Button>
            <Button variant="destructive" onClick={onConfirm} disabled={!matches || pending}>
              {pending && <Spinner />}
              {pending ? pendingLabel : confirmLabel}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          {error && <Alert tone="danger">{error}</Alert>}
          {children}
          <FormField id={inputId} label={confirmTextLabel}>
            {(control) => (
              <Input
                {...control}
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                disabled={pending}
              />
            )}
          </FormField>
        </div>
      </DialogContent>
    </Dialog>
  );
}
