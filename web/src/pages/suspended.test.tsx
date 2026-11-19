import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { fakeApi, renderApp, session } from "../test/render";
import { canExport } from "./Suspended";

function suspendedSession(exportUntil: string) {
  return {
    ...session,
    account: {
      ...session.account,
      suspension: { suspended_at: "2026-11-19T10:00:00Z", reason: "Sent to a bought list.", export_until: exportUntil },
    },
  };
}

describe("a suspended account", () => {
  it("sees why, the export and sign out in place of any page", async () => {
    const api = fakeApi({
      "GET /v1/sessions/current": () => ({ status: 200, body: suspendedSession("2999-12-19T10:00:00Z") }),
      "GET /v1/campaigns": (_, url) =>
        url.searchParams.get("cursor")
          ? { status: 200, body: { data: [{ id: "c2" }], next_cursor: null } }
          : { status: 200, body: { data: [{ id: "c1" }], next_cursor: "next" } },
      "DELETE /v1/sessions/current": () => ({ status: 204 }),
    });
    const created = vi.fn(() => "blob:campaigns");
    const { createObjectURL, revokeObjectURL } = URL;
    URL.createObjectURL = created;
    URL.revokeObjectURL = vi.fn();
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    const user = userEvent.setup();
    renderApp("/campaigns", api.fetch);

    expect(await screen.findByRole("heading", { name: "Your account is suspended" })).toBeTruthy();
    expect(screen.getByText(/Sent to a bought list\./)).toBeTruthy();
    expect(screen.getByText(/reply to the email we sent pat@example.com/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Download contacts (CSV)" }).getAttribute("href")).toBe("/v1/contacts/export");

    await user.click(screen.getByRole("button", { name: "Download campaigns (JSON)" }));
    await waitFor(() => expect(created).toHaveBeenCalled());
    expect(api.calls.filter((c) => c.path === "/v1/campaigns")).toHaveLength(2);

    await user.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "DELETE")).toBe(true));
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = revokeObjectURL;
    vi.restoreAllMocks();
  });

  it("has no export after its 30 days", async () => {
    const api = fakeApi({
      "GET /v1/sessions/current": () => ({ status: 200, body: suspendedSession("2026-12-19T10:00:00Z") }),
    });
    vi.useFakeTimers({ now: new Date("2027-01-01T00:00:00Z"), toFake: ["Date"] });
    renderApp("/", api.fetch);
    expect(await screen.findByText(/The 30 days to download your data ended/)).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Download contacts (CSV)" })).toBeNull();
    vi.useRealTimers();
  });
});

describe("canExport", () => {
  it("is true until export_until", () => {
    expect(canExport("2026-12-19T10:00:00Z", new Date("2026-12-19T09:59:59Z"))).toBe(true);
    expect(canExport("2026-12-19T10:00:00Z", new Date("2026-12-19T10:00:00Z"))).toBe(false);
  });
});
