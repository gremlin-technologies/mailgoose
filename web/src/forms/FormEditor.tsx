// One signup form: every option (FRM-1), a preview of the saved form, its
// embed code to copy (FRM-2), and its signup and confirmation counts (FRM-6).
import { useCallback, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { Badge, CopyButton, FailureNotice, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import { useTags, type Tag } from "../pages/Tags";

type Form = components["schemas"]["Form"];
type FieldName = components["schemas"]["FormField"]["name"];
type Embed = components["schemas"]["FormEmbed"];

export const defaultLabels: Record<FieldName, string> = { email: "Email", first_name: "First name", last_name: "Last name" };
const fieldNames: FieldName[] = ["email", "first_name", "last_name"];
const maxTags = 20;

type FieldRow = { name: FieldName; on: boolean; label: string; required: boolean };

/** The form's fields in its order, then the ones it doesn't ask for, switched off. */
export function fieldRows(form: Form): FieldRow[] {
  const rows: FieldRow[] = form.fields.map((f) => ({ name: f.name, on: true, label: f.label ?? "", required: f.name === "email" || f.required }));
  for (const name of fieldNames) {
    if (!rows.some((r) => r.name === name)) rows.push({ name, on: false, label: "", required: false });
  }
  return rows;
}

type Values = {
  name: string;
  fields: FieldRow[];
  tagIds: string[];
  buttonText: string;
  after: "message" | "redirect";
  successMessage: string;
  redirectUrl: string;
  doubleOptIn: boolean;
  confirmSubject: string;
  confirmText: string;
};

function valuesOf(f: Form): Values {
  return {
    name: f.name,
    fields: fieldRows(f),
    tagIds: f.tags.map((t) => t.id),
    buttonText: f.button_text,
    after: f.redirect_url ? "redirect" : "message",
    successMessage: f.success_message,
    redirectUrl: f.redirect_url ?? "",
    doubleOptIn: f.double_opt_in,
    confirmSubject: f.confirm_subject,
    confirmText: f.confirm_text,
  };
}

/** The PATCH body for the values: every option, so the form is saved as shown. */
export function bodyOf(v: Values) {
  return {
    name: v.name.trim(),
    fields: v.fields
      .filter((r) => r.on || r.name === "email")
      .map((r) => ({ name: r.name, label: r.label.trim(), required: r.name === "email" || r.required })),
    tag_ids: v.tagIds,
    button_text: v.buttonText.trim(),
    success_message: v.successMessage.trim(),
    redirect_url: v.after === "redirect" ? v.redirectUrl.trim() : "",
    double_opt_in: v.doubleOptIn,
    confirm_subject: v.confirmSubject.trim(),
    confirm_text: v.confirmText.trim(),
  };
}

export function FormEditor() {
  const api = useApi();
  const { id = "" } = useParams();
  const load = useCallback(() => attempt(() => api.GET("/v1/forms/{form_id}", { params: { path: { form_id: id } } })), [api, id]);
  const { state, reload, replace } = useLoad<Form>(load);
  const tags = useTags();
  // Bumped on each save, so the preview and embed code are fetched again.
  const [saves, setSaves] = useState(0);
  const [justSaved, setJustSaved] = useState(false);

  if (state.status === "loading" || tags.state.status === "loading") return <Loading />;
  if (state.status === "failed" || tags.state.status === "failed") {
    const failed = state.status === "failed" ? state.failure : tags.state.status === "failed" ? tags.state.failure : undefined;
    return (
      <section className="page">
        <p className="crumb">
          <Link to="/forms">Signup forms</Link>
        </p>
        <PageHeader title="Signup form" />
        {failed && (
          <LoadFailed
            failure={failed}
            retry={() => {
              reload();
              tags.reload();
            }}
          />
        )}
      </section>
    );
  }
  const f = state.data;

  return (
    <section className="page">
      <p className="crumb">
        <Link to="/forms">Signup forms</Link>
      </p>
      <PageHeader title={f.name}>
        {f.double_opt_in ? <Badge tone="good">Confirm by email</Badge> : <Badge tone="neutral">No confirmation</Badge>}
      </PageHeader>
      <dl className="tiles" aria-label="Counts">
        <div className="tile">
          <dt>Signups</dt>
          <dd>{f.signups.toLocaleString()}</dd>
        </div>
        <div className="tile">
          <dt>Confirmed</dt>
          <dd>{f.confirmations.toLocaleString()}</dd>
        </div>
      </dl>
      {f.problems && f.problems.length > 0 && (
        <div className="notice notice-warn" role="status">
          {f.problems.map((p) => (
            <p key={p.code}>
              {p.message} {p.hint && <span className="hint">{p.hint}</span>}
            </p>
          ))}
        </div>
      )}
      <div className="form-editor">
        <Options
          key={f.updated_at}
          form={f}
          tags={tags.state.data}
          justSaved={justSaved}
          edited={() => setJustSaved(false)}
          saved={(next) => {
            setJustSaved(true);
            // The answer to a change has no problems list; read it again for them.
            replace({ ...next, problems: f.problems });
            setSaves((n) => n + 1);
            reload();
          }}
        />
        <EmbedPanel formId={f.id} saves={saves} />
      </div>
      <DeleteForm form={f} />
    </section>
  );
}

function Options({
  form,
  tags,
  justSaved,
  edited,
  saved,
}: {
  form: Form;
  tags: Tag[];
  justSaved: boolean;
  edited: () => void;
  saved: (f: Form) => void;
}) {
  const api = useApi();
  const start = valuesOf(form);
  const [v, setV] = useState<Values>(start);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Failure>();
  const dirty = JSON.stringify(bodyOf(v)) !== JSON.stringify(bodyOf(start));
  const set = (patch: Partial<Values>) => {
    edited();
    setV((old) => ({ ...old, ...patch }));
  };
  const setField = (name: FieldName, patch: Partial<FieldRow>) =>
    set({ fields: v.fields.map((r) => (r.name === name ? { ...r, ...patch } : r)) });

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setFailure(undefined);
    const res = await attempt(() => api.PATCH("/v1/forms/{form_id}", { params: { path: { form_id: form.id } }, body: bodyOf(v) }));
    setBusy(false);
    if (!res.ok) return setFailure(res.failure);
    if (res.data) saved(res.data);
  }

  return (
    <form className="panel stack form-options" onSubmit={save} aria-label="Form options">
      <label className="field">
        <span>Form name</span>
        <input value={v.name} onChange={(e) => set({ name: e.target.value })} maxLength={200} required />
        <small className="muted">Only you see it.</small>
      </label>

      <fieldset className="choices">
        <legend>Fields</legend>
        {v.fields.map((r) => (
          <div key={r.name} className="form-field-row" role="group" aria-label={defaultLabels[r.name]}>
            <label className="check">
              <input
                type="checkbox"
                checked={r.name === "email" || r.on}
                disabled={r.name === "email"}
                onChange={(e) => setField(r.name, { on: e.target.checked })}
              />
              <span>{r.name === "email" ? "Email (always asked)" : `Ask for ${defaultLabels[r.name].toLowerCase()}`}</span>
            </label>
            {(r.name === "email" || r.on) && (
              <>
                <label className="field">
                  <span>Label</span>
                  <input value={r.label} placeholder={defaultLabels[r.name]} maxLength={100} onChange={(e) => setField(r.name, { label: e.target.value })} />
                </label>
                {r.name !== "email" && (
                  <label className="check">
                    <input type="checkbox" checked={r.required} onChange={(e) => setField(r.name, { required: e.target.checked })} />
                    <span>Required</span>
                  </label>
                )}
              </>
            )}
          </div>
        ))}
      </fieldset>

      <fieldset className="choices">
        <legend>Tags for everyone who signs up</legend>
        {tags.length === 0 ? (
          <p className="muted">
            No tags yet. <Link to="/tags">Make a tag</Link> to group the people who sign up here.
          </p>
        ) : (
          <div className="tag-choices flush" role="group" aria-label="Tags">
            {tags.map((t) => (
              <label key={t.id} className="choice">
                <input
                  type="checkbox"
                  checked={v.tagIds.includes(t.id)}
                  disabled={!v.tagIds.includes(t.id) && v.tagIds.length >= maxTags}
                  onChange={(e) => set({ tagIds: e.target.checked ? [...v.tagIds, t.id] : v.tagIds.filter((i) => i !== t.id) })}
                />
                {t.name}
              </label>
            ))}
          </div>
        )}
      </fieldset>

      <label className="field">
        <span>Button text</span>
        <input value={v.buttonText} onChange={(e) => set({ buttonText: e.target.value })} maxLength={50} placeholder="Subscribe" />
      </label>

      <fieldset className="choices">
        <legend>After someone signs up</legend>
        <label className="choice">
          <input type="radio" name="after" checked={v.after === "message"} onChange={() => set({ after: "message" })} />
          Show a message
        </label>
        <label className="choice">
          <input type="radio" name="after" checked={v.after === "redirect"} onChange={() => set({ after: "redirect" })} />
          Go to a page on my site
        </label>
        {v.after === "message" ? (
          <label className="field">
            <span>Message</span>
            <textarea rows={2} value={v.successMessage} onChange={(e) => set({ successMessage: e.target.value })} maxLength={500} />
          </label>
        ) : (
          <label className="field">
            <span>Page address</span>
            <input
              type="url"
              value={v.redirectUrl}
              onChange={(e) => set({ redirectUrl: e.target.value })}
              placeholder="https://example.com/thanks"
              maxLength={2000}
              required
            />
          </label>
        )}
      </fieldset>

      <fieldset className="choices">
        <legend>Confirmation</legend>
        <label className="check">
          <input type="checkbox" checked={v.doubleOptIn} onChange={(e) => set({ doubleOptIn: e.target.checked })} />
          <span>
            Ask people to confirm by email
            <small className="muted"> Recommended. Only people who click the link are subscribed, which keeps typos and bots off your list.</small>
          </span>
        </label>
        {v.doubleOptIn && (
          <>
            <label className="field">
              <span>Confirmation email subject</span>
              <input value={v.confirmSubject} onChange={(e) => set({ confirmSubject: e.target.value })} maxLength={200} required />
            </label>
            <label className="field">
              <span>Confirmation email text</span>
              <textarea rows={4} value={v.confirmText} onChange={(e) => set({ confirmText: e.target.value })} maxLength={2000} required />
              <small className="muted">The confirm link goes under it.</small>
            </label>
          </>
        )}
      </fieldset>

      <FailureNotice failure={failure} />
      <div className="row">
        <button type="submit" className="button primary" disabled={busy || !dirty}>
          {busy ? "Saving…" : "Save changes"}
        </button>
        {justSaved && !dirty ? (
          <span className="muted small" role="status">
            Saved. Code already on your site picks up new fields only once you replace it.
          </span>
        ) : (
          dirty && <span className="muted small">Unsaved changes</span>
        )}
      </div>
    </form>
  );
}

/** The saved form as it looks on a site, and its code to copy. */
function EmbedPanel({ formId, saves }: { formId: string; saves: number }) {
  const api = useApi();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/forms/{form_id}/embed", { params: { path: { form_id: formId } } })),
    // saves: a saved change shows on the next look.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [api, formId, saves],
  );
  const { state, reload } = useLoad<Embed>(load);
  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") return <LoadFailed failure={state.failure} retry={reload} />;
  const e = state.data;
  return (
    <div className="stack embed-panel">
      <section className="panel stack" aria-label="Preview">
        <h2>Preview</h2>
        <p className="muted small">The saved form. On your site it takes your page's fonts. Nothing is sent from here.</p>
        <iframe title="Form preview" sandbox="" srcDoc={e.html} className="form-preview" />
      </section>
      <section className="panel stack" aria-label="Embed code">
        <h2>Put it on your site</h2>
        <p className="muted small">Paste this where the form should go. It works without JavaScript.</p>
        <div className="code-copy">
          <textarea readOnly rows={6} value={e.html} aria-label="Form HTML" className="code" />
          <CopyButton value={e.html} label="form HTML" />
        </div>
        <p className="muted small">
          Optional: paste this after it to show the message in the page instead of on a new one.
        </p>
        <div className="code-copy">
          <textarea readOnly rows={3} value={e.script} aria-label="Script" className="code" />
          <CopyButton value={e.script} label="script" />
        </div>
      </section>
    </div>
  );
}

function DeleteForm({ form: f }: { form: Form }) {
  const api = useApi();
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState(false);
  const [failure, setFailure] = useState<Failure>();

  async function remove() {
    setFailure(undefined);
    const res = await attempt(() => api.DELETE("/v1/forms/{form_id}", { params: { path: { form_id: f.id } } }));
    if (!res.ok) return setFailure(res.failure);
    navigate("/forms");
  }

  return (
    <div className="panel danger-zone">
      <h2>Delete this form</h2>
      <p className="muted">Its code stops taking signups. Contacts who signed up with it stay.</p>
      {confirm ? (
        <div className="row">
          <button type="button" className="button danger" onClick={() => void remove()}>
            Delete {f.name}
          </button>
          <button type="button" className="button" onClick={() => setConfirm(false)}>
            Cancel
          </button>
        </div>
      ) : (
        <div>
          <button type="button" className="button" onClick={() => setConfirm(true)}>
            Delete form
          </button>
        </div>
      )}
      <FailureNotice failure={failure} />
    </div>
  );
}
