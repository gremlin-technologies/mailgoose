// Prices and the Stripe hand-off for the billing page.
import type { Failure, Result } from "../api/result";

/** Cents as dollars: 500 is "$5", 1250 is "$12.50". */
export function dollars(cents: number): string {
  const whole = cents % 100 === 0;
  return `$${(cents / 100).toLocaleString("en-US", { minimumFractionDigits: whole ? 0 : 2, maximumFractionDigits: 2 })}`;
}

/** The credit packs (BIL-3). The API's answer to a purchase says what was actually priced. */
export const packs = [
  { name: "10k", credits: 10_000, cents: 2_000 },
  { name: "50k", credits: 50_000, cents: 7_500 },
  { name: "200k", credits: 200_000, cents: 24_000 },
  { name: "1m", credits: 1_000_000, cents: 90_000 },
];

/** Leaves the app for a Stripe page; tests replace it. */
export const browser = {
  open(url: string) {
    window.location.assign(url);
  },
};

/** True for a Stripe page address, the only place the billing page sends people. */
export function isStripeUrl(url: string): boolean {
  try {
    const u = new URL(url);
    return u.protocol === "https:" && (u.hostname === "stripe.com" || u.hostname.endsWith(".stripe.com"));
  } catch {
    return false;
  }
}

const notStripe: Failure = {
  code: "unexpected_response",
  message: "mailgoose answered with a payment page that isn't Stripe's, so it wasn't opened.",
  hint: "Try again in a moment, or contact support.",
  status: 200,
};

/** Asks the API for a Stripe page, then goes there. Returns why it couldn't. */
export async function openStripe(get: () => Promise<Result<{ url: string } | undefined>>): Promise<Failure | undefined> {
  const res = await get();
  if (!res.ok) return res.failure;
  if (!res.data || !isStripeUrl(res.data.url)) return notStripe;
  browser.open(res.data.url);
  return undefined;
}
