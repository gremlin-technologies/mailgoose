import axe from "axe-core";
import { expect } from "vitest";

/**
 * Runs axe on the page as rendered and fails with each violation (WEB-3).
 * jsdom does no layout, so color contrast is left to styles.test.ts and the
 * end-to-end check in a real browser (web/e2e). Frames are skipped: they
 * only show previews of the customer's own email or form.
 */
export async function expectAccessible(root: Element = document.body) {
  const result = await axe.run(root, {
    // Frames hold email and form previews, the customer's own markup.
    iframes: false,
    runOnly: { type: "tag", values: ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"] },
    rules: {
      "color-contrast": { enabled: false },
      // The test harness renders a location probe after the app, outside
      // its landmarks; the real page has nothing outside them.
      region: { enabled: false },
    },
  });
  const found = result.violations.map(
    (v) => `${v.id} (${v.impact}): ${v.help}\n${v.nodes.map((n) => `  ${n.target.join(" ")}: ${n.failureSummary?.split("\n").slice(1).join(" ")}`).join("\n")}`,
  );
  expect(found, "accessibility violations").toEqual([]);
}
