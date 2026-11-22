import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

import { axeTags, names, signUp } from "./helpers";

// MG-75: a Mailchimp customer uploads an audience export (Mailchimp names
// each file for its members' status, as here), reads the preview,
// confirms it, and gets the checklist of what's next, at desktop and phone
// width. The addresses are on SES's mailbox simulator, and nothing is sent.

async function noViolations(page: Page, where: string) {
  const result = await new AxeBuilder({ page }).withTags(axeTags).exclude("iframe").analyze();
  const found = result.violations.map((v) => `${where}: ${v.id} (${v.impact}) ${v.help}`);
  expect(found, "accessibility violations").toEqual([]);
}

test("upload a Mailchimp audience export, preview it and import it", async ({ page }, testInfo) => {
  const n = names(`mailchimp-${testInfo.project.name}`);
  await test.step("sign up with the emailed code", () => signUp(page, n));

  const csv = [
    "Email Address,First Name,Last Name,TAGS,OPTIN_TIME,Favourite bread",
    `success+mc1-${n.run}@simulator.amazonses.com,Pat,Lee,"Regulars,Wholesale",2026-10-01 09:00:00,Rye`,
    `success+mc2-${n.run}@simulator.amazonses.com,Sam,,Regulars,2026-10-02 09:00:00,Sourdough`,
    "not-an-address,,,,,",
  ].join("\n");

  await test.step("upload the export from the contacts import page", async () => {
    await page.goto("/contacts/import");
    await page.getByRole("link", { name: "Import from Mailchimp" }).click();
    await expect(page.getByRole("heading", { name: "Import from Mailchimp", level: 1 })).toBeVisible();
    await page.getByLabel(/Export file/).setInputFiles({ name: "subscribed_members_export_bakery.csv", mimeType: "text/csv", buffer: Buffer.from(csv) });
    await page.getByLabel(/^Audience name/).fill("Bakery news");
    await noViolations(page, "start");
    await page.getByRole("button", { name: "Upload and preview" }).click();
  });

  await test.step("read the preview", async () => {
    await expect(page.getByText("Nothing is imported yet.", { exact: false })).toBeVisible();
    await expect(page.getByText("Bakery news: 3 members")).toBeVisible();
    await expect(page.getByText("New tags: Regulars, Wholesale.")).toBeVisible();
    // One row in three isn't an address, which holds the import for review.
    await expect(page.getByText("Held for review: too many rows aren't valid email addresses.")).toBeVisible();
    await expect(page.getByText(/These Mailchimp fields aren't kept: Favourite bread/)).toBeVisible();
    await noViolations(page, "preview");
  });

  await test.step("confirm with the permission box ticked", async () => {
    const importButton = page.getByRole("button", { name: "Import", exact: true });
    await expect(importButton).toBeDisabled();
    await page.getByRole("checkbox", { name: "Everyone in this file has given me permission to email them." }).check();
    await importButton.click();
    await expect(page.getByText("2 contacts added, 1 skipped.")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByRole("heading", { name: "What's next" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Verify your sending domain" })).toBeVisible();
    await noViolations(page, "imported");
  });

  await test.step("fits a phone", async () => {
    await page.setViewportSize({ width: 390, height: 844 });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, "no sideways scroll at phone width").toBeLessThanOrEqual(0);
    await noViolations(page, "imported, phone");
  });

  await test.step("download the skipped rows from the report", async () => {
    await page.getByRole("link", { name: "See the import report" }).click();
    const [download] = await Promise.all([
      page.waitForEvent("download"),
      page.getByRole("link", { name: "Download skipped rows (CSV)" }).click(),
    ]);
    expect(download.suggestedFilename()).toBe("skipped-rows.csv");
  });
});
