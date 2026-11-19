// A campaign's report (REP-1, REP-2): the same numbers `mailgoose campaign
// report` prints, as tiles that stack on a phone, and clicks per link.
import { useCallback, useEffect } from "react";
import { Link, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt } from "../api/result";
import type { components } from "../api/schema";
import { Badge, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { statusBadge } from "./campaign";
import { polling } from "./CampaignDetail";

type Report = components["schemas"]["CampaignReport"];

/** A count and its share of the copies sent, as the CLI shows it: "40 (33.3%)". */
export function share(n: number, of: number): string {
  if (of === 0) return n.toLocaleString();
  return `${n.toLocaleString()} (${((100 * n) / of).toFixed(1)}%)`;
}

export function CampaignReport() {
  const api = useApi();
  const { id = "" } = useParams();
  const loadCampaign = useCallback(
    () => attempt(() => api.GET("/v1/campaigns/{campaign_id}", { params: { path: { campaign_id: id } } })),
    [api, id],
  );
  const loadReport = useCallback(
    () => attempt(() => api.GET("/v1/campaigns/{campaign_id}/report", { params: { path: { campaign_id: id } } })),
    [api, id],
  );
  const campaign = useLoad(loadCampaign);
  const report = useLoad<Report>(loadReport);
  const sending = report.state.status === "ok" && report.state.data.status === "sending";
  const reloadReport = report.reload;

  // While it's sending, the numbers move; count them again now and then.
  useEffect(() => {
    if (!sending) return;
    const timer = setInterval(reloadReport, polling.every);
    return () => clearInterval(timer);
  }, [sending, reloadReport]);

  const name = campaign.state.status === "ok" ? campaign.state.data.name : "Campaign";
  return (
    <section className="page">
      <p className="crumb">
        <Link to="/campaigns">Campaigns</Link> / <Link to={`/campaigns/${id}`}>{name}</Link>
      </p>
      {report.state.status === "loading" && <Loading />}
      {report.state.status === "failed" && (
        <>
          <PageHeader title={`${name}: report`} />
          <LoadFailed failure={report.state.failure} retry={report.reload} />
        </>
      )}
      {report.state.status === "ok" && <ReportBody name={name} report={report.state.data} refresh={report.reload} />}
    </section>
  );
}

function ReportBody({ name, report: r, refresh }: { name: string; report: Report; refresh: () => void }) {
  const badge = statusBadge[r.status];
  const n = r.recipients;
  if (!r.launched_at) {
    return (
      <>
        <PageHeader title={`${name}: report`}>
          <Badge tone={badge.tone}>{badge.label}</Badge>
        </PageHeader>
        <p className="empty">Nothing sent yet. The report fills in once the campaign is launched.</p>
      </>
    );
  }
  const asOf = r.archived
    ? `Saved ${formatDate(r.as_of)}, when its raw events were deleted, 12 months after launch.`
    : `As of ${formatDate(r.as_of)}.`;
  const tiles: [string, string][] = [
    ["Sent", n.toLocaleString()],
    ["Delivered", share(r.delivered, n)],
    ["Bounced", share(r.bounced, n)],
    ["Complaints", share(r.complaints, n)],
    ["Unsubscribes", share(r.unsubscribes, n)],
  ];
  if (r.opens && r.clicks) {
    tiles.push(["Opens (estimated)", share(r.opens.unique, n)]);
    tiles.push(["Clicks", `${share(r.clicks.unique, n)} unique, ${r.clicks.total.toLocaleString()} total`]);
  }
  tiles.push(["Credits used", r.credits_used.toLocaleString()]);

  return (
    <>
      <PageHeader title={`${name}: report`} about={`Launched ${formatDate(r.launched_at)}. ${asOf}`}>
        <Badge tone={badge.tone}>{badge.label}</Badge>
      </PageHeader>
      {!r.archived && (
        <div>
          <button type="button" className="button small" onClick={refresh}>
            Count again
          </button>
        </div>
      )}
      <dl className="tiles" aria-label="Numbers">
        {tiles.map(([label, value]) => (
          <div key={label} className="tile">
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
      {r.opens && r.clicks ? (
        <p className="muted small">
          Opens are an estimate: some mail apps load images for privacy, which opens every copy, and others block them,
          which opens none. Clicks are exact.
        </p>
      ) : (
        <p className="muted small">Opens and clicks weren't counted for this campaign.</p>
      )}
      {r.status === "sending" && <p className="muted small">Credits used include those held for copies still to go.</p>}

      {r.opens && r.clicks && (
        <>
          <h2>Clicks per link</h2>
          {r.links.length === 0 ? (
            <p className="empty">No clicks yet.</p>
          ) : (
            <ol className="link-clicks" aria-label="Clicks per link">
              {r.links.map((l) => (
                <li key={l.position}>
                  <span className="link-url">
                    {l.position}. {l.url}
                    {l.varies && <span className="muted"> (differs per contact)</span>}
                  </span>
                  <span className="link-counts">
                    {l.unique_clicks.toLocaleString()} unique, {l.total_clicks.toLocaleString()} total
                  </span>
                </li>
              ))}
            </ol>
          )}
        </>
      )}
    </>
  );
}
