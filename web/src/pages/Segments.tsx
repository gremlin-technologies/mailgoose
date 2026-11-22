// Saved segments (MG-116): the list, with each one's count and any
// condition it can't use, renaming and deleting, and a page that edits a
// segment's conditions with the same builder as the contacts page.
import { useCallback, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure, type Result } from "../api/result";
import { listOf, plural } from "../campaigns/campaign";
import { Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { ContactCount, FilterBuilder, useTrackedCampaigns, type CampaignOption } from "../segments/FilterBuilder";
import {
  conditionsOf,
  conditionsToParams,
  draftFrom,
  emptyDraft,
  isEmpty,
  problemWords,
  rowKinds,
  type Conditions,
  type Draft,
  type Segment,
} from "../segments/conditions";
import { contactStatus } from "./Contacts";
import { useTags, type Tag } from "./Tags";

type ApiClient = ReturnType<typeof useApi>;

/** Every segment on the account; an account has at most 100, one page. */
export async function loadAllSegments(api: ApiClient): Promise<Result<Segment[]>> {
  const res = await attempt(() => api.GET("/v1/segments", { params: { query: { limit: 100 } } }));
  if (!res.ok) return res;
  return { ok: true, data: res.data?.data ?? [] };
}

export function useSegments() {
  const api = useApi();
  const load = useCallback(() => loadAllSegments(api), [api]);
  return useLoad(load);
}

/** The contacts page showing a segment's contacts. */
export function contactsLink(c: Conditions): string {
  const qs = conditionsToParams(c).toString();
  return `/contacts${qs ? `?${qs}` : ""}`;
}

/** A segment's conditions in a line: "Subscribed or unconfirmed; tagged VIP; opened May news". */
export function describe(c: Conditions, tags: Tag[], campaigns: CampaignOption[]): string {
  const tagName = (id: string) => tags.find((t) => t.id === id)?.name ?? "a deleted tag";
  const campaignName = (id: string) => campaigns.find((x) => x.id === id)?.name ?? "a campaign";
  const parts: string[] = [];
  if (c.statuses?.length) parts.push(listOf(c.statuses.map((s) => contactStatus[s].label)));
  if (c.tag_ids?.length) parts.push(`tagged ${listOf(c.tag_ids.map(tagName), "and")}`);
  const words: Record<string, string> = {
    opened: "opened",
    not_opened: "didn't open",
    clicked: "clicked in",
    not_clicked: "didn't click in",
  };
  for (const k of ["opened", "not_opened", "clicked", "not_clicked"] as const) {
    if (c[k]?.length) parts.push(`${words[k]} ${listOf(c[k].map(campaignName), "and")}`);
  }
  if (c.opened_within_days) parts.push(`opened any in the last ${plural(c.opened_within_days, "day")}`);
  if (c.clicked_within_days) parts.push(`clicked any in the last ${plural(c.clicked_within_days, "day")}`);
  if (c.never_opened) parts.push("never opened one");
  if (c.never_clicked) parts.push("never clicked one");
  const line = parts.join("; ");
  return line ? line[0].toUpperCase() + line.slice(1) : "No conditions";
}

/** A segment's problems in words: "Opened May news: its campaign was deleted." */
export function problemLines(s: Segment, tags: Tag[], campaigns: CampaignOption[]): string[] {
  return s.problems.map((p) => {
    const what =
      p.condition === "tag_ids"
        ? `The tag ${tags.find((t) => t.id === p.id)?.name ?? ""}`.trim()
        : `${rowKinds.find((k) => k.kind === p.condition)?.label ?? "A condition"} (${campaigns.find((c) => c.id === p.id)?.name ?? "a campaign"})`;
    return `${what}: ${problemWords[p.code]}.`;
  });
}

export function Segments() {
  const { state, reload } = useSegments();
  const tags = useTags();
  const campaigns = useTrackedCampaigns();
  const tagList = tags.state.status === "ok" ? tags.state.data : [];
  const campaignList = campaigns.state.status === "ok" ? campaigns.state.data : [];

  return (
    <section className="page">
      <PageHeader
        title="Segments"
        about="Saved filters of your contacts, worked out each time you use one, so they stay current. Send a campaign to one."
      >
        <Link to="/segments/new" className="button primary">
          New segment
        </Link>
      </PageHeader>
      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.length === 0 ? (
          <p className="empty">
            No segments yet. Make one here, or filter your <Link to="/contacts">contacts</Link> and save the filters as a segment.
          </p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Segment</th>
                <th>Contacts</th>
                <th>Conditions</th>
                <th>Changed</th>
                <th className="actions">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {state.data.map((s) => (
                <SegmentRow key={s.id} segment={s} tags={tagList} campaigns={campaignList} changed={reload} />
              ))}
            </tbody>
          </table>
        ))}
    </section>
  );
}

