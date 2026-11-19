# Brand

The mailgoose mascot and where the UI's colors come from. The web app and
editor tickets (MG-41 onward) build their design tokens from this file.

![The mailgoose mascot: a white goose carrying a brown mail satchel, on a periwinkle background](brand/mascot.png)

## Direction

- **The goose is the mascot.** Use it where the product introduces itself:
  sign-in, empty states, the 404 page, the docs. Not on every screen.
- **Modern and sleek, but simple to use.** Calm surfaces, generous spacing, few
  controls on screen at once. The goose brings the warmth; the layout stays
  plain.
- **The color palette comes from the mascot.**

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
