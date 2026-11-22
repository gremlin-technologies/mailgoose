// A segment's conditions (MG-114) as the web app edits them (MG-116): the
// same set the contact list filters by, kept in the page address so a
// filtered list can be linked to and saved as a segment.
import type { components } from "../api/schema";

export type Conditions = components["schemas"]["SegmentConditions"];
export type ContactStatus = components["schemas"]["ContactStatus"];
export type Segment = components["schemas"]["Segment"];

/** The conditions that name a campaign. */
export type CampaignKind = "opened" | "not_opened" | "clicked" | "not_clicked";
/** The conditions that take a number of days. */
export type DaysKind = "opened_within_days" | "clicked_within_days";
/** The yes-or-no conditions. */
export type FlagKind = "never_opened" | "never_clicked";
export type RowKind = CampaignKind | DaysKind | FlagKind;

/** One engagement condition as a row of the builder. */
export type Row = { key: number; kind: RowKind; campaign: string; days: string };

/** What the builder edits: statuses (any of), tags (all of) and engagement rows (all of). */
export type Draft = { statuses: ContactStatus[]; tags: string[]; rows: Row[] };

export const campaignKinds: CampaignKind[] = ["opened", "not_opened", "clicked", "not_clicked"];
const daysKinds: DaysKind[] = ["opened_within_days", "clicked_within_days"];
const flagKinds: FlagKind[] = ["never_opened", "never_clicked"];

/** Each engagement condition's words in the builder's menu, in its order. */
export const rowKinds: { kind: RowKind; label: string }[] = [
  { kind: "opened", label: "Opened a campaign" },
  { kind: "not_opened", label: "Got a campaign and didn't open it" },
  { kind: "clicked", label: "Clicked in a campaign" },
  { kind: "not_clicked", label: "Got a campaign and didn't click" },
  { kind: "opened_within_days", label: "Opened any campaign in the last" },
  { kind: "clicked_within_days", label: "Clicked in any campaign in the last" },
  { kind: "never_opened", label: "Never opened a campaign" },
  { kind: "never_clicked", label: "Never clicked in a campaign" },
];

/** At most this many campaigns across the engagement conditions, as the API allows. */
export const maxCampaigns = 20;
/** The most days a "in the last" condition looks back. */
export const maxDays = 365;

export function isCampaignKind(k: RowKind): k is CampaignKind {
  return (campaignKinds as RowKind[]).includes(k);
}

export function isDaysKind(k: RowKind): k is DaysKind {
  return (daysKinds as RowKind[]).includes(k);
}

/** Whether a kind can only be used once (all but the campaign ones). */
export function singleUse(k: RowKind): boolean {
  return !isCampaignKind(k);
}

let nextKey = 1;

/** A new row of a kind, with nothing picked yet. */
export function newRow(kind: RowKind, campaign = ""): Row {
  return { key: nextKey++, kind, campaign, days: isDaysKind(kind) ? "30" : "" };
}

export const emptyDraft: Draft = { statuses: [], tags: [], rows: [] };

/** The builder's draft of saved conditions. */
export function draftFrom(c: Conditions): Draft {
  const rows: Row[] = [];
  for (const kind of campaignKinds) for (const id of c[kind] ?? []) rows.push(newRow(kind, id));
  for (const kind of daysKinds) {
    const days = c[kind];
    if (days) rows.push({ ...newRow(kind), days: String(days) });
  }
  for (const kind of flagKinds) if (c[kind]) rows.push(newRow(kind));
  return { statuses: [...(c.statuses ?? [])], tags: [...(c.tag_ids ?? [])], rows };
}

/** Why a row can't be used yet, if it can't. */
export function rowProblem(r: Row): string | undefined {
  if (isCampaignKind(r.kind) && !r.campaign) return "Pick a campaign.";
  if (isDaysKind(r.kind)) {
    const n = Number(r.days);
    if (!Number.isInteger(n) || n < 1 || n > maxDays) return `Enter a number of days from 1 to ${maxDays}.`;
  }
  return undefined;
}