function SegmentRow({
  segment,
  tags,
  campaigns,
  changed,
}: {
  segment: Segment;
  tags: Tag[];
  campaigns: CampaignOption[];
  changed: () => void;
}) {
  const api = useApi();
  const [mode, setMode] = useState<"view" | "rename" | "delete">("view");
  const [name, setName] = useState(segment.name);
  const [failure, setFailure] = useState<Failure>();
  const path = { segment_id: segment.id };

  async function rename(e: FormEvent) {
    e.preventDefault();
    setFailure(undefined);
    const res = await attempt(() => api.PATCH("/v1/segments/{segment_id}", { params: { path }, body: { name: name.trim() } }));
    if (!res.ok) return setFailure(res.failure);
    setMode("view");
    changed();
  }

  async function remove() {
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/segments/{segment_id}", { params: { path } }));
    if (!res.ok) {
      setMode("view");
      return setFailure(res.failure);
    }
    changed();
  }

  return (
    <tr>
      <td>
        {mode === "rename" ? (
          <form onSubmit={rename} className="row">
            <label className="field grow">
              <span className="visually-hidden">New name for {segment.name}</span>
              <input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} required autoFocus />
            </label>
            <button type="submit" className="button small primary">
              Save
            </button>
            <button type="button" className="button small" onClick={() => setMode("view")}>
              Cancel
            </button>
          </form>
        ) : (
          <Link to={`/segments/${segment.id}`}>{segment.name}</Link>
        )}
        <FailureNotice failure={failure} />
      </td>
      <td>
        <SegmentCount segment={segment} />
      </td>
      <td className="small">
        {describe(segment.conditions, tags, campaigns)}
        {!segment.valid && (
          <ul className="problems">
            {problemLines(segment, tags, campaigns).map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        )}
      </td>
      <td className="muted">{formatDate(segment.updated_at)}</td>
      <td className="actions">
        {mode === "delete" ? (
          <div className="row end">
            <span className="small">Delete {segment.name}? Its contacts stay.</span>
            <button type="button" className="button small danger" onClick={remove}>
              Delete segment
            </button>
            <button type="button" className="button small" onClick={() => setMode("view")}>
              Cancel
            </button>
          </div>
        ) : (
          mode === "view" && (
            <div className="row end">
              <button type="button" className="button small" onClick={() => setMode("rename")} aria-label={`Rename ${segment.name}`}>
                Rename
              </button>
              <button type="button" className="button small" onClick={() => setMode("delete")} aria-label={`Delete ${segment.name}`}>
                Delete
              </button>
            </div>
          )
        )}
      </td>
    </tr>
  );
}

/** How many contacts a segment has now, linked to them; a segment with a problem shows that instead. */
function SegmentCount({ segment }: { segment: Segment }) {
  const api = useApi();
  const valid = segment.valid;
  const load = useCallback(
    () =>
      valid
        ? attempt(() =>
            api.GET("/v1/segments/{segment_id}/contacts", { params: { path: { segment_id: segment.id }, query: { limit: 1 } } }),
          )
        : Promise.resolve({ ok: true as const, data: { data: [], next_cursor: null, count: 0 } }),
    [api, segment.id, valid],
  );
  const { state } = useLoad(load);
  if (!valid) return <Badge tone="warn">Needs fixing</Badge>;
  if (state.status === "loading") return <span className="muted">…</span>;
  if (state.status === "failed") return <span className="muted">Couldn't count</span>;
  return (
    <Link to={contactsLink(segment.conditions)} aria-label={`${segment.name}: ${plural(state.data.count, "contact")}, see them`}>
      {state.data.count.toLocaleString()}
    </Link>
  );
}

