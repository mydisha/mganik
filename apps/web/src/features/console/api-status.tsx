import { useQuery } from "@tanstack/react-query";
import { api } from "@/shared/api/client";
import { toApiError } from "@/shared/api/problem";
import { strings } from "@/shared/lib/strings";
import { Button } from "@/shared/ui/button";
import { StatusBadge } from "@/shared/ui/status-badge";

/** Menampilkan apakah backend dan database siap (GET /readyz). */
export function ApiStatus() {
  const ready = useQuery({
    queryKey: ["readyz"],
    queryFn: async () => {
      try {
        const { data, error } = await api.GET("/readyz");
        if (error || !data) throw toApiError(error);
        return data;
      } catch (err) {
        throw toApiError(err);
      }
    },
    retry: false,
  });

  return (
    <div className="flex items-center gap-2" aria-live="polite">
      <span className="text-xs text-muted-foreground">{strings.apiStatus.label}</span>
      {ready.isPending && <StatusBadge tone="neutral" label={strings.apiStatus.checking} />}
      {ready.isSuccess && <StatusBadge tone="success" label={strings.apiStatus.ready(ready.data.schema_version)} />}
      {ready.isError && (
        <>
          <StatusBadge tone="danger" label={strings.apiStatus.notReady} />
          <Button variant="outline" size="sm" onClick={() => ready.refetch()}>
            {strings.apiStatus.retry}
          </Button>
        </>
      )}
    </div>
  );
}
