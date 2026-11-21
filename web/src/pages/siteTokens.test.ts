/// <reference types="node" />
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

// The public site (site/, served at mailgoose.app) uses the app's own design
// tokens, copied into site/assets/tokens.css. If this fails, run
// `node scripts/site/tokens.mjs` from the repository root.

const root = resolve(__dirname, "../../..");

describe("public site design tokens", () => {
  it("site/assets/tokens.css carries the app's tokens unchanged", () => {
    const css = readFileSync(resolve(root, "web/src/styles.css"), "utf8");
    const site = readFileSync(resolve(root, "site/assets/tokens.css"), "utf8");
    const tokens = css.slice(css.indexOf(":root {"), css.indexOf("\n* {")).trimEnd();
    expect(tokens).toContain("--accent:");
    expect(site).toContain(tokens);
  });
});
