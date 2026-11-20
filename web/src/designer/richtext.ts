// A text block's small Markdown subset (**bold**, *italic*, [links](url),
// "- " and "1. " lists, blank lines between paragraphs; see the server's
// internal/render/markdown.go) to and from the HTML the canvas edits in
// place. mdToHtml builds what the block shows; htmlToMd reads back whatever
// the browser's editing left there, keeping only what the subset can say.

const esc = (s: string) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

/** Links the canvas will show as links; the server has the final say. */
export const okLink = (href: string) => /^(https?:\/\/|mailto:|\{\{)/i.test(href);

/** The * that closes an italic run, skipping **. */
function closingStar(s: string): number {
  for (let i = 0; i < s.length; i++) {
    if (s[i] !== "*") continue;
    if (s[i + 1] === "*") {
      i++;
      continue;
    }
    return i;
  }
  return -1;
}

function inlineHtml(s: string): string {
  let out = "";
  for (let i = 0; i < s.length; ) {
    if (s.startsWith("**", i)) {
      const end = s.indexOf("**", i + 2);
      if (end > i + 2) {
        out += `<strong>${inlineHtml(s.slice(i + 2, end))}</strong>`;
        i = end + 2;
        continue;
      }
    } else if (s[i] === "*") {
      const end = closingStar(s.slice(i + 1));
      if (end > 0) {
        out += `<em>${inlineHtml(s.slice(i + 1, i + 1 + end))}</em>`;
        i += end + 2;
        continue;
      }
    } else if (s[i] === "[") {
      const mid = s.indexOf("](", i);
      const end = mid > i + 1 ? s.indexOf(")", mid + 2) : -1;
      if (end > mid + 2) {
        const href = s.slice(mid + 2, end).trim();
        if (okLink(href)) {
          out += `<a href="${esc(href)}" title="${esc(href)}">${inlineHtml(s.slice(i + 1, mid))}</a>`;
          i = end + 1;
          continue;
        }
      }
    }
    out += esc(s[i]);
    i++;
  }
  return out;
}

/** The HTML a text block shows and edits. Empty text is one empty paragraph. */
export function mdToHtml(text: string): string {
  const out: string[] = [];
  for (const para of text.replace(/\r\n/g, "\n").split(/\n\s*\n/)) {
    const lines = para.split("\n").filter((l) => l.trim() !== "");
    if (lines.length === 0) continue;
    const bullet = lines.every((l) => /^\s*- /.test(l));
    const numbered = lines.every((l) => /^\s*\d+\. /.test(l));
    if (bullet || numbered) {
      const items = lines.map((l) => `<li>${inlineHtml(l.replace(/^\s*(- |\d+\. )/, ""))}</li>`).join("");
      out.push(bullet ? `<ul>${items}</ul>` : `<ol>${items}</ol>`);
    } else {
      out.push(`<p>${lines.map(inlineHtml).join("<br>")}</p>`);
    }
  }
  return out.length ? out.join("") : "<p><br></p>";
}

type Ctx = { strong: boolean; link: boolean };

/** Wraps core in a marker, keeping spaces outside it so the Markdown parses. */
function mark(inner: string, open: string, close: string): string {
  const m = /^(\s*)([\s\S]*?)(\s*)$/.exec(inner);
  if (!m || m[2] === "") return inner;
  return m[1] + open + m[2] + close + m[3];
}

function inlineMd(node: Node, ctx: Ctx): string {
  let out = "";
  for (const c of Array.from(node.childNodes)) {
    if (c.nodeType === 3) {
      out += (c.textContent ?? "").replace(/[\n\r]+/g, " ");
      continue;
    }
    if (c.nodeType !== 1) continue;
    const el = c as HTMLElement;
    const tag = el.tagName;
    if (tag === "BR") out += "\n";
    else if (tag === "B" || tag === "STRONG") {
      // The subset has no bold italic: emphasis inside emphasis is dropped.
      out += ctx.strong ? inlineMd(el, ctx) : mark(inlineMd(el, { ...ctx, strong: true }), "**", "**");
    } else if (tag === "I" || tag === "EM") {
      out += ctx.strong ? inlineMd(el, ctx) : mark(inlineMd(el, { ...ctx, strong: true }), "*", "*");
    } else if (tag === "A" && !ctx.link && okLink(el.getAttribute("href") ?? "")) {
      const href = (el.getAttribute("href") ?? "").trim();
      out += mark(inlineMd(el, { ...ctx, link: true }).replace(/\n/g, " "), "[", `](${href})`);
    } else out += inlineMd(el, ctx);
  }
  return out;
}

const blockTags = new Set(["P", "DIV", "H1", "H2", "H3", "H4", "H5", "H6", "BLOCKQUOTE", "LI", "PRE"]);

/** The Markdown for an edited text block. */
export function htmlToMd(root: Node): string {
  const blocks: string[] = [];
  let line = "";
  const flush = () => {
    const t = line
      .replace(/\u00a0/g, " ")
      .replace(/[ \t]*\n[ \t]*/g, "\n")
      .replace(/\n{2,}/g, "\n")
      .trim();
    if (t) blocks.push(t);
    line = "";
  };
  const walk = (node: Node) => {
    for (const c of Array.from(node.childNodes)) {
      const el = c.nodeType === 1 ? (c as HTMLElement) : undefined;
      if (el && (el.tagName === "UL" || el.tagName === "OL")) {
        flush();
        const items = Array.from(el.children)
          .map((li) => inlineMd(li, { strong: false, link: false }).replace(/\u00a0/g, " ").replace(/\s*\n\s*/g, " ").trim())
          .filter((t) => t !== "")
          .map((t, i) => (el.tagName === "OL" ? `${i + 1}. ` : "- ") + t);
        if (items.length) blocks.push(items.join("\n"));
      } else if (el && blockTags.has(el.tagName)) {
        flush();
        walk(el);
        flush();
      } else if (el && el.querySelector("p,div,ul,ol")) {
        // A wrapper the browser put around blocks.
        walk(el);
      } else {
        const holder = { childNodes: [c] } as unknown as Node;
        line += inlineMd(holder, { strong: false, link: false });
      }
    }
  };
  walk(root);
  flush();
  return blocks.join("\n\n");
}
