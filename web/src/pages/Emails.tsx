import { useCallback, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";

type EmailSummary = components["schemas"]["EmailSummary"];
type Page = { data: EmailSummary[]; next_cursor: string | null };

/** The account's emails, newest first, and a form to start a new one. */
export function Emails() {
  const api = useApi();
  const navigate = useNavigate();
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const load = useCallback(
    () => attempt(() => api.GET("/v1/emails", { params: { query: { limit: 50, cursor } } })),
    [api, cursor],
  );
  const { state, reload } = useLoad<Page>(load);
  const [name, setName] = useState("");
  const [kind, setKind] = useState<"blocks" | "html">("blocks");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/emails", { body: { name: name.trim(), kind } }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    if (res.data) navigate(`/emails/${res.data.id}`);
  }

  return (
    <section className="page">
      <PageHeader title="Emails" about="Design an email with blocks, or bring your own HTML, then send it in a campaign." />
      <form onSubmit={create} className="panel row-form">
        <label className="field grow">
          <span>New email</span>
          <input
            name="name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Spring menu"
            autoComplete="off"
            maxLength={200}
            required
          />
        </label>
        <fieldset className="field kind-choice">
          <legend>Build it with</legend>
          <div className="row">
            <label className="choice">
              <input type="radio" name="kind" checked={kind === "blocks"} onChange={() => setKind("blocks")} />
              Blocks
            </label>
            <label className="choice">
              <input type="radio" name="kind" checked={kind === "html"} onChange={() => setKind("html")} />
              My own HTML
            </label>
          </div>
        </fieldset>
        <button type="submit" className="button primary" disabled={busy}>
          {busy ? "Creating…" : kind === "html" ? "Create and edit" : "Create and design"}
        </button>
      </form>
      <FailureNotice failure={failure} />

      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.data.length === 0 && !cursor ? (
          <p className="empty">No emails yet. Name one above to start designing it.</p>
        ) : (
          <>
            <table className="table">
              <thead>
                <tr>
                  <th>Email</th>
                  <th>Subject</th>
                  <th>Kind</th>
                  <th>Changed</th>
                </tr>
              </thead>
              <tbody>
                {state.data.data.map((e) => (
                  <tr key={e.id}>
                    <td>
                      <Link to={`/emails/${e.id}`}>{e.name}</Link>
                    </td>
                    <td className="muted">{e.subject || "No subject yet"}</td>
                    <td>
                      {e.kind === "html" ? <Badge tone="neutral">HTML</Badge> : <Badge tone="info">{e.block_count} blocks</Badge>}
                    </td>
                    <td className="muted">{formatDate(e.updated_at)}</td>
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
