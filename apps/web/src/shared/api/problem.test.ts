import { describe, expect, it } from "vitest";
import { ApiError, isProblem, toApiError } from "./problem";

describe("toApiError", () => {
  it("membawa problem dari server apa adanya", () => {
    const err = toApiError({ type: "about:blank", title: "Stok habis", status: 409, code: "out_of_stock", detail: "Stok tidak cukup." });
    expect(err.code).toBe("out_of_stock");
    expect(err.message).toBe("Stok tidak cukup.");
    expect(err.network).toBe(false);
  });

  it("menandai kegagalan jaringan", () => {
    const err = toApiError(new TypeError("Failed to fetch"));
    expect(err.network).toBe(true);
    expect(err.code).toBe("network_error");
  });

  it("tidak membungkus ulang ApiError", () => {
    const original = new ApiError(null, true);
    expect(toApiError(original)).toBe(original);
  });

  it("nilai yang bukan problem menjadi unknown_error", () => {
    expect(toApiError({ foo: 1 }).code).toBe("unknown_error");
    expect(isProblem(null)).toBe(false);
  });
});
