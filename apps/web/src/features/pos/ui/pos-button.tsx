import { Button, type ButtonProps } from "@/shared/ui/button";
import { cn } from "@/shared/lib/utils";

// Ukuran tombol kasir di atas Button bersama (DESIGN.md ## Kasir): target
// sentuh minimal 48 px, aksi utama 56-64 px, tombol numpad 64 × 64 px.
const posSizes = {
  touch: "h-12 px-4 text-base has-[>svg]:px-3.5 [&_svg:not([class*='size-'])]:size-5",
  pos: "h-14 px-5 text-lg has-[>svg]:px-4 [&_svg:not([class*='size-'])]:size-5",
  primary: "h-16 px-6 text-xl font-semibold [&_svg:not([class*='size-'])]:size-6",
  key: "h-16 min-w-16 p-0 text-2xl font-medium tabular-nums [&_svg:not([class*='size-'])]:size-6",
  icon: "size-12 p-0 [&_svg:not([class*='size-'])]:size-5",
} as const;

export type PosButtonSize = keyof typeof posSizes;

export function PosButton({
  posSize = "touch",
  className,
  ...props
}: Omit<ButtonProps, "size"> & { posSize?: PosButtonSize }) {
  return <Button className={cn(posSizes[posSize], "cursor-pointer disabled:cursor-not-allowed", className)} {...props} />;
}

// Pintasan keyboard ditampilkan di tombolnya sendiri.
export function ShortcutHint({ keys }: { keys: string }) {
  return (
    <kbd className="rounded border border-current/30 px-1.5 py-0.5 font-mono text-xs font-medium opacity-80">{keys}</kbd>
  );
}
