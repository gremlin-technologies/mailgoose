import { useCallback, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { AuthCard, Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { useSession } from "../session";

type SenderAddress = components["schemas"]["SenderAddress"];
type Confirmed = components["schemas"]["ConfirmedSenderAddress"];

/** Most sender addresses an account can have, pending ones included (MG-85). */
export const maxSenderAddresses = 10;

/**
 * The account's sender addresses (MG-85): addresses it has proved it owns,
 * so it can send from them without a domain of its own. Add, resend the
 * confirm link, remove.
 */
export function SenderAddresses() {
  const api = useApi();
  const { state: session } = useSession();
  const load = useCallback(() => attempt(() => api.GET("/v1/sender-addresses")), [api]);
  const { state, reload } = useLoad(load);
  const loadAccount = useCallback(() => attempt(() => api.GET("/v1/account")), [api]);
  const { state: account } = useLoad(loadAccount);
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [note, setNote] = useState("");

  const owner = session.status === "signed-in" ? session.session.user.email : undefined;
  const shared = account.status === "ok" ? (account.data.shared_address ?? null) : undefined;
  const count = state.status === "ok" ? state.data.data.length : 0;
  const full = count >= maxSenderAddresses;

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    setNote("");
    const res = await attempt(() => api.POST("/v1/sender-addresses", { body: { email: email.trim() } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    setEmail("");
    setNote(`A confirm link went to ${res.data?.email ?? "the address"}. It counts once someone opens it.`);
    reload();
  }

  return (
    <section className="page">
      <PageHeader
        title="Sender addresses"
        about={`Up to ${maxSenderAddresses} addresses you've proved you own, each by an emailed link, that you can send from without a domain of your own.`}
      />
      {shared === null && (
        <p className="muted">
          Sending without a domain of your own isn't on here, so every From needs a{" "}
          <Link to="/domains">verified domain</Link>.
        </p>
      )}
      {shared && (
        <div className="panel stack">
          <p>
            Without a domain of your own, your emails go from <strong>{shared}</strong>, mailgoose's shared domain,
            with replies to your From address. That has to be your own address
            {owner ? `, ${owner}, which needs no slot,` : ", which needs no slot,"} or one confirmed here.
          </p>
          <p>
            Your own domain gets more email into inboxes and shows readers it's really you.{" "}
            <Link to="/domains">Add your domain</Link>
          </p>
        </div>
      )}
      <form onSubmit={add} className="panel row-form">
        <label className="field grow">
          <span>Add a sender address</span>
          <input
            name="email"
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="hello@example.com"
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
      {full && <p className="muted small">That's {maxSenderAddresses}, the most an account can have. Remove one to add another.</p>}
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
          <p className="empty">No sender addresses yet.</p>
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
                <SenderAddressRow key={a.id} address={a} changed={reload} />
              ))}
            </tbody>
          </table>
        ))}
    </section>
  );
}

function statusLine(a: SenderAddress): string {
  if (a.status === "confirmed") return `Confirmed ${formatDate(a.confirmed_at)}`;
  if (a.confirm_expires_at && new Date(a.confirm_expires_at) <= new Date()) return "The link expired. Resend it.";
  if (!a.confirm_sent_at) return "The link wasn't sent. Resend it.";
  return `Link sent ${formatDate(a.confirm_sent_at)}`;
}

function SenderAddressRow({ address, changed }: { address: SenderAddress; changed: () => void }) {
  const api = useApi();
  const [confirming, setConfirming] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [note, setNote] = useState("");
  const path = { sender_address_id: address.id };

  async function resend() {
    setFailure(undefined);
    setNote("");
    const res = await attempt(() => api.POST("/v1/sender-addresses/{sender_address_id}/resend", { params: { path } }));
    if (!res.ok) return setFailure(res.failure);
    setNote("Sent a new link.");
    changed();
  }

  async function remove() {
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/sender-addresses/{sender_address_id}", { params: { path } }));
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
            <span className="small">Remove {address.email}? You can't send from it until it's confirmed again.</span>
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
 * Where a sender address's emailed link goes (/sender-addresses/confirm?token=…).
 * It confirms on a press, not on opening, so a mail scanner that opens links
 * doesn't confirm for the reader. Needs no sign-in.
 */
export function ConfirmSenderAddress() {
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
    const res = await attempt(() => api.POST("/v1/sender-addresses/confirm", { body: { token } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    setDone(res.data);
  }

  if (done) {
    return (
      <AuthCard title="Sender address confirmed">
        <p role="status">
          {done.email} is confirmed as a sender address of {done.account_name}. Their emails can name it as the From
          now.
        </p>
      </AuthCard>
    );
  }
  return (
    <AuthCard title="Confirm a sender address">
      {token ? (
        <form onSubmit={confirm} className="stack">
          <p>
            Someone wants to send emails on mailgoose from this address, with replies coming to you. Confirm only if
            that's you or someone you work with. If you don't know who, close this page: nothing will be sent from it.
          </p>
          <FailureNotice failure={failure} />
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? "Confirming…" : "Confirm this sender address"}
          </button>
        </form>
      ) : (
        <p>This link is missing its token. Open the link straight from the email.</p>
      )}
    </AuthCard>
  );
}
