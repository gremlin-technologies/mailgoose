// The form that makes a campaign or changes a draft: its email, who gets
// it (everyone, or contacts with chosen tags, with a live count), when it
// goes out and whether opens and clicks are counted.
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { FailureNotice, LoadFailed, Loading, useLoad } from "../components";
import { useTags } from "../pages/Tags";
import { fromLocalInput, plural, toLocalInput, type Campaign, type RecipientsInput } from "./campaign";

type EmailPage = components["schemas"]["EmailList"];

export type CampaignFields = {
  name: string;
  email_id: string;
  recipients: RecipientsInput;
  send_at: string | null;
  tracking: boolean;
};

/** How long the recipient count waits after the last change. */
export const countDelay = 300;

/** The emails a campaign can pick from: the newest 100. */
const emailLimit = 100;

export function CampaignForm({
  initial,
  submitLabel,
  busyLabel,
  onSubmit,
  onCancel,
}: {
  initial?: Campaign;
  submitLabel: string;
  busyLabel: string;
  /** Saves the fields; returns why it failed, if it did. */
  onSubmit: (fields: CampaignFields) => Promise<Failure | undefined>;
  onCancel?: () => void;
}) {
  const api = useApi();
  const loadEmails = useCallback(
    () => attempt(() => api.GET("/v1/emails", { params: { query: { limit: emailLimit } } })),
    [api],
  );
  const emails = useLoad<EmailPage>(loadEmails);
  const tags = useTags();

  const [name, setName] = useState(initial?.name ?? "");
  const [emailId, setEmailId] = useState(initial?.email_id ?? "");
  const [audience, setAudience] = useState<"all" | "tags">(initial && !initial.recipients.all ? "tags" : "all");
  const [tagIds, setTagIds] = useState<string[]>(initial?.recipients.tags.map((t) => t.id) ?? []);
  const [later, setLater] = useState(!!initial?.send_at);
  const [sendAt, setSendAt] = useState(toLocalInput(initial?.send_at));
  const [tracking, setTracking] = useState(initial?.tracking ?? true);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const [problem, setProblem] = useState<string>();

  const recipients: RecipientsInput | undefined =
    audience === "all" ? { all: true } : tagIds.length ? { tag_ids: tagIds } : undefined;
  const count = useRecipientCount(recipients);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFailure(undefined);
    let at: string | null = null;
    if (!emailId) return setProblem("Pick the email to send.");
    if (!recipients) return setProblem("Pick at least one tag, or send to all subscribed contacts.");
    if (later) {
      const iso = fromLocalInput(sendAt);
      if (!iso) return setProblem("Pick the date and time to send it.");
      at = iso;
    }
    setProblem(undefined);
    setBusy(true);
    const failed = await onSubmit({ name: name.trim(), email_id: emailId, recipients, send_at: at, tracking });
    setBusy(false);
    setFailure(failed);
  }

  if (emails.state.status === "loading" || tags.state.status === "loading") return <Loading />;
  if (emails.state.status === "failed") return <LoadFailed failure={emails.state.failure} retry={emails.reload} />;
  if (tags.state.status === "failed") return <LoadFailed failure={tags.state.failure} retry={tags.reload} />;
  const emailList = emails.state.data.data;
  const tagList = tags.state.data;
  const missingEmail = !!initial && initial.email_id === null;

  return (
    <form className="panel stack campaign-form" onSubmit={submit} aria-label="Campaign">
      <label className="field">
        <span>Campaign name</span>
        <input value={name} onChange={(e) => setName(e.target.value)} maxLength={200} required placeholder="May newsletter" />
      </label>

      <label className="field">
        <span>Email</span>
        {emailList.length === 0 ? (
          <span className="muted">
            You have no emails yet. <Link to="/emails">Make one</Link>, then come back.
          </span>
        ) : (
          <select value={emailId} onChange={(e) => setEmailId(e.target.value)} required>
            <option value="">{missingEmail ? "Its email was deleted; pick another" : "Pick an email"}</option>
            {emailList.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
                {m.subject ? ` (${m.subject})` : ""}
              </option>
            ))}
          </select>
        )}
      </label>

      <fieldset className="choices">
        <legend>Send to</legend>
        <label className="choice">
          <input type="radio" name="audience" checked={audience === "all"} onChange={() => setAudience("all")} />
          All subscribed contacts
        </label>
        <label className="choice">
          <input type="radio" name="audience" checked={audience === "tags"} onChange={() => setAudience("tags")} />
          Contacts with any of these tags
        </label>
        {audience === "tags" &&
          (tagList.length === 0 ? (
            <p className="muted">
              You have no tags yet. <Link to="/tags">Make a tag</Link> and add it to contacts first.
            </p>
          ) : (
            <div className="tag-choices" role="group" aria-label="Tags">
              {tagList.map((t) => (
                <label key={t.id} className="choice">
                  <input
                    type="checkbox"
                    checked={tagIds.includes(t.id)}
                    onChange={(e) => setTagIds((ids) => (e.target.checked ? [...ids, t.id] : ids.filter((i) => i !== t.id)))}
                  />
                  {t.name}
                </label>
              ))}
            </div>
          ))}
        <p className="muted small recipient-count" role="status" aria-label="Recipient count">
          {count}
        </p>
      </fieldset>

      <fieldset className="choices">
        <legend>When</legend>
        <label className="choice">
          <input type="radio" name="when" checked={!later} onChange={() => setLater(false)} />
          As soon as I launch it
        </label>
        <label className="choice">
          <input type="radio" name="when" checked={later} onChange={() => setLater(true)} />
          At a set time
        </label>
        {later && (
          <label className="field">
            <span>Send at (your time)</span>
            <input type="datetime-local" value={sendAt} onChange={(e) => setSendAt(e.target.value)} />
          </label>
        )}
      </fieldset>

      <label className="check">
        <input type="checkbox" checked={tracking} onChange={(e) => setTracking(e.target.checked)} />
        <span>
          Count opens and clicks
          <small className="muted"> Adds an invisible image and sends links through mailgoose. The unsubscribe link is never changed.</small>
        </span>
      </label>

      {problem && (
        <p className="field-error" role="alert">
          {problem}
        </p>
      )}
      <FailureNotice failure={failure} />
      <div className="row">
        <button type="submit" className="button primary" disabled={busy || emailList.length === 0}>
          {busy ? busyLabel : submitLabel}
        </button>
        {onCancel && (
          <button type="button" className="button" onClick={onCancel}>
            Cancel
          </button>
        )}
      </div>
    </form>
  );
}

/** How many subscribed contacts the recipients reach now, counted by the API a moment after a change. */
function useRecipientCount(recipients: RecipientsInput | undefined): string {
  const api = useApi();
  const key = recipients ? JSON.stringify(recipients) : "";
  const [answer, setAnswer] = useState<{ key: string; text: string }>({ key: "", text: "" });

  useEffect(() => {
    if (!key) return;
    let current = true;
    const timer = setTimeout(() => {
      void attempt(() => api.POST("/v1/campaigns/recipient-count", { body: { recipients: JSON.parse(key) as RecipientsInput } })).then(
        (res) => {
          if (!current) return;
          const text = res.ok
            ? `Reaches ${plural(res.data?.recipient_count ?? 0, "subscribed contact")} now.`
            : "Couldn't count the recipients just now.";
          setAnswer({ key, text });
        },
      );
    }, countDelay);
    return () => {
      current = false;
      clearTimeout(timer);
    };
  }, [api, key]);

  if (!key) return "Pick at least one tag.";
  return answer.key === key ? answer.text : "Counting…";
}
