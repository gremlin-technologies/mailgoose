import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { dollars } from "../billing/money";
import { Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { useContactFields, type ContactField } from "./Fields";
import { importPollMs, type FieldTarget } from "./Imports";

type Bundle = components["schemas"]["MailchimpBundle"];
type Preview = components["schemas"]["MailchimpPreview"];
type ContactsPreview = components["schemas"]["MailchimpContactsPreview"];
type Email = components["schemas"]["MailchimpEmail"];
type Connection = components["schemas"]["MailchimpConnection"];
type Fetch = components["schemas"]["MailchimpFetch"];
type MergeField = components["schemas"]["MailchimpMergeField"];

/** The largest export an upload takes (25 MB, as the API). */
export const maxMailchimpBytes = 25 << 20;
/** How many of the newest campaigns an import can bring, as the API. */
const maxCampaigns = 100;
/** How many audiences and segments one fetch reads, as the API. */
const maxFetchAudiences = 20;
const maxFetchSegments = 20;

/** Leaving for Mailchimp's page; tests swap it out, since jsdom can't navigate. */
export const leave = { to: (url: string) => window.location.assign(url) };

/** Why an import would be held, for each hold_reason code. */
const holds: Record<string, string> = {
  over_contact_limit: "it would take your account past its contact limit",
  large_first_import: "it's your account's first import of more than 2,000 contacts",
  invalid_addresses: "too many rows aren't valid email addresses",
  risky_addresses: "too many are role addresses like info@ or at throwaway email domains",
  shared_list: "many of its addresses are on lists other accounts imported",
};

const fetchSteps: Record<string, string> = {
  audiences: "Reading your audiences",
  segments: "Reading your saved segments",
  members: "Reading members",
  campaigns: "Reading your newest campaigns",
};

/** Why a merge field is left out by default, for each skip_reason code. */
const mergeSkips: Record<NonNullable<MergeField["skip_reason"]>, string> = {
  field_limit: "no room for another field",
  invalid_field_name: "its name can't be a field's",
  reserved_field_name: "its name is taken by a contact's own details",
  field_exists: "you have a field with that name",
};

/** The longest contact field name, as the API. */
const maxFieldName = 50;

/** What each merge field fills, by its name; one with none is left out. */
export type MergeMatching = Record<string, FieldTarget>;

/** The contact fields the preview matched or would make. */
export function previewMergeMatching(fields: MergeField[]): MergeMatching {
  const m: MergeMatching = {};
  for (const f of fields) {
    if (f.custom_field) m[f.name] = { key: f.custom_field.key };
    else if (f.new_field) m[f.name] = { name: f.new_field.name };
  }
  return m;
}

/** Puts target on one merge field, taking the same existing field off any other. */
export function matchMergeField(m: MergeMatching, name: string, target: FieldTarget | undefined): MergeMatching {
  const next: MergeMatching = {};
  for (const [n, t] of Object.entries(m)) {
    if (n === name) continue;
    if (target && "key" in target && "key" in t && t.key === target.key) continue;
    next[n] = t;
  }
  if (target) next[name] = target;
  return next;
}

/**
 * The confirm request's field choices: every field the page knows, by the
 * merge field it comes from or an empty string, so the import guesses
 * nothing; and exactly the new fields to make, none when the list is empty.
 */
export function mergeFieldRequest(
  merge: { name: string }[],
  m: MergeMatching,
  fields: { key: string }[],
): { field_columns: Record<string, string>; new_fields: { column: string; name: string }[] } {
  const cols: Record<string, string> = Object.fromEntries(fields.map((f) => [f.key, ""]));
  const news: { column: string; name: string }[] = [];
  for (const { name } of merge) {
    const t = m[name];
    if (!t) continue;
    if ("key" in t) cols[t.key] = name;
    else news.push({ column: name, name: t.name.trim() });
  }
  return { field_columns: cols, new_fields: news };
}

function plural(n: number, one: string, many = `${one}s`): string {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`;
}

/** /contacts/import/mailchimp: upload an export, or connect Mailchimp and fetch. */
export function MailchimpImport() {
  const api = useApi();
  const load = useCallback(() => attempt(() => api.GET("/v1/imports/mailchimp/connection")), [api]);
  const { state, replace } = useLoad(load);
  return (
    <section className="page">
      <p className="crumb">
        <Link to="/contacts">Contacts</Link> / <Link to="/contacts/import">Import</Link>
      </p>
      <PageHeader
        title="Import from Mailchimp"
        about="Bring your Mailchimp audiences, their tags and your emails. You see what would come over before anything is imported."
      />
      <UploadExport />
      {state.status === "ok" && state.data.can_connect && <ConnectMailchimp connection={state.data} onChange={replace} />}
    </section>
  );
}

function UploadExport() {
  const api = useApi();
  const navigate = useNavigate();
  const [file, setFile] = useState<File>();
  const [audienceName, setAudienceName] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const isCSV = file?.name.toLowerCase().endsWith(".csv") ?? false;

  function choose(f: File | undefined) {
    setFailure(undefined);
    setFile(undefined);
    if (!f) return;
    if (f.size > maxMailchimpBytes) {
      setFailure({
        code: "file_too_large",
        message: `${f.name} is larger than 25 MB.`,
        hint: "Export one audience at a time from Mailchimp and upload each.",
        status: 0,
      });
      return;
    }
    setFile(f);
  }

  async function upload(e: FormEvent) {
    e.preventDefault();
    if (!file) return;
    setBusy(true);
    setFailure(undefined);
    const form = new FormData();
    form.append("file", file, file.name);
    if (isCSV && audienceName.trim()) form.append("audience_name", audienceName.trim());
    const res = await attempt(() =>
      api.POST("/v1/imports/mailchimp/bundles", {
        // The typed body is the schema's; the request sends the form as it is.
        body: { file: "" },
        bodySerializer: () => form,
      }),
    );
    setBusy(false);
    if (!res.ok) setFailure(res.failure);
    else if (res.data) navigate(`/contacts/import/mailchimp/${res.data.id}`);
  }

  return (
    <form onSubmit={(e) => void upload(e)} className="panel stack" aria-labelledby="mailchimp-upload">
      <h2 id="mailchimp-upload">Upload Mailchimp's export</h2>
      <ol>
        <li>In Mailchimp, open your account's settings and choose Manage my data, then Export data.</li>
        <li>Mailchimp emails you when the export is ready. Download the ZIP file.</li>
        <li>Choose it here. A large account can export one audience at a time instead, as a ZIP or CSV.</li>
      </ol>
      <label className="field">
        <span>Export file (ZIP or CSV, up to 25 MB)</span>
        <input type="file" accept=".zip,.csv,application/zip,text/csv" onChange={(e) => choose(e.target.files?.[0])} />
      </label>
      {isCSV && (
        <label className="field">
          <span>Audience name</span>
          <input
            type="text"
            value={audienceName}
            maxLength={100}
            placeholder="Mailchimp"
            onChange={(e) => setAudienceName(e.target.value)}
          />
          <span className="hint">A CSV doesn't say which audience it's from; this names it.</span>
        </label>
      )}
      <FailureNotice failure={failure} />
      <div>
        <button type="submit" className="button primary" disabled={!file || busy}>
          {busy ? "Reading…" : "Upload and preview"}
        </button>
      </div>
    </form>
  );
}

function ConnectMailchimp({ connection, onChange }: { connection: Connection; onChange: (c: Connection) => void }) {
  const api = useApi();
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function connect() {
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/imports/mailchimp/connect"));
    if (!res.ok || !res.data) {
      setBusy(false);
      if (!res.ok) setFailure(res.failure);
      return;
    }
    leave.to(res.data.authorize_url);
  }

  async function disconnect() {
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/imports/mailchimp/disconnect"));
    setBusy(false);
    if (!res.ok) setFailure(res.failure);
    else if (res.data) onChange(res.data);
  }

  return (
    <div className="panel stack" role="region" aria-labelledby="mailchimp-connect">
      <h2 id="mailchimp-connect">Or connect Mailchimp</h2>
      {!connection.connected ? (
        <>
          <p>
            Let mailgoose read your Mailchimp account, then pick the audiences to bring. mailgoose only reads, and
            forgets the connection once you import, or after a day.
          </p>
          <FailureNotice failure={failure} />
          <div>
            <button type="button" className="button" disabled={busy} onClick={() => void connect()}>
              {busy ? "Opening Mailchimp…" : "Connect Mailchimp"}
            </button>
          </div>
        </>
      ) : (
        <>
          <p>
            Connected to {connection.account_name ?? "your Mailchimp account"}. mailgoose forgets it once you import, or
            on {formatDate(connection.expires_at)}.
          </p>
          <FailureNotice failure={failure} />
          <div>
            <button type="button" className="button" disabled={busy} onClick={() => void disconnect()}>
              Disconnect
            </button>
          </div>
          <FetchAudiences />
        </>
      )}
    </div>
  );
}

function FetchAudiences() {
  const api = useApi();
  const navigate = useNavigate();
  const load = useCallback(() => attempt(() => api.GET("/v1/imports/mailchimp/audiences")), [api]);
  const { state, reload } = useLoad(load);
  const [picked, setPicked] = useState<string[]>([]);
  const [segments, setSegments] = useState("");
  const [campaigns, setCampaigns] = useState(10);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  if (state.status === "loading") return <Loading label="Reading your Mailchimp audiences…" />;
  if (state.status === "failed") return <LoadFailed failure={state.failure} retry={reload} />;
  const audiences = state.data.audiences;
  const segmentIDs = segments
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean);

  async function start(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() =>
      api.POST("/v1/imports/mailchimp/fetch", {
        body: { audience_ids: picked, segment_ids: segmentIDs.length ? segmentIDs : undefined, campaigns },
      }),
    );
    setBusy(false);
    if (!res.ok) setFailure(res.failure);
    else if (res.data) navigate(`/contacts/import/mailchimp/fetch/${res.data.id}`);
  }

  function toggle(id: string) {
    setPicked((ids) => (ids.includes(id) ? ids.filter((a) => a !== id) : [...ids, id]));
  }

  if (audiences.length === 0) return <p className="muted">This Mailchimp account has no audiences.</p>;
  return (
    <form onSubmit={(e) => void start(e)} className="stack">
      <fieldset className="stack">
        <legend>Audiences to bring</legend>
        <div className="choices">
          {audiences.map((a) => (
            <label key={a.id} className="check">
              <input
                type="checkbox"
                checked={picked.includes(a.id)}
                disabled={!picked.includes(a.id) && picked.length >= maxFetchAudiences}
                onChange={() => toggle(a.id)}
              />
              <span>
                {a.name}: {plural(a.members.total, "member")} ({a.members.subscribed.toLocaleString()} subscribed)
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      <label className="field">
        <span>Saved segments to bring as tags (optional)</span>
        <input type="text" value={segments} onChange={(e) => setSegments(e.target.value)} inputMode="numeric" />
        <span className="hint">
          Segment ids, separated by commas. Each segment's members get a tag named after it.
        </span>
      </label>
      <label className="field">
        <span>Newest campaigns to bring as emails</span>
        <input
          type="number"
          min={0}
          max={maxCampaigns}
          value={campaigns}
          onChange={(e) => setCampaigns(Math.max(0, Math.min(maxCampaigns, Number(e.target.value) || 0)))}
        />
      </label>
      <FailureNotice failure={failure} />
      <div>
        <button
          type="submit"
          className="button primary"
          disabled={busy || picked.length === 0 || segmentIDs.length > maxFetchSegments}
        >
          {busy ? "Starting…" : "Fetch and preview"}
        </button>
      </div>
    </form>
  );
}

/** /contacts/import/mailchimp/fetch/:id: a fetch from Mailchimp, until it's done. */
export function MailchimpFetchProgress() {
  const api = useApi();
  const navigate = useNavigate();
  const { id = "" } = useParams();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/imports/mailchimp/fetch/{fetch_id}", { params: { path: { fetch_id: id } } })),
    [api, id],
  );
  const { state, reload } = useLoad(load);
  const running = state.status === "ok" && (state.data.status === "queued" || state.data.status === "running");
  const bundleID = state.status === "ok" && state.data.status === "done" ? state.data.bundle_id : null;
  useEffect(() => {
    if (!running) return;
    const t = setTimeout(reload, importPollMs);
    return () => clearTimeout(t);
  }, [running, reload, state]);
  useEffect(() => {
    if (bundleID) navigate(`/contacts/import/mailchimp/${bundleID}`, { replace: true });
  }, [bundleID, navigate]);

  const crumb = (
    <p className="crumb">
      <Link to="/contacts">Contacts</Link> / <Link to="/contacts/import/mailchimp">Import from Mailchimp</Link>
    </p>
  );
  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") {
    return (
      <section className="page">
        {crumb}
        <PageHeader title="Fetching from Mailchimp" />
        <LoadFailed failure={state.failure} retry={reload} />
      </section>
    );
  }
  const f = state.data;
  return (
    <section className="page">
      {crumb}
      <PageHeader title="Fetching from Mailchimp" about="Nothing is imported until you've seen the preview and confirmed it." />
      {f.status === "failed" ? (
        <div className="notice notice-error" role="alert">
          <p>The fetch stopped{f.error ? `: ${f.error.message}` : "."}</p>
          <p className="hint">
            <Link to="/contacts/import/mailchimp">Try again</Link>, or upload Mailchimp's export instead.
          </p>
        </div>
      ) : (
        <FetchSteps f={f} />
      )}
    </section>
  );
}

function FetchSteps({ f }: { f: Fetch }) {
  if (f.status === "queued" || !f.step) {
    return (
      <div className="panel" role="status">
        <p>{f.status === "done" ? "Done. Opening the preview…" : "Waiting to start…"}</p>
      </div>
    );
  }
  const step = fetchSteps[f.step] ?? "Reading your account";
  return (
    <div className="panel stack" role="status">
      {f.step === "members" && f.members_total > 0 ? (
        <label className="field">
          <span>
            {step}
            {f.audience ? ` of ${f.audience}` : ""}: {f.members_read.toLocaleString()} of {f.members_total.toLocaleString()}
          </span>
          <progress max={f.members_total} value={f.members_read} />
        </label>
      ) : (
        <p>{step}…</p>
      )}
      <p className="muted small">You can leave this page; the fetch carries on.</p>
    </div>
  );
}

/** /contacts/import/mailchimp/:id: an upload's preview and confirm; once confirmed, its progress and what's next. */
export function MailchimpBundlePage() {
  const api = useApi();
  const { id = "" } = useParams();
  const load = useCallback(
    () =>
      attempt(() => api.GET("/v1/imports/mailchimp/bundles/{bundle_id}", { params: { path: { bundle_id: id } } })),
    [api, id],
  );
  const { state, reload, replace } = useLoad(load);
  const emailsRunning =
    state.status === "ok" && (state.data.emails.status === "queued" || state.data.emails.status === "running");
  useEffect(() => {
    if (!emailsRunning) return;
    const t = setTimeout(reload, importPollMs);
    return () => clearTimeout(t);
  }, [emailsRunning, reload, state]);

  const crumb = (
    <p className="crumb">
      <Link to="/contacts">Contacts</Link> / <Link to="/contacts/import/mailchimp">Import from Mailchimp</Link>
    </p>
  );
  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") {
    return (
      <section className="page">
        {crumb}
        <PageHeader title="Mailchimp import" />
        <LoadFailed failure={state.failure} retry={reload} />
      </section>
    );
  }
  const b = state.data;
  const fetched = b.preview?.source === "connected_account";
  return (
    <section className="page">
      {crumb}
      <PageHeader
        title={fetched ? "From your Mailchimp account" : (b.file_name ?? "Mailchimp export")}
        about={
          b.status === "unconfirmed"
            ? `Nothing is imported yet. This preview is kept until ${formatDate(b.expires_at)}.`
            : `Imported ${formatDate(b.confirmed_at)}.`
        }
      >
        <Badge tone={b.status === "confirmed" ? "good" : "info"}>
          {b.status === "confirmed" ? "Imported" : "Preview"}
        </Badge>
      </PageHeader>
      {b.status === "unconfirmed" ? (
        <>
          {b.preview ? <PreviewView preview={b.preview} /> : <p>mailgoose couldn't read a preview of this export.</p>}
          {b.preview && <ConfirmBundle bundle={b} onConfirmed={replace} />}
        </>
      ) : (
        <Imported bundle={b} />
      )}
    </section>
  );
}

export function PreviewView({ preview }: { preview: Preview }) {
  const c = preview.contacts;
  return (
    <>
      <h2>Audiences</h2>
      <ul className="plain">
        {preview.audiences.map((a) => (
          <li key={a.name}>
            {a.name}: {plural(a.members, "member")}
          </li>
        ))}
      </ul>
      {c && <ContactsView c={c} segmentTags={preview.segment_tags ?? []} />}
      <EmailsView preview={preview} />
      {preview.ignored_files.some((f) => f.reason !== "too_many_templates") && (
        <p className="muted small">
          {plural(
            preview.ignored_files.filter((f) => f.reason !== "too_many_templates").length,
            "file in the export isn't",
            "files in the export aren't",
          )}{" "}
          imported, such as Mailchimp's reports.
        </p>
      )}
    </>
  );
}

function ContactsView({ c, segmentTags }: { c: ContactsPreview; segmentTags: NonNullable<Preview["segment_tags"]> }) {
  const skipped = c.skipped.invalid_email + c.skipped.invalid_row + c.skipped.suppressed + c.skipped.not_subscribed + c.skipped.archived;
  return (
    <>
      <h2>Contacts</h2>
      <dl className="stats">
        <div>
          <dt>Subscribed</dt>
          <dd>{c.new.subscribed.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Unconfirmed</dt>
          <dd>{c.new.unconfirmed.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Unsubscribed</dt>
          <dd>{c.new.unsubscribed.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Bounced</dt>
          <dd>{c.new.bounced.toLocaleString()}</dd>
        </div>
      </dl>
      <ul className="plain">
        <li>
          Subscribed contacts get your campaigns. Unconfirmed ones were pending in Mailchimp and don't, until they
          confirm. Unsubscribed and bounced ones come over so they're never emailed.
        </li>
        {c.existing_tagged > 0 && (
          <li>
            {plural(c.existing_tagged, "address is", "addresses are")} already your contacts and only gain tags
            {c.merge_fields.length > 0 ? " and the field values you bring" : ""}
            {c.existing_suppressed > 0
              ? `; ${c.existing_suppressed.toLocaleString()} of them come off your list, as Mailchimp has them unsubscribed or cleaned`
              : ""}
            .
          </li>
        )}
        {skipped > 0 && (
          <li>
            {plural(skipped, "row is", "rows are")} skipped: not email addresses, unreadable, already unsubscribed
            here, or non-subscribed and archived Mailchimp members.
          </li>
        )}
        {c.merge_suppressed > 0 && (
          <li>
            {plural(c.merge_suppressed, "address is", "addresses are")} in more than one audience and unsubscribed or
            cleaned in one, so {c.merge_suppressed === 1 ? "it comes" : "they come"} over that way
            {c.merge_suppressed_examples.length > 0 ? `, such as ${c.merge_suppressed_examples.slice(0, 3).join(", ")}` : ""}.
          </li>
        )}
        {c.names_cut > 0 && <li>{plural(c.names_cut, "name is", "names are")} cut to fit.</li>}
      </ul>

      <h3>Tags</h3>
      {c.tags_to_create.length > 0 ? (
        <p>New tags: {c.tags_to_create.join(", ")}.</p>
      ) : (
        <p className="muted">No new tags.</p>
      )}
      {segmentTags.length > 0 && (
        <ul className="plain">
          {segmentTags.map((s) => (
            <li key={`${s.audience}/${s.tag}`}>
              {s.tag} holds the {plural(s.members, "member")} of that saved segment in {s.audience}.
            </li>
          ))}
        </ul>
      )}
      {c.renamed_tags.length > 0 && (
        <ul className="plain">
          {c.renamed_tags.map((t) => (
            <li key={t.from}>
              {t.from} becomes {t.to}.
            </li>
          ))}
        </ul>
      )}

      {c.dropped_fields.length > 0 && (
        <>
          <h3>Mailchimp's own details</h3>
          <p>
            mailgoose keeps each contact's email, name, tags, opt-in details and the merge fields you choose below.
            These details Mailchimp keeps for itself aren't kept: {c.dropped_fields.join(", ")}.
          </p>
        </>
      )}

      {(c.plan.upgrade || c.waits || c.holds.length > 0) && (
        <>
          <h3>Plan and review</h3>
          {c.plan.upgrade && (
            <p>
              {plural(c.plan.contacts, "contact")} needs the paid plan: {dollars(c.plan.month_cents)} a month, or{" "}
              {dollars(c.plan.year_cents)} a year. <Link to="/billing">See billing</Link>
            </p>
          )}
          {c.waits && (
            <div className="notice notice-warn" role="status">
              <p>
                The subscribed contacts would take your account past its limit, so they wait for the paid plan.
                Unsubscribed and bounced ones come in now.
              </p>
            </div>
          )}
          {c.holds.map((h) => (
            <div key={h} className="notice notice-warn" role="status">
              <p>Held for review: {holds[h] ?? h}.</p>
              <p className="hint">They're on your list in the meantime, but don't get campaigns until mailgoose approves the import.</p>
            </div>
          ))}
        </>
      )}
    </>
  );
}

function EmailsView({ preview }: { preview: Preview }) {
  const templates = preview.emails.filter((e) => e.from === "template").length;
  const campaigns = preview.emails.length - templates;
  return (
    <>
      <h2>Emails</h2>
      {preview.emails.length === 0 ? (
        <p className="muted">No emails come over.</p>
      ) : (
        <>
          <p>
            {plural(templates, "template")} and the newest {plural(campaigns, "campaign")}
            {preview.campaigns_available > campaigns ? ` of ${preview.campaigns_available.toLocaleString()}` : ""} become
            HTML emails you can edit.
          </p>
          <EmailTable emails={preview.emails} />
          {preview.emails.some((e) => (e.merge_tags ?? []).length > 0) && (
            <p className="muted small">
              Mailchimp merge tags become your contact fields' tags, with the merge fields going where the contact
              fields below suggest; if you change those, the emails follow what you choose. A merge tag with no
              contact field is taken out. To keep one, add the field its note names on the{" "}
              <Link to="/fields">Fields page</Link> before you import.
            </p>
          )}
        </>
      )}
      {(preview.templates_not_fetched ?? 0) > 0 && (
        <p className="muted small">
          Mailchimp doesn't share templates through a connection, so your{" "}
          {plural(preview.templates_not_fetched ?? 0, "template")} aren't included. Upload the account export to bring
          them.
        </p>
      )}
    </>
  );
}

function EmailTable({ emails }: { emails: Email[] }) {
  return (
    <table className="table">
      <thead>
        <tr>
          <th>Email</th>
          <th>From</th>
          <th>Notes</th>
        </tr>
      </thead>
      <tbody>
        {emails.map((e) => {
          // MG-134: the merge tags that became a contact field's tag; the
          // ones taken out have a warning each.
          const converted = (e.merge_tags ?? []).flatMap((t) => (t.field ? [{ ...t, field: t.field }] : []));
          return (
            <tr key={e.file}>
              <td>
                {e.email_id ? <Link to={`/emails/${e.email_id}`}>{e.name}</Link> : e.name}
                {e.subject && <div className="muted small">{e.subject}</div>}
              </td>
              <td>{e.from === "template" ? "Template" : "Campaign"}</td>
              <td>
                {e.errors.length + e.warnings.length + converted.length === 0 ? (
                  <span className="muted">None</span>
                ) : (
                  <ul className="plain">
                    {e.errors.map((n, i) => (
                      <li key={`e${i}`}>
                        <Badge tone="bad">Fix</Badge> {n.message}
                      </li>
                    ))}
                    {converted.map((t) => (
                      <li key={`t${t.tag}`}>
                        <code>{t.tag}</code> became <code>{`{{${t.field.key}}}`}</code>, the tag of your{" "}
                        {t.new_field ? "new " : ""}field {t.field.name}, with no fallback.
                      </li>
                    ))}
                    {e.warnings.map((n, i) => (
                      <li key={`w${i}`}>{n.message}</li>
                    ))}
                  </ul>
                )}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

export function ConfirmBundle({ bundle, onConfirmed }: { bundle: Bundle; onConfirmed: (b: Bundle) => void }) {
  const api = useApi();
  const available = bundle.preview?.campaigns_available ?? 0;
  const [permission, setPermission] = useState(false);
  // As typed; read as a count from 1 (the API's least) to what the export has.
  const [campaignsText, setCampaignsText] = useState(String(Math.max(1, Math.min(bundle.campaigns, available))));
  const campaigns = Math.max(1, Math.min(maxCampaigns, available, Math.floor(Number(campaignsText)) || 1));
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const audiences = bundle.preview?.audiences.filter((a) => a.members > 0) ?? [];
  // Every audience's groups, once each; a ticked one goes on the form of
  // each audience that has it.
  const groups = [...new Set(audiences.flatMap((a) => a.groups ?? []))];
  const [formTags, setFormTags] = useState<string[]>([]);
  const contacts = bundle.preview?.contacts;
  const mergeFields = contacts?.merge_fields ?? [];
  const [mergeMatching, setMergeMatching] = useState(() => previewMergeMatching(mergeFields));
  const contactFields = useContactFields();
  // The account's fields, with any the preview matched that the list
  // couldn't load, so each can be shown and chosen.
  const loadedFields: ContactField[] = contactFields.state.status === "ok" ? contactFields.state.data : [];
  const fieldList: { key: string; name: string }[] = [...loadedFields];
  for (const f of mergeFields) {
    if (f.custom_field && !fieldList.some((x) => x.key === f.custom_field?.key)) fieldList.push(f.custom_field);
  }
  const fieldLimit = contacts?.fields.limit ?? 0;
  const existingFields = contactFields.state.status === "ok" ? loadedFields.length : (contacts?.fields.existing ?? 0);
  const newCount = Object.values(mergeMatching).filter((t) => "name" in t).length;
  const canMake = fieldLimit - existingFields - newCount;
  const unnamed = Object.values(mergeMatching).some((t) => "name" in t && !t.name.trim());

  /** Changes what one merge field becomes: a contact field ("field:key"), a new field ("new") or nothing (""). */
  function rematch(f: MergeField, value: string) {
    let target: FieldTarget | undefined;
    if (value.startsWith("field:")) target = { key: value.slice("field:".length) };
    // A merge field whose own name can't be a field's waits for a name.
    else if (value === "new") target = { name: f.skip_reason && f.skip_reason !== "field_limit" ? "" : f.name };
    setMergeMatching((m) => matchMergeField(m, f.name, target));
  }

  async function confirm(e: FormEvent) {
    e.preventDefault();
    if (!permission || unnamed) return;
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() =>
      api.POST("/v1/imports/mailchimp/bundles/{bundle_id}/confirm", {
        params: { path: { bundle_id: bundle.id } },
        body: {
          permission_confirmed: true,
          campaigns,
          ...(formTags.length > 0 && { form_tags: formTags }),
          ...(mergeFields.length > 0 && mergeFieldRequest(mergeFields, mergeMatching, fieldList)),
        },
      }),
    );
    setBusy(false);
    if (!res.ok) setFailure(res.failure);
    else if (res.data) onConfirmed(res.data);
  }

  return (
    <form onSubmit={(e) => void confirm(e)} className="panel stack" aria-labelledby="mailchimp-confirm">
      <h2 id="mailchimp-confirm">Import it</h2>
      {available > 0 && (
        <label className="field">
          <span>Newest campaigns to bring as emails (of {available.toLocaleString()})</span>
          <input
            type="number"
            min={1}
            max={Math.min(maxCampaigns, available)}
            value={campaignsText}
            onChange={(e) => setCampaignsText(e.target.value)}
          />
        </label>
      )}
      {mergeFields.length > 0 && (
        <fieldset className="stack">
          <legend>Contact fields</legend>
          <p className="muted small">
            Choose what each Mailchimp merge field becomes: one of your fields, a new field, or nothing. Your plan allows{" "}
            {plural(fieldLimit, "contact field")}, and you have {existingFields.toLocaleString()}.
          </p>
          {contacts && contacts.fields.left_out > 0 && (
            <p className="notice notice-warn" role="status">
              {plural(contacts.fields.left_out, "merge field doesn't", "merge fields don't")} fit under that limit, so{" "}
              {contacts.fields.left_out === 1 ? "it's" : "they're"} left out for now. Choose which come over as new
              fields, or put them into fields you have.
            </p>
          )}
          <ul className="column-match">
            {mergeFields.map((f, i) => {
              const target = mergeMatching[f.name];
              const value = target ? ("key" in target ? `field:${target.key}` : "new") : "";
              const isNew = target !== undefined && "name" in target;
              const ownName = !f.skip_reason || f.skip_reason === "field_limit";
              const id = `merge-${i}`;
              return (
                <li key={f.name}>
                  <div className="column-match-about">
                    <label htmlFor={id}>{f.name}</label>
                    <span className="muted small" id={`${id}-about`}>
                      {plural(f.members, "contact")}
                      {f.samples.length > 0 ? `: ${f.samples.join(", ")}` : ""}
                      {audiences.length > 1 ? ` (${f.audiences.join(", ")})` : ""}
                      {!target && f.skip_reason ? `. Left out: ${mergeSkips[f.skip_reason]}` : ""}
                    </span>
                  </div>
                  <div className="column-match-choice">
                    <div className="field">
                      <select id={id} value={value} onChange={(e) => rematch(f, e.target.value)} aria-describedby={`${id}-about`}>
                        <option value="">Don't import</option>
                        {fieldList.length > 0 && (
                          <optgroup label="Your fields">
                            {fieldList.map((x) => (
                              <option key={x.key} value={`field:${x.key}`}>
                                {x.name}
                              </option>
                            ))}
                          </optgroup>
                        )}
                        <option value="new" disabled={!isNew && canMake <= 0}>
                          {ownName ? `New field: ${f.name}` : "New field…"}
                        </option>
                      </select>
                    </div>
                    {isNew && !ownName && (
                      <label className="field">
                        <span>Name for the new field</span>
                        <input
                          type="text"
                          value={target.name}
                          maxLength={maxFieldName}
                          onChange={(e) => setMergeMatching((m) => matchMergeField(m, f.name, { name: e.target.value }))}
                          autoComplete="off"
                          required
                        />
                      </label>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
          {canMake <= 0 && (
            <p className="muted small" role="status">
              Your plan allows {plural(fieldLimit, "contact field")}, and {newCount > 0 ? "with the new ones here they're" : "they're"}{" "}
              all used, so no more can be made. A merge field can still go into one of your existing fields.
            </p>
          )}
          {canMake < 0 && (
            <p className="notice notice-warn" role="alert">
              That's {plural(-canMake, "new field")} too many. Leave some merge fields out, or put them into fields you have.
            </p>
          )}
        </fieldset>
      )}
      {audiences.length > 0 && (
        <fieldset className="stack">
          <legend>Signup forms</legend>
          <p className="muted small">
            {audiences.length === 1 ? "The audience gets" : "Each audience gets"} a signup form in its name, to replace
            Mailchimp's: email, first and last name, and an email to confirm. It's a draft, taking no signups until you
            check it and save it.
          </p>
          {audiences.some((a) => a.double_optin !== undefined) && (
            <ul className="plain small">
              {audiences
                .filter((a) => a.double_optin !== undefined)
                .map((a) => (
                  <li key={a.name}>
                    {a.name}: Mailchimp {a.double_optin ? "asked" : "didn't ask"} signups to confirm by email. mailgoose's
                    form does.
                  </li>
                ))}
            </ul>
          )}
          {groups.length > 0 && (
            <>
              <p className="small">Tag people who sign up with these groups:</p>
              {groups.map((g) => (
                <label className="check" key={g}>
                  <input
                    type="checkbox"
                    checked={formTags.includes(g)}
                    onChange={(e) => setFormTags((t) => (e.target.checked ? [...t, g] : t.filter((x) => x !== g)))}
                  />
                  <span>{g}</span>
                </label>
              ))}
            </>
          )}
        </fieldset>
      )}
      <label className="check">
        <input type="checkbox" checked={permission} onChange={(e) => setPermission(e.target.checked)} />
        <span>Everyone in this file has given me permission to email them.</span>
      </label>
      <p className="muted small">mailgoose needs this to import them. Subscribed contacts get no confirmation email.</p>
      <FailureNotice failure={failure} />
      <div>
        <button type="submit" className="button primary" disabled={!permission || busy || unnamed || canMake < 0}>
          {busy ? "Starting…" : "Import"}
        </button>
      </div>
    </form>
  );
}

function Imported({ bundle }: { bundle: Bundle }) {
  const emails = bundle.emails;
  const emailsRunning = emails.status === "queued" || emails.status === "running";
  const withErrors = emails.made.filter((e) => e.errors.length > 0 && e.email_id);
  return (
    <>
      <h2>Contacts</h2>
      {bundle.import_id ? <ImportProgress importID={bundle.import_id} /> : <p className="muted">No contacts come over.</p>}

      <h2>Emails</h2>
      <div className="panel stack" role="status">
        {emailsRunning ? (
          <label className="field">
            <span>
              {emails.made.length.toLocaleString()} of {emails.total.toLocaleString()} emails made
            </span>
            <progress max={Math.max(emails.total, 1)} value={emails.made.length} />
          </label>
        ) : emails.status === "failed" ? (
          <p>Making the emails stopped{emails.error ? `: ${emails.error}` : "."}</p>
        ) : (
          <p>{plural(emails.made.length, "email")} made.</p>
        )}
      </div>
      {emails.made.length > 0 && <EmailTable emails={emails.made} />}

      {bundle.forms.length > 0 && (
        <>
          <h2>Signup forms</h2>
          <ul className="plain">
            {bundle.forms.map((f) => (
              <li key={f.id}>
                <Link to={`/forms/${f.id}`}>{f.name}</Link>{" "}
                {f.new ? <Badge tone="warn">Draft</Badge> : <span className="muted small">made by an earlier import</span>}
              </li>
            ))}
          </ul>
        </>
      )}

      <h2>What's next</h2>
      <ol className="stack">
        <li>
          <Link to="/domains">Verify your sending domain</Link>, so your campaigns come from your own address.
        </li>
        <li>
          {bundle.forms.some((f) => f.new) ? (
            <>
              Check and save your new signup {bundle.forms.filter((f) => f.new).length === 1 ? "form" : "forms"} above, so{" "}
              {bundle.forms.filter((f) => f.new).length === 1 ? "it takes" : "they take"} signups, then put the embed code
              on your site in place of Mailchimp's.
            </>
          ) : (
            <>
              <Link to="/forms">Set up your signup forms</Link> and put their embed code on your site in place of
              Mailchimp's.
            </>
          )}
        </li>
        {withErrors.length > 0 && (
          <li>
            Fix the {withErrors.length === 1 ? "email" : "emails"} with errors:{" "}
            {withErrors.map((e, i) => (
              <span key={e.email_id}>
                {i > 0 && ", "}
                <Link to={`/emails/${e.email_id}`}>{e.name}</Link>
              </span>
            ))}
            .
          </li>
        )}
        <li>
          <Link to="/campaigns/new">Send a test</Link> of a campaign to yourself before you send it to everyone.
        </li>
      </ol>
    </>
  );
}

function ImportProgress({ importID }: { importID: string }) {
  const api = useApi();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/imports/{import_id}", { params: { path: { import_id: importID } } })),
    [api, importID],
  );
  const { state, reload } = useLoad(load);
  const running = state.status === "ok" && (state.data.status === "queued" || state.data.status === "running");
  useEffect(() => {
    if (!running) return;
    const t = setTimeout(reload, importPollMs);
    return () => clearTimeout(t);
  }, [running, reload, state]);

  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") return <LoadFailed failure={state.failure} retry={reload} />;
  const im = state.data;
  return (
    <div className="panel stack" role="status">
      {running ? (
        <label className="field">
          <span>
            {im.processed_rows.toLocaleString()} of {im.total_rows.toLocaleString()} contacts
          </span>
          <progress max={Math.max(im.total_rows, 1)} value={im.processed_rows} />
        </label>
      ) : (
        <p>
          {im.status === "done"
            ? `${plural(im.imported, "contact")} added, ${im.skipped.toLocaleString()} skipped.`
            : im.status === "waiting"
              ? "The subscribed contacts wait for the paid plan."
              : `The import ${im.status === "failed" ? "stopped" : "was cancelled"}.`}
        </p>
      )}
      {im.review === "held" && (
        <p className="hint">{im.hold_message ?? "mailgoose checks this import before these contacts get campaigns."}</p>
      )}
      <p>
        <Link to={`/contacts/imports/${im.id}`}>See the import report</Link>
        {im.skipped > 0 && !running ? ", with the skipped rows to download" : ""}
      </p>
    </div>
  );
}
