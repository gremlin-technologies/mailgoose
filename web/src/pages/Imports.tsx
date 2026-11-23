import { useCallback, useEffect, useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad, type Tone } from "../components";
import { useTags } from "./Tags";

type Import = components["schemas"]["Import"];
type SkipReason = components["schemas"]["SkipReason"];
type SkippedRow = components["schemas"]["SkippedRow"];
type ListSource = components["schemas"]["ListSource"];
type ImportField = components["schemas"]["ImportField"];
type ImportPreview = components["schemas"]["ImportPreview"];

/** The largest file an import takes (10 MB, as the API). */
export const maxImportBytes = 10 << 20;
/** Tags an import may put on every contact, as the API. */
const maxImportTags = 20;
/** How often an import's page asks how it's going, in ms. */
export const importPollMs = 750;

/** The fields a column can become, in the order the page offers them. */
export const importFields: { field: ImportField; label: string }[] = [
  { field: "email", label: "Email" },
  { field: "first_name", label: "First name" },
  { field: "last_name", label: "Last name" },
  { field: "status", label: "Status" },
  { field: "tags", label: "Tags" },
];

const separators: Record<ImportPreview["separator"], string> = {
  comma: "commas",
  semicolon: "semicolons",
  tab: "tabs",
};

type File_ = { name: string; text: string };

/** Which column (by position) each field comes from; a field with none isn't imported. */
type Matching = Partial<Record<ImportField, number>>;

/** The matching a preview found. */
export function previewMatching(p: ImportPreview): Matching {
  const m: Matching = {};
  for (const c of p.columns) if (c.field) m[c.field] = c.position;
  return m;
}

/** Puts field on the column at position, taking it off any other column. */
export function matchColumn(m: Matching, position: number, field: ImportField | ""): Matching {
  const next: Matching = {};
  for (const [f, pos] of Object.entries(m) as [ImportField, number][]) {
    if (pos !== position && f !== field) next[f] = pos;
  }
  if (field) next[field] = position;
  return next;
}

/**
 * An import's columns for a matching: each field's column by its name, and
 * an empty string for a field that isn't imported, so the import does
 * exactly what was confirmed and guesses nothing.
 */
export function matchingColumns(p: ImportPreview, m: Matching): Record<ImportField, string> {
  const name = (pos: number | undefined) => p.columns.find((c) => c.position === pos)?.name ?? "";
  return Object.fromEntries(importFields.map(({ field }) => [field, name(m[field])])) as Record<ImportField, string>;
}

/** A column's name to show: its header, or its place for a blank one. */
function columnLabel(c: ImportPreview["columns"][number]): string {
  return c.header.trim() || `Column ${c.position}`;
}

/** Where an import's people came from, in the order the page offers them. */
export const listSources: { source: ListSource; label: string }[] = [
  { source: "form", label: "They signed up on my site or form" },
  { source: "customers", label: "They're customers who bought from me" },
  { source: "personal", label: "I know them personally" },
  { source: "export", label: "An export from another email tool" },
  { source: "other", label: "Somewhere else" },
];
/** The longest description of an "other" list source, as the API. */
const maxListSourceDetail = 200;

