import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, fakeApi, renderApp, signedIn } from "../test/render";

const campaignId = "66666666-6666-4666-8666-666666666666";
const base = `/v1/campaigns/${campaignId}`;

const ann = {
  contact_id: "11111111-1111-4111-8111-111111111111",
  email: "ann@patsbakery.com",
  first_name: "Ann",
  last_name: "Baker",
  at: "2026-11-19T11:00:00Z",
};
const bo = { contact_id: "22222222-2222-4222-8222-222222222222", email: "bo@patsbakery.com", first_name: null, last_name: null, at: null };

function server(list: (url: URL) => ReturnType<typeof apiError>) {
  return fakeApi({
    ...signedIn,
    [`GET ${base}`]: () => ({
      status: 200,
      body: {
        id: campaignId,
        name: "May news",
        email_id: null,
        recipients: { all: true, tags: [], segment: null },
        send_at: null,
        tracking: true,
        status: "sent",
        version: 2,
        last_test: null,
        launch: { at: "2026-11-19T10:00:00Z", recipient_count: 120, credits_reserved: 120 },
        created_at: "2026-11-18T10:00:00Z",
        updated_at: "2026-11-18T10:00:00Z",
      },
    }),
    [`GET ${base}/report/recipients`]: (_body, url) => list(url),
  });
}

describe("report recipients (MG-112)", () => {
  it("lists who opened, with when, a page at a time, and offers the CSV", async () => {
    const seen: string[] = [];
    const api = server((url) => {
      seen.push(url.search);
      return url.searchParams.get("cursor")
        ? { status: 200, body: { data: [{ ...ann, contact_id: bo.contact_id, email: bo.email }], next_cursor: null } }
        : { status: 200, body: { data: [ann], next_cursor: "c2" } };
    });
    renderApp(`/campaigns/${campaignId}/report/opened`, api.fetch);
    expect(await screen.findByRole("heading", { name: "Who opened" })).toBeTruthy();
    const table = await screen.findByRole("table");
    expect(within(table).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Email", "Name", "First opened"]);
    expect(within(table).getByRole("link", { name: "ann@patsbakery.com" }).getAttribute("href")).toBe(`/contacts/${ann.contact_id}`);
    expect(within(table).getByText("Ann Baker")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Download CSV" }).getAttribute("href")).toBe(`${base}/report/recipients/export?activity=opened`);
    expect(screen.getByRole("link", { name: "Opened" }).getAttribute("aria-current")).toBe("page");

    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByRole("link", { name: "bo@patsbakery.com" })).toBeTruthy();
    expect(seen.at(-1)).toContain("cursor=c2");
    expect((screen.getByRole("button", { name: "Next" }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: "Previous" }));
    expect(await screen.findByRole("link", { name: "ann@patsbakery.com" })).toBeTruthy();
  });

  it("narrows clicks to one link, and shows no time for those who didn't open", async () => {
    const seen: string[] = [];
    const api = server((url) => {
      seen.push(url.search);
      return { status: 200, body: { data: url.searchParams.get("activity") === "not_opened" ? [bo] : [ann], next_cursor: null } };
    });
    renderApp(`/campaigns/${campaignId}/report/clicked?link=3`, api.fetch);
    expect(await screen.findByRole("heading", { name: "Who clicked link 3" })).toBeTruthy();
    await screen.findByRole("link", { name: "ann@patsbakery.com" });
    expect(seen.at(-1)).toContain("link=3");
    expect(screen.getByRole("link", { name: "Download CSV" }).getAttribute("href")).toBe(
      `${base}/report/recipients/export?activity=clicked&link=3`,
    );

    await userEvent.setup().click(screen.getByRole("link", { name: "Didn't open" }));
    expect(await screen.findByRole("heading", { name: "Who got it and didn't open" })).toBeTruthy();
    const table = await screen.findByRole("table");
    expect(within(table).getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Email", "Name"]);
    expect(within(table).getByRole("link", { name: "bo@patsbakery.com" })).toBeTruthy();
  });

  it("says when nobody is on the list, and when its events are archived", async () => {
    let archived = false;
    const api = server(() =>
      archived
        ? apiError(410, "report_archived", "This campaign's events were deleted 12 months after launch.")
        : { status: 200, body: { data: [], next_cursor: null } },
    );
    renderApp(`/campaigns/${campaignId}/report/complained`, api.fetch);
    expect(await screen.findByText("Nobody yet.")).toBeTruthy();

    archived = true;
    await userEvent.setup().click(screen.getByRole("link", { name: "Bounced" }));
    expect(await screen.findByText(/deleted 12 months after launch/)).toBeTruthy();
  });

  it("says when there's no such list", async () => {
    const api = server(() => ({ status: 200, body: { data: [], next_cursor: null } }));
    renderApp(`/campaigns/${campaignId}/report/forwarded`, api.fetch);
    expect(await screen.findByText(/There's no such list/)).toBeTruthy();
  });
});
