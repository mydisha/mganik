import { useId, useState } from "react";
import { Alert } from "./alert";
import { Button } from "./button";
import { CopyButton } from "./copy-button";
import { Dialog, DialogContent } from "./dialog";

interface Secret {
  label: string;
  value: string;
}

interface SecretRevealDialogProps {
  open: boolean;
  title: string;
  description: string;
  warning: string;
  secrets: Secret[];
  acknowledgeLabel: string;
  closeLabel: string;
  onClose: () => void;
}

/**
 * Menampilkan rahasia sekali saja. Tidak bisa ditutup (tombol, Escape, atau
 * klik di luar) sebelum pengguna mencentang bahwa rahasianya sudah disimpan.
 */
export function SecretRevealDialog({
  open,
  title,
  description,
  warning,
  secrets,
  acknowledgeLabel,
  closeLabel,
  onClose,
}: SecretRevealDialogProps) {
  const [saved, setSaved] = useState(false);
  const checkboxId = useId();

  function close() {
    if (!saved) return;
    setSaved(false);
    onClose();
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !next && close()}>
      <DialogContent
        title={title}
        description={description}
        onEscapeKeyDown={(e) => !saved && e.preventDefault()}
        onInteractOutside={(e) => e.preventDefault()}
        footer={
          <Button onClick={close} disabled={!saved}>
            {closeLabel}
          </Button>
        }
      >
        <div className="flex flex-col gap-4">
          <Alert tone="warning">{warning}</Alert>
          <dl className="flex flex-col gap-3">
            {secrets.map((secret) => (
              <div key={secret.label} className="space-y-1">
                <dt className="text-sm font-medium">{secret.label}</dt>
                <dd className="flex items-start gap-1 rounded-md border bg-muted px-3 py-2">
                  <span className="min-w-0 flex-1 font-mono text-sm break-all select-all">{secret.value}</span>
                  <CopyButton value={secret.value} label={secret.label.toLowerCase()} />
                </dd>
              </div>
            ))}
          </dl>
          <label htmlFor={checkboxId} className="flex items-start gap-3 text-sm max-md:min-h-11">
            <input
              id={checkboxId}
              type="checkbox"
              checked={saved}
              onChange={(e) => setSaved(e.target.checked)}
              className="mt-0.5 size-4 shrink-0 accent-primary"
            />
            <span>{acknowledgeLabel}</span>
          </label>
        </div>
      </DialogContent>
    </Dialog>
  );
}
