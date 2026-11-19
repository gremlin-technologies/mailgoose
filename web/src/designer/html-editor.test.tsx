import { EditorView } from "@codemirror/view";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, fakeApi, renderApp, signedIn } from "../test/render";
import { fillSample, issuesOf } from "./html";
import { starter } from "./HtmlEditor";

const emailId = "55555555-5555-4555-8555-555555555555";
const base = `/v1/emails/${emailId}`;
const slow = { timeout: 3000 };
const start = `<html><body>\n<p>Hi {{first_name|there}}</p>\n<a href="{{unsubscribe_url}}">Unsubscribe</a>\n</body></html>`;

const settings = {
  subject: "News",
  preview_text: "",
  from_name: "",
  from_email: null,
  reply_to: null,
  background_color: "#f4f4f5",
  content_width: 600,
};

function server(html: string, extra: Record<string, (body: unknown) => { status: number; body?: unknown }> = {}) {
  const email = {
    id: emailId,
    name: "News",
    kind: "html",
    version: 2,
    document: { schema_version: 1, settings: { ...settings }, html },
    created_at: "2026-11-18T10:00:00Z",
    updated_at: "2026-11-18T10:00:00Z",
  };
  const api = fakeApi({
    ...signedIn,
    [`GET ${base}`]: () => ({ status: 200, body: structuredClone(email) }),
    [`PUT ${base}/html`]: (b) => {
      const body = b as { version: number; html: string };
      if (body.version !== email.version) return apiError(409, "version_conflict", "Stale.");
      email.document.html = body.html;
      email.version++;
      const warnings = body.html.includes("position:") ? [{ line: 2, code: "unsupported_css", message: "Outlook ignores position." }] : [];
      return { status: 200, body: { email: structuredClone(email), warnings } };
    },
    [`PATCH ${base}`]: (b) => {
      const body = b as { version: number; settings?: Record<string, unknown> };
      Object.assign(email.document.settings, body.settings);
      email.version++;
      return { status: 200, body: structuredClone(email) };
    },
    ...extra,
  });
  return { ...api, email };
}

/** The CodeMirror view inside the page, to type into it as a person would. */
async function editor(): Promise<EditorView> {
  const host = await screen.findByTestId("code-editor");
  await waitFor(() => expect(host.querySelector(".cm-editor")).toBeTruthy());
  return EditorView.findFromDOM(host.querySelector(".cm-editor") as HTMLElement)!;
}

function typeInto(view: EditorView, text: string, at = view.state.doc.length) {
  view.dispatch({ changes: { from: at, insert: text } });
}

const frame = () => screen.getByTitle("Live preview");

describe("html helpers", () => {
  it("fills merge tags with the sample contact for the live preview", () => {
    expect(fillSample("Hi {{first_name|there}} {{ last_name }} <{{email}}> {{unsubscribe_url}} {{nickname}}")).toBe(
      "Hi Alex Rivera <alex@example.com> #unsubscribe {{nickname}}",
    );
  });

  it("reads the issues of an invalid_html answer, ignoring anything malformed", () => {
    const issues = [{ line: 3, code: "script", message: "Scripts aren't allowed." }, { line: "x" }, null];
    expect(issuesOf({ code: "invalid_html", message: "", hint: "", status: 422, details: { issues } })).toEqual([issues[0]]);
    expect(issuesOf({ code: "invalid_html", message: "", hint: "", status: 422 })).toEqual([]);
  });
});

