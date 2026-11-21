import {
  ArrowDown,
  ArrowLeft,
  ArrowUp,
  Columns2,
  Copy,
  GripVertical,
  Heading as HeadingIcon,
  Image as ImageIcon,
  Lock,
  Minus,
  Monitor,
  PencilRuler,
  RectangleHorizontal,
  Share2,
  Smartphone,
  Trash2,
  Type,
  UnfoldVertical,
  type LucideIcon,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState, type DragEvent, type FormEvent, type MouseEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure, type Result } from "../api/result";
import type { components } from "../api/schema";
import { FailureNotice, LoadFailed, Loading, useLoad } from "../components";
import {
  blockLabels,
  canDrop,
  columnLabel,
  columnLayouts,
  copyOf,
  findBlock,
  layoutGrid,
  layoutLabel,
  locate,
  needsDetails,
  paletteTypes,
  placeOf,
  starterBlock,
  zoneDown,
  zoneUp,
  type Block,
  type BlockDocument,
  type BlockType,
  type ColumnLayout,
  type Drag,
  type NewBlock,
  type PaletteType,
  type Zone,
} from "./blocks";
import { Preview, type Width } from "./Preview";
import { EMAIL, saveSettings, saveText, useAutosave } from "./autosave";
import { backLabel, SaveAndExit, useReturnTo } from "./exit";
import { HtmlEditor } from "./HtmlEditor";
import { InlineText, RichText } from "./inline";
import { LayoutGlyph } from "./layout";
import { base64, BlockProperties, EmailProperties, type Settings } from "./properties";
import { networkNames, networks, SocialCanvas, type Network } from "./social";

type Email = components["schemas"]["Email"];

type Mode = "design" | Width;

const blockIcons: Record<BlockType, LucideIcon> = {
  heading: HeadingIcon,
  text: Type,
  image: ImageIcon,
  button: RectangleHorizontal,
  divider: Minus,
  spacer: UnfoldVertical,
  columns: Columns2,
  social: Share2,
  footer: Lock,
};

/**
 * The email designer: a block palette, a canvas you drag blocks onto and
 * type on, and a properties panel for the selected block or the email's
 * settings. Edits save themselves a moment after the last one.
 */
export function Designer() {
  const { id = "" } = useParams();
  const api = useApi();
  const load = useCallback(() => attempt(() => api.GET("/v1/emails/{email_id}", { params: { path: { email_id: id } } })), [api, id]);
  const { state, reload, replace } = useLoad<Email>(load);

  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") return <LoadFailed failure={state.failure} retry={reload} />;
  if (state.data.kind === "html") return <HtmlEditor email={state.data} replace={replace} reload={reload} />;
  return <BlockDesigner email={state.data} replace={replace} reload={reload} />;
}

/** Every block id in a document, columns' contents too. */
function idsOf(doc: BlockDocument): Set<string> {
  const ids = new Set<string>();
  for (const b of doc.blocks) {
    ids.add(b.id);
    if (b.type === "columns") for (const col of b.columns) for (const inner of col.blocks) ids.add(inner.id);
  }
  return ids;
}

const zoneKey = (z: Zone) => `zone-${z.parent ?? "top"}-${z.column ?? "x"}-${z.after ?? "start"}`;

/**
 * The open drop zone nearest a point, so a drag never has to hit a thin
 * target: anywhere over the email picks the closest gap between blocks.
 */
function nearestZone(root: HTMLElement, x: number, y: number): Zone | undefined {
  let best: { zone: Zone; score: number } | undefined;
  for (const el of Array.from(root.querySelectorAll<HTMLElement>(".drop-zone.open"))) {
    const r = el.getBoundingClientRect();
    if (r.width === 0) continue;
    const dx = x < r.left ? r.left - x : x > r.right ? x - r.right : 0;
    const dy = y < r.top ? r.top - y : y > r.bottom ? y - r.bottom : 0;
    // Being outside a column counts for more than being a little further down.
    const score = dy + dx * 3;
    if (!best || score < best.score) best = { zone: JSON.parse(el.dataset.zone ?? "null") as Zone, score };
  }
  return best?.zone;
}

