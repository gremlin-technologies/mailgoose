# Design system

How the web app looks and behaves, and why. The tokens and components live in
`web/src/styles.css`; this file is the reasoning behind them. The mascot and
palette origin are in `docs/brand.md`.

## Direction (owner brief, Nov 20)

- Imitate the restraint of Stripe, Airbnb and Shopify, in mailgoose's colors.
- Mostly neutral: **white pages, near-black text**. The blue appears only on
  actions and state (primary buttons, links, the selected row, focus).
- **Minimal and shadow-based.** Depth comes from a one-pixel hairline plus a
  soft shadow, not from tinted fills or heavy borders.
- **Simple interactions.** A 120ms color or shadow change on hover and focus.
  Nothing moves, scales or bounces.
- **Simple icons in the sidebar.**
- The email editor is out of scope here; it is being redesigned separately.

## What we took from each

Sources are public docs and third-party write-ups; none of this copies a
design, and no fonts, icons or code from these companies are used.

| | Took | Source and confidence |
| --- | --- | --- |
| **Shopify Polaris** | Semantic tokens named by role (`bg`, `surface`, `border`, `text`), a white card on the page, small cast shadows on cards and a tighter one on buttons, heavier shadows only for overlays | Polaris docs on tokens, depth and color. Token names and structure confirmed; exact shadow values were not retrievable, so ours are our own. |
| **Stripe** | Light mode, dense tables with a quiet header row, one saturated accent that marks selection and the primary action, accessible-by-construction color pairs, thin outline icons | Stripe's Apps style docs and its accessible-color write-up (read via search results only), plus third-party breakdowns of the Dashboard. The dashboard itself is not public, so details are inferred. |
| **Airbnb** | Ink-on-white with one brand color, hairline borders, 8px controls and larger card radii, a single soft shadow tier, focus shown by changing the border rather than a glow, no press transform | A community-compiled spec of airbnb.com (not an official Airbnb source) and Airbnb's own DLS articles. Values are indicative. |

What the three agree on, and what we built from it: neutrals carry the screen,
one color earns its place, borders are hairlines, shadows are small and soft,
type is one clean sans, and spacing sits on a 4px grid.

## Tokens

All colors are CSS custom properties on `:root`, with a dark set under
`:root[data-theme="dark"]`. The app is light unless dark mode is turned on in
Settings (`web/src/theme.ts`, kept in the browser). `web/src/styles.test.ts`
checks every text and background pair against WCAG 2.2 AA, in both modes.

### Color (light)

| Token | Value | Use |
| --- | --- | --- |
| `--bg`, `--surface` | `#ffffff` | Page, cards, inputs |
| `--surface-subtle` | `#f7f8fa` | Sidebar, table header, code, empty states |
| `--surface-hover` | `#f0f2f5` | Hover on buttons and menu items |
| `--text` | `#0f1217` | Body and headings |
| `--muted` | `#596072` | Secondary text, labels, inactive icons |
| `--border` | `#e3e6ec` | Card, table and divider hairlines (decorative) |
| `--line-strong` | `#cdd2db` | Button edges, dashed empty states (decorative) |
| `--input-border` | `#858c9b` | Input edges; 3:1 against white, per WCAG 1.4.11 |
| `--accent` | `#5562a3` | Primary button, links, focus, active icon |
| `--accent-hover` | `#47528a` | Primary hover |
| `--accent-soft` / `-text` | `#eff1fb` / `#3f4a85` | Selected choice, info badge, next step |
| `--brand-mark` | `#f68a3f` | Beak orange. Unused since the logo replaced the text wordmark (Nov 21); kept for decoration only. |

Status colors (`good`, `warn`, `error`, `danger`) are unchanged in role and
used only for badges, notices and destructive actions.

The blue is the deep periwinkle from the mascot's shadow, the shade that gives
white text 5.7:1. The lighter periwinkle (`#6A7CC8`) is not used for text or
buttons. Cream and the warm shade from the old palette are no longer used in
the app; the mascot keeps them.

### Depth

| Token | Used for |
| --- | --- |
| `--shadow-xs` | Inputs, the account box, the selected menu item |
| `--shadow-card` | Cards, tables, lists, tiles |
| `--shadow-pop` | Floating things: the sign-in card (and menus and dialogs later) |
| `--shadow-button`, `--shadow-button-primary` | Buttons: an edge ring and a 1px drop; the primary adds a faint top highlight |

### Shape, type, spacing

- Radius: 6px small parts, 8px controls, 12px cards and tables.
- Type: Inter (variable, self-hosted), falling back to the system font. Body
  15px; titles 24px/600 with tight tracking; section heads 16px/600; small and
  label text 13px. Numbers in tables, tiles and balances use tabular figures.
- Control height: 36px for inputs, selects and buttons; 28px for small
  buttons. They share a height so a row of them lines up.
- Spacing: a 4px grid; cards pad 20px by 24px; pages pad 36px by 40px.

## Components

