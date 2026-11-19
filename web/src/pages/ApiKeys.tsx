import { useCallback, useState, type FormEvent } from "react";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { Badge, CopyButton, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";

type ApiKey = components["schemas"]["ApiKey"];
type Scope = ApiKey["scope"];

const scopes: { value: Scope; label: string; about: string }[] = [
  {
    value: "draft",
    label: "Draft",
    about: "Everything except launching campaigns, billing and managing keys. A good fit for an AI agent.",
  },
  { value: "full", label: "Full", about: "Everything, including launching campaigns, billing and keys." },
];

/** API keys: create one (its secret shows once), list them, revoke one. */
export function ApiKeys() {
  const api = useApi();
  const load = useCallback(() => attempt(() => api.GET("/v1/api-keys", { params: { query: { limit: 100 } } })), [api]);
  const { state, reload } = useLoad(load);
  const [name, setName] = useState("");
  const [scope, setScope] = useState<Scope>("draft");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [created, setCreated] = useState<{ name: string; secret: string }>();
  const [revoking, setRevoking] = useState<string>();

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/api-keys", { body: { name: name.trim(), scope } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    if (res.data) setCreated({ name: res.data.key.name, secret: res.data.secret });
    setName("");
    reload();
  }

  async function revoke(key: ApiKey) {
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/api-keys/{key_id}", { params: { path: { key_id: key.id } } }));
    setRevoking(undefined);
    if (!res.ok) return setFailure(res.failure);
    reload();
  }

  return (
    <section className="page">
      <PageHeader
        title="API keys"
        about={
          <>
            Keys let the mailgoose CLI, your scripts or your AI agent use your account. Set one as{" "}
            <code>MAILGOOSE_API_KEY</code>.
          </>
        }
      />

      {created ? (
        <div className="panel secret" role="status">
          <h2>Copy your new key now</h2>
          <p>This is the only time we show the secret for {created.name}. Store it somewhere safe.</p>
          <div className="secret-row">
            <code>{created.secret}</code>
            <CopyButton value={created.secret} label="the new API key" />
          </div>
          <div>
            <button type="button" className="button" onClick={() => setCreated(undefined)}>
              I've saved it
            </button>
          </div>
        </div>
      ) : (
        <form onSubmit={create} className="panel stack">
          <h2>Create a key</h2>
          <label className="field">
            <span>Name</span>
            <input
              name="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="My agent"
              maxLength={100}
              autoComplete="off"
              required
            />
          </label>
          <fieldset className="choices">
            <legend>What it can do</legend>
            {scopes.map((s) => (
              <label key={s.value} className="choice">
                <input
                  type="radio"
                  name="scope"
                  value={s.value}
                  checked={scope === s.value}
                  onChange={() => setScope(s.value)}
                />
                <span>
                  <strong>{s.label}</strong>
                  <span className="muted small"> {s.about}</span>
                </span>
              </label>
            ))}
          </fieldset>
          <div>
            <button type="submit" className="button primary" disabled={busy}>
              {busy ? "Creating…" : "Create key"}
            </button>
          </div>
        </form>
      )}
      <FailureNotice failure={failure} />

      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.data.length === 0 ? (
          <p className="empty">No keys yet. The CLI's `mailgoose login` doesn't need one; scripts and agents do.</p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Can do</th>
                <th>Key</th>
                <th>Created</th>
                <th>Last used</th>
                <th>
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {state.data.data.map((k) => (
                <tr key={k.id}>
                  <td>{k.name}</td>
                  <td>
                    <Badge tone={k.scope === "full" ? "info" : "neutral"}>{k.scope === "full" ? "Full" : "Draft"}</Badge>
                  </td>
                  <td>
                    <code>{k.hint}</code>
                  </td>
                  <td className="muted">{formatDate(k.created_at)}</td>
                  <td className="muted">{k.last_used_at ? formatDate(k.last_used_at) : "Never"}</td>
                  <td className="actions">
                    {revoking === k.id ? (
                      <span className="row">
                        <button type="button" className="button small danger" onClick={() => revoke(k)}>
                          Revoke {k.name}
                        </button>
                        <button type="button" className="button small" onClick={() => setRevoking(undefined)}>
                          Cancel
                        </button>
                      </span>
                    ) : (
                      <button
                        type="button"
                        className="button small"
                        onClick={() => setRevoking(k.id)}
                        aria-label={`Revoke ${k.name}`}
                      >
                        Revoke
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ))}
      {revoking && <p className="muted small">Anything using a revoked key stops working at once.</p>}
    </section>
  );
}
