import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import {
  fieldRequest,
  matchColumn,
  matchFieldColumn,
  matchingColumns,
  newFieldCount,
  previewFieldMatching,
  previewMatching,
} from "./Imports";
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
    fields: {} as Record<string, string>,
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
    await user.click(screen.getByText("Filters"));
    await user.click(screen.getByRole("checkbox", { name: "Unconfirmed" }));
    await user.click(screen.getByRole("checkbox", { name: "Newsletter" }));
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

  const previewCol = (
    position: number,
    header: string,
    field: string | null,
    samples: string[],
    name = header,
    extra: { custom_field?: unknown; new_field?: unknown } = {},
  ) => ({
    position,
    header,
    name,
    field,
    custom_field: null,
    new_field: null,
    samples,
    ...extra,
  });
  const peoplePreview = {
    separator: "comma",
    rows: 4,
    has_header_guess: true,
    columns: [
      previewCol(1, "E-mail Address", "email", ["pat@example.com", "sam@example.com", "not-an-address"]),
      previewCol(2, "Given name", null, ["Pat", "Sam", ""]),
      previewCol(3, "Surname", "last_name", ["Lee", "", ""]),
      previewCol(4, "Groups", null, ["a", "b", ""]),
      previewCol(5, "", null, ["", "", ""], "#5"),
    ],
    problem: null,
  };

  it("matches columns, confirms permission, shows progress and the skipped rows", async () => {
    let posted: unknown;
    const previewed: unknown[] = [];
    let polls = 0;
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([tagNews, tagVip]),
      "POST /v1/imports/preview": (body) => {
        previewed.push(body);
        return { status: 200, body: peoplePreview };
      },
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

    expect(await screen.findByText(/people.csv: 4 contacts, separated by commas/)).toBeTruthy();
    expect(previewed[0]).toEqual({ csv });
    // Each column shows its first values and what it becomes.
    expect((screen.getByLabelText("E-mail Address") as HTMLSelectElement).value).toBe("email");
    expect(screen.getByText("pat@example.com, sam@example.com, not-an-address")).toBeTruthy();
    expect(screen.getByText("Lee, (blank), (blank)")).toBeTruthy();
    expect((screen.getByLabelText("Given name") as HTMLSelectElement).value).toBe("");
    expect((screen.getByLabelText("Surname") as HTMLSelectElement).value).toBe("last_name");
    expect((screen.getByLabelText("Column 5") as HTMLSelectElement).value).toBe("");
    expect(screen.getByText("No values in the first rows")).toBeTruthy();
    await user.selectOptions(screen.getByLabelText("Given name"), "first_name");
    await user.selectOptions(screen.getByLabelText("Groups"), "tags");
    // Each change is checked again with the whole matching.
    await waitFor(() =>
      expect(previewed.at(-1)).toEqual({
        csv,
        columns: { email: "E-mail Address", first_name: "Given name", last_name: "Surname", status: "", tags: "Groups" },
      }),
    );
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
      columns: { email: "E-mail Address", first_name: "Given name", last_name: "Surname", status: "", tags: "Groups" },
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
    expect(screen.getByRole("link", { name: "Download skipped rows (CSV)" }).getAttribute("href")).toBe(
      `/v1/imports/${importID}/skipped-rows/export`,
    );
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

  it("shows a problem before the import starts, and won't start without an email column", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "POST /v1/imports/preview": (body) => {
        const cols = (body as { columns?: Record<string, string> }).columns;
        const problem =
          cols?.tags === "Groups" ? { code: "invalid_tag_name", message: "Row 3 has a tag that isn't 1 to 100 characters." } : null;
        return { status: 200, body: { ...peoplePreview, problem } };
      },
    });
    const user = userEvent.setup();
    renderApp("/contacts/import", api.fetch);
    await user.upload(await screen.findByLabelText("CSV file"), new File(["E-mail Address\nx\n"], "people.csv", { type: "text/csv" }));
    await user.click(await screen.findByRole("radio", { name: "They signed up on my site or form" }));
    const button = () => screen.getByRole("button", { name: "Import contacts" }) as HTMLButtonElement;
    await waitFor(() => expect(button().disabled).toBe(false));

    await user.selectOptions(screen.getByLabelText("Groups"), "tags");
    expect(await screen.findByText("Row 3 has a tag that isn't 1 to 100 characters.")).toBeTruthy();
    expect(button().disabled).toBe(true);
    await user.selectOptions(screen.getByLabelText("Groups"), "");
    await waitFor(() => expect(screen.queryByText(/has a tag that isn't/)).toBeNull());
    expect(button().disabled).toBe(false);

    await user.selectOptions(screen.getByLabelText("E-mail Address"), "");
    expect(await screen.findByText("Choose the column that holds email addresses.")).toBeTruthy();
    expect(button().disabled).toBe(true);
    expect(api.calls.some((c) => c.path === "/v1/imports")).toBe(false);
  });

  it("imports pasted rows without a header, and lets the header guess be changed", async () => {
    const previewed: { csv: string; has_header?: boolean; columns?: Record<string, string> }[] = [];
    let posted: unknown;
    const headerless = {
      separator: "tab",
      rows: 2,
      has_header_guess: false,
      columns: [
        previewCol(1, "", null, ["Pat", "Sam"], "#1"),
        previewCol(2, "", "email", ["pat@example.com", "sam@example.com"], "#2"),
        previewCol(3, "", null, ["Lee", "Ro"], "#3"),
      ],
      problem: null,
    };
    const headed = {
      separator: "tab",
      rows: 1,
      has_header_guess: false,
      columns: [previewCol(1, "Pat", null, ["Sam"]), previewCol(2, "pat@example.com", "email", ["sam@example.com"]), previewCol(3, "Lee", null, ["Ro"])],
      problem: null,
    };
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "POST /v1/imports/preview": (body) => {
        const b = body as (typeof previewed)[number];
        previewed.push(b);
        return { status: 200, body: b.has_header === false ? headerless : headed };
      },
      "POST /v1/imports": (body) => {
        posted = body;
        return { status: 202, body: anImport("queued", 0, { file_name: null, kind: "csv" }) };
      },
      [`GET /v1/imports/${importID}`]: () => ({ status: 200, body: anImport("done", 2, { file_name: null, kind: "csv" }) }),
      [`GET /v1/imports/${importID}/skipped-rows`]: () => ({ status: 200, body: { data: [], next_cursor: null } }),
    });
    const user = userEvent.setup();
    renderApp("/contacts/import", api.fetch);

    await user.click(await screen.findByRole("radio", { name: "Paste contacts" }));
    expect(screen.queryByLabelText("CSV file")).toBeNull();
    const box = screen.getByLabelText("Contacts");
    expect(screen.getByText(/Copy rows from a spreadsheet and paste them here/)).toBeTruthy();
    const continueButton = () => screen.getByRole("button", { name: "Continue" }) as HTMLButtonElement;
    // An empty or blank paste can't continue.
    expect(continueButton().disabled).toBe(true);
    await user.type(box, "   ");
    expect(continueButton().disabled).toBe(true);
    await user.clear(box);

    const text = "Pat\tpat@example.com\tLee\nSam\tsam@example.com\tRo\n";
    await user.click(box);
    await user.paste(text);
    await user.click(continueButton());

    // The first row holds an address, so it's read as a contact.
    expect(await screen.findByText(/Pasted contacts: 2 contacts, separated by tabs/)).toBeTruthy();
    expect(previewed.map((b) => b.has_header)).toEqual([undefined, false]);
    expect(previewed[0].csv).toBe(text);
    const header = screen.getByRole("checkbox", { name: "The first row is a header" }) as HTMLInputElement;
    expect(header.checked).toBe(false);
    expect((screen.getByLabelText("Column 2") as HTMLSelectElement).value).toBe("email");

    // Ticking it reads the first row as the header.
    await user.click(header);
    expect(await screen.findByText(/Pasted contacts: 1 contact, separated by tabs/)).toBeTruthy();
    expect(previewed.at(-1)?.has_header).toBeUndefined();
    expect(screen.getByLabelText("pat@example.com")).toBeTruthy();
    await user.click(header);
    expect(await screen.findByText(/Pasted contacts: 2 contacts/)).toBeTruthy();

    await user.selectOptions(screen.getByLabelText("Column 1"), "first_name");
    await waitFor(() =>
      expect(previewed.at(-1)).toEqual({
        csv: text,
        has_header: false,
        columns: { email: "#2", first_name: "#1", last_name: "", status: "", tags: "" },
      }),
    );
    await user.click(screen.getByRole("radio", { name: "They're customers who bought from me" }));
    await user.click(screen.getByRole("button", { name: "Import contacts" }));
    await waitFor(() => expect(location()).toBe(`/contacts/imports/${importID}`));
    expect(posted).toEqual({
      csv: text,
      has_header: false,
      columns: { email: "#2", first_name: "#1", last_name: "", status: "", tags: "" },
      tags: [],
      permission_confirmed: false,
      list_source: "customers",
    });
    // A pasted import has no file name.
    expect(await screen.findByRole("heading", { name: "Pasted contacts" })).toBeTruthy();
  });

  it("reads comma-separated lines with a header row as it guessed", async () => {
    const previewed: { has_header?: boolean }[] = [];
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "POST /v1/imports/preview": (body) => {
        previewed.push(body as { has_header?: boolean });
        return { status: 200, body: { ...peoplePreview, has_header_guess: true } };
      },
    });
    const user = userEvent.setup();
    renderApp("/contacts/import", api.fetch);
    await user.click(await screen.findByRole("radio", { name: "Paste contacts" }));
    await user.click(screen.getByLabelText("Contacts"));
    await user.paste("E-mail Address,Given name\npat@example.com,Pat\n");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText(/Pasted contacts: 4 contacts, separated by commas/)).toBeTruthy();
    expect(previewed).toHaveLength(1);
    expect((screen.getByRole("checkbox", { name: "The first row is a header" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("E-mail Address") as HTMLSelectElement).value).toBe("email");
  });

  const fieldCompanyRef = { id: "33333333-3333-4333-8333-333333333333", key: "company", name: "Company" };
  const fieldRow = (id: string, key: string, name: string) => ({
    id,
    key,
    name,
    created_at: "2026-11-18T10:00:00Z",
    updated_at: "2026-11-18T10:00:00Z",
  });
  const crmPreview = {
    separator: "comma",
    rows: 2,
    has_header_guess: true,
    columns: [
      previewCol(1, "Email", "email", ["pat@example.com", "sam@example.com"]),
      previewCol(2, "Company", null, ["Acme", ""], "Company", { custom_field: fieldCompanyRef }),
      previewCol(3, "Size", null, ["12", "40"]),
      previewCol(4, "Region", null, ["EU", "US"]),
    ],
    problem: null,
  };
  const crmCSV = "Email,Company,Size,Region\npat@example.com,Acme,12,EU\nsam@example.com,,40,US\n";

  it("matches columns to contact fields, creates fields from headers and sends the mappings (MG-131)", async () => {
    const previewed: Record<string, unknown>[] = [];
    let posted: unknown;
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "GET /v1/plan": () => ({ status: 200, body: { plan: "free", contact_field_limit: 20 } }),
      "GET /v1/contact-fields": () => ({
        status: 200,
        body: { data: [fieldRow(fieldCompanyRef.id, "company", "Company"), fieldRow("44444444-4444-4444-8444-444444444444", "plan", "Plan tier")], next_cursor: null },
      }),
      "POST /v1/imports/preview": (body) => {
        previewed.push(body as Record<string, unknown>);
        return { status: 200, body: crmPreview };
      },
      "POST /v1/imports": (body) => {
        posted = body;
        return { status: 202, body: anImport("queued", 0) };
      },
      [`GET /v1/imports/${importID}`]: () => ({ status: 200, body: anImport("done", 4, { existing_updated: 1 }) }),
      [`GET /v1/imports/${importID}/skipped-rows`]: () => ({ status: 200, body: { data: [], next_cursor: null } }),
    });
    const user = userEvent.setup();
    renderApp("/contacts/import", api.fetch);
    await user.upload(await screen.findByLabelText("CSV file"), new File([crmCSV], "crm.csv", { type: "text/csv" }));
    expect(await screen.findByText(/crm.csv: 2 contacts/)).toBeTruthy();

    // A header matching a field is matched to it; the rest default to not imported.
    const company = screen.getByLabelText("Company") as HTMLSelectElement;
    expect(company.value).toBe("field:company");
    expect(screen.getByText("Acme, (blank)")).toBeTruthy();
    const size = screen.getByLabelText("Size") as HTMLSelectElement;
    expect(size.value).toBe("");
    expect(within(size).getByRole("option", { name: "Plan tier" })).toBeTruthy();
    expect(within(size).getByRole("option", { name: "New field: Size" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Create field from Company" })).toBeNull();

    // Create field is one click away, named from the header.
    await user.click(screen.getByRole("button", { name: "Create field from Size" }));
    expect(size.value).toBe("new");
    await user.selectOptions(screen.getByLabelText("Region"), "field:plan");
    await waitFor(() =>
      expect(previewed.at(-1)).toEqual({
        csv: crmCSV,
        columns: { email: "Email", first_name: "", last_name: "", status: "", tags: "" },
        field_columns: { company: "Company", plan: "Region" },
        new_fields: [{ column: "Size", name: "Size" }],
      }),
    );
    // Back to not imported, and a field moves between columns.
    await user.selectOptions(screen.getByLabelText("Region"), "field:company");
    expect(company.value).toBe("");

    await user.click(screen.getByRole("radio", { name: "They signed up on my site or form" }));
    await user.click(screen.getByRole("button", { name: "Import contacts" }));
    await waitFor(() => expect(location()).toBe(`/contacts/imports/${importID}`));
    expect(posted).toEqual({
      csv: crmCSV,
      file_name: "crm.csv",
      columns: { email: "Email", first_name: "", last_name: "", status: "", tags: "" },
      field_columns: { company: "Region", plan: "" },
      new_fields: [{ column: "Size", name: "Size" }],
      tags: [],
      permission_confirmed: false,
      list_source: "form",
    });
    expect(await screen.findByText("Contacts you already had, with fields updated from the file: 1")).toBeTruthy();
  });

  it("stops offering Create field at the plan's field limit, and says so (MG-131)", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "GET /v1/plan": () => ({ status: 200, body: { plan: "free", contact_field_limit: 2 } }),
      "GET /v1/contact-fields": () => ({ status: 200, body: { data: [fieldRow(fieldCompanyRef.id, "company", "Company")], next_cursor: null } }),
      "POST /v1/imports/preview": () => ({ status: 200, body: crmPreview }),
    });
    const user = userEvent.setup();
    renderApp("/contacts/import", api.fetch);
    await user.upload(await screen.findByLabelText("CSV file"), new File([crmCSV], "crm.csv", { type: "text/csv" }));
    await screen.findByText(/crm.csv: 2 contacts/);
    expect(screen.queryByText(/so no more can be made/)).toBeNull();

    // One more field fits; after it, Create field is unavailable elsewhere.
    await user.click(screen.getByRole("button", { name: "Create field from Size" }));
    const regionButton = (await screen.findByRole("button", { name: "Create field from Region" })) as HTMLButtonElement;
    await waitFor(() => expect(regionButton.disabled).toBe(true));
    const region = screen.getByLabelText("Region");
    expect((within(region).getByRole("option", { name: "New field: Region" }) as HTMLOptionElement).disabled).toBe(true);
    expect((within(screen.getByLabelText("Size")).getByRole("option", { name: "New field: Size" }) as HTMLOptionElement).disabled).toBe(false);
    expect(
      screen.getByText(
        "Your plan allows 2 contact fields, and with the new ones here they're all used, so no more can be made. A column can still go into one of your existing fields.",
      ),
    ).toBeTruthy();
    // Mapping to an existing field still works.
    await user.selectOptions(region, "field:company");
    expect((region as HTMLSelectElement).value).toBe("field:company");

    // Taking the new field back makes room again.
    await user.selectOptions(screen.getByLabelText("Size"), "");
    await waitFor(() => expect(screen.queryByText(/so no more can be made/)).toBeNull());
  });

  it("asks for a name when Create field is used on pasted contacts without a header (MG-131)", async () => {
    let posted: unknown;
    const previewed: Record<string, unknown>[] = [];
    const headerless = {
      separator: "tab",
      rows: 2,
      has_header_guess: false,
      columns: [previewCol(1, "", "email", ["pat@example.com", "sam@example.com"], "#1"), previewCol(2, "", null, ["Acme", "Bolt"], "#2")],
      problem: null,
    };
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "GET /v1/plan": () => ({ status: 200, body: { plan: "free", contact_field_limit: 20 } }),
      "GET /v1/contact-fields": () => ({ status: 200, body: { data: [], next_cursor: null } }),
      "POST /v1/imports/preview": (body) => {
        previewed.push(body as Record<string, unknown>);
        return { status: 200, body: headerless };
      },
      "POST /v1/imports": (body) => {
        posted = body;
        return { status: 202, body: anImport("queued", 0, { file_name: null, kind: "csv" }) };
      },
      [`GET /v1/imports/${importID}`]: () => ({ status: 200, body: anImport("done", 2, { file_name: null, kind: "csv" }) }),
      [`GET /v1/imports/${importID}/skipped-rows`]: () => ({ status: 200, body: { data: [], next_cursor: null } }),
    });
    const user = userEvent.setup();
    renderApp("/contacts/import", api.fetch);
    await user.click(await screen.findByRole("radio", { name: "Paste contacts" }));
    await user.click(screen.getByLabelText("Contacts"));
    await user.paste("pat@example.com\tAcme\nsam@example.com\tBolt\n");
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByText(/Pasted contacts: 2 contacts/);
    expect(within(screen.getByLabelText("Column 2")).getByRole("option", { name: "New field…" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Create field from Column 2" }));
    const name = await screen.findByLabelText("Name for the new field");
    // Nothing is checked until it has a name.
    expect(previewed.some((b) => b.new_fields)).toBe(false);
    await user.click(screen.getByRole("radio", { name: "They're customers who bought from me" }));
    const button = () => screen.getByRole("button", { name: "Import contacts" }) as HTMLButtonElement;
    expect(button().disabled).toBe(true);
    await user.type(name, "Company");
    // The name is checked once typing stops.
    await waitFor(() => expect(previewed.at(-1)?.new_fields).toEqual([{ column: "#2", name: "Company" }]));
    await waitFor(() => expect(button().disabled).toBe(false));
    await user.click(button());
    await waitFor(() => expect(location()).toBe(`/contacts/imports/${importID}`));
    expect(posted).toEqual({
      csv: "pat@example.com\tAcme\nsam@example.com\tBolt\n",
      has_header: false,
      columns: { email: "#1", first_name: "", last_name: "", status: "", tags: "" },
      new_fields: [{ column: "#2", name: "Company" }],
      tags: [],
      permission_confirmed: false,
      list_source: "customers",
    });
  });

  it("refuses a paste over 10 MB in the browser", async () => {
    const api = fakeApi({ ...signedIn, "GET /v1/tags": tagsRoute([]) });
    const user = userEvent.setup();
    renderApp("/contacts/import", api.fetch);
    await user.click(await screen.findByRole("radio", { name: "Paste contacts" }));
    const box = screen.getByLabelText("Contacts");
    fireEvent.change(box, { target: { value: "a@example.com\n".repeat((10 << 20) / 14 + 1) } });
    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText("The pasted contacts are larger than 10 MB.")).toBeTruthy();
    expect(screen.getByText("Split them into smaller parts and import each one.")).toBeTruthy();
    expect(api.calls.some((c) => c.path.startsWith("/v1/imports"))).toBe(false);
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

const fieldCompany = {
  id: "33333333-3333-4333-8333-333333333333",
  key: "company",
  name: "Company",
  created_at: "2026-11-18T10:00:00Z",
  updated_at: "2026-11-18T10:00:00Z",
};
const fieldPlan = { ...fieldCompany, id: "44444444-4444-4444-8444-444444444444", key: "plan", name: "Plan tier" };
const fieldsRoute = (fields: unknown[]) => () => ({ status: 200, body: { data: fields, next_cursor: null } });
const planRoute = (limit: number) => () => ({ status: 200, body: { plan: "free", contact_field_limit: limit } });

describe("contact fields", () => {
  it("shows a column for each field on the contacts table, in a box that scrolls sideways", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "GET /v1/contact-fields": fieldsRoute([fieldCompany, fieldPlan]),
      "GET /v1/contacts": () => ({
        status: 200,
        body: { data: [contact(1, { fields: { company: "Acme", plan: "Pro" } }), contact(2)], next_cursor: null },
      }),
    });
    renderApp("/contacts", api.fetch);

    expect(await screen.findByRole("columnheader", { name: "Plan tier" })).toBeTruthy();
    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers.slice(-3)).toEqual(["Added", "Company", "Plan tier"]);
    const rows = screen.getAllByRole("row");
    expect(within(rows[1]).getByRole("cell", { name: "Acme" })).toBeTruthy();
    expect(within(rows[1]).getByRole("cell", { name: "Pro" })).toBeTruthy();
    expect(screen.getByRole("table").parentElement?.className).toBe("table-scroll");
  });

  it("shows the built-in columns when the fields can't be read", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "GET /v1/contacts": () => ({ status: 200, body: { data: [contact(1)], next_cursor: null } }),
    });
    renderApp("/contacts", api.fetch);
    expect(await screen.findByRole("link", { name: "person1@example.com" })).toBeTruthy();
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent).at(-1)).toBe("Added");
  });

  it("edits a contact's field values on its page, clearing an emptied one", async () => {
    let current = contact(1, { fields: { company: "Acme" } });
    const patches: unknown[] = [];
    const id = current.id;
    const api = fakeApi({
      ...signedIn,
      "GET /v1/tags": tagsRoute([]),
      "GET /v1/contact-fields": fieldsRoute([fieldCompany, fieldPlan]),
      [`GET /v1/contacts/${id}`]: () => ({ status: 200, body: current }),
      [`PATCH /v1/contacts/${id}`]: (body) => {
        patches.push(body);
        current = { ...current, fields: { plan: "Pro" }, updated_at: "2026-11-18T11:00:00Z" };
        return { status: 200, body: current };
      },
    });
    const user = userEvent.setup();
    renderApp(`/contacts/${id}`, api.fetch);

    const form = await screen.findByRole("form", { name: "Edit contact" });
    const company = await within(form).findByLabelText("Company");
    expect((company as HTMLInputElement).value).toBe("Acme");
    await user.clear(company);
    await user.type(within(form).getByLabelText("Plan tier"), " Pro ");
    await user.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(patches).toEqual([{ fields: { company: null, plan: "Pro" } }]));
    // The saved contact fills the form again.
    await waitFor(() => expect((screen.getByLabelText("Company") as HTMLInputElement).value).toBe(""));
    expect((screen.getByLabelText("Plan tier") as HTMLInputElement).value).toBe("Pro");
  });
});

