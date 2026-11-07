import { createBrowserRouter, Navigate } from "react-router";
import { ConsoleHome } from "@/features/console/console-home";
import { PosHome } from "@/features/pos/pos-home";
import { VerifyPage } from "@/features/verify/verify-page";
import { AppShell } from "./app-shell";

// Tiga permukaan: /console (ops), /pos (kasir), /v/:code (konsumen, tanpa shell).
export function createAppRouter() {
  return createBrowserRouter([
    { path: "/", element: <Navigate to="/console" replace /> },
    {
      element: <AppShell />,
      children: [
        { path: "/console", element: <ConsoleHome /> },
        { path: "/pos", element: <PosHome /> },
      ],
    },
    { path: "/v/:code", element: <VerifyPage /> },
    { path: "*", element: <Navigate to="/console" replace /> },
  ]);
}