function BlockDesigner({ email, replace, reload }: { email: Email; replace: (e: Email) => void; reload: () => void }) {
  const api = useApi();
  const [drag, setDrag] = useState<Drag>();
  const [over, setOver] = useState<string>();
  const [pending, setPending] = useState<{ type: "image" | "button" | "social"; zone?: Zone }>();
  const [selected, setSelected] = useState<string>();
  // A block just added: it is selected, and its words are ready to type over.
  const [fresh, setFresh] = useState<string>();
  const [mode, setMode] = useState<Mode>("design");
  const before = useRef<Set<string>>(undefined);

  const path = { email_id: email.id };
  const doc = email.document as BlockDocument;

  const emailOf = async <T extends { email: Email }>(p: Promise<Result<T | undefined>>): Promise<Result<Email | undefined>> => {
    const res = await p;
    return res.ok ? { ok: true, data: res.data?.email } : res;
  };

  const back = useReturnTo();
  const { failure, setFailure, busy, drafts, save, edit, flush, run, saveNow } = useAutosave({
    email,
    replace,
    reload,
    send: (key, fields, v) =>
      key === EMAIL
        ? saveSettings(api, email.id, fields, v)
        : emailOf(
            attempt(() =>
              api.PATCH("/v1/emails/{email_id}/blocks/{block_id}", {
                params: { path: { ...path, block_id: key } },
                // The spec types fields as any object; each block type takes its own.
                body: { version: v, fields: fields as Record<string, never> },
              }),
            ),
          ),
  });

  // After an add, the one id that wasn't there before is the new block: select it.
  useEffect(() => {
    const had = before.current;
    if (!had) return;
    const added = [...idsOf(doc)].find((id) => !had.has(id));
    if (!added) return;
    before.current = undefined;
    setSelected(added);
    setFresh(added);
  }, [doc]);

  const add = async (block: NewBlock, zone?: Zone) => {
    before.current = idsOf(doc);
    const ok = await run((v) =>
      emailOf(
        attempt(() =>
          api.POST("/v1/emails/{email_id}/blocks", {
            params: { path },
            body: { version: v, block, ...(zone ? placeOf(zone) : {}) },
          }),
        ),
      ),
    );
    if (!ok) before.current = undefined;
    return ok;
  };

  const move = (blockId: string, zone: Zone) =>
    run((v) =>
      emailOf(
        attempt(() =>
          api.POST("/v1/emails/{email_id}/blocks/{block_id}/move", {
            params: { path: { ...path, block_id: blockId } },
            body: { version: v, ...placeOf(zone) },
          }),
        ),
      ),
    );

  const remove = async (blockId: string) => {
    const ok = await run((v) =>
      attempt(() =>
        api.DELETE("/v1/emails/{email_id}/blocks/{block_id}", {
          params: { path: { ...path, block_id: blockId }, query: { version: v } },
        }),
      ),
    );
    if (ok && selected === blockId) setSelected(undefined);
    return ok;
  };

  function select(blockId?: string) {
    if (blockId === selected) return;
    void flush();
    setSelected(blockId);
  }

  /** A block with its unsaved edits. */
  const view = <B extends Block>(b: B): B => (drafts[b.id] ? ({ ...b, ...drafts[b.id] } as B) : b);
  const settings = { ...doc.settings, ...drafts[EMAIL] } as Settings;
  const name = (drafts[EMAIL]?.name as string | undefined) ?? email.name;
  const current = selected ? findBlock(doc, selected) : undefined;

  /** A new block from the palette, at zone or (with none) last before the footer. */
  function addNew(type: PaletteType, zone?: Zone, layout?: ColumnLayout) {
    if (needsDetails(type)) {
      setPending({ type, zone });
      return;
    }
    void add(starterBlock(type, layout), zone);
  }

  function endDrag() {
    setDrag(undefined);
    setOver(undefined);
  }

  function drop(zone: Zone) {
    const d = drag;
    endDrag();
    if (!d || busy || !canDrop(d, zone, doc)) return;
    if (d.kind === "new") addNew(d.type, zone, d.layout);
    else void move(d.id, zone);
  }

  const zoneProps = { drag, doc, over, setOver, onDrop: drop };
  const top = doc.blocks;
  const frame = (b: Block) => ({
    block: view(b),
    doc,
    busy,
    selected: selected === b.id,
    moving: drag?.kind === "move" && drag.id === b.id,
    select: () => select(b.id),
    setDrag,
    endDrag,
    move,
    duplicate: (zone: Zone) => add(copyOf(view(b)), zone),
    remove,
  });
  const content = (b: Block) => (
    <BlockView
      block={view(b)}
      active={selected === b.id}
      fresh={fresh === b.id}
      select={() => select(b.id)}
      set={(fields) => edit(b.id, fields)}
    />
  );

  const views = [
    ["design", "Design", PencilRuler, "Design"],
    ["desktop", "Desktop preview", Monitor, "Desktop"],
    ["mobile", "Mobile preview", Smartphone, "Mobile"],
  ] as const;

  return (
    <section className="editor">
      <header className="editor-bar">
        <Link to={back} className="editor-back" aria-label={backLabel(back)} title={backLabel(back)}>
          <ArrowLeft size={18} aria-hidden="true" />
        </Link>
        <h1>{name || email.name}</h1>
        <span className={`save-state save-${save}`} role="status">
          {saveText[save]}
        </span>
        <div className="segmented" role="group" aria-label="View">
          {views.map(([m, label, Icon, short]) => (
            <button
              key={m}
              type="button"
              aria-label={label}
              aria-pressed={mode === m}
              onClick={() => {
                void flush();
                setMode(m);
              }}
            >
              <Icon size={15} aria-hidden="true" />
              {short}
            </button>
          ))}
        </div>
        <SaveAndExit saveNow={saveNow} />
      </header>
      {failure && (
        <div className="editor-notice">
          <FailureNotice failure={failure} />
        </div>
      )}
      {mode !== "design" ? (
        <div className="editor-preview">
          <Preview emailId={email.id} width={mode} version={email.version} />
        </div>
      ) : (
        <div className="editor-body">
          <aside className="palette" aria-label="Blocks">
            <h2>Blocks</h2>
            <p className="muted small">Drag one onto the email, or click to add it at the end.</p>
            <ul>
              {paletteTypes
                .filter((type) => type !== "columns")
                .map((type) => {
                  const Icon = blockIcons[type];
                  return (
                    <li key={type}>
                      <button
                        type="button"
                        className="palette-item"
                        draggable={!busy}
                        disabled={busy}
                        aria-label={`Add ${blockLabels[type].toLowerCase()}`}
                        onDragStart={(e) => startDrag(e, { kind: "new", type }, setDrag)}
                        onDragEnd={endDrag}
                        onClick={() => addNew(type)}
                      >
                        <Icon size={20} strokeWidth={1.75} aria-hidden="true" />
                        <span>{blockLabels[type]}</span>
                      </button>
                    </li>
                  );
                })}
            </ul>
            <h2 className="palette-section">Columns</h2>
            <p className="muted small">Lay blocks out side by side. Columns stack on phones.</p>
            <ul className="palette-layouts">
              {columnLayouts.map((layout) => (
                <li key={layout}>
                  <button
                    type="button"
                    className="palette-item palette-layout"
                    draggable={!busy}
                    disabled={busy}
                    onDragStart={(e) => startDrag(e, { kind: "new", type: "columns", layout }, setDrag)}
                    onDragEnd={endDrag}
                    onClick={() => addNew("columns", undefined, layout)}
                  >
                    <LayoutGlyph layout={layout} />
                    {/* Named in words, shown as a ratio. With no aria-label the two needn't match (axe). */}
                    <span className="visually-hidden">{`Add ${layoutLabel(layout)} layout`}</span>
                    <span aria-hidden="true">{layout}</span>
                  </button>
                </li>
              ))}
            </ul>
          </aside>
          <div
            className="canvas-wrap"
            style={{ background: settings.background_color }}
            onClick={() => select(undefined)}
            onKeyDown={(e) => {
              if (e.key !== "Escape") return;
              (document.activeElement as HTMLElement | null)?.blur?.();
              select(undefined);
            }}
            onDragOver={(e) => {
              if (!drag) return;
              const zone = nearestZone(e.currentTarget, e.clientX, e.clientY);
              if (!zone) return;
              e.preventDefault();
              setOver(zoneKey(zone));
            }}
            onDragLeave={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOver(undefined);
            }}
            onDrop={(e) => {
              const zone = nearestZone(e.currentTarget, e.clientX, e.clientY);
              if (!zone) return;
              e.preventDefault();
              drop(zone);
            }}
          >
            {pending && (
              <div className="details-wrap" onClick={(e) => e.stopPropagation()}>
                <Details
                  type={pending.type}
                  busy={busy}
                  cancel={() => setPending(undefined)}
                  add={async (block) => {
                    if (await add(block, pending.zone)) setPending(undefined);
                  }}
                  setFailure={setFailure}
                />
              </div>
            )}
            <div
              className={`canvas${drag ? " dragging" : ""}`}
              style={{ maxWidth: settings.content_width }}
              aria-busy={busy}
              aria-label="Email content"
              role="list"
            >
              <DropZone zone={{ parent: null, column: null, after: null }} {...zoneProps} />
              {top.map((b) => (
                <div key={b.id} role="listitem">
                  <BlockFrame {...frame(b)}>
                    {b.type === "columns" ? (
                      <div className="canvas-columns" style={{ gridTemplateColumns: layoutGrid(view(b).layout, b.columns.length) }}>
                        {b.columns.map((col, c) => (
                          <div
                            key={c}
                            className="canvas-column"
                            // A list needs items; an empty column is a group until it has some.
                            role={col.blocks.length === 0 ? "group" : "list"}
                            aria-label={columnLabel(c, b.columns.length)}
                          >
                            <DropZone zone={{ parent: b.id, column: c, after: null }} fill={col.blocks.length === 0} {...zoneProps} />
                            {(col.blocks as Block[]).map((inner) => (
                              <div key={inner.id} role="listitem">
                                <BlockFrame {...frame(inner)}>{content(inner)}</BlockFrame>
                                <DropZone zone={{ parent: b.id, column: c, after: inner.id }} {...zoneProps} />
                              </div>
                            ))}
                          </div>
                        ))}
                      </div>
                    ) : (
                      content(b)
                    )}
                  </BlockFrame>
                  {b.type !== "footer" && <DropZone zone={{ parent: null, column: null, after: b.id }} {...zoneProps} />}
                </div>
              ))}
            </div>
          </div>
          <aside className="inspector" aria-label="Properties">
            {current ? (
              <>
                <button type="button" className="inspector-back" onClick={() => select(undefined)}>
                  <ArrowLeft size={15} aria-hidden="true" />
                  Email settings
                </button>
                <BlockProperties block={view(current)} set={(f) => edit(current.id, f)} />
              </>
            ) : (
              <EmailProperties name={name} settings={settings} set={(f) => edit(EMAIL, f)} />
            )}
          </aside>
        </div>
      )}
    </section>
  );
}

