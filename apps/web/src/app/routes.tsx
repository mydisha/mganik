import { createBrowserRouter, Navigate } from "react-router";
import { authRoutes } from "@/features/auth/routes";
import { consoleRoutes } from "@/features/console/routes";
import { posRoutes } from "@/features/pos/routes";
import { verifyRoutes } from "@/features/verify/routes";
import { AppShell } from "./app-shell";

// Tiga permukaan, masing-masing mendefinisikan route-nya sendiri di
// features/<fitur>/routes.tsx supaya pekerjaan paralel tidak saling tabrak:
// /console (staff, di dalam AppShell), /pos (kasir, layar penuh tanpa shell),
// /v/:code (konsumen, tanpa shell), plus route auth (/login).
export function createAppRouter() {
  return createBrowserRouter([
    { path: "/", element: <Navigate to="/console" replace /> },
    ...authRoutes,
    { path: "/console", element: <AppShell />, children: consoleRoutes },
    { path: "/pos", children: posRoutes },
    { path: "/v", children: verifyRoutes },
    { path: "*", element: <Navigate to="/console" replace /> },
  ]);
}
