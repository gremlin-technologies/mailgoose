import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { fakeApi, renderApp, signedIn } from "../test/render";
import { leave } from "./Mailchimp";

const location = () => screen.getByTestId("location").textContent;

const bundleID = "44444444-4444-4444-8444-444444444444";
const importID = "55555555-5555-4555-8555-555555555555";
const fetchID = "66666666-6666-4666-8666-666666666666";
const emailID = "77777777-7777-4777-8777-777777777777";

const notConnected = {
  connected: false,
  can_connect: true,
  account_name: null,
  data_center: null,
  connected_at: null,
  expires_at: null,
};

function email(name: string, from: "template" | "campaign", extra = {}) {
  return {
    from,
    file: `${from}s/${name}.html`,
    name,
    subject: from === "campaign" ? `${name} subject` : "",
    preview_text: "",
    images: 0,
    errors: [],
    warnings: [],
    email_id: null,
    ...extra,
  };
}

function preview(extra = {}, contacts = {}) {
  return {
    source: "account_export",
    audiences: [
      {
        name: "Bakery news",
        members: 6,
        by_status: { subscribed: 3, unsubscribed: 1, cleaned: 1, pending: 1, transactional: 0, archived: 0 },
        groups: [],
      },
    ],
    contacts: {
      new: { subscribed: 3, unconfirmed: 1, unsubscribed: 1, bounced: 1 },
      existing_tagged: 2,
      existing_suppressed: 1,
      skipped: { invalid_email: 1, invalid_row: 0, suppressed: 0, not_subscribed: 0, archived: 0 },
      tags_to_create: ["regulars", "wholesale"],
      merge_suppressed: 1,
      merge_suppressed_examples: ["sam@example.com"],
      dropped_fields: ["Birthday", "Phone"],
      renamed_tags: [{ from: "VIP", to: "vip" }],
      names_cut: 0,
      with_optin_time: 3,
      newest_optin: "2026-11-01T10:00:00Z",
      plan: { plan: "free", upgrade: true, contacts: 600, limit: 500, tester: false, units: 1, month_cents: 500, year_cents: 5000 },
      holds: ["large_first_import"],
      waits: false,
      ...contacts,
    },
    emails: [
      email("Welcome", "template"),
      email("November offers", "campaign", {
        warnings: [{ code: "mailchimp_tag_removed", message: "*|REWARDS|* was removed: mailgoose has no rewards badge." }],
      }),
      email("Old promo", "campaign", {
        errors: [{ code: "invalid_html", message: "The HTML has an unclosed table on line 12.", line: 12 }],
      }),
    ],
    campaigns_available: 14,
    ignored_files: [{ path: "reports/opens.csv", reason: "unknown_file" }],
    ...extra,
  };
}

function bundle(extra = {}) {
  return {
    id: bundleID,
    file_name: "mailchimp-export.zip",
    bytes: 52000,
    status: "unconfirmed",
    created_at: "2026-11-22T10:00:00Z",
    expires_at: "2026-11-29T10:00:00Z",
    confirmed_at: null,
    import_id: null,
    campaigns: 10,
    preview: preview(),
    emails: { status: "none", total: 0, made: [], error: null },
    forms: [],
    ...extra,
  };
}

function anImport(status: string, processed: number) {
  return {
    id: importID,
    kind: "mailchimp",
    file_name: "mailchimp-export.zip",
    status,
    contact_status: "subscribed",
    columns: {},
    tags: [],
    total_rows: 6,
    processed_rows: processed,
    imported: status === "done" ? 5 : 0,
    skipped: status === "done" ? 1 : 0,
    skipped_by_reason: { invalid_email: status === "done" ? 1 : 0 },
    review: "none",
    hold_reason: null,
    hold_message: null,
    list_source: "export",
    list_source_detail: null,
    checks: { invalid_rows: 0, role_addresses: 0, disposable_addresses: 0 },
    error: null,
    created_at: "2026-11-22T10:01:00Z",
    started_at: "2026-11-22T10:01:01Z",
    finished_at: null,
  };
}

afterEach(() => vi.restoreAllMocks());