function startDrag(e: DragEvent, d: Drag, setDrag: (d: Drag) => void, image?: Element | null) {
  // Firefox starts a drag only once something is set.
  e.dataTransfer?.setData("text/plain", d.kind === "new" ? d.type : d.id);
  if (e.dataTransfer) e.dataTransfer.effectAllowed = d.kind === "new" ? "copy" : "move";
  // Dragging by the handle still shows the whole block under the pointer.
  if (image && e.dataTransfer?.setDragImage) e.dataTransfer.setDragImage(image, 24, 16);
  setDrag(d);
}

/**
 * A place between blocks that a drag can drop on, drawn as a line when it's
 * the nearest one. It takes no room, so nothing moves under the pointer.
 * Mouse only: each block's buttons do the same by keyboard.
 */
function DropZone({
  zone,
  drag,
  doc,
  over,
  setOver,
  onDrop,
  fill = false,
}: {
  zone: Zone;
  drag?: Drag;
  doc: BlockDocument;
  over?: string;
  setOver: (key?: string) => void;
  onDrop: (z: Zone) => void;
  /** An empty column's zone covers the whole column. */
  fill?: boolean;
}) {
  const key = zoneKey(zone);
  const ok = drag !== undefined && canDrop(drag, zone, doc);
  return (
    <div
      className={`drop-zone${fill ? " fill" : ""}${ok ? " open" : ""}${over === key && ok ? " over" : ""}`}
      data-testid={key}
      data-zone={JSON.stringify(zone)}
      aria-hidden="true"
      onDragOver={(e) => {
        if (!ok) return;
        e.preventDefault();
        e.stopPropagation();
        setOver(key);
      }}
      onDrop={(e) => {
        e.preventDefault();
        e.stopPropagation();
        onDrop(zone);
      }}
    >
      {fill && <span className="canvas-empty">Drop blocks here</span>}
    </div>
  );
}

