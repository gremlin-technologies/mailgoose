# Social icons

The social block's icons (MG-109), one 96px PNG per network and color under
`icons/<version>/<color>/<network>.png`. Emails show them at 32, 40 or 48
pixels, so they stay sharp on high-density screens.

## Where they come from

The glyphs are Font Awesome Free's (`brands/` and, for the website icon,
`solid/globe`), by Fonticons, Inc., licensed under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/): see
<https://fontawesome.com/license/free>. We changed them by filling each with
one color (Instagram's with its gradient) and drawing it as a PNG.

The colors follow each network's own brand guidelines, which allow its logo
as a link to a profile:

- `brand`: Facebook #0866FF, Instagram's gradient, LinkedIn #0A66C2,
  YouTube #FF0000, Bluesky #1185FE, X, TikTok and Threads black, and the
  website globe in gray (#52525B).
- `dark`: near black (#111111), except Facebook, which allows only its blue
  or white.
- `light`: white, for dark backgrounds.

There are deliberately no circles, squares or outlines drawn around the
glyphs, and no gray brand logos: several networks' guidelines forbid
changing their logo's shape, putting it in a container or recoloring it.

## Redrawing them

Redraw only to change them, and then into a new version folder (raise
`Version` in `socialicons.go`), so emails already sent keep their icons.

```sh
mkdir /tmp/icons && cd /tmp/icons && npm init -y
npm i --ignore-scripts @fortawesome/fontawesome-free@7.3.1 @resvg/resvg-js@2.6.2
cd -   # back to the repository
node scripts/social-icons/generate.mjs \
  /tmp/icons/node_modules/@fortawesome/fontawesome-free/svgs /tmp/icons \
  internal/socialicons/icons/v2
```
