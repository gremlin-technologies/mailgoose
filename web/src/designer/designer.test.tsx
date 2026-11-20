import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, fakeApi, renderApp, signedIn } from "../test/render";
import { canDrop, copyOf, isNoop, placeOf, zoneDown, zoneUp, type BlockDocument } from "./blocks";

const emailId = "33333333-3333-4333-8333-333333333333";
const location = () => screen.getByTestId("location").textContent;
const canvas = () => screen.getByRole("list", { name: "Email content" });
// A block's own button, not one of a block nested inside it.
const tool = (group: HTMLElement, name: string) => {
  const own = within(group).getAllByRole("button", { name }).filter((b) => b.closest("[role=group]") === group);
  expect(own).toHaveLength(1);
  return own[0] as HTMLButtonElement;
};

const settings = {
  subject: "Spring",
  preview_text: "",
  from_name: "",
  from_email: null,
  reply_to: null,
  background_color: "#f4f4f5",
  content_width: 600,
};

function doc(): BlockDocument {
  return {
    schema_version: 1,
    settings,
    blocks: [
      { id: "b_heading001", type: "heading", text: "Spring menu", level: 1, align: "left" },
      { id: "b_text00001", type: "text", text: "Fresh **bread** and [cakes](https://bakery.example).", align: "left" },
      {
        id: "b_columns01",
        type: "columns",
        columns: [
          { blocks: [{ id: "b_inner0001", type: "spacer", height: 24 }] },
          { blocks: [] },
        ],
      },
      { id: "b_footer001", type: "footer", text: "" },
    ],
  };
}

function email(version = 3, document = doc()) {
  return {
    id: emailId,
    name: "Spring menu",
    kind: "blocks",
    version,
    document,
    created_at: "2026-11-18T10:00:00Z",
    updated_at: "2026-11-18T10:00:00Z",
  };
}

const change = (version: number) => () => ({
  status: 201,
  body: { block: { block: doc().blocks[0], path: "blocks[0]", parent_id: null, column: null, index: 0 }, email: email(version) },
});

const base = `/v1/emails/${emailId}`;

function designerApi(extra: Record<string, (body: unknown, url: URL) => { status: number; body?: unknown }> = {}) {
  return fakeApi({ ...signedIn, [`GET ${base}`]: () => ({ status: 200, body: email() }), ...extra });
}

describe("block logic", () => {
  it("places drops, refuses columns in columns and moves that change nothing", () => {
    const d = doc();
    expect(placeOf({ parent: null, column: null, after: null })).toEqual({ index: 0 });
    expect(placeOf({ parent: "b_columns01", column: 1, after: null })).toEqual({ parent_id: "b_columns01", column: 1, index: 0 });
    expect(placeOf({ parent: "b_columns01", column: 0, after: "b_inner0001" })).toEqual({ after: "b_inner0001" });

    const inColumn = { parent: "b_columns01", column: 1, after: null };
    expect(canDrop({ kind: "new", type: "columns" }, inColumn, d)).toBe(false);
    expect(canDrop({ kind: "move", id: "b_columns01", type: "columns" }, inColumn, d)).toBe(false);
    expect(canDrop({ kind: "new", type: "text" }, inColumn, d)).toBe(true);
    expect(canDrop({ kind: "move", id: "b_footer001", type: "footer" }, { parent: null, column: null, after: null }, d)).toBe(false);

    // Dropping a block just before or just after itself is no move.
    expect(isNoop("b_text00001", { parent: null, column: null, after: "b_heading001" }, d)).toBe(true);
    expect(isNoop("b_text00001", { parent: null, column: null, after: "b_text00001" }, d)).toBe(true);
    expect(isNoop("b_heading001", { parent: null, column: null, after: null }, d)).toBe(true);
    expect(isNoop("b_inner0001", { parent: "b_columns01", column: 0, after: null }, d)).toBe(true);
    expect(isNoop("b_inner0001", { parent: "b_columns01", column: 1, after: null }, d)).toBe(false);
  });

  it("works out moving up and down, keeping the footer last", () => {
    const d = doc();
    expect(zoneUp(d, "b_heading001")).toBeUndefined();
    expect(zoneUp(d, "b_text00001")).toEqual({ parent: null, column: null, after: null });
    expect(zoneUp(d, "b_columns01")).toEqual({ parent: null, column: null, after: "b_heading001" });
    expect(zoneDown(d, "b_heading001")).toEqual({ parent: null, column: null, after: "b_text00001" });
    expect(zoneDown(d, "b_columns01")).toBeUndefined();
    expect(zoneDown(d, "b_inner0001")).toBeUndefined();
  });

  it("copies a block without its ids, columns' contents too", () => {
    const copy = copyOf(doc().blocks[2]);
    expect(copy).toEqual({ type: "columns", columns: [{ blocks: [{ type: "spacer", height: 24 }] }, { blocks: [] }] });
  });
});

