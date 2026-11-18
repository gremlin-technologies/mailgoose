import { useCallback, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { Badge, CopyButton, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad, type Tone } from "../components";

type Domain = components["schemas"]["Domain"];
type DnsRecord = components["schemas"]["DnsRecord"];

const domainStatus: Record<Domain["status"], { label: string; tone: Tone }> = {
  pending: { label: "Waiting for DNS", tone: "warn" },
  verified: { label: "Verified", tone: "good" },
  failed: { label: "Not verified", tone: "bad" },
};

function DomainBadge({ status }: { status: Domain["status"] }) {
  const s = domainStatus[status];
  return <Badge tone={s.tone}>{s.label}</Badge>;
}

/** The account's sending domains, and a form to add one. */
export function Domains() {
  const api = useApi();
  const navigate = useNavigate();
  const load = useCallback(() => attempt(() => api.GET("/v1/domains", { params: { query: { limit: 100 } } })), [api]);
  const { state, reload } = useLoad(load);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function add(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/domains", { body: { name: name.trim() } }));
    setBusy(false);
    if (!res.ok) setFailure(res.failure);
    else if (res.data) navigate(`/domains/${res.data.id}`);
  }

  return (
    <section className="page">
      <PageHeader
        title="Domains"
        about="Verify the domain your emails come from. Mail from a verified domain reaches inboxes, not spam."
      />
      <form onSubmit={add} className="panel row-form">
        <label className="field grow">
          <span>Add a domain</span>
          <input
            name="domain"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="news.example.com"
            autoComplete="off"
            spellCheck={false}
            maxLength={253}
            required
          />
        </label>
        <button type="submit" className="button primary" disabled={busy}>
          {busy ? "Adding…" : "Add domain"}
        </button>
      </form>
      <FailureNotice failure={failure} />

      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.data.length === 0 ? (
          <p className="empty">No domains yet. Add the domain you send from, and we'll show you the DNS records it needs.</p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Domain</th>
                <th>Status</th>
                <th>Added</th>
              </tr>
            </thead>
            <tbody>
              {state.data.data.map((d) => (
                <tr key={d.id}>
                  <td>
                    <Link to={`/domains/${d.id}`}>{d.name}</Link>
                  </td>
                  <td>
                    <DomainBadge status={d.status} />
                  </td>
                  <td className="muted">{formatDate(d.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ))}
    </section>
  );
}

const purposes: Record<DnsRecord["purpose"], { label: string; about: string }> = {
  dkim: { label: "DKIM", about: "Signs your emails so inboxes know they're really from you." },
  mail_from_mx: { label: "Bounce MX", about: "Lets bounces come back to mailgoose, which keeps your list clean." },
  mail_from_spf: { label: "Bounce SPF", about: "Allows mailgoose to send for your bounce subdomain." },
  dmarc: { label: "DMARC", about: "Suggested, not required: tells inboxes what to do with mail that fails the checks." },
};

const recordStatus: Record<DnsRecord["status"], { label: string; tone: Tone }> = {
  found: { label: "Found", tone: "good" },
  missing: { label: "Missing", tone: "warn" },
  wrong: { label: "Wrong value", tone: "bad" },
  unchecked: { label: "Not checked yet", tone: "neutral" },
};

/** One domain: the DNS records to add, each one's status, and a check now button. */
export function DomainDetail() {
  const api = useApi();
  const navigate = useNavigate();
  const { id = "" } = useParams();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/domains/{domain_id}", { params: { path: { domain_id: id } } })),
    [api, id],
  );
  const { state, reload, replace } = useLoad(load);
  const [checking, setChecking] = useState(false);
  const [changed, setChanged] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [confirmDelete, setConfirmDelete] = useState(false);

  async function check() {
    setChecking(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/domains/{domain_id}/verify", { params: { path: { domain_id: id } } }));
    setChecking(false);
    if (!res.ok) setFailure(res.failure);
    else if (res.data) {
      replace(res.data);
      setChanged(res.data.records_changed === true);
    }
  }

  async function remove() {
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/domains/{domain_id}", { params: { path: { domain_id: id } } }));
    if (!res.ok) setFailure(res.failure);
    else navigate("/domains");
  }

  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") {
    return (
      <section className="page">
        <PageHeader title="Domain" />
        <LoadFailed failure={state.failure} retry={reload} />
      </section>
    );
  }
  const d = state.data;
  const required = d.records.filter((r) => r.required);
  const optional = d.records.filter((r) => !r.required);

  return (
    <section className="page">
      <p className="crumb">
        <Link to="/domains">Domains</Link>
      </p>
      <PageHeader title={d.name} about={<DomainSummary domain={d} />}>
        <DomainBadge status={d.status} />
      </PageHeader>

      <div className="row">
        <button type="button" className="button primary" onClick={check} disabled={checking}>
          {checking ? "Checking…" : "Check DNS now"}
        </button>
        {d.last_checked_at && <span className="muted small">Last checked {formatDate(d.last_checked_at)}</span>}
      </div>
      {changed && (
        <div className="notice notice-warn" role="status">
          <p>The records below changed since you last saw them. Update them at your DNS provider.</p>
        </div>
      )}
      <FailureNotice failure={failure} />

      <h2>Records to add</h2>
      <p className="muted">
        Add these at the company that runs your domain's DNS. Changes can take a few minutes to an hour to show up.
      </p>
      <RecordList records={required} />
      {optional.length > 0 && (
        <>
          <h2>Suggested</h2>
          <RecordList records={optional} />
        </>
      )}

      <div className="panel danger-zone">
        <h2>Remove this domain</h2>
        <p className="muted">Campaigns can't send from it once it's removed. Adding it again means new DKIM records.</p>
        {confirmDelete ? (
          <div className="row">
            <button type="button" className="button danger" onClick={remove}>
              Remove {d.name}
            </button>
            <button type="button" className="button" onClick={() => setConfirmDelete(false)}>
              Cancel
            </button>
          </div>
        ) : (
          <div>
            <button type="button" className="button" onClick={() => setConfirmDelete(true)}>
              Remove domain
            </button>
          </div>
        )}
      </div>
    </section>
  );
}

function DomainSummary({ domain: d }: { domain: Domain }) {
  switch (d.status) {
    case "verified":
      return <>Verified {formatDate(d.verified_at)}. You can send from any address at {d.name}.</>;
    case "failed":
      return <>We stopped checking on {formatDate(d.failed_at)}. Fix the records below, then check again.</>;
    case "pending":
      return <>Add the records below. We check on our own until {formatDate(d.check_until)}, or you can check now.</>;
  }
}

function RecordList({ records }: { records: DnsRecord[] }) {
  return (
    <ul className="records">
      {records.map((r) => {
        const p = purposes[r.purpose];
        const s = recordStatus[r.status];
        return (
          <li key={`${r.type} ${r.name}`} className="record">
            <div className="record-head">
              <strong>{p.label}</strong>
              <span className="muted small">{r.type}</span>
              <Badge tone={s.tone}>{s.label}</Badge>
            </div>
            <p className="muted small">{p.about}</p>
            <dl className="record-fields">
              <dt>Name</dt>
              <dd>
                <code>{r.name}</code>
                <CopyButton value={r.name} label={`${p.label} ${r.type} name`} />
              </dd>
              <dt>Value</dt>
              <dd>
                <code>{r.value}</code>
                <CopyButton value={r.value} label={`${p.label} ${r.type} value`} />
              </dd>
              {r.priority !== null && (
                <>
                  <dt>Priority</dt>
                  <dd>
                    <code>{r.priority}</code>
                  </dd>
                </>
              )}
            </dl>
            {r.status === "wrong" && r.found.length > 0 && (
              <p className="small">
                Found instead: <code>{r.found.join(", ")}</code>
              </p>
            )}
            {r.detail && <p className="small">{r.detail}</p>}
          </li>
        );
      })}
    </ul>
  );
}
