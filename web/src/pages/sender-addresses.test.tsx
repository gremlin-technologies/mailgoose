import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, fakeApi, renderApp, signedIn, signedOut } from "../test/render";

// Sender addresses and sending without a domain of your own (MG-85).

const pending = {
  id: "5b2c8e0a-1f43-4a8e-9d35-2b8a7c6e1a30",
  email: "hello@example.com",
  status: "pending",
  confirm_sent_at: "2026-11-21T10:00:00Z",
  confirm_expires_at: "2099-11-28T10:00:00Z",
  confirmed_at: null,
  created_at: "2026-11-21T10:00:00Z",
};
const confirmed = {
  id: "6c2c8e0a-1f43-4a8e-9d35-2b8a7c6e1a31",
  email: "orders@example.com",
  status: "confirmed",
  confirm_sent_at: null,
  confirm_expires_at: null,
  confirmed_at: "2026-11-20T10:00:00Z",
  created_at: "2026-11-20T09:00:00Z",
};

/** GET /v1/account with shared sending on (or off, with shared null). */
function account(shared: string | null) {
  return {
    "GET /v1/account": () => ({
      status: 200,
      body: {
        id: "7c1d0f7e-8f43-4a8e-9d35-2b8a7c6e1a10",
        name: "Pat's Bakery",
        from_name: null,
        from_email: "pat@example.com",
        postal_address: "12 Main St",
        time_zone: "Europe/London",
        missing_for_send: [],
        sending_mode: "full",
        send_limit: { daily: 1000, usual: 10000, reason: "New accounts start at 1,000 a day.", lowered_until: null, needs_domain: false },
        sending_slug: "pats-bakery",
        shared_address: shared,
        created_at: "2026-11-18T10:00:00Z",
      },
    }),
  };
}

