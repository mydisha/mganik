import type { RouteObject } from "react-router";
import { LoginPage } from "./login-page";

// Route autentikasi staff. Berdiri sendiri tanpa AppShell.
export const authRoutes: RouteObject[] = [{ path: "/login", element: <LoginPage /> }];
