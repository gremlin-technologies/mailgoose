import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { fakeApi, renderApp, signedIn } from "../test/render";
import { bodyOf, fieldRows } from "./FormEditor";

const formId = "77777777-7777-4777-8777-777777777777";
const base = `/v1/forms/${formId}`;
const news = { id: "11111111-1111-4111-8111-111111111111", name: "news", contact_count: 40, created_at: "2026-11-18T10:00:00Z" };
const vip = { id: "22222222-2222-4222-8222-222222222222", name: "vip", contact_count: 15, created_at: "2026-11-18T10:00:00Z" };

type Form = Record<string, unknown> & { fields: { name: string; label?: string; required: boolean }[] };

function form(over: Partial<Form> = {}): Form {
  return {
    id: formId,
    name: "Newsletter signup",
    fields: [{ name: "email", label: "", required: true }],
    tags: [],
    button_text: "Subscribe",
    success_message: "Thanks for signing up! Check your inbox for an email to confirm it.",
    redirect_url: null,
    double_opt_in: true,
    confirm_subject: "Please confirm your subscription",
    confirm_text: "Thanks for signing up. Please confirm with the link below.",
    signups: 42,
    confirmations: 30,
    problems: [],
    created_at: "2026-11-18T10:00:00Z",
    updated_at: "2026-11-18T10:00:00Z",
    ...over,
  };
}

function server(start: Form) {
  const state = { form: start, embeds: 0 };
  const api = fakeApi({
    ...signedIn,
    "GET /v1/tags": () => ({ status: 200, body: { data: [news, vip], next_cursor: null } }),
    "GET /v1/forms": () => ({ status: 200, body: { data: [state.form], next_cursor: null } }),
    "POST /v1/forms": () => ({ status: 201, body: state.form }),
    [`GET ${base}`]: () => ({ status: 200, body: state.form }),
    [`GET ${base}/embed`]: () => {
      state.embeds++;
      return {
        status: 200,
        body: {
          action_url: "https://api.mailgoose.app/f/abc",
          html: `<form class="mg-form" action="https://api.mailgoose.app/f/abc"><button>${String(state.form.button_text)}</button></form>`,
          script: "<script src=\"https://api.mailgoose.app/f.js\"></script>",
        },
      };
    },
    [`PATCH ${base}`]: (b) => {
      const body = b as Record<string, unknown> & { tag_ids: string[]; redirect_url: string };
      const { tag_ids, redirect_url, ...rest } = body;
      state.form = {
        ...state.form,
        ...rest,
        fields: body.fields as Form["fields"],
        tags: [news, vip].filter((t) => tag_ids.includes(t.id)),
        redirect_url: redirect_url || null,
        updated_at: "2026-11-19T10:00:00Z",
      };
      return { status: 200, body: { ...state.form, problems: undefined } };
    },
    [`DELETE ${base}`]: () => ({ status: 204 }),
  });
  return { ...api, state };
}

describe("form helpers", () => {
  it("lists every field, the form's first in its order, then the rest switched off", () => {
    const rows = fieldRows(form({ fields: [{ name: "first_name", label: "Name", required: true }, { name: "email", required: true }] }) as never);
    expect(rows.map((r) => [r.name, r.on])).toEqual([
      ["first_name", true],
      ["email", true],
      ["last_name", false],
    ]);
  });

  it("saves every option, leaving out switched-off fields and an unused redirect", () => {
    const body = bodyOf({
      name: " Signup ",
      fields: [
        { name: "email", on: true, label: "", required: false },
        { name: "first_name", on: false, label: "First", required: true },
        { name: "last_name", on: true, label: " Surname ", required: false },
      ],
      tagIds: [news.id],
      buttonText: "Join",
      after: "message",
      successMessage: "Thanks!",
      redirectUrl: "https://example.com/thanks",
      doubleOptIn: false,
      confirmSubject: "Confirm",
      confirmText: "Please confirm.",
    });
    expect(body).toEqual({
      name: "Signup",
      fields: [
        { name: "email", label: "", required: true },
        { name: "last_name", label: "Surname", required: false },
      ],
      tag_ids: [news.id],
      button_text: "Join",
      success_message: "Thanks!",
      redirect_url: "",
      double_opt_in: false,
      confirm_subject: "Confirm",
      confirm_text: "Please confirm.",
    });
  });
});

