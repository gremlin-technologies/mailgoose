import { useCallback, useState, type DragEvent, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router";

import { useApi } from "../api/context";
import { attempt, type Failure, type Result } from "../api/result";
import type { components } from "../api/schema";
import { Badge, FailureNotice, LoadFailed, Loading, PageHeader, useLoad } from "../components";
import {
  blockLabels,
  canDrop,
  copyOf,
  locate,
  needsDetails,
  paletteTypes,
  placeOf,
  starterBlock,
  zoneDown,
  zoneUp,
  type Block,
  type BlockDocument,
  type Drag,
  type NewBlock,
  type PaletteType,
  type Zone,
} from "./blocks";
import { Markdown } from "./markdown";

type Email = components["schemas"]["Email"];

const conflict: Failure = {
  code: "version_conflict",
  message: "This email was changed somewhere else, so it has been reloaded.",
  hint: "Make your change again on the email as it is now.",
  status: 409,
};

/** The email designer: a block palette and a canvas you drag blocks onto. */
export function Designer() {
  const { id = "" } = useParams();
  const api = useApi();
  const load = useCallback(() => attempt(() => api.GET("/v1/emails/{email_id}", { params: { path: { email_id: id } } })), [api, id]);
  const { state, reload, replace } = useLoad<Email>(load);
  const [failure, setFailure] = useState<Failure>();
  const [busy, setBusy] = useState(false);
  const [drag, setDrag] = useState<Drag>();
  const [pending, setPending] = useState<{ type: "image" | "button" | "social"; zone?: Zone }>();

  if (state.status === "loading") return <Loading />;
  if (state.status === "failed") return <LoadFailed failure={state.failure} retry={reload} />;
  const email = state.data;
  const path = { email_id: email.id };

  if (email.kind !== "blocks") {
    return (
      <section className="page">
        <PageHeader title={email.name} about="This email is hand-written HTML, so it's edited as code, not with blocks." />
        <p className="crumb">
          <Link to="/emails">All emails</Link>
        </p>
      </section>
    );
  }
  const doc = email.document as BlockDocument;

  /** Runs one block operation; the answer's email replaces the one shown. */
  async function change(call: () => Promise<Result<Email | undefined>>) {
    setBusy(true);
    setFailure(undefined);
    const res = await call();
    setBusy(false);
    if (res.ok) {
      if (res.data) replace(res.data);
      return true;
    }
    if (res.failure.code === "version_conflict") {
      setFailure(conflict);
      reload();
    } else {
      setFailure(res.failure);
    }
    return false;
  }

  const emailOf = async <T extends { email: Email }>(p: Promise<Result<T | undefined>>): Promise<Result<Email | undefined>> => {
    const res = await p;
    return res.ok ? { ok: true, data: res.data?.email } : res;
  };

  const add = (block: NewBlock, zone?: Zone) =>
    change(() =>
      emailOf(
        attempt(() =>
          api.POST("/v1/emails/{email_id}/blocks", {
            params: { path },
            body: { version: email.version, block, ...(zone ? placeOf(zone) : {}) },
          }),
        ),
      ),
    );

  const move = (blockId: string, zone: Zone) =>
    change(() =>
      emailOf(
        attempt(() =>
          api.POST("/v1/emails/{email_id}/blocks/{block_id}/move", {
            params: { path: { ...path, block_id: blockId } },
            body: { version: email.version, ...placeOf(zone) },
          }),
        ),
      ),
    );

  const remove = (blockId: string) =>
    change(() =>
      attempt(() =>
        api.DELETE("/v1/emails/{email_id}/blocks/{block_id}", {
          params: { path: { ...path, block_id: blockId }, query: { version: email.version } },
        }),
      ),
    );

  /** A new block from the palette, at zone or (with none) last before the footer. */
  function addNew(type: PaletteType, zone?: Zone) {
    if (needsDetails(type)) {
      setPending({ type, zone });
      return;
    }
    void add(starterBlock(type), zone);
  }

  function drop(zone: Zone) {
    const d = drag;
    setDrag(undefined);
    if (!d || busy || !canDrop(d, zone, doc)) return;
    if (d.kind === "new") addNew(d.type, zone);
    else void move(d.id, zone);
  }

  const zoneProps = { drag, doc, onDrop: drop };
  const top = doc.blocks;

  return (
    <section className="page designer">
      <PageHeader title={email.name} about="Drag blocks onto the email, or click one to add it at the end.">
        <Link to="/emails" className="button small">
          All emails
        </Link>
      </PageHeader>
      <FailureNotice failure={failure} />
      <div className="designer-body">
        <aside className="palette" aria-label="Blocks">
          <h2 className="small muted">Blocks</h2>
          <ul>
            {paletteTypes.map((type) => (
              <li key={type}>
                <button
                  type="button"
                  className="palette-item"
                  draggable={!busy}
                  disabled={busy}
                  aria-label={`Add ${blockLabels[type].toLowerCase()}`}
                  onDragStart={(e) => startDrag(e, { kind: "new", type }, setDrag)}
                  onDragEnd={() => setDrag(undefined)}
                  onClick={() => addNew(type)}
                >
                  {blockLabels[type]}
                </button>
              </li>
            ))}
          </ul>
        </aside>
        <div className="canvas-wrap" style={{ background: doc.settings.background_color }}>
          {pending && (
            <Details
              type={pending.type}
              busy={busy}
              cancel={() => setPending(undefined)}
              add={async (block) => {
                if (await add(block, pending.zone)) setPending(undefined);
              }}
              setFailure={setFailure}
            />
          )}
          <div
            className={`canvas${drag ? " dragging" : ""}`}
            style={{ maxWidth: doc.settings.content_width }}
            aria-busy={busy}
            aria-label="Email content"
            role="list"
          >
            <DropZone zone={{ parent: null, column: null, after: null }} {...zoneProps} />
            {top.map((b) => (
              <div key={b.id} role="listitem">
                <BlockFrame
                  block={b}
                  doc={doc}
                  busy={busy}
                  setDrag={setDrag}
                  move={move}
                  duplicate={(zone) => add(copyOf(b), zone)}
                  remove={remove}
                >
                  {b.type === "columns" ? (
                    <div className="canvas-columns">
                      {b.columns.map((col, c) => (
                        <div key={c} className="canvas-column" role="list" aria-label={c === 0 ? "Left column" : "Right column"}>
                          <DropZone zone={{ parent: b.id, column: c, after: null }} {...zoneProps} />
                          {col.blocks.length === 0 && <p className="canvas-empty">Drop blocks here</p>}
                          {(col.blocks as Block[]).map((inner) => (
                            <div key={inner.id} role="listitem">
                              <BlockFrame
                                block={inner}
                                doc={doc}
                                busy={busy}
                                setDrag={setDrag}
                                move={move}
                                duplicate={(zone) => add(copyOf(inner), zone)}
                                remove={remove}
                              >
                                <BlockView block={inner} />
                              </BlockFrame>
                              <DropZone zone={{ parent: b.id, column: c, after: inner.id }} {...zoneProps} />
                            </div>
                          ))}
                        </div>
                      ))}
                    </div>
                  ) : (
                    <BlockView block={b} />
                  )}
                </BlockFrame>
                {b.type !== "footer" && <DropZone zone={{ parent: null, column: null, after: b.id }} {...zoneProps} />}
              </div>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}

function startDrag(e: DragEvent, d: Drag, setDrag: (d: Drag) => void) {
  // Firefox starts a drag only once something is set.
  e.dataTransfer?.setData("text/plain", d.kind === "new" ? d.type : d.id);
  if (e.dataTransfer) e.dataTransfer.effectAllowed = d.kind === "new" ? "copy" : "move";
  setDrag(d);
}

function zoneKey(z: Zone) {
  return `zone-${z.parent ?? "top"}-${z.column ?? "x"}-${z.after ?? "start"}`;
}

/** A place between blocks that a drag can drop on. Mouse only: each block's buttons do the same by keyboard. */
function DropZone({ zone, drag, doc, onDrop }: { zone: Zone; drag?: Drag; doc: BlockDocument; onDrop: (z: Zone) => void }) {
  const [over, setOver] = useState(false);
  const ok = drag !== undefined && canDrop(drag, zone, doc);
  return (
    <div
      className={`drop-zone${ok ? " open" : ""}${over && ok ? " over" : ""}`}
      data-testid={zoneKey(zone)}
      aria-hidden="true"
      onDragOver={(e) => {
        if (!ok) return;
        e.preventDefault();
        setOver(true);
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(e) => {
        e.preventDefault();
        setOver(false);
        onDrop(zone);
      }}
    />
  );
}

/** A block on the canvas, with its buttons: move up and down, duplicate, delete. The footer is locked. */
function BlockFrame({
  block,
  doc,
  busy,
  setDrag,
  move,
  duplicate,
  remove,
  children,
}: {
  block: Block;
  doc: BlockDocument;
  busy: boolean;
  setDrag: (d?: Drag) => void;
  move: (id: string, zone: Zone) => Promise<boolean>;
  duplicate: (zone: Zone) => Promise<boolean>;
  remove: (id: string) => Promise<boolean>;
  children: ReactNode;
}) {
  const [confirming, setConfirming] = useState(false);
  const label = blockLabels[block.type];
  if (block.type === "footer") {
    return (
      <div className="canvas-block locked" role="group" aria-label="Footer block">
        <div className="block-tools">
          <Badge tone="neutral">Footer, locked</Badge>
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
  return (
    <div
      className="canvas-block"
      role="group"
      aria-label={`${label} block`}
      draggable={!busy}
      onDragStart={(e) => {
        e.stopPropagation();
        startDrag(e, { kind: "move", id: block.id, type: block.type }, setDrag);
      }}
      onDragEnd={() => setDrag(undefined)}
    >
      <div className="block-tools">
        <span className="block-label">{label}</span>
        <button type="button" className="button small" disabled={busy || !up} onClick={() => up && move(block.id, up)}>
          Move up
        </button>
        <button type="button" className="button small" disabled={busy || !down} onClick={() => down && move(block.id, down)}>
          Move down
        </button>
        <button type="button" className="button small" disabled={busy} onClick={() => duplicate(here)}>
          Duplicate
        </button>
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
          <button type="button" className="button small" disabled={busy} onClick={() => setConfirming(true)}>
            Delete
          </button>
        )}
      </div>
      {children}
    </div>
  );
}

const align = (a: string | undefined) => ({ textAlign: (a ?? "left") as "left" | "center" | "right" });

/** How a block looks in the email, near enough to design with. */
function BlockView({ block }: { block: Block }) {
  switch (block.type) {
    case "heading": {
      const H = (`h${block.level}` as "h1" | "h2" | "h3");
      return <H className="canvas-heading" style={align(block.align)}>{block.text}</H>;
    }
    case "text":
      return (
        <div className="canvas-text" style={align(block.align)}>
          <Markdown text={block.text} />
        </div>
      );
    case "image":
      return (
        <div style={align(block.align)}>
          <img className="canvas-image" src={block.src} alt={block.alt} width={block.width ?? undefined} />
        </div>
      );
    case "button":
      return (
        <div style={align(block.align)}>
          <span className="canvas-button" style={{ background: block.background_color, color: block.text_color }} title={block.href}>
            {block.text}
          </span>
        </div>
      );
    case "divider":
      return <hr className="canvas-divider" style={{ borderTop: `${block.thickness}px solid ${block.color}` }} />;
    case "spacer":
      return <div className="canvas-spacer" style={{ height: block.height }} />;
    case "social":
      return (
        <p className="canvas-social" style={align(block.align)}>
          {block.links.map((l) => (
            <span key={l.network + l.url} title={l.url}>
              {l.network}
            </span>
          ))}
        </p>
      );
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

const networks = ["website", "facebook", "instagram", "x", "linkedin", "youtube", "tiktok", "threads", "bluesky"] as const;

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
  const [network, setNetwork] = useState<(typeof networks)[number]>("website");
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
            <select value={network} onChange={(e) => setNetwork(e.target.value as (typeof networks)[number])}>
              {networks.map((n) => (
                <option key={n} value={n}>
                  {n}
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

/** A file's bytes in standard base64, as the upload API takes them. */
async function base64(file: File): Promise<string> {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}
