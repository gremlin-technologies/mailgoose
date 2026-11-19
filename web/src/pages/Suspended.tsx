import { useState } from "react";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { AuthCard, FailureNotice, formatDate } from "../components";
import { useSession, type Session } from "../session";

type Campaign = components["schemas"]["Campaign"];

/** Whether the 30 days to export data are still running. */
export function canExport(exportUntil: string, now = new Date()): boolean {
  return now.getTime() < new Date(exportUntil).getTime();
}

/**
 * What a suspended account sees in place of the app (MG-65): why, how to
 * appeal, and its contacts and campaigns to download while the export runs.
 */
export function Suspended({ session }: { session: Session }) {
  const api = useApi();
  const { signOut } = useSession();
  const [failure, setFailure] = useState<Failure>();
  const [busy, setBusy] = useState(false);
  const suspension = session.account.suspension;
  if (!suspension) return null;
  const exportable = canExport(suspension.export_until);

  const exportCampaigns = async () => {
    setBusy(true);
    setFailure(undefined);
    const all: Campaign[] = [];
    let cursor: string | undefined;
    for (;;) {
      const res = await attempt(() => api.GET("/v1/campaigns", { params: { query: { limit: 100, cursor } } }));
      if (!res.ok || !res.data) {
        if (!res.ok) setFailure(res.failure);
        setBusy(false);
        return;
      }
      all.push(...res.data.data);
      if (!res.data.next_cursor) break;
      cursor = res.data.next_cursor;
    }
    download("mailgoose-campaigns.json", JSON.stringify({ campaigns: all }, null, 2));
    setBusy(false);
  };

  return (
    <AuthCard title="Your account is suspended">
      <div className="stack">
        <p>
          {session.account.name} was suspended on {formatDate(suspension.suspended_at)}, so it can't send, sign
          people up or change anything.
        </p>
        <p>
          <strong>Why:</strong> {suspension.reason}
        </p>
        <p>To appeal, reply to the email we sent {session.user.email} about the suspension.</p>
        {exportable ? (
          <>
            <p>Until {formatDate(suspension.export_until)}, you can download your data.</p>
            <div className="row">
              <a className="button" href="/v1/contacts/export" download>
                Download contacts (CSV)
              </a>
              <button type="button" className="button" disabled={busy} onClick={() => void exportCampaigns()}>
                {busy ? "Preparing…" : "Download campaigns (JSON)"}
              </button>
            </div>
          </>
        ) : (
          <p>The 30 days to download your data ended on {formatDate(suspension.export_until)}.</p>
        )}
        <FailureNotice failure={failure} />
        <button type="button" className="button" onClick={() => void signOut()}>
          Sign out
        </button>
      </div>
    </AuthCard>
  );
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}
