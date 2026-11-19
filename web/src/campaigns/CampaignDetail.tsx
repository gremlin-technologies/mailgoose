// One campaign: a draft is edited, checked, tested and launched here; a
// scheduled one can be moved or cancelled; a sending one shows its status
// as it changes.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import { Badge, FailureNotice, formatDate, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { fromLocalInput, plural, recipientsText, statusBadge, toLocalInput, type Campaign } from "./campaign";
import { CampaignForm } from "./CampaignForm";
import { CheckStep, LaunchStep, TestStep } from "./steps";

/** How often a sending campaign is looked at again. */
export const pollEvery = 5000;

const changedElsewhere: Failure = {
  code: "version_conflict",
  message: "This campaign was changed somewhere else, so it has been reloaded.",
  hint: "Make your change again on the campaign as it is now.",
  status: 409,
};

export function CampaignDetail() {
  const api = useApi();
  const { id = "" } = useParams();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/campaigns/{campaign_id}", { params: { path: { campaign_id: id } } })),
    [api, id],
  );
  const { state, reload, replace } = useLoad<Campaign>(load);
  const [notice, setNotice] = useState<string>();
  const [failure, setFailure] = useState<Failure>();
  const status = state.status === "ok" ? state.data.status : undefined;

  // A sending campaign's status changes on its own; look again now and then.
  useEffect(() => {
    if (status !== "sending") return;
    const timer = setInterval(reload, pollEvery);
    return () => clearInterval(timer);
  }, [status, reload]);

  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") {
    return (
      <section className="page">
        <p className="crumb">
          <Link to="/campaigns">Campaigns</Link>
        </p>
        <PageHeader title="Campaign" />
        <LoadFailed failure={state.failure} retry={reload} />
      </section>
    );
  }
  const c = state.data;
  const badge = statusBadge[c.status];
  // A change refreshes the page from the answer, which has no live count.
  const changed = (next: Campaign) => {
    setFailure(undefined);
    replace({ ...next, recipient_count: next.recipient_count ?? c.recipient_count });
  };

  return (
    <section className="page">
      <p className="crumb">
        <Link to="/campaigns">Campaigns</Link>
      </p>
      <PageHeader title={c.name} about={<StatusLine campaign={c} />}>
        <Badge tone={badge.tone}>{badge.label}</Badge>
      </PageHeader>
      {notice && (
        <p className="notice notice-good" role="status">
          {notice}
        </p>
      )}
      <FailureNotice failure={failure} />

      {c.status === "draft" ? (
        <Draft
          campaign={c}
          changed={changed}
          conflicted={() => {
            setFailure(changedElsewhere);
            reload();
          }}
          launched={(next) => {
            changed(next);
            setNotice(
              next.status === "scheduled"
                ? `Scheduled. It goes out ${formatDate(next.send_at)}.`
                : `Launched. It's on its way to ${plural(next.launch?.recipient_count ?? 0, "contact")}.`,
            );
          }}
        />
      ) : (
        <>
          <Facts campaign={c} />
          {c.status !== "scheduled" && c.status !== "cancelled" && (
            <div>
              <Link to={`/campaigns/${c.id}/report`} className="button">
                See the report
              </Link>
            </div>
          )}
          {c.status === "scheduled" && (
            <Scheduled
              campaign={c}
              changed={(next, message) => {
                changed(next);
                setNotice(message);
              }}
            />
          )}
        </>
      )}
    </section>
  );
}

/** One line on where the campaign stands. */
function StatusLine({ campaign: c }: { campaign: Campaign }) {
  const n = c.launch?.recipient_count ?? 0;
  switch (c.status) {
    case "draft":
      return <>Not sent. Check the email, send a test, then launch it.</>;
    case "scheduled":
      return <>Goes out {formatDate(c.send_at)} to {plural(n, "contact")}. You can move or cancel it until then.</>;
    case "sending":
      return <>Sending to {plural(n, "contact")} now. This page updates by itself.</>;
    case "sent":
      return <>Sent to {plural(n, "contact")}.</>;
    case "cancelled":
      return <>Cancelled. Nobody else gets it, and the credits held for it came back.</>;
    case "paused":
      return <>Paused by mailgoose. We'll be in touch by email.</>;
    case "failed":
      return <>It couldn't be sent. Credits for anyone who didn't get it came back.</>;
  }
}

/** The campaign's email, by name, linked to its editor. */
function EmailName({ emailId }: { emailId: string | null }) {
  const api = useApi();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/emails/{email_id}", { params: { path: { email_id: emailId ?? "" } } })),
    [api, emailId],
  );
  const { state } = useLoad(load);
  if (!emailId) return <>Deleted. Pick another email.</>;
  if (state.status !== "ok") return <Link to={`/emails/${emailId}`}>Open the email</Link>;
  return <Link to={`/emails/${emailId}`}>{state.data.name}</Link>;
}

