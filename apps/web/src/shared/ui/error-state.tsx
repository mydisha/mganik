import { errorMessage, errorRequestId, isForbidden } from "@/shared/lib/api";
import { strings } from "@/shared/lib/strings";
import { cn } from "@/shared/lib/utils";
import { Button } from "./button";

interface ForbiddenStateProps {
  className?: string;
}

/** Halaman atau bagian yang tidak boleh dibuka peran ini; tampil di tempat isinya. */
export function ForbiddenState({ className }: ForbiddenStateProps) {
  return (
    <div className={cn("rounded-lg border bg-card px-6 py-10 text-center", className)}>
      <h2 className="text-base font-semibold">{strings.errors.forbiddenTitle}</h2>
      <p className="mx-auto mt-1 max-w-md text-sm text-muted-foreground">{strings.errors.forbiddenDescription}</p>
    </div>
  );
}

interface ErrorStateProps {
  error: unknown;
  onRetry: () => void;
  title?: string;
  className?: string;
}

/** Gagal memuat: apa yang gagal dan tombol coba lagi. 403 menjadi ForbiddenState. */
export function ErrorState({ error, onRetry, title = strings.errors.loadTitle, className }: ErrorStateProps) {
  if (isForbidden(error)) return <ForbiddenState className={className} />;
  const requestId = errorRequestId(error);

  return (
    <div role="alert" className={cn("rounded-lg border border-danger-border bg-card px-6 py-10 text-center", className)}>
      <h2 className="text-base font-semibold">{title}</h2>
      <p className="mx-auto mt-1 max-w-md text-sm text-muted-foreground">{errorMessage(error)}</p>
      {requestId && (
        <p className="mt-2 text-xs text-muted-foreground">
          {strings.common.requestId}: <span className="font-mono break-all">{requestId}</span>
        </p>
      )}
      <Button variant="outline" className="mt-4" onClick={onRetry}>
        {strings.common.retry}
      </Button>
    </div>
  );
}
