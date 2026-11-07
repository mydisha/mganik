import { LoginScreen } from "./login-screen";
import { SellScreen } from "./sell-screen";
import { posStrings as s } from "./strings";
import { PosHeader } from "./ui/pos-header";
import { useSession } from "./use-pos-data";

// /pos: layar jual untuk kasir yang sudah login, selain itu layar login.
// Sesi disimpan di IndexedDB, jadi refresh di tengah transaksi tidak
// mengeluarkan kasir; "Kunci layar" yang mengakhirinya.
export function PosHome() {
  const session = useSession();
  if (session === undefined) {
    return (
      <main className="flex min-h-dvh items-center justify-center px-4" aria-busy="true">
        <p role="status" className="text-base text-muted-foreground">
          {s.boot.loading}
        </p>
      </main>
    );
  }
  if (session) return <SellScreen />;
  return (
    <div className="flex min-h-dvh flex-col">
      <PosHeader />
      <main className="flex flex-1 justify-center px-4 py-8">
        <LoginScreen />
      </main>
    </div>
  );
}
