import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { fakeApi, renderApp, signedIn } from "../test/render";
import { polling } from "./CampaignDetail";
import { share } from "./Report";

const campaignId = "66666666-6666-4666-8666-666666666666";
const base = `/v1/campaigns/${campaignId}`;
const slow = { timeout: 3000 };

const report = {
  campaign_id: campaignId,
  status: "sent",
  launched_at: "2026-11-19T10:00:00Z",
  tracking: true,
  recipients: 120,
  delivered: 117,
  bounced: 3,
  complaints: 0,
  unsubscribes: 2,
  opens: { unique: 48, estimated: true },
  clicks: { unique: 12, total: 19 },
  credits_used: 120,
  links: [
    { position: 1, url: "https://patsbakery.com/menu", varies: false, unique_clicks: 10, total_clicks: 15 },
    { position: 3, url: "https://patsbakery.com", varies: true, unique_clicks: 2, total_clicks: 4 },
  ],
  archived: false,
  as_of: "2026-11-19T12:00:00Z",
};

function server(rep: Record<string, unknown>, status = "sent") {
  const state = { report: rep };
  const api = fakeApi({
    ...signedIn,
    [`GET ${base}`]: () => ({
      status: 200,
      body: {
        id: campaignId,
        name: "May news",
        email_id: null,
        recipients: { all: true, tags: [] },
        send_at: null,
        tracking: true,
        status,
        version: 2,
        last_test: null,
        launch: { at: "2026-11-19T10:00:00Z", recipient_count: 120, credits_reserved: 120 },
        created_at: "2026-11-18T10:00:00Z",
        updated_at: "2026-11-18T10:00:00Z",
      },
    }),
    [`GET ${base}/report`]: () => ({ status: 200, body: state.report }),
  });
  return { ...api, state };
}


afterEach(() => {
  polling.every = 5000;
});

describe("report", () => {
  it("shares a count of the copies sent the way the CLI does", () => {
    expect(share(40, 120)).toBe("40 (33.3%)");
    expect(share(0, 0)).toBe("0");
    expect(share(1500, 3000)).toBe("1,500 (50.0%)");
  });

  it("shows the CLI's numbers, with opens marked as estimated, and clicks per link", async () => {
    const api = server(report);
    renderApp(`/campaigns/${campaignId}/report`, api.fetch);
    const numbers = await screen.findByLabelText("Numbers");
    const pairs = [...numbers.querySelectorAll(".tile")].map((t) => [t.querySelector("dt")!.textContent, t.querySelector("dd")!.textContent]);
    expect(pairs).toEqual([
      ["Sent", "120"],
      ["Delivered", "117 (97.5%)"],
      ["Bounced", "3 (2.5%)"],
      ["Complaints", "0 (0.0%)"],
      ["Unsubscribes", "2 (1.7%)"],
      ["Opens (estimated)", "48 (40.0%)"],
      ["Clicks", "12 (10.0%) unique, 19 total"],
      ["Credits used", "120"],
    ]);
    expect(screen.getByText(/Opens are an estimate/)).toBeTruthy();
    const links = screen.getByRole("list", { name: "Clicks per link" });
    expect(within(links).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "1. https://patsbakery.com/menu10 unique, 15 total",
      "3. https://patsbakery.com (differs per contact)2 unique, 4 total",
    ]);
    expect(await screen.findByRole("heading", { name: "May news: report" })).toBeTruthy();
  });

  it("says when opens and clicks weren't counted, and when nothing was sent", async () => {
    const api = server({ ...report, tracking: false, opens: null, clicks: null, links: [] });
    renderApp(`/campaigns/${campaignId}/report`, api.fetch);
    expect(await screen.findByText("Opens and clicks weren't counted for this campaign.")).toBeTruthy();
    expect(screen.queryByText("Opens (estimated)")).toBeNull();
    expect(screen.queryByRole("heading", { name: "Clicks per link" })).toBeNull();

    api.state.report = { ...report, status: "draft", launched_at: null, recipients: 0 };
    await userEvent.setup().click(screen.getByRole("button", { name: "Count again" }));
    expect(await screen.findByText(/Nothing sent yet/)).toBeTruthy();
  });

  it("says when the numbers are the archived ones", async () => {
    const api = server({ ...report, archived: true });
    renderApp(`/campaigns/${campaignId}/report`, api.fetch);
    expect(await screen.findByText(/when its raw events were deleted, 12 months after launch/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Count again" })).toBeNull();
  });

  it("counts again while the campaign is sending", async () => {
    polling.every = 50;
    const api = server({ ...report, status: "sending", delivered: 10 }, "sending");
    renderApp(`/campaigns/${campaignId}/report`, api.fetch);
    expect(await screen.findByText("10 (8.3%)", {}, slow)).toBeTruthy();
    expect(screen.getByText(/include those held for copies still to go/)).toBeTruthy();
    api.state.report = report;
    expect(await screen.findByText("117 (97.5%)", {}, slow)).toBeTruthy();
  });

  it("is linked from a sent campaign's page", async () => {
    const api = server(report);
    renderApp(`/campaigns/${campaignId}`, api.fetch);
    expect((await screen.findByRole("link", { name: "See the report" })).getAttribute("href")).toBe(`/campaigns/${campaignId}/report`);
  });
});
