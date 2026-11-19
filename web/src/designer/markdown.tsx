// Shows a text block's small Markdown subset on the canvas: **bold**,
// *italic*, [links](https://...), "- " and "1. " lists, and blank lines
// between paragraphs. The server's renderer (MG-21) makes the real email;
// this only has to look like it. Links aren't clickable here.
import type { ReactNode } from "react";

const inline = /(\*\*[^*]+\*\*|\*[^*]+\*|\[[^\]]+\]\([^)\s]+\))/g;

function spans(text: string): ReactNode[] {
  return text.split(inline).map((part, i) => {
    if (part.startsWith("**") && part.endsWith("**") && part.length > 4) return <strong key={i}>{part.slice(2, -2)}</strong>;
    if (part.startsWith("*") && part.endsWith("*") && part.length > 2) return <em key={i}>{part.slice(1, -1)}</em>;
    const link = /^\[([^\]]+)\]\(([^)\s]+)\)$/.exec(part);
    if (link) {
      return (
        <span key={i} className="canvas-link" title={link[2]}>
          {link[1]}
        </span>
      );
    }
    return part;
  });
}

export function Markdown({ text }: { text: string }) {
  const out: ReactNode[] = [];
  text
    .replace(/\r\n/g, "\n")
    .split(/\n\s*\n/)
    .forEach((para, p) => {
      const lines = para.split("\n").filter((l) => l.trim() !== "");
      if (lines.length === 0) return;
      const bullet = lines.every((l) => /^\s*- /.test(l));
      const numbered = lines.every((l) => /^\s*\d+\. /.test(l));
      if (bullet || numbered) {
        const items = lines.map((l, i) => <li key={i}>{spans(l.replace(/^\s*(- |\d+\. )/, ""))}</li>);
        out.push(bullet ? <ul key={p}>{items}</ul> : <ol key={p}>{items}</ol>);
        return;
      }
      out.push(
        <p key={p}>
          {lines.map((l, i) => (
            <span key={i}>
              {i > 0 && <br />}
              {spans(l)}
            </span>
          ))}
        </p>,
      );
    });
  return <>{out}</>;
}
