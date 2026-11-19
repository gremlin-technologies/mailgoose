import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";

import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";

// MG-58 and MG-59: a new customer signs up in the web app and launches a
// campaign, with nothing done outside the browser, in each browser the
// config names. The local stack's fake sender
// writes mail to MAIL_CAPTURE_DIR, where the test reads the sign-in code and
// checks the campaign went out. Contacts are on SES's mailbox simulator, so
// the run sends nothing to anyone.

const mailDir = process.env.MAIL_CAPTURE_DIR ?? resolve(import.meta.dirname, "../../.local/mail");

/** Names for one run in one browser, so the browsers don't share an account. */
function names(browser: string) {
  const run = `${browser}-${Date.now()}-${Math.floor(Math.random() * 1e4)}`;
  return {
    run,
    owner: `e2e-web-${run}@example.com`,
    domain: `e2e-web-${run}.example.com`,
    contacts: [`success+web-${run}@simulator.amazonses.com`, `bounce+web-${run}@simulator.amazonses.com`],
  };
}
type Names = ReturnType<typeof names>;

/** The newest captured message to `to` whose text has `has`, or undefined. */
function mailTo(to: string, has: string): string | undefined {
  let files: string[];
  try {
    files = readdirSync(mailDir).filter((f) => f.endsWith(".eml"));
  } catch {
    return undefined;
  }
  return files
    .map((f) => join(mailDir, f))
    .sort((a, b) => statSync(b).mtimeMs - statSync(a).mtimeMs)
    .map((f) => readFileSync(f, "utf8"))
    .find((m) => m.toLowerCase().includes(`to: <${to.toLowerCase()}>`) && m.includes(has));
}

async function waitForMail(to: string, has: string): Promise<string> {
  let found: string | undefined;
  await expect.poll(() => (found = mailTo(to, has)), { message: `mail to ${to} with "${has}"`, timeout: 30_000 }).toBeTruthy();
  return found!;
}

async function signUp(page: Page, { run, owner }: Names) {
  await page.goto("/signup");
  await page.getByLabel("Email", { exact: true }).fill(owner);
  await page.getByLabel("Account name", { exact: true }).fill(`E2E Web ${run}`);
  await page.getByLabel(/I accept the/).check();
  await page.getByRole("button", { name: "Email me a code" }).click();

  const mail = await waitForMail(owner, "Your mailgoose code");
  const code = /Your mailgoose code: (\d{6})/.exec(mail)?.[1];
  expect(code, "a six-digit code in the subject").toBeTruthy();
  await page.getByLabel("Code", { exact: true }).fill(code!);
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
}

