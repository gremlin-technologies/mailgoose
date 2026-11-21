import { useCallback, useEffect, useState, type ChangeEvent, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad, type Tone } from "../components";
import { csvHeader, csvRowCount, guessColumn, type ImportField } from "../csv";
import { useTags } from "./Tags";

type Import = components["schemas"]["Import"];
type SkipReason = components["schemas"]["SkipReason"];
type SkippedRow = components["schemas"]["SkippedRow"];
type ListSource = components["schemas"]["ListSource"];

/** The largest file an import takes (10 MB, as the API). */
export const maxImportBytes = 10 << 20;
/** Tags an import may put on every contact, as the API. */
const maxImportTags = 20;
/** How often an import's page asks how it's going, in ms. */
export const importPollMs = 750;

const fields: { field: ImportField; label: string; about: string }[] = [
  { field: "email", label: "Email", about: "Required." },
  { field: "first_name", label: "First name", about: "" },
  { field: "last_name", label: "Last name", about: "" },
  {
    field: "status",
    label: "Status",
    about: "Rows marked unsubscribed, bounced, complained or unconfirmed keep that status.",
  },
  { field: "tags", label: "Tags", about: "Tag names separated by commas; new ones are created." },
];

type File_ = { name: string; text: string; headers: string[]; rows: number };

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
  const [columns, setColumns] = useState<Partial<Record<ImportField, string>>>({});
  const [tagIDs, setTagIDs] = useState<string[]>([]);
  const [permission, setPermission] = useState(false);
  const [listSource, setListSource] = useState<ListSource>();
  const [listSourceDetail, setListSourceDetail] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function choose(e: ChangeEvent<HTMLInputElement>) {
    setFailure(undefined);
    setFile(undefined);
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
    const headers = csvHeader(text);
    if (headers.length === 0) {
      setFailure({
        code: "empty_file",
        message: `${f.name} has no header row.`,
        hint: "The first line should name the columns, such as email,first_name.",
        status: 0,
      });
      return;
    }
    setFile({ name: f.name, text, headers, rows: csvRowCount(text) });
    setColumns(Object.fromEntries(fields.map(({ field }) => [field, guessColumn(field, headers)])));
  }

  async function start(e: FormEvent) {
    e.preventDefault();
    if (!file || !listSource) return;
    setBusy(true);
    setFailure(undefined);
    const picked = Object.fromEntries(Object.entries(columns).filter(([, header]) => header));
    const res = await attempt(() =>
      api.POST("/v1/imports", {
        body: {
          csv: file.text,
          file_name: file.name,
          columns: picked,
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
      <form onSubmit={start} className="stack">
        <div className="panel stack">
          <label className="field">
            <span>CSV file</span>
            <input type="file" accept=".csv,text/csv" onChange={(e) => void choose(e)} />
          </label>
          {file && (
            <p className="muted small">
              {file.name}: {file.headers.length} columns, about {file.rows.toLocaleString()} rows.
            </p>
          )}
        </div>

        {file && (
          <>
            <fieldset className="panel stack">
              <legend>Match the columns</legend>
              {fields.map(({ field, label, about }) => (
                <div key={field} className="field">
                  <label htmlFor={`map-${field}`}>{label}</label>
                  <select
                    id={`map-${field}`}
                    value={columns[field] ?? ""}
                    onChange={(e) => setColumns((c) => ({ ...c, [field]: e.target.value }))}
                    required={field === "email"}
                    aria-describedby={about ? `map-${field}-about` : undefined}
                  >
                    <option value="">{field === "email" ? "Choose a column" : "Not mapped"}</option>
                    {file.headers.map((h, i) => (
                      <option key={`${i}:${h}`} value={h}>
                        {h || `Column ${i + 1}`}
                      </option>
                    ))}
                  </select>
                  {about && (
                    <span id={`map-${field}-about`} className="muted small">
                      {about}
                    </span>
                  )}
                </div>
              ))}
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
        {file && (
          <div>
            <button
              type="submit"
              className="button primary"
              disabled={busy || !columns.email || !listSource || (listSource === "other" && !listSourceDetail.trim())}
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