function Facts({ campaign: c }: { campaign: Campaign }) {
  return (
    <dl className="facts panel">
      <dt>Email</dt>
      <dd>
        <EmailName emailId={c.email_id} />
      </dd>
      <dt>To</dt>
      <dd>
        {recipientsText(c.recipients)}
        {c.launch
          ? `: ${plural(c.launch.recipient_count, "contact")} when launched`
          : c.recipient_count !== undefined && `: ${plural(c.recipient_count, "contact")} now`}
      </dd>
      <dt>When</dt>
      <dd>
        {c.status === "draft"
          ? c.send_at
            ? formatDate(c.send_at)
            : "As soon as you launch it"
          : c.launch
            ? `Launched ${formatDate(c.launch.at)}${c.send_at ? `, for ${formatDate(c.send_at)}` : ""}`
            : formatDate(c.send_at)}
      </dd>
      <dt>Opens and clicks</dt>
      <dd>{c.tracking ? "Counted" : "Not counted"}</dd>
      {c.launch && (
        <>
          <dt>Credits held</dt>
          <dd>{c.launch.credits_reserved.toLocaleString()}</dd>
        </>
      )}
    </dl>
  );
}

function Draft({
  campaign: c,
  changed,
  conflicted,
  launched,
}: {
  campaign: Campaign;
  changed: (c: Campaign) => void;
  conflicted: () => void;
  launched: (c: Campaign) => void;
}) {
  const api = useApi();
  const [editing, setEditing] = useState(false);

  if (editing) {
    return (
      <CampaignForm
        initial={c}
        submitLabel="Save changes"
        busyLabel="Saving…"
        onCancel={() => setEditing(false)}
        onSubmit={async (fields) => {
          const res = await attempt(() =>
            api.PATCH("/v1/campaigns/{campaign_id}", {
              params: { path: { campaign_id: c.id } },
              body: { ...fields, version: c.version },
            }),
          );
          if (!res.ok) {
            if (res.failure.code !== "version_conflict") return res.failure;
            setEditing(false);
            conflicted();
            return undefined;
          }
          setEditing(false);
          if (res.data) changed(res.data);
          return undefined;
        }}
      />
    );
  }
  return (
    <>
      <Facts campaign={c} />
      <div>
        <button type="button" className="button" onClick={() => setEditing(true)}>
          Edit campaign
        </button>
      </div>
      <h2>Before you launch</h2>
      <ol className="steps">
        <CheckStep emailId={c.email_id} />
        <TestStep campaign={c} onSent={changed} />
        <LaunchStep campaign={c} onLaunched={launched} />
      </ol>
      <DeleteDraft campaign={c} />
    </>
  );
}

function DeleteDraft({ campaign: c }: { campaign: Campaign }) {
  const api = useApi();
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function remove() {
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/campaigns/{campaign_id}", { params: { path: { campaign_id: c.id } } }));
    if (!res.ok) return setFailure(res.failure);
    navigate("/campaigns");
  }

  return (
    <div className="panel danger-zone">
      <h2>Delete this draft</h2>
      <p className="muted">The email itself stays. Only drafts can be deleted.</p>
      {confirm ? (
        <div className="row">
          <button type="button" className="button danger" onClick={() => void remove()}>
            Delete {c.name}
          </button>
          <button type="button" className="button" onClick={() => setConfirm(false)}>
            Cancel
          </button>
        </div>
      ) : (
        <div>
          <button type="button" className="button" onClick={() => setConfirm(true)}>
            Delete draft
          </button>
        </div>
      )}
      <FailureNotice failure={failure} />
    </div>
  );
}

/** A scheduled campaign can be moved to another time or cancelled until it starts (CAM-6). */
function Scheduled({ campaign: c, changed }: { campaign: Campaign; changed: (c: Campaign, message: string) => void }) {
  const api = useApi();
  const [at, setAt] = useState(toLocalInput(c.send_at));
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [problem, setProblem] = useState<string>();

  async function move(e: FormEvent) {
    e.preventDefault();
    const iso = fromLocalInput(at);
    if (!iso) return setProblem("Pick the new date and time.");
    setProblem(undefined);
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() =>
      api.POST("/v1/campaigns/{campaign_id}/reschedule", { params: { path: { campaign_id: c.id } }, body: { send_at: iso } }),
    );
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    if (res.data) changed(res.data, `Moved. It now goes out ${formatDate(res.data.send_at)}.`);
  }

  async function cancel() {
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.POST("/v1/campaigns/{campaign_id}/cancel", { params: { path: { campaign_id: c.id } } }));
    setBusy(false);
    setConfirm(false);
    if (!res.ok) return setFailure(res.failure);
    if (res.data) changed(res.data, "Cancelled. Nobody gets it, and the credits held for it came back.");
  }

  return (
    <>
      <form className="panel row-form" onSubmit={move} aria-label="Move send time">
        <label className="field grow">
          <span>New send time (your time)</span>
          <input type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} />
        </label>
        <button type="submit" className="button" disabled={busy}>
          Move send time
        </button>
      </form>
      {problem && (
        <p className="field-error" role="alert">
          {problem}
        </p>
      )}
      <div className="panel danger-zone">
        <h2>Cancel this campaign</h2>
        <p className="muted">
          Nobody gets it, and the {plural(c.launch?.credits_reserved ?? 0, "credit")} held for it come back. A cancelled
          campaign can't be launched again.
        </p>
        {confirm ? (
          <div className="row">
            <button type="button" className="button danger" onClick={() => void cancel()} disabled={busy}>
              Yes, cancel it
            </button>
            <button type="button" className="button" onClick={() => setConfirm(false)}>
              Keep it
            </button>
          </div>
        ) : (
          <div>
            <button type="button" className="button" onClick={() => setConfirm(true)}>
              Cancel campaign
            </button>
          </div>
        )}
      </div>
      <FailureNotice failure={failure} />
    </>
  );
}