/**
 * A block on the canvas. Selected (or hovered), it shows a tab that names it
 * and drags it, and its buttons: move up and down, duplicate, delete. They
 * float above the block, so the email keeps its real spacing. The footer is
 * locked.
 */
function BlockFrame({
  block,
  doc,
  busy,
  selected,
  moving,
  select,
  setDrag,
  endDrag,
  move,
  duplicate,
  remove,
  children,
}: {
  block: Block;
  doc: BlockDocument;
  busy: boolean;
  selected: boolean;
  moving: boolean;
  select: () => void;
  setDrag: (d?: Drag) => void;
  endDrag: () => void;
  move: (id: string, zone: Zone) => Promise<boolean>;
  duplicate: (zone: Zone) => Promise<boolean>;
  remove: (id: string) => Promise<boolean>;
  children: ReactNode;
}) {
  const [confirming, setConfirming] = useState(false);
  const label = blockLabels[block.type];
  const pick = (e: MouseEvent) => {
    e.stopPropagation();
    select();
  };
  const cls = `canvas-block canvas-block-${block.type}${selected ? " selected" : ""}${moving ? " moving" : ""}`;
  if (block.type === "footer") {
    return (
      <div className={`${cls} locked`} role="group" aria-label="Footer block" onClick={pick}>
        <div className="block-bar">
          <button type="button" className="block-tab" aria-label="Edit" aria-pressed={selected} title="Footer, locked: it stays last" onClick={pick}>
            <Lock size={12} aria-hidden="true" />
            <span aria-hidden="true">Footer</span>
          </button>
        </div>
        {children}
      </div>
    );
  }
  const up = zoneUp(doc, block.id);
  const down = zoneDown(doc, block.id);
  // The copy lands right after the block, in its own list.
  const at = locate(doc, block.id);
  const here: Zone = { parent: at?.parent ?? null, column: at?.column ?? null, after: block.id };
  // Blocks you type on are dragged by their tab, so the mouse can select words.
  const typed = block.type === "heading" || block.type === "text" || block.type === "button";
  const act = (name: string, Icon: LucideIcon, run: () => void, disabled: boolean) => (
    <button type="button" className="tool" aria-label={name} title={name} disabled={disabled} onClick={run}>
      <Icon size={15} aria-hidden="true" />
    </button>
  );
  return (
    <div
      className={cls}
      role="group"
      aria-label={`${label} block`}
      onClick={pick}
      draggable={!busy && !typed ? true : undefined}
      onDragStart={(e) => {
        e.stopPropagation();
        startDrag(e, { kind: "move", id: block.id, type: block.type }, setDrag, e.currentTarget);
      }}
      onDragEnd={endDrag}
    >
      <div className="block-bar" onClick={(e) => e.stopPropagation()}>
        <button
          type="button"
          className="block-tab"
          aria-label="Edit"
          aria-pressed={selected}
          title="Drag to move"
          draggable={!busy}
          onClick={pick}
        >
          <GripVertical size={12} aria-hidden="true" />
          <span aria-hidden="true">{label}</span>
        </button>
        <div className="block-actions">
          {confirming ? (
            <>
              <button
                type="button"
                className="button small danger"
                disabled={busy}
                onClick={async () => {
                  if (await remove(block.id)) setConfirming(false);
                }}
              >
                {block.type === "columns" ? "Delete with its contents" : "Delete block"}
              </button>
              <button type="button" className="button small" onClick={() => setConfirming(false)}>
                Keep
              </button>
            </>
          ) : (
            <>
              {act("Move up", ArrowUp, () => up && void move(block.id, up), busy || !up)}
              {act("Move down", ArrowDown, () => down && void move(block.id, down), busy || !down)}
              {act("Duplicate", Copy, () => void duplicate(here), busy)}
              {act("Delete", Trash2, () => setConfirming(true), busy)}
            </>
          )}
        </div>
      </div>
      {children}
    </div>
  );
}

