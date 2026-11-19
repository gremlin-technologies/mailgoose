import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { fakeApi, renderApp, session } from "../test/render";

function withReview(inReview: boolean) {
  return fakeApi({
    "GET /v1/sessions/current": () => ({
      status: 200,
      body: { ...session, account: { ...session.account, in_review: inReview } },
    }),
  });
}

describe("the sign-up review note", () => {
  it("shows on every page while the account waits for review", async () => {
    renderApp("/contacts", withReview(true).fetch);
    const note = (await screen.findByText(/reviewing your new account/)).closest("[role=status]");
    expect(note?.textContent).toMatch(/launching campaigns and taking form signups open once it's approved/);
  });

  it("isn't there once the account is approved", async () => {
    renderApp("/contacts", withReview(false).fetch);
    expect(await screen.findByRole("navigation", { name: "Main" })).toBeTruthy();
    expect(screen.queryByText(/reviewing your new account/)).toBeNull();
  });
});
