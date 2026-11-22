import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, fakeApi, renderApp, signedIn } from "../test/render";
import { conditionsFromParams, conditionsOf, conditionsToParams, draftFrom, newRow, type Conditions } from "./conditions";

const location = () => screen.getByTestId("location").textContent;
const slow = { timeout: 3000 };

const vip = { id: "22222222-2222-4222-8222-222222222222", name: "VIP", contact_count: 3, created_at: "2026-11-18T10:00:00Z" };
const may = "a1111111-1111-4111-8111-111111111111";
const june = "a2222222-2222-4222-8222-222222222222";
const goneTag = "99999999-9999-4999-8999-999999999999";

function campaign(id: string, name: string, over = {}) {
  return {
    id,
    name,
    email_id: null,
    recipients: { all: true, tags: [], segment: null },
    send_at: null,
    tracking: true,
    status: "sent",
    version: 1,
    last_test: null,
    launch: null,
    created_at: "2026-11-18T10:00:00Z",
    updated_at: "2026-11-18T10:00:00Z",
    ...over,
  };
}

function segment(id: string, name: string, conditions: Conditions, over = {}) {
  return {
    id,
    name,
    conditions,
    valid: true,
    problems: [],
    created_at: "2026-11-20T10:00:00Z",
    updated_at: "2026-11-21T10:00:00Z",
    ...over,
  };
}

const engaged = segment("51111111-1111-4111-8111-111111111111", "Engaged", { statuses: ["subscribed"], clicked: [may] });
const broken = segment("52222222-2222-4222-8222-222222222222", "Old VIPs", { tag_ids: [goneTag] }, {
  valid: false,
  problems: [{ condition: "tag_ids", id: goneTag, code: "tag_deleted" }],
});

/** Routes every page here reads; counts answer 7 unless a test says otherwise. */
function routes(extra: Record<string, Parameters<typeof fakeApi>[0][string]> = {}) {
  return {
    ...signedIn,
    "GET /v1/tags": () => ({ status: 200, body: { data: [vip], next_cursor: null } }),
    "GET /v1/campaigns": () => ({
      status: 200,
      body: {
        data: [
          campaign(may, "May news"),
          campaign(june, "June news"),
          campaign("a3333333-3333-4333-8333-333333333333", "Untracked", { tracking: false }),
          campaign("a4444444-4444-4444-8444-444444444444", "Draft", { status: "draft" }),
        ],
        next_cursor: null,
      },
    }),
    "GET /v1/contacts": () => ({ status: 200, body: { data: [], next_cursor: null } }),
    "GET /v1/contacts/count": () => ({ status: 200, body: { count: 7 } }),
    "GET /v1/segments": () => ({ status: 200, body: { data: [engaged, broken], next_cursor: null } }),
    ...extra,
  };
}

describe("segment conditions", () => {
  it("round-trip through the page address and the builder's draft", () => {
    const c: Conditions = {
      statuses: ["subscribed", "unconfirmed"],
      tag_ids: [vip.id],
      opened: [may],
      not_clicked: [june],
      opened_within_days: 30,
      never_clicked: true,
    };
    const params = conditionsToParams(c);
    expect(params.toString()).toBe(
      `status=subscribed&status=unconfirmed&tag=${vip.id}&opened=${may}&not_clicked=${june}&opened_within_days=30&never_clicked=1`,
    );
    expect(conditionsFromParams(params)).toEqual(c);
    expect(conditionsOf(draftFrom(c))).toEqual(c);
    // What can't be a condition is dropped.
    expect(conditionsFromParams(new URLSearchParams("status=gone&tag=x&opened_within_days=999&never_opened=yes"))).toEqual({});
    // A row not filled in yet isn't a condition.
    expect(conditionsOf({ statuses: [], tags: [], rows: [newRow("clicked"), { ...newRow("clicked_within_days"), days: "0" }] })).toEqual({});
  });
});

