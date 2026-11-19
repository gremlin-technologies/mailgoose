import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { fakeApi, renderApp, signedIn } from "../test/render";

const page = (data: unknown[]) => () => ({ status: 200, body: { data, next_cursor: null } });
const campaignId = "66666666-6666-4666-8666-666666666666";

function campaign(over: Record<string, unknown>) {
  return {
    id: campaignId,
    name: "May news",
    email_id: null,
    recipients: { all: true, tags: [] },
    send_at: null,
    tracking: true,
    status: "draft",
    version: 1,
    last_test: null,
    launch: null,
    created_at: "2026-11-18T10:00:00Z",
    updated_at: "2026-11-18T10:00:00Z",
    ...over,
  };
}

function server(state: { domains?: unknown[]; contacts?: unknown[]; emails?: unknown[]; campaigns?: unknown[] }) {
  return fakeApi({
    ...signedIn,
    "GET /v1/domains": page(state.domains ?? []),
    "GET /v1/contacts": page(state.contacts ?? []),
    "GET /v1/emails": page(state.emails ?? []),
    "GET /v1/campaigns": page(state.campaigns ?? []),
    "GET /v1/credits": () => ({ status: 200, body: { balance: 1000, plan: "free", next_grant_at: "2026-12-18T10:00:00Z" } }),
  });
}

/** Each checklist step's label and whether it reads as done. */
async function checklist() {
  const list = await screen.findByRole("region", { name: "Getting started" });
  return within(list)
    .getAllByRole("listitem")
    .map((li) => [within(li).getByRole("link").textContent, li.textContent?.includes("(done)")]);
}

describe("dashboard", () => {
  it("starts a new account with every step to do, each linking to its page", async () => {
    const api = server({});
    renderApp("/", api.fetch);
    expect(await checklist()).toEqual([
      ["Verify your domain", false],
      ["Add contacts", false],
      ["Build an email", false],
      ["Send a test", false],
      ["Launch a campaign", false],
    ]);
    const list = screen.getByRole("region", { name: "Getting started" });
    expect(within(list).getAllByRole("link").map((a) => a.getAttribute("href"))).toEqual(["/domains", "/contacts", "/emails", "/campaigns", "/campaigns"]);
    expect(within(list).getByText("Five steps to your first campaign.")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "Credits" })).getByText("1,000")).toBeTruthy();
    expect(screen.getByText("No campaigns yet.")).toBeTruthy();
  });

  it("ticks each step from the account's real state", async () => {
    const api = server({
      domains: [{ id: "d1", name: "patsbakery.com", status: "pending" }],
      contacts: [{ id: "c1" }],
      emails: [{ id: "e1" }],
      campaigns: [campaign({ last_test: { at: "2026-11-19T10:00:00Z", version: 1, email_version: 1 } })],
    });
    renderApp("/", api.fetch);
    expect(await checklist()).toEqual([
      ["Verify your domain", false],
      ["Add contacts", true],
      ["Build an email", true],
      ["Send a test", true],
      ["Launch a campaign", false],
    ]);
    expect(screen.getByText("2 of 5 steps left.")).toBeTruthy();
    // Only one contact, email and the like is asked for: enough to tick a step.
    expect(api.calls.find((c) => c.path === "/v1/contacts")).toBeTruthy();
  });

  it("hides the checklist once everything is done, and lists the latest campaigns", async () => {
    const launch = { at: "2026-11-19T10:00:00Z", recipient_count: 120, credits_reserved: 120 };
    const api = server({
      domains: [{ id: "d1", name: "patsbakery.com", status: "verified" }],
      contacts: [{ id: "c1" }],
      emails: [{ id: "e1" }],
      campaigns: [campaign({ status: "sent", launch }), campaign({ id: "77777777-7777-4777-8777-777777777777", name: "June news" })],
    });
    renderApp("/", api.fetch);
    const latest = await screen.findByRole("region", { name: "Latest campaigns" });
    expect(screen.queryByRole("region", { name: "Getting started" })).toBeNull();
    expect(within(latest).getByRole("link", { name: "May news" }).getAttribute("href")).toBe(`/campaigns/${campaignId}/report`);
    expect(within(latest).getByRole("link", { name: "June news" }).getAttribute("href")).toBe("/campaigns/77777777-7777-4777-8777-777777777777");
    expect(within(latest).getByText("Sent")).toBeTruthy();
    expect(within(latest).getByText("Draft")).toBeTruthy();
  });
});
