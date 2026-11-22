// Who is behind a report's number (MG-112): a campaign's recipients by what
// they did, a page at a time, with the whole list as a CSV download.
import { useCallback, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";

import { useApi } from "../api/context";
import { attempt } from "../api/result";
import type { components } from "../api/schema";
import { formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";

type Activity = components["schemas"]["ReportActivity"];
type Page = components["schemas"]["ReportRecipientList"];

/** Each list's tab label, its heading, and the time column's name (none for the not_ lists). */
export const activities: { value: Activity; tab: string; title: string; when?: string }[] = [
  { value: "opened", tab: "Opened", title: "Who opened", when: "First opened" },
  { value: "not_opened", tab: "Didn't open", title: "Who got it and didn't open" },
  { value: "clicked", tab: "Clicked", title: "Who clicked", when: "First clicked" },
  { value: "not_clicked", tab: "Didn't click", title: "Who got it and didn't click" },
  { value: "bounced", tab: "Bounced", title: "Who hard bounced", when: "Bounced" },
  { value: "complained", tab: "Complained", title: "Who marked it as spam", when: "Complained" },
  { value: "unsubscribed", tab: "Unsubscribed", title: "Who unsubscribed", when: "Unsubscribed" },
];

/** The page listing activity's recipients, narrowed to one link for clicks. */
export function recipientsPath(id: string, activity: Activity, link?: number): string {
  return `/campaigns/${id}/report/${activity}${link ? `?link=${link}` : ""}`;
}

export function ReportRecipients() {
  const { id = "", activity = "" } = useParams();
  const [params] = useSearchParams();
  const link = params.get("link") ?? "";
  // A new list starts at its first page.
  return <RecipientList key={`${id}/${activity}/${link}`} id={id} raw={activity} linkParam={Number(link)} />;
}

function RecipientList({ id, raw, linkParam }: { id: string; raw: string; linkParam: number }) {
  const api = useApi();
  const kind = activities.find((a) => a.value === raw);
  const link = kind?.value === "clicked" && Number.isInteger(linkParam) && linkParam > 0 ? linkParam : undefined;
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const loadCampaign = useCallback(
    () => attempt(() => api.GET("/v1/campaigns/{campaign_id}", { params: { path: { campaign_id: id } } })),
    [api, id],
  );
  const load = useCallback(
    () =>
      attempt(() =>
        api.GET("/v1/campaigns/{campaign_id}/report/recipients", {
          params: {
            path: { campaign_id: id },
            query: { activity: (kind?.value ?? "opened") as Activity, limit: 50, cursor, ...(link ? { link } : {}) },
          },
        }),
      ),
    [api, id, kind, link, cursor],
  );
  const campaign = useLoad(loadCampaign);
  const { state, reload } = useLoad<Page>(load);
  const name = campaign.state.status === "ok" ? campaign.state.data.name : "Campaign";
  const report = `/campaigns/${id}/report`;
  if (!kind) {
    return (
      <section className="page">
        <p className="empty">
          There's no such list. <Link to={report}>Back to the report</Link>.
        </p>
      </section>
    );
  }
  const title = `${kind.title}${link ? ` link ${link}` : ""}`;
  const csv = `/v1/campaigns/${id}/report/recipients/export?activity=${kind.value}${link ? `&link=${link}` : ""}`;

  return (
    <section className="page">
      <p className="crumb">
        <Link to="/campaigns">Campaigns</Link> / <Link to={`/campaigns/${id}`}>{name}</Link> / <Link to={report}>Report</Link>
      </p>
      <PageHeader title={title} about="A contact deleted since the send isn't listed, so a list can be shorter than its number.">
        <a href={csv} className="button" download>
          Download CSV
        </a>
      </PageHeader>
      <nav className="row segmented" aria-label="Lists">
        {activities.map((a) => (
          <Link
            key={a.value}
            to={recipientsPath(id, a.value)}
            className="button small"
            aria-current={a.value === kind.value && !link ? "page" : undefined}
          >
            {a.tab}
          </Link>
        ))}
      </nav>
      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.data.length === 0 && !cursor ? (
          <p className="empty">Nobody yet.</p>
        ) : (
          <>
            <table className="table">
              <thead>
                <tr>
                  <th>Email</th>
                  <th>Name</th>
                  {kind.when && <th>{kind.when}</th>}
                </tr>
              </thead>
              <tbody>
                {state.data.data.map((r) => (
                  <tr key={r.contact_id}>
                    <td>
                      <Link to={`/contacts/${r.contact_id}`}>{r.email}</Link>
                    </td>
                    <td>{[r.first_name, r.last_name].filter(Boolean).join(" ")}</td>
                    {kind.when && <td className="muted">{formatDate(r.at)}</td>}
                  </tr>
                ))}
              </tbody>
            </table>
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
