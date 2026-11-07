import { Toaster as Sonner } from "sonner";

/** Hasil aksi yang baru dijalankan pengguna. Bukan untuk galat saat memuat data. */
export function Toaster() {
  return (
    <Sonner
      theme="system"
      position="bottom-center"
      toastOptions={{
        classNames: {
          toast: "bg-popover! text-popover-foreground! border-border! shadow-md! font-sans!",
          description: "text-muted-foreground!",
        },
      }}
    />
  );
}
