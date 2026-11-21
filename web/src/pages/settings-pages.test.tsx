import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, fakeApi, renderApp, signedIn } from "../test/render";

const location = () => screen.getByTestId("location").textContent;

const id = "3f9c2a1e-5b6d-4e7f-8a9b-0c1d2e3f4a5b";

function record(purpose: string, type: string, name: string, value: string, status = "unchecked", extra = {}) {
  return { purpose, type, name, value, priority: null, required: purpose !== "dmarc", status, found: [], detail: null, ...extra };
}

function domain(status: "pending" | "verified" | "failed", recordStatus = "unchecked") {
  const s = (want: string) => (recordStatus === "unchecked" ? "unchecked" : want);
  return {
    id,
    name: "news.example.com",
    status,
    mail_from_domain: "bounce.news.example.com",
    records: [
      record("dkim", "CNAME", "abc._domainkey.news.example.com", "abc.dkim.amazonses.com", s("found")),
      record("mail_from_mx", "MX", "bounce.news.example.com", "feedback-smtp.us-east-2.amazonses.com", s("wrong"), {
        priority: 10,
        found: ["mx.other.example"],
        detail: "No MX record points to feedback-smtp.us-east-2.amazonses.com.",
      }),
      record("mail_from_spf", "TXT", "bounce.news.example.com", "v=spf1 include:amazonses.com ~all", s("found")),
      record("dmarc", "TXT", "_dmarc.news.example.com", "v=DMARC1; p=none;", s("missing")),
    ],
    ses_status: null,
    created_at: "2026-11-18T10:00:00Z",
    last_checked_at: recordStatus === "unchecked" ? null : "2026-11-18T10:05:00Z",
    check_until: "2026-11-21T10:00:00Z",
    verified_at: status === "verified" ? "2026-11-18T10:05:00Z" : null,
    failed_at: null,
  };
}

