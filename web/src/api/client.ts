// Typed client for the mailgoose API. The types come from schema.d.ts, which
// `make generate` writes from api/openapi.yaml; don't edit that file by hand.
//
//   const api = createApiClient({ baseUrl: "https://api.staging.mailgoose.app" });
//   const { data, error } = await api.GET("/healthz");
import createClient, { type ClientOptions } from "openapi-fetch";

import type { components, paths } from "./schema";

export type { components, paths };

/** The error body every failed request returns. */
export type ApiError = components["schemas"]["Error"];

export function createApiClient(options: ClientOptions = {}) {
  return createClient<paths>(options);
}
