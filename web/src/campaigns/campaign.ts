// Shared helpers for the campaign pages.
import type { components } from "../api/schema";
import type { Tone } from "../components";

export type Campaign = components["schemas"]["Campaign"];
export type CampaignStatus = components["schemas"]["CampaignStatus"];
export type RecipientsInput = components["schemas"]["RecipientsInput"];

export const statusBadge: Record<CampaignStatus, { label: string; tone: Tone }> = {
  draft: { label: "Draft", tone: "neutral" },
  scheduled: { label: "Scheduled", tone: "info" },
  sending: { label: "Sending", tone: "info" },
  sent: { label: "Sent", tone: "good" },
  cancelled: { label: "Cancelled", tone: "neutral" },
  paused: { label: "Paused", tone: "warn" },
  failed: { label: "Failed", tone: "bad" },
};

/** Who a campaign goes to, in words. */
export function recipientsText(r: Campaign["recipients"]): string {
  if (r.all) return "All subscribed contacts";
  if (r.tags.length === 0) return "Nobody yet";
  return `Contacts tagged ${listOf(r.tags.map((t) => t.name))}`;
}

/** "a", "a or b", "a, b or c". */
export function listOf(names: string[], word = "or"): string {
  if (names.length <= 1) return names.join("");
  return `${names.slice(0, -1).join(", ")} ${word} ${names.at(-1)}`;
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n.toLocaleString()} ${n === 1 ? one : many}`;
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

/** An ISO time as a datetime-local input's value, in the viewer's time zone. */
export function toLocalInput(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** A datetime-local input's value (the viewer's time zone) as an ISO time; undefined if it isn't one. */
export function fromLocalInput(value: string): string | undefined {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return undefined;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}