describe("sender addresses", () => {
  it("lists, adds, resends and removes them, and says where email goes from", async () => {
    let list = [pending, confirmed];
    const api = fakeApi({
      ...signedIn,
      ...account("pats-bakery@shared.mailgoose.test"),
      "GET /v1/sender-addresses": () => ({ status: 200, body: { data: list, next_cursor: null } }),
      "POST /v1/sender-addresses": (body) => {
        const added = { ...pending, id: "7d2c8e0a-1f43-4a8e-9d35-2b8a7c6e1a32", email: (body as { email: string }).email };
        list = [...list, added];
        return { status: 201, body: added };
      },
      [`POST /v1/sender-addresses/${pending.id}/resend`]: () => ({ status: 200, body: pending }),
      [`DELETE /v1/sender-addresses/${confirmed.id}`]: () => {
        list = list.filter((a) => a.id !== confirmed.id);
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderApp("/sender-addresses", api.fetch);

    expect(await screen.findByText("hello@example.com")).toBeTruthy();
    expect(await screen.findByText("pats-bakery@shared.mailgoose.test")).toBeTruthy();
    expect(screen.getByText(/your own address, pat@example.com, which needs no slot/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Add your domain" }).getAttribute("href")).toBe("/domains");
    expect(screen.queryByRole("button", { name: "Resend the link to orders@example.com" })).toBeNull();

    await user.type(screen.getByLabelText("Add a sender address"), "news@example.com");
    await user.click(screen.getByRole("button", { name: "Add and send link" }));
    expect(await screen.findByText(/A confirm link went to news@example.com/)).toBeTruthy();
    expect(await screen.findByText("news@example.com")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Resend the link to hello@example.com" }));
    expect(await screen.findByText("Sent a new link.")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Remove orders@example.com" }));
    await user.click(screen.getByRole("button", { name: "Remove" }));
    await screen.findByText("news@example.com");
    expect(screen.queryByText("orders@example.com")).toBeNull();

    expect(api.calls.filter((c) => c.method !== "GET").map((c) => `${c.method} ${c.path} ${JSON.stringify(c.body ?? null)}`)).toEqual([
      `POST /v1/sender-addresses {"email":"news@example.com"}`,
      `POST /v1/sender-addresses/${pending.id}/resend null`,
      `DELETE /v1/sender-addresses/${confirmed.id} null`,
    ]);
  });

  it("stops adding at ten, and shows the API's refusals", async () => {
    const ten = Array.from({ length: 10 }, (_, i) => ({ ...confirmed, id: `${i}c2c8e0a-1f43-4a8e-9d35-2b8a7c6e1a31`, email: `s${i}@example.com` }));
    renderApp(
      "/sender-addresses",
      fakeApi({
        ...signedIn,
        ...account("pats-bakery@shared.mailgoose.test"),
        "GET /v1/sender-addresses": () => ({ status: 200, body: { data: ten, next_cursor: null } }),
      }).fetch,
    );
    expect(await screen.findByText(/That's 10, the most an account can have/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Add and send link" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows a rate limit from the API", async () => {
    const user = userEvent.setup();
    renderApp(
      "/sender-addresses",
      fakeApi({
        ...signedIn,
        ...account("pats-bakery@shared.mailgoose.test"),
        "GET /v1/sender-addresses": () => ({ status: 200, body: { data: [], next_cursor: null } }),
        "POST /v1/sender-addresses": () =>
          apiError(429, "sender_address_rate_limited", "This account has sent 10 sender address emails in the last 24 hours."),
      }).fetch,
    );
    await user.type(await screen.findByLabelText("Add a sender address"), "hello@example.com");
    await user.click(screen.getByRole("button", { name: "Add and send link" }));
    expect((await screen.findByRole("alert")).textContent).toContain("in the last 24 hours");
  });

  it("says when sending without a domain is off", async () => {
    renderApp(
      "/sender-addresses",
      fakeApi({
        ...signedIn,
        ...account(null),
        "GET /v1/sender-addresses": () => ({ status: 200, body: { data: [], next_cursor: null } }),
      }).fetch,
    );
    expect(await screen.findByText(/Sending without a domain of your own isn't on here/)).toBeTruthy();
  });
});

describe("confirming a sender address", () => {
  it("confirms on a press, signed out", async () => {
    const api = fakeApi({
      ...signedOut,
      "POST /v1/sender-addresses/confirm": () => ({
        status: 200,
        body: { email: "hello@example.com", account_name: "Pat's Bakery", confirmed_at: "2026-11-21T11:00:00Z" },
      }),
    });
    const user = userEvent.setup();
    renderApp("/sender-addresses/confirm?token=s1abc", api.fetch);
    await user.click(await screen.findByRole("button", { name: "Confirm this sender address" }));
    expect(await screen.findByText(/hello@example.com is confirmed as a sender address of Pat's Bakery/)).toBeTruthy();
    expect(api.calls.filter((c) => c.method === "POST")).toEqual([
      { method: "POST", path: "/v1/sender-addresses/confirm", body: { token: "s1abc" } },
    ]);
  });

  it("says when the link has expired", async () => {
    const user = userEvent.setup();
    renderApp(
      "/sender-addresses/confirm?token=s1old",
      fakeApi({
        ...signedOut,
        "POST /v1/sender-addresses/confirm": () => apiError(410, "sender_address_link_expired", "This link has expired."),
      }).fetch,
    );
    await user.click(await screen.findByRole("button", { name: "Confirm this sender address" }));
    expect((await screen.findByRole("alert")).textContent).toContain("expired");
  });
});

describe("domains are optional with shared sending", () => {
  const noDomains = { "GET /v1/domains": () => ({ status: 200, body: { data: [], next_cursor: null } }) };

  it("explains why a domain is still worth it, and what sending without one looks like", async () => {
    renderApp("/domains", fakeApi({ ...signedIn, ...noDomains, ...account("pats-bakery@shared.mailgoose.test") }).fetch);
    const panel = await screen.findByRole("region", { name: "Sending without a domain" });
    expect(within(panel).getByText(/We strongly recommend adding one/)).toBeTruthy();
    expect(within(panel).getByText("From Pat's Bakery <pats-bakery@shared.mailgoose.test>, with replies to you")).toBeTruthy();
    expect(within(panel).getByRole("link", { name: "confirmed sender address" }).getAttribute("href")).toBe("/sender-addresses");
  });

  it("says nothing of it with shared sending off", async () => {
    renderApp("/domains", fakeApi({ ...signedIn, ...noDomains, ...account(null) }).fetch);
    expect(await screen.findByText(/No domains yet/)).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Sending without a domain" })).toBeNull();
  });

  it("links Settings to the sender addresses", async () => {
    renderApp("/settings", fakeApi({ ...signedIn, ...account("pats-bakery@shared.mailgoose.test") }).fetch);
    expect((await screen.findByRole("link", { name: "Sender addresses" })).getAttribute("href")).toBe("/sender-addresses");
    expect(screen.getByText(/your emails go from pats-bakery@shared.mailgoose.test/)).toBeTruthy();
  });
});
