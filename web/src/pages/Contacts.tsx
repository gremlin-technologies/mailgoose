import { useCallback, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad, type Tone } from "../components";
import { ContactCount, FilterBuilder, useTrackedCampaigns } from "../segments/FilterBuilder";
import { SaveSegment } from "../segments/SaveSegment";
import {
  conditionsFromParams,
  conditionsOf,
  conditionsToParams,
  contactQuery,
  contactSearch,
  draftFrom,
  isEmpty,
  type Conditions,
  type Draft,
} from "../segments/conditions";
import { useTags, type Tag } from "./Tags";

type Contact = components["schemas"]["Contact"];
type ContactStatus = components["schemas"]["ContactStatus"];
type EditableStatus = Extract<ContactStatus, "subscribed" | "unconfirmed" | "unsubscribed">;

export const contactStatus: Record<ContactStatus, { label: string; tone: Tone }> = {
  subscribed: { label: "Subscribed", tone: "good" },
  unconfirmed: { label: "Unconfirmed", tone: "warn" },
  unsubscribed: { label: "Unsubscribed", tone: "neutral" },
  bounced: { label: "Bounced", tone: "bad" },
  complained: { label: "Complained", tone: "bad" },
};

export function ContactBadge({ status }: { status: ContactStatus }) {
  const s = contactStatus[status];
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

/** Contacts a page lists. */
export const contactsPageSize = 50;

type Filters = { q: string; by: "email" | "name"; conditions: Conditions };

function filtersFrom(params: URLSearchParams): Filters {
  return {
    q: params.get("q") ?? "",
    by: params.get("by") === "name" ? "name" : "email",
    conditions: conditionsFromParams(params),
  };
}

/** The search box as API query parameters; empty, it's left out. */
function searchQuery(q: string, by: Filters["by"]) {
  const t = q.trim();
  return { email: by === "email" && t ? t : undefined, name: by === "name" && t ? t : undefined };
}

/** The filters as API query parameters; empty ones are left out. */
function filterQuery(f: Filters) {
  return { ...searchQuery(f.q, f.by), ...contactQuery(f.conditions) };
}

function exportHref(f: Filters): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(searchQuery(f.q, f.by))) if (v) params.set(k, v);
  contactSearch(f.conditions, params);
  const qs = params.toString();
  return `/v1/contacts/export${qs ? `?${qs}` : ""}`;
}

/** The contact list: search, filters, pages, adding one, bulk tagging, import and export. */
export function Contacts() {
  const [params] = useSearchParams();
  const filters = useMemo(() => filtersFrom(params), [params]);
  // Changing a filter starts a new list (key), back on its first page.
  return <ContactList key={params.toString()} filters={filters} />;
}