- **Logo.** `Wordmark` in `components.tsx`: `brand/wordmark.png`, with the
  light-text version when dark mode is on. 28px tall in the side menu, 36px above the
  sign-in and policy cards.
- **Side menu.** 240px, on `--surface-subtle`. The logo, then an account
  box, then the menu in three runs: Dashboard on its own, **Marketing**
  (Emails, Campaigns, Contacts, Tags, Signup forms) and **Account** (Domains,
  API keys, Credits and billing, Settings). The selected page is a white pill
  with a hairline and a small shadow, and its icon turns blue. Below it, a
  divider, the signed-in email and Sign out.
- **Icons.** Lucide, 18px, 1.75 stroke, outline only, decorative
  (`aria-hidden`; the label carries the meaning). Used in the menu and on
  Sign out. Not on every button.
- **Buttons.** Default is white with an edge ring; primary is the blue, used
  once per view for the main action; danger only for destructive ones.
- **Cards and tables.** A hairline plus `--shadow-card`. Table headers are 12px
  muted on `--surface-subtle`; rows highlight on hover.
- **Badges.** Pills, 12px/500, soft fill. Neutral ones get a hairline.
- **Notices.** Soft status fill, no icon, no border.
- **Inputs.** A 3:1 edge. Focus turns the edge blue and adds a soft 3px ring.
- **Empty states.** A dashed hairline on `--surface-subtle`.
- **Small screens.** Under 720px the menu becomes a header with a
  horizontally scrolling row of items.

## Interaction rules

- Hover and focus change color, border or shadow over 120ms. No transforms.
- `prefers-reduced-motion` turns the transitions off.
- Links are blue and underline on hover; buttons never underline.
- Light by default. Dark mode is a switch in Settings, not the system setting
  (owner, Nov 21), with the same structure: hairlines and shadows are re-tuned
  rather than inverted. The public site is light only.
- **UI text names the UI, not CLI commands** (owner, Nov 22, MG-121). No error,
  help text or empty state in the web app, and no public page, names a
  `mailgoose ...` command or a `--flag`; each points to the page or control
  that fixes the problem ("Update your card on the Billing page"). The API's
  hints are written for the CLI and agents, so the web app words its own in one
  map from error code to wording (`web/src/api/hints.ts`), and anything the map
  doesn't cover has its hint dropped when it names the CLI, never shown raw.
  A message that names a command is reworded in the API to suit both.

## The email editor

The block designer (`web/src/designer/`) was rebuilt on Nov 20 after studying
how Mailchimp's editor behaves on screen: what happens on hover, on click, on
drop. Only behavior was observed; none of its code, assets or block options
were used, and the blocks are mailgoose's own.

What was taken from it:

- **Click anything to edit it.** Clicking a block selects it: an outline, a
  small tab that names it, and its buttons. The properties panel follows.
- **Type on the email.** Headings, text and a button's words are edited where
  they sit. A text block gets a floating toolbar (bold, italic, link, lists,
  merge tag) and is saved as the same Markdown subset as before.
- **A dropped block is ready.** A new block is selected at once, with its
  starter words selected so typing replaces them.
- **Chrome floats.** The tab and buttons sit above the block instead of taking
  room in it, so the canvas has the email's real spacing.

What is mailgoose's own:

- **Layout.** The editor takes the whole window (the app's side menu is
  hidden): a bar with the name, save state, Design / Desktop / Mobile and
  a primary "Save and exit" button (MG-83), which saves what's left at once
  and goes back to the campaign or the email list it came from; the
  block palette on the left; the email in the middle; properties on the right.
  The palette stays visible while a block is selected, so blocks can be added
  without leaving it.
- **Dragging.** A drag never has to hit a thin target: anywhere over the email
  picks the nearest gap and draws one line there, labeled "Drop here". The
  line takes no room, so nothing shifts under the pointer. Blocks you type on
  are dragged by their tab; the others from anywhere on them. An empty column
  is one big target.
- **Keyboard.** Every block's tab and buttons are in the tab order; Move up
  and Move down do what a drag does. Escape deselects.

Not done yet: undo and redo (the API has no history), optimistic moves (a move
shows when the server answers), adding an image, button or social block still
asks for its details first, and the HTML email editor keeps its old layout.

## Not done here

- **The mascot** is not used in the app yet. `docs/brand.md` suggests the
  sign-in page, empty states and the 404 page.
- **Toasts, menus and dialogs** don't exist yet; `--shadow-pop` is ready for
  them.

## The public site

mailgoose.app lives in its own public repository,
gremlin-technologies/mailgoose-public, and uses these tokens too:
`scripts/site/tokens.mjs` copies the light `:root` block into its
`site/assets/tokens.css`. After changing a token here, run
`node scripts/site/tokens.mjs` with that repository checked out next to this
one, and open a PR there (`docs/public-site.md`). The site's own stylesheet
follows the same rules, a size up for a landing page.

## Open questions

- Is "the blue" the deep periwinkle (`#5562a3`) used here, or a different
  blue? Changing it is one token plus a run of `styles.test.ts`.
