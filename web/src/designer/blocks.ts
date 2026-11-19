// The designer's block logic, kept apart from React so it can be tested on
// its own: what a new block starts as, where a drop puts a block, and which
// moves are allowed. Every change itself goes through the block operations
// API (MG-20); nothing here edits a document.
import type { components } from "../api/schema";

export type Block = components["schemas"]["Block"];
export type ColumnBlock = components["schemas"]["ColumnContentBlock"];
export type BlockDocument = components["schemas"]["BlockDocument"];
export type BlockType = Block["type"];

/** The blocks the palette offers, in order. The footer is never added. */
export const paletteTypes = ["heading", "text", "image", "button", "divider", "spacer", "columns", "social"] as const;
export type PaletteType = (typeof paletteTypes)[number];

export const blockLabels: Record<BlockType, string> = {
  heading: "Heading",
  text: "Text",
  image: "Image",
  button: "Button",
  divider: "Divider",
  spacer: "Spacer",
  columns: "Two columns",
  social: "Social links",
  footer: "Footer",
};

/** Blocks that need something from the person before they can be added. */
export const needsDetails = (type: PaletteType): type is "image" | "button" | "social" =>
  type === "image" || type === "button" || type === "social";

/** A new block of a type that needs nothing more; the API fills the rest. */
export function starterBlock(type: Exclude<PaletteType, "image" | "button" | "social">): NewBlock {
  switch (type) {
    case "heading":
      return { type, text: "A heading" };
    case "text":
      return { type, text: "Write something here." };
    case "columns":
      return { type, columns: [{ blocks: [] }, { blocks: [] }] };
    default:
      return { type };
  }
}

/** A block as the add operation takes it: a type, and any fields. */
export type NewBlock = { type: string } & Record<string, unknown>;

/**
 * Where a drop lands: in the top level (parent null) or in one column of a
 * columns block, after a block, or first in that list when after is null.
 */
export type Zone = { parent: string | null; column: number | null; after: string | null };

/** The place fields of an add or move request for a zone. */
export function placeOf(zone: Zone): { after?: string; index?: number; parent_id?: string; column?: number } {
  if (zone.after) return { after: zone.after };
  if (zone.parent) return { parent_id: zone.parent, column: zone.column ?? 0, index: 0 };
  return { index: 0 };
}

/** Where a block is: its list, the list's owner, and its index in it. */
export type Location = { list: Block[]; parent: string | null; column: number | null; index: number };

export function locate(doc: BlockDocument, id: string): Location | undefined {
  for (let i = 0; i < doc.blocks.length; i++) {
    const b = doc.blocks[i];
    if (b.id === id) return { list: doc.blocks, parent: null, column: null, index: i };
    if (b.type === "columns") {
      for (let c = 0; c < b.columns.length; c++) {
        const list = b.columns[c].blocks as Block[];
        const j = list.findIndex((x) => x.id === id);
        if (j >= 0) return { list, parent: b.id, column: c, index: j };
      }
    }
  }
  return undefined;
}

/** What is being dragged: a new block from the palette, or a block on the canvas. */
export type Drag = { kind: "new"; type: PaletteType } | { kind: "move"; id: string; type: BlockType };

/** Whether a drag may drop in a zone. Columns don't go inside columns. */
export function canDrop(drag: Drag, zone: Zone, doc: BlockDocument): boolean {
  if (drag.type === "footer") return false;
  if (zone.parent && drag.type === "columns") return false;
  if (drag.kind === "move") return !isNoop(drag.id, zone, doc);
  return true;
}

/** Whether moving id to zone would leave it where it is. */
export function isNoop(id: string, zone: Zone, doc: BlockDocument): boolean {
  if (zone.after === id) return true;
  const at = locate(doc, id);
  if (!at || at.parent !== zone.parent || at.column !== zone.column) return false;
  const before = at.index === 0 ? null : at.list[at.index - 1].id;
  return before === zone.after;
}

/** The zone one place up in a block's own list, or undefined at the top. */
export function zoneUp(doc: BlockDocument, id: string): Zone | undefined {
  const at = locate(doc, id);
  if (!at || at.index === 0) return undefined;
  const after = at.index === 1 ? null : at.list[at.index - 2].id;
  return { parent: at.parent, column: at.column, after };
}

/** The zone one place down in a block's own list; the footer stays last. */
export function zoneDown(doc: BlockDocument, id: string): Zone | undefined {
  const at = locate(doc, id);
  if (!at) return undefined;
  const next = at.list[at.index + 1];
  if (!next || next.type === "footer") return undefined;
  return { parent: at.parent, column: at.column, after: next.id };
}

/** A copy of a block for adding: the same fields, with no ids, so the API gives new ones. */
export function copyOf(block: Block): NewBlock {
  const strip = (b: Record<string, unknown>): Record<string, unknown> => {
    const { id: _id, ...rest } = b;
    void _id;
    if (Array.isArray(rest.columns)) {
      rest.columns = (rest.columns as { blocks: Record<string, unknown>[] }[]).map((col) => ({
        blocks: col.blocks.map(strip),
      }));
    }
    return rest;
  };
  return strip(block as unknown as Record<string, unknown>) as NewBlock;
}
