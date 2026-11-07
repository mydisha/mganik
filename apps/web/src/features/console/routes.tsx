import type { RouteObject } from "react-router";
import { ConsoleHome } from "./console-home";

// Route Command Center (staff). Dipasang di bawah AppShell oleh app/routes.tsx.
// Path relatif terhadap /console.
export const consoleRoutes: RouteObject[] = [{ index: true, element: <ConsoleHome /> }];