describe("domains", () => {
  it("adds a domain, shows its records with copy buttons, and checks them", async () => {
    let domains: ReturnType<typeof domain>[] = [];
    const api = fakeApi({
      ...signedIn,
      "GET /v1/domains": () => ({ status: 200, body: { data: domains, next_cursor: null } }),
      "POST /v1/domains": (body) => {
        if ((body as { name: string }).name !== "news.example.com") return apiError(422, "invalid_domain", "That isn't a domain name.");
        domains = [domain("pending")];
        return { status: 201, body: domains[0] };
      },
      [`GET /v1/domains/${id}`]: () => ({ status: 200, body: domains[0] }),
      [`POST /v1/domains/${id}/verify`]: () => ({ status: 200, body: { ...domain("pending", "checked"), records_changed: false } }),
    });
    const user = userEvent.setup();
    renderApp("/domains", api.fetch);

    expect(await screen.findByText(/No domains yet/)).toBeTruthy();
    await user.type(screen.getByLabelText("Add a domain"), "news.example.com ");
    await user.click(screen.getByRole("button", { name: "Add domain" }));
    await waitFor(() => expect(location()).toBe(`/domains/${id}`));

    expect(await screen.findByRole("heading", { name: "news.example.com" })).toBeTruthy();
    expect(screen.getByText("Waiting for DNS")).toBeTruthy();
    expect(screen.getAllByText("Not checked yet")).toHaveLength(4);
    expect(screen.getByRole("heading", { name: "Suggested" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Copy DKIM CNAME value" }));
    expect(await navigator.clipboard.readText()).toBe("abc.dkim.amazonses.com");
    expect(screen.getByRole("button", { name: "Copy DKIM CNAME value" }).textContent).toBe("Copied");
    await user.click(screen.getByRole("button", { name: "Copy Bounce MX MX name" }));
    expect(await navigator.clipboard.readText()).toBe("bounce.news.example.com");

    await user.click(screen.getByRole("button", { name: "Check DNS now" }));
    expect(await screen.findAllByText("Found")).toHaveLength(2);
    expect(screen.getByText("Wrong value")).toBeTruthy();
    expect(screen.getByText("Missing")).toBeTruthy();
    expect(screen.getByText("No MX record points to feedback-smtp.us-east-2.amazonses.com.")).toBeTruthy();
    expect(screen.getByText("mx.other.example")).toBeTruthy();

    expect(api.calls.filter((c) => c.method === "POST")).toEqual([
      { method: "POST", path: "/v1/domains", body: { name: "news.example.com" } },
      { method: "POST", path: `/v1/domains/${id}/verify`, body: undefined },
    ]);
  });

  it("lists domains with their status, and removes one after a second click", async () => {
    let domains = [domain("verified", "checked")];
    const api = fakeApi({
      ...signedIn,
      "GET /v1/domains": () => ({ status: 200, body: { data: domains, next_cursor: null } }),
      [`GET /v1/domains/${id}`]: () => ({ status: 200, body: domains[0] }),
      [`DELETE /v1/domains/${id}`]: () => {
        domains = [];
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderApp("/domains", api.fetch);

    const row = (await screen.findByRole("link", { name: "news.example.com" })).closest("tr")!;
    expect(within(row).getByText("Verified")).toBeTruthy();
    await user.click(within(row).getByRole("link"));
    expect(await screen.findByText(/You can send from any address at news.example.com/)).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Remove domain" }));
    expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Remove news.example.com" }));
    await waitFor(() => expect(location()).toBe("/domains"));
    expect(await screen.findByText(/No domains yet/)).toBeTruthy();
  });

  it("shows why adding failed", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/domains": () => ({ status: 200, body: { data: [], next_cursor: null } }),
      "POST /v1/domains": () => apiError(409, "domain_taken", "Another account uses that domain.", "Contact support if it's yours."),
    });
    const user = userEvent.setup();
    renderApp("/domains", api.fetch);
    await user.type(await screen.findByLabelText("Add a domain"), "taken.example.com");
    await user.click(screen.getByRole("button", { name: "Add domain" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Another account uses that domain.");
    expect(location()).toBe("/domains");
  });
});

describe("API keys", () => {
  const key = (n: number, name: string, scope: "full" | "draft") => ({
    id: `00000000-0000-4000-8000-00000000000${n}`,
    name,
    scope,
    hint: `mg_live_…${n}abc`,
    created_at: "2026-11-18T10:00:00Z",
    last_used_at: null,
  });

  it("creates a key with a scope, shows its secret once, and revokes one", async () => {
    let keys = [key(1, "Laptop", "draft")];
    const api = fakeApi({
      ...signedIn,
      "GET /v1/api-keys": () => ({ status: 200, body: { data: keys, next_cursor: null } }),
      "POST /v1/api-keys": (body) => {
        const b = body as { name: string; scope: "full" | "draft" };
        const k = key(2, b.name, b.scope);
        keys = [...keys, k];
        return { status: 201, body: { key: k, secret: "mg_live_supersecretvalue2abc" } };
      },
      "DELETE /v1/api-keys/00000000-0000-4000-8000-000000000001": () => {
        keys = keys.filter((k) => k.name !== "Laptop");
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderApp("/api-keys", api.fetch);

    expect(await screen.findByText("Laptop")).toBeTruthy();
    expect((screen.getByRole("radio", { name: /Draft/ }) as HTMLInputElement).checked).toBe(true);
    await user.type(screen.getByLabelText("Name"), "Release script");
    await user.click(screen.getByRole("radio", { name: /Full/ }));
    await user.click(screen.getByRole("button", { name: "Create key" }));

    expect(await screen.findByText("mg_live_supersecretvalue2abc")).toBeTruthy();
    expect(screen.getByText(/only time we show the secret for Release script/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Copy the new API key" }));
    expect(await navigator.clipboard.readText()).toBe("mg_live_supersecretvalue2abc");
    expect(await screen.findByText("Release script")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "I've saved it" }));
    expect(screen.queryByText("mg_live_supersecretvalue2abc")).toBeNull();
    expect(screen.getByRole("button", { name: "Create key" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Revoke Laptop" }));
    expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Revoke Laptop" }));
    await waitFor(() => expect(screen.queryByText("Laptop")).toBeNull());

    expect(api.calls.filter((c) => c.method !== "GET")).toEqual([
      { method: "POST", path: "/v1/api-keys", body: { name: "Release script", scope: "full" } },
      { method: "DELETE", path: "/v1/api-keys/00000000-0000-4000-8000-000000000001", body: undefined },
    ]);
  });

  it("explains a refusal", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/api-keys": () => apiError(403, "forbidden", "This key can't manage keys.", "Use a full-scope key."),
    });
    renderApp("/api-keys", api.fetch);
    expect((await screen.findByRole("alert")).textContent).toContain("This key can't manage keys.");
    expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
  });
});

describe("settings send limit", () => {
  it("warns when the daily limit is lowered", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/account": () => ({
        status: 200,
        body: {
          id: "7c1d0f7e-8f43-4a8e-9d35-2b8a7c6e1a10",
          name: "Pat's Bakery",
          plan: "free",
          from_name: null,
          from_email: null,
          postal_address: "12 Main St",
          time_zone: "UTC",
          missing_for_send: [],
          send_limit: {
            daily: 1000,
            usual: 10000,
            reason: "Lowered automatically: 0.2% marked it as spam.",
            lowered_until: "2026-11-26T10:00:00Z",
            needs_domain: false,
          },
          created_at: "2026-11-18T10:00:00Z",
        },
      }),
    });
    renderApp("/settings", api.fetch);
    expect(await screen.findByText("Your daily send limit is lowered to 1,000.")).toBeTruthy();
    expect(screen.getByText(/0.2% marked it as spam/)).toBeTruthy();
  });
});

describe("settings send limit without a domain", () => {
  it("says the limit grows only with a domain of the account's own, and links to Domains (MG-98)", async () => {
    const reason =
      "Accounts without a verified domain of their own send at most 1,000 a day, from mailgoose's shared domain. Add your own domain to send more: the limit then doubles each week of clean sending, up to 10,000 a day.";
    const api = fakeApi({
      ...signedIn,
      "GET /v1/account": () => ({
        status: 200,
        body: {
          id: "7c1d0f7e-8f43-4a8e-9d35-2b8a7c6e1a10",
          name: "Pat's Bakery",
          plan: "free",
          from_name: null,
          from_email: null,
          postal_address: "12 Main St",
          time_zone: "UTC",
          missing_for_send: [],
          sending_mode: "full",
          sending_slug: "pats-bakery",
          shared_address: "pats-bakery@shared.mailgoose.test",
          send_limit: { daily: 1000, usual: 10000, reason, lowered_until: null, needs_domain: true },
          created_at: "2026-11-18T10:00:00Z",
        },
      }),
    });
    renderApp("/settings", api.fetch);
    expect(await screen.findByText(/Add your own domain to send more/)).toBeTruthy();
    const link = screen.getByRole("link", { name: "Add your own domain" });
    expect(link.getAttribute("href")).toBe("/domains");
    expect(screen.getByText(/Emails from mailgoose's shared domain go out under this name/)).toBeTruthy();
  });
});

describe("settings", () => {
  const account = {
    id: "7c1d0f7e-8f43-4a8e-9d35-2b8a7c6e1a10",
    name: "Pat's Bakery",
    plan: "free",
    from_name: "Pat",
    from_email: "news@patsbakery.example",
    postal_address: null,
    time_zone: "UTC",
    missing_for_send: ["postal_address"],
    sending_mode: "full",
    send_limit: {
      daily: 1000,
      usual: 10000,
      reason: "New accounts start at 1,000 a day; it doubles each week of clean sending, up to 10,000 a day.",
      lowered_until: null,
      needs_domain: false,
    },
    created_at: "2026-11-18T10:00:00Z",
  };

  it("saves only the fields that changed, and an emptied field as null", async () => {
    let current = { ...account };
    const api = fakeApi({
      ...signedIn,
      "GET /v1/account": () => ({ status: 200, body: current }),
      "PATCH /v1/account": (body) => {
        current = { ...current, ...(body as object), missing_for_send: [] };
        return { status: 200, body: current };
      },
    });
    const user = userEvent.setup();
    renderApp("/settings", api.fetch);

    expect(await screen.findByText(/Campaigns can't launch until you add a postal address/)).toBeTruthy();
    expect(screen.getByText(/Daily send limit: 1,000 recipients. New accounts start at 1,000 a day/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Save settings" }));
    expect(screen.getByText("Nothing changed.")).toBeTruthy();

    await user.clear(screen.getByLabelText("Default From address"));
    await user.type(screen.getByLabelText("Postal address"), "Pat's Bakery{Enter}12 Main St");
    await user.clear(screen.getByLabelText("Time zone"));
    await user.type(screen.getByLabelText("Time zone"), "America/Chicago");
    await user.click(screen.getByRole("button", { name: "Save settings" }));

    expect(await screen.findByText("Saved.")).toBeTruthy();
    expect(screen.queryByText(/Campaigns can't launch/)).toBeNull();
    expect((screen.getByLabelText("Time zone") as HTMLInputElement).value).toBe("America/Chicago");
    expect(api.calls.filter((c) => c.method === "PATCH")).toEqual([
      {
        method: "PATCH",
        path: "/v1/account",
        body: { from_email: null, postal_address: "Pat's Bakery\n12 Main St", time_zone: "America/Chicago" },
      },
    ]);
  });

  it("shows the API's reason a save failed", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/account": () => ({ status: 200, body: account }),
      "PATCH /v1/account": () => apiError(422, "invalid_time_zone", "Mars/Olympus isn't a time zone.", "Use an IANA name."),
    });
    const user = userEvent.setup();
    renderApp("/settings", api.fetch);
    await user.clear(await screen.findByLabelText("Time zone"));
    await user.type(screen.getByLabelText("Time zone"), "Mars/Olympus");
    await user.click(screen.getByRole("button", { name: "Save settings" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Mars/Olympus isn't a time zone.");
  });

  it("turns dark mode on and off, keeps it in this browser, and swaps the logo", async () => {
    const api = fakeApi({ ...signedIn, "GET /v1/account": () => ({ status: 200, body: account }) });
    const user = userEvent.setup();
    renderApp("/settings", api.fetch);

    const toggle = (await screen.findByRole("checkbox", { name: /Dark mode/ })) as HTMLInputElement;
    const logo = () => (screen.getByAltText("mailgoose") as HTMLImageElement).getAttribute("src");
    expect(toggle.checked).toBe(false);
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(logo()).toBe("/brand/wordmark.png");

    await user.click(toggle);
    expect(toggle.checked).toBe(true);
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(window.localStorage.getItem("mailgoose.theme")).toBe("dark");
    expect(logo()).toBe("/brand/wordmark-light.png");

    await user.click(toggle);
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(window.localStorage.getItem("mailgoose.theme")).toBeNull();
    expect(logo()).toBe("/brand/wordmark.png");
  });

  it("deletes the account only after its exact name is typed, then signs out", async () => {
    let confirm: string | null = null;
    const api = fakeApi({
      ...signedIn,
      "GET /v1/account": () => ({ status: 200, body: account }),
      "DELETE /v1/account": (_body, url) => {
        confirm = url.searchParams.get("confirm");
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderApp("/settings", api.fetch);

    const button = (await screen.findByRole("button", { name: "Delete account" })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    const input = screen.getByLabelText(/to confirm/);
    await user.type(input, "pat's bakery");
    expect(button.disabled).toBe(true);
    await user.clear(input);
    await user.type(input, "Pat's Bakery");
    expect(button.disabled).toBe(false);
    await user.click(button);

    await waitFor(() => expect(location()).toBe("/sign-in"));
    expect(confirm).toBe("Pat's Bakery");
  });
});