describe("forms list", () => {
  it("shows each form's signup and confirmation counts, and makes a new form with double opt-in on", async () => {
    const api = server(form());
    const user = userEvent.setup();
    renderApp("/forms", api.fetch);
    const row = (await screen.findByRole("link", { name: "Newsletter signup" })).closest("tr")!;
    expect([...row.querySelectorAll("td")].map((td) => td.textContent)).toEqual(["Newsletter signup", "42", "30", "By email"]);

    await user.type(screen.getByLabelText("New form"), "Footer form");
    await user.click(screen.getByRole("button", { name: "Create form" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(`/forms/${formId}`));
    expect(api.calls.find((c) => c.method === "POST")?.body).toEqual({ name: "Footer form", double_opt_in: true });
  });
});

describe("form editor", () => {
  it("shows the counts, a sandboxed preview and the embed code to copy", async () => {
    const api = server(form());
    renderApp(`/forms/${formId}`, api.fetch);
    const counts = await screen.findByLabelText("Counts");
    expect(counts.textContent).toBe("Signups42Confirmed30");
    const preview = await screen.findByTitle("Form preview");
    expect(preview.getAttribute("sandbox")).toBe("");
    expect(preview.getAttribute("srcdoc")).toContain('<form class="mg-form"');
    expect((screen.getByLabelText("Form HTML") as HTMLTextAreaElement).value).toContain("api.mailgoose.app/f/abc");
    expect((screen.getByLabelText("Script") as HTMLTextAreaElement).value).toContain("f.js");
    expect(screen.getByRole("button", { name: "Copy form HTML" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy script" })).toBeTruthy();
  });

  it("saves every option, and shows the saved form in the preview", async () => {
    const api = server(form());
    const user = userEvent.setup();
    renderApp(`/forms/${formId}`, api.fetch);
    const options = await screen.findByRole("form", { name: "Form options" });
    const save = within(options).getByRole("button", { name: "Save changes" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);

    const first = within(options).getByRole("group", { name: "First name" });
    await user.click(within(first).getByLabelText("Ask for first name"));
    await user.type(within(first).getByLabelText("Label"), "Your name");
    await user.click(within(first).getByLabelText("Required"));
    await user.click(within(options).getByLabelText("vip"));
    const button = within(options).getByLabelText("Button text");
    await user.clear(button);
    await user.type(button, "Join us");
    await user.click(within(options).getByLabelText("Go to a page on my site"));
    await user.type(within(options).getByLabelText("Page address"), "https://patsbakery.com/thanks");
    await user.clear(within(options).getByLabelText("Confirmation email subject"));
    await user.type(within(options).getByLabelText("Confirmation email subject"), "One more step");
    expect(within(options).getByText("Unsaved changes")).toBeTruthy();
    await user.click(save);

    expect(await screen.findByText(/^Saved\./)).toBeTruthy();
    expect(api.calls.find((c) => c.method === "PATCH")?.body).toEqual({
      name: "Newsletter signup",
      fields: [
        { name: "email", label: "", required: true },
        { name: "first_name", label: "Your name", required: true },
      ],
      tag_ids: [vip.id],
      button_text: "Join us",
      success_message: "Thanks for signing up! Check your inbox for an email to confirm it.",
      redirect_url: "https://patsbakery.com/thanks",
      double_opt_in: true,
      confirm_subject: "One more step",
      confirm_text: "Thanks for signing up. Please confirm with the link below.",
    });
    await waitFor(() => expect(screen.getByTitle("Form preview").getAttribute("srcdoc")).toContain("Join us"));
    // The saved form is what's shown now, so there's nothing left to save.
    expect((within(screen.getByRole("form", { name: "Form options" })).getByRole("button", { name: "Save changes" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("hides the confirmation email when double opt-in is turned off, and says what stops signups", async () => {
    const api = server(
      form({
        problems: [{ code: "no_verified_from", message: "Signups can't be confirmed yet.", hint: "Verify a sending domain." }],
      }),
    );
    const user = userEvent.setup();
    renderApp(`/forms/${formId}`, api.fetch);
    expect(await screen.findByText("Signups can't be confirmed yet.")).toBeTruthy();
    const options = screen.getByRole("form", { name: "Form options" });
    await user.click(within(options).getByLabelText(/Ask people to confirm by email/));
    expect(within(options).queryByLabelText("Confirmation email subject")).toBeNull();
    await user.click(within(options).getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(api.calls.some((c) => c.method === "PATCH")).toBe(true));
    expect((api.calls.find((c) => c.method === "PATCH")?.body as { double_opt_in: boolean }).double_opt_in).toBe(false);
  });

  it("deletes a form only after a second press", async () => {
    const api = server(form());
    const user = userEvent.setup();
    renderApp(`/forms/${formId}`, api.fetch);
    await user.click(await screen.findByRole("button", { name: "Delete form" }));
    expect(api.calls.some((c) => c.method === "DELETE")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Delete Newsletter signup" }));
    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe("/forms"));
  });
});
