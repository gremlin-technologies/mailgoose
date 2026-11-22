// The contact filter builder (MG-116): statuses, tags and what contacts did
// with past campaigns, every condition joined with AND as segments are. The
// contacts page filters with it, and a segment's page edits its conditions.
import { useCallback, useEffect, useState } from "react";

import { useApi } from "../api/context";
import { attempt } from "../api/result";
import { useLoad } from "../components";
import { plural } from "../campaigns/campaign";
import { contactStatus } from "../pages/Contacts";
import type { Tag } from "../pages/Tags";
import {
  contactQuery,
  isCampaignKind,
  isDaysKind,
  maxDays,
  newRow,
  opensHelp,
  rowKinds,
  rowProblem,
  singleUse,
  type Conditions,
  type ContactStatus,
  type Draft,
  type Row,
  type RowKind,
} from "./conditions";

/** A campaign the engagement conditions can name: launched, with tracking on. */
export type CampaignOption = { id: string; name: string };

/** How long the count waits after the last change. */
export const countDelay = 300;

/** Campaigns that launched with opens and clicks counted, newest first: the ones a condition can name. */
export function useTrackedCampaigns() {
  const api = useApi();
  const load = useCallback(
    async () => {
      const res = await attempt(() => api.GET("/v1/campaigns", { params: { query: { limit: 100 } } }));
      if (!res.ok) return res;
      const options: CampaignOption[] = (res.data?.data ?? [])
        .filter((c) => c.tracking && c.status !== "draft" && c.status !== "scheduled")
        .map((c) => ({ id: c.id, name: c.name }));
      return { ok: true as const, data: options };
    },
    [api],
  );
  return useLoad(load);
}

export function FilterBuilder({
  draft,
  onChange,
  tags,
  campaigns,
}: {
  draft: Draft;
  onChange: (d: Draft) => void;
  tags: Tag[];
  campaigns: CampaignOption[];
}) {
  // Tags and campaigns the conditions name that aren't there any more stay
  // listed, so they can be seen and taken off.
  const goneTags = draft.tags.filter((id) => !tags.some((t) => t.id === id));
  const usesOpens = draft.rows.some((r) => r.kind.includes("opened"));
  // Read here, not when the module loads: the contacts page imports this one.
  const statuses = Object.keys(contactStatus) as ContactStatus[];

  function toggle<T>(list: T[], item: T, on: boolean): T[] {
    return on ? [...list, item] : list.filter((x) => x !== item);
  }

  function setRow(key: number, change: Partial<Row>) {
    onChange({ ...draft, rows: draft.rows.map((r) => (r.key === key ? { ...r, ...change } : r)) });
  }

  function changeKind(r: Row, kind: RowKind) {
    // A campaign picked for one campaign condition carries over to another.
    const campaign = isCampaignKind(kind) ? r.campaign || (campaigns[0]?.id ?? "") : r.campaign;
    setRow(r.key, { kind, campaign, days: isDaysKind(kind) && !r.days ? "30" : r.days });
  }

  function add() {
    const used = new Set(draft.rows.map((r) => r.kind));
    const kind = campaigns.length
      ? "opened"
      : (rowKinds.find((k) => !isCampaignKind(k.kind) && !used.has(k.kind))?.kind ?? "opened");
    onChange({ ...draft, rows: [...draft.rows, newRow(kind, isCampaignKind(kind) ? (campaigns[0]?.id ?? "") : "")] });
  }

  const allSingleUsed = rowKinds.filter((k) => singleUse(k.kind)).every((k) => draft.rows.some((r) => r.kind === k.kind));

  return (
    <div className="filter-builder stack">
      <p className="muted small">A contact has to match every condition you set.</p>
      <fieldset className="choices">
        <legend>Status</legend>
        <p className="muted small">Any one of those ticked. Tick none for every status.</p>
        <div className="tag-choices flush">
          {statuses.map((s) => (
            <label key={s} className="choice">
              <input
                type="checkbox"
                checked={draft.statuses.includes(s)}
                onChange={(e) => onChange({ ...draft, statuses: toggle(draft.statuses, s, e.target.checked) })}
              />
              {contactStatus[s].label}
            </label>
          ))}
        </div>
      </fieldset>

      <fieldset className="choices">
        <legend>Tags</legend>
        {tags.length === 0 && goneTags.length === 0 ? (
          <p className="muted small">No tags yet.</p>
        ) : (
          <>
            <p className="muted small">Contacts with every tag ticked.</p>
            <div className="tag-choices flush">
              {tags.map((t) => (
                <label key={t.id} className="choice">
                  <input
                    type="checkbox"
                    checked={draft.tags.includes(t.id)}
                    onChange={(e) => onChange({ ...draft, tags: toggle(draft.tags, t.id, e.target.checked) })}
                  />
                  {t.name}
                </label>
              ))}
              {goneTags.map((id) => (
                <label key={id} className="choice">
                  <input type="checkbox" checked onChange={() => onChange({ ...draft, tags: toggle(draft.tags, id, false) })} />
                  A deleted tag
                </label>
              ))}
            </div>
          </>
        )}
      </fieldset>

      <fieldset className="choices">
        <legend>What they did with your campaigns</legend>
        {draft.rows.length > 0 && (
          <ol className="condition-rows">
            {draft.rows.map((r, i) => (
              <ConditionRow
                key={r.key}
                row={r}
                n={i + 1}
                rows={draft.rows}
                campaigns={campaigns}
                onKind={(k) => changeKind(r, k)}
                onChange={(change) => setRow(r.key, change)}
                onRemove={() => onChange({ ...draft, rows: draft.rows.filter((x) => x.key !== r.key) })}
              />
            ))}
          </ol>
        )}
        <div>
          <button type="button" className="button small" onClick={add} disabled={!campaigns.length && allSingleUsed}>
            Add a condition
          </button>
        </div>
        {campaigns.length === 0 && (
          <p className="muted small">A condition on one campaign needs a campaign that was sent with opens and clicks counted.</p>
        )}
        {usesOpens && <p className="muted small">{opensHelp}</p>}
      </fieldset>
    </div>
  );
}