describe("emails list", () => {
  it("lists emails and creates a blocks email, then opens it in the designer", async () => {
    let posted: unknown;
    const api = designerApi({
      "GET /v1/emails": () => ({
        status: 200,
        body: {
          data: [
            { id: emailId, name: "Spring menu", kind: "blocks", version: 3, subject: "Spring", block_count: 4, created_at: "2026-11-18T10:00:00Z", updated_at: "2026-11-18T10:00:00Z" },
            { id: "44444444-4444-4444-8444-444444444444", name: "Old", kind: "html", version: 1, subject: "", block_count: 0, created_at: "2026-11-18T10:00:00Z", updated_at: "2026-11-18T10:00:00Z" },
          ],
          next_cursor: null,
        },
      }),
      "POST /v1/emails": (body) => {
        posted = body;
        return { status: 201, body: email(1) };
      },
    });
    const user = userEvent.setup();
    renderApp("/emails", api.fetch);
    expect(await screen.findByRole("link", { name: "Spring menu" })).toBeTruthy();
    expect(screen.getByText("4 blocks")).toBeTruthy();
    expect(screen.getByText("HTML")).toBeTruthy();

    await user.type(screen.getByLabelText("New email"), "Summer menu");
    await user.click(screen.getByRole("button", { name: "Create and design" }));
    await waitFor(() => expect(location()).toBe(`/emails/${emailId}`));
    expect(posted).toEqual({ name: "Summer menu", kind: "blocks" });
    expect(await screen.findByRole("group", { name: "Footer block" })).toBeTruthy();
  });
});

