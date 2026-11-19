import { useCallback, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";

import { useApi } from "../api/context";
import { attempt } from "../api/result";
import type { components } from "../api/schema";
import { Badge, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { recipientsText, statusBadge, type Campaign, type CampaignStatus } from "../campaigns/campaign";
import { CampaignForm } from "../campaigns/CampaignForm";

type Page = components["schemas"]["CampaignList"];

const filters: { value: CampaignStatus | ""; label: string }[] = [
  { value: "", label: "All" },
  { value: "draft", label: "Drafts" },
  { value: "scheduled", label: "Scheduled" },
  { value: "sending", label: "Sending" },
  { value: "sent", label: "Sent" },
];

/** When a campaign went out, goes out, or that it hasn't been launched. */
export function whenText(c: Campaign): string {
  if (c.status === "draft") return c.send_at ? `Set for ${formatDate(c.send_at)}` : "When you launch it";
  if (c.status === "scheduled") return `Goes out ${formatDate(c.send_at)}`;
  if (c.launch) return `Launched ${formatDate(c.launch.at)}`;
  return "";
}

/** The account's campaigns, newest first, with a filter by status. */
export function Campaigns() {
  const api = useApi();
  const [params, setParams] = useSearchParams();
  const status = (params.get("status") ?? "") as CampaignStatus | "";
  const [cursors, setCursors] = useState<string[]>([]);
  const cursor = cursors.at(-1);
  const load = useCallback(
    () =>
      attempt(() =>
        api.GET("/v1/campaigns", { params: { query: { limit: 50, cursor, ...(status ? { status } : {}) } } }),
      ),
    [api, cursor, status],
  );
  const { state, reload } = useLoad<Page>(load);

  return (
    <section className="page">
      <PageHeader title="Campaigns" about="Send an email to your contacts. Test it, then launch it now or at a set time.">
        <Link to="/campaigns/new" className="button primary">
          New campaign
        </Link>
      </PageHeader>
      <div className="row segmented" role="group" aria-label="Show">
        {filters.map((f) => (
          <button
            key={f.value}
            type="button"
            className="button small"
            aria-pressed={status === f.value}
            onClick={() => {
              setCursors([]);
              setParams(f.value ? { status: f.value } : {});
            }}
          >
            {f.label}
          </button>
        ))}
      </div>

      {state.status === "loading" && <Loading />}
      {state.status === "failed" && <LoadFailed failure={state.failure} retry={reload} />}
      {state.status === "ok" &&
        (state.data.data.length === 0 && !cursor ? (
          <p className="empty">
            {status ? "No campaigns here." : "No campaigns yet. Start one to send an email to your contacts."}
          </p>
        ) : (
          <>
            <table className="table">
              <thead>
                <tr>
                  <th>Campaign</th>
                  <th>Status</th>
                  <th>To</th>
                  <th>When</th>
                </tr>
              </thead>
              <tbody>
                {state.data.data.map((c) => (
                  <tr key={c.id}>
                    <td>
                      <Link to={`/campaigns/${c.id}`}>{c.name}</Link>
                    </td>
                    <td>
                      <Badge tone={statusBadge[c.status].tone}>{statusBadge[c.status].label}</Badge>
                    </td>
                    <td className="muted">
                      {c.launch ? `${c.launch.recipient_count.toLocaleString()} contacts` : recipientsText(c.recipients)}
                    </td>
                    <td className="muted">{whenText(c)}</td>
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

/** Starts a draft campaign, then opens it to test and launch. */
export function NewCampaign() {
  const api = useApi();
  const navigate = useNavigate();
  return (
    <section className="page">
      <p className="crumb">
        <Link to="/campaigns">Campaigns</Link>
      </p>
      <PageHeader title="New campaign" about="Nothing is sent yet. Next you'll check the email, send yourself a test, and launch it." />
      <CampaignForm
        submitLabel="Save draft"
        busyLabel="Saving…"
        onCancel={() => navigate("/campaigns")}
        onSubmit={async (fields) => {
          const res = await attempt(() => api.POST("/v1/campaigns", { body: fields }));
          if (!res.ok) return res.failure;
          if (res.data) navigate(`/campaigns/${res.data.id}`);
          return undefined;
        }}
      />
    </section>
  );
}
