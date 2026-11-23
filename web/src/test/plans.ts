// GET /v1/plan answers for tests, with the default terms (MG-126).
const prices = { month_cents: 500, year_cents: 5000, currency: "usd" };

export const planTerms = {
  prices,
  included_contacts: 2500,
  contacts_per_unit: 10000,
  free_plan: { contacts: 1000, monthly_credits: 2500, daily_sends: 1000 },
  paid_sending: { starting_daily: 1000, most_daily: 10000, needs_domain: false },
  credit_packs: { cheapest: { name: "10k", credits: 10000, price_cents: 2000 }, months: 24 },
};

export const freePlan = {
  plan: "free",
  status: null,
  interval: null,
  units: 0,
  current_period_end: null,
  cancel_at_period_end: false,
  contacts: 3100,
  units_needed: 2,
  ...planTerms,
};

export const paidPlan = {
  ...freePlan,
  plan: "paid",
  status: "active",
  interval: "month",
  units: 1,
  current_period_end: "2026-12-18T10:00:00Z",
};
