# Brand

The mailgoose mascot and where the UI's colors come from. The web app and
editor tickets (MG-41 onward) build their design tokens from this file.

![The mailgoose wordmark: the goose's head in a blue mail carrier's cap, then the word mailgoose](brand/wordmark.png)

## Logos (Nov 21, the owner's)

- `brand/wordmark.png`: the goose's head in a mail carrier's cap, then the word
  "mailgoose" in dark charcoal. `brand/wordmark-light.png` is the same with
  light text, for dark backgrounds (the app in dark mode). Used in the app's
  side menu and the public site's header.
- `brand/goose.png`: the whole goose, walking, with a satchel of letters. Used
  wherever the goose appears on its own: the site's hero and 404 page.
- The favicons (app and site) are the head from the wordmark, cropped square.

These are transparent cut-outs of the owner's artwork, which came on black.
The web copies (`web/public/brand/`, the public site's `site/assets/brand/`
in gremlin-technologies/mailgoose-public, the favicons) are
downsized and palette-compressed from these. `brand/mascot.png` is the earlier
mascot and is no longer used.

## Direction

- **The goose is the mascot.** Use it where the product introduces itself:
  sign-in, empty states, the 404 page, the docs. Not on every screen.
- **Modern and sleek, but simple to use.** Calm surfaces, generous spacing, few
  controls on screen at once. The goose brings the warmth; the layout stays
  plain.
- **The color palette comes from the mascot.**

> **Update, Nov 20:** the web app's look moved to white pages and near-black
> text with the blue as the only accent; see `design-system.md`. Cream, the warm
> shade and the satchel colors are no longer used in the app, and orange is
> only the dot in the wordmark. The mascot and this palette still describe the
> goose and the public site.

## Starting palette

Sampled from `brand/mascot.png`. These are a starting point, not final tokens.

| Name | Hex | Taken from | Suggested use |
| --- | --- | --- | --- |
| Periwinkle | `#6A7CC8` | background | primary, brand backgrounds |
| Deep periwinkle | `#5562A3` | the goose's shadow | primary hover and pressed |
| Cream | `#F4E3D2` | the goose | surfaces |
| Warm shade | `#E0CCBE` | the goose's shading | borders, muted fills |
| Beak orange | `#F68A3F` | beak and feet | accent, calls to action |
| Satchel brown | `#825142` | the satchel | secondary |
| Buckle gold | `#D8A24C` | the buckle | highlights |
| Eye charcoal | `#473E3B` | the eye | text |

## Tokens

`web/src/styles.css` turns the palette into light and dark design tokens,
adjusted for contrast, and `web/src/styles.test.ts` checks every text and
background pair the app draws against WCAG 2.2 AA. Change a token there and
the test says whether the new shade still passes.

## Contrast notes

Ratios of the raw colors, which the tokens work around (4.5:1 for body text,
3:1 for large text and UI parts):

- Charcoal text on cream: 8.3:1. Passes.
- White text on deep periwinkle: 5.7:1. Passes.
- Satchel brown on cream: 5.2:1. Passes.
- White text on periwinkle: 3.9:1. Large text only; body text needs a darker
  shade.
- White text on beak orange: 2.4:1. Fails. Use charcoal text (4.3:1, large text
  only) or darken the orange for buttons with white labels.
- Warm shade on cream: 1.2:1. Fine for decorative borders, but not for borders
  that mark an input; those need 3:1.
