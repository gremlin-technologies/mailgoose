import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import { AuthCard, FailureNotice } from "../components";
import { CodeStep } from "./CodeStep";

export function SignUp() {
  const api = useApi();
  const [params] = useSearchParams();
  const [email, setEmail] = useState("");
  const [accountName, setAccountName] = useState("");
  const [accepted, setAccepted] = useState(false);
  const [sentTo, setSentTo] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function requestCode(): Promise<Failure | undefined> {
    const res = await attempt(() =>
      api.POST("/v1/signup", {
        body: { email: email.trim(), account_name: accountName.trim(), accepted_terms: accepted },
      }),
    );
    return res.ok ? undefined : res.failure;
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const f = await requestCode();
    setBusy(false);
    if (f) setFailure(f);
    else setSentTo(email.trim());
  }

  const next = params.get("next");
  const signInLink = next ? `/sign-in?next=${encodeURIComponent(next)}` : "/sign-in";

  return (
    <AuthCard
      title="Create your account"
      footer={
        <>
          Already have an account? <Link to={signInLink}>Sign in</Link>
        </>
      }
    >
      {sentTo ? (
        <CodeStep email={sentTo} resend={requestCode} onBack={() => setSentTo(undefined)} />
      ) : (
        <form onSubmit={submit} className="stack">
          <p>Free for up to 1,000 contacts and 2,500 emails a month.</p>
          <label className="field">
            <span>Email</span>
            <input
              type="email"
              name="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoComplete="email"
              maxLength={254}
              required
              autoFocus
            />
          </label>
          <label className="field">
            <span>Account name</span>
            <input
              name="account_name"
              value={accountName}
              onChange={(e) => setAccountName(e.target.value)}
              autoComplete="organization"
              placeholder="Your business or newsletter"
              maxLength={100}
              required
            />
          </label>
          <label className="check">
            <input
              type="checkbox"
              name="accepted_terms"
              checked={accepted}
              onChange={(e) => setAccepted(e.target.checked)}
              required
            />
            <span>
              I accept the{" "}
              <a href="/terms" target="_blank" rel="noopener">
                terms of service
              </a>{" "}
              and the{" "}
              <a href="/anti-spam" target="_blank" rel="noopener">
                anti-spam policy
              </a>
              , and I'll only email people who asked to hear from me.
            </span>
          </label>
          <p className="muted small">
            How we handle personal data:{" "}
            <a href="/privacy" target="_blank" rel="noopener">
              privacy policy
            </a>{" "}
            and{" "}
            <a href="/dpa" target="_blank" rel="noopener">
              data processing agreement
            </a>
            .
          </p>
          <FailureNotice failure={failure} />
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? "Sending…" : "Email me a code"}
          </button>
        </form>
      )}
    </AuthCard>
  );
}