const align = (a: string | undefined) => ({ textAlign: (a ?? "left") as "left" | "center" | "right" });

/**
 * How a block looks in the email, near enough to design with. A heading, a
 * text block and a button's words are typed right here.
 */
function BlockView({
  block,
  active,
  fresh,
  select,
  set,
}: {
  block: Block;
  active: boolean;
  fresh: boolean;
  select: () => void;
  set: (fields: Record<string, unknown>) => void;
}) {
  switch (block.type) {
    case "heading":
      return (
        <InlineText
          as={`h${block.level}` as "h1" | "h2" | "h3"}
          className="canvas-heading"
          style={align(block.align)}
          value={block.text}
          placeholder="Heading"
          // An emptied heading is an empty h1-h3 (the CSS placeholder isn't part of its
          // accessible name), so it gets one until it has words again.
          ariaLabel={block.text.trim() ? undefined : "Empty heading"}
          autoFocus={fresh}
          onFocus={select}
          onChange={(text) => set({ text })}
        />
      );
    case "text":
      return (
        <RichText value={block.text} active={active} style={align(block.align)} autoFocus={fresh} onFocus={select} onChange={(text) => set({ text })} />
      );
    case "image":
      return (
        <div style={align(block.align)}>
          <img className="canvas-image" src={block.src} alt={block.alt} width={block.width ?? undefined} draggable={false} />
        </div>
      );
    case "button":
      return (
        <div style={align(block.align)}>
          <InlineText
            className="canvas-button"
            style={{ background: block.background_color, color: block.text_color }}
            value={block.text}
            label="Button text"
            placeholder="Button"
            onFocus={select}
            onChange={(text) => set({ text })}
          />
        </div>
      );
    case "divider":
      return <hr className="canvas-divider" style={{ borderTop: `${block.thickness}px solid ${block.color}` }} />;
    case "spacer":
      return <div className="canvas-spacer" style={{ height: block.height }} />;
    case "social":
      return <SocialCanvas block={block} />;
    case "footer":
      return (
        <div className="canvas-footer">
          {block.text && <p>{block.text}</p>}
          <p>Your postal address and an unsubscribe link go here when it's sent.</p>
        </div>
      );
    default:
      return null;
  }
}

