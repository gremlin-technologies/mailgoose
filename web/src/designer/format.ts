// Editing helpers for the designer's text fields. Text blocks are stored as
// a small Markdown subset (bold, italic, links, lists), so the formatting
// buttons write that Markdown around the selection. Each helper takes the
// text and the selection and returns the new text and where the selection
// goes, so they can be tested without a browser.

export type Edit = { text: string; start: number; end: number };

/** Puts insert in place of the selection, and the caret after it. */
export function insert(text: string, start: number, end: number, insert: string): Edit {
  const out = text.slice(0, start) + insert + text.slice(end);
  const at = start + insert.length;
  return { text: out, start: at, end: at };
}

/** Wraps the selection in marker (** or *); with nothing selected, wraps placeholder and selects it. */
export function wrap(text: string, start: number, end: number, marker: string, placeholder: string): Edit {
  const inner = start === end ? placeholder : text.slice(start, end);
  const out = text.slice(0, start) + marker + inner + marker + text.slice(end);
  return { text: out, start: start + marker.length, end: start + marker.length + inner.length };
}

/** Makes the selection a link to url; with nothing selected, the address is the link text. */
export function link(text: string, start: number, end: number, url: string): Edit {
  const label = start === end ? url : text.slice(start, end);
  const md = `[${label}](${url})`;
  const out = text.slice(0, start) + md + text.slice(end);
  return { text: out, start: start + md.length, end: start + md.length };
}

/** Starts each line the selection touches with "- " or "1. ", "2. " and so on. */
export function list(text: string, start: number, end: number, ordered: boolean): Edit {
  const from = text.lastIndexOf("\n", start - 1) + 1;
  const nl = text.indexOf("\n", end);
  const to = nl === -1 ? text.length : nl;
  const lines = text
    .slice(from, to)
    .split("\n")
    .map((line, i) => (ordered ? `${i + 1}. ` : "- ") + line.replace(/^(\s*)(?:[-*] |\d+\. )/, "$1"));
  const body = lines.join("\n");
  return { text: text.slice(0, from) + body + text.slice(to), start: from, end: from + body.length };
}

export const mergeTags = [
  { tag: "first_name", label: "First name" },
  { tag: "last_name", label: "Last name" },
  { tag: "email", label: "Email address" },
  { tag: "unsubscribe_url", label: "Unsubscribe link" },
] as const;
export type MergeTag = (typeof mergeTags)[number]["tag"];

/** The longest fallback the API takes. */
export const maxFallback = 100;

/** A merge tag as it's written in the email, with its fallback if any. */
export function mergeTag(tag: MergeTag, fallback = ""): string {
  const f = fallback.trim();
  return tag === "unsubscribe_url" || f === "" ? `{{${tag}}}` : `{{${tag}|${f}}}`;
}

/** Why a fallback can't be used, or undefined when it can. */
export function fallbackProblem(fallback: string): string | undefined {
  if (/[{}]/.test(fallback)) return "A fallback can't contain { or }.";
  if ([...fallback.trim()].length > maxFallback) return `A fallback can be up to ${maxFallback} characters.`;
  return undefined;
}
