import { createContext, useContext, type ReactNode } from "react";

import type { createApiClient } from "./client";

export type ApiClient = ReturnType<typeof createApiClient>;

const ApiContext = createContext<ApiClient | null>(null);

/** Gives the app its API client. Tests pass one with a fake fetch. */
export function ApiProvider({ client, children }: { client: ApiClient; children: ReactNode }) {
  return <ApiContext.Provider value={client}>{children}</ApiContext.Provider>;
}

export function useApi(): ApiClient {
  const client = useContext(ApiContext);
  if (!client) throw new Error("useApi needs an ApiProvider");
  return client;
}
