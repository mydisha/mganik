import { X } from "lucide-react";
import { strings } from "@/shared/lib/strings";

interface FilterChipProps {
  label: string;
  onRemove: () => void;
}

/** Filter aktif yang bisa dilepas dengan sekali klik. */
export function FilterChip({ label, onRemove }: FilterChipProps) {
  return (
    <button
      type="button"
      onClick={onRemove}
      aria-label={strings.common.removeFilter(label)}
      className="inline-flex max-w-full items-center gap-1.5 rounded-md border border-input bg-card px-2.5 py-1 text-sm hover:bg-accent active:bg-secondary max-md:min-h-11"
    >
      <span className="truncate">{label}</span>
      <X aria-hidden="true" className="size-3.5 shrink-0" />
    </button>
  );
}
