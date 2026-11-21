// Draws the social block's icons (MG-109) as PNGs from Font Awesome Free's
// brand SVGs (CC BY 4.0). Run it only to change the icons; the PNGs are
// committed in internal/socialicons/icons. See internal/socialicons/README.md.
//
//   node scripts/social-icons/generate.mjs <fontawesome-free/svgs> <dir with @resvg/resvg-js> [out dir]
import { createRequire } from "node:module";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const [svgs, modules, out = "internal/socialicons/icons/v1"] = process.argv.slice(2);
if (!svgs || !modules) {
  console.error("usage: generate.mjs <fontawesome-free/svgs> <dir with node_modules/@resvg/resvg-js> [out dir]");
  process.exit(2);
}
const { Resvg } = createRequire(join(modules, "package.json"))("@resvg/resvg-js");

// Each network's glyph and its own color, from the brand's guidelines.
// Instagram's glyph uses its gradient; the others one color.
const instagram = "url(#ig)";
const networks = {
  facebook: { file: "brands/facebook.svg", brand: "#0866FF", dark: "#0866FF" }, // Facebook allows only its blue or white
  instagram: { file: "brands/instagram.svg", brand: instagram },
  x: { file: "brands/x-twitter.svg", brand: "#000000" },
  linkedin: { file: "brands/linkedin.svg", brand: "#0A66C2" },
  youtube: { file: "brands/youtube.svg", brand: "#FF0000" },
  tiktok: { file: "brands/tiktok.svg", brand: "#000000" },
  threads: { file: "brands/threads.svg", brand: "#000000" },
  bluesky: { file: "brands/bluesky.svg", brand: "#1185FE" },
  website: { file: "solid/globe.svg", brand: "#52525B" },
};
const dark = "#111111";
const light = "#FFFFFF";
const size = 96; // twice the largest size the block shows (48px), for sharp icons on high-density screens

const gradient = `<defs><linearGradient id="ig" x1="0" y1="1" x2="1" y2="0">
<stop offset="0" stop-color="#FEDA75"/><stop offset="0.25" stop-color="#FA7E1E"/><stop offset="0.5" stop-color="#D62976"/>
<stop offset="0.75" stop-color="#962FBF"/><stop offset="1" stop-color="#4F5BD5"/></linearGradient></defs>`;

for (const [network, n] of Object.entries(networks)) {
  const src = readFileSync(join(svgs, n.file), "utf8");
  const [, , , w, h] = src.match(/viewBox="(\S+) (\S+) (\S+) (\S+)"/).map(Number);
  const path = src.match(/<path[^>]* d="([^"]+)"/)[1];
  const side = Math.max(w, h);
  for (const [color, fill] of Object.entries({ brand: n.brand, dark: n.dark ?? dark, light })) {
    // Centre the glyph in a square, keeping its proportions.
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${side} ${side}">${fill === instagram ? gradient : ""}
<path transform="translate(${(side - w) / 2} ${(side - h) / 2})" fill="${fill}" d="${path}"/></svg>`;
    const png = new Resvg(svg, { fitTo: { mode: "width", value: size } }).render().asPng();
    mkdirSync(join(out, color), { recursive: true });
    writeFileSync(join(out, color, `${network}.png`), png);
  }
}
console.log(`wrote ${Object.keys(networks).length * 3} icons to ${out}`);