/** What an image, button or social block needs before it can be added. */
function Details({
  type,
  busy,
  cancel,
  add,
  setFailure,
}: {
  type: "image" | "button" | "social";
  busy: boolean;
  cancel: () => void;
  add: (block: NewBlock) => Promise<void>;
  setFailure: (f?: Failure) => void;
}) {
  const api = useApi();
  const [file, setFile] = useState<File>();
  const [url, setUrl] = useState("");
  const [alt, setAlt] = useState("");
  const [text, setText] = useState("");
  const [network, setNetwork] = useState<Network>("website");
  const [uploading, setUploading] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setFailure(undefined);
    if (type === "button") return add({ type, text: text.trim(), href: url.trim() });
    if (type === "social") return add({ type, links: [{ network, url: url.trim() }] });
    let src = url.trim();
    if (file) {
      if (file.size > maxImageBytes) {
        setFailure({ code: "invalid_image", message: "The image is over 5 MB.", hint: "Resize or compress it, then try again.", status: 0 });
        return;
      }
      setUploading(true);
      const res = await attempt(async () =>
        api.POST("/v1/images", { body: { data: await base64(file), file_name: file.name } }),
      );
      setUploading(false);
      if (!res.ok) return setFailure(res.failure);
      src = res.data?.url ?? "";
    }
    if (!src) {
      setFailure({ code: "invalid_image", message: "Choose an image to upload, or give its web address.", hint: "", status: 0 });
      return;
    }
    return add({ type, src, alt: alt.trim() });
  }

  const title = type === "image" ? "Add an image" : type === "button" ? "Add a button" : "Add social links";
  return (
    <form className="panel stack details" onSubmit={submit} aria-label={title}>
      <h2>{title}</h2>
      {type === "image" && (
        <>
          <label className="field">
            <span>Upload a picture (JPEG, PNG, GIF or WebP, up to 5 MB)</span>
            <input type="file" accept="image/jpeg,image/png,image/gif,image/webp" onChange={(e) => setFile(e.target.files?.[0])} />
          </label>
          <label className="field">
            <span>Or its web address</span>
            <input type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://" disabled={!!file} />
          </label>
          <label className="field">
            <span>Alt text, for people who can't see it</span>
            <input value={alt} onChange={(e) => setAlt(e.target.value)} maxLength={500} />
          </label>
        </>
      )}
      {type === "button" && (
        <>
          <label className="field">
            <span>Button text</span>
            <input value={text} onChange={(e) => setText(e.target.value)} maxLength={100} required placeholder="See the menu" />
          </label>
          <label className="field">
            <span>Link</span>
            <input value={url} onChange={(e) => setUrl(e.target.value)} required placeholder="https://" maxLength={2000} />
          </label>
        </>
      )}
      {type === "social" && (
        <>
          <label className="field">
            <span>Network</span>
            <select value={network} onChange={(e) => setNetwork(e.target.value as Network)}>
              {networks.map((n) => (
                <option key={n} value={n}>
                  {networkNames[n]}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>Profile address</span>
            <input type="url" value={url} onChange={(e) => setUrl(e.target.value)} required placeholder="https://" maxLength={2000} />
          </label>
        </>
      )}
      <div className="row">
        <button type="submit" className="button primary" disabled={busy || uploading}>
          {uploading ? "Uploading…" : "Add block"}
        </button>
        <button type="button" className="button" onClick={cancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}

const maxImageBytes = 5 * 1024 * 1024;

