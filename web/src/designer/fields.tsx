// Form fields the designer's properties panel is built from.
import { useLayoutEffect, useRef, useState, type RefObject } from "react";

import { fallbackProblem, insert, maxFallback, mergeTag, mergeTags, type Edit, type MergeTag } from "./format";

type TextControl = HTMLInputElement | HTMLTextAreaElement;

/**
 * Keeps a text control's selection where an edit put it. React moves the
 * caret to the end when it sets a controlled value, so the edit's selection
 * is put back right after the render.
 */
export function useSelection<T extends TextControl>(ref: RefObject<T | null>) {
  const next = useRef<Edit>(undefined);
  useLayoutEffect(() => {
    const el = ref.current;
    const e = next.current;
    if (!el || !e) return;
    next.current = undefined;
    el.focus();
    el.setSelectionRange(e.start, e.end);
  });
  return (e: Edit) => {
    next.current = e;
  };
}

/** A menu that writes a merge tag, with an optional fallback for an empty value. */
export function MergeTags({ onInsert, unsubscribe = true }: { onInsert: (tag: string) => void; unsubscribe?: boolean }) {
  const [open, setOpen] = useState(false);
  const [tag, setTag] = useState<MergeTag>("first_name");
  const [fallback, setFallback] = useState("");
  const problem = tag === "unsubscribe_url" ? undefined : fallbackProblem(fallback);
  const tags = mergeTags.filter((t) => unsubscribe || t.tag !== "unsubscribe_url");
  if (!open) {
    return (
      <button type="button" className="button small" onClick={() => setOpen(true)}>
        Merge tag
      </button>
    );
  }
  return (
    <div className="merge-tags panel" role="group" aria-label="Merge tag">
      <label className="field">
        <span>Fill in</span>
        <select value={tag} onChange={(e) => setTag(e.target.value as MergeTag)}>
          {tags.map((t) => (
            <option key={t.tag} value={t.tag}>
              {t.label}
            </option>
          ))}
        </select>
      </label>
      {tag !== "unsubscribe_url" && (
        <label className="field">
          <span>When it's empty, use (optional)</span>
          <input value={fallback} onChange={(e) => setFallback(e.target.value)} maxLength={maxFallback + 10} placeholder="there" />
        </label>
      )}
      {problem && <p className="field-error">{problem}</p>}
      <div className="row">
        <button
          type="button"
          className="button small primary"
          disabled={!!problem}
          onClick={() => {
            onInsert(mergeTag(tag, fallback));
            setOpen(false);
            setFallback("");
          }}
        >
          Insert
        </button>
        <button type="button" className="button small" onClick={() => setOpen(false)}>
          Cancel
        </button>
      </div>
    </div>
  );
}

/** A text field that merge tags can be put into, at the caret. */
export function TagField({
  label,
  value,
  onChange,
  maxLength,
  placeholder,
  multiline = false,
  unsubscribe = true,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  maxLength?: number;
  placeholder?: string;
  multiline?: boolean;
  unsubscribe?: boolean;
}) {
  const ref = useRef<TextControl>(null);
  const select = useSelection(ref);
  function put(tag: string) {
    const el = ref.current;
    const start = el?.selectionStart ?? value.length;
    const end = el?.selectionEnd ?? value.length;
    const e = insert(value, start, end, tag);
    select(e);
    onChange(e.text);
  }
  const props = {
    value,
    maxLength,
    placeholder,
    onChange: (e: { target: { value: string } }) => onChange(e.target.value),
  };
  return (
    <div className="field tag-field">
      <label className="field">
        <span>{label}</span>
        {multiline ? (
          <textarea ref={ref as RefObject<HTMLTextAreaElement>} rows={3} {...props} />
        ) : (
          <input ref={ref as RefObject<HTMLInputElement>} {...props} />
        )}
      </label>
      <MergeTags onInsert={put} unsubscribe={unsubscribe} />
    </div>
  );
}

/**
 * A whole number between min and max. It reports only numbers in range (or
 * null when nullable and empty), so a half-typed value is never saved.
 */
export function NumberField({
  label,
  value,
  min,
  max,
  nullable = false,
  hint,
  onChange,
}: {
  label: string;
  value: number | null;
  min: number;
  max: number;
  nullable?: boolean;
  hint?: string;
  onChange: (v: number | null) => void;
}) {
  const [text, setText] = useState(value === null ? "" : String(value));
  const [seen, setSeen] = useState(value);
  if (seen !== value) {
    // The value changed from outside (a reload); show it.
    setSeen(value);
    setText(value === null ? "" : String(value));
  }
  const n = Number(text);
  const ok = (text.trim() === "" && nullable) || (/^\d+$/.test(text.trim()) && n >= min && n <= max);
  return (
    <label className="field">
      <span>{label}</span>
      <input
        inputMode="numeric"
        value={text}
        aria-invalid={!ok}
        onChange={(e) => {
          const t = e.target.value;
          setText(t);
          const v = Number(t);
          if (t.trim() === "" && nullable) {
            setSeen(null);
            onChange(null);
          } else if (/^\d+$/.test(t.trim()) && v >= min && v <= max) {
            setSeen(v);
            onChange(v);
          }
        }}
      />
      {ok ? hint && <small className="muted">{hint}</small> : <small className="field-error">A whole number from {min} to {max}{nullable ? ", or empty" : ""}.</small>}
    </label>
  );
}

export function ColorField({ label, value, onChange }: { label: string; value: string; onChange: (v: string) => void }) {
  return (
    <label className="field color-field">
      <span>{label}</span>
      <input type="color" value={value} onChange={(e) => onChange(e.target.value)} />
    </label>
  );
}

const aligns = ["left", "center", "right"] as const;
export type Align = (typeof aligns)[number];

export function AlignField({ value, onChange }: { value: Align; onChange: (v: Align) => void }) {
  return (
    <fieldset className="field align-field">
      <legend>Alignment</legend>
      <div className="row">
        {aligns.map((a) => (
          <label key={a} className="choice">
            <input type="radio" name="align" checked={value === a} onChange={() => onChange(a)} />
            {a[0].toUpperCase() + a.slice(1)}
          </label>
        ))}
      </div>
    </fieldset>
  );
}