describe("fields page", () => {
  it("lists, adds, renames and deletes fields, and counts them against the limit", async () => {
    let fields = [fieldCompany];
    const api = fakeApi({
      ...signedIn,
      "GET /v1/plan": planRoute(20),
      "GET /v1/contact-fields": () => ({ status: 200, body: { data: fields, next_cursor: null } }),
      "POST /v1/contact-fields": (body) => {
        const name = (body as { name: string }).name;
        if (name === "Email") return apiError(422, "reserved_field_name", "Email is one of a contact's own columns.");
        fields = [...fields, { ...fieldPlan, name, key: "plan" }];
        return { status: 201, body: fields[1] };
      },
      [`PATCH /v1/contact-fields/${fieldCompany.id}`]: (body) => {
        fields = fields.map((f) => (f.id === fieldCompany.id ? { ...f, name: (body as { name: string }).name } : f));
        return { status: 200, body: fields[0] };
      },
      [`DELETE /v1/contact-fields/${fieldPlan.id}`]: () => {
        fields = fields.filter((f) => f.id !== fieldPlan.id);
        return { status: 200, body: { values_deleted: 3 } };
      },
    });
    const user = userEvent.setup();
    renderApp("/fields", api.fetch);

    expect(await screen.findByRole("cell", { name: "Company" })).toBeTruthy();
    expect(screen.getByRole("cell", { name: "company" })).toBeTruthy();
    expect(await screen.findByText("1 of 20 fields used")).toBeTruthy();

    await user.type(screen.getByLabelText("New field"), "Email");
    await user.click(screen.getByRole("button", { name: "Add field" }));
    expect(await screen.findByText("Email is one of a contact's own columns.")).toBeTruthy();
    await user.clear(screen.getByLabelText("New field"));
    await user.type(screen.getByLabelText("New field"), "Plan");
    await user.click(screen.getByRole("button", { name: "Add field" }));
    expect(await screen.findByRole("cell", { name: "Plan" })).toBeTruthy();
    expect(screen.getByText("2 of 20 fields used")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Rename Company" }));
    const input = screen.getByLabelText("New name for Company");
    await user.clear(input);
    await user.type(input, "Employer");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByRole("cell", { name: "Employer" })).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Delete Plan" }));
    expect(screen.getByText(/Delete Plan\? Every contact's Plan value is deleted too/)).toBeTruthy();
    expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Delete field" }));
    await waitFor(() => expect(screen.queryByRole("cell", { name: "Plan" })).toBeNull());
    expect(screen.getByText("1 of 20 fields used")).toBeTruthy();
  });

  it("shows why a field an email uses can't be deleted, in the web app's words", async () => {
    const message = 'The email "Spring", in a draft or scheduled campaign, uses {{plan}}, so the field can\'t be deleted.';
    const api = fakeApi({
      ...signedIn,
      "GET /v1/plan": planRoute(20),
      "GET /v1/contact-fields": fieldsRoute([fieldCompany, fieldPlan]),
      [`DELETE /v1/contact-fields/${fieldPlan.id}`]: () =>
        apiError(409, "field_in_use", message, "Take {{plan}} out of those emails. `mailgoose email get <email> --json` shows where it is."),
    });
    const user = userEvent.setup();
    renderApp("/fields", api.fetch);

    await user.click(await screen.findByRole("button", { name: "Delete Plan tier" }));
    await user.click(screen.getByRole("button", { name: "Delete field" }));
    expect(await screen.findByText(message)).toBeTruthy();
    expect(screen.getByText(/Take the field's merge tag out of those emails in the editor/)).toBeTruthy();
    expect(screen.queryByText(/mailgoose email/)).toBeNull();
    // The field is still there.
    expect(screen.getByRole("button", { name: "Delete field" })).toBeTruthy();
  });

  it("disables adding at the limit, and shows the API's refusal", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/plan": planRoute(2),
      "GET /v1/contact-fields": fieldsRoute([fieldCompany, fieldPlan]),
    });
    renderApp("/fields", api.fetch);

    expect(await screen.findByText("2 of 2 fields used. Delete a field you no longer use to add another.")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Add field" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByLabelText("New field") as HTMLInputElement).disabled).toBe(true);
  });

  it("shows a refused create past the limit when the plan isn't known", async () => {
    const api = fakeApi({
      ...signedIn,
      "GET /v1/contact-fields": fieldsRoute([fieldCompany]),
      "POST /v1/contact-fields": () =>
        apiError(409, "field_limit", "An account can have at most 20 contact fields, and this one has 20.", "Delete one."),
    });
    const user = userEvent.setup();
    renderApp("/fields", api.fetch);

    expect(await screen.findByText("1 field")).toBeTruthy();
    await user.type(screen.getByLabelText("New field"), "Region");
    await user.click(screen.getByRole("button", { name: "Add field" }));
    expect(await screen.findByText("An account can have at most 20 contact fields, and this one has 20.")).toBeTruthy();
    expect(screen.getByText("Delete a field you no longer use on the Fields page to add another.")).toBeTruthy();
  });
});

describe("column matching", () => {
  const preview = {
    separator: "tab" as const,
    rows: 1,
    has_header_guess: true,
    columns: [
      { position: 1, header: "Email", name: "Email", field: "email" as const, custom_field: null, new_field: null, samples: [] },
      { position: 2, header: "", name: "#2", field: "first_name" as const, custom_field: null, new_field: null, samples: [] },
      { position: 3, header: "Name", name: "Name", field: null, custom_field: null, new_field: null, samples: [] },
      {
        position: 4,
        header: "Org",
        name: "Org",
        field: null,
        custom_field: { id: "33333333-3333-4333-8333-333333333333", key: "company", name: "Company" },
        new_field: null,
        samples: [],
      },
      { position: 5, header: "Size", name: "Size", field: null, custom_field: null, new_field: { name: "Size", key: "size" }, samples: [] },
    ],
    problem: null,
  };

  it("gives each field one column and sends the rest as not imported", () => {
    const m = previewMatching(preview);
    expect(m).toEqual({ email: 1, first_name: 2 });
    // Picking a field for a second column takes it off the first.
    const moved = matchColumn(m, 3, "first_name");
    expect(moved).toEqual({ email: 1, first_name: 3 });
    // A column takes one field.
    expect(matchColumn(moved, 3, "last_name")).toEqual({ email: 1, last_name: 3 });
    expect(matchColumn(moved, 1, "")).toEqual({ first_name: 3 });
    expect(matchingColumns(preview, m)).toEqual({ email: "Email", first_name: "#2", last_name: "", status: "", tags: "" });
  });

  it("gives each contact field one column, and sends every known field and the new ones (MG-131)", () => {
    const fm = previewFieldMatching(preview);
    expect(fm).toEqual({ 4: { key: "company" }, 5: { name: "Size" } });
    expect(newFieldCount(fm)).toBe(1);
    // Picking an existing field for another column takes it off the first.
    const moved = matchFieldColumn(fm, 3, { key: "company" });
    expect(moved).toEqual({ 3: { key: "company" }, 5: { name: "Size" } });
    expect(matchFieldColumn(moved, 5, undefined)).toEqual({ 3: { key: "company" } });
    expect(fieldRequest(preview, moved, [{ key: "company" }, { key: "plan" }])).toEqual({
      field_columns: { company: "Name", plan: "" },
      new_fields: [{ column: "Size", name: "Size" }],
    });
    // Nothing to say, nothing sent.
    expect(fieldRequest(preview, {}, [])).toEqual({});
  });
});
