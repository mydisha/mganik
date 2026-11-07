import { strings } from "@/shared/lib/strings";
import { EmptyState } from "@/shared/ui/empty-state";
import { PageHeader } from "@/shared/ui/page-header";
import { ApiStatus } from "./api-status";

export function ConsoleHome() {
  return (
    <div className="flex flex-col gap-6">
      <PageHeader title={strings.console.title} description={strings.console.description} actions={<ApiStatus />} />
      <EmptyState title={strings.console.emptyTitle} description={strings.console.emptyDescription} />
    </div>
  );
}
