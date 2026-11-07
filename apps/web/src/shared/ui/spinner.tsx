import { LoaderCircle } from "lucide-react";

/** Penanda proses di dalam tombol; teks tombol tetap menyebut kerjanya. */
export function Spinner() {
  return <LoaderCircle aria-hidden="true" className="size-4 motion-safe:animate-spin" />;
}
