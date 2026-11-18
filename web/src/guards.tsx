import type { ReactNode } from "react";
import { Navigate, useLocation, useSearchParams } from "react-router";

import { AuthCard, FailureNotice, Loading } from "./components";
import { safeNext, useSession } from "./session";

/**
 * Sends people who aren't signed in to sign in, then back here (but not after
 * they sign out themselves).
 */
export function RequireSession({ children }: { children: ReactNode }) {
  const { state, refresh } = useSession();
  const location = useLocation();
  switch (state.status) {
    case "loading":
      return <Loading />;
    case "signed-out": {
      const here = location.pathname + location.search;
      const to = here === "/" || state.left ? "/sign-in" : `/sign-in?next=${encodeURIComponent(here)}`;
      return <Navigate to={to} replace />;
    }
    case "error":
      return <SessionError failure={state.failure} retry={refresh} />;
    case "signed-in":
      return children;
  }
}

/** Sign-in and sign-up pages: once signed in, go on to ?next= or home. */
export function SignedOutOnly({ children }: { children: ReactNode }) {
  const { state, refresh } = useSession();
  const [params] = useSearchParams();
  switch (state.status) {
    case "loading":
      return <Loading />;
    case "signed-in":
      return <Navigate to={safeNext(params.get("next"))} replace />;
    case "error":
      return <SessionError failure={state.failure} retry={refresh} />;
    case "signed-out":
      return children;
  }
}

function SessionError({ failure, retry }: { failure: Parameters<typeof FailureNotice>[0]["failure"]; retry: () => void }) {
  return (
    <AuthCard title="Can't load mailgoose">
      <div className="stack">
        <FailureNotice failure={failure} />
        <button type="button" className="button" onClick={retry}>
          Try again
        </button>
      </div>
    </AuthCard>
  );
}
