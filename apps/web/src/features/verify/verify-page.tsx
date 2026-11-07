import { useMutation } from "@tanstack/react-query";
import { type ReactNode, useRef, useState } from "react";
import { useParams } from "react-router";
import { errorMessage } from "@/shared/lib/api";
import { formatDate } from "@/shared/lib/format";
import { strings } from "@/shared/lib/strings";
import { useDocumentTitle } from "@/shared/lib/use-document-title";
import { cn } from "@/shared/lib/utils";
import { Alert } from "@/shared/ui/alert";
import { Button } from "@/shared/ui/button";
import { Spinner } from "@/shared/ui/spinner";
import { type GeoPoint, type VerifyResponse, verifyCode } from "./api";
import { ClaimSection } from "./claim-section";
import { requestLocation } from "./input";
import { verifyStrings as t } from "./strings";

const toneClasses = {
  success: "border-success-border bg-success text-success-foreground",
  warning: "border-warning-border bg-warning text-warning-foreground",
  danger: "border-danger-border bg-danger text-danger-foreground",
};

function ProductFacts({ product }: { product: NonNullable<VerifyResponse["product"]> }) {
  const rows = [
    { label: t.product.name, value: product.name },
    { label: t.product.lot, value: product.lot_no },
    { label: t.product.expiry, value: product.exp_date && formatDate(product.exp_date) },
  ].filter((row) => row.value);
  if (rows.length === 0) return null;
  return (
    <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-sm">
      {rows.map((row) => (
        <div key={row.label} className="contents">
          <dt>{row.label}</dt>
          <dd className="font-medium break-words">{row.value}</dd>
        </div>
      ))}
    </dl>
  );
}

function Steps({ steps }: { steps: readonly string[] }) {
  return (
    <div className="space-y-1">
      <h3 className="text-sm font-semibold">{t.results.stepsHeading}</h3>
      <ol className="list-decimal space-y-1 pl-5 text-sm">
        {steps.map((step) => (
          <li key={step}>{step}</li>
        ))}
      </ol>
    </div>
  );
}

/** Satu hasil besar dan jelas. Status internal serial tidak pernah ditampilkan. */
function ResultCard({ result }: { result: VerifyResponse }) {
  const r = t.results;
  let tone: keyof typeof toneClasses;
  let title: string;
  let body: ReactNode;

  switch (result.result) {
    case "GENUINE_FIRST":
      tone = "success";
      title = r.GENUINE_FIRST.title;
      body = <p>{r.GENUINE_FIRST.body}</p>;
      break;
    case "GENUINE_REPEAT":
      tone = "success";
      title = r.GENUINE_REPEAT.title;
      body = (
        <>
          <p className="font-medium">
            {result.first_verified_at
              ? r.GENUINE_REPEAT.first(formatDate(result.first_verified_at), result.first_verified_city)
              : r.GENUINE_REPEAT.firstUnknown}
          </p>
          <p>{r.GENUINE_REPEAT.meaning}</p>
        </>
      );
      break;
    case "SUSPICIOUS":
      tone = "danger";
      title = r.SUSPICIOUS.title;
      body = (
        <>
          <p>{r.SUSPICIOUS.body}</p>
          <Steps steps={r.SUSPICIOUS.steps} />
        </>
      );
      break;
    case "NOT_FOUND":
      tone = "danger";
      title = r.NOT_FOUND.title;
      body = (
        <>
          <p>{r.NOT_FOUND.body}</p>
          <Steps steps={r.NOT_FOUND.steps} />
        </>
      );
      break;
    case "RECALLED":
      tone = "warning";
      title = r.RECALLED.title;
      body = (
        <>
          <p>{r.RECALLED.body(result.product?.lot_no ?? null)}</p>
          <Steps steps={r.RECALLED.steps} />
        </>
      );
      break;
  }

  return (
    <section aria-labelledby="hasil" aria-live="polite" className={cn("flex flex-col gap-3 rounded-lg border p-5", toneClasses[tone])}>
      <h2 id="hasil" className="text-2xl leading-tight font-semibold">
        {title}
      </h2>
      <div className="flex flex-col gap-3 text-base">{body}</div>
      {result.product && <ProductFacts product={result.product} />}
    </section>
  );
}

/** Halaman publik yang dibuka dari QR hologram. Mobile-first, tanpa login. */
export function VerifyPage() {
  const { code = "" } = useParams();
  const [geo, setGeo] = useState<GeoPoint | null>(null);
  const [locating, setLocating] = useState(false);
  const [locationDenied, setLocationDenied] = useState(false);
  const verify = useMutation({ mutationFn: (point: GeoPoint | null) => verifyCode(code, point) });
  useDocumentTitle(t.title);

  // Izin lokasi diminta setelah pengguna menekan tombol, bukan saat halaman dibuka.
  // "Cek tanpa lokasi" tetap bisa ditekan saat menunggu izin; percobaan yang
  // lebih baru membatalkan hasil permintaan lokasi yang lama.
  const attempt = useRef(0);
  async function start(withLocation: boolean) {
    const id = ++attempt.current;
    let point: GeoPoint | null = null;
    if (withLocation) {
      setLocating(true);
      point = await requestLocation();
      if (id !== attempt.current) return;
      setLocating(false);
      setLocationDenied(point === null);
    } else {
      setLocating(false);
    }
    setGeo(point);
    verify.mutate(point);
  }

  const busy = locating || verify.isPending;
  const genuine = verify.data?.result === "GENUINE_FIRST" || verify.data?.result === "GENUINE_REPEAT";

  return (
    <main className="mx-auto flex min-h-dvh w-full max-w-md flex-col gap-6 px-4 py-8">
      <p className="text-base font-semibold">{strings.brand}</p>
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{t.title}</h1>
        <p className="text-sm text-muted-foreground">
          {t.codeLabel}: <span className="font-mono break-all text-foreground">{code}</span>
        </p>
      </div>

      {verify.isSuccess ? (
        <>
          <ResultCard result={verify.data} />
          {locationDenied && <p className="text-sm text-muted-foreground">{t.locationDenied}</p>}
          {genuine && <ClaimSection code={code} geo={geo} />}
        </>
      ) : verify.isError ? (
        <div className="flex flex-col items-start gap-3">
          <Alert tone="danger" title={t.failedTitle} className="w-full">
            {errorMessage(verify.error)}
          </Alert>
          <Button onClick={() => verify.mutate(geo)}>{t.retry}</Button>
        </div>
      ) : (
        <div className="flex flex-col gap-4 rounded-lg border bg-card p-4 shadow-xs">
          <p className="text-base">{t.locationAsk}</p>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Button size="lg" onClick={() => void start(true)} disabled={busy}>
              {busy && <Spinner />}
              {locating ? t.locating : verify.isPending ? t.checking : t.checkWithLocation}
            </Button>
            <Button size="lg" variant="outline" onClick={() => void start(false)} disabled={verify.isPending}>
              {t.checkWithoutLocation}
            </Button>
          </div>
        </div>
      )}
    </main>
  );
}
