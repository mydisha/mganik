import { ApiError, isProblem, toApiError, type Problem } from "@/shared/api/problem";
import { strings } from "./strings";

// Batas tunggu satu request. Request yang lewat batas tidak dibatalkan (fetch
// tetap selesai di belakang), tetapi UI berhenti menunggu dan menampilkan pesan.
export const REQUEST_TIMEOUT_MS = 15_000;

/** Error HTTP dari server: status dan Retry-After ikut dibawa untuk UI. */
export class ResponseError extends ApiError {
  readonly status: number;
  readonly retryAfterSeconds: number | null;

  constructor(problem: Problem | null, status: number, retryAfterSeconds: number | null) {
    super(problem);
    this.status = status;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

/** Server tidak menjawab dalam REQUEST_TIMEOUT_MS. */
export class TimeoutError extends ApiError {
  constructor() {
    super(null, true);
  }

  override get code(): string {
    return "timeout";
  }
}

interface FetchResult<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

function retryAfter(response: Response): number | null {
  const raw = response.headers.get("Retry-After");
  if (!raw) return null;
  const seconds = Number(raw);
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : null;
}

/**
 * Menjalankan satu panggilan openapi-fetch: error server menjadi
 * ResponseError, kegagalan jaringan menjadi ApiError(network), dan request
 * yang terlalu lama menjadi TimeoutError.
 */
export async function apiCall<T>(run: () => Promise<FetchResult<T>>): Promise<{ data: T; response: Response }> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new TimeoutError()), REQUEST_TIMEOUT_MS);
  });
  let result: FetchResult<T>;
  try {
    result = await Promise.race([run(), timeout]);
  } catch (err) {
    throw toApiError(err);
  } finally {
    clearTimeout(timer);
  }
  const { response } = result;
  if (!response.ok || result.error !== undefined) {
    throw new ResponseError(isProblem(result.error) ? result.error : null, response.status, retryAfter(response));
  }
  return { data: result.data as T, response };
}

export function errorStatus(error: unknown): number | null {
  if (error instanceof ResponseError) return error.status;
  if (error instanceof ApiError) return error.problem?.status ?? null;
  return null;
}

export function isForbidden(error: unknown): boolean {
  return errorStatus(error) === 403;
}

/**
 * Pesan untuk ditampilkan. Pesan dari server (`detail`, lalu `title`) dipakai
 * apa adanya; teks frontend hanya untuk jaringan putus, timeout, dan error
 * yang tidak dikenal.
 */
export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.problem?.detail) return error.problem.detail;
    if (error.problem?.title) return error.problem.title;
    if (error.code === "timeout") return strings.errors.timeout;
    if (error.network) return strings.errors.network;
  }
  return strings.errors.unknown;
}

/** Id request dari server, untuk dilaporkan ke tim teknis. */
export function errorRequestId(error: unknown): string | null {
  return error instanceof ApiError ? (error.problem?.request_id ?? null) : null;
}

/** Pesan galat per field dari 422 validation_failed. */
export function fieldErrors(error: unknown): Record<string, string> {
  if (!(error instanceof ApiError) || !error.problem?.errors) return {};
  return Object.fromEntries(error.problem.errors.map((e) => [e.field, e.message]));
}
