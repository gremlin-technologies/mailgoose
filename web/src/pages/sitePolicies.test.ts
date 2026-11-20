/// <reference types="node" />
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";

import { currentPolicyVersion } from "../policyVersions.gen";
import { policies } from "./Policies";

// The public site (site/, served at mailgoose.app) carries static copies of
// the policy pages. They must say what the app's pages say: if this fails,
// run `node scripts/site/policies.mjs` from the repository root.

function page(path: string): string {
  return readFileSync(resolve(__dirname, `../../../site${path}`), "utf8");
}

function escapeHTML(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

describe("public site policy pages", () => {
  for (const policy of policies) {
    it(`${policy.path} matches the app's ${policy.title.toLowerCase()}`, () => {
      const html = page(`${policy.path}/index.html`);
      expect(html).toContain(`<h1>${escapeHTML(policy.title)}</h1>`);
      expect(html).toContain(`<p>${escapeHTML(policy.summary)}</p>`);
      expect(html).toContain(`Version ${currentPolicyVersion.version},`);
      expect(html).toContain("<strong>Placeholder.</strong>");
      for (const section of policy.sections) {
        expect(html).toContain(`<h2>${escapeHTML(section.heading)}</h2>`);
        for (const point of section.points) {
          expect(html).toContain(`<li>${escapeHTML(point)}</li>`);
        }
      }
    });
  }

  it("the landing page links every policy page", () => {
    const html = page("/index.html");
    for (const policy of policies) {
      expect(html).toContain(`href="${policy.path}/"`);
    }
  });
});
