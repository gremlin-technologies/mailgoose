// The designer's properties panel: the selected block's fields, or the
// email's own settings when no block is selected. Every edit is reported with
// set(), and the designer saves it a moment later (autosave).
import { useCallback, useRef, useState, type FormEvent } from "react";

import { useApi } from "../api/context";
import { attempt, type Failure } from "../api/result";
import type { components } from "../api/schema";
import { useLoad } from "../components";
import type { Block } from "./blocks";
import { blockLabels } from "./blocks";
import { AlignField, ColorField, MergeTags, NumberField, TagField, useSelection, type Align } from "./fields";
import { insert, link, list, wrap } from "./format";

export type Settings = components["schemas"]["EmailSettings"];
type Image = components["schemas"]["Image"];
export type Fields = Record<string, unknown>;

export function EmailProperties({ name, settings, set }: { name: string; settings: Settings; set: (f: Fields) => void }) {
  return (
    <form className="stack" aria-label="Email settings" onSubmit={(e) => e.preventDefault()}>
      <h2>Email settings</h2>
      <p className="muted small">Select a block on the email to change it.</p>
      <label className="field">
        <span>Name (only you see it)</span>
        <input value={name} maxLength={200} onChange={(e) => set({ name: e.target.value })} />
      </label>
      <TagField label="Subject" value={settings.subject} maxLength={200} unsubscribe={false} onChange={(v) => set({ subject: v })} />
      <TagField
        label="Preview text, shown after the subject in inboxes"
        value={settings.preview_text}
        maxLength={200}
        unsubscribe={false}
        onChange={(v) => set({ preview_text: v })}
      />
      <label className="field">
        <span>From name</span>
        <input value={settings.from_name} maxLength={100} onChange={(e) => set({ from_name: e.target.value })} />
      </label>
      <label className="field">
        <span>From address (at one of your verified domains)</span>
        <input
          type="email"
          value={settings.from_email ?? ""}
          onChange={(e) => set({ from_email: e.target.value.trim() || null })}
          placeholder="hello@yourdomain.com"
        />
      </label>
      <label className="field">
        <span>Reply-to address (optional)</span>
        <input type="email" value={settings.reply_to ?? ""} onChange={(e) => set({ reply_to: e.target.value.trim() || null })} />
      </label>
      <ColorField label="Background color" value={settings.background_color} onChange={(v) => set({ background_color: v })} />
      <NumberField
        label="Content width on desktop (pixels)"
        value={settings.content_width}
        min={480}
        max={800}
        onChange={(v) => v !== null && set({ content_width: v })}
      />
    </form>
  );
}

export function BlockProperties({ block, set }: { block: Block; set: (f: Fields) => void }) {
  const title = `${blockLabels[block.type]} block`;
  return (
    <form className="stack" aria-label={`${title} settings`} onSubmit={(e) => e.preventDefault()}>
      <h2>{title}</h2>
      <Fieldset block={block} set={set} />
    </form>
  );
}

function Fieldset({ block, set }: { block: Block; set: (f: Fields) => void }) {
  switch (block.type) {
    case "heading":
      return (
        <>
          <TagField label="Heading" value={block.text} maxLength={500} onChange={(v) => set({ text: v })} />
          <label className="field">
            <span>Size</span>
            <select value={block.level} onChange={(e) => set({ level: Number(e.target.value) })}>
              <option value={1}>Large</option>
              <option value={2}>Medium</option>
              <option value={3}>Small</option>
            </select>
          </label>
          <AlignField value={block.align as Align} onChange={(v) => set({ align: v })} />
        </>
      );
    case "text":
      return (
        <>
          <p className="muted small">Edit the words on the email itself. Use the buttons above them for bold, italic, links and lists.</p>
          <AlignField value={block.align as Align} onChange={(v) => set({ align: v })} />
        </>
      );
    case "image":
      return (
        <>
          <ImagePicker src={block.src} choose={(src) => set({ src })} />
          <label className="field">
            <span>Image address</span>
            <input type="url" value={block.src} maxLength={2000} onChange={(e) => set({ src: e.target.value.trim() })} />
          </label>
          <label className="field">
            <span>Alt text, for people who can't see it</span>
            <input value={block.alt} maxLength={500} onChange={(e) => set({ alt: e.target.value })} />
          </label>
          <label className="field">
            <span>Link when clicked (optional)</span>
            <input value={block.href ?? ""} maxLength={2000} placeholder="https://" onChange={(e) => set({ href: e.target.value.trim() || null })} />
          </label>
          <NumberField
            label="Width (pixels)"
            value={block.width}
            min={1}
            max={800}
            nullable
            hint="Leave empty for the full width."
            onChange={(v) => set({ width: v })}
          />
          <AlignField value={block.align as Align} onChange={(v) => set({ align: v })} />
        </>
      );
    case "button":
      return (
        <>
          <TagField label="Button text" value={block.text} maxLength={100} unsubscribe={false} onChange={(v) => set({ text: v })} />
          <TagField label="Link" value={block.href} maxLength={2000} placeholder="https://" onChange={(v) => set({ href: v.trim() })} />
          <ColorField label="Button color" value={block.background_color} onChange={(v) => set({ background_color: v })} />
          <ColorField label="Text color" value={block.text_color} onChange={(v) => set({ text_color: v })} />
          <AlignField value={block.align as Align} onChange={(v) => set({ align: v })} />
        </>
      );
    case "divider":
      return (
        <>
          <ColorField label="Line color" value={block.color} onChange={(v) => set({ color: v })} />
          <NumberField label="Thickness (pixels)" value={block.thickness} min={1} max={10} onChange={(v) => v !== null && set({ thickness: v })} />
        </>
      );
    case "spacer":
      return <NumberField label="Height (pixels)" value={block.height} min={4} max={200} onChange={(v) => v !== null && set({ height: v })} />;
    case "social":
      return <SocialLinks links={block.links} align={block.align as Align} set={set} />;
    case "footer":
      return (
        <>
          <TagField
            label="A line above the address (optional)"
            value={block.text}
            maxLength={1000}
            multiline
            onChange={(v) => set({ text: v })}
          />
          <p className="muted small">Your postal address and the unsubscribe link are added below it when the email is sent. The footer stays last.</p>
        </>
      );
    case "columns":
      return <p className="muted small">Two columns hold other blocks. Select a block inside them to change it.</p>;
    default:
      return null;
  }
}

