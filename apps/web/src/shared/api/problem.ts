import type { components } from "./schema";

// Error API selalu berbentuk application/problem+json (RFC 9457). `detail`
// sudah berbahasa Indonesia dan aman ditampilkan; `code` dipakai untuk
// percabangan logika, bukan untuk menyusun pesan sendiri.
export type Problem = components["schemas"]["Problem"];

export function isProblem(value: unknown): value is Problem {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof (value as Problem).code === "string" &&
    typeof (value as Problem).status === "number"
  );
}

// ApiError membawa Problem dari server, atau menandai kegagalan jaringan
// (server tidak terjangkau) yang perlu pesan dan perlakuan berbeda di UI.
export class ApiError extends Error {
  readonly problem: Problem | null;
  readonly network: boolean;

  constructor(problem: Problem | null, network = false) {
    super(problem?.detail ?? problem?.code ?? (network ? "network_error" : "unknown_error"));
    this.name = "ApiError";
    this.problem = problem;
    this.network = network;
  }

  get code(): string {
    return this.problem?.code ?? (this.network ? "network_error" : "unknown_error");
  }
}

// toApiError mengubah hasil openapi-fetch (`error`) atau exception fetch
// menjadi ApiError.
export function toApiError(error: unknown): ApiError {
  if (error instanceof ApiError) return error;
  if (isProblem(error)) return new ApiError(error);
  if (error instanceof TypeError) return new ApiError(null, true);
  return new ApiError(null);
}
