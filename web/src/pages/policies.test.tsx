import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { fakeApi, renderApp, signedIn, signedOut } from "../test/render";
import { policies } from "./Policies";

describe("policy pages", () => {
  it.each(policies.map((p) => [p.path, p.title]))("%s shows the %s, marked as a placeholder", async (path, title) => {
    renderApp(path, fakeApi(signedOut).fetch);
    expect(await screen.findByRole("heading", { level: 1, name: title })).toBeTruthy();
    expect(screen.getByRole("note").textContent).toContain("Placeholder.");
    expect(screen.getByTestId("location").textContent).toBe(path);

    // Each page links to the other three.
    const nav = screen.getByRole("navigation", { name: "Policies" });
    const links = within(nav)
      .getAllByRole("link")
      .map((a) => a.getAttribute("href"));
    expect(links.sort()).toEqual(policies.map((p) => p.path).filter((p) => p !== path).sort());
  });

  it("stays open to signed-in people", async () => {
    renderApp("/privacy", fakeApi(signedIn).fetch);
    expect(await screen.findByRole("heading", { level: 1, name: "Privacy policy" })).toBeTruthy();
    expect(screen.getByTestId("location").textContent).toBe("/privacy");
  });

  it("are linked from sign-up, opening in a new tab", async () => {
    renderApp("/signup", fakeApi(signedOut).fetch);
    await screen.findByLabelText("Email");
    const want: Record<string, string> = {
      "terms of service": "/terms",
      "anti-spam policy": "/anti-spam",
      "privacy policy": "/privacy",
      "data processing agreement": "/dpa",
    };
    for (const [name, href] of Object.entries(want)) {
      const link = screen.getByRole("link", { name });
      expect(link.getAttribute("href")).toBe(href);
      expect(link.getAttribute("target")).toBe("_blank");
    }
  });
});
