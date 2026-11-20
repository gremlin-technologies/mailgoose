import { useCallback, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { AuthCard, Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { useSession } from "../session";

type TestAddress = components["schemas"]["TestAddress"];
type Confirmed = components["schemas"]["ConfirmedTestAddress"];

/** Most test addresses an account can have, pending ones included (MG-80). */
export const maxTestAddresses = 5;

/**
 * Shown across the app while the account is in test mode (MG-80): it sends
 * only to its confirmed test addresses and the owner's own address.
 */
export function TestModeBanner() {
  const { state } = useSession();
  if (state.status !== "signed-in" || state.session.account.sending_mode !== "test") return null;
  return (
    <div className="notice notice-warn test-mode-banner" role="status">
      <p>
        <strong>Test mode.</strong> This account sends campaigns and tests only to its confirmed test addresses and
        to you, and its forms take signups only from them. <Link to="/test-addresses">Manage test addresses</Link>
      </p>
    </div>
  );
}

/** The account's test addresses: add, resend the confirm link, remove. */
export function TestAddresses() {
  const api = useApi();
  const { state: session } = useSession();
  const load = useCallback(() => attempt(() => api.GET("/v1/test-addresses")), [api]);
  const { state, reload } = useLoad(load);
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [note, setNote] = useState("");

  const testMode = session.status === "signed-in" && session.session.account.sending_mode === "test";
  const owner = session.status === "signed-in" ? session.session.user.email : undefined;
  const count = state.status === "ok" ? state.data.data.length : 0;
  const full = count >= maxTestAddresses;

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    setNote("");
    const res = await attempt(() => api.POST("/v1/test-addresses", { body: { email: email.trim() } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    setEmail("");
    setNote(`A confirm link went to ${res.data?.email ?? "the address"}. It counts once someone opens it.`);
    reload();
  }

  return (
    <section className="page">
      <PageHeader
        title="Test addresses"
        about={`Up to ${maxTestAddresses} addresses, each confirmed by an emailed link, that an account in test mode may send to.`}
      />
      <p className={testMode ? "" : "muted"}>
        {testMode
          ? "This account is in test mode: campaigns, tests and form signups reach only confirmed test addresses"
          : "This account has full sending, so these addresses don't limit who it sends to. In test mode, it would send only to confirmed test addresses"}
        {owner ? ` and your own address, ${owner}, which needs no slot.` : " and your own address, which needs no slot."}
        {testMode && " Ask mailgoose if you need full sending."}
      </p>
      <form onSubmit={add} className="panel row-form">
        <label className="field grow">
          <span>Add a test address</span>
          <input
            name="email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="qa@example.com"
            autoComplete="off"
            maxLength={254}
            required
            disabled={full}
          />
        </label>
        <button type="submit" className="button primary" disabled={busy || full}>
          {busy ? "Adding…" : "Add and send link"}
        </button>
      </form>
      {full && <p className="muted small">That's {maxTestAddresses}, the most an account can have. Remove one to add another.</p>}
      <FailureNotice failure={failure} />
      {note && (
        <p className="muted" role="status">
          {note}
        </p>
      )}

      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.data.length === 0 ? (
          <p className="empty">No test addresses yet.</p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Address</th>
                <th>Status</th>
                <th className="actions">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {state.data.data.map((a) => (
                <TestAddressRow key={a.id} address={a} changed={reload} />
              ))}
            </tbody>
          </table>
        ))}
    </section>
  );
}

function statusLine(a: TestAddress): string {
  if (a.status === "confirmed") return `Confirmed ${formatDate(a.confirmed_at)}`;
  if (a.confirm_expires_at && new Date(a.confirm_expires_at) <= new Date()) return "The link expired. Resend it.";
  if (!a.confirm_sent_at) return "The link wasn't sent. Resend it.";
  return `Link sent ${formatDate(a.confirm_sent_at)}`;
}

function TestAddressRow({ address, changed }: { address: TestAddress; changed: () => void }) {
  const api = useApi();
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [note, setNote] = useState("");
  const path = { test_address_id: address.id };

  async function resend() {
    setFailure(undefined);
    setNote("");
    const res = await attempt(() => api.POST("/v1/test-addresses/{test_address_id}/resend", { params: { path } }));
    if (!res.ok) return setFailure(res.failure);
    setNote("Sent a new link.");
    changed();
  }

  async function remove() {
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/test-addresses/{test_address_id}", { params: { path } }));
    if (!res.ok) return setFailure(res.failure);
    changed();
  }

  return (
    <tr>
      <td>
        {address.email}
        <FailureNotice failure={failure} />
      </td>
      <td>
        <Badge tone={address.status === "confirmed" ? "good" : "warn"}>{address.status === "confirmed" ? "Confirmed" : "Pending"}</Badge>{" "}
        <span className="muted small">{statusLine(address)}</span>
        {note && (
          <span className="muted small" role="status">
            {" "}
            {note}
          </span>
        )}
      </td>
      <td className="actions">
        {confirming ? (
          <div className="row end">
            <span className="small">Remove {address.email}? Test mode stops sending to it.</span>
            <button type="button" className="button small danger" onClick={remove}>
              Remove
            </button>
            <button type="button" className="button small" onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </div>
        ) : (
          <div className="row end">
            {address.status === "pending" && (
              <button type="button" className="button small" onClick={resend} aria-label={`Resend the link to ${address.email}`}>
                Resend link
              </button>
            )}
            <button type="button" className="button small" onClick={() => setConfirming(true)} aria-label={`Remove ${address.email}`}>
              Remove
            </button>
          </div>
        )}
      </td>
    </tr>
  );
}

/**
 * Where a test address's emailed link goes (/test-addresses/confirm?token=…).
 * It confirms on a press, not on opening, so a mail scanner that opens links
 * doesn't confirm for the reader. Needs no sign-in.
 */
export function ConfirmTestAddress() {
  const api = useApi();
  const [params] = useSearchParams();
  const token = params.get("token")?.trim() ?? "";
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [done, setDone] = useState<Confirmed>();

  async function confirm(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/test-addresses/confirm", { body: { token } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    setDone(res.data);
  }

  if (done) {
    return (
      <AuthCard title="Test address confirmed">
        <p role="status">
          {done.email} is confirmed as a test address of {done.account_name}. It can get their test emails now.
        </p>
      </AuthCard>
    );
  }
  return (
    <AuthCard title="Confirm a test address">
      {token ? (
        <form onSubmit={confirm} className="stack">
          <p>
            Someone added this email address as a test address on mailgoose, so it can get their emails while their
            account is in test mode. If you don't know who, close this page: nothing will be sent to you.
          </p>
          <FailureNotice failure={failure} />
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? "Confirming…" : "Confirm this test address"}
          </button>
        </form>
      ) : (
        <p>This link is missing its token. Open the link straight from the email.</p>
      )}
    </AuthCard>
  );
}
