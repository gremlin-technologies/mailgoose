// The HTML code editor, built on CodeMirror. It's loaded only when an HTML
// email is opened, so the rest of the app stays small.
import { html } from "@codemirror/lang-html";
import { lintGutter, setDiagnostics, type Diagnostic } from "@codemirror/lint";
import { EditorView } from "@codemirror/view";
import { basicSetup } from "codemirror";
import { useEffect, useRef } from "react";

import type { HtmlIssue } from "./html";

export type Marked = HtmlIssue & { severity: "error" | "warning" };

/** Puts each issue on its line, as a squiggle with its message. */
function diagnostics(view: EditorView, issues: Marked[]): Diagnostic[] {
  const lines = view.state.doc.lines;
  return issues.map((i) => {
    const line = view.state.doc.line(Math.min(Math.max(i.line, 1), lines));
    return { from: line.from, to: line.to, severity: i.severity, message: i.message, source: i.code };
  });
}

export default function CodeEditor({
  value,
  onChange,
  issues,
  label,
}: {
  value: string;
  onChange: (v: string) => void;
  issues: Marked[];
  label: string;
}) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView>(undefined);
  const changed = useRef(onChange);

  useEffect(() => {
    changed.current = onChange;
  });

  useEffect(() => {
    const v = new EditorView({
      parent: host.current!,
      doc: value,
      extensions: [
        basicSetup,
        html(),
        lintGutter(),
        EditorView.lineWrapping,
        EditorView.contentAttributes.of({ "aria-label": label }),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) changed.current(u.state.doc.toString());
        }),
      ],
    });
    view.current = v;
    return () => v.destroy();
    // The editor is made once; value and issues are synced below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // A change from outside (an upload, or a reload after someone else saved).
  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
    }
  }, [value]);

  useEffect(() => {
    const v = view.current;
    if (v) v.dispatch(setDiagnostics(v.state, diagnostics(v, issues)));
  }, [issues, value]);

  return <div ref={host} className="code-editor" data-testid="code-editor" />;
}