/** Uploading a CSV file: pick it, match its columns, confirm permission, start the import. */
export function ImportContacts() {
  const api = useApi();
  const navigate = useNavigate();
  const tags = useTags();
  const [file, setFile] = useState<File_>();
  const [preview, setPreview] = useState<ImportPreview>();
  const [matching, setMatching] = useState<Matching>({});
  const [checking, setChecking] = useState(false);
  const [tagIDs, setTagIDs] = useState<string[]>([]);
  const [permission, setPermission] = useState(false);
  const [listSource, setListSource] = useState<ListSource>();
  const [listSourceDetail, setListSourceDetail] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  // Only the newest preview's answer counts.
  const previews = useRef(0);

  async function check(text: string, columns?: Record<ImportField, string>) {
    const n = ++previews.current;
    setChecking(true);
    const res = await attempt(() => api.POST("/v1/imports/preview", { body: { csv: text, columns } }));
    if (n !== previews.current) return undefined;
    setChecking(false);
    if (!res.ok) {
      setFailure(res.failure);
      return undefined;
    }
    return res.data;
  }

  async function choose(e: ChangeEvent<HTMLInputElement>) {
    setFailure(undefined);
    setFile(undefined);
    setPreview(undefined);
    const f = e.target.files?.[0];
    if (!f) return;
    if (f.size > maxImportBytes) {
      setFailure({
        code: "file_too_large",
        message: `${f.name} is larger than 10 MB.`,
        hint: "Split it into smaller files and import each one.",
        status: 0,
      });
      return;
    }
    const text = await f.text();
    if (text.trim() === "") {
      setFailure({
        code: "empty_file",
        message: `${f.name} has no header row.`,
        hint: "The first line should name the columns, such as email,first_name.",
        status: 0,
      });
      return;
    }
    const p = await check(text);
    if (!p) return;
    setFile({ name: f.name, text });
    setPreview(p);
    setMatching(previewMatching(p));
  }

  /** Changes what one column becomes, and checks the file again with the new matching. */
  async function rematch(position: number, field: ImportField | "") {
    if (!file || !preview) return;
    const m = matchColumn(matching, position, field);
    setMatching(m);
    setFailure(undefined);
    const p = await check(file.text, matchingColumns(preview, m));
    if (p) setPreview((old) => (old ? { ...old, problem: p.problem } : p));
  }

  async function start(e: FormEvent) {
    e.preventDefault();
    if (!file || !preview || !listSource) return;
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() =>
      api.POST("/v1/imports", {
        body: {
          csv: file.text,
          file_name: file.name,
          columns: matchingColumns(preview, matching),
          tags: tagIDs,
          permission_confirmed: permission,
          list_source: listSource,
          list_source_detail: listSource === "other" ? listSourceDetail : undefined,
        },
      }),
    );
    setBusy(false);
    if (!res.ok) setFailure(res.failure);
    else if (res.data) navigate(`/contacts/imports/${res.data.id}`);
  }

  function toggleTag(id: string) {
    setTagIDs((ids) => (ids.includes(id) ? ids.filter((t) => t !== id) : [...ids, id]));
  }

  const tagList = tags.state.status === "ok" ? tags.state.data : [];
  return (
    <section className="page">
      <p className="crumb">
        <Link to="/contacts">Contacts</Link>
      </p>
      <PageHeader title="Import contacts" about="Add contacts from a CSV file with a header row. Addresses you already have are left as they are." />
      <p>
        Moving from Mailchimp? <Link to="/contacts/import/mailchimp">Import from Mailchimp</Link> to bring your audiences,
        tags and emails over together.
      </p>
      <form onSubmit={start} className="stack">
        <div className="panel stack">
          <label className="field">
            <span>CSV file</span>
            <input type="file" accept=".csv,.tsv,.txt,text/csv,text/tab-separated-values" onChange={(e) => void choose(e)} />
          </label>
          {checking && !preview && <p className="muted small">Reading the file…</p>}
        </div>

        {file && preview && (
          <>
            <fieldset className="panel stack">
              <legend>Match the columns</legend>
              <p className="muted small">
                {file.name}: {preview.rows.toLocaleString()} {preview.rows === 1 ? "contact" : "contacts"}, separated by{" "}
                {separators[preview.separator]}. Choose what each column becomes. mailgoose keeps email, first and last
                name, status and tags, so other columns aren't imported.
              </p>
              <ul className="column-match">
                {preview.columns.map((c) => {
                  const field = (Object.entries(matching) as [ImportField, number][]).find(([, pos]) => pos === c.position)?.[0];
                  const id = `match-${c.position}`;
                  return (
                    <li key={c.position}>
                      <div className="column-match-about">
                        <label htmlFor={id}>{columnLabel(c)}</label>
                        <span className="muted small" id={`${id}-samples`}>
                          {c.samples.some((v) => v.trim()) ? c.samples.map((v) => v.trim() || "(blank)").join(", ") : "No values in the first rows"}
                        </span>
                      </div>
                      <div className="field">
                        <select
                          id={id}
                          value={field ?? ""}
                          onChange={(e) => void rematch(c.position, e.target.value as ImportField | "")}
                          aria-describedby={`${id}-samples`}
                        >
                          <option value="">Don't import</option>
                          {importFields.map(({ field: f, label }) => (
                            <option key={f} value={f}>
                              {label}
                            </option>
                          ))}
                        </select>
                      </div>
                    </li>
                  );
                })}
              </ul>
              <p className="muted small">
                A status column can only hold contacts back: rows marked unsubscribed, bounced, complained or unconfirmed
                keep that status. A tags column lists tag names separated by commas; new ones are created.
              </p>
              {matching.email === undefined ? (
                <p className="notice notice-warn" role="alert">
                  Choose the column that holds email addresses.
                </p>
              ) : (
                preview.problem && (
                  <p className="notice notice-warn" role="alert">
                    {preview.problem.message}
                  </p>
                )
              )}
            </fieldset>

            {tagList.length > 0 && (
              <fieldset className="panel stack">
                <legend>Tag everyone in this file</legend>
                <div className="choices">
                  {tagList.map((t) => (
                    <label key={t.id} className="check">
                      <input
                        type="checkbox"
                        checked={tagIDs.includes(t.id)}
                        onChange={() => toggleTag(t.id)}
                        disabled={!tagIDs.includes(t.id) && tagIDs.length >= maxImportTags}
                      />
                      <span>{t.name}</span>
                    </label>
                  ))}
                </div>
              </fieldset>
            )}

            <fieldset className="panel stack">
              <legend>Where did these contacts come from?</legend>
              <div className="choices">
                {listSources.map(({ source, label }) => (
                  <label key={source} className="check">
                    <input
                      type="radio"
                      name="list_source"
                      value={source}
                      checked={listSource === source}
                      onChange={() => setListSource(source)}
                      required
                    />
                    <span>{label}</span>
                  </label>
                ))}
              </div>
              {listSource === "other" && (
                <label className="field">
                  <span>Where, in a few words</span>
                  <input
                    type="text"
                    value={listSourceDetail}
                    onChange={(e) => setListSourceDetail(e.target.value)}
                    maxLength={maxListSourceDetail}
                    required
                  />
                </label>
              )}
              <p className="muted small">
                It's kept with each contact as where they came from. Files are also checked for bad, role (like info@)
                and throwaway addresses.
              </p>
            </fieldset>

            <div className="panel stack">
              <label className="check">
                <input type="checkbox" checked={permission} onChange={(e) => setPermission(e.target.checked)} />
                <span>
                  Everyone in this file has given me permission to email them. Without this they're added unconfirmed and
                  don't receive campaigns.
                </span>
              </label>
            </div>
          </>
        )}

        <FailureNotice failure={failure} />
        {file && preview && (
          <div>
            <button
              type="submit"
              className="button primary"
              disabled={
                busy ||
                checking ||
                matching.email === undefined ||
                !!preview.problem ||
                !listSource ||
                (listSource === "other" && !listSourceDetail.trim())
              }
            >
              {busy ? "Starting…" : "Import contacts"}
            </button>
          </div>
        )}
      </form>
    </section>
  );
}

