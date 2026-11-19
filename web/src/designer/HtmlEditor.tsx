import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import { Link } from "react-router";

import { useApi } from "../api/context";
import { attempt } from "../api/result";
import type { components } from "../api/schema";
import { FailureNotice, Loading, PageHeader } from "../components";
import { EMAIL, saveSettings, saveText, useAutosave } from "./autosave";
import type { Marked } from "./CodeEditor";
import { byLine, fillSample, issuesOf, maxFileBytes, type HtmlIssue } from "./html";
import type { Width } from "./Preview";
import { EmailProperties, type Settings } from "./properties";

type Email = components["schemas"]["Email"];
type HtmlDocument = components["schemas"]["HtmlDocument"];

const CodeEditor = lazy(() => import("./CodeEditor"));

/** The autosave key for the markup. */
const HTML = "html";
/** How long the live preview waits after the last keystroke. */
export const previewDelay = 300;

/** A small email to start from: it has the unsubscribe link every email needs. */
export const starter = `<!doctype html>
<html>
  <body style="margin:0;padding:24px;background:#f4f4f5;font-family:Arial,sans-serif;">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0">
      <tr>
        <td align="center">
          <table role="presentation" width="600" cellpadding="24" cellspacing="0" style="background:#ffffff;">
            <tr>
              <td>
                <h1 style="margin:0 0 16px;">Hi {{first_name|there}}</h1>
                <p style="margin:0 0 16px;">Write your email here.</p>
                <p style="margin:0;font-size:12px;color:#71717a;">
                  <a href="{{unsubscribe_url}}">Unsubscribe</a>
                </p>
              </td>
            </tr>
          </table>
        </td>
      </tr>
    </table>
  </body>
</html>
`;

/**
 * An HTML email's editor: code on one side, a live preview on the other.
 * Code is the only way to change it. Each pause in typing saves the markup,
 * and the API's checks come back as problems marked on their lines.
 */
export function HtmlEditor({ email, replace, reload }: { email: Email; replace: (e: Email) => void; reload: () => void }) {
  const api = useApi();
  const [errors, setErrors] = useState<HtmlIssue[]>([]);
  const [warnings, setWarnings] = useState<HtmlIssue[]>([]);
  const [width, setWidth] = useState<Width>("desktop");
  const [fileProblem, setFileProblem] = useState<string>();

  const { failure, drafts, save, edit } = useAutosave({
    email,
    replace,
    reload,
    send: async (key, fields, v) => {
      if (key === EMAIL) return saveSettings(api, email.id, fields, v);
      const res = await attempt(() =>
        api.PUT("/v1/emails/{email_id}/html", {
          params: { path: { email_id: email.id } },
          body: { version: v, html: fields.html as string },
        }),
      );
      if (!res.ok) return res;
      setErrors([]);
      setWarnings(res.data?.warnings ?? []);
      return { ok: true, data: res.data?.email };
    },
    handled: (key, f) => {
      if (key !== HTML || f.code !== "invalid_html") return false;
      setErrors(issuesOf(f));
      return true;
    },
  });

  const doc = email.document as HtmlDocument;
  const code = (drafts[HTML]?.html as string | undefined) ?? doc.html;
  const settings = { ...doc.settings, ...drafts[EMAIL] } as Settings;
  const name = (drafts[EMAIL]?.name as string | undefined) ?? email.name;

  // The preview follows the code a moment behind, so typing stays quick.
  const [previewed, setPreviewed] = useState(code);
  useEffect(() => {
    const t = setTimeout(() => setPreviewed(code), previewDelay);
    return () => clearTimeout(t);
  }, [code]);
  const preview = useMemo(() => fillSample(previewed), [previewed]);

  const marked: Marked[] = useMemo(
    () => [...errors.map((i) => ({ ...i, severity: "error" as const })), ...warnings.map((i) => ({ ...i, severity: "warning" as const }))],
    [errors, warnings],
  );
  const problems = byLine(marked) as Marked[];
  const status =
    save === "failed" && errors.length
      ? `Not saved: ${errors.length} ${errors.length === 1 ? "problem" : "problems"} to fix`
      : saveText[save];

  async function upload(file?: File) {
    setFileProblem(undefined);
    if (!file) return;
    if (file.size > maxFileBytes) {
      setFileProblem("That file is too big. An email's HTML can be up to 102 KB.");
      return;
    }
    edit(HTML, { html: await file.text() });
  }

  return (
    <section className="page html-editor">
      <PageHeader title={name || email.name} about="Edit the HTML on the left; the preview on the right follows as you type. Changes save as you go.">
        <span className={`save-state save-${save}`} role="status">
          {status}
        </span>
        <Link to="/emails" className="button small">
          All emails
        </Link>
      </PageHeader>
      <FailureNotice failure={failure} />
      <div className="html-body">
        <div className="stack">
          <div className="row html-tools">
            <label className="field">
              <span>Upload an HTML file</span>
              <input
                type="file"
                accept=".html,.htm,text/html"
                onChange={(e) => {
                  void upload(e.target.files?.[0]);
                  e.target.value = "";
                }}
              />
            </label>
            {code.trim() === "" && (
              <button type="button" className="button small" onClick={() => edit(HTML, { html: starter })}>
                Start from a simple template
              </button>
            )}
          </div>
          {fileProblem && (
            <p className="field-error" role="alert">
              {fileProblem}
            </p>
          )}
          <p className="muted small">Paste your HTML, or type it. It needs an unsubscribe link: {"{{unsubscribe_url}}"}.</p>
          <Suspense fallback={<Loading />}>
            <CodeEditor value={code} onChange={(html) => edit(HTML, { html })} issues={marked} label="HTML code" />
          </Suspense>
          {problems.length > 0 && (
            <section className="panel stack problems" aria-label="Problems">
              <h2 className="small">{errors.length ? "Fix these to save" : "Some email apps will ignore this"}</h2>
              <ul className="plain stack">
                {problems.map((p, i) => (
                  <li key={i} className={p.severity === "error" ? "problem-error" : "problem-warning"}>
                    <strong>Line {p.line}:</strong> {p.message}
                    {p.severity === "warning" && <span className="muted"> (warning; it still sends)</span>}
                  </li>
                ))}
              </ul>
            </section>
          )}
        </div>
        <div className="stack">
          <div className="row segmented" role="group" aria-label="Preview width">
            {(
              [
                ["desktop", "Desktop"],
                ["mobile", "Mobile"],
              ] as const
            ).map(([w, label]) => (
              <button key={w} type="button" className="button small" aria-pressed={width === w} onClick={() => setWidth(w)}>
                {label}
              </button>
            ))}
          </div>
          <p className="muted small">Filled in with sample details: Alex Rivera, alex@example.com. The footer with your postal address is added when it's sent, if the email doesn't show it.</p>
          <iframe title="Live preview" sandbox="" srcDoc={preview} className={`preview-frame live-${width}`} />
        </div>
      </div>
      <div className="panel html-settings">
        <EmailProperties name={name} settings={settings} set={(f) => edit(EMAIL, f)} html />
      </div>
    </section>
  );
}
