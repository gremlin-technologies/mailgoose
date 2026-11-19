import { useCallback, useState, type ReactNode } from "react";
import { Link } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { FailureNotice, useLoad } from "../components";
import { currentPolicyVersion, policyChangesPath } from "../policyVersions.gen";
import { formatPolicyDate } from "./Policies";

type TermsStatus = components["schemas"]["TermsStatus"];

/**
 * Asks the person to accept the terms again when they've changed since the
 * account last accepted them (MG-68), before showing the page. Until it
 * knows, or if the account can't be loaded, it shows the page: launching is
 * refused by the API anyway, so nothing depends on this check.
 */
export function TermsGate({ children }: { children: ReactNode }) {
  const api = useApi();
  const load = useCallback(() => attempt(() => api.GET("/v1/account")), [api]);
  const { state, replace } = useLoad(load);
  const [failure, setFailure] = useState<Failure>();
  const [busy, setBusy] = useState(false);

  // Older answers may lack terms; treat that as nothing to accept.
  const terms = state.status === "ok" ? (state.data.terms as TermsStatus | undefined) : undefined;
  if (!terms?.acceptance_needed) return children;

  async function onAccept() {
    if (!terms) return;
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/account/terms", { body: { version: terms.current_version } }));
    setBusy(false);
    if (!res.ok) setFailure(res.failure);
    else if (res.data) replace(res.data);
  }

  const effective = formatPolicyDate(terms.current_effective);
  return (
    <section className="card terms-gate" aria-labelledby="terms-gate-title">
      <h1 id="terms-gate-title">Our terms have changed</h1>
      <p>
        {terms.accepted_version
          ? `New versions of mailgoose's terms of service, anti-spam policy, privacy policy and data processing agreement took effect on ${effective}.`
          : `mailgoose's terms of service, anti-spam policy, privacy policy and data processing agreement need your acceptance.`}{" "}
        Accept them to keep using mailgoose. Campaigns can't launch until you do.
      </p>
      <p>
        <Link to={policyChangesPath} target="_blank" rel="noopener">
          See what changed
        </Link>{" "}
        ·{" "}
        <Link to="/terms" target="_blank" rel="noopener">
          Read the terms of service
        </Link>
      </p>
      <FailureNotice failure={failure} />
      <div>
        <button type="button" className="button" onClick={onAccept} disabled={busy}>
          {busy ? "Accepting…" : `Accept version ${terms.current_version}`}
        </button>
      </div>
      {terms.current_version !== currentPolicyVersion.version && (
        <p className="muted small">Reload the page to see the latest terms.</p>
      )}
    </section>
  );
}