const importStatus: Record<Import["status"], { label: string; tone: Tone }> = {
  queued: { label: "Waiting to start", tone: "neutral" },
  running: { label: "Importing", tone: "info" },
  waiting: { label: "Waiting for the paid plan", tone: "neutral" },
  done: { label: "Done", tone: "good" },
  failed: { label: "Failed", tone: "bad" },
  cancelled: { label: "Cancelled", tone: "neutral" },
};

export const skipReasons: Record<SkipReason, string> = {
  invalid_email: "Not an email address",
  invalid_row: "Row couldn't be read",
  duplicate: "Repeated in the file",
  exists: "Already a contact",
  suppressed: "Unsubscribed, bounced or complained before",
  over_limit: "Over the free plan's contact limit",
  not_subscribed: "Never subscribed in Mailchimp",
  archived: "Archived in Mailchimp",
};

/** One import: progress while it runs, then what was added and what was skipped. */
export function ImportDetail() {
  const api = useApi();
  const { id = "" } = useParams();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/imports/{import_id}", { params: { path: { import_id: id } } })),
    [api, id],
  );
  const { state, reload } = useLoad(load);
  const running = state.status === "ok" && (state.data.status === "queued" || state.data.status === "running");
  useEffect(() => {
    if (!running) return;
    const t = setTimeout(reload, importPollMs);
    return () => clearTimeout(t);
  }, [running, reload, state]);

  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") {
    return (
      <section className="page">
        <p className="crumb">
          <Link to="/contacts">Contacts</Link>
        </p>
        <PageHeader title="Import" />
        <LoadFailed failure={state.failure} retry={reload} />
      </section>
    );
  }
  const im = state.data;
  const s = importStatus[im.status];
  const reasons = (Object.keys(skipReasons) as SkipReason[]).filter((r) => (im.skipped_by_reason[r] ?? 0) > 0);
  return (
    <section className="page">
      <p className="crumb">
        <Link to="/contacts">Contacts</Link>
      </p>
      <PageHeader title={im.file_name ?? "Import"} about={`Started ${formatDate(im.created_at)}.`}>
        <Badge tone={s.tone}>{s.label}</Badge>
      </PageHeader>

      {running && (
        <div className="panel stack">
          <label className="field">
            <span>
              {im.processed_rows.toLocaleString()} of {im.total_rows.toLocaleString()} rows
            </span>
            <progress max={Math.max(im.total_rows, 1)} value={im.processed_rows} />
          </label>
          <p className="muted small">You can leave this page; the import carries on.</p>
        </div>
      )}
      {im.status === "failed" && (
        <div className="notice notice-error" role="alert">
          <p>The import stopped{im.error ? `: ${im.error}` : "."}</p>
          <p className="hint">Contacts it added before stopping stay. Fix the file and import it again.</p>
        </div>
      )}
      {im.status === "waiting" && <WaitingImport im={im} onCancelled={reload} />}
      {im.status === "cancelled" && (
        <div className="notice" role="status">
          <p>This import was cancelled while it waited for the paid plan.</p>
          <p className="hint">The contacts it took off your list stay off; the rest weren't added.</p>
        </div>
      )}
      {im.review === "held" && (
        <div className="notice notice-warn" role="status">
          <p>{im.hold_message ?? "mailgoose checks this import before these contacts get campaigns."}</p>
          <p className="hint">They're on your list in the meantime, and the rest of your list sends as usual.</p>
        </div>
      )}

      <dl className="stats">
        <div>
          <dt>Added</dt>
          <dd>{im.imported.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Skipped</dt>
          <dd>{im.skipped.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Rows</dt>
          <dd>{im.total_rows.toLocaleString()}</dd>
        </div>
      </dl>

      {im.list_source && (
        <p className="muted">
          Where they came from:{" "}
          {im.list_source === "other"
            ? (im.list_source_detail ?? "somewhere else")
            : listSources.find((l) => l.source === im.list_source)?.label}
        </p>
      )}
      {(im.checks.invalid_rows > 0 || im.checks.role_addresses > 0 || im.checks.disposable_addresses > 0) && (
        <>
          <h2>What the checks found</h2>
          <ul className="plain">
            <li>Rows that aren't email addresses: {im.checks.invalid_rows.toLocaleString()}</li>
            <li>Role addresses (like info@ or sales@): {im.checks.role_addresses.toLocaleString()}</li>
            <li>Addresses at throwaway email domains: {im.checks.disposable_addresses.toLocaleString()}</li>
          </ul>
        </>
      )}
      {reasons.length > 0 && (
        <>
          <h2>Why rows were skipped</h2>
          <ul className="plain">
            {reasons.map((r) => (
              <li key={r}>
                {skipReasons[r]}: {(im.skipped_by_reason[r] ?? 0).toLocaleString()}
              </li>
            ))}
          </ul>
        </>
      )}
      {!running && im.skipped > 0 && <SkippedRows importID={im.id} />}
      <p>
        <Link to="/contacts">Back to contacts</Link>
      </p>
    </section>
  );
}

/** A Mailchimp import waiting for the paid plan: what it waits for, and a way to cancel it. */
function WaitingImport({ im, onCancelled }: { im: Import; onCancelled: () => void }) {
  const api = useApi();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function cancel() {
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() =>
      api.POST("/v1/imports/{import_id}/cancel", { params: { path: { import_id: im.id } } }),
    );
    setBusy(false);
    if (!res.ok) setFailure(res.failure);
    else onCancelled();
  }

  return (
    <div className="notice notice-warn stack" role="status">
      <p>
        Its unsubscribed and bounced contacts are in. The rest would take your account past its contact limit, so
        they wait.
      </p>
      <p className="hint">{im.waiting_note ?? "It carries on once the account is on the paid plan."}</p>
      <FailureNotice failure={failure} />
      <div>
        <button type="button" className="button danger" disabled={busy} onClick={() => void cancel()}>
          {busy ? "Cancelling…" : "Cancel import"}
        </button>
      </div>
    </div>
  );
}

