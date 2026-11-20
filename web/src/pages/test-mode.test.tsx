import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, fakeApi, renderApp, session, signedOut } from "../test/render";

const inTestMode = {
  "GET /v1/sessions/current": () => ({
    status: 200,
    body: { ...session, account: { ...session.account, sending_mode: "test" } },
  }),
};

const pending = {
  id: "5b2c8e0a-1f43-4a8e-9d35-2b8a7c6e1a20",
  email: "qa@example.com",
  status: "pending",
  confirm_sent_at: "2026-11-20T10:00:00Z",
  confirm_expires_at: "2099-11-27T10:00:00Z",
  confirmed_at: null,
  created_at: "2026-11-20T10:00:00Z",
};
const confirmed = {
  id: "6c2c8e0a-1f43-4a8e-9d35-2b8a7c6e1a21",
  email: "lead@example.com",
  status: "confirmed",
  confirm_sent_at: null,
  confirm_expires_at: null,
  confirmed_at: "2026-11-19T10:00:00Z",
  created_at: "2026-11-19T09:00:00Z",
};

describe("the test mode banner", () => {
  it("shows on every page in test mode, linking to the test addresses", async () => {
    renderApp("/contacts", fakeApi(inTestMode).fetch);
    const note = (await screen.findByText("Test mode.")).closest("[role=status]") as HTMLElement;
    expect(note.textContent).toMatch(/only to its confirmed test addresses/);
    expect(within(note).getByRole("link", { name: "Manage test addresses" }).getAttribute("href")).toBe("/test-addresses");
  });

  it("isn't there with full sending", async () => {
    renderApp("/contacts", fakeApi({ "GET /v1/sessions/current": () => ({ status: 200, body: session }) }).fetch);
    expect(await screen.findByRole("navigation", { name: "Main" })).toBeTruthy();
    expect(screen.queryByText("Test mode.")).toBeNull();
  });
});

describe("test addresses", () => {
  it("lists, adds, resends and removes them", async () => {
    let list = [pending, confirmed];
    const api = fakeApi({
      ...inTestMode,
      "GET /v1/test-addresses": () => ({ status: 200, body: { data: list, next_cursor: null } }),
      "POST /v1/test-addresses": (body) => {
        const added = { ...pending, id: "7d2c8e0a-1f43-4a8e-9d35-2b8a7c6e1a22", email: (body as { email: string }).email };
        list = [...list, added];
        return { status: 201, body: added };
      },
      [`POST /v1/test-addresses/${pending.id}/resend`]: () => ({ status: 200, body: pending }),
      [`DELETE /v1/test-addresses/${confirmed.id}`]: () => {
        list = list.filter((a) => a.id !== confirmed.id);
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderApp("/test-addresses", api.fetch);

    expect(await screen.findByText("qa@example.com")).toBeTruthy();
    expect(screen.getByText(/your own address, pat@example.com, which needs no slot/)).toBeTruthy();
    expect(screen.getByText("Confirmed")).toBeTruthy();
    expect(screen.getByText("Pending")).toBeTruthy();
    // Only a pending address has a link to resend.
    expect(screen.queryByRole("button", { name: "Resend the link to lead@example.com" })).toBeNull();

    await user.type(screen.getByLabelText("Add a test address"), "new@example.com");
    await user.click(screen.getByRole("button", { name: "Add and send link" }));
    expect(await screen.findByText(/A confirm link went to new@example.com/)).toBeTruthy();
    expect(await screen.findByText("new@example.com")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Resend the link to qa@example.com" }));
    expect(await screen.findByText("Sent a new link.")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Remove lead@example.com" }));
    await user.click(screen.getByRole("button", { name: "Remove" }));
    await screen.findByText("new@example.com");
    expect(screen.queryByText("lead@example.com")).toBeNull();

    expect(api.calls.filter((c) => c.method !== "GET").map((c) => `${c.method} ${c.path} ${JSON.stringify(c.body ?? null)}`)).toEqual([
      `POST /v1/test-addresses {"email":"new@example.com"}`,
      `POST /v1/test-addresses/${pending.id}/resend null`,
      `DELETE /v1/test-addresses/${confirmed.id} null`,
    ]);
  });

  it("stops adding at five, and shows why the API refused", async () => {
    const five = [0, 1, 2, 3, 4].map((i) => ({ ...confirmed, id: `${i}c2c8e0a-1f43-4a8e-9d35-2b8a7c6e1a21`, email: `t${i}@example.com` }));
    renderApp(
      "/test-addresses",
      fakeApi({ ...inTestMode, "GET /v1/test-addresses": () => ({ status: 200, body: { data: five, next_cursor: null } }) }).fetch,
    );
    expect(await screen.findByText(/That's 5, the most an account can have/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Add and send link" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows the API's refusal", async () => {
    const user = userEvent.setup();
    renderApp(
      "/test-addresses",
      fakeApi({
        ...inTestMode,
        "GET /v1/test-addresses": () => ({ status: 200, body: { data: [], next_cursor: null } }),
        "POST /v1/test-addresses": () =>
          apiError(422, "test_address_is_owner", "That's the owner's own sign-in address, which test mode always sends to, so it needs no slot."),
      }).fetch,
    );
    await user.type(await screen.findByLabelText("Add a test address"), "pat@example.com");
    await user.click(screen.getByRole("button", { name: "Add and send link" }));
    expect((await screen.findByRole("alert")).textContent).toContain("needs no slot");
  });
});

describe("confirming a test address", () => {
  it("confirms on a press, signed out", async () => {
    const api = fakeApi({
      ...signedOut,
      "POST /v1/test-addresses/confirm": () => ({
        status: 200,
        body: { email: "qa@example.com", account_name: "Pat's Bakery", confirmed_at: "2026-11-20T11:00:00Z" },
      }),
    });
    const user = userEvent.setup();
    renderApp("/test-addresses/confirm?token=t1abc", api.fetch);
    await user.click(await screen.findByRole("button", { name: "Confirm this test address" }));
    expect(await screen.findByText(/qa@example.com is confirmed as a test address of Pat's Bakery/)).toBeTruthy();
    expect(api.calls.filter((c) => c.method === "POST")).toEqual([
      { method: "POST", path: "/v1/test-addresses/confirm", body: { token: "t1abc" } },
    ]);
  });

  it("says when the link has expired", async () => {
    const user = userEvent.setup();
    renderApp(
      "/test-addresses/confirm?token=t1old",
      fakeApi({
        ...signedOut,
        "POST /v1/test-addresses/confirm": () =>
          apiError(410, "test_address_link_expired", "That confirm link is older than 7 days, so it no longer works."),
      }).fetch,
    );
    await user.click(await screen.findByRole("button", { name: "Confirm this test address" }));
    expect((await screen.findByRole("alert")).textContent).toContain("older than 7 days");
  });
});