test("sign up, set up, design, test and launch a campaign", async ({ page }, testInfo) => {
  const n = names(testInfo.project.name);
  const { run, owner, domain, contacts } = n;
  const started = Date.now();
  let emailPath = "";
  let campaignPath = "";
  await test.step("sign up with the emailed code", () => signUp(page, n));

  await test.step("add and verify a sending domain", async () => {
    await page.goto("/domains");
    await page.getByLabel("Add a domain", { exact: true }).fill(domain);
    await page.getByRole("button", { name: "Add domain" }).click();
    await expect(page.getByRole("heading", { name: domain, exact: true })).toBeVisible();
    // The local stack's fake SES verifies on the first check.
    await page.getByRole("button", { name: "Check DNS now" }).click();
    await expect(page.getByText(`You can send from any address at ${domain}`)).toBeVisible();
  });

  await test.step("set the From and the postal address", async () => {
    await page.goto("/settings");
    await page.getByLabel("Default From name").fill("E2E Web Bakery");
    await page.getByLabel("Default From address").fill(`news@${domain}`);
    await page.getByLabel("Postal address").fill("E2E Web Bakery\n12 Main St\nSpringfield, IL 62701");
    await page.getByRole("button", { name: "Save settings" }).click();
    await expect(page.getByText("Saved.")).toBeVisible();
  });

  await test.step("add contacts", async () => {
    await page.goto("/contacts");
    for (const email of contacts) {
      await page.getByRole("button", { name: "Add contact" }).click();
      const form = page.getByRole("form", { name: "Add a contact" });
      await form.getByLabel("Email", { exact: true }).fill(email);
      await form.getByLabel("First name", { exact: true }).fill("Sam");
      await form.getByLabel(/I have their permission/).check();
      await form.getByRole("button", { name: "Add contact" }).click();
      await expect(page.getByText(`Added ${email}.`)).toBeVisible();
    }
  });

  await test.step("design an email from blocks", async () => {
    await page.goto("/emails");
    await page.getByLabel("New email").fill(`Spring menu ${run}`);
    await page.getByRole("button", { name: "Create and design" }).click();
    await expect(page.getByRole("group", { name: "Footer block" })).toBeVisible();
    emailPath = new URL(page.url()).pathname;
    await page.getByRole("button", { name: "Add heading" }).click();
    await page.getByRole("button", { name: "Add text" }).click();
    await expect(page.getByRole("group", { name: "Text block" })).toBeVisible();
    const settings = page.getByRole("form", { name: "Email settings" });
    await settings.getByLabel("Subject", { exact: true }).fill(`Spring is here ${run}`);
    await expect(page.locator(".save-state")).toHaveText("All changes saved");
  });

  await test.step("make a campaign and send a test", async () => {
    await page.goto("/campaigns/new");
    const form = page.getByRole("form", { name: "Campaign" });
    await form.getByLabel("Campaign name").fill(`Spring menu ${run}`);
    // The select's label reads "Email" plus its chosen option, and each
    // option reads "<name> (<subject>)".
    await form.getByLabel(/^Email/).selectOption({ label: `Spring menu ${run} (Spring is here ${run})` });
    await form.getByLabel("All subscribed contacts").check();
    await form.getByRole("button", { name: "Save draft" }).click();
    await expect(page.getByRole("button", { name: "Send test" })).toBeVisible();
    campaignPath = new URL(page.url()).pathname;
    await page.getByRole("button", { name: "Send test" }).click();
    await expect(page.getByText(`Sent to ${owner}`)).toBeVisible();
    await waitForMail(owner, `Spring is here ${run}`);
  });

  await test.step("launch from the summary", async () => {
    await page.getByRole("button", { name: "Review and launch" }).click();
    const summary = page.getByRole("region", { name: "Launch summary" });
    await summary.getByRole("button", { name: "Send to 2 contacts now" }).click();
    await expect(page.getByText("Launched. It's on its way to 2 contacts.")).toBeVisible();
    for (const email of contacts) await waitForMail(email, `Spring is here ${run}`);
  });

  console.log(`${testInfo.project.name}: signup to launched campaign in ${((Date.now() - started) / 1000).toFixed(1)}s`);

  // MG-59: axe on every main page, filled in by the flow above, in one
  // browser (the rules don't depend on the engine). Unlike the unit tests'
  // axe check, this one sees real layout, so it checks color contrast too.
  if (testInfo.project.name !== "chromium") return;
  await test.step("axe finds nothing on the main pages", async () => {
    const signedIn = [
      "/",
      "/emails",
      emailPath,
      "/campaigns",
      campaignPath,
      `${campaignPath}/report`,
      "/campaigns/new",
      "/contacts",
      "/contacts/import",
      "/tags",
      "/forms",
      "/domains",
      "/api-keys",
      "/credits",
      "/settings",
    ];
    const signedOut = ["/sign-in", "/signup", "/terms", "/privacy", "/anti-spam", "/dpa"];
    const found: string[] = [];
    const check = async (path: string) => {
      await page.goto(path);
      await expect(page.getByRole("heading", { level: 1 }).first()).toBeVisible();
      const result = await new AxeBuilder({ page }).withTags(axeTags).exclude("iframe").analyze();
      for (const v of result.violations) {
        found.push(`${path}: ${v.id} (${v.impact}) ${v.help}: ${v.nodes.map((node) => node.target.join(" ")).join(", ")}`);
      }
    };
    for (const path of signedIn) await check(path);
    await page.context().clearCookies();
    for (const path of signedOut) await check(path);
    expect(found, "accessibility violations").toEqual([]);
  });
});

// WCAG 2.2 AA, as far as axe checks it, plus axe's best practices.
const axeTags = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"];
