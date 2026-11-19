import { useCallback, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { Badge, FailureNotice, LoadFailed, Loading, PageHeader, useLoad } from "../components";

type Page = components["schemas"]["FormList"];

/** The account's signup forms with their counts, and a form to start a new one. */
export function Forms() {
  const api = useApi();
  const navigate = useNavigate();
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const load = useCallback(() => attempt(() => api.GET("/v1/forms", { params: { query: { limit: 50, cursor } } })), [api, cursor]);
  const { state, reload } = useLoad<Page>(load);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/forms", { body: { name: name.trim(), double_opt_in: true } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    if (res.data) navigate(`/forms/${res.data.id}`);
  }

  return (
    <section className="page">
      <PageHeader
        title="Signup forms"
        about="Put a form on your site to collect subscribers. People confirm by email first, unless you turn that off."
      />
      <form onSubmit={create} className="panel row-form">
        <label className="field grow">
          <span>New form</span>
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Newsletter signup" maxLength={200} required autoComplete="off" />
        </label>
        <button type="submit" className="button primary" disabled={busy}>
          {busy ? "Creating…" : "Create form"}
        </button>
      </form>
      <FailureNotice failure={failure} />

      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.data.length === 0 && !cursor ? (
          <p className="empty">No forms yet. Name one above, then copy its code onto your site.</p>
        ) : (
          <>
            <table className="table">
              <thead>
                <tr>
                  <th>Form</th>
                  <th>Signups</th>
                  <th>Confirmed</th>
                  <th>Confirmation</th>
                </tr>
              </thead>
              <tbody>
                {state.data.data.map((f) => (
                  <tr key={f.id}>
                    <td>
                      <Link to={`/forms/${f.id}`}>{f.name}</Link>
                    </td>
                    <td>{f.signups.toLocaleString()}</td>
                    <td>{f.double_opt_in ? f.confirmations.toLocaleString() : "Not asked"}</td>
                    <td>{f.double_opt_in ? <Badge tone="good">By email</Badge> : <Badge tone="neutral">Off</Badge>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <nav className="row pager" aria-label="Pages">
              <button type="button" className="button small" disabled={cursors.length === 0} onClick={() => setCursors((c) => c.slice(0, -1))}>
                Previous
              </button>
              <button
                type="button"
                className="button small"
                disabled={!state.data.next_cursor}
                onClick={() => state.data.next_cursor && setCursors((c) => [...c, state.data.next_cursor as string])}
              >
                Next
              </button>
            </nav>
          </>
        ))}
    </section>
  );
}
