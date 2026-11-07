import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/shared/api/problem";
import { apiCall, errorMessage, fieldErrors, REQUEST_TIMEOUT_MS, ResponseError, TimeoutError } from "./api";

const ok = (data: unknown, status = 200) => ({ data, response: new Response(null, { status }) });

afterEach(() => {
  vi.useRealTimers();
});

describe("apiCall", () => {
  it("mengembalikan data untuk respons sukses, termasuk 204", async () => {
    await expect(apiCall(async () => ok({ a: 1 }))).resolves.toMatchObject({ data: { a: 1 } });
    await expect(apiCall(async () => ok(undefined, 204))).resolves.toMatchObject({ data: undefined });
  });

  it("membawa problem, status, dan Retry-After", async () => {
    const problem = { type: "about:blank", title: "Terlalu sering", status: 429, code: "too_many_attempts", detail: "Coba lagi dalam 3 menit." };
    const err = await apiCall(async () => ({ error: problem, response: new Response(null, { status: 429, headers: { "Retry-After": "180" } }) })).catch(
      (e: unknown) => e,
    );
    expect(err).toBeInstanceOf(ResponseError);
    expect((err as ResponseError).status).toBe(429);
    expect((err as ResponseError).retryAfterSeconds).toBe(180);
    expect(errorMessage(err)).toBe("Coba lagi dalam 3 menit.");
  });

  it("body bukan problem tetap menjadi error dengan pesan umum", async () => {
    const err = await apiCall(async () => ({ error: "<html>502</html>", response: new Response(null, { status: 502 }) })).catch((e: unknown) => e);
    expect((err as ResponseError).status).toBe(502);
    expect(errorMessage(err)).toBe("Terjadi kesalahan yang tidak terduga. Coba lagi beberapa saat lagi.");
  });

  it("fetch yang gagal menjadi error jaringan", async () => {
    const err = await apiCall(() => Promise.reject(new TypeError("Failed to fetch"))).catch((e: unknown) => e);
    expect((err as ApiError).network).toBe(true);
    expect(errorMessage(err)).toBe("Tidak bisa terhubung ke server. Periksa koneksi internet, lalu coba lagi.");
  });

  it("berhenti menunggu setelah batas waktu", async () => {
    vi.useFakeTimers();
    const pending = apiCall(() => new Promise<never>(() => {})).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS);
    const err = await pending;
    expect(err).toBeInstanceOf(TimeoutError);
    expect(errorMessage(err)).toBe("Server terlalu lama menjawab. Coba lagi sebentar lagi.");
  });
});

describe("fieldErrors", () => {
  it("memetakan errors 422 per field", () => {
    const err = new ApiError({
      type: "about:blank",
      title: "Tidak valid",
      status: 422,
      code: "validation_failed",
      errors: [{ field: "name", code: "too_long", message: "Nama terlalu panjang." }],
    });
    expect(fieldErrors(err)).toEqual({ name: "Nama terlalu panjang." });
    expect(fieldErrors(new Error("x"))).toEqual({});
  });
});
