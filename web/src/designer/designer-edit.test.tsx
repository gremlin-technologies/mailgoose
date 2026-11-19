import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, fakeApi, renderApp, signedIn } from "../test/render";
import type { Block, BlockDocument } from "./blocks";
import { fallbackProblem, insert, link, list, mergeTag, wrap } from "./format";

const emailId = "33333333-3333-4333-8333-333333333333";
const base = `/v1/emails/${emailId}`;
const slow = { timeout: 3000 };

function doc(): BlockDocument {
  return {
    schema_version: 1,
    settings: {
      subject: "Spring",
      preview_text: "",
      from_name: "Pat's Bakery",
      from_email: null,
      reply_to: null,
      background_color: "#f4f4f5",
      content_width: 600,
    },
    blocks: [
      { id: "b_heading001", type: "heading", text: "Spring menu", level: 1, align: "left" },
      { id: "b_text00001", type: "text", text: "Fresh bread.", align: "left" },
      { id: "b_image0001", type: "image", src: "https://img.mailgoose.test/a/old.png", alt: "Bread", href: null, width: null, align: "center" },
      { id: "b_footer001", type: "footer", text: "" },
    ],
  };
}

/**
 * A fake API that keeps one email and applies saves to it, so the page
 * shows what the server would send back.
 */
function server(extra: Record<string, (body: unknown, url: URL) => { status: number; body?: unknown; html?: string }> = {}) {
  const email = {
    id: emailId,
    name: "Spring menu",
    kind: "blocks",
    version: 3,
    document: doc(),
    created_at: "2026-11-18T10:00:00Z",
    updated_at: "2026-11-18T10:00:00Z",
  };
  const conflict = (v: number) => v !== email.version && apiError(409, "version_conflict", `The email is at version ${email.version}.`);
  const api = fakeApi({
    ...signedIn,
    [`GET ${base}`]: () => ({ status: 200, body: structuredClone(email) }),
    [`PATCH ${base}`]: (b) => {
      const body = b as { version: number; name?: string; settings?: Record<string, unknown> };
      const c = conflict(body.version);
      if (c) return c;
      if (body.name !== undefined) email.name = body.name;
      Object.assign(email.document.settings, body.settings ?? {});
      email.version++;
      return { status: 200, body: structuredClone(email) };
    },
    ...Object.fromEntries(
      doc().blocks.map((blk) => [
        `PATCH ${base}/blocks/${blk.id}`,
        (b: unknown) => {
          const body = b as { version: number; fields: Record<string, unknown> };
          const c = conflict(body.version);
          if (c) return c;
          const target = email.document.blocks.find((x) => x.id === blk.id) as Block;
          Object.assign(target, body.fields);
          email.version++;
          return { status: 200, body: { block: target, email: structuredClone(email) } };
        },
      ]),
    ),
    ...extra,
  });
  return { ...api, email };
}

const patches = (calls: { method: string; path: string; body: unknown }[], path: string) =>
  calls.filter((c) => c.method === "PATCH" && c.path === path).map((c) => c.body);

const canvas = () => screen.getByRole("list", { name: "Email content" });
const properties = () => screen.getByRole("complementary", { name: "Properties" });
const editButton = (name: string) => within(within(canvas()).getByRole("group", { name })).getByRole("button", { name: "Edit" });

