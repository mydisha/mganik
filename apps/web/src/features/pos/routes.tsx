import type { RouteObject } from "react-router";
import { PosHome } from "./pos-home";

// Route kasir. Berdiri sendiri (tanpa AppShell console): layar penuh untuk
// perangkat toko. Path relatif terhadap /pos.
export const posRoutes: RouteObject[] = [{ index: true, element: <PosHome /> }];