describe("contact filter builder", () => {
  it("builds conditions with a live count, lists the matches and saves them as a segment", async () => {
    const counts: string[] = [];
    const lists: string[] = [];
    let saved: unknown;
    const api = fakeApi(
      routes({
        "GET /v1/contacts/count": (_, url) => {
          counts.push(url.search);
          return { status: 200, body: { count: url.searchParams.has("clicked_within_days") ? 2 : 7 } };
        },
        "GET /v1/contacts": (_, url) => {
          lists.push(url.search);
          return { status: 200, body: { data: [], next_cursor: null } };
        },
        "POST /v1/segments": (body) => {
          saved = body;
          return { status: 201, body: segment("53333333-3333-4333-8333-333333333333", "Recent clickers", (body as { conditions: Conditions }).conditions) };
        },
      }),
    );
    const user = userEvent.setup();
    renderApp("/contacts", api.fetch);
    const form = await screen.findByRole("search", { name: "Find contacts" });
    const count = () => within(form).getByRole("status", { name: "Contact count" }).textContent;
    await waitFor(() => expect(count()).toBe("7 contacts match now."), slow);
    expect((within(form).getByRole("button", { name: "Save as segment" }) as HTMLButtonElement).disabled).toBe(true);

    await user.click(within(form).getByText("Filters"));
    await user.click(within(form).getByRole("checkbox", { name: "Subscribed" }));
    await user.click(within(form).getByRole("checkbox", { name: "Unconfirmed" }));
    await user.click(within(form).getByRole("checkbox", { name: "VIP" }));

    // Keyboard only: the add button and the native menus.
    within(form).getByRole("button", { name: "Add a condition" }).focus();
    await user.keyboard("{Enter}");
    const kind = within(form).getByRole("combobox", { name: "Condition 1" });
    expect((kind as HTMLSelectElement).value).toBe("opened");
    const pick = within(form).getByRole("combobox", { name: "Campaign for condition 1" });
    // Only launched campaigns with tracking on can be named.
    expect(within(pick).getAllByRole("option").map((o) => o.textContent)).toEqual(["May news", "June news"]);
    await user.selectOptions(kind, "not_clicked");
    await user.selectOptions(pick, june);
    // Opens get the same quiet line as the report, and no other label.
    expect(within(form).queryByText(/Opens can run high/)).toBeNull();

    await user.click(within(form).getByRole("button", { name: "Add a condition" }));
    await user.selectOptions(within(form).getByRole("combobox", { name: "Condition 2" }), "clicked_within_days");
    const days = within(form).getByRole("spinbutton", { name: "Days for condition 2" });
    await user.clear(days);
    expect(within(form).getByText("Enter a number of days from 1 to 365.")).toBeTruthy();
    await user.type(days, "14");
    await waitFor(() => expect(count()).toBe("2 contacts match now."), slow);
    expect(counts.at(-1)).toBe(
      `?status=subscribed&status=unconfirmed&tag_id=${vip.id}&not_clicked=${june}&clicked_within_days=14`,
    );

    await user.click(within(form).getByRole("button", { name: "Add a condition" }));
    await user.selectOptions(within(form).getByRole("combobox", { name: "Condition 3" }), "never_opened");
    expect(within(form).getByText(/Opens can run high/)).toBeTruthy();
    // A one-off condition can't be picked twice.
    const used = within(within(form).getByRole("combobox", { name: "Condition 1" })).getByRole("option", { name: "Never opened a campaign" });
    expect((used as HTMLOptionElement).disabled).toBe(true);
    await user.click(within(form).getByRole("button", { name: "Remove condition 3" }));

    await user.click(within(form).getByRole("button", { name: "Search" }));
    await waitFor(() =>
      expect(location()).toBe(
        `/contacts?status=subscribed&status=unconfirmed&tag=${vip.id}&not_clicked=${june}&clicked_within_days=14`,
      ),
    );
    await waitFor(() =>
      expect(lists.at(-1)).toBe(`?limit=50&status=subscribed&status=unconfirmed&tag_id=${vip.id}&not_clicked=${june}&clicked_within_days=14`),
    );
    expect(screen.getByRole("link", { name: "Export CSV" }).getAttribute("href")).toBe(
      `/v1/contacts/export?status=subscribed&status=unconfirmed&tag_id=${vip.id}&not_clicked=${june}&clicked_within_days=14`,
    );

    // The applied filters come back open, as they were set.
    const again = await screen.findByRole("search", { name: "Find contacts" });
    expect(within(again).getByText("Filters (4 set)")).toBeTruthy();
    await user.click(within(again).getByRole("button", { name: "Save as segment" }));
    await user.type(within(again).getByLabelText("Segment name"), "Recent clickers{Enter}");
    expect(await screen.findByRole("link", { name: "Recent clickers" })).toBeTruthy();
    expect(saved).toEqual({
      name: "Recent clickers",
      conditions: { tag_ids: [vip.id], statuses: ["subscribed", "unconfirmed"], not_clicked: [june], clicked_within_days: 14 },
    });
  });

  it("shows why the API won't count, such as a campaign sent without tracking", async () => {
    const api = fakeApi(
      routes({
        "GET /v1/contacts/count": (_, url) =>
          url.searchParams.has("opened")
            ? apiError(422, "tracking_off", "The campaign Untracked was sent without open and click tracking.")
            : { status: 200, body: { count: 7 } },
      }),
    );
    const user = userEvent.setup();
    renderApp(`/contacts?opened=a3333333-3333-4333-8333-333333333333`, api.fetch);
    const form = await screen.findByRole("search", { name: "Find contacts" });
    await waitFor(
      () =>
        expect(within(form).getByRole("status", { name: "Contact count" }).textContent).toBe(
          "The campaign Untracked was sent without open and click tracking.",
        ),
      slow,
    );
    // The campaign it names isn't one the menu offers, so it says so.
    expect(within(form).getByRole("option", { name: "A deleted or untracked campaign" })).toBeTruthy();
    await user.click(within(form).getByRole("button", { name: "Remove condition 1" }));
    await waitFor(() => expect(within(form).getByRole("status", { name: "Contact count" }).textContent).toBe("7 contacts match now."), slow);
  });
});

