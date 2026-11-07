import createClient from "openapi-fetch";
import type { paths } from "./schema";

// Client bertipe dari packages/contracts/openapi.yaml (`pnpm gen:api`).
// fetch dibaca saat request supaya test bisa mengganti globalThis.fetch.
export const api = createClient<paths>({
  baseUrl: globalThis.location?.origin ?? "",
  fetch: (request) => globalThis.fetch(request),
});
