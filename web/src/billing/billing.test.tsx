import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { freePlan, paidPlan } from "../test/plans";
import { apiError, fakeApi, renderApp, signedIn } from "../test/render";
import { browser, dollars, isStripeUrl } from "./money";

const campaignId = "66666666-6666-4666-8666-666666666666";
const history = [
  { id: "t3", type: "reserve", amount: -120, expires_at: null, ref: campaignId, created_at: "2026-11-19T10:00:00Z" },
  { id: "t2", type: "purchase", amount: 10000, expires_at: "2028-11-19T10:00:00Z", ref: "cs_test_1", created_at: "2026-11-19T09:00:00Z" },
  { id: "t1", type: "grant", amount: 1000, expires_at: "2026-12-18T10:00:00Z", ref: "", created_at: "2026-11-18T10:00:00Z" },
];

function server(plan: Record<string, unknown>, extra: Record<string, (b: unknown) => { status: number; body?: unknown }> = {}) {
  return fakeApi({
    ...signedIn,
    "GET /v1/plan": () => ({ status: 200, body: plan }),
    "GET /v1/credits": () => ({
      status: 200,
      body: { balance: 10880, plan: plan.plan, next_grant_at: "2026-12-18T10:00:00Z" },
    }),
    "GET /v1/credits/transactions": () => ({ status: 200, body: { data: history, next_cursor: null } }),
    ...extra,
  });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("money helpers", () => {
  it("shows cents as dollars", () => {
    expect(dollars(500)).toBe("$5");
    expect(dollars(1250)).toBe("$12.50");
    expect(dollars(90000)).toBe("$900");
  });

  it("only treats Stripe's own https pages as payment pages", () => {
    expect(isStripeUrl("https://checkout.stripe.com/c/pay/cs_test_1")).toBe(true);
    expect(isStripeUrl("https://billing.stripe.com/p/session/x")).toBe(true);
    expect(isStripeUrl("http://checkout.stripe.com/x")).toBe(false);
    expect(isStripeUrl("https://stripe.com.example.net/x")).toBe(false);
    expect(isStripeUrl("not a url")).toBe(false);
  });
});

describe("billing page", () => {
  it("shows the balance, the history with each entry's type, and when free credits come", async () => {
    renderApp("/credits", server(freePlan).fetch);
    const credits = await screen.findByRole("region", { name: "Credits" });
    expect(within(credits).getByText("10,880")).toBeTruthy();
    expect(within(credits).getByText(/More free credits arrive/)).toBeTruthy();
    expect(within(credits).queryByRole("group", { name: "Buy credits" })).toBeNull();
    const rows = within(await screen.findByRole("list", { name: "Balance changes" })).getAllByRole("listitem");
    expect(rows.map((r) => r.querySelector(".history-amount")!.textContent)).toEqual(["−120", "+10,000", "+1,000"]);
    expect(rows[0].textContent).toContain("Held for a campaign");
    expect(within(rows[0]).getByRole("link", { name: "Campaign" }).getAttribute("href")).toBe(`/campaigns/${campaignId}`);
    expect(rows[1].textContent).toContain("Credit pack");
    expect(rows[2].textContent).toContain("Free monthly credits");
  });

  it("says what the free plan has and offers the upgrade, with no talk of units (MG-126)", async () => {
    renderApp("/credits", server(freePlan).fetch);
    const plan = await screen.findByRole("region", { name: "Plan" });
    expect(
      within(plan).getByText(
        'Your account sends to up to 1,000 contacts, with 2,500 emails a month and up to 1,000 a day. Every email carries a "Sent by mailgoose" line.',
      ),
    ).toBeTruthy();
    expect(within(plan).getByText(/It starts at \$5 a month\./)).toBeTruthy();
    expect(within(plan).queryByRole("radio")).toBeNull();
    expect(plan.textContent).not.toMatch(/\bunits?\b/);
    expect(await within(plan).findByRole("button", { name: "Upgrade to Pay as you go" })).toBeTruthy();
  });

  it("starts Pay as you go monthly or yearly from the upgrade dialog (MG-126)", async () => {
    const open = vi.spyOn(browser, "open").mockImplementation(() => {});
    const api = server(freePlan, {
      "POST /v1/plan/checkout": () => ({
        status: 201,
        body: { url: "https://checkout.stripe.com/c/pay/cs_plan", interval: "year", units: 2, unit_price_cents: 5000, currency: "usd", expires_at: "2026-11-20T10:00:00Z" },
      }),
    });
    const user = userEvent.setup();
    renderApp("/credits", api.fetch);
    const plan = await screen.findByRole("region", { name: "Plan" });
    await user.click(await within(plan).findByRole("button", { name: "Upgrade to Pay as you go" }));
    const dialog = await screen.findByRole("dialog", { name: "Upgrade to Pay as you go" });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(within(dialog).getByRole("radio", { name: /Monthly/ })).toHaveProperty("checked", true);
    expect(within(dialog).getByText("two months free")).toBeTruthy();
    const benefits = within(within(dialog).getByRole("list", { name: "What Pay as you go adds" })).getAllByRole("listitem");
    expect(benefits.map((b) => b.textContent)).toEqual([
      "Room past 1,000 contacts. The price covers your first 2,500.",
      'No "Sent by mailgoose" line on your emails.',
      "Credit packs from $20 for 10,000 emails, kept for 24 months, on top of the 2,500 free each month.",
      "A daily limit that grows past 1,000, up to 10,000 emails a day with clean sending.",
    ]);
    expect(
      within(dialog).getByText(
        "$5 a month covers up to 2,500 contacts. Each extra 10,000 contacts adds $5 a month, from your next renewal.",
      ),
    ).toBeTruthy();
    expect(within(dialog).getByText("With your 3,100 contacts, that's $10 a month, plus any sales tax.")).toBeTruthy();
    expect(dialog.textContent).not.toMatch(/\bunits?\b|mailgoose plan|--/);

    await user.click(within(dialog).getByRole("radio", { name: /Yearly/ }));
    expect(within(dialog).getByText("With your 3,100 contacts, that's $100 a year, plus any sales tax.")).toBeTruthy();
    expect(within(dialog).getByText(/^\$50 a year covers up to 2,500 contacts/)).toBeTruthy();
    await user.click(within(dialog).getByRole("button", { name: "Continue to payment" }));
    await waitFor(() => expect(open).toHaveBeenCalledWith("https://checkout.stripe.com/c/pay/cs_plan"));
    expect(api.calls.find((c) => c.path === "/v1/plan/checkout")?.body).toEqual({ interval: "year" });
  });

  it("shows a failed checkout inside the dialog", async () => {
    const api = server(freePlan, {
      "POST /v1/plan/checkout": () => apiError(503, "billing_unavailable", "Payments aren't available right now."),
    });
    const user = userEvent.setup();
    renderApp("/credits", api.fetch);
    await user.click(await screen.findByRole("button", { name: "Upgrade to Pay as you go" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Continue to payment" }));
    expect(await within(dialog).findByText("Payments aren't available right now.")).toBeTruthy();
  });

  it("keeps focus in the dialog, closes on Escape or Close, and gives focus back", async () => {
    const user = userEvent.setup();
    renderApp("/credits", server(freePlan).fetch);
    const upgrade = await screen.findByRole("button", { name: "Upgrade to Pay as you go" });
    await user.click(upgrade);
    let dialog = await screen.findByRole("dialog");
    expect(document.activeElement).toBe(dialog);
    expect(document.body.style.overflow).toBe("hidden");

    // Tab from the last control comes round to the first, Close.
    within(dialog).getByRole("button", { name: "Continue to payment" }).focus();
    await user.tab();
    expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "Close" }));
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "Continue to payment" }));

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(upgrade);
    expect(document.body.style.overflow).toBe("");

    await user.click(upgrade);
    dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(upgrade);
  });

  it("words the dialog from the plan's own terms", async () => {
    const terms = {
      ...freePlan,
      contacts: 400,
      units_needed: 1,
      prices: { month_cents: 700, year_cents: 7000, currency: "usd" },
      included_contacts: 3000,
      contacts_per_unit: 5000,
      free_plan: { contacts: 500, monthly_credits: 2000, daily_sends: 0 },
      paid_sending: { starting_daily: 1000, most_daily: 20000, needs_domain: true },
      credit_packs: { cheapest: { name: "5k", credits: 5000, price_cents: 1200 }, months: 12 },
    };
    const user = userEvent.setup();
    renderApp("/credits", server(terms).fetch);
    const plan = await screen.findByRole("region", { name: "Plan" });
    expect(
      within(plan).getByText(
        'Your account sends to up to 500 contacts, with 2,000 emails a month. Every email carries a "Sent by mailgoose" line.',
      ),
    ).toBeTruthy();
    await user.click(await within(plan).findByRole("button", { name: "Upgrade to Pay as you go" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("$7 a month")).toBeTruthy();
    expect(within(dialog).getByText("two months free")).toBeTruthy();
    expect(within(within(dialog).getByRole("list")).getAllByRole("listitem").map((b) => b.textContent)).toEqual([
      "Room past 500 contacts. The price covers your first 3,000.",
      'No "Sent by mailgoose" line on your emails.',
      "Credit packs from $12 for 5,000 emails, kept for 12 months, on top of the 2,000 free each month.",
      "A daily limit that grows to 20,000 emails a day with clean sending, once you add your own domain.",
    ]);
    expect(
      within(dialog).getByText(
        "$7 a month covers up to 3,000 contacts. Each extra 5,000 contacts adds $7 a month, from your next renewal.",
      ),
    ).toBeTruthy();
    expect(within(dialog).getByText("With your 400 contacts, that's $7 a month, plus any sales tax.")).toBeTruthy();
  });

  it("shows the next payment's date and amount, and buys a pack on Stripe", async () => {
    const open = vi.spyOn(browser, "open").mockImplementation(() => {});
    const api = server(paidPlan, {
      "POST /v1/credits/checkout": () => ({
        status: 201,
        body: { url: "https://checkout.stripe.com/c/pay/cs_pack", pack: "50k", credits: 50000, price_cents: 7500, currency: "usd", expires_at: "2026-11-20T10:00:00Z" },
      }),
      "POST /v1/plan/portal": () => ({ status: 201, body: { url: "https://evil.example.com/portal" } }),
    });
    const user = userEvent.setup();
    renderApp("/credits", api.fetch);
    const plan = await screen.findByRole("region", { name: "Plan" });
    expect(within(plan).getByText("Pay as you go, billed monthly")).toBeTruthy();
    expect(within(plan).getByText("$5 a month, for up to 2,500 contacts")).toBeTruthy();
    // Its 3,100 contacts now need the next step, so the renewal bills for it.
    expect(within(plan).getByText(/^\$10 plus any sales tax, on/)).toBeTruthy();
    expect(within(plan).getByText("The price moves to $10 a month when it renews, to match your contacts now.")).toBeTruthy();
    expect(plan.textContent).not.toMatch(/\bunits?\b/);
    // The paid plan isn't offered again.
    expect(screen.queryByRole("button", { name: "Upgrade to Pay as you go" })).toBeNull();
    expect(screen.getByText("Bought credits last 24 months, and the oldest are used first.")).toBeTruthy();

    const buy = await screen.findByRole("group", { name: "Buy credits" });
    expect(within(buy).getAllByRole("button").map((b) => b.textContent)).toEqual([
      "10,000 for $20",
      "50,000 for $75",
      "200,000 for $240",
      "1,000,000 for $900",
    ]);
    await user.click(within(buy).getByRole("button", { name: "50,000 for $75" }));
    await waitFor(() => expect(open).toHaveBeenCalledWith("https://checkout.stripe.com/c/pay/cs_pack"));
    expect(api.calls.find((c) => c.path === "/v1/credits/checkout")?.body).toEqual({ pack: "50k" });

    // An address that isn't Stripe's is never opened.
    await user.click(within(plan).getByRole("button", { name: "Manage plan and card" }));
    expect(await within(plan).findByText(/isn't Stripe's, so it wasn't opened/)).toBeTruthy();
    expect(open).toHaveBeenCalledTimes(1);
  });

  it("says when a checkout came back done or cancelled", async () => {
    renderApp("/billing?checkout=done", server(paidPlan).fetch);
    expect(await screen.findByText(/Stripe has your payment/)).toBeTruthy();
  });

  it("shows a cancelled plan's end date instead of a next payment", async () => {
    renderApp("/credits?checkout=cancelled", server({ ...paidPlan, cancel_at_period_end: true }).fetch);
    expect(await screen.findByText("Checkout was cancelled, so nothing was charged.")).toBeTruthy();
    const plan = await screen.findByRole("region", { name: "Plan" });
    expect(within(plan).getByText("Ending")).toBeTruthy();
    expect(within(plan).queryByText("Next payment")).toBeNull();
  });

  it("marks a plan whose first payment is still going through as pending, not active", async () => {
    renderApp("/billing", server({ ...paidPlan, status: "incomplete" }).fetch);
    const plan = await screen.findByRole("region", { name: "Plan" });
    expect(within(plan).getByText("Payment pending")).toBeTruthy();
    expect(within(plan).queryByText("Active")).toBeNull();
  });
});

describe("paused banner", () => {
  it("shows on every page while the plan's payment has failed", async () => {
    const api = server({ ...paidPlan, status: "past_due" }, { "GET /v1/emails": () => ({ status: 200, body: { data: [], next_cursor: null } }) });
    const user = userEvent.setup();
    renderApp("/emails", api.fetch);
    const banner = await screen.findByText(/Sending is paused: your plan's last payment didn't go through/);
    await user.click(within(banner).getByRole("link", { name: "Update your card" }));
    expect(screen.getByTestId("location").textContent).toBe("/credits");
    expect(await screen.findByText("Payment failed")).toBeTruthy();
  });

  it("stays hidden while the plan is paid up, or can't be read", async () => {
    renderApp("/emails", fakeApi({ ...signedIn, "GET /v1/plan": () => apiError(500, "internal", "Oops."), "GET /v1/emails": () => ({ status: 200, body: { data: [], next_cursor: null } }) }).fetch);
    expect(await screen.findByText(/No emails yet/)).toBeTruthy();
    expect(screen.queryByText(/Sending is paused/)).toBeNull();
  });
});