describe("segments page", () => {
  it("lists segments with their count, flags one that needs fixing, renames and refuses a delete in use", async () => {
    let renamed: unknown;
    const api = fakeApi(
      routes({
        [`GET /v1/segments/${engaged.id}/contacts`]: () => ({ status: 200, body: { data: [], next_cursor: null, count: 42 } }),
        [`PATCH /v1/segments/${engaged.id}`]: (body) => {
          renamed = body;
          return { status: 200, body: { ...engaged, name: "Clickers" } };
        },
        [`DELETE /v1/segments/${engaged.id}`]: () =>
          apiError(409, "segment_in_use", 'The draft campaign "June news" goes to this segment.'),
      }),
    );
    const user = userEvent.setup();
    renderApp("/segments", api.fetch);
    const link = await screen.findByRole("link", { name: "Engaged: 42 contacts, see them" });
    expect(link.getAttribute("href")).toBe(`/contacts?status=subscribed&clicked=${may}`);
    expect(screen.getByText("Subscribed; clicked in May news")).toBeTruthy();
    const brokenRow = screen.getByRole("link", { name: "Old VIPs" }).closest("tr")!;
    expect(within(brokenRow).getByText("Needs fixing")).toBeTruthy();
    expect(within(brokenRow).getByText("The tag: its tag was deleted.")).toBeTruthy();
    // A segment with a problem isn't counted: its contacts would be refused.
    expect(api.calls.some((c) => c.path === `/v1/segments/${broken.id}/contacts`)).toBe(false);

    await user.click(screen.getByRole("button", { name: "Rename Engaged" }));
    const input = screen.getByLabelText("New name for Engaged");
    await user.clear(input);
    await user.type(input, "Clickers{Enter}");
    await waitFor(() => expect(renamed).toEqual({ name: "Clickers" }));

    await user.click(screen.getByRole("button", { name: "Delete Engaged" }));
    await user.click(screen.getByRole("button", { name: "Delete segment" }));
    expect(await screen.findByText('The draft campaign "June news" goes to this segment.')).toBeTruthy();
  });

  it("edits a segment's conditions, and makes a new one", async () => {
    let patched: unknown;
    let posted: unknown;
    const made = segment("54444444-4444-4444-8444-444444444444", "Never openers", { never_opened: true });
    const api = fakeApi(
      routes({
        [`GET /v1/segments/${broken.id}`]: () => ({ status: 200, body: broken }),
        [`PATCH /v1/segments/${broken.id}`]: (body) => {
          patched = body;
          return { status: 200, body: { ...broken, ...(body as object), valid: true, problems: [] } };
        },
        "POST /v1/segments": (body) => {
          posted = body;
          return { status: 201, body: made };
        },
        [`GET /v1/segments/${made.id}`]: () => ({ status: 200, body: made }),
      }),
    );
    const user = userEvent.setup();
    renderApp(`/segments/${broken.id}`, api.fetch);
    const form = await screen.findByRole("form", { name: "Segment" });
    expect(screen.getByText("The tag: its tag was deleted.")).toBeTruthy();
    // The deleted tag shows, ticked, so it can be taken off.
    await user.click(within(form).getByRole("checkbox", { name: "A deleted tag" }));
    await user.click(within(form).getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Set at least one condition.", { selector: "[role=alert]" })).toBeTruthy();
    await user.click(within(form).getByRole("checkbox", { name: "VIP" }));
    await user.click(within(form).getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved.")).toBeTruthy();
    expect(patched).toEqual({ name: "Old VIPs", conditions: { tag_ids: [vip.id] } });
    expect(screen.queryByText("The tag: its tag was deleted.")).toBeNull();

    await user.click(screen.getByRole("link", { name: "Segments" , current: false }));
    await user.click(await screen.findByRole("link", { name: "New segment" }));
    const fresh = await screen.findByRole("form", { name: "Segment" });
    await user.type(within(fresh).getByLabelText("Name"), "Never openers");
    await user.click(within(fresh).getByRole("button", { name: "Add a condition" }));
    await user.selectOptions(within(fresh).getByRole("combobox", { name: "Condition 1" }), "never_opened");
    await user.click(within(fresh).getByRole("button", { name: "Save segment" }));
    await waitFor(() => expect(location()).toBe(`/segments/${made.id}`));
    expect(posted).toEqual({ name: "Never openers", conditions: { never_opened: true } });
  });
});
