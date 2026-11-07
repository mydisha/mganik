import { api } from "@/shared/api/client";
import type { components } from "@/shared/api/schema";
import { apiCall } from "@/shared/lib/api";

type Schemas = components["schemas"];
export type VerifyResponse = Schemas["PublicVerifyResponse"];
export type VerifyResult = Schemas["VerifyResult"];
export type ConsumerMe = Schemas["ConsumerMeResponse"];
export type ClaimResponse = Schemas["LoyaltyClaimResponse"];
export type Balance = Schemas["LoyaltyBalanceResponse"];

export interface GeoPoint {
  lat: number;
  lng: number;
  accuracy_m: number;
}

export const consumerKeys = {
  me: ["consumer", "me"] as const,
  balance: ["consumer", "balance"] as const,
};

const json = { "Content-Type": "application/json" };

export async function verifyCode(code: string, geo: GeoPoint | null): Promise<VerifyResponse> {
  return (await apiCall(() => api.POST("/api/v1/public/verify", { body: { code, ...geo } }))).data;
}

export async function requestOtp(phone: string) {
  return (await apiCall(() => api.POST("/api/v1/public/otp/request", { body: { phone } }))).data;
}

export async function verifyOtp(phone: string, code: string): Promise<ConsumerMe> {
  return (await apiCall(() => api.POST("/api/v1/public/otp/verify", { body: { phone, code } }))).data;
}

export async function fetchConsumer(): Promise<ConsumerMe> {
  return (await apiCall(() => api.GET("/api/v1/public/consumer/me"))).data;
}

export async function logoutConsumer(): Promise<void> {
  await apiCall(() => api.POST("/api/v1/public/consumer/logout", { headers: json }));
}

export async function claimPoints(code: string, pin: string, geo: GeoPoint | null): Promise<ClaimResponse> {
  return (await apiCall(() => api.POST("/api/v1/public/claims", { body: { code, pin, ...geo } }))).data;
}

export async function fetchBalance(): Promise<Balance> {
  return (await apiCall(() => api.GET("/api/v1/public/loyalty/balance"))).data;
}