function ConditionRow({
  row,
  n,
  rows,
  campaigns,
  onKind,
  onChange,
  onRemove,
}: {
  row: Row;
  n: number;
  rows: Row[];
  campaigns: CampaignOption[];
  onKind: (k: RowKind) => void;
  onChange: (change: Partial<Row>) => void;
  onRemove: () => void;
}) {
  const problem = rowProblem(row);
  const missing = isCampaignKind(row.kind) && row.campaign && !campaigns.some((c) => c.id === row.campaign);
  return (
    <li className="condition-row">
      <div className="row-form">
        <label className="field">
          <span>Condition {n}</span>
          <select value={row.kind} onChange={(e) => onKind(e.target.value as RowKind)}>
            {rowKinds.map((k) => (
              <option
                key={k.kind}
                value={k.kind}
                disabled={
                  k.kind !== row.kind &&
                  ((singleUse(k.kind) && rows.some((r) => r.kind === k.kind)) || (isCampaignKind(k.kind) && !campaigns.length && !row.campaign))
                }
              >
                {k.label}
              </option>
            ))}
          </select>
        </label>
        {isCampaignKind(row.kind) && (
          <label className="field grow">
            <span>Campaign</span>
            {/* Named for its row, as every row has a Campaign menu; the name starts with the label shown. */}
            <select value={row.campaign} onChange={(e) => onChange({ campaign: e.target.value })} aria-label={`Campaign for condition ${n}`}>
              {!row.campaign && <option value="">Pick a campaign</option>}
              {missing && <option value={row.campaign}>A deleted or untracked campaign</option>}
              {campaigns.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </label>
        )}
        {isDaysKind(row.kind) && (
          <label className="field days">
            <span>Days</span>
            <input
              aria-label={`Days for condition ${n}`}
              type="number"
              inputMode="numeric"
              min={1}
              max={maxDays}
              value={row.days}
              onChange={(e) => onChange({ days: e.target.value })}
            />
          </label>
        )}
        <button type="button" className="button small" onClick={onRemove} aria-label={`Remove condition ${n}`}>
          Remove
        </button>
      </div>
      {problem && <p className="field-error">{problem}</p>}
    </li>
  );
}

/**
 * How many contacts match, counted by the API a moment after the conditions
 * change. extra holds the contact list's search, which a segment doesn't keep.
 */
export function ContactCount({ conditions, extra }: { conditions: Conditions; extra?: { email?: string; name?: string } }) {
  const api = useApi();
  const key = JSON.stringify({ ...contactQuery(conditions), ...extra });
  const [answer, setAnswer] = useState<{ key: string; text: string }>({ key: "", text: "" });

  useEffect(() => {
    let current = true;
    const timer = setTimeout(() => {
      const query = JSON.parse(key) as ReturnType<typeof contactQuery> & { email?: string; name?: string };
      void attempt(() => api.GET("/v1/contacts/count", { params: { query } })).then((res) => {
        if (!current) return;
        setAnswer({
          key,
          text: res.ok ? `${plural(res.data?.count ?? 0, "contact matches", "contacts match")} now.` : res.failure.message,
        });
      });
    }, countDelay);
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [api, key]);

  return (
    <p className="muted small recipient-count" role="status" aria-label="Contact count">
      {answer.key === key ? answer.text : "Counting…"}
    </p>
  );
}

