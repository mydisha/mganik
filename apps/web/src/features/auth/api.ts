import { api } from "@/shared/api/client";
import type { components } from "@/shared/api/schema";
import { apiCall } from "@/shared/lib/api";

export type Me = components["schemas"]["MeResponse"];
export type LoginRequest = components["schemas"]["LoginRequest"];

export const meQueryKey = ["auth", "me"] as const;

// Request ber-cookie dengan metode tulis wajib Content-Type JSON (CSRF), juga
// saat tidak ada body.
const jsonHeaders = { "Content-Type": "application/json" };

export async function fetchMe(): Promise<Me> {
  const { data } = await apiCall(() => api.GET("/api/v1/auth/me"));
  return data;
}

export async function login(body: LoginRequest): Promise<Me> {
  const { data } = await apiCall(() => api.POST("/api/v1/auth/login", { body }));
  return data;
}

export async function logout(): Promise<void> {
  await apiCall(() => api.POST("/api/v1/auth/logout", { headers: jsonHeaders }));
}
