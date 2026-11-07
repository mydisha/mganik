import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ApiStatus } from "./api-status";

function renderWithQuery() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ApiStatus />
    </QueryClientProvider>,
  );
}

function stubFetch(status: number, body: unknown, contentType = "application/json") {
  const fetchMock = vi.fn(
    async (_request: Request) =>
      new Response(JSON.stringify(body), { status, headers: { "Content-Type": contentType } }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("ApiStatus", () => {
  it("menampilkan versi skema saat API siap", async () => {
    const fetchMock = stubFetch(200, { status: "ready", schema_version: 1, checked_at: "2026-11-07T03:00:00Z" });

    renderWithQuery();

    expect(screen.getByText("Memeriksa…")).toBeInTheDocument();
    expect(await screen.findByText("Terhubung · skema v1")).toBeInTheDocument();
    const request = fetchMock.mock.calls[0]?.[0];
    expect(request && new URL(request.url).pathname).toBe("/readyz");
  });

  it("menampilkan status gagal dan tombol coba lagi saat API belum siap", async () => {
    stubFetch(
      503,
      { type: "about:blank", title: "Belum siap", status: 503, code: "database_unavailable" },
      "application/problem+json",
    );

    renderWithQuery();

    expect(await screen.findByText("API belum siap")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Coba lagi" })).toBeInTheDocument();
  });
});
