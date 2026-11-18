import { useState, type FormEvent } from "react";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import { FailureNotice } from "../components";
import { useSession } from "../session";

/**
 * The second step of signing up or in: the six-digit code from the email.
 * A good code starts the session; the page then moves on by itself.
 */
export function CodeStep({
  email,
  resend,
  onBack,
}: {
  email: string;
  /** Asks for a new code. */
  resend: () => Promise<Failure | undefined>;
  onBack: () => void;
}) {
  const api = useApi();
  const { signedIn } = useSession();
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [resent, setResent] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() =>
      api.POST("/v1/sessions", { body: { email, code: code.replace(/\D/g, "") } }),
    );
    setBusy(false);
    if (res.ok && res.data) {
      signedIn(res.data);
    } else if (!res.ok) {
      setFailure(res.failure);
    }
  }

  async function sendAgain() {
    setFailure(undefined);
    setResent(false);
    const f = await resend();
    if (f) setFailure(f);
    else setResent(true);
  }

  return (
    <form onSubmit={submit} className="stack">
      <p>
        We sent a six-digit code to <strong>{email}</strong>. It works for 10 minutes.
      </p>
      <label className="field">
        <span>Code</span>
        <input
          name="code"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          inputMode="numeric"
          autoComplete="one-time-code"
          pattern="[0-9 ]{6,7}"
          maxLength={7}
          required
          autoFocus
        />
      </label>
      <FailureNotice failure={failure} />
      {resent && (
        <p className="notice" role="status">
          If that address can get email, a new code is on its way.
        </p>
      )}
      <button type="submit" className="button primary" disabled={busy}>
        {busy ? "Checking…" : "Continue"}
      </button>
      <p className="row">
        <button type="button" className="link" onClick={sendAgain}>
          Send a new code
        </button>
        <button type="button" className="link" onClick={onBack}>
          Use a different email
        </button>
      </p>
    </form>
  );
}
