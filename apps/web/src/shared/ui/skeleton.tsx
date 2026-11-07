import { cn } from "@/shared/lib/utils";

/** Placeholder seukuran isi yang sedang dimuat. Diam saat reduced motion. */
export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden="true" data-slot="skeleton" className={cn("rounded-md bg-muted motion-safe:animate-pulse", className)} />;
}
