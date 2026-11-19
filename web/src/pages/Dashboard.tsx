import { useCallback } from "react";
import { Link } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Result } from "../api/result";
import type { components } from "../api/schema";
import { Badge, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { recipientsText, statusBadge } from "../campaigns/campaign";
import { whenText } from "./Campaigns";

type Campaign = components["schemas"]["Campaign"];

/** The account's state that the checklist and the page show, read from the API. */
export type Overview = {
  domainVerified: boolean;
  hasContacts: boolean;
  hasEmail: boolean;
  tested: boolean;
  launched: boolean;
  balance: number;
  nextGrantAt: string | null;
  campaigns: Campaign[];
};

/** How many of the newest campaigns are looked at for a test or a launch, and shown. */
const campaignsRead = 50;
const campaignsShown = 5;

export type Step = { key: string; label: string; about: string; to: string; done: boolean };

/** The getting-started checklist (WEB-1), each step ticked from the account's real state. */
export function steps(o: Overview): Step[] {
  return [
    { key: "domain", label: "Verify your domain", about: "So your emails come from your own address.", to: "/domains", done: o.domainVerified },
    { key: "contacts", label: "Add contacts", about: "Import a CSV, add people one by one, or put a signup form on your site.", to: "/contacts", done: o.hasContacts },
    { key: "email", label: "Build an email", about: "With blocks, or your own HTML.", to: "/emails", done: o.hasEmail },
    { key: "test", label: "Send a test", about: "Make a campaign and send yourself a copy.", to: "/campaigns", done: o.tested },
    { key: "launch", label: "Launch a campaign", about: "Check the summary, then send it or schedule it.", to: "/campaigns", done: o.launched },
  ];
}

function useOverview() {
  const api = useApi();
  const load = useCallback(async (): Promise<Result<Overview>> => {
    const [domains, contacts, emails, campaigns, credits] = await Promise.all([
      attempt(() => api.GET("/v1/domains")),
      attempt(() => api.GET("/v1/contacts", { params: { query: { limit: 1 } } })),
      attempt(() => api.GET("/v1/emails", { params: { query: { limit: 1 } } })),
      attempt(() => api.GET("/v1/campaigns", { params: { query: { limit: campaignsRead } } })),
      attempt(() => api.GET("/v1/credits")),
    ]);
    for (const r of [domains, contacts, emails, campaigns, credits]) if (!r.ok) return r;
    const list = (campaigns.ok && campaigns.data?.data) || [];
    return {
      ok: true,
      data: {
        domainVerified: !!(domains.ok && domains.data?.data.some((d) => d.status === "verified")),
        hasContacts: !!(contacts.ok && contacts.data?.data.length),
        hasEmail: !!(emails.ok && emails.data?.data.length),
        tested: list.some((c) => c.last_test !== null || c.launch !== null),
        launched: list.some((c) => c.launch !== null),
        balance: (credits.ok && credits.data?.balance) || 0,
        nextGrantAt: (credits.ok && credits.data?.next_grant_at) || null,
        campaigns: list.slice(0, campaignsShown),
      },
    };
  }, [api]);
  return useLoad<Overview>(load);
}

/** The first page after signing in: what's left to set up, the balance, and the latest campaigns. */
export function Dashboard() {
  const { state, reload } = useOverview();

  return (
    <section className="page">
      <PageHeader title="Dashboard" />
      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" && <Body overview={state.data} />}
    </section>
  );
}

function Body({ overview: o }: { overview: Overview }) {
  const list = steps(o);
  const left = list.filter((s) => !s.done).length;
  const next = list.find((s) => !s.done);
  return (
    <>
      {left > 0 && (
        <section className="panel stack" aria-label="Getting started">
          <h2>Getting started</h2>
          <p className="muted">
            {left === list.length ? "Five steps to your first campaign." : `${left} of ${list.length} steps left.`}
          </p>
          <ol className="checklist">
            {list.map((s) => (
              <li key={s.key} className={s.done ? "done" : s === next ? "next" : undefined}>
                <span className="tick" aria-hidden="true">
                  {s.done ? "✓" : ""}
                </span>
                <span className="checklist-text">
                  <Link to={s.to}>{s.label}</Link>
                  <span className="visually-hidden">{s.done ? " (done)" : " (to do)"}</span>
                  {!s.done && <span className="muted small"> {s.about}</span>}
                </span>
              </li>
            ))}
          </ol>
        </section>
      )}

      <div className="grid-2">
        <section className="panel stack" aria-label="Credits">
          <h2>Credits</h2>
          <p className="balance">
            <span className="balance-number">{o.balance.toLocaleString()}</span> credits
          </p>
          {o.nextGrantAt && <p className="muted small">1,000 more arrive {formatDate(o.nextGrantAt)}.</p>}
          <Link to="/credits">Credits and billing</Link>
        </section>
        <section className="panel stack" aria-label="Start something">
          <h2>Start something</h2>
          <div className="row">
            <Link to="/campaigns/new" className="button primary">
              New campaign
            </Link>
            <Link to="/emails" className="button">
              New email
            </Link>
          </div>
        </section>
      </div>

      <section className="stack" aria-label="Latest campaigns">
        <h2>Latest campaigns</h2>
        {o.campaigns.length === 0 ? (
          <p className="empty">No campaigns yet.</p>
        ) : (
          <ul className="history">
            {o.campaigns.map((c) => (
              <li key={c.id}>
                <span className="history-what">
                  <Link to={c.launch && c.status !== "scheduled" && c.status !== "cancelled" ? `/campaigns/${c.id}/report` : `/campaigns/${c.id}`}>
                    {c.name}
                  </Link>
                  <span className="muted small">
                    {" "}
                    {c.launch ? `${c.launch.recipient_count.toLocaleString()} contacts` : recipientsText(c.recipients)}. {whenText(c)}
                  </span>
                </span>
                <Badge tone={statusBadge[c.status].tone}>{statusBadge[c.status].label}</Badge>
              </li>
            ))}
          </ul>
        )}
        <Link to="/campaigns">All campaigns</Link>
      </section>
    </>
  );
}
