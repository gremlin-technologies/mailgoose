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
    recipients: { all: true, tags: [] },
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
    expect(recipientsText({ all: true, tags: [] })).toBe("All subscribed contacts");
    expect(recipientsText({ all: false, tags: [news, vip] })).toBe("Contacts tagged news or vip");
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

