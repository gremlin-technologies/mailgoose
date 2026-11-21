// The steps before a launch: check the email, send a test, and launch from
// a summary the person confirms (CAM-2 to CAM-4, DES-12).
import { useState, type FormEvent } from "react";
import { Link, useLocation } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { FailureNotice, formatDate } from "../components";
import type { EditorFrom } from "../designer/exit";
import { plural, type Campaign } from "./campaign";

type Check = components["schemas"]["EmailCheck"];
type CheckIssue = { code: string; message: string; path?: string; line?: number; url?: string };
type Problem = { code: string; message: string; hint: string };
type Address = { name: string; email: string };

export type Summary = {
  subject: string;
  from: Address;
  reply_to: Address[];
  recipient_count: number;
  send_at: string | null;
  credits: number;
  balance: number;
  warnings: CheckIssue[];
};

export type Confirmation = { summary: Summary; code: string; expires_at: string };

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null;
const isAddress = (v: unknown): v is Address => isObject(v) && typeof v.email === "string" && typeof v.name === "string";
const isIssue = (v: unknown): v is CheckIssue => isObject(v) && typeof v.code === "string" && typeof v.message === "string";

/** The problems a not_ready answer lists, skipping any it can't read. */
export function problemsOf(failure: Failure | undefined): Problem[] {
  const list = failure?.details?.problems;
  if (!Array.isArray(list)) return [];
  return list
    .filter((p): p is Record<string, unknown> => isObject(p) && typeof p.message === "string")
    .map((p) => ({ code: String(p.code ?? ""), message: p.message as string, hint: typeof p.hint === "string" ? p.hint : "" }));
}

/** The launch summary and code in a confirmation_required answer; undefined if it isn't one. */
export function confirmationOf(failure: Failure): Confirmation | undefined {
  const d = failure.details;
  if (failure.code !== "confirmation_required" || !d || !isObject(d.summary)) return undefined;
  const s = d.summary;
  if (typeof d.code !== "string" || typeof d.expires_at !== "string") return undefined;
  if (typeof s.subject !== "string" || !isAddress(s.from) || typeof s.recipient_count !== "number") return undefined;
  if (typeof s.credits !== "number" || typeof s.balance !== "number") return undefined;
  return {
    code: d.code,
    expires_at: d.expires_at,
    summary: {
      subject: s.subject,
      from: s.from,
      reply_to: Array.isArray(s.reply_to) ? s.reply_to.filter(isAddress) : [],
      recipient_count: s.recipient_count,
      send_at: typeof s.send_at === "string" ? s.send_at : null,
      credits: s.credits,
      balance: s.balance,
      warnings: Array.isArray(s.warnings) ? s.warnings.filter(isIssue) : [],
    },
  };
}

function address(a: Address): string {
  return a.name ? `${a.name} <${a.email}>` : a.email;
}

/** What stops a test or a launch, each with what to do about it. */
export function Problems({ failure }: { failure: Failure }) {
  const problems = problemsOf(failure);
  if (problems.length === 0) return <FailureNotice failure={failure} />;
  return (
    <div className="notice notice-error" role="alert">
      <p>{failure.message}</p>
      <ul className="problems-list">
        {problems.map((p, i) => (
          <li key={i}>
            {p.message}
            {p.hint && <span className="hint"> {p.hint}</span>}
          </li>
        ))}
      </ul>
    </div>
  );
}

function issueWhere(i: CheckIssue): string {
  if (i.line) return `Line ${i.line}: `;
  if (i.url) return `${i.url}: `;
  return "";
}

function IssueList({ issues, kind }: { issues: CheckIssue[]; kind: "error" | "warning" }) {
  if (issues.length === 0) return null;
  return (
    <ul className={`issues issues-${kind}`} aria-label={kind === "error" ? "Errors" : "Warnings"}>
      {issues.map((i, n) => (
        <li key={n}>
          {issueWhere(i)}
          {i.message}
        </li>
      ))}
    </ul>
  );
}

/** Runs the email check (DES-12): errors stop a launch, warnings don't. */
export function CheckStep({ emailId }: { emailId: string | null }) {
  const api = useApi();
  const [result, setResult] = useState<Check>();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  // The editor's "Save and exit" comes back here.
  const from: EditorFrom = { from: useLocation().pathname };

  async function check() {
    if (!emailId) return;
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/emails/{email_id}/check", { params: { path: { email_id: emailId } } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    setResult(res.data);
  }

  return (
    <li className="step">
      <h3>Check the email</h3>
      <p className="muted">Looks for a missing subject or unsubscribe link, broken links, images without alt text and more.</p>
      <div className="row">
        <button type="button" className="button" onClick={() => void check()} disabled={busy || !emailId}>
          {busy ? "Checking…" : "Check email"}
        </button>
        {emailId && result && !result.ok && (
          <Link to={`/emails/${emailId}`} state={from}>
            Fix it in the editor
          </Link>
        )}
      </div>
      <FailureNotice failure={failure} />
      {result && (
        <div className="check-result" role="region" aria-label="Email check">
          {result.errors.length === 0 && result.warnings.length === 0 ? (
            <p className="good">No problems found.</p>
          ) : (
            <p>
              {result.errors.length > 0
                ? `${plural(result.errors.length, "error")} to fix before you can launch.`
                : "It can be sent. These are worth a look:"}
            </p>
          )}
          <IssueList issues={result.errors} kind="error" />
          <IssueList issues={result.warnings} kind="warning" />
        </div>
      )}
    </li>
  );
}

