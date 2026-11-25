// Helpers for the HTML email editor, kept apart from React so they can be
// tested on their own.
import type { Failure } from "../api/result";
import type { components } from "../api/schema";
import { isBuiltinTag } from "./format";

export type HtmlIssue = components["schemas"]["HtmlIssue"];

/** The sample contact the server's preview uses, so both previews match. */
const sample: Record<string, string> = {
  first_name: "Alex",
  last_name: "Rivera",
  email: "alex@example.com",
  unsubscribe_url: "#unsubscribe",
};

/**
 * The markup with its merge tags filled in from the sample contact, for the
 * live preview. The sample has no contact field values, so a field's tag
 * (MG-133) shows its fallback, written into the markup as it stands, or
 * nothing. Unknown tags stay as they are, so they show up.
 */
export function fillSample(html: string, fieldKeys: readonly string[] = []): string {
  return html.replace(/\{\{\s*([^{}|]*?)\s*(?:\|([^{}]*))?\}\}/g, (all, tag: string, fallback?: string) => {
    if (isBuiltinTag(tag)) return escape(sample[tag]);
    if (fieldKeys.includes(tag)) return (fallback ?? "").trim();
    return all;
  });
}

function escape(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/"/g, "&quot;");
}

/** The issues an invalid_html failure lists, each with its line. */
export function issuesOf(failure: Failure): HtmlIssue[] {
  const issues = failure.details?.issues;
  if (!Array.isArray(issues)) return [];
  return issues.filter(
    (i): i is HtmlIssue =>
      typeof i === "object" && i !== null && typeof i.line === "number" && typeof i.message === "string" && typeof i.code === "string",
  );
}

/** Issues in line order, so the list reads top to bottom like the code. */
export const byLine = (issues: HtmlIssue[]) => [...issues].sort((a, b) => a.line - b.line);

/** The most markup the editor reads from a file; the API refuses over 102 KB anyway. */
export const maxFileBytes = 512_000;
