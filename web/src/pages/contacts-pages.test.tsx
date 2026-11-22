import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { csvHeader, csvRowCount, guessColumn } from "../csv";
import { apiError, fakeApi, renderApp, signedIn } from "../test/render";

const location = () => screen.getByTestId("location").textContent;

const tagNews = { id: "11111111-1111-4111-8111-111111111111", name: "Newsletter", contact_count: 2, created_at: "2026-11-18T10:00:00Z" };
const tagVip = { id: "22222222-2222-4222-8222-222222222222", name: "VIP", contact_count: 0, created_at: "2026-11-18T10:00:00Z" };

function contact(n: number, extra = {}) {
  return {
    id: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`,
    email: `person${n}@example.com`,
    first_name: `Person${n}`,
    last_name: null,
    status: "subscribed",
    source: "manual",
    tags: [] as { id: string; name: string }[],
    created_at: "2026-11-18T10:00:00Z",
    updated_at: "2026-11-18T10:00:00Z",
    status_changed_at: "2026-11-18T10:00:00Z",
    ...extra,
  };
}

const tagsRoute = (tags: unknown[]) => () => ({ status: 200, body: { data: tags, next_cursor: null } });

describe("contacts list", () => {
  it("searches, filters and pages through contacts, and links the export to the filters", async () => {
    const queries: string[] = [];
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([tagNews]),
      "GET /v1/contacts": (_, url) => {
        queries.push(url.search);
        if (url.searchParams.get("cursor") === "page2") return { status: 200, body: { data: [contact(3)], next_cursor: null } };
        return { status: 200, body: { data: [contact(1), contact(2)], next_cursor: "page2" } };
      },
    });
    const user = userEvent.setup();
    renderApp("/contacts", api.fetch);

    expect(await screen.findByRole("link", { name: "person1@example.com" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Export CSV" }).getAttribute("href")).toBe("/v1/contacts/export");

    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByRole("link", { name: "person3@example.com" })).toBeTruthy();
    expect(queries.at(-1)).toContain("cursor=page2");
    expect((screen.getByRole("button", { name: "Next" }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: "Previous" }));
    expect(await screen.findByRole("link", { name: "person1@example.com" })).toBeTruthy();

    await user.type(screen.getByLabelText("Search"), "pat");
    await user.selectOptions(screen.getByLabelText("In"), "name");
    await user.selectOptions(screen.getByLabelText("Status"), "unconfirmed");
    await user.selectOptions(screen.getByLabelText("Tag"), tagNews.id);
    await user.click(screen.getByRole("button", { name: "Search" }));

    await waitFor(() => expect(location()).toBe(`/contacts?q=pat&by=name&status=unconfirmed&tag=${tagNews.id}`));
    await waitFor(() => expect(queries.at(-1)).toBe(`?limit=50&name=pat&status=unconfirmed&tag_id=${tagNews.id}`));
    expect(screen.getByRole("link", { name: "Export CSV" }).getAttribute("href")).toBe(
      `/v1/contacts/export?name=pat&status=unconfirmed&tag_id=${tagNews.id}`,
    );

    await user.click(screen.getByRole("button", { name: "Clear" }));
    await waitFor(() => expect(location()).toBe("/contacts"));
  });

  it("adds a contact by hand, unconfirmed unless permission is ticked", async () => {
    let posted: unknown;
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "GET /v1/contacts": () => ({ status: 200, body: { data: [], next_cursor: null } }),
      "POST /v1/contacts": (body) => {
        posted = body;
        if ((body as { email: string }).email === "full@example.com") {
          return apiError(402, "contact_limit", "The free plan holds 500 contacts.", "Buy credits to add more.");
        }
        return { status: 201, body: contact(9, { email: "pat@example.com" }) };
      },
    });
    const user = userEvent.setup();
    renderApp("/contacts", api.fetch);

    expect(await screen.findByText(/No contacts yet/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Add contact" }));
    const form = screen.getByRole("form", { name: "Add a contact" });
    await user.type(within(form).getByLabelText("Email"), "full@example.com");
    await user.click(within(form).getByRole("button", { name: "Add contact" }));
    expect(await screen.findByText("The free plan holds 500 contacts.")).toBeTruthy();
    expect(screen.getByText("Buy credits to add more.")).toBeTruthy();

    await user.clear(within(form).getByLabelText("Email"));
    await user.type(within(form).getByLabelText("Email"), "pat@example.com");
    await user.type(within(form).getByLabelText("First name"), "Pat");
    await user.click(within(form).getByRole("checkbox"));
    await user.click(within(form).getByRole("button", { name: "Add contact" }));
    expect(await screen.findByText("Added pat@example.com.")).toBeTruthy();
    expect(posted).toEqual({ email: "pat@example.com", first_name: "Pat", last_name: null, status: "subscribed", source: "manual" });
    expect(screen.queryByRole("form", { name: "Add a contact" })).toBeNull();
  });

  it("tags and untags the selected contacts", async () => {
    const bodies: { path: string; body: unknown }[] = [];
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([tagNews, tagVip]),
      "GET /v1/contacts": () => ({ status: 200, body: { data: [contact(1), contact(2), contact(3)], next_cursor: null } }),
      [`POST /v1/tags/${tagVip.id}/contacts`]: (body) => {
        bodies.push({ path: "add", body });
        return { status: 200, body: { matched: 2, changed: 2 } };
      },
      [`POST /v1/tags/${tagVip.id}/contacts/remove`]: (body) => {
        bodies.push({ path: "remove", body });
        return { status: 200, body: { matched: 3, changed: 1 } };
      },
    });
    const user = userEvent.setup();
    renderApp("/contacts", api.fetch);

    await user.click(await screen.findByRole("checkbox", { name: "Select person1@example.com" }));
    await user.click(screen.getByRole("checkbox", { name: "Select person3@example.com" }));
    const bar = screen.getByRole("region", { name: "Selected contacts" });
    expect(within(bar).getByText("2 contacts selected")).toBeTruthy();
    expect((within(bar).getByRole("button", { name: "Add tag" }) as HTMLButtonElement).disabled).toBe(true);
    await user.selectOptions(within(bar).getByLabelText("Tag"), tagVip.id);
    await user.click(within(bar).getByRole("button", { name: "Add tag" }));
    expect(await screen.findByText("Tagged 2 contacts VIP.")).toBeTruthy();
    expect(bodies[0]).toEqual({ path: "add", body: { contact_ids: [contact(1).id, contact(3).id] } });
    expect(screen.queryByRole("region", { name: "Selected contacts" })).toBeNull();

    await user.click(screen.getByRole("checkbox", { name: "Select every contact on this page" }));
    const bar2 = screen.getByRole("region", { name: "Selected contacts" });
    expect(within(bar2).getByText("3 contacts selected")).toBeTruthy();
    await user.selectOptions(within(bar2).getByLabelText("Tag"), tagVip.id);
    await user.click(within(bar2).getByRole("button", { name: "Remove tag" }));
    expect(await screen.findByText("Took VIP off 1 contact.")).toBeTruthy();
    expect(bodies[1]).toEqual({ path: "remove", body: { contact_ids: [contact(1).id, contact(2).id, contact(3).id] } });
  });
});

describe("contact page", () => {
  it("edits a contact, changes its tags and deletes it with erase", async () => {
    let current = contact(1, { tags: [{ id: tagNews.id, name: tagNews.name }] });
    const patches: unknown[] = [];
    const tagCalls: string[] = [];
    let deleted = "";
    const id = current.id;
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([tagNews, tagVip]),
      "GET /v1/contacts": () => ({ status: 200, body: { data: [], next_cursor: null } }),
      [`GET /v1/contacts/${id}`]: () => ({ status: 200, body: current }),
      [`PATCH /v1/contacts/${id}`]: (body) => {
        patches.push(body);
        current = { ...current, ...(body as object), updated_at: "2026-11-18T11:00:00Z" };
        return { status: 200, body: current };
      },
      [`POST /v1/tags/${tagVip.id}/contacts`]: () => {
        tagCalls.push("add VIP");
        current = { ...current, tags: [...current.tags, { id: tagVip.id, name: tagVip.name }] };
        return { status: 200, body: { matched: 1, changed: 1 } };
      },
      [`POST /v1/tags/${tagNews.id}/contacts/remove`]: () => {
        tagCalls.push("remove Newsletter");
        current = { ...current, tags: current.tags.filter((t) => t.id !== tagNews.id) };
        return { status: 200, body: { matched: 1, changed: 1 } };
      },
      [`DELETE /v1/contacts/${id}`]: (_, url) => {
        deleted = url.search;
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderApp(`/contacts/${id}`, api.fetch);

    expect(await screen.findByRole("heading", { name: "person1@example.com" })).toBeTruthy();
    const form = screen.getByRole("form", { name: "Edit contact" });
    await user.click(within(form).getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Nothing changed.")).toBeTruthy();
    expect(patches).toHaveLength(0);

    await user.type(within(form).getByLabelText("Last name"), "Lee");
    await user.selectOptions(within(form).getByLabelText("Status"), "unsubscribed");
    await user.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patches).toEqual([{ last_name: "Lee", status: "unsubscribed" }]));
    expect(await screen.findByText("Unsubscribed", { selector: ".badge" })).toBeTruthy();

    await user.selectOptions(screen.getByLabelText("Add a tag"), tagVip.id);
    await user.click(screen.getByRole("button", { name: "Add tag" }));
    expect(await screen.findByRole("button", { name: "Remove tag VIP" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Remove tag Newsletter" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Remove tag Newsletter" })).toBeNull());
    expect(tagCalls).toEqual(["add VIP", "remove Newsletter"]);

    await user.click(screen.getByRole("button", { name: "Delete contact" }));
    await user.click(screen.getByRole("checkbox", { name: /erased/ }));
    await user.click(screen.getByRole("button", { name: "Delete person1@example.com" }));
    await waitFor(() => expect(location()).toBe("/contacts"));
    expect(deleted).toBe("?erase=true");
  });

  it("doesn't offer a status change for a bounced contact", async () => {
    const c = contact(4, { status: "bounced" });
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      [`GET /v1/contacts/${c.id}`]: () => ({ status: 200, body: c }),
    });
    renderApp(`/contacts/${c.id}`, api.fetch);
    expect(await screen.findByText(/This address bounced, so it stays bounced/)).toBeTruthy();
    expect(screen.queryByLabelText("Status")).toBeNull();
  });
});

describe("imports", () => {
  const importID = "33333333-3333-4333-8333-333333333333";
  function anImport(status: string, processed: number, extra = {}) {
    return {
      id: importID,
      file_name: "people.csv",
      status,
      contact_status: "subscribed",
      columns: {},
      tags: [],
      total_rows: 4,
      processed_rows: processed,
      imported: status === "done" ? 2 : 0,
      skipped: status === "done" ? 2 : 0,
      skipped_by_reason: { invalid_email: status === "done" ? 1 : 0, invalid_row: 0, duplicate: status === "done" ? 1 : 0, exists: 0, suppressed: 0, over_limit: 0 },
      review: "none",
      hold_reason: null,
      hold_message: null,
      list_source: "other",
      list_source_detail: "A trade show",
      checks: { invalid_rows: status === "done" ? 1 : 0, role_addresses: status === "done" ? 2 : 0, disposable_addresses: 0 },
      error: null,
      created_at: "2026-11-18T10:00:00Z",
      started_at: "2026-11-18T10:00:01Z",
      finished_at: status === "done" ? "2026-11-18T10:00:05Z" : null,
      ...extra,
    };
  }

  it("maps columns, confirms permission, shows progress and the skipped rows", async () => {
    let posted: unknown;
    let polls = 0;
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([tagNews, tagVip]),
      "POST /v1/imports": (body) => {
        posted = body;
        return { status: 202, body: anImport("queued", 0) };
      },
      [`GET /v1/imports/${importID}`]: () => {
        polls++;
        return { status: 200, body: polls === 1 ? anImport("running", 2) : anImport("done", 4) };
      },
      [`GET /v1/imports/${importID}/skipped-rows`]: (_, url) =>
        url.searchParams.get("cursor")
          ? { status: 200, body: { data: [{ row: 4, email: "pat@example.com", reason: "duplicate" }], next_cursor: null } }
          : { status: 200, body: { data: [{ row: 3, email: "not-an-address", reason: "invalid_email" }], next_cursor: "more" } },
    });
    const user = userEvent.setup();
    renderApp("/contacts/import", api.fetch);

    const csv = 'E-mail Address,"Given name",Surname,Groups\npat@example.com,Pat,Lee,a\nsam@example.com,Sam,,b\nnot-an-address,,,\npat@example.com,,,\n';
    const file = new File([csv], "people.csv", { type: "text/csv" });
    await user.upload(await screen.findByLabelText("CSV file"), file);

    expect(await screen.findByText("people.csv: 4 columns, about 4 rows.")).toBeTruthy();
    expect((screen.getByLabelText("Email") as HTMLSelectElement).value).toBe("E-mail Address");
    expect((screen.getByLabelText("First name") as HTMLSelectElement).value).toBe("Given name");
    expect((screen.getByLabelText("Last name") as HTMLSelectElement).value).toBe("Surname");
    expect((screen.getByLabelText("Tags") as HTMLSelectElement).value).toBe("");
    await user.selectOptions(screen.getByLabelText("Tags"), "Groups");
    await user.click(screen.getByRole("checkbox", { name: "VIP" }));
    await user.click(screen.getByRole("checkbox", { name: /given me permission/ }));
    // Where the list came from is required.
    expect((screen.getByRole("button", { name: "Import contacts" }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("radio", { name: "Somewhere else" }));
    expect((screen.getByRole("button", { name: "Import contacts" }) as HTMLButtonElement).disabled).toBe(true);
    await user.type(screen.getByLabelText("Where, in a few words"), "A trade show");
    await user.click(screen.getByRole("button", { name: "Import contacts" }));

    await waitFor(() => expect(location()).toBe(`/contacts/imports/${importID}`));
    expect(posted).toEqual({
      csv,
      file_name: "people.csv",
      columns: { email: "E-mail Address", first_name: "Given name", last_name: "Surname", tags: "Groups" },
      tags: [tagVip.id],
      permission_confirmed: true,
      list_source: "other",
      list_source_detail: "A trade show",
    });

    expect(await screen.findByText("2 of 4 rows")).toBeTruthy();
    expect(await screen.findByText("Done", {}, { timeout: 3000 })).toBeTruthy();
    expect(screen.getByText("Not an email address: 1")).toBeTruthy();
    expect(screen.getByText("Repeated in the file: 1")).toBeTruthy();
    expect(screen.getByText("Where they came from: A trade show")).toBeTruthy();
    expect(screen.getByText("Role addresses (like info@ or sales@): 2")).toBeTruthy();
    expect(await screen.findByText("not-an-address")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findByRole("cell", { name: "Repeated in the file" })).toBeTruthy();
    expect(screen.getAllByRole("row")).toHaveLength(3);
    expect(screen.queryByRole("button", { name: "Show more" })).toBeNull();
  });

  it("explains a held import and a failed one", async () => {
    let im = anImport("done", 4, {
      review: "held",
      hold_reason: "risky_addresses",
      hold_message: "Many of these addresses are role addresses (like info@ or sales@) or throwaway inboxes.",
      skipped: 0,
    });
    const api = fakeApi({ ...signedIn, [`GET /v1/imports/${importID}`]: () => ({ status: 200, body: im }) });
    const { unmount } = renderApp(`/contacts/imports/${importID}`, api.fetch);
    expect(await screen.findByText(/Many of these addresses are role addresses/)).toBeTruthy();
    unmount();

    im = anImport("failed", 2, { error: "The file isn't UTF-8 text." });
    renderApp(`/contacts/imports/${importID}`, api.fetch);
    expect(await screen.findByText("The import stopped: The file isn't UTF-8 text.")).toBeTruthy();
  });

  it("explains a waiting import and cancels it", async () => {
    let im = anImport("waiting", 2, {
      waiting_note: "A tester account keeps at most 500 contacts, so this import won't carry on.",
    });
    let cancelled = 0;
    const api = fakeApi({
      ...signedIn,
      [`GET /v1/imports/${importID}`]: () => ({ status: 200, body: im }),
      [`POST /v1/imports/${importID}/cancel`]: () => {
        cancelled++;
        im = anImport("cancelled", 2);
        return { status: 200, body: im };
      },
    });
    const user = userEvent.setup();
    renderApp(`/contacts/imports/${importID}`, api.fetch);
    expect(await screen.findByText(/won't carry on/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Cancel import" }));
    expect(await screen.findByText("This import was cancelled while it waited for the paid plan.")).toBeTruthy();
    expect(cancelled).toBe(1);
    expect(screen.queryByRole("button", { name: "Cancel import" })).toBeNull();
  });

  it("refuses a file with no header before uploading anything", async () => {
    const api = fakeApi({ ...signedIn, "GET /v1/tags": tagsRoute([]) });
    const user = userEvent.setup();
    renderApp("/contacts/import", api.fetch);
    await user.upload(await screen.findByLabelText("CSV file"), new File([""], "empty.csv", { type: "text/csv" }));
    expect(await screen.findByText("empty.csv has no header row.")).toBeTruthy();
    expect(api.calls.some((c) => c.path === "/v1/imports")).toBe(false);
  });
});

describe("tags page", () => {
  it("creates, renames and deletes tags", async () => {
    let tags = [tagNews];
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": () => ({ status: 200, body: { data: tags, next_cursor: null } }),
      "POST /v1/tags": (body) => {
        const name = (body as { name: string }).name;
        if (name.includes(",")) return apiError(422, "invalid_tag", "A tag name can't have a comma.");
        tags = [...tags, { ...tagVip, name }];
        return { status: 201, body: tags[1] };
      },
      [`PATCH /v1/tags/${tagNews.id}`]: (body) => {
        tags = tags.map((t) => (t.id === tagNews.id ? { ...t, name: (body as { name: string }).name } : t));
        return { status: 200, body: tags[0] };
      },
      [`DELETE /v1/tags/${tagVip.id}`]: () => {
        tags = tags.filter((t) => t.id !== tagVip.id);
        return { status: 200, body: { contacts_untagged: 0, campaigns_updated: 0 } };
      },
    });
    const user = userEvent.setup();
    renderApp("/tags", api.fetch);

    const link = await screen.findByRole("link", { name: "Newsletter" });
    expect(link.getAttribute("href")).toBe(`/contacts?tag=${tagNews.id}`);

    await user.type(screen.getByLabelText("New tag"), "a,b");
    await user.click(screen.getByRole("button", { name: "Create tag" }));
    expect(await screen.findByText("A tag name can't have a comma.")).toBeTruthy();
    await user.clear(screen.getByLabelText("New tag"));
    await user.type(screen.getByLabelText("New tag"), "VIP");
    await user.click(screen.getByRole("button", { name: "Create tag" }));
    expect(await screen.findByRole("link", { name: "VIP" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Rename Newsletter" }));
    const input = screen.getByLabelText("New name for Newsletter");
    await user.clear(input);
    await user.type(input, "Weekly");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("link", { name: "Weekly" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Delete VIP" }));
    expect(screen.getByText(/Delete VIP\? Its 0 contacts stay/)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Delete tag" }));
    await waitFor(() => expect(screen.queryByRole("link", { name: "VIP" })).toBeNull());
  });
});

describe("csv header", () => {
  it("reads quoted cells, a byte-order mark and CRLF", () => {
    expect(csvHeader('﻿email,"Name, full","say ""hi"""\r\nx,y,z')).toEqual(["email", "Name, full", 'say "hi"']);
    expect(csvHeader("")).toEqual([]);
    expect(csvRowCount("email\r\na@b.co\r\n\r\nc@d.co\r\n")).toBe(2);
    expect(guessColumn("email", ["Name", "Email Address"])).toBe("Email Address");
    expect(guessColumn("tags", ["Name"])).toBe("");
  });
});
