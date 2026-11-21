/// <reference types="node" />
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

// The public site (site/, served at mailgoose.app) uses the app's own design
// tokens, copied into site/assets/tokens.css. If this fails, run
// `node scripts/site/tokens.mjs` from the repository root.

const root = resolve(__dirname, "../../..");

describe("public site design tokens", () => {
  it("site/assets/tokens.css carries the app's light tokens unchanged", () => {
    const css = readFileSync(resolve(root, "web/src/styles.css"), "utf8");
    const site = readFileSync(resolve(root, "site/assets/tokens.css"), "utf8");
    const tokens = css.slice(css.indexOf(":root {"), css.indexOf("\n/* Dark mode")).trimEnd();
    expect(tokens).toContain("--accent:");
    expect(site).toContain(tokens);
  });

  it("is light only: no dark tokens, dark logo or dark theme color", () => {
    const pages = ["index.html", "404.html", "terms/index.html", "privacy/index.html", "anti-spam/index.html", "dpa/index.html", "alpha/index.html"];
    for (const file of ["assets/tokens.css", "assets/site.css", ...pages]) {
      const text = readFileSync(resolve(root, "site", file), "utf8");
      expect(text, file).not.toMatch(/prefers-color-scheme|data-theme|wordmark-light/);
    }
  });
});
