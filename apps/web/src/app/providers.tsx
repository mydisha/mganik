import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type ReactNode, useEffect } from "react";
import { meQueryKey } from "@/features/auth/api";
import { sessionRedirect } from "@/features/auth/session";
import { errorStatus } from "@/shared/lib/api";
import { Toaster } from "@/shared/ui/toaster";

interface NavigatingRouter {
  state: { location: { pathname: string } };
  navigate: (to: string, options: { replace: boolean }) => Promise<void>;
}

// Error 4xx tidak membaik dengan dicoba ulang; jaringan dan 5xx dicoba sekali.
function shouldRetry(failureCount: number, error: unknown) {
  const status = errorStatus(error);
  if (status !== null && status < 500) return false;
  return failureCount < 1;
}

/**
 * QueryClient aplikasi. Request console yang menjawab 401 menghapus semua
 * cache dan mengirim pengguna ke /login (dengan pesan kalau sesinya habis).
 * Query profil (/auth/me) ditangani penjaganya sendiri.
 */
export function createQueryClient(router: NavigatingRouter): QueryClient {
  const client: QueryClient = new QueryClient({
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (query.queryKey[0] !== meQueryKey[0]) onAuthError(error);
      },
    }),
    mutationCache: new MutationCache({ onError: (error) => onAuthError(error) }),
    defaultOptions: {
      queries: { retry: shouldRetry, refetchOnWindowFocus: false },
      mutations: { retry: false },
    },
  });

  function onAuthError(error: unknown) {
    const to = sessionRedirect(error, router.state.location.pathname);
    if (!to) return;
    client.clear();
    void router.navigate(to, { replace: true });
  }

  return client;
}

// Tema mengikuti sistem, termasuk saat pengguna menggantinya ketika app terbuka.
function useSystemTheme() {
  useEffect(() => {
    const media = globalThis.matchMedia?.("(prefers-color-scheme: dark)");
    if (!media) return;
    const apply = () => document.documentElement.classList.toggle("dark", media.matches);
    media.addEventListener("change", apply);
    return () => media.removeEventListener("change", apply);
  }, []);
}

export function AppProviders({ client, children }: { client: QueryClient; children: ReactNode }) {
  useSystemTheme();
  return (
    <QueryClientProvider client={client}>
      {children}
      <Toaster />
    </QueryClientProvider>
  );
}