describe("text formatting", () => {
  it("wraps, links, lists and inserts at the selection", () => {
    expect(wrap("Fresh bread", 6, 11, "**", "bold text")).toEqual({ text: "Fresh **bread**", start: 8, end: 13 });
    expect(wrap("Hi ", 3, 3, "*", "italic text")).toEqual({ text: "Hi *italic text*", start: 4, end: 15 });
    expect(link("See the menu", 4, 12, "https://bakery.example")).toMatchObject({ text: "See [the menu](https://bakery.example)" });
    expect(link("", 0, 0, "https://bakery.example").text).toBe("[https://bakery.example](https://bakery.example)");
    expect(list("Intro\nbread\ncakes\nEnd", 7, 13, false).text).toBe("Intro\n- bread\n- cakes\nEnd");
    expect(list("- bread\n- cakes", 0, 15, true).text).toBe("1. bread\n2. cakes");
    expect(insert("Hi !", 3, 3, "{{first_name}}")).toEqual({ text: "Hi {{first_name}}!", start: 17, end: 17 });
  });

  it("writes merge tags with fallbacks the API accepts", () => {
    expect(mergeTag("first_name", " there ")).toBe("{{first_name|there}}");
    expect(mergeTag("email")).toBe("{{email}}");
    expect(mergeTag("unsubscribe_url", "ignored")).toBe("{{unsubscribe_url}}");
    expect(fallbackProblem("friend")).toBeUndefined();
    expect(fallbackProblem("{x}")).toMatch(/can't contain/);
    expect(fallbackProblem("x".repeat(101))).toMatch(/up to 100/);
  });
});

describe("designer properties and autosave", () => {
  it("edits the selected block, showing it at once and saving once typing pauses", async () => {
    const api = server();
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByText("Fresh bread."));
    // Clicking the text block opens it for editing on the canvas.
    expect(within(canvas()).getByRole("textbox", { name: "Text" })).toBeTruthy();

    await user.click(editButton("Heading block"));
    const field = within(properties()).getByLabelText("Heading");
    await user.clear(field);
    await user.type(field, "Summer menu");
    expect(within(canvas()).getByRole("heading", { name: "Summer menu" })).toBeTruthy();
    expect(screen.getByRole("status").textContent).toBe("Unsaved changes");

    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("All changes saved"), slow);
    expect(patches(api.calls, `${base}/blocks/b_heading001`)).toEqual([{ version: 3, fields: { text: "Summer menu" } }]);
    expect(api.email.version).toBe(4);
    expect(within(canvas()).getByRole("heading", { name: "Summer menu" })).toBeTruthy();

    await user.selectOptions(within(properties()).getByLabelText("Size"), "Small");
    await user.click(within(properties()).getByLabelText("Center"));
    await waitFor(() => expect(patches(api.calls, `${base}/blocks/b_heading001`)).toHaveLength(2), slow);
    expect(patches(api.calls, `${base}/blocks/b_heading001`)[1]).toEqual({ version: 4, fields: { level: 3, align: "center" } });
  });

  it("changes the email's settings, with a merge tag in the subject", async () => {
    const api = server();
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    const panel = await screen.findByRole("form", { name: "Email settings" });
    const subject = within(panel).getByLabelText("Subject");
    await user.clear(subject);
    await user.type(subject, "Hi , it's spring");
    // Put the caret after "Hi " and add the first name.
    (subject as HTMLInputElement).setSelectionRange(3, 3);
    const subjectField = subject.closest(".tag-field") as HTMLElement;
    await user.click(within(subjectField).getByRole("button", { name: "Merge tag" }));
    const menu = within(subjectField).getByRole("group", { name: "Merge tag" });
    expect(within(menu).queryByRole("option", { name: "Unsubscribe link" })).toBeNull();
    await user.type(within(menu).getByLabelText(/When it's empty/), "there");
    await user.click(within(menu).getByRole("button", { name: "Insert" }));
    expect((subject as HTMLInputElement).value).toBe("Hi {{first_name|there}}, it's spring");

    await user.clear(within(panel).getByLabelText(/^Name/));
    await user.type(within(panel).getByLabelText(/^Name/), "Spring 2027");
    await waitFor(() => expect(patches(api.calls, base)).toHaveLength(1), slow);
    expect(patches(api.calls, base)[0]).toEqual({
      version: 3,
      name: "Spring 2027",
      settings: { subject: "Hi {{first_name|there}}, it's spring" },
    });
    expect(screen.getByRole("heading", { name: "Spring 2027", level: 1 })).toBeTruthy();
  });

  it("formats a text block in place", async () => {
    const api = server();
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByText("Fresh bread."));
    const box = within(canvas()).getByRole("textbox", { name: "Text" }) as HTMLTextAreaElement;
    box.setSelectionRange(6, 11);
    await user.click(within(canvas()).getByRole("button", { name: "Bold" }));
    expect(box.value).toBe("Fresh **bread**.");
    await user.click(within(canvas()).getByRole("button", { name: "Link" }));
    const url = within(canvas()).getByLabelText("Link address");
    await user.clear(url);
    await user.type(url, "https://bakery.example");
    box.setSelectionRange(0, 5);
    await user.click(within(canvas()).getByRole("button", { name: "Add link" }));
    expect(box.value).toBe("[Fresh](https://bakery.example) **bread**.");

    await waitFor(() => expect(patches(api.calls, `${base}/blocks/b_text00001`)).toHaveLength(1), slow);
    expect(patches(api.calls, `${base}/blocks/b_text00001`)[0]).toEqual({
      version: 3,
      fields: { text: "[Fresh](https://bakery.example) **bread**." },
    });

    // Clicking off the block shows it as it'll look.
    await user.click(screen.getByRole("button", { name: "Email settings" }));
    expect(within(canvas()).getByText("bread").tagName).toBe("STRONG");
  });

  it("picks an image uploaded before", async () => {
    let query = "";
    const api = server({
      "GET /v1/images": (_, url) => {
        query = url.search;
        return {
          status: 200,
          body: {
            data: [
              { id: "i1", url: "https://img.mailgoose.test/a/new.png", file_name: "logo.png", content_type: "image/png", bytes: 4, created_at: "2026-11-18T10:00:00Z" },
            ],
            next_cursor: null,
          },
        };
      },
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByRole("img", { name: "Bread" }));
    await user.click(within(properties()).getByRole("button", { name: "Choose from your images" }));
    await user.click(await within(properties()).findByRole("button", { name: "Use logo.png" }));
    expect(query).toBe("?limit=24");
    await waitFor(() => expect(patches(api.calls, `${base}/blocks/b_image0001`)).toHaveLength(1), slow);
    expect(patches(api.calls, `${base}/blocks/b_image0001`)[0]).toEqual({
      version: 3,
      fields: { src: "https://img.mailgoose.test/a/new.png" },
    });
    expect((within(canvas()).getByRole("img", { name: "Bread" }) as HTMLImageElement).src).toBe("https://img.mailgoose.test/a/new.png");
  });

  it("saves pending edits before a block operation, chaining the versions", async () => {
    const api = server({
      [`POST ${base}/blocks`]: (b) => {
        const body = b as { version: number };
        return body.version === 4 ? { status: 201, body: { block: doc().blocks[0], email: { ...doc(), id: emailId } } } : apiError(409, "version_conflict", "Stale.");
      },
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByText("Spring menu", { selector: "h1.canvas-heading" }));
    await user.type(within(properties()).getByLabelText("Heading"), "!");
    await user.click(screen.getByRole("button", { name: "Add divider" }));
    await waitFor(() => expect(api.calls.filter((c) => c.method === "POST").map((c) => c.body)).toEqual([{ version: 4, block: { type: "divider" } }]));
    expect(patches(api.calls, `${base}/blocks/b_heading001`)).toEqual([{ version: 3, fields: { text: "Spring menu!" } }]);
    expect(screen.queryByText(/changed somewhere else/)).toBeNull();
  });

  it("reloads when a save meets a newer version, dropping the edit", async () => {
    const api = server();
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await screen.findByRole("form", { name: "Email settings" });
    api.email.version = 9;
    api.email.document.settings.subject = "Changed from the CLI";
    const subject = within(properties()).getByLabelText("Subject");
    await user.type(subject, " sale");
    expect(await screen.findByText("This email was changed somewhere else, so it has been reloaded.", {}, slow)).toBeTruthy();
    await waitFor(() => expect((within(properties()).getByLabelText("Subject") as HTMLInputElement).value).toBe("Changed from the CLI"));
    expect(screen.getByRole("status").textContent).toBe("All changes saved");
  });

  it("says when a save is refused and keeps the edit", async () => {
    const api = server({
      [`PATCH ${base}/blocks/b_heading001`]: () => apiError(422, "invalid_block_change", "A heading needs some text."),
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByText("Spring menu", { selector: "h1.canvas-heading" }));
    await user.clear(within(properties()).getByLabelText("Heading"));
    expect(await screen.findByText("A heading needs some text.", {}, slow)).toBeTruthy();
    expect(screen.getByRole("status").textContent).toBe("Not saved");
    expect((within(properties()).getByLabelText("Heading") as HTMLInputElement).value).toBe("");
  });

  it("only saves whole numbers in range", async () => {
    const api = server();
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    const width = within(await screen.findByRole("form", { name: "Email settings" })).getByLabelText(/Content width/);
    await user.clear(width);
    await user.type(width, "48");
    expect(screen.getByText("A whole number from 480 to 800.")).toBeTruthy();
    await user.type(width, "0");
    await waitFor(() => expect(patches(api.calls, base)).toEqual([{ version: 3, settings: { content_width: 480 } }]), slow);
  });

  it("previews the server's render at both widths, in a frame that runs no scripts", async () => {
    const widths: string[] = [];
    const api = server({
      [`GET ${base}/preview`]: (_, url) => {
        widths.push(url.searchParams.get("width") ?? "");
        return { status: 200, html: `<p>Rendered ${url.searchParams.get("width")}</p>` };
      },
    });
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Desktop preview" }));
    const frame = await screen.findByTitle("Desktop preview");
    expect(frame.getAttribute("sandbox")).toBe("");
    expect(frame.getAttribute("srcdoc")).toBe("<p>Rendered desktop</p>");
    expect(screen.queryByRole("list", { name: "Email content" })).toBeNull();

    await user.click(screen.getByRole("button", { name: "Mobile preview" }));
    expect((await screen.findByTitle("Mobile preview")).getAttribute("srcdoc")).toBe("<p>Rendered mobile</p>");
    expect(widths).toEqual(["desktop", "mobile"]);

    await user.click(screen.getByRole("button", { name: "Design" }));
    expect(canvas()).toBeTruthy();
  });

  it("explains a preview the server can't render", async () => {
    const api = server({
      [`GET ${base}/preview`]: () => apiError(422, "unrenderable_email", "The email has an unknown merge tag {{nickname}}."),
    });
    renderApp(`/emails/${emailId}`, api.fetch);
    fireEvent.click(await screen.findByRole("button", { name: "Mobile preview" }));
    expect(await screen.findByText("The email has an unknown merge tag {{nickname}}.")).toBeTruthy();
  });
});
