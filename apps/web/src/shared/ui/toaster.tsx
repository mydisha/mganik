import { Toaster as Sonner } from "sonner";
// CSP `style-src 'self'` memblokir <style> yang disisipkan sonner saat
// runtime, jadi stylesheet-nya ikut dibundel sebagai file CSS biasa.
import "sonner/dist/styles.css";

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
