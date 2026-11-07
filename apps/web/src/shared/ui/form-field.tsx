import type { ReactNode } from "react";
import { cn } from "@/shared/lib/utils";

export interface FieldControlProps {
  id: string;
  "aria-invalid": true | undefined;
  "aria-describedby": string | undefined;
}

interface FormFieldProps {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  className?: string;
  children: (control: FieldControlProps) => ReactNode;
}

/** Label terlihat, petunjuk, dan pesan galat yang terhubung ke kontrolnya. */
export function FormField({ id, label, hint, error, className, children }: FormFieldProps) {
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [hintId, errorId].filter(Boolean).join(" ") || undefined;

  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <label htmlFor={id} className="text-sm font-medium">
        {label}
      </label>
      {children({ id, "aria-invalid": error ? true : undefined, "aria-describedby": describedBy })}
      {hint && (
        <p id={hintId} className="text-xs text-muted-foreground">
          {hint}
        </p>
      )}
      {error && (
        <p id={errorId} className="text-xs font-medium text-danger-foreground">
          {error}
        </p>
      )}
    </div>
  );
}
