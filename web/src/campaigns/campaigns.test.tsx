import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { fakeApi, renderApp, signedIn } from "../test/render";
import { fromLocalInput, recipientsText, toLocalInput, type Campaign } from "./campaign";
import { polling } from "./CampaignDetail";
import { confirmationOf, problemsOf } from "./steps";

const emailId = "55555555-5555-4555-8555-555555555555";
const campaignId = "66666666-6666-4666-8666-666666666666";
const news = { id: "11111111-1111-4111-8111-111111111111", name: "news", contact_count: 40, created_at: "2026-11-18T10:00:00Z" };
const vip = { id: "22222222-2222-4222-8222-222222222222", name: "vip", contact_count: 15, created_at: "2026-11-18T10:00:00Z" };
const base = `/v1/campaigns/${campaignId}`;
const slow = { timeout: 3000 };

type Reply = { status: number; body?: unknown };
type Route = (body: unknown, url: URL) => Reply;

function fail(status: number, code: string, message: string, details?: Record<string, unknown>): Reply {
  return { status, body: { error: { code, message, hint: "", ...(details ? { details } : {}) } } };
}

function campaign(over: Partial<Campaign> = {}): Campaign {
  return {
    id: campaignId,
    name: "May news",
    email_id: emailId,
    recipients: { all: true, tags: [], segment: null },
    recipient_count: 120,
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

const summary = {
  campaign_id: campaignId,
  name: "May news",
  subject: "Fresh bread in May",
  from: { name: "Pat's Bakery", email: "hello@patsbakery.com" },
  reply_to: [],
  recipient_count: 120,
  send_at: null,
  credits: 120,
  balance: 1000,
  campaign_version: 1,
  email_version: 4,
  warnings: [{ code: "missing_alt", message: "An image has no alt text." }],
};

/** A fake API holding one campaign, with routes for every campaign call. */
function server(start: Campaign, extra: Record<string, Route> = {}) {
  const state = { campaign: start };
  const reach = (r: { all?: boolean; tag_ids?: string[] }) =>
    r.all ? 120 : (r.tag_ids ?? []).reduce((n, id) => n + (id === news.id ? 40 : id === vip.id ? 15 : 0), 0);
  const api = fakeApi({
    ...signedIn,
    "GET /v1/emails": () => ({
      status: 200,
      body: {
        data: [{ id: emailId, name: "May letter", kind: "blocks", subject: "Fresh bread in May", block_count: 4, updated_at: "2026-11-18T10:00:00Z" }],
        next_cursor: null,
      },
    }),
    [`GET /v1/emails/${emailId}`]: () => ({ status: 200, body: { id: emailId, name: "May letter" } }),
    "GET /v1/tags": () => ({ status: 200, body: { data: [news, vip], next_cursor: null } }),
    "POST /v1/campaigns/recipient-count": (b) => ({
      status: 200,
      body: { recipient_count: reach((b as { recipients: { all?: boolean; tag_ids?: string[] } }).recipients) },
    }),
    "GET /v1/campaigns": () => ({ status: 200, body: { data: [state.campaign], next_cursor: null } }),
    "POST /v1/campaigns": () => ({ status: 201, body: state.campaign }),
    [`GET ${base}`]: () => ({ status: 200, body: state.campaign }),
    ...extra,
  });
  return { ...api, state };
}

afterEach(() => {
  polling.every = 5000;
});

describe("campaign helpers", () => {
  it("round-trips a send time through the date input, in local time", () => {
    const iso = fromLocalInput("2026-12-01T09:30")!;
    expect(new Date(iso).getHours()).toBe(9);
    expect(toLocalInput(iso)).toBe("2026-12-01T09:30");
    expect(fromLocalInput("2026-12-01")).toBeUndefined();
    expect(toLocalInput(null)).toBe("");
  });

  it("says who a campaign goes to", () => {
    expect(recipientsText({ all: true, tags: [], segment: null })).toBe("All subscribed contacts");
    expect(recipientsText({ all: false, tags: [news, vip], segment: null })).toBe("Contacts tagged news or vip");
    expect(recipientsText({ all: false, tags: [], segment: { id: "s1", name: "Clickers" } })).toBe("Contacts in segment Clickers");
  });

  it("reads a launch summary and problems from an error's details, skipping anything malformed", () => {
    const f = { code: "confirmation_required", message: "", hint: "", status: 409 };
    expect(confirmationOf({ ...f, details: { summary, code: "ABCD-1234", expires_at: "2026-11-19T10:15:00Z" } })?.summary.credits).toBe(120);
    expect(confirmationOf({ ...f, details: { summary: { subject: 1 }, code: "x", expires_at: "y" } })).toBeUndefined();
    expect(confirmationOf({ ...f, code: "not_ready" })).toBeUndefined();
    const problems = [{ code: "untested", message: "No test yet.", hint: "Send a test." }, { code: 3 }, null];
    expect(problemsOf({ ...f, code: "not_ready", details: { problems } })).toEqual([problems[0]]);
  });
});

describe("campaign list", () => {
  it("lists campaigns with their status, and filters by status", async () => {
    const api = server(campaign({ status: "scheduled", send_at: "2026-12-01T09:30:00Z", launch: { at: "2026-11-19T10:00:00Z", recipient_count: 120, credits_reserved: 120 } }));
    const user = userEvent.setup();
    renderApp("/campaigns", api.fetch);
    const row = (await screen.findByRole("link", { name: "May news" })).closest("tr")!;
    expect(within(row).getByText("Scheduled")).toBeTruthy();
    expect(within(row).getByText("120 contacts")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Scheduled" }));
    await waitFor(() => expect(api.calls.at(-1)?.path).toBe("/v1/campaigns"));
    expect(screen.getByTestId("location").textContent).toBe("/campaigns?status=scheduled");
  });

  it("shows a sent campaign's report numbers, each linking to its report, and dashes for the rest (MG-111)", async () => {
    const sent = {
      ...campaign({ status: "sent", launch: { at: "2026-11-19T10:00:00Z", recipient_count: 120, credits_reserved: 120 } }),
      summary: {
        recipients: 120,
        delivered: 117,
        bounced: 3,
        opens: 48,
        clicks: null,
        delivered_rate: 0.975,
        bounce_rate: 0.025,
        open_rate: 48 / 117,
        click_rate: null,
        archived: false,
      },
    };
    const draft = { ...campaign(), id: "77777777-7777-4777-8777-777777777777", name: "June news", summary: null };
    const api = server(sent, { "GET /v1/campaigns": () => ({ status: 200, body: { data: [sent, draft], next_cursor: null } }) });
    renderApp("/campaigns", api.fetch);
    const row = (await screen.findByRole("link", { name: "May news" })).closest("tr")!;
    const cells = [...row.querySelectorAll("td.num")].map((td) => td.textContent);
    expect(cells).toEqual(["117", "41.0%", "–", "2.5%"]);
    const open = within(row).getByRole("link", { name: "Open rate: 41.0%, see the report" });
    expect(open.getAttribute("href")).toBe(`/campaigns/${campaignId}/report`);
    const draftRow = screen.getByRole("link", { name: "June news" }).closest("tr")!;
    expect([...draftRow.querySelectorAll("td.num")].map((td) => td.textContent)).toEqual(["–", "–", "–", "–"]);
    expect(screen.getByText(/Open and click rates are of the emails delivered/)).toBeTruthy();
  });
});

describe("new campaign", () => {
  it("picks an email, tags with a live count, a send time and tracking, then opens the draft", async () => {
    const api = server(campaign());
    const user = userEvent.setup();
    renderApp("/campaigns/new", api.fetch);
    const form = await screen.findByRole("form", { name: "Campaign" });
    const count = () => within(form).getByRole("status", { name: "Recipient count" }).textContent;
    await waitFor(() => expect(count()).toBe("Reaches 120 subscribed contacts now."));

    await user.type(within(form).getByLabelText("Campaign name"), "May news");
    await user.selectOptions(within(form).getByLabelText("Email"), emailId);
    await user.click(within(form).getByLabelText("Contacts with any of these tags"));
    expect(count()).toBe("Pick at least one tag.");
    await user.click(within(form).getByLabelText("news"));
    await waitFor(() => expect(count()).toBe("Reaches 40 subscribed contacts now."));
    await user.click(within(form).getByLabelText("vip"));
    await waitFor(() => expect(count()).toBe("Reaches 55 subscribed contacts now."));

    await user.click(within(form).getByLabelText("At a set time"));
    fireEvent.change(within(form).getByLabelText("Send at (your time)"), { target: { value: "2026-12-01T09:30" } });
    await user.click(within(form).getByLabelText(/Count opens and clicks/));
    await user.click(within(form).getByRole("button", { name: "Save draft" }));

    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/campaigns/${campaignId}`));
    expect(api.calls.find((c) => c.method === "POST" && c.path === "/v1/campaigns")?.body).toEqual({
      name: "May news",
      email_id: emailId,
      recipients: { tag_ids: [news.id, vip.id] },
      send_at: fromLocalInput("2026-12-01T09:30"),
      tracking: false,
    });
  });

  it("says what's missing before saving", async () => {
    const api = server(campaign());
    const user = userEvent.setup();
    renderApp("/campaigns/new", api.fetch);
    const form = await screen.findByRole("form", { name: "Campaign" });
    await user.type(within(form).getByLabelText("Campaign name"), "May news");
    await user.selectOptions(within(form).getByLabelText("Email"), emailId);
    await user.click(within(form).getByLabelText("Contacts with any of these tags"));
    await user.click(within(form).getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Pick at least one tag, or send to all subscribed contacts.")).toBeTruthy();
    await user.click(within(form).getByLabelText("All subscribed contacts"));
    await user.click(within(form).getByLabelText("At a set time"));
    await user.click(within(form).getByRole("button", { name: "Save draft" }));
    expect(await screen.findByText("Pick the date and time to send it.")).toBeTruthy();
    expect(api.calls.some((c) => c.method === "POST" && c.path === "/v1/campaigns")).toBe(false);
  });
});

describe("segment recipients (MG-116)", () => {
  const clickers = {
    id: "77777777-7777-4777-8777-777777777777",
    name: "Clickers",
    conditions: { clicked: [campaignId] },
    valid: true,
    problems: [],
    created_at: "2026-11-20T10:00:00Z",
    updated_at: "2026-11-20T10:00:00Z",
  };
  const stale = { ...clickers, id: "88888888-8888-4888-8888-888888888888", name: "Old VIPs", valid: false };
  const segmentRoutes: Record<string, Route> = {
    "GET /v1/segments": () => ({ status: 200, body: { data: [clickers, stale], next_cursor: null } }),
    "POST /v1/campaigns/recipient-count": (b) => {
      const r = (b as { recipients: { all?: boolean; segment_id?: string } }).recipients;
      if (r.segment_id === stale.id) {
        return fail(422, "segment_invalid", 'The segment "Old VIPs" has a condition it can\'t use now: its tag was deleted.');
      }
      return { status: 200, body: { recipient_count: r.segment_id ? 9 : 120 } };
    },
  };

  it("offers saved segments with the count, says when one can't be used, and saves the pick", async () => {
    const api = server(campaign(), segmentRoutes);
    const user = userEvent.setup();
    renderApp("/campaigns/new", api.fetch);
    const form = await screen.findByRole("form", { name: "Campaign" });
    const count = () => within(form).getByRole("status", { name: "Recipient count" }).textContent;
    await user.type(within(form).getByLabelText("Campaign name"), "Thanks");
    await user.selectOptions(within(form).getByLabelText("Email"), emailId);

    await user.click(within(form).getByLabelText("Contacts in a saved segment"));
    expect(count()).toBe("Pick a segment.");
    const pick = within(form).getByLabelText("Segment");
    expect(within(pick).getAllByRole("option").map((o) => o.textContent)).toEqual(["Pick a segment", "Clickers", "Old VIPs (needs fixing)"]);
    await user.selectOptions(pick, stale.id);
    await waitFor(() => expect(count()).toBe('The segment "Old VIPs" has a condition it can\'t use now: its tag was deleted.'));
    await user.selectOptions(pick, clickers.id);
    await waitFor(() => expect(count()).toBe("Reaches 9 subscribed contacts now."));

    await user.click(within(form).getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/campaigns/${campaignId}`));
    expect(api.calls.find((c) => c.method === "POST" && c.path === "/v1/campaigns")?.body).toMatchObject({
      recipients: { segment_id: clickers.id },
    });
  });

  it("opens a draft's form on its segment", async () => {
    const api = server(campaign({ recipients: { all: false, tags: [], segment: { id: clickers.id, name: "Clickers" } } }), segmentRoutes);
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Edit campaign" }));
    const form = await screen.findByRole("form", { name: "Campaign" });
    expect((within(form).getByLabelText("Contacts in a saved segment") as HTMLInputElement).checked).toBe(true);
    expect((within(form).getByLabelText("Segment") as HTMLSelectElement).value).toBe(clickers.id);
    await waitFor(() => expect(within(form).getByRole("status", { name: "Recipient count" }).textContent).toBe("Reaches 9 subscribed contacts now."));
  });

  it("keeps the other choices working when segments won't load", async () => {
    const api = server(campaign());
    const user = userEvent.setup();
    renderApp("/campaigns/new", api.fetch);
    const form = await screen.findByRole("form", { name: "Campaign" });
    await user.click(within(form).getByLabelText("Contacts in a saved segment"));
    expect(within(form).getByText(/Couldn't load your segments/)).toBeTruthy();
    await user.click(within(form).getByLabelText("All subscribed contacts"));
    await waitFor(() => expect(within(form).getByRole("status", { name: "Recipient count" }).textContent).toBe("Reaches 120 subscribed contacts now."));
  });
});

describe("draft campaign", () => {
  it("runs the email check and lists its errors and warnings", async () => {
    const api = server(campaign(), {
      [`POST /v1/emails/${emailId}/check`]: () => ({
        status: 200,
        body: {
          ok: false,
          errors: [{ code: "missing_unsubscribe", message: "Add an unsubscribe link." }],
          warnings: [{ code: "broken_link", message: "This link didn't answer.", url: "https://example.com/menu" }],
        },
      }),
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Check email" }));
    const result = await screen.findByRole("region", { name: "Email check" });
    expect(within(result).getByText("1 error to fix before you can launch.")).toBeTruthy();
    expect(within(within(result).getByRole("list", { name: "Errors" })).getByText("Add an unsubscribe link.")).toBeTruthy();
    expect(within(result).getByRole("list", { name: "Warnings" }).textContent).toBe("https://example.com/menu: This link didn't answer.");
    expect(screen.getByRole("link", { name: "Fix it in the editor" }).getAttribute("href")).toBe(`/emails/${emailId}`);
  });

  it("opens the email's editor, whose Save and exit comes back to the campaign (MG-83)", async () => {
    const email = {
      id: emailId,
      name: "May letter",
      kind: "blocks",
      version: 1,
      document: {
        schema_version: 1,
        settings: { subject: "May", preview_text: "", from_name: "", from_email: null, reply_to: null, background_color: "#f4f4f5", content_width: 600 },
        blocks: [{ id: "b_footer001", type: "footer", text: "" }],
      },
      created_at: "2026-11-18T10:00:00Z",
      updated_at: "2026-11-18T10:00:00Z",
    };
    const api = server(campaign(), { [`GET /v1/emails/${emailId}`]: () => ({ status: 200, body: email }) });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("link", { name: "May letter" }));
    await user.click(await screen.findByRole("button", { name: "Save and exit" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/campaigns/${campaignId}`));
  });

  it("sends a test to the addresses given, and shows what stops it", async () => {
    let refuse = false;
    const api = server(campaign(), {
      [`POST ${base}/test`]: (b) => {
        if (refuse) {
          return fail(422, "not_ready", "This campaign can't be tested yet.", {
            problems: [{ code: "unverified_from", message: "The From address isn't on a verified domain.", hint: "Verify the domain first." }],
          });
        }
        const to = (b as { to?: string[] }).to ?? ["pat@example.com"];
        return {
          status: 200,
          body: { campaign: campaign({ version: 1, last_test: { at: "2026-11-19T10:00:00Z", version: 1, email_version: 4 } }), to, sent_today: 2, daily_limit: 50 },
        };
      },
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    expect(await screen.findByText("No test sent yet.")).toBeTruthy();
    await user.type(screen.getByLabelText("Send it to (optional)"), "a@patsbakery.com, b@patsbakery.com");
    await user.click(screen.getByRole("button", { name: "Send test" }));
    expect(await screen.findByText("Sent to a@patsbakery.com, b@patsbakery.com. 2 of 50 test emails used today.")).toBeTruthy();
    expect(api.calls.find((c) => c.path === `${base}/test`)?.body).toEqual({ to: ["a@patsbakery.com", "b@patsbakery.com"] });

    refuse = true;
    await user.clear(screen.getByLabelText("Send it to (optional)"));
    await user.click(screen.getByRole("button", { name: "Send test" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText(/isn't on a verified domain/).textContent).toContain("Verify the domain first.");
    expect(api.calls.filter((c) => c.path === `${base}/test`).at(-1)?.body).toEqual({});
  });

  it.each([
    { shared: true, links: ["Sender addresses", "domain"], says: /Send from your sign-in address, pat@example.com, which works as it is/ },
    { shared: false, links: ["Domains"], says: /Verify its domain on Domains/ },
  ])("words an unverified From itself, with links, not the CLI hint (MG-100, shared sending $shared)", async ({ shared, links, says }) => {
    const unverified = {
      code: "unverified_from",
      message: "The From address hi@pendingbakery.com isn't on a verified sending domain of this account.",
      hint: "Verify the address with `mailgoose sender-addresses add hi@pendingbakery.com`.",
      from_email: "hi@pendingbakery.com",
      shared_sending: shared,
    };
    const api = server(campaign(), {
      [`POST ${base}/test`]: () => fail(422, "not_ready", "This campaign can't be tested yet.", { problems: [unverified] }),
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Send test" }));
    const item = within(await screen.findByRole("alert")).getByRole("listitem");
    expect(item.textContent).toMatch(says);
    expect(item.textContent).not.toContain("mailgoose sender-addresses");
    expect(item.textContent?.includes("sign-in address")).toBe(shared);
    expect(within(item).getAllByRole("link").map((a) => [a.textContent, a.getAttribute("href")])).toEqual(
      links.map((name) => [name, name === "Sender addresses" ? "/sender-addresses" : "/domains"]),
    );
  });

  it("launches only after the summary is confirmed, sending its code back", async () => {
    const api = server(campaign({ last_test: { at: "2026-11-19T10:00:00Z", version: 1, email_version: 4 } }), {
      [`POST ${base}/launch`]: (b) => {
        const code = (b as { confirm?: string }).confirm;
        if (!code) {
          return fail(409, "confirmation_required", "Confirm to launch.", { summary, code: "ABCD-1234", expires_at: "2026-11-19T10:15:00Z" });
        }
        const launched = campaign({ status: "sending", version: 2, launch: { at: "2026-11-19T10:05:00Z", recipient_count: 120, credits_reserved: 120 } });
        api.state.campaign = launched;
        return { status: 200, body: { campaign: launched, recipient_count: 120, credits_reserved: 120 } };
      },
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Review and launch" }));
    const panel = await screen.findByRole("region", { name: "Launch summary" });
    expect(within(panel).getByText("Fresh bread in May")).toBeTruthy();
    expect(within(panel).getByText("Pat's Bakery <hello@patsbakery.com>")).toBeTruthy();
    expect(within(panel).getByText("120 credits, of the 1,000 you have")).toBeTruthy();
    expect(within(panel).getByText("An image has no alt text.")).toBeTruthy();
    // Nothing launched yet: the first call carries no code.
    expect(api.calls.filter((c) => c.path === `${base}/launch`).map((c) => c.body)).toEqual([{}]);

    await user.click(within(panel).getByRole("button", { name: "Send to 120 contacts now" }));
    expect(await screen.findByText("Launched. It's on its way to 120 contacts.")).toBeTruthy();
    expect(api.calls.filter((c) => c.path === `${base}/launch`).map((c) => c.body)).toEqual([{}, { confirm: "ABCD-1234" }]);
    expect(screen.getByText("Sending")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Review and launch" })).toBeNull();
  });

  it("says when a test or launch goes from the shared domain, with a link to add a domain (MG-85)", async () => {
    const shared = { name: "Pat's Bakery", email: "pats-bakery@shared.mailgoose.test" };
    const replyTo = [{ name: "", email: "pat@example.com" }];
    const api = server(campaign({ last_test: { at: "2026-11-19T10:00:00Z", version: 1, email_version: 4 } }), {
      [`POST ${base}/test`]: () => ({
        status: 200,
        body: {
          campaign: campaign({ last_test: { at: "2026-11-19T10:00:00Z", version: 1, email_version: 4 } }),
          to: ["pat@example.com"],
          sent_today: 1,
          daily_limit: 50,
          from: shared,
          reply_to: replyTo,
          shared_domain: true,
          sender: "pat@example.com",
        },
      }),
      [`POST ${base}/launch`]: () =>
        fail(409, "confirmation_required", "Confirm to launch.", {
          summary: { ...summary, from: shared, reply_to: replyTo, shared_domain: true, sender: "pat@example.com" },
          code: "ABCD-1234",
          expires_at: "2026-11-19T10:15:00Z",
        }),
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Send test" }));
    const nudge = "Sending from pats-bakery@shared.mailgoose.test, mailgoose's shared domain, with replies to pat@example.com.";
    const testNote = (await screen.findByText(new RegExp(nudge.replace(/\./g, "\\.")))).closest("[role=note]") as HTMLElement;
    expect(within(testNote).getByRole("link", { name: "Add your own domain" }).getAttribute("href")).toBe("/domains");

    await user.click(screen.getByRole("button", { name: "Review and launch" }));
    const panel = await screen.findByRole("region", { name: "Launch summary" });
    expect(within(panel).getByRole("note").textContent).toBe(`${nudge} Add your own domain for better deliverability and trust.`);
    expect(within(panel).getByText("pat@example.com")).toBeTruthy();
  });

  it("says when no From is set, so a test or launch goes as the sign-in address (MG-108)", async () => {
    const shared = { name: "Pat's Bakery", email: "pats-bakery@shared.mailgoose.test" };
    const replyTo = [{ name: "", email: "pat@example.com" }];
    const tested = campaign({ last_test: { at: "2026-11-19T10:00:00Z", version: 1, email_version: 4 } });
    const api = server(tested, {
      [`POST ${base}/test`]: () => ({
        status: 200,
        body: {
          campaign: tested,
          to: ["pat@example.com"],
          sent_today: 1,
          daily_limit: 50,
          from: shared,
          reply_to: replyTo,
          shared_domain: true,
          sender: "pat@example.com",
          default_from: true,
        },
      }),
      [`POST ${base}/launch`]: () =>
        fail(409, "confirmation_required", "Confirm to launch.", {
          summary: { ...summary, from: shared, reply_to: replyTo, shared_domain: true, sender: "pat@example.com", default_from: true },
          code: "ABCD-1234",
          expires_at: "2026-11-19T10:15:00Z",
        }),
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Send test" }));
    const lead = "No From is set on the email or in Settings, so it goes as your sign-in address, pat@example.com.";
    const nudge = "Sending from pats-bakery@shared.mailgoose.test, mailgoose's shared domain, with replies to pat@example.com.";
    const testNote = (await screen.findByText(/No From is set on the email/)).closest("[role=note]") as HTMLElement;
    expect(testNote.textContent).toBe(`${lead} ${nudge} Add your own domain for better deliverability and trust.`);
    expect(within(testNote).getByRole("link", { name: "Settings" }).getAttribute("href")).toBe("/settings");

    await user.click(screen.getByRole("button", { name: "Review and launch" }));
    const panel = await screen.findByRole("region", { name: "Launch summary" });
    expect(within(panel).getByRole("note").textContent).toBe(`${lead} ${nudge} Add your own domain for better deliverability and trust.`);
    expect(within(panel).getByText("Pat's Bakery <pats-bakery@shared.mailgoose.test>")).toBeTruthy();
  });

  it("shows no shared-domain note for a From on a verified domain", async () => {
    const api = server(campaign({ last_test: { at: "2026-11-19T10:00:00Z", version: 1, email_version: 4 } }), {
      [`POST ${base}/launch`]: () =>
        fail(409, "confirmation_required", "Confirm to launch.", { summary, code: "ABCD-1234", expires_at: "2026-11-19T10:15:00Z" }),
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Review and launch" }));
    const panel = await screen.findByRole("region", { name: "Launch summary" });
    expect(within(panel).queryByRole("note")).toBeNull();
  });

  it("lists what stops a launch, and drops a summary whose code no longer works", async () => {
    let answer: "not_ready" | "summary" | "stale" = "not_ready";
    const api = server(campaign(), {
      [`POST ${base}/launch`]: () => {
        if (answer === "not_ready") {
          return fail(422, "not_ready", "This campaign can't launch yet.", {
            problems: [
              { code: "untested", message: "No test since the last change.", hint: "Send a test first." },
              { code: "credits_short", message: "You need 20 more credits.", hint: "Buy credits." },
            ],
          });
        }
        if (answer === "summary") {
          answer = "stale";
          return fail(409, "confirmation_required", "Confirm to launch.", { summary, code: "ABCD-1234", expires_at: "2026-11-19T10:15:00Z" });
        }
        return fail(422, "invalid_confirmation", "The campaign changed since that summary.", { reason: "changed" });
      },
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Review and launch" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "No test since the last change. Send a test first.",
      "You need 20 more credits. Buy credits.",
    ]);

    answer = "summary";
    await user.click(screen.getByRole("button", { name: "Review and launch" }));
    const panel = await screen.findByRole("region", { name: "Launch summary" });
    await user.click(within(panel).getByRole("button", { name: "Send to 120 contacts now" }));
    expect(await screen.findByText("The campaign changed since that summary.")).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Launch summary" })).toBeNull();
    expect(screen.getByRole("button", { name: "Review and launch" })).toBeTruthy();
  });

  it("edits the draft with the version read, and reloads when someone else changed it", async () => {
    let conflict = false;
    const api = server(campaign({ version: 3 }), {
      [`PATCH ${base}`]: (b) => {
        if (conflict) return fail(409, "version_conflict", "Stale.");
        const body = b as { name: string; tracking: boolean };
        api.state.campaign = campaign({ version: 4, name: body.name, tracking: body.tracking, recipient_count: undefined });
        return { status: 200, body: api.state.campaign };
      },
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Edit campaign" }));
    const form = await screen.findByRole("form", { name: "Campaign" });
    await user.type(within(form).getByLabelText("Campaign name"), " letter");
    await user.click(within(form).getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("heading", { name: "May news letter" })).toBeTruthy();
    expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
      name: "May news letter",
      email_id: emailId,
      recipients: { all: true },
      send_at: null,
      tracking: true,
      version: 3,
    });
    // The live count from the page's first load stays.
    expect(screen.getByText("All subscribed contacts: 120 contacts now")).toBeTruthy();

    conflict = true;
    await user.click(screen.getByRole("button", { name: "Edit campaign" }));
    await user.click(within(await screen.findByRole("form", { name: "Campaign" })).getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("This campaign was changed somewhere else, so it has been reloaded.")).toBeTruthy();
    expect(screen.queryByRole("form", { name: "Campaign" })).toBeNull();
  });
});

describe("scheduled campaign", () => {
  const scheduled = () =>
    campaign({
      status: "scheduled",
      send_at: "2026-12-01T09:30:00Z",
      version: 2,
      launch: { at: "2026-11-19T10:00:00Z", recipient_count: 120, credits_reserved: 120 },
    });

  it("moves the send time", async () => {
    const api = server(scheduled(), {
      [`POST ${base}/reschedule`]: (b) => ({ status: 200, body: { ...scheduled(), send_at: (b as { send_at: string }).send_at } }),
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    const form = await screen.findByRole("form", { name: "Move send time" });
    expect((within(form).getByLabelText("New send time (your time)") as HTMLInputElement).value).toBe(toLocalInput("2026-12-01T09:30:00Z"));
    fireEvent.change(within(form).getByLabelText("New send time (your time)"), { target: { value: "2026-12-02T08:00" } });
    await user.click(within(form).getByRole("button", { name: "Move send time" }));
    expect(await screen.findByText(/^Moved\. It now goes out/)).toBeTruthy();
    expect(api.calls.find((c) => c.path === `${base}/reschedule`)?.body).toEqual({ send_at: fromLocalInput("2026-12-02T08:00") });
  });

  it("cancels only after a second press", async () => {
    const api = server(scheduled(), {
      [`POST ${base}/cancel`]: () => ({ status: 200, body: { ...scheduled(), status: "cancelled" } }),
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Cancel campaign" }));
    expect(api.calls.some((c) => c.path === `${base}/cancel`)).toBe(false);
    await user.click(screen.getByRole("button", { name: "Yes, cancel it" }));
    expect(await screen.findByText("Cancelled. Nobody gets it, and the credits held for it came back.")).toBeTruthy();
    expect(screen.getAllByText("Cancelled").length).toBeGreaterThan(0);
    expect(screen.queryByRole("form", { name: "Move send time" })).toBeNull();
  });
});

describe("sending campaign", () => {
  it("looks again until it has finished sending", async () => {
    polling.every = 50;
    const launch = { at: "2026-11-19T10:00:00Z", recipient_count: 120, credits_reserved: 120 };
    const api = server(campaign({ status: "sending", launch }));
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    expect(await screen.findByText("Sending to 120 contacts now. This page updates by itself.", {}, slow)).toBeTruthy();
    api.state.campaign = campaign({ status: "sent", launch });
    expect(await screen.findByText("Sent to 120 contacts.", {}, slow)).toBeTruthy();
    // Once it has finished, it isn't looked at again.
    const gets = () => api.calls.filter((c) => c.method === "GET" && c.path === base).length;
    const seen = gets();
    await new Promise((r) => setTimeout(r, 200));
    expect(gets()).toBe(seen);
  });
});

describe("sliced campaign", () => {
  const launch = { at: "2026-11-19T10:00:00Z", recipient_count: 3000, credits_reserved: 3000 };
  const waiting = () =>
    campaign({ status: "sending", launch, slice: { state: "slice_hold", size: 300, release_at: "2026-11-19T11:05:00Z" } });

  it("says the rest waits for the slice, and cancels the rest after a second press", async () => {
    const api = server(waiting(), {
      [`POST ${base}/cancel`]: () => ({
        status: 200,
        body: { ...waiting(), status: "cancelled", slice: { state: "cancelled", size: 300, release_at: null } },
      }),
    });
    const user = userEvent.setup();
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    expect(await screen.findByText("Sent to the first 300 contacts of 3,000 so far; the rest waits.")).toBeTruthy();
    expect(screen.getByText(/^It went to the first 300 recipients\. The rest goes .* if their bounces and complaints are within the limits\.$/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Cancel the rest" }));
    expect(api.calls.some((c) => c.path === `${base}/cancel`)).toBe(false);
    await user.click(screen.getByRole("button", { name: "Yes, cancel it" }));
    expect(await screen.findByText("Cancelled. The rest won't get it, and the credits held for them came back.")).toBeTruthy();
    expect(
      screen.getByText("It went to the first 300 recipients, then was cancelled, so the rest wasn't sent and its credits came back."),
    ).toBeTruthy();
    expect(screen.queryByText(/The rest goes .* if their bounces and complaints are within the limits/)).toBeNull();
  });

  it("says when a bad slice stopped the rest", async () => {
    const api = server(campaign({ status: "cancelled", launch, slice: { state: "stopped", size: 300, release_at: null } }));
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    expect(await screen.findByText(/the rest wasn't sent and its credits came back/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Cancel the rest" })).toBeNull();
  });
});