function ContactList({ filters }: { filters: Filters }) {
  const api = useApi();
  const [, setParams] = useSearchParams();
  const tags = useTags();
  const tagList = tags.state.status === "ok" ? tags.state.data : [];
  // Cursors of the pages before this one; the last is this page's.
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors[cursors.length - 1];
  const load = useCallback(
    () =>
      attempt(() =>
        api.GET("/v1/contacts", { params: { query: { limit: contactsPageSize, cursor, ...filterQuery(filters) } } }),
      ),
    [api, cursor, filters],
  );
  const { state, reload } = useLoad(load);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [adding, setAdding] = useState(false);
  const [notice, setNotice] = useState("");
  const [q, setQ] = useState(filters.q);
  const [by, setBy] = useState(filters.by);
  const [draft, setDraft] = useState<Draft>(() => draftFrom(filters.conditions));
  const campaigns = useTrackedCampaigns();
  const campaignList = campaigns.state.status === "ok" ? campaigns.state.data : [];
  const drafted = conditionsOf(draft);

  function search(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const next = new URLSearchParams();
    if (q.trim()) {
      next.set("q", q.trim());
      if (by === "name") next.set("by", "name");
    }
    conditionsToParams(conditionsOf(draft), next);
    setParams(next);
  }

  function changed(message: string) {
    setNotice(message);
    setSelected(new Set());
    reload();
    tags.reload();
  }

  const contacts = state.status === "ok" ? state.data.data : [];
  const filtered = Boolean(filters.q.trim()) || !isEmpty(filters.conditions);
  const conditionCount = Object.keys(filters.conditions).length;
  const allSelected = contacts.length > 0 && contacts.every((c) => selected.has(c.id));

  function toggle(id: string) {
    setSelected((s) => {
      const next = new Set(s);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  return (
    <section className="page">
      <PageHeader title="Contacts" about="Everyone you can email. Only subscribed contacts receive campaigns.">
        <div className="row">
          <Link to="/contacts/import" className="button">
            Import CSV
          </Link>
          <a href={exportHref(filters)} className="button" download>
            Export CSV
          </a>
          <button type="button" className="button primary" onClick={() => setAdding((a) => !a)} aria-expanded={adding}>
            Add contact
          </button>
        </div>
      </PageHeader>

      {adding && (
        <AddContact
          onAdded={(c) => {
            setAdding(false);
            changed(`Added ${c.email}.`);
          }}
          onCancel={() => setAdding(false)}
        />
      )}

      <form onSubmit={search} className="panel stack" role="search" aria-label="Find contacts">
        <div className="row-form">
          <label className="field grow">
            <span>Search</span>
            <input name="q" type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder="pat@example.com" maxLength={254} />
          </label>
          <label className="field">
            <span>In</span>
            <select name="by" value={by} onChange={(e) => setBy(e.target.value === "name" ? "name" : "email")}>
              <option value="email">Email</option>
              <option value="name">Name</option>
            </select>
          </label>
        </div>
        <details className="filters" open={conditionCount > 0}>
          <summary>Filters{conditionCount > 0 ? ` (${conditionCount} set)` : ""}</summary>
          <FilterBuilder draft={draft} onChange={setDraft} tags={tagList} campaigns={campaignList} />
        </details>
        <ContactCount conditions={drafted} extra={searchQuery(q, by)} />
        <div className="row">
          <button type="submit" className="button">
            Search
          </button>
          {filtered && (
            <button type="button" className="button" onClick={() => setParams(new URLSearchParams())}>
              Clear
            </button>
          )}
          <SaveSegment conditions={drafted} searched={Boolean(q.trim())} />
        </div>
      </form>

      {notice && (
        <div className="notice notice-good" role="status">
          <p>{notice}</p>
        </div>
      )}

      {selected.size > 0 && <BulkTag ids={[...selected]} tags={tagList} onDone={changed} />}

      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (contacts.length === 0 ? (
          <p className="empty">
            {filtered || cursor
              ? "No contacts match."
              : "No contacts yet. Add one, import a CSV file, or put a signup form on your site."}
          </p>
        ) : (
          <>
            <table className="table">
              <thead>
                <tr>
                  <th className="pick">
                    <input
                      type="checkbox"
                      aria-label="Select every contact on this page"
                      checked={allSelected}
                      onChange={() => setSelected(allSelected ? new Set() : new Set(contacts.map((c) => c.id)))}
                    />
                  </th>
                  <th>Email</th>
                  <th>Name</th>
                  <th>Status</th>
                  <th>Tags</th>
                  <th>Added</th>
                </tr>
              </thead>
              <tbody>
                {contacts.map((c) => (
                  <tr key={c.id}>
                    <td className="pick">
                      <input
                        type="checkbox"
                        aria-label={`Select ${c.email}`}
                        checked={selected.has(c.id)}
                        onChange={() => toggle(c.id)}
                      />
                    </td>
                    <td>
                      <Link to={`/contacts/${c.id}`}>{c.email}</Link>
                    </td>
                    <td>{fullName(c)}</td>
                    <td>
                      <ContactBadge status={c.status} />
                    </td>
                    <td className="small">{c.tags.map((t) => t.name).join(", ")}</td>
                    <td className="muted">{formatDate(c.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <nav className="row pager" aria-label="Pages">
              <button
                type="button"
                className="button"
                disabled={cursors.length === 0}
                onClick={() => {
                  setSelected(new Set());
                  setCursors((cs) => cs.slice(0, -1));
                }}
              >
                Previous
              </button>
              <button
                type="button"
                className="button"
                disabled={!state.data.next_cursor}
                onClick={() => {
                  const next = state.data.next_cursor;
                  if (!next) return;
                  setSelected(new Set());
                  setCursors((cs) => [...cs, next]);
                }}
              >
                Next
              </button>
            </nav>
          </>
        ))}
    </section>
  );
}

function fullName(c: Contact): string {
  return [c.first_name, c.last_name].filter(Boolean).join(" ");
}

/** The form for adding one contact by hand. */
function AddContact({ onAdded, onCancel }: { onAdded: (c: Contact) => void; onCancel: () => void }) {
  const api = useApi();
  const [email, setEmail] = useState("");
  const [first, setFirst] = useState("");
  const [last, setLast] = useState("");
  const [permission, setPermission] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() =>
      api.POST("/v1/contacts", {
        body: {
          email: email.trim(),
          first_name: first.trim() || null,
          last_name: last.trim() || null,
          status: permission ? "subscribed" : "unconfirmed",
          source: "manual",
        },
      }),
    );
    setBusy(false);
    if (!res.ok) setFailure(res.failure);
    else if (res.data) onAdded(res.data);
  }

  return (
    <form onSubmit={add} className="panel stack" aria-label="Add a contact">
      <div className="row-form">
        <label className="field grow">
          <span>Email</span>
          <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} maxLength={254} required autoFocus />
        </label>
        <label className="field">
          <span>First name</span>
          <input value={first} onChange={(e) => setFirst(e.target.value)} maxLength={100} />
        </label>
        <label className="field">
          <span>Last name</span>
          <input value={last} onChange={(e) => setLast(e.target.value)} maxLength={100} />
        </label>
      </div>
      <label className="check">
        <input type="checkbox" checked={permission} onChange={(e) => setPermission(e.target.checked)} />
        <span>
          I have their permission to email them. Without it they're added unconfirmed and don't receive campaigns.
        </span>
      </label>
      <FailureNotice failure={failure} />
      <div className="row">
        <button type="submit" className="button primary" disabled={busy}>
          {busy ? "Adding…" : "Add contact"}
        </button>
        <button type="button" className="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}

/** Puts a tag on, or takes it off, the selected contacts. */
function BulkTag({ ids, tags, onDone }: { ids: string[]; tags: Tag[]; onDone: (message: string) => void }) {
  const api = useApi();
  const [tag, setTag] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const n = ids.length;
  const contactsWord = n === 1 ? "contact" : "contacts";

  async function apply(remove: boolean) {
    const name = tags.find((t) => t.id === tag)?.name ?? "the tag";
    setBusy(true);
    setFailure(undefined);
    const path = { tag_id: tag };
    const body = { contact_ids: ids };
    const res = await attempt(() =>
      remove
        ? api.POST("/v1/tags/{tag_id}/contacts/remove", { params: { path }, body })
        : api.POST("/v1/tags/{tag_id}/contacts", { params: { path }, body }),
    );
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    const changed = res.data?.changed ?? 0;
    onDone(
      remove
        ? `Took ${name} off ${changed.toLocaleString()} ${changed === 1 ? "contact" : "contacts"}.`
        : `Tagged ${changed.toLocaleString()} ${changed === 1 ? "contact" : "contacts"} ${name}.`,
    );
  }

  return (
    <div className="panel stack" role="region" aria-label="Selected contacts">
      <div className="row-form">
        <p className="grow">
          <strong>
            {n.toLocaleString()} {contactsWord} selected
          </strong>
        </p>
        {tags.length === 0 ? (
          <p className="muted small">
            <Link to="/tags">Create a tag</Link> to tag contacts.
          </p>
        ) : (
          <>
            <label className="field">
              <span>Tag</span>
              <select value={tag} onChange={(e) => setTag(e.target.value)}>
                <option value="">Choose a tag</option>
                {tags.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </select>
            </label>
            <button type="button" className="button primary" disabled={!tag || busy} onClick={() => void apply(false)}>
              Add tag
            </button>
            <button type="button" className="button" disabled={!tag || busy} onClick={() => void apply(true)}>
              Remove tag
            </button>
          </>
        )}
      </div>
      <FailureNotice failure={failure} />
    </div>
  );
}

const editable: EditableStatus[] = ["subscribed", "unconfirmed", "unsubscribed"];

/** One contact: edit its fields, its tags, or delete it. */
export function ContactDetail() {
  const api = useApi();
  const { id = "" } = useParams();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/contacts/{contact_id}", { params: { path: { contact_id: id } } })),
    [api, id],
  );
  const { state, reload, replace } = useLoad(load);

  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") {
    return (
      <section className="page">
        <p className="crumb">
          <Link to="/contacts">Contacts</Link>
        </p>
        <PageHeader title="Contact" />
        <LoadFailed failure={state.failure} retry={reload} />
      </section>
    );
  }
  const c = state.data;
  return (
    <section className="page">
      <p className="crumb">
        <Link to="/contacts">Contacts</Link>
      </p>
      <PageHeader title={c.email} about={`Added ${formatDate(c.created_at)} by ${sourceLabel[c.source]}.`}>
        <ContactBadge status={c.status} />
      </PageHeader>
      <EditContact key={c.updated_at} contact={c} onSaved={replace} />
      <ContactTags contact={c} changed={reload} />
      <DeleteContact contact={c} />
    </section>
  );
}

const sourceLabel: Record<Contact["source"], string> = {
  form: "a signup form",
  import: "an import",
  api: "the API",
  manual: "hand",
};

function EditContact({ contact: c, onSaved }: { contact: Contact; onSaved: (c: Contact) => void }) {
  const api = useApi();
  const [email, setEmail] = useState(c.email);
  const [first, setFirst] = useState(c.first_name ?? "");
  const [last, setLast] = useState(c.last_name ?? "");
  const [status, setStatus] = useState(c.status);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const canChangeStatus = editable.includes(c.status as EditableStatus);

  async function save(e: FormEvent) {
    e.preventDefault();
    const body: components["schemas"]["UpdateContactRequest"] = {};
    if (email.trim() !== c.email) body.email = email.trim();
    if ((first.trim() || null) !== (c.first_name || null)) body.first_name = first.trim() || null;
    if ((last.trim() || null) !== (c.last_name || null)) body.last_name = last.trim() || null;
    if (status !== c.status && canChangeStatus) body.status = status as EditableStatus;
    setSaved(false);
    setFailure(undefined);
    if (Object.keys(body).length === 0) return setSaved(true);
    setBusy(true);
    const res = await attempt(() => api.PATCH("/v1/contacts/{contact_id}", { params: { path: { contact_id: c.id } }, body }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    if (res.data) onSaved(res.data);
  }

  return (
    <form onSubmit={save} className="panel stack" aria-label="Edit contact">
      <div className="row-form">
        <label className="field grow">
          <span>Email</span>
          <input type="email" value={email} onChange={(e) => setEmail(e.target.value)} maxLength={254} required />
        </label>
        <label className="field">
          <span>First name</span>
          <input value={first} onChange={(e) => setFirst(e.target.value)} maxLength={100} />
        </label>
        <label className="field">
          <span>Last name</span>
          <input value={last} onChange={(e) => setLast(e.target.value)} maxLength={100} />
        </label>
        {canChangeStatus && (
          <label className="field">
            <span>Status</span>
            <select value={status} onChange={(e) => setStatus(e.target.value as ContactStatus)}>
              {editable.map((s) => (
                <option key={s} value={s}>
                  {contactStatus[s].label}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>
      {!canChangeStatus && (
        <p className="muted small">
          This address {c.status === "bounced" ? "bounced" : "marked an email as spam"}, so it stays{" "}
          {contactStatus[c.status].label.toLowerCase()} and isn't mailed again.
        </p>
      )}
      <FailureNotice failure={failure} />
      <div className="row">
        <button type="submit" className="button primary" disabled={busy}>
          {busy ? "Saving…" : "Save"}
        </button>
        {saved && (
          <span className="muted small" role="status">
            Nothing changed.
          </span>
        )}
      </div>
    </form>
  );
}

function ContactTags({ contact: c, changed }: { contact: Contact; changed: () => void }) {
  const api = useApi();
  const tags = useTags();
  const [tag, setTag] = useState("");
  const [failure, setFailure] = useState<Failure>();
  const has = new Set(c.tags.map((t) => t.id));
  const others = tags.state.status === "ok" ? tags.state.data.filter((t) => !has.has(t.id)) : [];

  async function change(tagID: string, remove: boolean) {
    setFailure(undefined);
    const path = { tag_id: tagID };
    const body = { contact_ids: [c.id] };
    const res = await attempt(() =>
      remove
        ? api.POST("/v1/tags/{tag_id}/contacts/remove", { params: { path }, body })
        : api.POST("/v1/tags/{tag_id}/contacts", { params: { path }, body }),
    );
    if (!res.ok) return setFailure(res.failure);
    setTag("");
    changed();
  }

  return (
    <div className="panel stack">
      <h2>Tags</h2>
      {c.tags.length === 0 ? (
        <p className="muted">No tags.</p>
      ) : (
        <ul className="tag-list">
          {c.tags.map((t) => (
            <li key={t.id}>
              <span>{t.name}</span>
              <button type="button" className="button small" onClick={() => void change(t.id, true)} aria-label={`Remove tag ${t.name}`}>
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}
      {others.length > 0 && (
        <div className="row-form">
          <label className="field">
            <span>Add a tag</span>
            <select value={tag} onChange={(e) => setTag(e.target.value)}>
              <option value="">Choose a tag</option>
              {others.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </label>
          <button type="button" className="button" disabled={!tag} onClick={() => void change(tag, false)}>
            Add tag
          </button>
        </div>
      )}
      <FailureNotice failure={failure} />
    </div>
  );
}

function DeleteContact({ contact: c }: { contact: Contact }) {
  const api = useApi();
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState(false);
  const [erase, setErase] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function remove() {
    setFailure(undefined);
    const res = await attempt(() =>
      api.DELETE("/v1/contacts/{contact_id}", { params: { path: { contact_id: c.id }, query: erase ? { erase: true } : {} } }),
    );
    if (!res.ok) return setFailure(res.failure);
    navigate("/contacts");
  }

  return (
    <div className="panel danger-zone">
      <h2>Delete this contact</h2>
      <p className="muted">
        Deletes the contact and its history. If it unsubscribed, bounced or complained, adding the address again keeps
        that status.
      </p>
      {confirm ? (
        <div className="stack">
          <label className="check">
            <input type="checkbox" checked={erase} onChange={(e) => setErase(e.target.checked)} />
            <span>
              They asked to have their data erased. Removes the address from import reports too, and keeps it from being
              added back.
            </span>
          </label>
          <div className="row">
            <button type="button" className="button danger" onClick={() => void remove()}>
              Delete {c.email}
            </button>
            <button type="button" className="button" onClick={() => setConfirm(false)}>
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <div>
          <button type="button" className="button" onClick={() => setConfirm(true)}>
            Delete contact
          </button>
        </div>
      )}
      <FailureNotice failure={failure} />
    </div>
  );
}
