import { strings } from "@/shared/lib/strings";
import { EmptyState } from "@/shared/ui/empty-state";
import { PageHeader } from "@/shared/ui/page-header";

export function PosHome() {
  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={strings.pos.title} description={strings.pos.description} />
      <EmptyState title={strings.pos.emptyTitle} description={strings.pos.emptyDescription} />
    </div>
  );
}