describe("html editor", () => {
  it("shows the markup in a code editor beside a live preview that runs no scripts", async () => {
    const api = server(start);
    renderApp(`/emails/${emailId}`, api.fetch);
    const view = await editor();
    expect(view.state.doc.toString()).toBe(start);
    expect(view.contentDOM.getAttribute("aria-label")).toBe("HTML code");
    expect(frame().getAttribute("sandbox")).toBe("");
    expect(frame().getAttribute("srcdoc")).toContain("<p>Hi Alex</p>");
    expect(screen.queryByRole("list", { name: "Email content" })).toBeNull();

    typeInto(view, "\n<p>New line</p>");
    await waitFor(() => expect(frame().getAttribute("srcdoc")).toContain("<p>New line</p>"));
    // Saved once typing pauses, with the version read.
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("All changes saved"), slow);
    expect(api.calls.filter((c) => c.method === "PUT")).toEqual([
      { method: "PUT", path: `${base}/html`, body: { version: 2, html: `${start}\n<p>New line</p>` } },
    ]);
  });

  it("marks the API's errors on their lines and keeps the code until it's fixed", async () => {
    let refuse = true;
    const api = server(start, {
      [`PUT ${base}/html`]: (b) => {
        if (refuse) {
          return {
            status: 422,
            body: {
              error: {
                code: "invalid_html",
                message: "Line 3: scripts aren't allowed.",
                hint: "",
                details: { issues: [{ line: 3, code: "script", message: "Scripts aren't allowed." }] },
              },
            },
          };
        }
        const body = b as { version: number; html: string };
        return { status: 200, body: { email: { ...api.email, version: 3, document: { ...api.email.document, html: body.html } }, warnings: [] } };
      },
    });
    renderApp(`/emails/${emailId}`, api.fetch);
    const view = await editor();
    typeInto(view, "<script>x()</script>\n", view.state.doc.line(3).from);
    const problems = await screen.findByRole("region", { name: "Problems" }, slow);
    expect(within(problems).getByText("Line 3:")).toBeTruthy();
    expect(within(problems).getByText(/Scripts aren't allowed/)).toBeTruthy();
    expect(screen.getByRole("status").textContent).toBe("Not saved: 1 problem to fix");
    // No banner as well: the problems list says it.
    expect(screen.queryByText("Line 3: scripts aren't allowed.")).toBeNull();
    expect(view.state.doc.toString()).toContain("<script>");
    await waitFor(() => expect(document.querySelector(".cm-lintRange-error")).toBeTruthy());

    refuse = false;
    view.dispatch({ changes: { from: view.state.doc.line(3).from, to: view.state.doc.line(3).to + 1, insert: "" } });
    await waitFor(() => expect(screen.queryByRole("region", { name: "Problems" })).toBeNull(), slow);
    expect(screen.getByRole("status").textContent).toBe("All changes saved");
  });

  it("lists CSS warnings without stopping the save", async () => {
    const api = server(start);
    renderApp(`/emails/${emailId}`, api.fetch);
    const view = await editor();
    typeInto(view, '<div style="position:absolute">x</div>', view.state.doc.line(2).from);
    const problems = await screen.findByRole("region", { name: "Problems" }, slow);
    expect(within(problems).getByText(/Outlook ignores position/)).toBeTruthy();
    expect(within(problems).getByText(/warning; it still sends/)).toBeTruthy();
    expect(screen.getByRole("status").textContent).toBe("All changes saved");
  });

  it("starts an empty email from a template, and takes an uploaded file", async () => {
    const api = server("");
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    const view = await editor();
    await user.click(screen.getByRole("button", { name: "Start from a simple template" }));
    expect(view.state.doc.toString()).toBe(starter);
    expect(screen.queryByRole("button", { name: "Start from a simple template" })).toBeNull();
    await waitFor(() => expect(api.email.document.html).toBe(starter), slow);

    const file = new File([start], "news.html", { type: "text/html" });
    await user.upload(screen.getByLabelText("Upload an HTML file"), file);
    await waitFor(() => expect(view.state.doc.toString()).toBe(start));
    await waitFor(() => expect(api.email.document.html).toBe(start), slow);
  });

  it("changes the email's subject, without the blocks-only settings", async () => {
    const api = server(start);
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    const form = await screen.findByRole("form", { name: "Email settings" });
    expect(within(form).queryByLabelText(/Content width/)).toBeNull();
    expect(within(form).queryByLabelText("Background color")).toBeNull();
    await user.type(within(form).getByLabelText("Subject"), " for May");
    await waitFor(() => expect(api.email.document.settings.subject).toBe("News for May"), slow);
  });

  it("switches the live preview to a phone's width", async () => {
    const api = server(start);
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await editor();
    await user.click(screen.getByRole("button", { name: "Mobile" }));
    expect(frame().className).toContain("live-mobile");
  });
});

describe("creating an html email", () => {
  it("creates an html email and opens its editor", async () => {
    let posted: unknown;
    const api = server(start, {
      "POST /v1/emails": (body) => {
        posted = body;
        return { status: 201, body: structuredClone(api.email) };
      },
    });
    const all = fakeApi({
      "GET /v1/emails": () => ({ status: 200, body: { data: [], next_cursor: null } }),
    });
    const fetch = (req: Request) => (new URL(req.url).pathname === "/v1/emails" && req.method === "GET" ? all.fetch(req) : api.fetch(req));
    const user = userEvent.setup();
    renderApp("/emails", fetch);
    await user.type(await screen.findByLabelText("New email"), "News");
    await user.click(screen.getByLabelText("My own HTML"));
    await user.click(screen.getByRole("button", { name: "Create and edit" }));
    await editor();
    expect(posted).toEqual({ name: "News", kind: "html" });
  });
});
