import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";

import { expect, type Page } from "@playwright/test";

// Shared by the end-to-end tests: a fresh account per run, the mail the local
// stack's fake sender captures, and the accessibility rules axe checks.

const mailDir = process.env.MAIL_CAPTURE_DIR ?? resolve(import.meta.dirname, "../../.local/mail");

/** Names for one run in one browser, so the browsers don't share an account. */
export function names(browser: string) {
  const run = `${browser}-${Date.now()}-${Math.floor(Math.random() * 1e4)}`;
  return {
    run,
    owner: `e2e-web-${run}@example.com`,
    domain: `e2e-web-${run}.example.com`,
    contacts: [`success+web-${run}@simulator.amazonses.com`, `bounce+web-${run}@simulator.amazonses.com`],
  };
}
export type Names = ReturnType<typeof names>;

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

export async function waitForMail(to: string, has: string): Promise<string> {
  let found: string | undefined;
  await expect.poll(() => (found = mailTo(to, has)), { message: `mail to ${to} with "${has}"`, timeout: 30_000 }).toBeTruthy();
  return found!;
}

export async function signUp(page: Page, { run, owner }: Names) {
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

// WCAG 2.2 AA, as far as axe checks it, plus axe's best practices.
export const axeTags = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa", "best-practice"];
