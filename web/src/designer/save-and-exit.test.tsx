import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { apiError, fakeApi, renderApp, signedIn } from "../test/render";
import { saveDelay } from "./autosave";

// MG-83: "Save and exit" saves what isn't saved yet at once, then goes back
// to where the editor was opened from; when the save fails it stays.

const emailId = "44444444-4444-4444-8444-444444444444";
const base = `/v1/emails/${emailId}`;
const campaign = "/campaigns/66666666-6666-4666-8666-666666666666";
const location = () => screen.getByTestId("location").textContent;
// Well inside the autosave delay, so a save seen by then was Save and exit's.
const soon = { timeout: saveDelay / 2 };

const settings = {
  subject: "Spring",
  preview_text: "",
  from_name: "",
  from_email: null,
  reply_to: null,
  background_color: "#f4f4f5",
  content_width: 600,
};

function server(kind: "blocks" | "html", failFirst = false) {
  const email = {
    id: emailId,
    name: "Spring menu",
    kind,
    version: 3,
    document:
      kind === "blocks"
        ? { schema_version: 1, settings: { ...settings }, blocks: [{ id: "b_footer001", type: "footer", text: "" }] }
        : { schema_version: 1, settings: { ...settings }, html: '<html><body><a href="{{unsubscribe_url}}">Unsubscribe</a></body></html>' },
    created_at: "2026-11-18T10:00:00Z",
    updated_at: "2026-11-18T10:00:00Z",
  };
  let fail = failFirst;
  return fakeApi({
    ...signedIn,
    [`GET ${base}`]: () => ({ status: 200, body: structuredClone(email) }),
    [`PATCH ${base}`]: (b) => {
      if (fail) {
        fail = false;
        return apiError(503, "unavailable", "mailgoose couldn't be reached.");
      }
      const body = b as { version: number; name?: string };
      if (body.version !== email.version) return apiError(409, "version_conflict", "Stale.");
      if (body.name !== undefined) email.name = body.name;
      email.version++;
      return { status: 200, body: structuredClone(email) };
    },
  });
}

/** Opens the editor and renames the email, so there's an edit not yet saved. */
async function rename(user: ReturnType<typeof userEvent.setup>) {
  const panel = await screen.findByRole("form", { name: "Email settings" });
  const name = within(panel).getByLabelText(/^Name/);
  await user.clear(name);
  await user.type(name, "Spring 2027");
  return name as HTMLInputElement;
}

const saves = (calls: { method: string; path: string; body: unknown }[]) =>
  calls.filter((c) => c.method === "PATCH" && c.path === base).map((c) => c.body);

describe("save and exit", () => {
  it("saves the block designer's edits at once and goes back to the email list", async () => {
    const api = server("blocks");
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    await rename(user);
    expect(screen.getByRole("status")).toHaveProperty("textContent", "Unsaved changes");
    await user.click(screen.getByRole("button", { name: "Save and exit" }));
    await waitFor(() => expect(location()).toBe("/emails"), soon);
    expect(saves(api.calls)).toEqual([{ version: 3, name: "Spring 2027" }]);
  });

  it("goes back to the campaign the editor was opened from", async () => {
    const api = server("blocks");
    const user = userEvent.setup();
    renderApp({ pathname: `/emails/${emailId}`, state: { from: campaign } }, api.fetch);
    await rename(user);
    expect(screen.getByRole("link", { name: "Back to the campaign" }).getAttribute("href")).toBe(campaign);
    await user.click(screen.getByRole("button", { name: "Save and exit" }));
    await waitFor(() => expect(location()).toBe(campaign), soon);
    expect(saves(api.calls)).toHaveLength(1);
  });

  it("leaves at once when everything is saved, by keyboard too", async () => {
    const api = server("blocks");
    const user = userEvent.setup();
    renderApp({ pathname: `/emails/${emailId}`, state: { from: campaign } }, api.fetch);
    const button = await screen.findByRole("button", { name: "Save and exit" });
    button.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(location()).toBe(campaign), soon);
    expect(saves(api.calls)).toEqual([]);
  });

  it("stays and shows why when the save fails, keeping the edit to try again", async () => {
    const api = server("blocks", true);
    const user = userEvent.setup();
    renderApp(`/emails/${emailId}`, api.fetch);
    const name = await rename(user);
    await user.click(screen.getByRole("button", { name: "Save and exit" }));
    expect(await screen.findByText("mailgoose couldn't be reached.")).toBeTruthy();
    expect(screen.getByText("Not saved")).toBeTruthy();
    expect(location()).toBe(`/emails/${emailId}`);
    expect(name.value).toBe("Spring 2027");

    // The failed edit is sent again, then it leaves.
    await user.click(screen.getByRole("button", { name: "Save and exit" }));
    await waitFor(() => expect(location()).toBe("/emails"), soon);
    expect(saves(api.calls)).toEqual([
      { version: 3, name: "Spring 2027" },
      { version: 3, name: "Spring 2027" },
    ]);
  });

  it("saves and leaves the HTML editor the same way", async () => {
    const api = server("html");
    const user = userEvent.setup();
    renderApp({ pathname: `/emails/${emailId}`, state: { from: campaign } }, api.fetch);
    await rename(user);
    await user.click(screen.getByRole("button", { name: "Save and exit" }));
    await waitFor(() => expect(location()).toBe(campaign), soon);
    expect(saves(api.calls)).toEqual([{ version: 3, name: "Spring 2027" }]);
  });

  it("only goes back to a campaign page, whatever else the link carried", async () => {
    for (const from of ["/settings", "https://example.com/campaigns/x", "/campaigns/x/../../settings"]) {
      const api = server("blocks");
      const user = userEvent.setup();
      const { unmount } = renderApp({ pathname: `/emails/${emailId}`, state: { from } }, api.fetch);
      await user.click(await screen.findByRole("button", { name: "Save and exit" }));
      await waitFor(() => expect(location()).toBe("/emails"), soon);
      unmount();
    }
  });
});
