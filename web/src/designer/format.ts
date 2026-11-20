// Editing helpers for the designer's text fields: putting text in at the
// caret, and writing merge tags. insert takes the text and the selection and
// returns the new text and where the caret goes, so it can be tested without
// a browser.

export type Edit = { text: string; start: number; end: number };

/** Puts insert in place of the selection, and the caret after it. */
export function insert(text: string, start: number, end: number, insert: string): Edit {
  const out = text.slice(0, start) + insert + text.slice(end);
  const at = start + insert.length;
  return { text: out, start: at, end: at };
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
