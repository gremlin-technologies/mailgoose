import { useCallback, useState, type FormEvent } from "react";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { FailureNotice, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { useSession } from "../session";

type Account = components["schemas"]["AccountSettings"];
type Update = components["schemas"]["UpdateAccountRequest"];

/** Account settings, and deleting the account. */
export function Settings() {
  const api = useApi();
  const load = useCallback(() => attempt(() => api.GET("/v1/account")), [api]);
  const { state, reload, replace } = useLoad(load);
  const [justSaved, setJustSaved] = useState(false);

  return (
    <section className="page">
      <PageHeader title="Settings" about="Your account name, the defaults new campaigns start with, and your postal address." />
      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" && (
        <>
          {/* The key resets the form to what was saved. */}
          <SettingsForm
            key={JSON.stringify(state.data)}
            account={state.data}
            justSaved={justSaved}
            onSaved={(a) => {
              replace(a);
              setJustSaved(true);
            }}
          />
          <DeleteAccount name={state.data.name} />
        </>
      )}
    </section>
  );
}

const zones = (() => {
  try {
    return Intl.supportedValuesOf("timeZone");
  } catch {
    return [];
  }
})();

const fieldLabels: Record<string, string> = {
  postal_address: "a postal address",
  from_email: "a From address",
  from_name: "a From name",
};

function SettingsForm({
  account,
  justSaved,
  onSaved,
}: {
  account: Account;
  justSaved: boolean;
  onSaved: (a: Account) => void;
}) {
  const api = useApi();
  const { refresh } = useSession();
  const [name, setName] = useState(account.name);
  const [fromName, setFromName] = useState(account.from_name ?? "");
  const [fromEmail, setFromEmail] = useState(account.from_email ?? "");
  const [postal, setPostal] = useState(account.postal_address ?? "");
  const [timeZone, setTimeZone] = useState(account.time_zone);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [note, setNote] = useState(justSaved ? "Saved." : "");

  /** Only the fields that changed; an emptied optional field becomes null. */
  function changes(): Update {
    const body: Update = {};
    const optional = (v: string, was: string | null): string | null | undefined => {
      const now = v.trim() === "" ? null : v.trim();
      return now === was ? undefined : now;
    };
    if (name.trim() !== account.name) body.name = name.trim();
    const fn = optional(fromName, account.from_name);
    if (fn !== undefined) body.from_name = fn;
    const fe = optional(fromEmail, account.from_email);
    if (fe !== undefined) body.from_email = fe;
    const pa = optional(postal, account.postal_address);
    if (pa !== undefined) body.postal_address = pa;
    if (timeZone.trim() !== account.time_zone) body.time_zone = timeZone.trim();
    return body;
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setFailure(undefined);
    setNote("");
    const body = changes();
    if (Object.keys(body).length === 0) {
      setNote("Nothing changed.");
      return;
    }
    setBusy(true);
    const res = await attempt(() => api.PATCH("/v1/account", { body }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    if (res.data) {
      onSaved(res.data);
      // The sidebar shows the account name from the session.
      if (body.name !== undefined) void refresh();
    }
  }

  const missing = account.missing_for_send.map((f) => fieldLabels[f] ?? f);
  const limit = account.send_limit;

  return (
    <form onSubmit={save} className="panel stack">
      {missing.length > 0 && (
        <div className="notice notice-warn">
          <p>Campaigns can't launch until you add {missing.join(" and ")}. Every marketing email needs one by law.</p>
        </div>
      )}
      {limit.lowered_until ? (
        <div className="notice notice-warn" role="status">
          <p>
            <strong>Your daily send limit is lowered to {limit.daily.toLocaleString("en-US")}.</strong> {limit.reason}
          </p>
        </div>
      ) : (
        <p className="muted small">
          Daily send limit: {limit.daily.toLocaleString("en-US")} recipients.{limit.reason ? ` ${limit.reason}` : ""}
        </p>
      )}
      <label className="field">
        <span>Account name</span>
        <input name="name" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} required />
      </label>
      <div className="grid-2">
        <label className="field">
          <span>Default From name</span>
          <input
            name="from_name"
            value={fromName}
            onChange={(e) => setFromName(e.target.value)}
            maxLength={100}
            placeholder="Pat's Bakery"
          />
        </label>
        <label className="field">
          <span>Default From address</span>
          <input
            name="from_email"
            type="email"
            value={fromEmail}
            onChange={(e) => setFromEmail(e.target.value)}
            maxLength={254}
            placeholder="news@example.com"
          />
        </label>
      </div>
      <div className="field">
        <label htmlFor="postal_address">Postal address</label>
        <textarea
          id="postal_address"
          name="postal_address"
          value={postal}
          onChange={(e) => setPostal(e.target.value)}
          maxLength={500}
          rows={4}
          aria-describedby="postal-help"
        />
        <span id="postal-help" className="muted small">
          Goes in the footer of every email you send.
        </span>
      </div>
      <label className="field">
        <span>Time zone</span>
        <input
          name="time_zone"
          value={timeZone}
          onChange={(e) => setTimeZone(e.target.value)}
          list="time-zones"
          autoComplete="off"
          spellCheck={false}
          required
        />
        <datalist id="time-zones">
          {zones.map((z) => (
            <option key={z} value={z} />
          ))}
        </datalist>
      </label>
      <FailureNotice failure={failure} />
      <div className="row">
        <button type="submit" className="button primary" disabled={busy}>
          {busy ? "Saving…" : "Save settings"}
        </button>
        <span className="muted" role="status">
          {note}
        </span>
      </div>
    </form>
  );
}

function DeleteAccount({ name }: { name: string }) {
  const api = useApi();
  const { ended } = useSession();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function remove(e: FormEvent) {
    e.preventDefault();
    if (typed !== name) return;
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/account", { params: { query: { confirm: typed } } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    // The API ended every session; go to the sign-in page.
    ended();
  }

  return (
    <form onSubmit={remove} className="panel danger-zone stack">
      <h2>Delete account</h2>
      <p>
        This deletes your emails, contacts, tags, domains and API keys at once, and signs you out everywhere. It
        can't be undone.
      </p>
      <label className="field">
        <span>
          Type <strong>{name}</strong> to confirm
        </span>
        <input name="confirm" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" />
      </label>
      <FailureNotice failure={failure} />
      <div>
        <button type="submit" className="button danger" disabled={busy || typed !== name}>
          {busy ? "Deleting…" : "Delete account"}
        </button>
      </div>
    </form>
  );
}
