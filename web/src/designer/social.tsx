// The social block (MG-109): its icons on the canvas, and its links and
// style in the properties panel. The icons are the API's own PNGs, the same
// files the sent email shows.
import { ArrowDown, ArrowUp, X } from "lucide-react";

import type { components } from "../api/schema";
import type { Fields } from "./autosave";
import { AlignField, NumberField, type Align } from "./fields";

type Social = components["schemas"]["SocialBlock"];
export type SocialLink = Social["links"][number];
export type Network = SocialLink["network"];
type Display = NonNullable<Social["display"]>;
type Color = NonNullable<Social["color"]>;
type Size = NonNullable<Social["size"]>;
type Layout = NonNullable<Social["layout"]>;

/** The networks, in the order the picker lists them, with their names. */
export const networkNames: Record<Network, string> = {
  website: "Website",
  facebook: "Facebook",
  instagram: "Instagram",
  x: "X",
  linkedin: "LinkedIn",
  youtube: "YouTube",
  tiktok: "TikTok",
  threads: "Threads",
  bluesky: "Bluesky",
};
export const networks = Object.keys(networkNames) as Network[];

/** A social block's style, with the defaults for one saved before it had one. */
export function socialStyle(b: Partial<Social>) {
  return {
    display: b.display ?? "icons",
    color: b.color ?? "brand",
    size: b.size ?? 32,
    spacing: b.spacing ?? 12,
    layout: b.layout ?? "horizontal",
    align: (b.align ?? "center") as Align,
  };
}

/** Where the API serves a network's icon in a color. */
export const iconUrl = (color: Color, network: Network) => `/assets/social/v1/${color}/${network}.png`;

const textColors: Record<Color, string> = { brand: "#2563eb", dark: "#18181b", light: "#ffffff" };

/** A social block as it will send. White icons get a dark backdrop, so they show on the canvas. */
export function SocialCanvas({ block }: { block: Social }) {
  const s = socialStyle(block);
  const icons = s.display !== "text";
  const text = s.display !== "icons";
  const vertical = s.layout === "vertical";
  const justify = { left: "flex-start", center: "center", right: "flex-end" }[s.align];
  return (
    <ul
      className={`canvas-social plain${s.color === "light" ? " canvas-social-light" : ""}`}
      aria-label="Social links"
      style={{
        flexDirection: vertical ? "column" : "row",
        justifyContent: vertical ? undefined : justify,
        alignItems: vertical ? justify : "center",
        gap: s.spacing,
      }}
    >
      {block.links.map((l, i) => {
        const label = l.label || networkNames[l.network];
        return (
          <li key={i} title={l.url} className="canvas-social-link" style={{ color: textColors[s.color] }}>
            {icons && <img src={iconUrl(s.color, l.network)} alt={text ? "" : label} width={s.size} height={s.size} />}
            {text && <span style={{ lineHeight: `${s.size}px` }}>{label}</span>}
          </li>
        );
      })}
    </ul>
  );
}

function Choice<T extends string | number>({
  legend,
  name,
  value,
  options,
  onChange,
}: {
  legend: string;
  name: string;
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
}) {
  return (
    <fieldset className="field align-field">
      <legend>{legend}</legend>
      <div className="row">
        {options.map((o) => (
          <label key={String(o.value)} className="choice">
            <input type="radio" name={name} checked={value === o.value} onChange={() => onChange(o.value)} />
            {o.label}
          </label>
        ))}
      </div>
    </fieldset>
  );
}

const displays: { value: Display; label: string }[] = [
  { value: "icons", label: "Icons" },
  { value: "icons_and_text", label: "Icons and names" },
  { value: "text", label: "Names" },
];
const colors: { value: Color; label: string }[] = [
  { value: "brand", label: "Brand colors" },
  { value: "dark", label: "Dark" },
  { value: "light", label: "White" },
];
const sizes: { value: Size; label: string }[] = [
  { value: 32, label: "Small" },
  { value: 40, label: "Medium" },
  { value: 48, label: "Large" },
];
const layouts: { value: Layout; label: string }[] = [
  { value: "horizontal", label: "In a row" },
  { value: "vertical", label: "One a line" },
];

/** A social block's links, then its style. */
export function SocialProperties({ block, set }: { block: Social; set: (f: Fields) => void }) {
  const s = socialStyle(block);
  const links = block.links;
  const change = (i: number, l: Partial<SocialLink>) => set({ links: links.map((x, j) => (j === i ? { ...x, ...l } : x)) });
  const swap = (i: number, j: number) => {
    const next = [...links];
    [next[i], next[j]] = [next[j], next[i]];
    set({ links: next });
  };
  return (
    <>
      <h3 className="small">Links</h3>
      <ul className="stack plain">
        {links.map((l, i) => (
          <li key={i} className="social-link panel stack" role="group" aria-label={`Link ${i + 1}`}>
            <div className="row social-row">
              <label className="field">
                <span>Network</span>
                <select
                  value={l.network}
                  onChange={(e) => change(i, { network: e.target.value as Network })}
                  style={{ backgroundImage: `url(${iconUrl("brand", l.network)})` }}
                  className="social-network"
                >
                  {networks.map((n) => (
                    <option key={n} value={n}>
                      {networkNames[n]}
                    </option>
                  ))}
                </select>
              </label>
              <div className="row social-tools">
                <button type="button" className="button small" aria-label="Move up" title="Move up" disabled={i === 0} onClick={() => swap(i, i - 1)}>
                  <ArrowUp size={16} aria-hidden />
                </button>
                <button type="button" className="button small" aria-label="Move down" title="Move down" disabled={i === links.length - 1} onClick={() => swap(i, i + 1)}>
                  <ArrowDown size={16} aria-hidden />
                </button>
                <button
                  type="button"
                  className="button small"
                  aria-label="Remove"
                  title="Remove"
                  disabled={links.length === 1}
                  onClick={() => set({ links: links.filter((_, j) => j !== i) })}
                >
                  <X size={16} aria-hidden />
                </button>
              </div>
            </div>
            <label className="field">
              <span>Address</span>
              <input type="url" value={l.url} maxLength={2000} onChange={(e) => change(i, { url: e.target.value.trim() })} />
            </label>
            <label className="field">
              <span>Name shown (optional)</span>
              <input
                value={l.label ?? ""}
                maxLength={50}
                placeholder={networkNames[l.network]}
                onChange={(e) => change(i, { label: e.target.value || undefined })}
              />
            </label>
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
      <Choice legend="Show" name="social-display" value={s.display} options={displays} onChange={(display) => set({ display })} />
      <Choice legend="Icon color" name="social-color" value={s.color} options={colors} onChange={(color) => set({ color })} />
      <Choice legend="Icon size" name="social-size" value={s.size} options={sizes} onChange={(size) => set({ size })} />
      <Choice legend="Layout" name="social-layout" value={s.layout} options={layouts} onChange={(layout) => set({ layout })} />
      <NumberField label="Space between links (pixels)" value={s.spacing} min={0} max={48} onChange={(v) => v !== null && set({ spacing: v })} />
      <AlignField value={s.align} onChange={(v) => set({ align: v })} />
    </>
  );
}
