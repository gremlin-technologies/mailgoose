import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { currentPolicyVersion, policyChangesPath, policyVersions } from "../policyVersions.gen";
import { apiError, fakeApi, renderApp, signedIn, signedOut } from "../test/render";
import { formatPolicyDate, policies } from "./Policies";

const account = (terms: object) => ({
  id: "7c1d0f7e-8f43-4a8e-9d35-2b8a7c6e1a10",
  name: "Pat's Bakery",
  plan: "free",
  from_name: "Pat",
  from_email: "news@patsbakery.example",
  postal_address: "Pat's Bakery\n12 Main St",
  time_zone: "UTC",
  missing_for_send: [],
  send_limit: { daily: 10000, usual: 10000, reason: null, lowered_until: null, needs_domain: false },
  terms: {
    accepted_version: currentPolicyVersion.version,
    accepted_at: "2026-11-19T10:00:00Z",
    current_version: currentPolicyVersion.version,
    current_effective: currentPolicyVersion.effective,
    acceptance_needed: false,
    ...terms,
  },
  created_at: "2026-11-18T10:00:00Z",
});

describe("terms versions", () => {
  it("each policy page shows the version, its date and a link to what changed", async () => {
    for (const p of policies) {
      const { unmount } = renderApp(p.path, fakeApi(signedOut).fetch);
      await screen.findByRole("heading", { level: 1, name: p.title });
      const line = `Version ${currentPolicyVersion.version}, effective ${formatPolicyDate(currentPolicyVersion.effective)}.`;
      expect(screen.getByText(line, { exact: false })).toBeTruthy();
      expect(screen.getByRole("link", { name: "What changed" }).getAttribute("href")).toBe(policyChangesPath);
      unmount();
    }
  });

  it("lists what changed in every version, open to anyone", async () => {
    renderApp(policyChangesPath, fakeApi(signedOut).fetch);
    expect(await screen.findByRole("heading", { level: 1, name: "What changed in our policies" })).toBeTruthy();
    for (const v of policyVersions) {
      expect(screen.getByRole("heading", { level: 2, name: new RegExp(`^Version ${v.version}`) })).toBeTruthy();
      for (const c of v.changes) expect(screen.getByText(c)).toBeTruthy();
    }
  });

  it("formats a policy date", () => {
    expect(formatPolicyDate("2026-11-19")).toBe("November 19, 2026");
  });

  it("asks for the new terms before the page, then shows it", async () => {
    let current = account({ accepted_version: "2026-01-01", acceptance_needed: true });
    const api = fakeApi({
      ...signedIn,
      "GET /v1/account": () => ({ status: 200, body: current }),
      "POST /v1/account/terms": () => {
        current = account({});
        return { status: 200, body: current };
      },
    });
    const user = userEvent.setup();
    renderApp("/settings", api.fetch);

    expect(await screen.findByRole("heading", { name: "Our terms have changed" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save settings" })).toBeNull();
    const changes = screen.getByRole("link", { name: "See what changed" });
    expect(changes.getAttribute("href")).toBe(policyChangesPath);
    expect(changes.getAttribute("target")).toBe("_blank");
    // Signing out still works.
    expect(screen.getByRole("button", { name: "Sign out" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: `Accept version ${currentPolicyVersion.version}` }));
    expect(await screen.findByRole("button", { name: "Save settings" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Our terms have changed" })).toBeNull();
    expect(api.calls.filter((c) => c.method === "POST")).toEqual([
      { method: "POST", path: "/v1/account/terms", body: { version: currentPolicyVersion.version } },
    ]);
  });

  it("shows why accepting failed", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/account": () => ({ status: 200, body: account({ accepted_version: "2026-01-01", acceptance_needed: true }) }),
      "POST /v1/account/terms": () => apiError(422, "terms_version_not_current", "The current terms are version 2027-01-01."),
    });
    const user = userEvent.setup();
    renderApp("/settings", api.fetch);
    await user.click(await screen.findByRole("button", { name: /^Accept version/ }));
    expect((await screen.findByRole("alert")).textContent).toContain("The current terms are version 2027-01-01.");
    expect(screen.getByRole("heading", { name: "Our terms have changed" })).toBeTruthy();
  });

  it("doesn't ask when the account is up to date", async () => {
    const api = fakeApi({ ...signedIn, "GET /v1/account": () => ({ status: 200, body: account({}) }) });
    renderApp("/settings", api.fetch);
    expect(await screen.findByRole("button", { name: "Save settings" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Our terms have changed" })).toBeNull();
  });
});
