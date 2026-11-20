import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { expectAccessible } from "../test/axe";
import { apiError, fakeApi, renderApp, signedIn } from "../test/render";
import type { Block, BlockDocument } from "./blocks";
import { fallbackProblem, insert, mergeTag } from "./format";
import { htmlToMd, mdToHtml } from "./richtext";

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

const holder = (html: string) => {
  const el = document.createElement("div");
  el.innerHTML = html;
  return el;
};

describe("text formatting", () => {
  it("inserts at the selection", () => {
    expect(insert("Hi !", 3, 3, "{{first_name}}")).toEqual({ text: "Hi {{first_name}}!", start: 17, end: 17 });
  });

  it("shows a text block's Markdown as HTML and reads edited HTML back", () => {
    const md = "Fresh **bread** and [cakes](https://bakery.example).\n\n- One *thing*\n- Two\n\n1. First\n2. Second\n\nLine one\nline two";
    expect(mdToHtml(md)).toBe(
      '<p>Fresh <strong>bread</strong> and <a href="https://bakery.example" title="https://bakery.example">cakes</a>.</p>' +
        "<ul><li>One <em>thing</em></li><li>Two</li></ul><ol><li>First</li><li>Second</li></ol><p>Line one<br>line two</p>",
    );
    expect(htmlToMd(holder(mdToHtml(md)))).toBe(md);
    expect(mdToHtml("")).toBe("<p><br></p>");
    expect(mdToHtml("<b>&</b> [x](javascript:alert(1))")).toBe("<p>&lt;b&gt;&amp;&lt;/b&gt; [x](javascript:alert(1))</p>");
    // What browsers leave: divs for lines, spaces inside bold, styles the subset can't say.
    expect(htmlToMd(holder("One<div>Two <b>bold </b>three</div><div><br></div>"))).toBe("One\n\nTwo **bold** three");
    expect(htmlToMd(holder('<p><u>plain</u> <b><i>both</i></b> <a href="javascript:x">no</a></p>'))).toBe("plain **both** no");
    expect(htmlToMd(holder("<p><br></p>"))).toBe("");
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
    // A new size is a new element (h3), which still shows the words.
    expect(within(canvas()).getByRole("heading", { name: "Summer menu", level: 3 })).toBeTruthy();
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

  it("types on a text block in place, saving its Markdown", async () => {
    const api = server();
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByText("Fresh bread."));
    // The block is selected, with its formatting buttons, and typed on where it sits.
    const box = within(canvas()).getByRole("textbox", { name: "Text" });
    expect(box.getAttribute("contenteditable")).toBe("true");
    expect(within(canvas()).getByRole("toolbar", { name: "Formatting" })).toBeTruthy();
    for (const name of ["Bold", "Italic", "Link", "Bulleted list", "Numbered list", "Merge tag"]) {
      expect(within(canvas()).getByRole("button", { name })).toBeTruthy();
    }
    // What the browser's editing leaves behind is read back as Markdown.
    box.innerHTML = '<p><a href="https://bakery.example">Fresh</a> <b>bread</b>.</p>';
    fireEvent.input(box);
    expect(screen.getByRole("status").textContent).toBe("Unsaved changes");

    await waitFor(() => expect(patches(api.calls, `${base}/blocks/b_text00001`)).toHaveLength(1), slow);
    expect(patches(api.calls, `${base}/blocks/b_text00001`)[0]).toEqual({
      version: 3,
      fields: { text: "[Fresh](https://bakery.example) **bread**." },
    });
    // Saving doesn't redraw the words under the caret.
    expect(within(canvas()).getByText("bread").tagName).toBe("B");

    await user.click(screen.getByRole("button", { name: "Email settings" }));
    expect(within(canvas()).queryByRole("toolbar", { name: "Formatting" })).toBeNull();
  });

  it("types a heading and a button's words on the email", async () => {
    const api = server();
    renderApp(`/emails/${emailId}`, api.fetch);
    const heading = await within(await screen.findByRole("list", { name: "Email content" })).findByRole("heading", { name: "Spring menu" });
    fireEvent.focus(heading);
    heading.textContent = "Summer menu";
    fireEvent.input(heading);
    // The properties panel follows what's typed on the email.
    expect((within(properties()).getByLabelText("Heading") as HTMLInputElement).value).toBe("Summer menu");
    await waitFor(() => expect(patches(api.calls, `${base}/blocks/b_heading001`)).toEqual([{ version: 3, fields: { text: "Summer menu" } }]), slow);
  });

  it("names an emptied heading so it isn't an empty h1, and drops the name once it has words", async () => {
    const api = server();
    renderApp(`/emails/${emailId}`, api.fetch);
    const heading = await within(await screen.findByRole("list", { name: "Email content" })).findByRole("heading", { name: "Spring menu" });
    fireEvent.focus(heading);
    heading.textContent = "";
    fireEvent.input(heading);
    // The placeholder is CSS, so without a name the heading would be empty to axe (empty-heading).
    expect(within(canvas()).getByRole("heading", { name: "Empty heading" })).toBe(heading);
    await expectAccessible();

    heading.textContent = "Summer";
    fireEvent.input(heading);
    expect(within(canvas()).getByRole("heading", { name: "Summer" })).toBe(heading);
    expect(heading.hasAttribute("aria-label")).toBe(false);
    await waitFor(() => expect(patches(api.calls, `${base}/blocks/b_heading001`).at(-1)).toMatchObject({ fields: { text: "Summer" } }), slow);
  });

  it("refuses a link address the saved email would drop", async () => {
    const api = server();
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await user.click(await screen.findByText("Fresh bread."));
    await user.click(within(canvas()).getByRole("button", { name: "Link" }));
    const address = within(canvas()).getByLabelText("Link address");
    for (const bad of ["javascript:alert(1)", "bakery.example"]) {
      await user.clear(address);
      await user.type(address, bad);
      await user.click(within(canvas()).getByRole("button", { name: "Add link" }));
      expect(within(canvas()).getByRole("alert").textContent).toMatch(/Start the address with https:\/\//);
      expect(address.getAttribute("aria-invalid")).toBe("true");
      // The form stays open for a fix, and nothing was made or saved.
      expect(within(canvas()).getByRole("group", { name: "Add a link" })).toBeTruthy();
    }
    expect(patches(api.calls, `${base}/blocks/b_text00001`)).toHaveLength(0);

    // Typing again clears the message; an address that survives a save is accepted.
    await user.clear(address);
    expect(within(canvas()).queryByRole("alert")).toBeNull();
    await user.type(address, "https://bakery.example/menu");
    await user.click(within(canvas()).getByRole("button", { name: "Add link" }));
    expect(within(canvas()).queryByRole("alert")).toBeNull();
    expect(within(canvas()).queryByRole("group", { name: "Add a link" })).toBeNull();
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
        if (body.version !== 4) return apiError(409, "version_conflict", "Stale.");
        api.email.version = 5;
        return { status: 201, body: { block: doc().blocks[0], email: structuredClone(api.email) } };
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
