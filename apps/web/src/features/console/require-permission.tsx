import type { ReactNode } from "react";
import { useCan } from "@/features/auth/session";
import { ForbiddenState } from "@/shared/ui/error-state";
import { PageHeader } from "@/shared/ui/page-header";

interface RequirePermissionProps {
  permission: string;
  title: string;
  children: ReactNode;
}

/** Halaman yang tidak boleh dibuka peran ini: judul tetap ada, isinya ForbiddenState. */
export function RequirePermission({ permission, title, children }: RequirePermissionProps) {
  if (!useCan(permission)) {
    return (
      <div className="flex flex-col gap-6">
        <PageHeader title={title} />
        <ForbiddenState />
      </div>
    );
  }
  return children;
}