function SkippedRows({ importID }: { importID: string }) {
  const api = useApi();
  const page = useCallback(
    (cursor?: string) =>
      attempt(() =>
        api.GET("/v1/imports/{import_id}/skipped-rows", {
          params: { path: { import_id: importID }, query: { limit: 100, cursor } },
        }),
      ),
    [api, importID],
  );
  const first = useCallback(() => page(), [page]);
  const { state, reload } = useLoad(first);
  // Pages after the first, as "Show more" loads them.
  const [later, setLater] = useState<{ rows: SkippedRow[]; next: string | null }>();
  const [failure, setFailure] = useState<Failure>();
  const [busy, setBusy] = useState(false);

  async function more(cursor: string) {
    setBusy(true);
    setFailure(undefined);
    const res = await page(cursor);
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    setLater((l) => ({ rows: [...(l?.rows ?? []), ...(res.data?.data ?? [])], next: res.data?.next_cursor ?? null }));
  }

  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") return <LoadFailed failure={state.failure} retry={reload} />;
  const rows = [...state.data.data, ...(later?.rows ?? [])];
  const next = later ? later.next : state.data.next_cursor;

  return (
    <>
      <h2>Skipped rows</h2>
      <p>
        <a className="button" href={`/v1/imports/${encodeURIComponent(importID)}/skipped-rows/export`} download>
          Download skipped rows (CSV)
        </a>
      </p>
      {rows.length > 0 && (
        <table className="table">
          <thead>
            <tr>
              <th>Row</th>
              <th>Email</th>
              <th>Reason</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.row}>
                <td>{r.row}</td>
                <td>{r.email}</td>
                <td>{skipReasons[r.reason]}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {busy && <Loading />}
      <FailureNotice failure={failure} />
      {next && !busy && (
        <div>
          <button type="button" className="button" onClick={() => void more(next)}>
            Show more
          </button>
        </div>
      )}
    </>
  );
}
