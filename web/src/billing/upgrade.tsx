// The upgrade moment (MG-126): one dialog, mounted once in the signed-in
// layout, that any page opens the same way, with useUpgrade().open() or an
// <UpgradeButton>. Every price, allowance and step in its words comes from
// GET /v1/plan, so a change to the plan's terms changes the copy with it.
import { Check, X } from "lucide-react";
import {
  createContext,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { FailureNotice, type Loaded, LoadFailed, Loading, useLoad } from "../components";
import { dollars, openStripe } from "./money";

type Plan = components["schemas"]["Plan"];
type Interval = "month" | "year";

/** What the app calls the paid plan wherever it sells it. */
export const planName = "Pay as you go";

const smallNumbers = ["no", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven"];

/** "two months free" when a year costs two months less than twelve; "" when it saves no whole months. */
export function monthsFree(p: Plan): string {
  const { month_cents: month, year_cents: year } = p.prices;
  if (month <= 0) return "";
  const saved = (12 * month - year) / month;
  if (!Number.isInteger(saved) || saved < 1 || saved > 11) return "";
  return `${smallNumbers[saved]} ${saved === 1 ? "month" : "months"} free`;
}

function price(p: Plan, interval: Interval): number {
  return interval === "year" ? p.prices.year_cents : p.prices.month_cents;
}

/** The free plan's limits, as one sentence. */
export function freeHas(p: Plan): string {
  const f = p.free_plan;
  const daily = f.daily_sends > 0 ? ` and up to ${f.daily_sends.toLocaleString()} a day` : "";
  return `Your account sends to up to ${f.contacts.toLocaleString()} contacts, with ${f.monthly_credits.toLocaleString()} emails a month${daily}. Every email carries a "Sent by mailgoose" line.`;
}

/** How contacts change the price, with no talk of units. */
export function contactPricing(p: Plan, interval: Interval): string {
  const each = `${dollars(price(p, interval))} a ${interval}`;
  return `${each} covers up to ${p.included_contacts.toLocaleString()} contacts. Each extra ${p.contacts_per_unit.toLocaleString()} contacts adds ${each}, from your next renewal.`;
}

/** What this account would pay today. */
export function todayPrice(p: Plan, interval: Interval): string {
  const total = price(p, interval) * Math.max(p.units_needed, 1);
  const people = p.contacts === 1 ? "1 contact" : `${p.contacts.toLocaleString()} contacts`;
  return `With your ${people}, that's ${dollars(total)} a ${interval}, plus any sales tax.`;
}

/** The contacts a paid plan billed for this many steps covers. */
export function contactsCovered(p: Plan, units: number): number {
  return p.included_contacts + Math.max(units - 1, 0) * p.contacts_per_unit;
}

function dailyBenefit(p: Plan): string {
  const s = p.paid_sending;
  const most = s.most_daily.toLocaleString();
  const free = p.free_plan.daily_sends;
  if (s.needs_domain) {
    return `A daily limit that grows to ${most} emails a day with clean sending, once you add your own domain.`;
  }
  if (s.starting_daily >= s.most_daily) return `Send up to ${most} emails a day.`;
  if (free > 0 && free < s.most_daily) {
    return `A daily limit that grows past ${free.toLocaleString()}, up to ${most} emails a day with clean sending.`;
  }
  return `A daily limit that starts at ${s.starting_daily.toLocaleString()} and grows to ${most} emails a day with clean sending.`;
}

/** What the paid plan gets an account over the free one. */
export function benefits(p: Plan): string[] {
  const pack = p.credit_packs.cheapest;
  return [
    `Room past ${p.free_plan.contacts.toLocaleString()} contacts. The price covers your first ${p.included_contacts.toLocaleString()}.`,
    `No "Sent by mailgoose" line on your emails.`,
    `Credit packs from ${dollars(pack.price_cents)} for ${pack.credits.toLocaleString()} emails, kept for ${p.credit_packs.months} months, on top of the ${p.free_plan.monthly_credits.toLocaleString()} free each month.`,
    dailyBenefit(p),
  ];
}

type UpgradeContext = {
  plan: Loaded<Plan>;
  open: (reason?: string) => void;
};

const Upgrade = createContext<UpgradeContext | null>(null);

/** Opens the upgrade dialog from anywhere inside the signed-in app. */
export function useUpgrade(): UpgradeContext | null {
  return useContext(Upgrade);
}

/** Holds the one upgrade dialog and the plan it sells against. */
export function UpgradeProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const load = useCallback(() => attempt(() => api.GET("/v1/plan")), [api]);
  const { state, reload } = useLoad<Plan>(load);
  const [shown, setShown] = useState<{ reason?: string } | null>(null);
  const opener = useRef<HTMLElement | null>(null);

  const open = useCallback(
    (reason?: string) => {
      opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      setShown({ reason });
      // Contacts change, so the price today is read again each time.
      reload();
    },
    [reload],
  );

  const close = useCallback(() => {
    setShown(null);
    const back = opener.current;
    opener.current = null;
    if (back && back.isConnected) back.focus();
  }, []);

  const value = useMemo(() => ({ plan: state, open }), [state, open]);
  return (
    <Upgrade.Provider value={value}>
      {children}
      {shown && <UpgradeDialog plan={state} reason={shown.reason} onClose={close} onRetry={reload} />}
    </Upgrade.Provider>
  );
}

/**
 * The one way to offer the upgrade. Renders nothing on the paid plan, or
 * outside the signed-in app.
 */
export function UpgradeButton({
  label,
  reason,
  className = "button primary",
}: {
  label?: string;
  reason?: string;
  className?: string;
}) {
  const upgrade = useUpgrade();
  if (!upgrade) return null;
  const p = upgrade.plan;
  if (p.status === "ok" && p.data.plan === "paid" && p.data.status) return null;
  return (
    <button type="button" className={className} onClick={() => upgrade.open(reason)}>
      {label ?? `Upgrade to ${planName}`}
    </button>
  );
}

const focusable =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function UpgradeDialog({
  plan,
  reason,
  onClose,
  onRetry,
}: {
  plan: Loaded<Plan>;
  reason?: string;
  onClose: () => void;
  onRetry: () => void;
}) {
  const api = useApi();
  const title = useId();
  const panel = useRef<HTMLDivElement>(null);
  const [interval, setBillingInterval] = useState<Interval>("month");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  useEffect(() => {
    panel.current?.focus();
    const body = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = body;
    };
  }, []);

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key !== "Tab" || !panel.current) return;
    const items = Array.from(panel.current.querySelectorAll<HTMLElement>(focusable));
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    const at = document.activeElement;
    if (e.shiftKey && (at === first || at === panel.current)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && at === last) {
      e.preventDefault();
      first.focus();
    }
  }

  async function checkout() {
    setBusy(true);
    setFailure(undefined);
    const failed = await openStripe(() => attempt(() => api.POST("/v1/plan/checkout", { body: { interval } })));
    setBusy(false);
    setFailure(failed);
  }

  return (
    <div
      className="modal-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        ref={panel}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={title}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        <div className="modal-head">
          <h2 id={title}>Upgrade to {planName}</h2>
          <button type="button" className="icon-button" aria-label="Close" onClick={onClose}>
            <X size={18} strokeWidth={1.75} aria-hidden="true" />
          </button>
        </div>
        {reason && <p className="modal-reason">{reason}</p>}
        {plan.status === "loading" && <Loading />}
        {plan.status === "failed" && <LoadFailed failure={plan.failure} retry={onRetry} />}
        {plan.status === "ok" && (
          <>
            <p className="muted">
              Keep everything you've built, and take the free plan's limits off. You only pay for the contacts you have.
            </p>
            <fieldset className="plan-options">
              <legend className="visually-hidden">How you'd like to pay</legend>
              <label className="plan-option">
                <input
                  type="radio"
                  name="upgrade-interval"
                  checked={interval === "month"}
                  onChange={() => setBillingInterval("month")}
                />
                <span className="plan-option-name">Monthly</span>
                <span className="plan-option-price">{dollars(plan.data.prices.month_cents)} a month</span>
              </label>
              <label className="plan-option">
                <input
                  type="radio"
                  name="upgrade-interval"
                  checked={interval === "year"}
                  onChange={() => setBillingInterval("year")}
                />
                <span className="plan-option-name">Yearly</span>
                <span className="plan-option-price">{dollars(plan.data.prices.year_cents)} a year</span>
                {monthsFree(plan.data) && <span className="plan-option-note">{monthsFree(plan.data)}</span>}
              </label>
            </fieldset>
            <ul className="benefits" aria-label={`What ${planName} adds`}>
              {benefits(plan.data).map((b) => (
                <li key={b}>
                  <Check className="benefit-icon" size={16} strokeWidth={2} aria-hidden="true" />
                  <span>{b}</span>
                </li>
              ))}
            </ul>
            <p className="small muted">{contactPricing(plan.data, interval)}</p>
            <p className="modal-total">{todayPrice(plan.data, interval)}</p>
            <FailureNotice failure={failure} />
            <div className="modal-actions">
              <button type="button" className="button" onClick={onClose}>
                Not now
              </button>
              <button type="button" className="button primary" disabled={busy} onClick={() => void checkout()}>
                {busy ? "Opening Stripe…" : "Continue to payment"}
              </button>
            </div>
            <p className="small muted">You pay on Stripe's page, and you can cancel any time.</p>
          </>
        )}
      </div>
    </div>
  );
}
