import { useQuery } from "@tanstack/react-query";
import { createContext, type ReactNode, useContext } from "react";
import { Navigate } from "react-router";
import { ApiError } from "@/shared/api/problem";
import { errorStatus } from "@/shared/lib/api";
import { strings } from "@/shared/lib/strings";
import { ErrorState } from "@/shared/ui/error-state";
import { fetchMe, type Me, meQueryKey } from "./api";
import { authStrings } from "./strings";

const SessionContext = createContext<Me | null>(null);

/** Profil staff dari GET /auth/me. Sesi sendiri ada di cookie HttpOnly. */
export function useMeQuery() {
  return useQuery({ queryKey: meQueryKey, queryFn: fetchMe, staleTime: 5 * 60_000, retry: false });
}

/** Profil staff yang sudah login. Hanya dipakai di dalam RequireSession. */
export function useSession(): Me {
  const me = useContext(SessionContext);
  if (!me) throw new Error("useSession dipakai di luar RequireSession");
  return me;
}

/**
 * Apakah peran pengguna mencakup permission ini. Hanya untuk menyembunyikan
 * aksi; server tetap memutuskan dan bisa menjawab 403.
 */
export function useCan(permission: string): boolean {
  return useSession().permissions.includes(permission);
}

/**
 * Ke mana pengguna console dikirim saat sebuah request menjawab 401.
 * Halaman di luar /console (login, verifikasi konsumen) tidak dialihkan.
 */
export function sessionRedirect(error: unknown, pathname: string): string | null {
  if (!pathname.startsWith("/console") || errorStatus(error) !== 401) return null;
  return error instanceof ApiError && error.code === "session_expired" ? "/login?reason=session_expired" : "/login";
}

interface RequireSessionProps {
  /** Kerangka shell selama profil dimuat, supaya console tidak berkedip. */
  loading: ReactNode;
  children: ReactNode;
}

/** Penjaga semua halaman /console. */
export function RequireSession({ loading, children }: RequireSessionProps) {
  const me = useMeQuery();

  if (me.isPending) return loading;
  if (me.isError) {
    const redirect = sessionRedirect(me.error, "/console");
    if (redirect) return <Navigate to={redirect} replace />;
    return (
      <main className="mx-auto flex min-h-dvh max-w-lg flex-col justify-center gap-4 px-4 py-8">
        <p className="text-base font-semibold">{strings.brand}</p>
        <ErrorState error={me.error} onRetry={() => me.refetch()} title={authStrings.session.loadFailed} />
      </main>
    );
  }
  return <SessionContext value={me.data}>{children}</SessionContext>;
}
