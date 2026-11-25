import { useCallback, useState, type FormEvent } from "react";

import { useApi } from "../api/context";
import { attempt, type Failure, type Result } from "../api/result";
import type { components } from "../api/schema";
import { FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";

export type ContactField = components["schemas"]["ContactField"];
type ApiClient = ReturnType<typeof useApi>;

/** Most pages of fields the app reads in one go: far past any plan's limit. */
const maxFieldPages = 5;

/** Every contact field on the account, in the order they were made, following next_cursor. */
export async function loadAllFields(api: ApiClient): Promise<Result<ContactField[]>> {
  const fields: ContactField[] = [];
  let cursor: string | undefined;
  for (let page = 0; page < maxFieldPages; page++) {
    const res = await attempt(() => api.GET("/v1/contact-fields", { params: { query: { limit: 100, cursor } } }));
    if (!res.ok) return res;
    fields.push(...(res.data?.data ?? []));
    cursor = res.data?.next_cursor ?? undefined;
    if (!cursor) break;
  }
  return { ok: true, data: fields };
}

/** The account's contact fields, loaded when the page opens. */
export function useContactFields() {
  const api = useApi();
  const load = useCallback(() => loadAllFields(api), [api]);
  return useLoad(load);
}

/** "4 of 20 fields used", or just the count when the plan's limit isn't known. */
export function fieldsUsed(used: number, limit: number | undefined): string {
  const word = (n: number) => (n === 1 ? "field" : "fields");
  if (limit === undefined) return `${used.toLocaleString()} ${word(used)}`;
  return `${used.toLocaleString()} of ${limit.toLocaleString()} ${word(limit)} used`;
}

/** Contact fields: add, rename and delete the account's own fields, up to its plan's limit. */
export function Fields() {
  const api = useApi();
  const { state, reload } = useContactFields();
  const loadPlan = useCallback(() => attempt(() => api.GET("/v1/plan")), [api]);
  const plan = useLoad(loadPlan);
  const limit = plan.state.status === "ok" ? plan.state.data.contact_field_limit : undefined;
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  const fields = state.status === "ok" ? state.data : [];
  const full = state.status === "ok" && limit !== undefined && fields.length >= limit;

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/contact-fields", { body: { name: name.trim() } }));
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
      <PageHeader
        title="Fields"
        about="Your own contact fields, such as company or plan. Each one gets a column on the contacts table and in exports, and is a merge tag in emails by its key, such as {{company}}."
      />
      <form onSubmit={create} className="panel stack" aria-label="Add a field">
        <div className="row-form">
          <label className="field grow">
            <span>New field</span>
            <input
              name="field"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Company"
              autoComplete="off"
              maxLength={50}
              disabled={full}
              required
            />
          </label>
          <button type="submit" className="button primary" disabled={busy || full}>
            {busy ? "Adding…" : "Add field"}
          </button>
        </div>
        {state.status === "ok" && (
          <p className="muted small" role="status">
            {fieldsUsed(fields.length, limit)}
            {full && ". Delete a field you no longer use to add another."}
          </p>
        )}
      </form>
      <FailureNotice failure={failure} />

      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (fields.length === 0 ? (
          <p className="empty">No fields yet. Add one here, and fill it in on each contact's page.</p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Field</th>
                <th>Key</th>
                <th>Created</th>
                <th className="actions">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {fields.map((f) => (
                <FieldRow key={f.id} field={f} changed={reload} />
              ))}
            </tbody>
          </table>
        ))}
    </section>
  );
}

function FieldRow({ field, changed }: { field: ContactField; changed: () => void }) {
  const api = useApi();
  const [mode, setMode] = useState<"view" | "rename" | "delete">("view");
  const [name, setName] = useState(field.name);
  const [failure, setFailure] = useState<Failure>();
  const path = { field_id: field.id };

  async function rename(e: FormEvent) {
    e.preventDefault();
    setFailure(undefined);
    const res = await attempt(() =>
      api.PATCH("/v1/contact-fields/{field_id}", { params: { path }, body: { name: name.trim() } }),
    );
    if (!res.ok) return setFailure(res.failure);
    setMode("view");
    changed();
  }

  async function remove() {
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/contact-fields/{field_id}", { params: { path } }));
    if (!res.ok) return setFailure(res.failure);
    changed();
  }

  return (
    <tr>
      <td>
        {mode === "rename" ? (
          <form onSubmit={rename} className="row">
            <label className="field grow">
              <span className="visually-hidden">New name for {field.name}</span>
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
          field.name
        )}
        <FailureNotice failure={failure} />
      </td>
      <td>
        <code>{field.key}</code>
      </td>
      <td className="muted">{formatDate(field.created_at)}</td>
      <td className="actions">
        {mode === "delete" ? (
          <div className="row end">
            <span className="small">
              Delete {field.name}? Every contact's {field.name} value is deleted too, and can't be brought back.
            </span>
            <button type="button" className="button small danger" onClick={() => void remove()}>
              Delete field
            </button>
            <button type="button" className="button small" onClick={() => setMode("view")}>
              Cancel
            </button>
          </div>
        ) : (
          mode === "view" && (
            <div className="row end">
              <button type="button" className="button small" onClick={() => setMode("rename")} aria-label={`Rename ${field.name}`}>
                Rename
              </button>
              <button type="button" className="button small" onClick={() => setMode("delete")} aria-label={`Delete ${field.name}`}>
                Delete
              </button>
            </div>
          )
        )}
      </td>
    </tr>
  );
}
