import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { apiError, fakeApi, renderApp, signedIn } from "../test/render";
import { browser, dollars, isStripeUrl } from "./money";

const prices = { month_cents: 500, year_cents: 5000, currency: "usd" };
const freePlan = {
  plan: "free",
  status: null,
  interval: null,
  units: 0,
  current_period_end: null,
  cancel_at_period_end: false,
  contacts: 3100,
  units_needed: 2,
  prices,
};
const paidPlan = {
  ...freePlan,
  plan: "paid",
  status: "active",
  interval: "month",
  units: 1,
  current_period_end: "2026-12-18T10:00:00Z",
};
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

  it("starts the paid plan monthly or yearly on Stripe", async () => {
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
    expect(within(plan).getByText(/make 2 units: \$10 a month/)).toBeTruthy();
    await user.click(within(plan).getByLabelText("Yearly, $50 a year"));
    expect(within(plan).getByText(/make 2 units: \$100 a year/)).toBeTruthy();
    await user.click(within(plan).getByRole("button", { name: "Start the paid plan" }));
    await waitFor(() => expect(open).toHaveBeenCalledWith("https://checkout.stripe.com/c/pay/cs_plan"));
    expect(api.calls.find((c) => c.path === "/v1/plan/checkout")?.body).toEqual({ interval: "year" });
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
    expect(within(plan).getByText("Paid plan, billed monthly")).toBeTruthy();
    // Its contacts now make 2 units, so the next renewal bills for 2.
    expect(within(plan).getByText(/^\$10 plus any sales tax, on/)).toBeTruthy();
    expect(within(plan).getByText(/moves to the units your contacts make now/)).toBeTruthy();

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
