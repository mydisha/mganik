import type { ReactNode } from "react";
import { useDocumentTitle } from "@/shared/lib/use-document-title";

interface PageHeaderProps {
  title: string;
  description?: string;
  actions?: ReactNode;
}

/** Satu h1 per halaman, satu kalimat konteks, dan aksi halaman di kanan. */
export function PageHeader({ title, description, actions }: PageHeaderProps) {
  useDocumentTitle(title);
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{title}</h1>
        {description && <p className="text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}
