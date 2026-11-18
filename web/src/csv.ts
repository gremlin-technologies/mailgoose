/**
 * The header row of a CSV file: the first record, read the way the import
 * reads it (commas, double-quoted cells with "" for a quote, CRLF or LF).
 */
export function csvHeader(text: string): string[] {
  const cells: string[] = [];
  let cell = "";
  let quoted = false;
  let i = text.charCodeAt(0) === 0xfeff ? 1 : 0;
  for (; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') {
        cell += '"';
        i++;
      } else if (c === '"') quoted = false;
      else cell += c;
    } else if (c === '"') quoted = true;
    else if (c === ",") {
      cells.push(cell.trim());
      cell = "";
    } else if (c === "\n" || c === "\r") break;
    else cell += c;
  }
  cells.push(cell.trim());
  return cells.length === 1 && cells[0] === "" ? [] : cells;
}

/** Rows after the header, not counting blank lines; for showing the size of a file. */
export function csvRowCount(text: string): number {
  return Math.max(0, text.split(/\r?\n/).filter((l) => l.trim() !== "").length - 1);
}

export type ImportField = "email" | "first_name" | "last_name" | "status" | "tags";

const usual: Record<ImportField, string[]> = {
  email: ["email", "e-mail", "email address", "e-mail address", "mail"],
  first_name: ["first name", "first_name", "firstname", "first", "given name"],
  last_name: ["last name", "last_name", "lastname", "last", "surname", "family name"],
  status: ["status"],
  tags: ["tags", "tag"],
};

/** The header a field most likely comes from, or "" when none looks right. */
export function guessColumn(field: ImportField, headers: string[]): string {
  const want = usual[field];
  return headers.find((h) => want.includes(h.toLowerCase().replace(/\s+/g, " "))) ?? "";
}
