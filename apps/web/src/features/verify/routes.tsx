import type { RouteObject } from "react-router";
import { VerifyPage } from "./verify-page";

// Halaman publik konsumen dari QR hologram. Path relatif terhadap /v.
export const verifyRoutes: RouteObject[] = [{ path: ":code", element: <VerifyPage /> }];