describe("Mailchimp import", () => {
  it("is linked from the contacts import page", async () => {
    renderApp("/contacts/import", fakeApi({ ...signedIn, "GET /v1/tags": () => ({ status: 200, body: { data: [], next_cursor: null } }) }).fetch);
    expect((await screen.findByRole("link", { name: "Import from Mailchimp" })).getAttribute("href")).toBe("/contacts/import/mailchimp");
  });

  it("hides connecting where it isn't set up", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/imports/mailchimp/connection": () => ({ status: 200, body: { ...notConnected, can_connect: false } }),
    });
    renderApp("/contacts/import/mailchimp", api.fetch);
    expect(await screen.findByRole("heading", { name: "Upload Mailchimp's export" })).toBeTruthy();
    await waitFor(() => expect(api.calls.some((c) => c.path === "/v1/imports/mailchimp/connection")).toBe(true));
    expect(screen.queryByRole("button", { name: "Connect Mailchimp" })).toBeNull();
  });

  it("uploads an export and opens its preview", async () => {
    let posted: Record<string, unknown> = {};
    const api = fakeApi({
      ...signedIn,
      "GET /v1/imports/mailchimp/connection": () => ({ status: 200, body: notConnected }),
      "POST /v1/imports/mailchimp/bundles": (body) => {
        posted = body as Record<string, unknown>;
        return { status: 201, body: bundle({ file_name: "audience.csv" }) };
      },
      [`GET /v1/imports/mailchimp/bundles/${bundleID}`]: () => ({ status: 200, body: bundle() }),
    });
    const user = userEvent.setup();
    renderApp("/contacts/import/mailchimp", api.fetch);

    const file = new File(["Email Address,First Name\npat@example.com,Pat\n"], "audience.csv", { type: "text/csv" });
    await user.upload(await screen.findByLabelText(/Export file/), file);
    await user.type(await screen.findByLabelText(/^Audience name/), "Bakery news");
    await user.click(screen.getByRole("button", { name: "Upload and preview" }));

    await waitFor(() => expect(location()).toBe(`/contacts/import/mailchimp/${bundleID}`), { timeout: 3000 });
    expect((posted.file as File).name).toBe("audience.csv");
    expect(await (posted.file as Blob).text()).toBe("Email Address,First Name\npat@example.com,Pat\n");
    expect(posted.audience_name).toBe("Bakery news");
  });

  it("refuses a file over 25 MB before uploading it", async () => {
    const api = fakeApi({ ...signedIn, "GET /v1/imports/mailchimp/connection": () => ({ status: 200, body: notConnected }) });
    const user = userEvent.setup();
    renderApp("/contacts/import/mailchimp", api.fetch);
    const big = new File(["x"], "huge.zip", { type: "application/zip" });
    Object.defineProperty(big, "size", { value: 26 << 20 });
    await user.upload(await screen.findByLabelText(/Export file/), big);
    expect(await screen.findByText("huge.zip is larger than 25 MB.")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Upload and preview" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("connects Mailchimp by leaving for its page", async () => {
    const to = vi.spyOn(leave, "to").mockImplementation(() => {});
    const api = fakeApi({
      ...signedIn,
      "GET /v1/imports/mailchimp/connection": () => ({ status: 200, body: notConnected }),
      "POST /v1/imports/mailchimp/connect": () => ({
        status: 200,
        body: { authorize_url: "https://login.mailchimp.com/oauth2/authorize?state=x", expires_at: "2026-11-22T10:10:00Z" },
      }),
    });
    const user = userEvent.setup();
    renderApp("/contacts/import/mailchimp", api.fetch);
    await user.click(await screen.findByRole("button", { name: "Connect Mailchimp" }));
    await waitFor(() => expect(to).toHaveBeenCalledWith("https://login.mailchimp.com/oauth2/authorize?state=x"));
  });

  it("fetches the picked audiences from a connected account, then shows the preview", async () => {
    let posted: unknown;
    let polls = 0;
    const done = bundle({ file_name: null, preview: preview({ source: "connected_account", segment_tags: [{ audience: "Bakery news", tag: "segment: Regulars", members: 2 }], templates_not_fetched: 4 }) });
    const fetchBody = (status: string, read: number) => ({
      id: fetchID,
      status,
      step: status === "running" ? "members" : "",
      audience: "Bakery news",
      members_read: read,
      members_total: 6,
      audience_ids: ["a1b2c3"],
      segment_ids: ["40213"],
      campaigns: 5,
      error: null,
      bundle_id: status === "done" ? bundleID : null,
      bundle: status === "done" ? done : null,
      created_at: "2026-11-22T10:00:00Z",
      finished_at: null,
    });
    const api = fakeApi({
      ...signedIn,
      "GET /v1/imports/mailchimp/connection": () => ({
        status: 200,
        body: { ...notConnected, connected: true, account_name: "Corner Bakery", data_center: "us21", connected_at: "2026-11-22T09:00:00Z", expires_at: "2026-11-23T09:00:00Z" },
      }),
      "GET /v1/imports/mailchimp/audiences": () => ({
        status: 200,
        body: {
          audiences: [
            { id: "a1b2c3", name: "Bakery news", double_optin: true, members: { subscribed: 3, unsubscribed: 1, cleaned: 1, total: 6 } },
            { id: "d4e5f6", name: "Wholesale", double_optin: false, members: { subscribed: 1, unsubscribed: 0, cleaned: 0, total: 1 } },
          ],
        },
      }),
      "POST /v1/imports/mailchimp/fetch": (body) => {
        posted = body;
        return { status: 202, body: fetchBody("queued", 0) };
      },
      [`GET /v1/imports/mailchimp/fetch/${fetchID}`]: () => {
        polls++;
        return { status: 200, body: polls === 1 ? fetchBody("running", 2) : fetchBody("done", 6) };
      },
      [`GET /v1/imports/mailchimp/bundles/${bundleID}`]: () => ({ status: 200, body: done }),
    });
    const user = userEvent.setup();
    renderApp("/contacts/import/mailchimp", api.fetch);

    expect(await screen.findByText(/Connected to Corner Bakery/)).toBeTruthy();
    const fetchButton = await screen.findByRole("button", { name: "Fetch and preview" });
    expect((fetchButton as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("checkbox", { name: /Bakery news/ }));
    await user.type(screen.getByLabelText(/Saved segments/), "40213");
    await user.clear(screen.getByLabelText("Newest campaigns to bring as emails"));
    await user.type(screen.getByLabelText("Newest campaigns to bring as emails"), "5");
    await user.click(fetchButton);

    await waitFor(() => expect(location()).toBe(`/contacts/import/mailchimp/fetch/${fetchID}`), { timeout: 3000 });
    expect(posted).toEqual({ audience_ids: ["a1b2c3"], segment_ids: ["40213"], campaigns: 5 });
    expect(await screen.findByText("Reading members of Bakery news: 2 of 6")).toBeTruthy();
    await waitFor(() => expect(location()).toBe(`/contacts/import/mailchimp/${bundleID}`), { timeout: 3000 });
    expect(await screen.findByText("segment: Regulars holds the 2 members of that saved segment in Bakery news.")).toBeTruthy();
    expect(screen.getByText(/your 4 templates aren't included/)).toBeTruthy();
  });

  it("shows why a fetch stopped", async () => {
    const api = fakeApi({
      ...signedIn,
      [`GET /v1/imports/mailchimp/fetch/${fetchID}`]: () => ({
        status: 200,
        body: {
          id: fetchID,
          status: "failed",
          step: "",
          audience: null,
          members_read: 0,
          members_total: 0,
          audience_ids: ["a1b2c3"],
          segment_ids: [],
          campaigns: 10,
          error: { code: "mailchimp_access_revoked", message: "Mailchimp no longer allows mailgoose to read this account." },
          bundle_id: null,
          bundle: null,
          created_at: "2026-11-22T10:00:00Z",
          finished_at: "2026-11-22T10:00:05Z",
        },
      }),
    });
    renderApp(`/contacts/import/mailchimp/fetch/${fetchID}`, api.fetch);
    expect(await screen.findByText("The fetch stopped: Mailchimp no longer allows mailgoose to read this account.")).toBeTruthy();
  });

  it("previews an export in plain words", async () => {
    const api = fakeApi({ ...signedIn, [`GET /v1/imports/mailchimp/bundles/${bundleID}`]: () => ({ status: 200, body: bundle() }) });
    renderApp(`/contacts/import/mailchimp/${bundleID}`, api.fetch);

    expect(await screen.findByRole("heading", { name: "mailchimp-export.zip" })).toBeTruthy();
    expect(screen.getByText("Bakery news: 6 members")).toBeTruthy();
    const stats = screen.getByText("Subscribed").closest("dl") as HTMLElement;
    expect(within(stats).getAllByRole("definition").map((d) => d.textContent)).toEqual(["3", "1", "1", "1"]);
    expect(screen.getByText(/2 addresses are already your contacts and only gain tags; 1 of them come off your list/)).toBeTruthy();
    expect(screen.getByText(/1 row is skipped/)).toBeTruthy();
    expect(screen.getByText(/1 address is in more than one audience .* such as sam@example.com/)).toBeTruthy();
    expect(screen.getByText("New tags: regulars, wholesale.")).toBeTruthy();
    expect(screen.getByText("VIP becomes vip.")).toBeTruthy();
    expect(screen.getByText(/These Mailchimp fields aren't kept: Birthday, Phone/)).toBeTruthy();
    expect(screen.getByText(/600 contacts needs the paid plan: \$5 a month, or \$50 a year/)).toBeTruthy();
    expect(screen.getByText("Held for review: it's your account's first import of more than 2,000 contacts.")).toBeTruthy();
    expect(screen.getByText(/1 template and the newest 2 campaigns of 14 become HTML emails/)).toBeTruthy();
    expect(screen.getByText("*|REWARDS|* was removed: mailgoose has no rewards badge.")).toBeTruthy();
    expect(screen.getByText("The HTML has an unclosed table on line 12.")).toBeTruthy();
    expect(screen.getByText(/1 file in the export isn't imported/)).toBeTruthy();
  });

  it("says when the subscribed contacts would wait for the paid plan", async () => {
    const waiting = bundle({ preview: preview({}, { waits: true, holds: [] }) });
    const api = fakeApi({ ...signedIn, [`GET /v1/imports/mailchimp/bundles/${bundleID}`]: () => ({ status: 200, body: waiting }) });
    renderApp(`/contacts/import/mailchimp/${bundleID}`, api.fetch);
    expect(await screen.findByText(/so they wait for the paid plan/)).toBeTruthy();
    expect(screen.queryByText(/Held for review/)).toBeNull();
  });

  it("confirms only with the permission box ticked, then shows progress and what's next", async () => {
    let posted: unknown;
    let current = bundle();
    let importPolls = 0;
    const confirmed = (emails: object) =>
      bundle({ status: "confirmed", confirmed_at: "2026-11-22T10:01:00Z", import_id: importID, campaigns: 3, emails });
    const made = [
      email("Welcome", "template", { email_id: "88888888-8888-4888-8888-888888888888" }),
      email("Old promo", "campaign", {
        email_id: emailID,
        errors: [{ code: "invalid_html", message: "The HTML has an unclosed table on line 12.", line: 12 }],
      }),
    ];
    const api = fakeApi({
      ...signedIn,
      [`GET /v1/imports/mailchimp/bundles/${bundleID}`]: () => ({ status: 200, body: current }),
      [`POST /v1/imports/mailchimp/bundles/${bundleID}/confirm`]: (body) => {
        posted = body;
        current = confirmed({ status: "done", total: 2, made, error: null });
        return { status: 202, body: confirmed({ status: "running", total: 2, made: made.slice(0, 1), error: null }) };
      },
      [`GET /v1/imports/${importID}`]: () => {
        importPolls++;
        return { status: 200, body: importPolls === 1 ? anImport("running", 2) : anImport("done", 6) };
      },
    });
    const user = userEvent.setup();
    renderApp(`/contacts/import/mailchimp/${bundleID}`, api.fetch);

    const importButton = (await screen.findByRole("button", { name: "Import" })) as HTMLButtonElement;
    expect(importButton.disabled).toBe(true);
    const campaigns = screen.getByLabelText("Newest campaigns to bring as emails (of 14)");
    expect((campaigns as HTMLInputElement).value).toBe("10");
    await user.clear(campaigns);
    await user.type(campaigns, "3");
    await user.click(screen.getByRole("checkbox", { name: "Everyone in this file has given me permission to email them." }));
    expect(importButton.disabled).toBe(false);
    await user.click(importButton);

    await waitFor(() => expect(posted).toEqual({ permission_confirmed: true, campaigns: 3 }));
    expect(await screen.findByText("2 of 6 contacts")).toBeTruthy();
    expect(await screen.findByText("1 of 2 emails made")).toBeTruthy();
    expect(await screen.findByText("5 contacts added, 1 skipped.", {}, { timeout: 3000 })).toBeTruthy();
    expect(await screen.findByText("2 emails made.", {}, { timeout: 3000 })).toBeTruthy();
    expect(screen.getByRole("link", { name: "See the import report" }).getAttribute("href")).toBe(`/contacts/imports/${importID}`);

    const next = screen.getByRole("heading", { name: "What's next" }).nextElementSibling as HTMLElement;
    expect(within(next).getAllByRole("link").map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["Verify your sending domain", "/domains"],
      ["Set up your signup forms", "/forms"],
      ["Old promo", `/emails/${emailID}`],
      ["Send a test", "/campaigns/new"],
    ]);
  });
  it("gives each audience a draft signup form, tagged with the groups picked", async () => {
    let posted: unknown;
    const audience = (name: string, groups: string[], double_optin: boolean) => ({
      name,
      members: 3,
      by_status: { subscribed: 3, unsubscribed: 0, cleaned: 0, pending: 0, transactional: 0, archived: 0 },
      groups,
      double_optin,
    });
    const fetched = bundle({
      file_name: null,
      preview: preview({
        source: "connected_account",
        campaigns_available: 0,
        emails: [],
        audiences: [audience("Bakery news", ["Interests: Bread", "Interests: Cakes"], false), audience("Wholesale", ["Interests: Bread"], true)],
      }),
    });
    const forms = [
      { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", name: "Bakery news", audience: "Bakery news", new: true },
      { id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", name: "Trade signups", audience: "Wholesale", new: false },
    ];
    const api = fakeApi({
      ...signedIn,
      [`GET /v1/imports/mailchimp/bundles/${bundleID}`]: () => ({ status: 200, body: fetched }),
      [`POST /v1/imports/mailchimp/bundles/${bundleID}/confirm`]: (body) => {
        posted = body;
        return {
          status: 202,
          body: bundle({ status: "confirmed", confirmed_at: "2026-11-22T10:01:00Z", preview: null, emails: { status: "done", total: 0, made: [], error: null }, forms }),
        };
      },
    });
    const user = userEvent.setup();
    renderApp(`/contacts/import/mailchimp/${bundleID}`, api.fetch);

    const group = await screen.findByRole("group", { name: "Signup forms" });
    expect(within(group).getByText(/Each audience gets a signup form in its name/)).toBeTruthy();
    expect(within(group).getByText("Bakery news: Mailchimp didn't ask signups to confirm by email. mailgoose's form does.")).toBeTruthy();
    expect(within(group).getByText("Wholesale: Mailchimp asked signups to confirm by email. mailgoose's form does.")).toBeTruthy();
    // A group two audiences share is offered once.
    expect(within(group).getAllByRole("checkbox").map((c) => c.parentElement?.textContent)).toEqual(["Interests: Bread", "Interests: Cakes"]);
    await user.click(within(group).getByRole("checkbox", { name: "Interests: Cakes" }));
    await user.click(screen.getByRole("checkbox", { name: "Everyone in this file has given me permission to email them." }));
    await user.click(screen.getByRole("button", { name: "Import" }));

    await waitFor(() => expect(posted).toEqual({ permission_confirmed: true, campaigns: 1, form_tags: ["Interests: Cakes"] }));
    const list = (await screen.findByRole("heading", { name: "Signup forms" })).nextElementSibling as HTMLElement;
    expect(within(list).getAllByRole("link").map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["Bakery news", "/forms/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"],
      ["Trade signups", "/forms/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"],
    ]);
    expect(within(list).getByText("Draft")).toBeTruthy();
    expect(within(list).getByText("made by an earlier import")).toBeTruthy();
    expect(screen.getByText(/Check and save your new signup form above, so it takes signups/)).toBeTruthy();
  });
});
