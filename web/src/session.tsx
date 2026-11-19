import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { useApi } from "./api/context";
import { attempt, type Failure } from "./api/result";
import type { components } from "./api/schema";

export type Session = components["schemas"]["Session"];

export type SessionState =
  | { status: "loading" }
  /** left is true when the person just signed out here. */
  | { status: "signed-out"; left?: boolean }
  | { status: "signed-in"; session: Session }
  | { status: "error"; failure: Failure };

type SessionContextValue = {
  state: SessionState;
  /** Asks the API who is signed in. */
  refresh: () => Promise<void>;
  /** Records a session the API just started. */
  signedIn: (session: Session) => void;
  /** Ends this session. Returns why it failed, if it did. */
  signOut: () => Promise<Failure | undefined>;
  /** Records that the API already ended this session, as deleting the account does. */
  ended: () => void;
};

const SessionContext = createContext<SessionContextValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const [state, setState] = useState<SessionState>({ status: "loading" });

  const load = useCallback(async (): Promise<SessionState> => {
    const res = await attempt(() => api.GET("/v1/sessions/current"));
    if (res.ok && res.data) return { status: "signed-in", session: res.data };
    if (!res.ok && res.failure.status === 401) return { status: "signed-out" };
    if (!res.ok) return { status: "error", failure: res.failure };
    return { status: "error", failure: { code: "empty_response", message: "mailgoose sent an empty answer.", hint: "Try again.", status: 200 } };
  }, [api]);

  const refresh = useCallback(async () => setState(await load()), [load]);

  useEffect(() => {
    let current = true;
    void load().then((next) => {
      if (current) setState(next);
    });
    return () => {
      current = false;
    };
  }, [load]);

  const value = useMemo<SessionContextValue>(
    () => ({
      state,
      refresh,
      signedIn: (session) => setState({ status: "signed-in", session }),
      ended: () => setState({ status: "signed-out", left: true }),
      signOut: async () => {
        const res = await attempt(() => api.DELETE("/v1/sessions/current"));
        if (!res.ok && res.failure.status !== 401) return res.failure;
        setState({ status: "signed-out", left: true });
        return undefined;
      },
    }),
    [api, state, refresh],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionContextValue {
  const value = useContext(SessionContext);
  if (!value) throw new Error("useSession needs a SessionProvider");
  return value;
}

/**
 * The page to go to after signing in, from a ?next= parameter. Only paths
 * on this site count, so a link can't send someone elsewhere after they sign
 * in.
 */
export function safeNext(raw: string | null): string {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//") || raw.startsWith("/\\")) return "/";
  try {
    const base = "https://mailgoose.invalid";
    const url = new URL(raw, base);
    if (url.origin !== base) return "/";
    return url.pathname + url.search + url.hash;
  } catch {
    return "/";
  }
}
