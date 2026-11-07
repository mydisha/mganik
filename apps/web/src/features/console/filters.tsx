import { type ReactNode, useId } from "react";
import { useSearchParams } from "react-router";
import { strings } from "@/shared/lib/strings";
import { Button } from "@/shared/ui/button";
import { FilterChip } from "@/shared/ui/filter-chip";
import { Select } from "@/shared/ui/select";
import { consoleStrings as t } from "./strings";

/**
 * Filter disimpan di URL supaya tampilan bisa dibagikan dan tombol kembali
 * bekerja. Mengubah filter tidak menambah riwayat untuk setiap pilihan.
 */
export function useUrlFilters<K extends string>(keys: readonly K[]) {
  const [params, setParams] = useSearchParams();
  const values = Object.fromEntries(keys.map((key) => [key, params.get(key) ?? ""])) as Record<K, string>;

  function set(key: K, value: string) {
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        if (value) next.set(key, value);
        else next.delete(key);
        return next;
      },
      { replace: true },
    );
  }

  function clear() {
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev);
        for (const key of keys) next.delete(key);
        return next;
      },
      { replace: true },
    );
  }

  /** Alamat daftar yang sama dengan satu filter tambahan, untuk tautan di baris. */
  function hrefWith(key: K, value: string) {
    const next = new URLSearchParams(params);
    next.set(key, value);
    return `?${next.toString()}`;
  }

  const active = keys.some((key) => values[key] !== "");
  return { values, set, clear, hrefWith, active };
}

interface SelectFilterProps {
  label: string;
  value: string;
  allLabel: string;
  /** Nilai URL untuk pilihan "semua" bila daftar tidak dimulai dari semua (mis. antrean dimulai dari Terbuka). */
  allValue?: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
}

export function SelectFilter({ label, value, allLabel, allValue = "", options, onChange }: SelectFilterProps) {
  const id = useId();
  return (
    <div className="flex min-w-0 flex-col gap-1.5 sm:w-56">
      <label htmlFor={id} className="text-xs font-medium text-muted-foreground">
        {label}
      </label>
      <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value={allValue}>{allLabel}</option>
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </Select>
    </div>
  );
}

interface FilterBarProps {
  children?: ReactNode;
  chips: { key: string; label: string; onRemove: () => void }[];
  active: boolean;
  onClear: () => void;
}

/** Kontrol filter, filter aktif dari tautan baris, dan "Hapus semua filter". */
export function FilterBar({ children, chips, active, onClear }: FilterBarProps) {
  return (
    <div className="flex flex-col gap-3">
      {children && <div className="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-end">{children}</div>}
      {(chips.length > 0 || active) && (
        <div className="flex flex-wrap items-center gap-2">
          {chips.map((chip) => (
            <FilterChip key={chip.key} label={chip.label} onRemove={chip.onRemove} />
          ))}
          {active && (
            <Button variant="ghost" size="sm" onClick={onClear}>
              {strings.common.clearFilters}
            </Button>
          )}
        </div>
      )}
    </div>
  );
}

export const locationTypeOptions = Object.entries(t.locationTypes).map(([value, label]) => ({ value, label }));
