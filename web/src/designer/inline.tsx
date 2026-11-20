// Editing on the email itself. A heading or a button's words are plain text
// typed where they sit; a text block is rich text, with a small toolbar for
// bold, italic, links, lists and merge tags, saved as the block's Markdown.
//
// These elements are edited by the browser, not by React: the content is set
// once, and again only when the value changes from outside (a reload, or the
// properties panel), so the caret never jumps while typing.
import { Bold, Italic, Link as LinkIcon, List, ListOrdered } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type FormEvent, type ReactNode } from "react";

import { MergeTags } from "./fields";
import { htmlToMd, mdToHtml, okLink } from "./richtext";

/** Puts the caret in el with everything selected, so typing replaces a starter text. */
function selectAll(el: HTMLElement) {
  el.focus();
  const sel = window.getSelection?.();
  if (!sel) return;
  sel.removeAllRanges();
  const range = document.createRange();
  range.selectNodeContents(el);
  sel.addRange(range);
}

/** document.execCommand, where the browser has it (jsdom doesn't). */
function exec(command: string, value?: string) {
  if (typeof document.execCommand !== "function") return false;
  return document.execCommand(command, false, value);
}

/** A toolbar button that leaves the selection in the text while it's pressed. */
function Tool({ label, icon, run, expanded }: { label: string; icon: ReactNode; run: () => void; expanded?: boolean }) {
  return (
    <button type="button" className="tool" aria-label={label} title={label} aria-expanded={expanded} onMouseDown={(e) => e.preventDefault()} onClick={run}>
      {icon}
    </button>
  );
}

/** One line of plain text, typed in place. */
export function InlineText({
  as: Tag = "span",
  value,
  onChange,
  onFocus,
  label,
  ariaLabel,
  placeholder,
  className,
  style,
  autoFocus = false,
}: {
  as?: "h1" | "h2" | "h3" | "span";
  value: string;
  onChange: (v: string) => void;
  onFocus?: () => void;
  /** An accessible name, for text that isn't its own name (a heading is). */
  label?: string;
  /** A name added to the element as it is, keeping its role (a heading stays a heading). */
  ariaLabel?: string;
  placeholder?: string;
  className?: string;
  style?: CSSProperties;
  autoFocus?: boolean;
}) {
  const ref = useRef<HTMLElement>(null);
  const shown = useRef<string>(undefined);
  const filled = useRef<HTMLElement>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    // A new element (a heading's size changed, so h1 became h3) starts empty and needs the words.
    if (!el || (filled.current === el && shown.current === value)) return;
    el.textContent = value;
    shown.current = value;
    filled.current = el;
  }, [value, Tag]);

  useEffect(() => {
    // A block just added: its starter words are selected, ready to type over.
    if (autoFocus && ref.current) selectAll(ref.current);
  }, [autoFocus]);

  return (
    <Tag
      ref={ref as never}
      className={`inline-edit${className ? ` ${className}` : ""}`}
      style={style}
      contentEditable
      suppressContentEditableWarning
      spellCheck
      {...(label ? { role: "textbox", "aria-label": label } : ariaLabel ? { "aria-label": ariaLabel } : {})}
      data-placeholder={placeholder}
      onFocus={onFocus}
      onInput={(e) => {
        const el = e.currentTarget;
        const text = (el.textContent ?? "").replace(/[\n\r]+/g, " ");
        // An emptied element can keep a stray <br>, which hides the placeholder.
        if (text === "" && el.childNodes.length) el.textContent = "";
        shown.current = text;
        onChange(text);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          e.currentTarget.blur();
        }
      }}
      onPaste={(e) => {
        e.preventDefault();
        exec("insertText", e.clipboardData.getData("text/plain").replace(/\s*[\n\r]+\s*/g, " "));
      }}
      onDrop={(e) => e.preventDefault()}
    />
  );
}