describe("designer", () => {
  it("renders the document, with the footer locked", async () => {
    renderApp(`/emails/${emailId}`, designerApi().fetch);
    expect(await within(await screen.findByRole("list", { name: "Email content" })).findByRole("heading", { name: "Spring menu", level: 1 })).toBeTruthy();
    expect(screen.getByText("bread").tagName).toBe("STRONG");
    expect(screen.getByText("cakes").getAttribute("title")).toBe("https://bakery.example");
    expect(screen.getByRole("list", { name: "Left column" })).toBeTruthy();
    // A list needs items, so the column with no blocks is a group until it has some.
    expect(screen.getByRole("group", { name: "Right column" })).toBeTruthy();
    expect(screen.queryByRole("list", { name: "Right column" })).toBeNull();
    const footer = screen.getByRole("group", { name: "Footer block" });
    // The footer can't be moved, copied or deleted; only its line of text can change.
    expect(within(footer).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual(["Edit"]);
    expect(footer.getAttribute("draggable")).toBeNull();
    expect(screen.queryByTestId("zone-top-x-b_footer001")).toBeNull();
  });

  it("adds a block by clicking the palette, at the end", async () => {
    let posted: unknown;
    const api = designerApi({
      [`POST ${base}/blocks`]: (body) => {
        posted = body;
        return change(4)();
      },
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Add heading" }));
    await waitFor(() => expect(posted).toEqual({ version: 3, block: { type: "heading", text: "A heading" } }));
  });

  it("selects a block it just added, ready to type over", async () => {
    const added = doc();
    added.blocks.splice(3, 0, { id: "b_new000001", type: "text", text: "Write something here.", align: "left" });
    const api = designerApi({
      [`POST ${base}/blocks`]: () => ({
        status: 201,
        body: { block: { block: added.blocks[3], path: "blocks[3]", parent_id: null, column: null, index: 3 }, email: email(4, added) },
      }),
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Add text" }));
    const box = await screen.findByText("Write something here.");
    const block = box.closest("[role=group]") as HTMLElement;
    await waitFor(() => expect(within(block).getByRole("button", { name: "Edit" }).getAttribute("aria-pressed")).toBe("true"));
    expect(document.activeElement).toBe(box.closest("[role=textbox]"));
    expect(screen.getByRole("form", { name: "Text block settings" })).toBeTruthy();
  });

  it("drags a new block into a column and an existing block to a new place", async () => {
    const bodies: unknown[] = [];
    let version = 3;
    const api = designerApi({
      [`GET ${base}`]: () => ({ status: 200, body: email(version) }),
      [`POST ${base}/blocks`]: (body) => {
        bodies.push(body);
        return change(++version)();
      },
      [`POST ${base}/blocks/b_heading001/move`]: (body) => {
        bodies.push(body);
        return change(++version)();
      },
    });
    renderApp(`/emails/${emailId}`, api.fetch);
    const text = await screen.findByRole("button", { name: "Add text" });

    fireEvent.dragStart(text);
    fireEvent.dragOver(screen.getByTestId("zone-b_columns01-1-start"));
    fireEvent.drop(screen.getByTestId("zone-b_columns01-1-start"));
    await waitFor(() =>
      expect(bodies[0]).toEqual({ version: 3, block: { type: "text", text: "Write something here." }, parent_id: "b_columns01", column: 1, index: 0 }),
    );

    // Columns can't go in columns: the drop does nothing.
    fireEvent.dragStart(screen.getByRole("button", { name: "Add two columns" }));
    fireEvent.drop(screen.getByTestId("zone-b_columns01-0-b_inner0001"));
    await new Promise((r) => setTimeout(r, 20));
    expect(bodies).toHaveLength(1);

    const heading = screen.getByRole("group", { name: "Heading block" });
    fireEvent.dragStart(heading);
    fireEvent.drop(screen.getByTestId("zone-top-x-b_columns01"));
    await waitFor(() => expect(bodies[1]).toEqual({ version: 4, after: "b_columns01" }));
  });

  it("moves, duplicates and deletes with buttons", async () => {
    const calls: string[] = [];
    let version = 3;
    const api = designerApi({
      [`GET ${base}`]: () => ({ status: 200, body: email(version) }),
      [`POST ${base}/blocks/b_text00001/move`]: (body) => {
        calls.push("move " + JSON.stringify(body));
        return change(++version)();
      },
      [`POST ${base}/blocks`]: (body) => {
        calls.push("add " + JSON.stringify(body));
        return change(++version)();
      },
      [`DELETE ${base}/blocks/b_columns01`]: (_, url) => {
        calls.push("delete " + url.search);
        return { status: 200, body: email(++version) };
      },
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    const text = await screen.findByRole("group", { name: "Text block" });

    await user.click(within(text).getByRole("button", { name: "Move up" }));
    await waitFor(() => expect(calls[0]).toBe(`move {"version":3,"index":0}`));

    await user.click(tool(within(canvas()).getByRole("group", { name: "Two columns block" }), "Duplicate"));
    await waitFor(() =>
      expect(calls[1]).toBe(
        `add {"version":4,"block":{"type":"columns","columns":[{"blocks":[{"type":"spacer","height":24}]},{"blocks":[]}]},"after":"b_columns01"}`,
      ),
    );

    const columns = screen.getByRole("group", { name: "Two columns block" });
    expect(tool(columns, "Move down").disabled).toBe(true);
    await user.click(tool(columns, "Delete"));
    await user.click(within(columns).getByRole("button", { name: "Delete with its contents" }));
    await waitFor(() => expect(calls[2]).toBe("delete ?version=5"));
  });

  it("asks for an image, uploads it, and adds it with the upload's URL", async () => {
    let upload: { data: string; file_name: string } | undefined;
    let added: unknown;
    const api = designerApi({
      "POST /v1/images": (body) => {
        upload = body as typeof upload;
        return {
          status: 201,
          body: { id: "i1", url: "https://img.mailgoose.test/acc/abc.png", file_name: "logo.png", content_type: "image/png", bytes: 4, created_at: "2026-11-18T10:00:00Z" },
        };
      },
      [`POST ${base}/blocks`]: (body) => {
        added = body;
        return change(4)();
      },
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Add image" }));
    const form = screen.getByRole("form", { name: "Add an image" });
    await user.upload(within(form).getByLabelText(/Upload a picture/), new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "logo.png", { type: "image/png" }));
    await user.type(within(form).getByLabelText(/Alt text/), "Our logo");
    await user.click(within(form).getByRole("button", { name: "Add block" }));

    await waitFor(() =>
      expect(added).toEqual({ version: 3, block: { type: "image", src: "https://img.mailgoose.test/acc/abc.png", alt: "Our logo" } }),
    );
    expect(upload).toEqual({ data: "iVBORw==", file_name: "logo.png" });
    await waitFor(() => expect(screen.queryByRole("form", { name: "Add an image" })).toBeNull());
  });

  it("asks for a button's text and link", async () => {
    let added: unknown;
    const api = designerApi({
      [`POST ${base}/blocks`]: (body) => {
        added = body;
        return change(4)();
      },
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Add button" }));
    await user.type(screen.getByLabelText("Button text"), "See the menu");
    await user.type(screen.getByLabelText("Link"), "https://bakery.example/menu");
    await user.click(screen.getByRole("button", { name: "Add block" }));
    await waitFor(() =>
      expect(added).toEqual({ version: 3, block: { type: "button", text: "See the menu", href: "https://bakery.example/menu" } }),
    );
  });

  it("reloads when the email changed elsewhere, such as from the CLI", async () => {
    let version = 3;
    const api = designerApi({
      [`GET ${base}`]: () => ({ status: 200, body: email(version) }),
      [`POST ${base}/blocks`]: () => {
        version = 7;
        return apiError(409, "version_conflict", "The email changed since version 3.");
      },
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Add divider" }));
    expect(await screen.findByText("This email was changed somewhere else, so it has been reloaded.")).toBeTruthy();
    await waitFor(() => expect(api.calls.filter((c) => c.method === "GET" && c.path === base)).toHaveLength(2));
  });

  it("shows the API's reason when a change is refused", async () => {
    const api = designerApi({
      [`POST ${base}/blocks`]: () => apiError(422, "invalid_block_change", "An email holds at most 200 blocks."),
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Add spacer" }));
    expect(await screen.findByText("An email holds at most 200 blocks.")).toBeTruthy();
  });
});
