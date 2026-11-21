import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { fakeApi, renderApp, signedIn } from "../test/render";

const account = {
  id: "7c1d0f7e-8f43-4a8e-9d35-2b8a7c6e1a10",
  name: "Pat's Bakery",
  plan: "free",
  from_name: null,
  from_email: null,
  postal_address: null,
  time_zone: "UTC",
  missing_for_send: [],
  created_at: "2026-11-18T10:00:00Z",
  send_limit: { daily: 100, usual: 10000, reason: null, lowered_until: null },
  sending_mode: "full",
};

function withTester(tester: object | null) {
  return fakeApi({
    ...signedIn,
    "GET /v1/account": () => ({ status: 200, body: { ...account, tester } }),
  });
}

describe("the tester banner", () => {
  it("shows a tester account's credits left and its end", async () => {
    renderApp(
      "/contacts",
      withTester({ started_at: "2026-11-18T12:00:00Z", until: "2026-12-18T12:00:00Z", credits: 500, used: 120, left: 380, ended: false })
        .fetch,
    );
    const note = (await screen.findByText(/Tester account: 380 of 500 credits left/)).closest("[role=status]");
    expect(note?.textContent).toMatch(/until Dec 18, 2026/);
    expect(note?.textContent).toMatch(/simulator addresses/);
  });

  it("says when a tester account has ended", async () => {
    renderApp(
      "/contacts",
      withTester({ started_at: "2026-10-18T12:00:00Z", until: "2026-11-18T12:00:00Z", credits: 500, used: 500, left: 0, ended: true })
        .fetch,
    );
    const note = (await screen.findByText(/It ended on Nov 18, 2026/)).closest("[role=status]");
    expect(note?.className).toMatch(/notice-warn/);
  });

  it("isn't there on other accounts", async () => {
    renderApp("/contacts", withTester(null).fetch);
    expect(await screen.findByRole("navigation", { name: "Main" })).toBeTruthy();
    expect(screen.queryByText(/Tester account/)).toBeNull();
  });
});
