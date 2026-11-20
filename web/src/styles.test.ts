import { describe, expect, it } from "vitest";

import css from "./styles.css?raw";

/** The custom properties set in a block of styles.css, by name. */
function tokens(block: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of block.matchAll(/--([a-z-]+):\s*(#[0-9a-f]{6})/gi)) out[m[1]] = m[2];
  return out;
}

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

const light = tokens(css.slice(css.indexOf(":root {"), css.indexOf("@media (prefers-color-scheme: dark)")));
const dark = { ...light, ...tokens(css.slice(css.indexOf("@media (prefers-color-scheme: dark)"), css.indexOf("* {"))) };

// Every pair the app draws, and the WCAG 2.2 AA ratio it needs: 4.5 for
// text, 3 for the edges of controls and the focus ring.
const pairs: [string, string, number][] = [
  ["text", "bg", 4.5],
  ["text", "surface", 4.5],
  ["text", "accent-soft", 4.5],
  ["muted", "bg", 4.5],
  ["muted", "surface", 4.5],
  ["muted", "accent-soft", 4.5],
  ["accent-text", "accent", 4.5],
  ["accent-text", "accent-hover", 4.5],
  ["accent-soft-text", "accent-soft", 4.5],
  ["good-text", "good-bg", 4.5],
  ["warn-text", "warn-bg", 4.5],
  ["error-text", "error-bg", 4.5],
  ["danger-text", "danger", 4.5],
  // Links are the accent on whatever they sit on.
  ["accent", "bg", 4.5],
  ["accent", "surface", 4.5],
  ["accent", "surface-subtle", 4.5],
  ["accent", "accent-soft", 4.5],
  // The quiet fills: sidebar, table headers, row hover, code.
  ["text", "surface-subtle", 4.5],
  ["text", "surface-hover", 4.5],
  ["muted", "surface-subtle", 4.5],
  ["muted", "surface-hover", 4.5],
  ["good-text", "surface", 4.5],
  ["input-border", "surface", 3],
  ["input-border", "bg", 3],
  ["focus", "surface", 3],
  ["focus", "bg", 3],
  ["danger", "surface", 3],
];

describe("design tokens", () => {
  for (const [mode, t] of [
    ["light", light],
    ["dark", dark],
  ] as const) {
    it.each(pairs)(`${mode}: %s on %s is at least %d:1`, (fg, bg, need) => {
      expect(t[fg], `--${fg}`).toBeDefined();
      expect(t[bg], `--${bg}`).toBeDefined();
      expect(contrast(t[fg], t[bg])).toBeGreaterThanOrEqual(need);
    });
  }
});
