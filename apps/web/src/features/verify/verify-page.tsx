import { useParams } from "react-router";
import { strings } from "@/shared/lib/strings";

/** Halaman publik yang dibuka dari QR hologram. Mobile-first, tanpa login. */
export function VerifyPage() {
  const { code = "" } = useParams();

  return (
    <main className="mx-auto flex min-h-dvh max-w-md flex-col gap-6 px-4 py-8">
      <p className="text-base font-semibold">{strings.brand}</p>
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{strings.verify.title}</h1>
        <p className="text-sm text-muted-foreground">
          {strings.verify.codeLabel}: <span className="font-mono break-all text-foreground">{code}</span>
        </p>
      </div>
      <div className="rounded-lg border bg-card p-4 text-sm text-muted-foreground">{strings.verify.pending}</div>
    </main>
  );
}