/** Sends a test to the account owner, or up to 5 addresses on a verified domain (CAM-2). */
export function TestStep({ campaign, onSent }: { campaign: Campaign; onSent: (c: Campaign) => void }) {
  const api = useApi();
  const [to, setTo] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [sent, setSent] = useState<string>();
  const t = campaign.last_test;

  async function send(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    setSent(undefined);
    const list = to.split(/[\s,;]+/).filter(Boolean);
    const res = await attempt(() =>
      api.POST("/v1/campaigns/{campaign_id}/test", {
        params: { path: { campaign_id: campaign.id } },
        body: list.length ? { to: list } : {},
      }),
    );
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    if (!res.data) return;
    setSent(`Sent to ${res.data.to.join(", ")}. ${res.data.sent_today} of ${res.data.daily_limit} test emails used today.`);
    onSent(res.data.campaign);
  }

  return (
    <li className="step">
      <h3>Send a test</h3>
      <p className="muted">
        Goes to you, or to up to 5 addresses on your verified domain. It's free. Launching needs a test since the last
        change.
      </p>
      <form className="row-form" onSubmit={send}>
        <label className="field grow">
          <span>Send it to (optional)</span>
          <input value={to} onChange={(e) => setTo(e.target.value)} placeholder="You, if left empty" autoComplete="off" />
        </label>
        <button type="submit" className="button" disabled={busy}>
          {busy ? "Sending…" : "Send test"}
        </button>
      </form>
      {sent ? (
        <p className="notice notice-good" role="status">
          {sent}
        </p>
      ) : t ? (
        <p className="muted small">
          Last test {formatDate(t.at)}.
          {t.version !== campaign.version && " The campaign changed since, so send another before you launch."}
        </p>
      ) : (
        <p className="muted small">No test sent yet.</p>
      )}
      {failure && <Problems failure={failure} />}
    </li>
  );
}

/**
 * Launch (CAM-3, CAM-4): asks the API for the summary first, shows it, and
 * launches only when the person presses confirm, sending the summary's code
 * back, the same flow the CLI and agents use.
 */
export function LaunchStep({ campaign, onLaunched }: { campaign: Campaign; onLaunched: (c: Campaign) => void }) {
  const api = useApi();
  const [confirming, setConfirming] = useState<Confirmation>();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function launch(code?: string) {
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() =>
      api.POST("/v1/campaigns/{campaign_id}/launch", {
        params: { path: { campaign_id: campaign.id } },
        body: code ? { confirm: code } : {},
      }),
    );
    setBusy(false);
    if (res.ok) {
      setConfirming(undefined);
      if (res.data) onLaunched(res.data.campaign);
      return;
    }
    const c = confirmationOf(res.failure);
    if (c) return setConfirming(c);
    setConfirming(undefined);
    setFailure(res.failure);
  }

  return (
    <li className="step">
      <h3>Launch</h3>
      {!confirming && (
        <>
          <p className="muted">You'll see who it goes to and what it costs before anything is sent.</p>
          <div>
            <button type="button" className="button primary" onClick={() => void launch()} disabled={busy}>
              {busy ? "Getting the summary…" : "Review and launch"}
            </button>
          </div>
        </>
      )}
      {confirming && (
        <LaunchSummary
          confirmation={confirming}
          busy={busy}
          onConfirm={() => void launch(confirming.code)}
          onCancel={() => setConfirming(undefined)}
        />
      )}
      {failure && <Problems failure={failure} />}
    </li>
  );
}

function LaunchSummary({
  confirmation: { summary: s, expires_at },
  busy,
  onConfirm,
  onCancel,
}: {
  confirmation: Confirmation;
  busy: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const short = s.credits > s.balance;
  const action = s.send_at ? `Schedule for ${formatDate(s.send_at)}` : `Send to ${plural(s.recipient_count, "contact")} now`;
  return (
    <div className="panel launch-summary" role="region" aria-label="Launch summary">
      <h4>Ready to launch?</h4>
      <dl className="facts">
        <dt>Subject</dt>
        <dd>{s.subject}</dd>
        <dt>From</dt>
        <dd>{address(s.from)}</dd>
        {s.reply_to.length > 0 && (
          <>
            <dt>Replies go to</dt>
            <dd>{s.reply_to.map(address).join(", ")}</dd>
          </>
        )}
        <dt>Recipients</dt>
        <dd>{plural(s.recipient_count, "contact")}</dd>
        <dt>Sends</dt>
        <dd>{s.send_at ? formatDate(s.send_at) : "As soon as you confirm"}</dd>
        <dt>Cost</dt>
        <dd>
          {plural(s.credits, "credit")}, of the {s.balance.toLocaleString()} you have
        </dd>
      </dl>
      {s.warnings.length > 0 && (
        <>
          <p>The email check has warnings. They don't stop the launch:</p>
          <IssueList issues={s.warnings} kind="warning" />
        </>
      )}
      {short && <p className="field-error">You don't have enough credits for this. Buy more before you launch.</p>}
      <p className="muted small">This summary is good until {formatDate(expires_at)}. Changing the campaign or its email needs a new one.</p>
      <div className="row">
        <button type="button" className="button primary" onClick={onConfirm} disabled={busy}>
          {busy ? "Launching…" : action}
        </button>
        <button type="button" className="button" onClick={onCancel} disabled={busy}>
          Not yet
        </button>
      </div>
    </div>
  );
}