/** A text block's words, edited as they look. `active` shows the formatting toolbar. */
export function RichText({
  value,
  onChange,
  onFocus,
  active,
  style,
  autoFocus = false,
}: {
  value: string;
  onChange: (v: string) => void;
  onFocus?: () => void;
  active: boolean;
  style?: CSSProperties;
  autoFocus?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const shown = useRef<string>(undefined);
  const range = useRef<Range>(undefined);
  const [linking, setLinking] = useState(false);
  const [url, setUrl] = useState("https://");
  const [linkError, setLinkError] = useState("");
  const [empty, setEmpty] = useState(value.trim() === "");

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || shown.current === value) return;
    el.innerHTML = mdToHtml(value);
    shown.current = value;
  }, [value]);

  useEffect(() => {
    // A block just added: its starter words are selected, ready to type over.
    if (autoFocus && ref.current) selectAll(ref.current);
  }, [autoFocus]);

  /** Remembers the selection, so a toolbar form that takes focus can come back to it. */
  function remember() {
    const el = ref.current;
    const sel = window.getSelection?.();
    if (!el || !sel || sel.rangeCount === 0) return;
    const r = sel.getRangeAt(0);
    if (el.contains(r.commonAncestorContainer)) range.current = r.cloneRange();
  }

  /** Puts the caret back in the text, where it last was. */
  function restore() {
    const el = ref.current;
    if (!el) return;
    el.focus();
    const sel = window.getSelection?.();
    if (!sel || !range.current) return;
    sel.removeAllRanges();
    sel.addRange(range.current);
  }

  function read() {
    const el = ref.current;
    if (!el) return;
    const md = htmlToMd(el);
    shown.current = md;
    setEmpty(md === "");
    onChange(md);
  }

  function command(name: string, arg?: string) {
    restore();
    exec("styleWithCSS", "false");
    exec("defaultParagraphSeparator", "p");
    exec(name, arg);
    remember();
    read();
  }

  function addLink(e: FormEvent) {
    e.preventDefault();
    const u = url.trim();
    if (!u || u === "https://") return;
    // A link that okLink() refuses would show here and then be dropped on save.
    if (!okLink(u)) {
      setLinkError("Start the address with https://, http:// or mailto:");
      return;
    }
    restore();
    const sel = window.getSelection?.();
    if (sel && sel.isCollapsed) {
      // Nothing selected: the address itself becomes the link's words.
      exec("insertText", u);
      extendBack(u.length);
    }
    exec("createLink", u);
    remember();
    read();
    setLinking(false);
    setUrl("https://");
    setLinkError("");
  }

  /** Selects the n characters before the caret (the address just typed in). */
  function extendBack(n: number) {
    const sel = window.getSelection?.();
    if (!sel || typeof sel.modify !== "function") return;
    for (let i = 0; i < n; i++) sel.modify("extend", "backward", "character");
  }

  return (
    <>
      {active && (
        <div className="format-bar" role="toolbar" aria-label="Formatting" onClick={(e) => e.stopPropagation()}>
          <Tool label="Bold" icon={<Bold size={15} aria-hidden="true" />} run={() => command("bold")} />
          <Tool label="Italic" icon={<Italic size={15} aria-hidden="true" />} run={() => command("italic")} />
          <Tool
            label="Link"
            icon={<LinkIcon size={15} aria-hidden="true" />}
            expanded={linking}
            run={() => {
              remember();
              setLinkError("");
              setLinking((l) => !l);
            }}
          />
          <Tool label="Bulleted list" icon={<List size={15} aria-hidden="true" />} run={() => command("insertUnorderedList")} />
          <Tool label="Numbered list" icon={<ListOrdered size={15} aria-hidden="true" />} run={() => command("insertOrderedList")} />
          <span className="format-merge" onMouseDown={remember}>
            <MergeTags onInsert={(tag) => command("insertText", tag)} />
          </span>
          {linking && (
            <form className="format-pop" role="group" aria-label="Add a link" onSubmit={addLink}>
              <label className="field grow">
                <span>Link address</span>
                <input
                  value={url}
                  onChange={(e) => {
                    setUrl(e.target.value);
                    setLinkError("");
                  }}
                  aria-invalid={linkError ? true : undefined}
                  autoFocus
                />
              </label>
              <button type="submit" className="button small primary">
                Add link
              </button>
              {linkError && (
                <p className="field-error" role="alert">
                  {linkError}
                </p>
              )}
            </form>
          )}
        </div>
      )}
      <div
        ref={ref}
        className="canvas-text inline-edit"
        style={style}
        role="textbox"
        aria-label="Text"
        aria-multiline="true"
        contentEditable
        suppressContentEditableWarning
        spellCheck
        data-empty={empty}
        data-placeholder="Start typing…"
        onFocus={() => {
          exec("defaultParagraphSeparator", "p");
          onFocus?.();
        }}
        onBlur={remember}
        onKeyUp={remember}
        onMouseUp={remember}
        onInput={read}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
            e.preventDefault();
            remember();
            setLinking(true);
          }
        }}
        onPaste={(e) => {
          e.preventDefault();
          exec("insertText", e.clipboardData.getData("text/plain"));
        }}
        onDrop={(e) => e.preventDefault()}
      />
    </>
  );
}