/** The draft's finished conditions; rows not filled in yet are left out. */
export function conditionsOf(d: Draft): Conditions {
  const c: Conditions = {};
  if (d.tags.length) c.tag_ids = [...d.tags];
  if (d.statuses.length) c.statuses = [...d.statuses];
  for (const r of d.rows) {
    if (rowProblem(r)) continue;
    if (isCampaignKind(r.kind)) {
      const ids = (c[r.kind] ??= []);
      if (!ids.includes(r.campaign)) ids.push(r.campaign);
    } else if (isDaysKind(r.kind)) c[r.kind] = Number(r.days);
    else c[r.kind] = true;
  }
  return c;
}

/** Whether there's no condition at all. */
export function isEmpty(c: Conditions): boolean {
  return Object.keys(c).length === 0;
}

/** How many campaigns the conditions name. */
export function campaignCount(c: Conditions): number {
  return campaignKinds.reduce((n, k) => n + (c[k]?.length ?? 0), 0);
}

/** The conditions as the contact list's query parameters (listContacts, countContacts, exportContacts). */
export function contactQuery(c: Conditions) {
  return {
    status: c.statuses,
    tag_id: c.tag_ids,
    opened: c.opened,
    not_opened: c.not_opened,
    clicked: c.clicked,
    not_clicked: c.not_clicked,
    never_opened: c.never_opened || undefined,
    never_clicked: c.never_clicked || undefined,
    opened_within_days: c.opened_within_days,
    clicked_within_days: c.clicked_within_days,
  };
}

/** The same parameters as a query string, for links such as the CSV export. */
export function contactSearch(c: Conditions, into = new URLSearchParams()): URLSearchParams {
  for (const [k, v] of Object.entries(contactQuery(c))) {
    if (v === undefined) continue;
    if (Array.isArray(v)) for (const item of v) into.append(k, item);
    else into.set(k, String(v));
  }
  return into;
}

const statusNames: ContactStatus[] = ["subscribed", "unconfirmed", "unsubscribed", "bounced", "complained"];
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** The conditions in a page address; "tag" and "status" repeat, as the contacts page links them. */
export function conditionsFromParams(p: URLSearchParams): Conditions {
  const c: Conditions = {};
  const statuses = p.getAll("status").filter((s): s is ContactStatus => statusNames.includes(s as ContactStatus));
  if (statuses.length) c.statuses = [...new Set(statuses)];
  const tags = p.getAll("tag").filter((t) => uuid.test(t));
  if (tags.length) c.tag_ids = [...new Set(tags)];
  for (const k of campaignKinds) {
    const ids = p.getAll(k).filter((t) => uuid.test(t));
    if (ids.length) c[k] = [...new Set(ids)];
  }
  for (const k of daysKinds) {
    const n = Number(p.get(k));
    if (Number.isInteger(n) && n >= 1 && n <= maxDays) c[k] = n;
  }
  for (const k of flagKinds) if (p.get(k) === "1") c[k] = true;
  return c;
}

/** The conditions as page address parameters, the reverse of conditionsFromParams. */
export function conditionsToParams(c: Conditions, into = new URLSearchParams()): URLSearchParams {
  for (const s of c.statuses ?? []) into.append("status", s);
  for (const t of c.tag_ids ?? []) into.append("tag", t);
  for (const k of campaignKinds) for (const id of c[k] ?? []) into.append(k, id);
  for (const k of daysKinds) if (c[k]) into.set(k, String(c[k]));
  for (const k of flagKinds) if (c[k]) into.set(k, "1");
  return into;
}

/** Problem codes in words, for a condition the segment can't use now. */
export const problemWords: Record<components["schemas"]["SegmentProblem"]["code"], string> = {
  tag_deleted: "its tag was deleted",
  campaign_deleted: "its campaign was deleted",
  tracking_off: "its campaign is set to send without open and click tracking",
  report_archived: "its campaign launched more than 12 months ago and its opens and clicks were archived",
};

/** The quiet help line wherever a condition uses opens, as on the report page. */
export const opensHelp = "Opens can run high: some mail apps load the tracking image without anyone reading. Clicks are the surer sign.";
