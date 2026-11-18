import { useEffect, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { AuthCard, FailureNotice, Loading } from "../components";
import { useSession } from "../session";

type DeviceLogin = components["schemas"]["DeviceLogin"];

type State =
  | { status: "loading" }
  | { status: "ready"; login: DeviceLogin }
  | { status: "gone" }
  | { status: "approved" }
  | { status: "denied" }
  | { status: "failed"; failure: Failure };

/**
 * Where `mailgoose login` sends people (/device?code=BCDF-GHJK): they check
 * the code matches their terminal, then approve or deny. Only reached signed
 * in (RequireSession).
 */
export function Device() {
  const [params, setParams] = useSearchParams();
  const code = params.get("code")?.trim();
  return (
    <AuthCard title="Sign in to the mailgoose CLI">
      {code ? <Approval key={code} code={code} /> : <EnterCode onCode={(c) => setParams({ code: c })} />}
    </AuthCard>
  );
}

function EnterCode({ onCode }: { onCode: (code: string) => void }) {
  const [code, setCode] = useState("");
  function submit(e: FormEvent) {
    e.preventDefault();
    onCode(code.trim());
  }
  return (
    <form onSubmit={submit} className="stack">
      <p>
        Enter the code that <code>mailgoose login</code> shows in your terminal.
      </p>
      <label className="field">
        <span>Code</span>
        <input
          name="code"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          placeholder="XXXX-XXXX"
          autoComplete="off"
          autoCapitalize="characters"
          maxLength={20}
          required
          autoFocus
        />
      </label>
      <button type="submit" className="button primary">
        Continue
      </button>
    </form>
  );
}

function Approval({ code }: { code: string }) {
  const api = useApi();
  const { state: session } = useSession();
  const [state, setState] = useState<State>({ status: "loading" });
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let current = true;
    void attempt(() => api.GET("/v1/device-logins/{user_code}", { params: { path: { user_code: code } } })).then(
      (res) => {
        if (!current) return;
        if (res.ok && res.data) setState({ status: "ready", login: res.data });
        else if (!res.ok && res.failure.status === 404) setState({ status: "gone" });
        else if (!res.ok) setState({ status: "failed", failure: res.failure });
      },
    );
    return () => {
      current = false;
    };
  }, [api, code]);

  async function decide(approve: boolean) {
    setBusy(true);
    const opts = { params: { path: { user_code: code } } };
    const res = await attempt(() =>
      approve
        ? api.POST("/v1/device-logins/{user_code}/approve", opts)
        : api.POST("/v1/device-logins/{user_code}/deny", opts),
    );
    setBusy(false);
    if (res.ok) setState({ status: approve ? "approved" : "denied" });
    else if (res.failure.status === 404) setState({ status: "gone" });
    else setState({ status: "failed", failure: res.failure });
  }

  switch (state.status) {
    case "loading":
      return <Loading />;
    case "gone":
      return (
        <div className="stack">
          <p>That code has expired or was already used.</p>
          <p>
            Run <code>mailgoose login</code> again to get a new one.
          </p>
        </div>
      );
    case "approved":
      return (
        <div className="stack" role="status">
          <p>
            <strong>The CLI is signed in.</strong> Go back to your terminal; you can close this tab.
          </p>
        </div>
      );
    case "denied":
      return (
        <div className="stack" role="status">
          <p>
            <strong>Denied.</strong> Nothing was signed in.
          </p>
        </div>
      );
    case "failed":
      return <FailureNotice failure={state.failure} />;
    case "ready": {
      const { login } = state;
      const who = session.status === "signed-in" ? session.session : undefined;
      return (
        <div className="stack">
          <p>Check that this code matches the one in your terminal:</p>
          <p className="user-code" aria-label="Code">
            {login.user_code}
          </p>
          <p className="muted">
            Started{" "}
            <time dateTime={login.created_at} title={new Date(login.created_at).toLocaleString()}>
              {startedAgo(login.created_at)}
            </time>
            {login.client_name && (
              <>
                {" "}
                on <strong>{login.client_name}</strong>
              </>
            )}
            . If you didn't just run <code>mailgoose login</code>, deny it.
          </p>
          <p>
            Approving lets that terminal, and any AI agent running in it, use
            {who ? (
              <>
                {" "}
                <strong>{who.account.name}</strong> as <strong>{who.user.email}</strong>
              </>
            ) : (
              " your account"
            )}{" "}
            with full access. Only approve a login you started yourself.
          </p>
          <div className="row">
            <button type="button" className="button primary" disabled={busy} onClick={() => decide(true)}>
              Approve
            </button>
            <button type="button" className="button" disabled={busy} onClick={() => decide(false)}>
              Deny
            </button>
          </div>
        </div>
      );
    }
  }
}

/** "just now", "3 minutes ago": when the login started, for the person to check. */
export function startedAgo(iso: string, now: Date = new Date()): string {
  const seconds = Math.round((now.getTime() - new Date(iso).getTime()) / 1000);
  if (!Number.isFinite(seconds) || seconds < 60) return "just now";
  const minutes = Math.round(seconds / 60);
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "always" });
  if (minutes < 60) return rtf.format(-minutes, "minute");
  return rtf.format(-Math.round(minutes / 60), "hour");
}