const networks = ["website", "facebook", "instagram", "x", "linkedin", "youtube", "tiktok", "threads", "bluesky"] as const;
type Network = (typeof networks)[number];
type SocialLink = { network: Network; url: string };

function SocialLinks({ links, align, set }: { links: SocialLink[]; align: Align; set: (f: Fields) => void }) {
  const change = (i: number, l: Partial<SocialLink>) => set({ links: links.map((x, j) => (j === i ? { ...x, ...l } : x)) });
  return (
    <>
      <ul className="stack plain">
        {links.map((l, i) => (
          <li key={i} className="row social-row" role="group" aria-label={`Link ${i + 1}`}>
            <label className="field">
              <span>Network</span>
              <select value={l.network} onChange={(e) => change(i, { network: e.target.value as Network })}>
                {networks.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </label>
            <label className="field grow">
              <span>Address</span>
              <input type="url" value={l.url} maxLength={2000} onChange={(e) => change(i, { url: e.target.value.trim() })} />
            </label>
            <button
              type="button"
              className="button small"
              disabled={links.length === 1}
              onClick={() => set({ links: links.filter((_, j) => j !== i) })}
            >
              Remove
            </button>
          </li>
        ))}
      </ul>
      {links.length < 10 && (
        <div>
          <button type="button" className="button small" onClick={() => set({ links: [...links, { network: "website", url: "https://" }] })}>
            Add a link
          </button>
        </div>
      )}
      <AlignField value={align} onChange={(v) => set({ align: v })} />
    </>
  );
}

const maxImageBytes = 5 * 1024 * 1024;

/** Uploads an image, or picks one uploaded before. */
export function ImagePicker({ src, choose }: { src: string; choose: (src: string) => void }) {
  const api = useApi();
  const [failure, setFailure] = useState<Failure>();
  const [uploading, setUploading] = useState(false);
  const [browsing, setBrowsing] = useState(false);

  async function upload(file?: File) {
    setFailure(undefined);
    if (!file) return;
    if (file.size > maxImageBytes) {
      setFailure({ code: "invalid_image", message: "The image is over 5 MB.", hint: "Resize or compress it, then try again.", status: 0 });
      return;
    }
    setUploading(true);
    const res = await attempt(async () => api.POST("/v1/images", { body: { data: await base64(file), file_name: file.name } }));
    setUploading(false);
    if (!res.ok) return setFailure(res.failure);
    if (res.data) choose(res.data.url);
  }

  return (
    <div className="stack image-picker">
      {src && <img className="image-current" src={src} alt="The image now" />}
      <label className="field">
        <span>{uploading ? "Uploading…" : "Upload a new picture (JPEG, PNG, GIF or WebP, up to 5 MB)"}</span>
        <input
          type="file"
          accept="image/jpeg,image/png,image/gif,image/webp"
          disabled={uploading}
          onChange={(e) => {
            void upload(e.target.files?.[0]);
            e.target.value = "";
          }}
        />
      </label>
      {failure && (
        <p className="field-error" role="alert">
          {failure.message} {failure.hint}
        </p>
      )}
      {browsing ? (
        <ImageLibrary
          choose={(url) => {
            choose(url);
            setBrowsing(false);
          }}
          close={() => setBrowsing(false)}
        />
      ) : (
        <div>
          <button type="button" className="button small" onClick={() => setBrowsing(true)}>
            Choose from your images
          </button>
        </div>
      )}
    </div>
  );
}

function ImageLibrary({ choose, close }: { choose: (url: string) => void; close: () => void }) {
  const api = useApi();
  const [cursor, setCursor] = useState<string>();
  const load = useCallback(
    () => attempt(() => api.GET("/v1/images", { params: { query: { limit: 24, ...(cursor ? { cursor } : {}) } } })),
    [api, cursor],
  );
  const { state, reload } = useLoad(load);
  return (
    <div className="panel stack" role="group" aria-label="Your images">
      {state.status === "loading" && <p className="muted small">Loading your images…</p>}
      {state.status === "failed" && (
        <p className="field-error">
          {state.failure.message}{" "}
          <button type="button" className="link" onClick={reload}>
            Try again
          </button>
        </p>
      )}
      {state.status === "ok" && state.data.data.length === 0 && <p className="muted small">No images yet. Upload one above.</p>}
      {state.status === "ok" && (
        <ul className="image-grid plain">
          {state.data.data.map((img: Image) => (
            <li key={img.id}>
              <button type="button" className="image-choice" aria-label={`Use ${img.file_name}`} onClick={() => choose(img.url)}>
                <img src={img.url} alt="" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="row">
        {state.status === "ok" && state.data.next_cursor && (
          <button type="button" className="button small" onClick={() => setCursor(state.data.next_cursor ?? undefined)}>
            More
          </button>
        )}
        <button type="button" className="button small" onClick={close}>
          Close
        </button>
      </div>
    </div>
  );
}

/** A file's bytes in standard base64, as the upload API takes them. */
export async function base64(file: File): Promise<string> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

/**
 * A text block edited where it sits on the email: its Markdown, with buttons
 * that write bold, italic, links and lists, and a merge tag menu.
 */
export function TextEditor({ text, onChange }: { text: string; onChange: (v: string) => void }) {
  const ref = useRef<HTMLTextAreaElement>(null);
  const select = useSelection(ref);
  const [linking, setLinking] = useState(false);
  const [url, setUrl] = useState("https://");

  function apply(f: (t: string, s: number, e: number) => { text: string; start: number; end: number }) {
    const el = ref.current;
    const start = el?.selectionStart ?? text.length;
    const end = el?.selectionEnd ?? text.length;
    const e = f(text, start, end);
    select(e);
    onChange(e.text);
  }

  function addLink(e: FormEvent) {
    e.preventDefault();
    const u = url.trim();
    if (!u || u === "https://") return;
    apply((t, s, en) => link(t, s, en, u));
    setLinking(false);
    setUrl("https://");
  }

  return (
    <div className="text-editor" onClick={(e) => e.stopPropagation()}>
      <div className="row toolbar" role="toolbar" aria-label="Formatting">
        <button type="button" className="button small" onClick={() => apply((t, s, e) => wrap(t, s, e, "**", "bold text"))}>
          Bold
        </button>
        <button type="button" className="button small" onClick={() => apply((t, s, e) => wrap(t, s, e, "*", "italic text"))}>
          Italic
        </button>
        <button type="button" className="button small" onClick={() => setLinking((l) => !l)} aria-expanded={linking}>
          Link
        </button>
        <button type="button" className="button small" onClick={() => apply((t, s, e) => list(t, s, e, false))}>
          Bulleted list
        </button>
        <button type="button" className="button small" onClick={() => apply((t, s, e) => list(t, s, e, true))}>
          Numbered list
        </button>
        <MergeTags onInsert={(tag) => apply((t, s, e) => insert(t, s, e, tag))} />
      </div>
      {linking && (
        <div className="row" role="group" aria-label="Add a link">
          <label className="field grow">
            <span>Link address</span>
            <input
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") addLink(e);
              }}
            />
          </label>
          <button type="button" className="button small primary" onClick={addLink}>
            Add link
          </button>
        </div>
      )}
      <label className="field">
        <span className="visually-hidden">Text</span>
        <textarea ref={ref} value={text} rows={Math.min(16, Math.max(4, text.split("\n").length + 1))} maxLength={20000} onChange={(e) => onChange(e.target.value)} />
      </label>
      <small className="muted">**bold**, *italic*, [words](https://link), lines starting "- " or "1. " for lists, a blank line between paragraphs.</small>
    </div>
  );
}
