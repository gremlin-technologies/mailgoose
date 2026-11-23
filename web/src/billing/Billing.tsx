// Credits and billing (BIL-3, BIL-4, BIL-6): the plan and its next payment,
// the credit balance, credit packs, and every change to the balance.
// Payments happen on Stripe's own pages; this page only opens them.
import { useCallback, useState } from "react";
import { Link, useSearchParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure, type Result } from "../api/result";
import type { components } from "../api/schema";
import { Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { browser, dollars, isStripeUrl, packs } from "./money";

type Plan = components["schemas"]["Plan"];
type Credits = components["schemas"]["CreditSummary"];
type Transaction = components["schemas"]["CreditTransaction"];
type TransactionType = components["schemas"]["CreditTransactionType"];
type TransactionPage = components["schemas"]["CreditTransactionList"];

/** Stripe statuses that pause sending until a payment goes through. */
export const pausedStatuses = ["past_due", "unpaid"];
/** A plan in any other status, such as a first payment still waiting on the bank, isn't paid up yet. */
const liveStatuses = ["active", "trialing"];

export const typeLabels: Record<TransactionType, string> = {
  grant: "Free monthly credits",
  purchase: "Credit pack",
  reserve: "Held for a campaign",
  release: "Given back from a campaign",
  spend: "Used",
  fee: "Account fee",
  expiry: "Expired",
};

const notStripe: Failure = {
  code: "unexpected_response",
  message: "mailgoose answered with a payment page that isn't Stripe's, so it wasn't opened.",
  hint: "Try again in a moment, or contact support.",
  status: 200,
};

/** Asks the API for a Stripe page, then goes there. Returns why it couldn't. */
async function openStripe(get: () => Promise<Result<{ url: string } | undefined>>): Promise<Failure | undefined> {
  const res = await get();
  if (!res.ok) return res.failure;
  if (!res.data || !isStripeUrl(res.data.url)) return notStripe;
  browser.open(res.data.url);
  return undefined;
}

export function Billing() {
  const api = useApi();
  const [params] = useSearchParams();
  const checkout = params.get("checkout");
  const loadPlan = useCallback(() => attempt(() => api.GET("/v1/plan")), [api]);
  const loadCredits = useCallback(() => attempt(() => api.GET("/v1/credits")), [api]);
  const plan = useLoad<Plan>(loadPlan);
  const credits = useLoad<Credits>(loadCredits);

  return (
    <section className="page">
      <PageHeader title="Credits and billing" about="One credit sends one email to one person. Payments are made on Stripe's pages." />
      {checkout === "done" && (
        <p className="notice notice-good" role="status">
          Thanks. Stripe has your payment. It shows here once Stripe confirms it, usually within a minute.
        </p>
      )}
      {checkout === "cancelled" && (
        <p className="notice" role="status">
          Checkout was cancelled, so nothing was charged.
        </p>
      )}
      {plan.state.status === "loading" && <Loading />}
      {plan.state.status === "failed" && <LoadFailed failure={plan.state.failure} retry={plan.reload} />}
      {plan.state.status === "ok" && <PlanPanel plan={plan.state.data} />}

      {credits.state.status === "loading" && <Loading />}
      {credits.state.status === "failed" && <LoadFailed failure={credits.state.failure} retry={credits.reload} />}
      {credits.state.status === "ok" && <CreditsPanel credits={credits.state.data} />}

      <History />
    </section>
  );
}

function PlanPanel({ plan: p }: { plan: Plan }) {
  const api = useApi();
  const [interval, setBillingInterval] = useState<"month" | "year">("month");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function go(get: () => Promise<Result<{ url: string } | undefined>>) {
    setBusy(true);
    setFailure(undefined);
    const failed = await openStripe(get);
    setBusy(false);
    setFailure(failed);
  }

  if (p.plan === "free" || !p.status) {
    const units = Math.max(p.units_needed, 1);
    const price = (interval === "month" ? p.prices.month_cents : p.prices.year_cents) * units;
    return (
      <section className="panel stack" aria-label="Plan">
        <h2>Free plan</h2>
        <p>Up to 1,000 contacts and 2,500 credits a month, and up to 1,000 emails a day. The paid plan raises your contacts and lets you buy credit packs.</p>
        <fieldset className="choices">
          <legend>Pay for the paid plan</legend>
          <label className="choice">
            <input type="radio" name="interval" checked={interval === "month"} onChange={() => setBillingInterval("month")} />
            Monthly, {dollars(p.prices.month_cents)} a month
          </label>
          <label className="choice">
            <input type="radio" name="interval" checked={interval === "year"} onChange={() => setBillingInterval("year")} />
            Yearly, {dollars(p.prices.year_cents)} a year
          </label>
        </fieldset>
        <p className="muted small">
          That covers 2,500 contacts, and each further 10,000 is the same again. Your {p.contacts.toLocaleString()} contacts make{" "}
          {units === 1 ? "1 unit" : `${units} units`}: {dollars(price)} a {interval}, plus any sales tax.
        </p>
        <div>
          <button
            type="button"
            className="button primary"
            disabled={busy}
            onClick={() => void go(() => attempt(() => api.POST("/v1/plan/checkout", { body: { interval } })))}
          >
            {busy ? "Opening Stripe…" : "Start the paid plan"}
          </button>
        </div>
        <FailureNotice failure={failure} />
      </section>
    );
  }

  const unit = p.interval === "year" ? p.prices.year_cents : p.prices.month_cents;
  const per = p.interval === "year" ? "year" : "month";
  const paused = pausedStatuses.includes(p.status);
  return (
    <section className="panel stack" aria-label="Plan">
      <div className="row">
        <h2>Paid plan, billed {p.interval === "year" ? "yearly" : "monthly"}</h2>
        {paused ? (
          <Badge tone="bad">Payment failed</Badge>
        ) : !liveStatuses.includes(p.status) ? (
          <Badge tone="neutral">Payment pending</Badge>
        ) : p.cancel_at_period_end ? (
          <Badge tone="warn">Ending</Badge>
        ) : (
          <Badge tone="good">Active</Badge>
        )}
      </div>
      {paused && (
        <p className="notice notice-error" role="alert">
          The last payment didn't go through, so sending is paused. Stripe tries the card again; update it to start sending
          sooner. Your contacts and emails are kept.
        </p>
      )}
      <dl className="facts">
        <dt>Contacts</dt>
        <dd>
          {p.contacts.toLocaleString()}, which {p.units_needed === 1 ? "is 1 unit" : `are ${p.units_needed} units`}
        </dd>
        <dt>Now billed for</dt>
        <dd>
          {p.units === 1 ? "1 unit" : `${p.units} units`}, {dollars(unit * p.units)} a {per}
        </dd>
        {p.cancel_at_period_end ? (
          <>
            <dt>Ends</dt>
            <dd>{formatDate(p.current_period_end)}. No more payments; bought credits stay.</dd>
          </>
        ) : (
          <>
            <dt>Next payment</dt>
            <dd>
              {dollars(unit * p.units_needed)} plus any sales tax, on {formatDate(p.current_period_end)}
            </dd>
          </>
        )}
      </dl>
      {!p.cancel_at_period_end && p.units_needed !== p.units && (
        <p className="muted small">The plan moves to the units your contacts make now when it renews.</p>
      )}
      <div>
        <button
          type="button"
          className="button"
          disabled={busy}
          onClick={() => void go(() => attempt(() => api.POST("/v1/plan/portal")))}
        >
          {busy ? "Opening Stripe…" : "Manage plan and card"}
        </button>
      </div>
      <p className="muted small">On Stripe you can change your card, see invoices, switch monthly or yearly, or cancel.</p>
      <FailureNotice failure={failure} />
    </section>
  );
}

function CreditsPanel({ credits: c }: { credits: Credits }) {
  const api = useApi();
  const [busy, setBusy] = useState<string>();
  const [failure, setFailure] = useState<Failure>();

  async function buy(pack: string) {
    setBusy(pack);
    setFailure(undefined);
    const failed = await openStripe(() => attempt(() => api.POST("/v1/credits/checkout", { body: { pack } })));
    setBusy(undefined);
    setFailure(failed);
  }

  return (
    <section className="panel stack" aria-label="Credits">
      <h2>Credits</h2>
      <p className="balance">
        <span className="balance-number">{c.balance.toLocaleString()}</span> credits
      </p>
      {c.plan === "free" ? (
        <p className="muted">
          {c.next_grant_at
            ? `More free credits arrive ${formatDate(c.next_grant_at)}, and any free credits left then expire.`
            : "Your free credits arrive once your account is active."}{" "}
          Credit packs come with the paid plan.
        </p>
      ) : (
        <>
          {c.next_grant_at && (
            <p className="muted">
              More free credits arrive {formatDate(c.next_grant_at)}, and any free credits left then expire.
            </p>
          )}
          <p className="muted">Bought credits last 24 months and the oldest are used first.</p>
          <div className="packs" role="group" aria-label="Buy credits">
            {packs.map((p) => (
              <button key={p.name} type="button" className="button pack" disabled={!!busy} onClick={() => void buy(p.name)}>
                {busy === p.name ? "Opening Stripe…" : `${p.credits.toLocaleString()} for ${dollars(p.cents)}`}
              </button>
            ))}
          </div>
          <p className="muted small">Sales tax is added at checkout, and Stripe emails the receipt.</p>
        </>
      )}
      <FailureNotice failure={failure} />
    </section>
  );
}

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** What a transaction is for: its campaign, when it points to one. */
function TransactionRef({ t }: { t: Transaction }) {
  if (["reserve", "release", "spend"].includes(t.type) && uuid.test(t.ref)) {
    return <Link to={`/campaigns/${t.ref}`}>Campaign</Link>;
  }
  if (t.expires_at) return <span className="muted">Expires {formatDate(t.expires_at)}</span>;
  return null;
}

function History() {
  const api = useApi();
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const load = useCallback(
    () => attempt(() => api.GET("/v1/credits/transactions", { params: { query: { limit: 50, cursor } } })),
    [api, cursor],
  );
  const { state, reload } = useLoad<TransactionPage>(load);

  return (
    <section className="stack" aria-label="History">
      <h2>History</h2>
      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.data.length === 0 && !cursor ? (
          <p className="empty">No changes to your balance yet.</p>
        ) : (
          <>
            <ul className="history" aria-label="Balance changes">
              {state.data.data.map((t) => (
                <li key={t.id}>
                  <span className="history-what">
                    {typeLabels[t.type] ?? t.type}
                    <span className="muted small"> {formatDate(t.created_at)}</span>
                  </span>
                  <span className="history-ref">
                    <TransactionRef t={t} />
                  </span>
                  <span className={`history-amount ${t.amount < 0 ? "minus" : "plus"}`}>
                    {t.amount > 0 ? "+" : t.amount < 0 ? "−" : ""}
                    {Math.abs(t.amount).toLocaleString()}
                  </span>
                </li>
              ))}
            </ul>
            <nav className="row pager" aria-label="Pages">
              <button type="button" className="button small" disabled={cursors.length === 0} onClick={() => setCursors((c) => c.slice(0, -1))}>
                Previous
              </button>
              <button
                type="button"
                className="button small"
                disabled={!state.data.next_cursor}
                onClick={() => state.data.next_cursor && setCursors((c) => [...c, state.data.next_cursor as string])}
              >
                Next
              </button>
            </nav>
          </>
        ))}
    </section>
  );
}
