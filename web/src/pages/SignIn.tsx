import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import { AuthCard, FailureNotice } from "../components";
import { CodeStep } from "./CodeStep";

export function SignIn() {
  const api = useApi();
  const [params] = useSearchParams();
  const [email, setEmail] = useState("");
  const [sentTo, setSentTo] = useState<string>();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function requestCode(address: string): Promise<Failure | undefined> {
    const res = await attempt(() => api.POST("/v1/sign-in", { body: { email: address } }));
    return res.ok ? undefined : res.failure;
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const address = email.trim();
    const f = await requestCode(address);
    setBusy(false);
    if (f) setFailure(f);
    else setSentTo(address);
  }

  const next = params.get("next");
  const signupLink = next ? `/signup?next=${encodeURIComponent(next)}` : "/signup";

  return (
    <AuthCard
      title="Sign in"
      footer={
        <>
          New to mailgoose? <Link to={signupLink}>Create an account</Link>
        </>
      }
    >
      {sentTo ? (
        <CodeStep email={sentTo} resend={() => requestCode(sentTo)} onBack={() => setSentTo(undefined)} />
      ) : (
        <form onSubmit={submit} className="stack">
          <p>We'll email you a code. No password needed.</p>
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
          <FailureNotice failure={failure} />
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? "Sending…" : "Email me a code"}
          </button>
        </form>
      )}
    </AuthCard>
  );
}
