import { Copy } from "lucide-react";
import { toast } from "sonner";
import { strings } from "@/shared/lib/strings";
import { Button } from "./button";

interface CopyButtonProps {
  value: string;
  /** Nama nilai untuk aria-label dan toast, mis. "kode serial". */
  label: string;
}

/** Tombol ikon untuk menyalin kode, id, atau rahasia ke clipboard. */
export function CopyButton({ value, label }: CopyButtonProps) {
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      toast.success(strings.common.copied(label.charAt(0).toUpperCase() + label.slice(1)));
    } catch {
      toast.error(strings.common.copyFailed);
    }
  }

  return (
    <Button variant="ghost" size="sm" aria-label={strings.common.copy(label)} title={strings.common.copy(label)} onClick={copy}>
      <Copy aria-hidden="true" />
    </Button>
  );
}

/** Kode serial atau id: mono, bisa dipilih, dan bisa disalin. */
export function CodeText({ value, label }: CopyButtonProps) {
  return (
    <span className="inline-flex max-w-full items-center gap-1">
      <span className="font-mono break-all">{value}</span>
      <CopyButton value={value} label={label} />
    </span>
  );
}
