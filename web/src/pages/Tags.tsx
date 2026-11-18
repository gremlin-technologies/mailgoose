import { useCallback, useState, type FormEvent } from "react";
import { Link } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure, type Result } from "../api/result";
import type { components } from "../api/schema";
import { FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";

export type Tag = components["schemas"]["Tag"];
type ApiClient = ReturnType<typeof useApi>;

/** Most pages of tags the app reads in one go: 1,000 tags. */
const maxTagPages = 10;

/** Every tag on the account, following next_cursor; for menus and the tags page. */
export async function loadAllTags(api: ApiClient): Promise<Result<Tag[]>> {
  const tags: Tag[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < maxTagPages; page++) {
    const res = await attempt(() => api.GET("/v1/tags", { params: { query: { limit: 100, cursor } } }));
    if (!res.ok) return res;
    tags.push(...(res.data?.data ?? []));
    cursor = res.data?.next_cursor ?? undefined;
    if (!cursor) break;
  }
  return { ok: true, data: tags };
}

/** The account's tags, loaded when the page opens. */
export function useTags() {
  const api = useApi();
  const load = useCallback(() => loadAllTags(api), [api]);
  return useLoad(load);
}

/** Tag management: create, rename and delete tags, and see how many contacts each has. */
export function Tags() {
  const api = useApi();
  const { state, reload } = useTags();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/tags", { body: { name: name.trim() } }));
    setBusy(false);
    if (!res.ok) {
      setFailure(res.failure);
      return;
    }
    setName("");
    reload();
  }

  return (
    <section className="page">
      <PageHeader title="Tags" about="Group contacts with tags, then send a campaign to one tag instead of your whole list." />
      <form onSubmit={create} className="panel row-form">
        <label className="field grow">
          <span>New tag</span>
          <input
            name="tag"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Newsletter"
            autoComplete="off"
            maxLength={50}
            required
          />
        </label>
        <button type="submit" className="button primary" disabled={busy}>
          {busy ? "Creating…" : "Create tag"}
        </button>
      </form>
      <FailureNotice failure={failure} />

      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.length === 0 ? (
          <p className="empty">No tags yet. Create one here, or put tags on contacts as you import them.</p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Tag</th>
                <th>Contacts</th>
                <th>Created</th>
                <th className="actions">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {state.data.map((t) => (
                <TagRow key={t.id} tag={t} changed={reload} />
              ))}
            </tbody>
          </table>
        ))}
    </section>
  );
}

function TagRow({ tag, changed }: { tag: Tag; changed: () => void }) {
  const api = useApi();
  const [mode, setMode] = useState<"view" | "rename" | "delete">("view");
  const [name, setName] = useState(tag.name);
  const [failure, setFailure] = useState<Failure>();
  const path = { tag_id: tag.id };

  async function rename(e: FormEvent) {
    e.preventDefault();
    setFailure(undefined);
    const res = await attempt(() => api.PATCH("/v1/tags/{tag_id}", { params: { path }, body: { name: name.trim() } }));
    if (!res.ok) return setFailure(res.failure);
    setMode("view");
    changed();
  }

  async function remove() {
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/tags/{tag_id}", { params: { path } }));
    if (!res.ok) return setFailure(res.failure);
    changed();
  }

  const count = tag.contact_count.toLocaleString();
  return (
    <tr>
      <td>
        {mode === "rename" ? (
          <form onSubmit={rename} className="row">
            <label className="field grow">
              <span className="visually-hidden">New name for {tag.name}</span>
              <input value={name} onChange={(e) => setName(e.target.value)} maxLength={50} required autoFocus />
            </label>
            <button type="submit" className="button small primary">
              Save
            </button>
            <button type="button" className="button small" onClick={() => setMode("view")}>
              Cancel
            </button>
          </form>
        ) : (
          <Link to={`/contacts?tag=${tag.id}`}>{tag.name}</Link>
        )}
        <FailureNotice failure={failure} />
      </td>
      <td>{count}</td>
      <td className="muted">{formatDate(tag.created_at)}</td>
      <td className="actions">
        {mode === "delete" ? (
          <div className="row end">
            <span className="small">
              Delete {tag.name}? Its {count} contacts stay, and draft or scheduled campaigns sent to it lose it.
            </span>
            <button type="button" className="button small danger" onClick={remove}>
              Delete tag
            </button>
            <button type="button" className="button small" onClick={() => setMode("view")}>
              Cancel
            </button>
          </div>
        ) : (
          mode === "view" && (
            <div className="row end">
              <button type="button" className="button small" onClick={() => setMode("rename")} aria-label={`Rename ${tag.name}`}>
                Rename
              </button>
              <button type="button" className="button small" onClick={() => setMode("delete")} aria-label={`Delete ${tag.name}`}>
                Delete
              </button>
            </div>
          )
        )}
      </td>
    </tr>
  );
}