/** /segments/new and /segments/:id: a segment's name and conditions. */
export function SegmentEditor() {
  const { id = "" } = useParams();
  const api = useApi();
  const creating = id === "new";
  const load = useCallback(async (): Promise<Result<{ segment?: Segment }>> => {
    if (creating) return { ok: true, data: {} };
    const res = await attempt(() => api.GET("/v1/segments/{segment_id}", { params: { path: { segment_id: id } } }));
    return res.ok ? { ok: true, data: { segment: res.data } } : res;
  }, [api, id, creating]);
  const loaded = useLoad(load);
  const tags = useTags();
  const campaigns = useTrackedCampaigns();

  if (loaded.state.status === "loading" || tags.state.status === "loading" || campaigns.state.status === "loading") return <Loading />;
  if (loaded.state.status === "failed") return <LoadFailed failure={loaded.state.failure} retry={loaded.reload} />;
  if (tags.state.status === "failed") return <LoadFailed failure={tags.state.failure} retry={tags.reload} />;
  if (campaigns.state.status === "failed") return <LoadFailed failure={campaigns.state.failure} retry={campaigns.reload} />;
  return (
    <SegmentForm
      key={loaded.state.data.segment?.id ?? "new"}
      segment={loaded.state.data.segment}
      tags={tags.state.data}
      campaigns={campaigns.state.data}
      onSaved={loaded.replace}
    />
  );
}

function SegmentForm({
  segment,
  tags,
  campaigns,
  onSaved,
}: {
  segment?: Segment;
  tags: Tag[];
  campaigns: CampaignOption[];
  onSaved: (data: { segment?: Segment }) => void;
}) {
  const api = useApi();
  const navigate = useNavigate();
  const [name, setName] = useState(segment?.name ?? "");
  const [draft, setDraft] = useState<Draft>(() => (segment ? draftFrom(segment.conditions) : emptyDraft));
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [problem, setProblem] = useState<string>();
  const [notice, setNotice] = useState("");
  const [deleting, setDeleting] = useState(false);
  const conditions = conditionsOf(draft);

  async function save(e: FormEvent) {
    e.preventDefault();
    setFailure(undefined);
    setNotice("");
    if (isEmpty(conditions)) return setProblem("Set at least one condition.");
    setProblem(undefined);
    setBusy(true);
    const body = { name: name.trim(), conditions };
    const res = segment
      ? await attempt(() => api.PATCH("/v1/segments/{segment_id}", { params: { path: { segment_id: segment.id } }, body }))
      : await attempt(() => api.POST("/v1/segments", { body }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    if (!segment && res.data) {
      navigate(`/segments/${res.data.id}`, { replace: true });
      return;
    }
    if (res.data) {
      onSaved({ segment: res.data });
      setNotice("Saved.");
    }
  }

  async function remove() {
    if (!segment) return;
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/segments/{segment_id}", { params: { path: { segment_id: segment.id } } }));
    setDeleting(false);
    if (!res.ok) return setFailure(res.failure);
    navigate("/segments");
  }

  const title = segment?.name ?? "New segment";
  return (
    <section className="page">
      <p className="crumb">
        <Link to="/segments">Segments</Link> / {title}
      </p>
      <PageHeader title={title} about={segment ? `Changed ${formatDate(segment.updated_at)}.` : "Name it and set its conditions."}>
        {segment && segment.valid && (
          <Link to={contactsLink(segment.conditions)} className="button">
            See its contacts
          </Link>
        )}
      </PageHeader>
      {segment && !segment.valid && (
        <div className="notice notice-warn" role="status">
          <p>This segment can't be used until its conditions are changed:</p>
          <ul>
            {problemLines(segment, tags, campaigns).map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </div>
      )}
      {notice && (
        <div className="notice notice-good" role="status">
          <p>{notice}</p>
        </div>
      )}
      <form className="panel stack" onSubmit={save} aria-label="Segment">
        <label className="field">
          <span>Name</span>
          <input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} required placeholder="Engaged subscribers" />
        </label>
        <FilterBuilder draft={draft} onChange={setDraft} tags={tags} campaigns={campaigns} />
        {isEmpty(conditions) ? (
          <p className="muted small">Set at least one condition.</p>
        ) : (
          <ContactCount conditions={conditions} />
        )}
        {problem && (
          <p className="field-error" role="alert">
            {problem}
          </p>
        )}
        <FailureNotice failure={failure} />
        <div className="row">
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? "Saving…" : segment ? "Save changes" : "Save segment"}
          </button>
          {segment &&
            (deleting ? (
              <>
                <span className="small">Delete {segment.name}? Its contacts stay.</span>
                <button type="button" className="button danger" onClick={remove}>
                  Delete segment
                </button>
                <button type="button" className="button" onClick={() => setDeleting(false)}>
                  Cancel
                </button>
              </>
            ) : (
              <button type="button" className="button" onClick={() => setDeleting(true)}>
                Delete
              </button>
            ))}
        </div>
      </form>
    </section>
  );
}
