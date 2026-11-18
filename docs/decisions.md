# Decisions log

Newest first. Each entry says who decided and whether it's confirmed. Anything
marked **recommended** is a proposal the owner hasn't confirmed yet; treat it as
the default until the owner does, but don't build anything hard to undo on it.

## 2026-11-18: HTML emails (MG-22, reversible defaults)

Picked while building MG-22; easy to change.

- **Errors stop the save**, not only the send: import, create and document
  replace all refuse markup with errors, so a stored html email is always
  sendable once it has content. Empty markup is allowed until the first
  import. A code editor that saves as you type (DES-10) may need a draft
  save that skips the check; that's for its ticket.
- **More than the ticket's list is refused:** frames (`frame`, `frameset`)
  with iframes, embedded objects (`object`, `embed`, `applet`), and
  `vbscript:` with `javascript:`. Mail clients don't show any of them.
  After review, also `data:` links, `<base>`, `<meta http-equiv="refresh">`
  and `javascript:` inside a `<style>` element.
- **The unsubscribe link must be a real link:** an `a` (or `area`) whose
  `href` is exactly `{{unsubscribe_url}}`. The tag inside a comment or as
  plain text doesn't count. `CompileHTML` runs the same check, so markup
  that was never imported can't be sent.
- **Malformed markup** means a non-void element that never closes, or an
  end tag that closes nothing or skips an open element. End tags HTML lets
  you leave out (`p`, `li`, `td`, `tr` and the like) aren't required.
- **102 KB is 104,448 bytes** (102 × 1,024), Gmail's clipping point.
- **The postal address footer** is added at send time, not on import, so
  it follows the account's current address. It's skipped when every line
  of the address already shows in the email's text (ignoring case and
  spacing).
- **The CSS warning list** is in the README and `emails.UnsupportedCSS`.

## 2026-11-18: Email renderer (MG-21, reversible defaults)

Picked while building MG-21; easy to change.

- **Social links render as text** ("Instagram · Website"), not icons, until
  image hosting lands (MG-23). Icons need hosted images.
- **HTML emails aren't rendered by the block renderer.** MG-22 validates
  them and adds the postal address footer.
- **No postal address, no render.** `Compile` refuses an account without
  one, so no campaign email can leave without it (CMP-2).
- **Merge values are HTML-escaped**, also inside links, and never
  URL-encoded. A link that is a whole tag (`{{unsubscribe_url}}`) is filled
  as is.
- **Fallbacks** work on first name, last name and email, up to 100
  characters, without `{` or `}`. The unsubscribe link takes none.
- **Links in text** must be `https://`, `http://`, `mailto:` or a whole
  merge tag; anything else is a lint error.
- **Look:** system font stack, 16px body text, near-black text on white,
  a light grey page, dark buttons by default. These are the email's
  defaults, not the app's brand; a customer's email shouldn't look like
  mailgoose.

## 2026-11-18: Deleting an account, and the settings pages (MG-43, reversible defaults)

Picked while building MG-43; easy to change. No ticket covered AUTH-6's API,
so MG-43 builds it along with the page.

- **Deletion is immediate**, not within 30 days: nothing waits in a queue, so
  nothing can be forgotten. AUTH-6 allows up to 30 days, so a grace period
  can be added later.
- **What stays:** the credit ledger (payments and refunds need it) and the
  account row, marked `closed` with its name set to "Deleted account" and the
  From name, From address and postal address cleared. Everything else with
  the account's id goes, including SES identities for its domains. A test
  fails when a new table with an `account_id` isn't listed as deleted or kept.
  Sign-in code rate-limit rows (by address) age out on their own.
- **Confirmation is the exact account name**, in the API (`?confirm=`), the
  CLI (`--confirm`) and the web page (typed). A mismatch is a 422
  `confirmation_mismatch`.
- **Who can:** a signed-in person or a full-scope key; a draft key gets 403.
- **A closed account gets no monthly free grant.**
- **New API keys default to draft scope** in the web form, since a key is
  usually for an agent or a script; full is one click away.
- **Brand tokens:** the palette in `docs/brand.md`, tuned for WCAG 2.2 AA:
  deep periwinkle `#5562A3` for primary buttons and links, charcoal text on
  a lightened cream, a darker warm grey `#8C7A72` for input borders (3:1),
  and beak orange only as decoration (it fails as a background for text).
  Dark mode uses a lighter periwinkle on a charcoal background. A test checks
  every pair.

## 2026-11-18: Block operation details (MG-20, reversible defaults)

Picked while building MG-20; easy to change.

- **One endpoint per operation**: list, add, update (PATCH the block's
  `fields`), move and remove under `/v1/emails/{id}/blocks`. Each change
  names the version read, like every other email change; remove takes it as
  `?version=`. Add, update and move answer with the block where it now is and
  the whole email; remove answers with the email.
- **Places:** `after` a block id on its own, or `index` (where the block ends
  up) at the top level, or `parent_id` plus `column` with an optional `index`.
  No place means last, which at the top level is just before the footer.
- **The footer is locked:** it can't be removed, moved or added, and nothing
  goes after it (422 `footer_locked`). Its text can still be updated.
- **Listing an html email's blocks** is a 422 `kind_mismatch`, not an empty
  list, so an agent learns it's on the wrong kind of email.
- **CLI:** `mailgoose email blocks list|add|update|move|remove`. Common fields
  have flags (`--text`, `--level`, `--align`, `--src`, `--alt`, `--href`,
  `--width`, `--color`, `--thickness`, `--height`, `--background-color`,
  `--text-color`); `--data` takes any fields as JSON and flags win over it.
  `--in` and `--column` place a block in a column. `--type columns` without
  `--data` starts with two empty columns. Without `--version` a change applies
  to the email as it is now. Removing a block needs no `--yes`.

## 2026-11-18: Email document details (MG-19, reversible defaults)

Picked while building MG-19; easy to change.

- **Text blocks hold a small Markdown subset** (`**bold**`, `*italic*`,
  `[text](https://...)`, `- ` and `1. ` lists, blank lines between
  paragraphs), not HTML, so agents can write them and the renderer (MG-21)
  decides the markup. Headings are one plain line.
- **Settings live in the document**, next to the blocks (DES-1), for both
  kinds. Fields a request leaves out take defaults: empty subject, preview
  text and From name, no From address or reply-to, background `#f4f4f5`,
  width 600 (480 to 800). From and reply-to are only checked as addresses
  here; the email check (MG-26) says whether From is at a verified domain.
- **Block ids** are optional in requests: a block without one gets `b_` and
  ten random characters. Ids a document brings are kept (1 to 40 letters,
  digits, `-` or `_`, unique in the document), so replacing a document with
  moved blocks keeps their ids. A duplicated email keeps the same ids.
- **The footer** is added at the end of a blocks document that lacks one, and
  a footer anywhere but last is refused. A column can't hold columns or a
  footer. A document holds at most 200 blocks, counting those in columns.
- **Versions:** the API requires the version read on every update and
  replace. The CLI uses the version in a `email get --json` file, or
  `--version`; with neither it reads the current version first, so a bare
  document from a person or agent replaces the email as it is now.
- **HTML emails** take up to 512 KB here; MG-22 adds the 102 KB sendable limit
  and the markup checks. The kind can't change after creation.
- **Links:** image `src` and social URLs must be http or https; button and
  image links may also be `mailto:` or a merge tag such as
  `{{unsubscribe_url}}`. MG-26 warns about plain http.
- **Scopes:** draft keys can do everything with emails.

## 2026-11-18: Contact export and data request details (MG-18, reversible defaults)

Picked while building MG-18; easy to change.

- **Export columns:** email, first_name, last_name, status, tags (names
  separated by commas), source, created_at, status_changed_at and
  permission_confirmed. The consent fields today are status, source,
  status_changed_at and, for imported contacts, whether the import confirmed
  permission (contacts now remember their import). Form consent records come
  with MG-36 and should add their columns here.
- **Export is one streamed CSV** (`GET /v1/contacts/export`, `mailgoose
  export`), oldest first, with the same filters as listing contacts. Held
  contacts are included. Cells starting with `=`, `+`, `-` or `@` get a `'`
  in front so spreadsheets don't run them; imports strip it again.
- **Round trip:** imports now read a `status` column and a `tags` column
  (found under those headers, or named with `--map`). A status can only hold a
  contact back: unsubscribed, bounced and complained rows keep that status,
  unconfirmed rows stay unconfirmed, other values are ignored. Tags the
  account lacks are created; a file may name at most 200 different tags, and
  a tag name over 50 characters refuses the file. Contacts from one import
  keep the file's order.
- **Data request** (`GET /v1/contacts/{id}/data`, `mailgoose contacts
  export-data`): the contact, when each tag was added, the import that added
  it, and import skip rows with its address. Later tickets that store more
  about a contact (sends, events, consent) add it here.
- **Erase** (`DELETE /v1/contacts/{id}?erase=true`, `contacts delete
  --erase`): deletes the contact, blanks its address in import skip reports
  (the row and reason stay), and always leaves a suppression hash: the
  contact's status if it unsubscribed, bounced or complained, otherwise
  unsubscribed, so an old list can't bring the person back. An import still
  waiting to run holds its file until it finishes; the suppression hash makes
  it skip the address.
- **Scopes:** export, data and erase work with draft keys, like listing and
  deleting contacts already do.

## 2026-11-18: CSV import details (MG-17, reversible defaults)

Picked while building MG-17; easy to change.

- **Limits per import:** a file up to 10 MB and 100,000 rows, and at most 20
  tags. Larger files are refused (413 or 422 `file_too_large`,
  `too_many_rows`). The request body limit is raised for this one route only.
- **Imports wait in Postgres, not SQS.** The worker claims queued imports with
  a lease and works in batches of 1,000 rows, so a crash or deploy picks up
  where it stopped; after three stops the import fails. No infra change.
- **Existing contacts are skipped as `exists`** and left as they are, tags
  included. Repeats inside the file are `duplicate`, suppressed addresses are
  `suppressed`, rows that won't parse are `invalid_row`. A quote that never
  closes refuses the whole file, since every later row would be wrong.
- **Over the free limit:** rows past 500 counted contacts are skipped as
  `over_limit` and the import is held. Paid accounts have no cap.
- **"First import"** means no earlier import that didn't fail. A first import
  over 2,000 rows is held. Held contacts are stored but aren't campaign
  recipients until an admin approves them (MG-34). The contacts API doesn't
  show a held flag yet.
- **Columns:** named with `columns` (`--map` in the CLI) or detected from
  common headers; the delimiter (comma, semicolon or tab) is detected from the
  header line and a UTF-8 BOM is ignored. Names over 100 characters are cut,
  not skipped.
- **Tags must exist**, by name or id. A per-row tags column waits for MG-18's
  export round trip.
- **The file is deleted when the import finishes; the skipped-row report is
  kept**, and it holds the raw addresses. MG-18's erase should remove an
  address from it too.

## 2026-11-18: Contact limit details (MG-16, reversible defaults)

Picked while building MG-16; easy to change.

- **Counted contacts are subscribed plus unconfirmed**, as the ticket says.
  Unsubscribed, bounced and complained don't count, so adding an
  unsubscribed contact, or a suppressed address that comes back unsubscribed,
  works at the limit.
- **The limit is checked inside the write, with the account row locked**, so
  concurrent adds can't both take the last place. A free account at 500 gets
  402 `contact_limit`; the error body carries `details.limit` and
  `details.count`. Error bodies may now have an optional `details` object, and
  the CLI passes it through in `--json` errors.
- **Which routes check it:** creating a contact now, and a status change that
  makes a contact counted. Today no API status change can (an unsubscribed
  contact can't be subscribed again through the API), so the first real
  status-change route is form re-subscribe in MG-36. CSV import (MG-17) and
  form signups (MG-36) call the same check and add their own tests.
- **The plan comes from `accounts.plan`**; anything but `free` has no cap.

## 2026-11-18: Tag details (MG-15, reversible defaults)

Picked while building MG-15; easy to change.

- **Tag names are 1 to 50 characters on one line, without commas,** and
  unique per account ignoring case. Commas are kept out so a list of tags can
  be written `a,b` in CSV imports and CLI flags later.
- **Tags must exist before they're put on contacts;** tagging doesn't create
  them.
- **Bulk changes take up to 1,000 contact ids, or a filter** (email, name,
  status, another tag), and answer how many contacts were selected and how
  many changed. An empty filter means every contact; the CLI asks for
  `--all` to say so. Ids from another account are skipped, not refused.
- **Deleting a tag answers `contacts_untagged` and `campaigns_updated`.**
  Campaigns come with MG-27, which must take a deleted tag off draft and
  scheduled campaigns and count them; until then `campaigns_updated` is 0.
- **`listContacts` filters by `tag_id`**, which completes the tag filter
  MG-14 left for this ticket.

## 2026-11-18: Contact details (MG-14, reversible defaults)

Picked while building MG-14; easy to change.

- **A contact added through the API or CLI is subscribed unless the request
  says otherwise** (`unconfirmed` or `unsubscribed`). `source` is `api`, or
  `manual` for the web app; forms and imports set their own.
- **Only Amazon SES events set bounced and complained.** An unsubscribed,
  bounced or complained contact can't be moved back to subscribed or
  unconfirmed by the API, CLI or web app (422 `contact_suppressed`); the
  person subscribes again through a signup form with double opt-in. Anyone
  may unsubscribe a contact. Open for the owner: should an account be able to
  resubscribe someone who unsubscribed, with a consent note?
- **The suppression list keeps a SHA-256 of the account id and the address**,
  not the address. Re-adding a suppressed address, or changing a contact's
  address to one, gives it the suppressed status and removes the entry,
  since the contact now carries the status.
- **Search:** `email` and `name` match any part, ignoring case; `status` is
  exact. Lists are newest first. The tag filter comes with tags in MG-15.
  (Done in MG-15: `tag_id`.)
- **Campaigns pick recipients only through `contacts.Recipients`,** which
  returns subscribed contacts and nothing else.

## 2026-11-18: Mascot and UI direction (owner decision, confirmed)

The owner picked the goose in `docs/brand/mascot.png` as the mascot.

- **The UI's color palette is derived from the mascot.** The starting palette,
  sampled from the image, is in `docs/brand.md`.
- **The interface should be modern and sleek, but simple to use.**
- **Contrast is not settled.** The UI tickets check the palette against WCAG
  2.2 AA when they build design tokens and adjust shades where needed.
- This changes no UI work already merged; the web and editor tickets use
  `docs/brand.md` from here on.

## 2026-11-18: Sending domain details (MG-13, reversible defaults)

Picked while building MG-13; easy to change.

- **The MAIL FROM subdomain is `bounce.<domain>`,** with an MX to
  `feedback-smtp.us-east-2.amazonses.com` and an SPF TXT including
  `amazonses.com`. If its MX goes missing later, SES falls back to its own
  MAIL FROM rather than refusing to send.
- **The DMARC record is only suggested** (`v=DMARC1; p=none;`), and only when
  neither the domain nor, for a subdomain, its organizational domain has one.
  It doesn't count toward verification.
- **Verified means every required record is found and SES reports the
  identity verified for sending.** A record lookup that times out or errors
  counts as missing, with the error in `detail`.
- **The 5-minute re-check runs in the API service, not the worker.** Only the
  API's task role may read SES identities today, so this avoids an infra
  change. Due rows are claimed with `FOR UPDATE SKIP LOCKED`, so several API
  tasks don't check the same domain twice. It can move to the worker once
  the worker role has `ses:GetEmailIdentity`.
- **After 72 hours a pending domain is marked failed.** A manual
  `domain verify` on a failed domain checks again and, if the records are
  now right, verifies it; otherwise it starts another 72 hours. If SES itself
  had given up on DKIM, the identity is made again with new DKIM records, and
  the response says `records_changed: true`.
- **A domain belongs to one account.** Another account can take a name over
  only once the first account's attempt has failed. SES identities are tagged
  `mg_account=<id>` plus the environment tag; one SES already has that isn't
  tagged with this account is refused as `domain_unavailable`.
- **Known limit:** staging and prod share one AWS account, so SES has one
  identity per name across both, and the same customer domain can't be added
  in both environments at once.
- **mailgoose.app and the system sending domain, and their subdomains, can't
  be added.**
- **The From check (`RequireVerifiedFrom`) accepts an address at a verified
  domain or any of its subdomains,** which SES also allows.

## 2026-11-18: CLI sign-up (owner decision, confirmed)

The owner chose to allow sign-up from the CLI, answering the question left
open in MG-07.

- **`mailgoose signup` uses the same emailed one-time code as the web.** Step 1
  (`--email`, `--account-name`, `--accept-terms`) calls `createSignup` and
  emails a code; step 2 (`--email`, `--code`) calls the new
  `POST /v1/cli-tokens` (`createCliToken`), which uses the code like
  `createSession` but answers with a full-scope CLI token instead of a
  cookie. The CLI stores it like `mailgoose login`. The code lands in the
  address's inbox, so a person still has to read it and pass it on; an agent
  can't finish a sign-up alone.
- **Same limits as the web:** code emails are capped at 5 an hour per address
  and 20 an hour per IP (shared with `createSignIn`), a code locks after 5
  wrong tries on either path, 10 wrong codes an hour for one address (across
  codes and both paths) refuse even the right code until the hour passes,
  and the per-IP request limit applies.
- **`--accept-terms` is required** in step 1 and stands for the address
  owner's acceptance of the terms of service and the anti-spam policy, as the
  web checkbox does. (Reversible default.)
- **Both steps take flags and never prompt**, so agents and scripts can run
  them. An address that already has an account gets a sign-in code, and step
  2 then signs the CLI in to that account. (Reversible default.)
- `createSignup` leaves the CLI parity allowlist.

## 2026-11-18: Web app shell details (MG-41, reversible defaults)

Picked while building MG-41; easy to change.

- **The API serves the web app from its own origin.** The image builds
  `web/dist` and sets `WEB_DIR=/srv/web`; every GET or HEAD that no API route
  matches gets the app (index.html for page paths, the file for assets), and
  `/v1`, `/healthz` and `/readyz` stay the API's. One origin means the session
  cookie and the same-origin check need no CORS or `WEB_ORIGINS`, and the CLI's
  `/device` link (built from `PUBLIC_BASE_URL`) lands on the app. A separate
  `app.` host can come later with `WEB_ORIGINS`.
- **Plain React with React Router, no UI kit or data library.** The typed
  openapi-fetch client from MG-02 talks to the API; pages handle errors through
  one helper that shows the API's message and hint.
- **The app's pages:** `/signup`, `/sign-in`, `/device?code=` (the CLI approval
  page, signed in only, outside the app layout) and a signed-in layout with
  Dashboard, Emails, Campaigns, Contacts, Tags, Signup forms, Domains, API keys,
  Credits and Settings. Reports live under each campaign. Signed-out visitors
  go to sign in and come back after (`?next=`, same-site paths only).
- **Static file rules:** hashed files under `/assets` cache for a year;
  index.html is `no-cache`; dotfiles and missing files with an extension are
  404s. The app is served with a Content-Security-Policy that allows only its
  own scripts and refuses framing (`frame-ancestors 'none'`, `X-Frame-Options:
  DENY`), so the approval page can't be clickjacked.
- **CI runs the web checks through `go test`** (`internal/web`): ESLint, the
  TypeScript check, the Vitest tests and a production build, like the OpenAPI
  checks from MG-02. That keeps `.github/workflows` unchanged; a separate web
  job can replace it once a workflow change is due anyway.
- **The sign-up page's terms line has no links yet;** the policy pages come
  with MG-57.

## 2026-11-18: CLI device login details (MG-10, reversible defaults)

Picked while building MG-10; easy to change.

- **User codes are 8 letters, shown as `XXXX-XXXX`,** from 20 consonants, so
  they can't spell words or be misread (no vowels, no I, O, U or Y). They're
  matched without regard to case or the dash. Requests last 10 minutes and
  the CLI polls every 5 seconds; polling faster gets `slow_down` (429), and
  the CLI adds 5 seconds each time. Expired requests are deleted a day later.
- **The token is a full-scope CLI token** (`mg_cli_…`), named after the
  computer (`client_name`, the hostname), and shown once, to the first poll
  after approval. It doesn't expire on its own; `mailgoose logout` or a
  later `login` for the same API URL revokes it.
- **Where the token is kept:** the OS keychain (service `mailgoose`, account
  = API URL), through `zalando/go-keyring`, which needs no cgo. Where there's
  no keychain (a Linux box without a Secret Service, for one), it goes in the
  config file, mode 0600, and `login` says so.
- **Credential order:** `MAILGOOSE_API_KEY`, then the login for the API URL,
  then the config file's `api_key`. AUTH-4 asks for the environment key to win
  over a stored login; putting a login ahead of a saved `api_key` is the
  default picked here, since logging in is the newer, deliberate act.
- **Logout keeps the token if the API can't be reached,** so it can be
  revoked on a retry instead of staying live on the server with no way to
  find it. A token the API already rejects (401) is just removed.
- **Approving and denying need a web session**, not an API key or CLI token,
  so an agent holding a key can't approve its own sign-in. These three
  operations are on the CLI parity allowlist; the approval page is MG-41.

## 2026-11-18: Account settings details (MG-11, reversible defaults)

Picked while building MG-11; easy to change.

- **`PATCH /v1/account` changes only the fields sent**, and `null` removes the
  default From name, From address or postal address. The name and time zone
  can't be removed. In the CLI, an empty flag value (`--from-email ""`)
  removes.
- **The postal address is 1 to 8 non-blank lines, at most 500 characters.**
  Blank lines and spaces at line ends are dropped. It isn't checked against
  any address database. `missing_for_send` in the response lists what a
  campaign can't launch without (today only `postal_address`) for MG-26 and
  MG-30 to use.
- **Time zones are IANA names checked against Go's embedded zone database.**
  `Local`, offsets such as `+05:00` and padded names are refused. New
  accounts start on `UTC`.
- **The From address is only checked for format here.** Whether its domain
  is verified is checked at test send and launch (MG-13), so a person can
  set it before adding the domain.
- **Draft keys can change account settings**, since the draft scope only
  excludes launching, billing and key management.

## 2026-11-18: Credit ledger details (MG-12, reversible defaults)

Picked while building MG-12; easy to change.

- **Grants and expiries are applied when the account is next read or
  debited, not by a scheduled job.** Every credits read or debit first
  catches the account up under its row lock, so the result is the same as a
  job running at each anniversary, with nothing to schedule or monitor.
- **The anniversary is the activation time.** A free account activated on
  Jan 31 at 09:00 gets its grants on Feb 28, Mar 31, Apr 30 and so on, each
  at 09:00 UTC (short months use their last day).
- **No backfill.** An account untouched for three months gets one grant (the
  current month's) when it's next read, not three.
- **Credits come in lots.** Each grant or purchase is a lot with its own
  expiry; debits use the lot that expires first and record which lots they
  took from, so expiring a lot removes exactly what's left of it, and a
  release puts credits back in the lots they came from (latest expiry first).
  Credits released after their lot expired are expired again at once.
- **Paid accounts get no free grant.** What happens to a free account's
  grants when it buys its first pack is MG-39's call.
- **Reading credits is allowed to draft keys**; buying them (MG-39) will need
  a full key.

## 2026-11-18: API key details (MG-09, reversible defaults)

Picked while building MG-09; easy to change.

- **`mg_live_` means production, `mg_test_` means staging and local.** The
  ticket names both prefixes without saying what "test" means; with no test
  mode in the MVP, the prefix shows which environment a key works in.
  CLI tokens start `mg_cli_`.
- **Secrets are 160 random bits, stored as SHA-256**, not Argon2id: a random
  secret that long can't be brute-forced from its hash, and a fast hash keeps
  every request's lookup cheap.
- **50 active API keys per account.** CLI tokens don't count.
- **`last_used_at` is updated at most once a minute per key**, so a busy key
  isn't a write on every request. Keys list it "to the minute".
- **Revoking a key from the CLI needs `--yes`**, since it can't be undone and
  the CLI never prompts.
- **Idempotency-Keys are scoped to the account and the credential** (API key,
  CLI token or signed-in person), not the account alone, so a draft key can
  never replay a response made for a full key, such as a new key's secret.
- **Rate limits stay per credential** (a hash of the `Authorization` header,
  which is per key without a database lookup); web sessions are limited per
  session. Wrong bearer secrets are limited to 30 a minute per IP, after which
  the API answers 429 without looking them up.
- **A draft key gets 403 `insufficient_scope` on every operation marked
  `x-required-scope: full`** in the spec. Today that's the three key
  operations; launch and billing operations get the mark when they're built.
- **Key management needs a full-scope key or a web session**, so the web app
  can manage keys too.

## 2026-11-18: Web sign-in details (MG-08, reversible defaults)

Picked while building MG-08; easy to change.

- **Code-request limits:** 5 an hour per address and 20 an hour per IP,
  counted together for sign-up and sign-in and kept in Postgres so they hold
  across API tasks. Addresses with no account count too, so the limit can't
  be used to tell which addresses have one.
- **Wrong codes are capped per address across codes:** 10 an hour, counted
  for every address whether or not it has an account or a live code. Over
  the cap, even the right code gets 429 with `Retry-After` until the oldest
  wrong one is an hour old. The per-code lock (5 tries) alone would allow 25
  guesses an hour with 5 fresh codes. (Asked for in review.)
- **Code requests for one address take a Postgres advisory lock**, so two at
  the limit can't both pass. The per-IP count can still be off by a few in a
  burst.
- **Sign-in never says whether an address has an account:** the same 202
  either way, and no email to an unknown address. An account that hasn't
  finished signing up gets its sign-up code.
- **Cross-site protection is a same-origin check, not a token.** Browser
  requests that change something and carry no `Authorization` header must come
  from the API's own origin or one in `WEB_ORIGINS` (Go's
  `http.CrossOriginProtection`, which reads `Sec-Fetch-Site` and `Origin`).
  API keys and non-browser clients aren't affected. The public signup-form
  endpoint (MG-36) will need an exception when it lands, since it takes posts
  from any site.
- **Sign-in and the session endpoints have no CLI command** and are in the
  CLI parity allowlist; the CLI signs in with `mailgoose login` (MG-10).

## 2026-11-18: Sign-up details (MG-07, reversible defaults)

Picked while building MG-07; easy to change, and the owner may want to.

- **Codes come from `signin@` the system sending domain**, named "mailgoose",
  with the code in the subject so it shows in a notification.
- **Signing up with an address that already has an active account** gets the
  same 202 and an email with a sign-in code instead of a new account. A
  pending account that signs up again gets a new code and takes the newer
  account name. One email a minute per address at most.
- **Codes are hashed with Argon2id** (OWASP minimum settings, per-code salt).
  A six-digit code has only a million values, so a plain hash would fall to
  brute force if the table leaked.
- **Sign-up and the web session endpoint have no CLI command** and are in
  `api/cli-parity-allowlist.yaml`: signing up accepts the terms on the web
  page, and the CLI gets access through `mailgoose login` (MG-10). The owner
  then chose to allow sign-up from the CLI (see "CLI sign-up", 2026-11-18), so
  `createSignup` now has a command.
- **A wrong code locks after 5 tries** already in MG-07, since the endpoint
  exists now; MG-08 adds the per-address and per-IP limits on requesting codes.

## 2026-11-18: OpenAPI pipeline (MG-02, reversible defaults)

Picked while building MG-02; technical and easy to change.

- **Tools:** Redocly CLI lints the spec (`recommended-strict`, so warnings fail
  too; settings in `api/redocly.yaml`), oapi-codegen generates the Go types and
  a `net/http` server interface, and openapi-typescript plus openapi-fetch make
  the TypeScript client. The contract test validates responses with
  libopenapi-validator, which handles OpenAPI 3.1.
- **CI runs the checks as Go tests**, not as new workflow steps, so this PR
  doesn't touch `.github/workflows` and stays within what the reviewer can
  merge. `internal/api/apigen` tests run the lint and the two freshness checks;
  they install the `web/` npm packages with `npm ci` when needed and fail in CI
  if Node is missing. A later workflow PR (the owner merges those) can move
  them to their own steps.
- **The TypeScript client lives in a minimal `web/` package** (package.json,
  the generated `schema.d.ts` and a small `client.ts`). MG-41 builds the web
  app around it.

## 2026-11-18: API conventions (MG-01, reversible defaults)

Picked while building MG-01; technical and easy to change.

- **Rate limits are counted in memory on each API task**, 600 requests a
  minute per API key (or per IP without credentials) by default
  (`RATE_LIMIT_PER_MINUTE`). With N tasks a key can get up to N times that;
  fine while staging and prod run one or two tasks. Move the buckets to
  Postgres if it needs to be exact.
- **Idempotency keys live in Postgres** (`idempotency_keys`), so every task sees
  them. Until API keys exist (MG-09) they are scoped by a hash of the
  `Authorization` header, and requests without credentials skip idempotency;
  MG-09 switches the scope to the account.

## 2026-11-18: How prod gets DNS (MG-60): default, still open for the owner

A reversible default, picked while writing the prod Terraform. The owner has not
confirmed it.

- **Default:** keep mailgoose.app at GoDaddy and delegate only `api`, `app` and
  `mail` to Route 53, one zone each, the same way `staging` is delegated
  (`infra/prod-dns`, NS records added at GoDaddy by hand).
- **Nothing forces the apex into Route 53.** SES domain verification (Easy DKIM
  CNAMEs), the custom MAIL FROM domain (`bounce.mail`) and DMARC all live under
  `mail.mailgoose.app`; ACM validation CNAMEs live under `api` and `app`. A
  delegation changes only the name it is added at, so the Microsoft 365 records
  at the apex (MX, SPF, autodiscover, verification TXT) are untouched.
- **Checked against live DNS on Nov 18:** no record exists at or under `api`,
  `app`, `mail`, `bounce.mail` or `_dmarc.mail`, there is no wildcard record, and
  there is no CAA record, so the delegations cannot shadow anything and ACM can
  issue.
- **Cost of the default:** three zones instead of one (about $1.50/month), and
  twelve NS records added by hand at GoDaddy. Moving the whole domain would
  instead mean copying every GoDaddy record exactly, including the live mail
  ones, and changing the name servers at the registrar, which puts mail at risk
  if anything is missed.

## 2026-11-18: CI deploy without OIDC (MG-52): what blocks it, and the default

Found by reading AWS's published project policies; **not yet confirmed against
the live account** (needs the owner's AWS sign-in, see the MG-52 PR for the
commands).

- **The denying statement:** `DenyIAMRestrictedActions` in the AWS-managed
  service control policy for the project's plan. It denies `iam:*Provider*`,
  which matches `iam:CreateOpenIDConnectProvider`. The Free Tier policy and the
  Paid Plan policy both carry it, so upgrading to the Paid plan does not lift
  it. It is attached by AWS to the managed organization the project belongs to,
  and AWS documents these policies as not modifiable by the project owner. The
  policy id recorded on Nov 17 (p-6tzxgdae) should be one of those two.
- **Nothing narrower is possible from inside the managed setup.** The only
  documented way to remove the statement is activating advanced features (which
  needs a Paid upgrade first). Activation cannot be reversed, makes the owner
  the administrator of the AWS organization, and removes the spend limit.
  Afterwards the owner can keep the guardrails and exempt only
  `iam:CreateOpenIDConnectProvider` for the GitHub provider.
- **Default until the owner decides (reversible):** keep pushing images by hand
  with `make push-ecr`. The deploy role, the staging deploy workflow and the
  deploy script are merged but dormant: the role is off in Terraform, and the
  workflow skips itself until the repository variable `STAGING_DEPLOY_ROLE_ARN`
  exists. No AWS keys go into GitHub in any case.
- Still open, see below.

## 2026-11-18: No personal names in the repo

Decided by the owner: "as a rule, let's not refer to me ... in the repo. just
gremlin technologies is fine". Docs, code, commit messages, PRs and issues say
"Gremlin Technologies", or "the owner" where a role reads better. Earlier
mentions were replaced.

## 2026-11-18: Open and click tracking source (default)

Picked as a reversible default while writing the tickets, after review of
PR 7. Opens and clicks come only from our own pixel and signed redirects
(MG-37). SES's own open and click tracking is turned off for the configuration
set, because with both on, links would be wrapped twice, opens counted twice,
and the per-campaign off switch wouldn't turn tracking off. SES events are used
for delivery, bounce, complaint, reject and delay only (MG-33).

## 2026-11-18: Reviewer agent merges PRs

Decided by the owner: "i want this to be as autonomous as possible. delegate a
reviewer agent and if it passes their review, allow them to merge it."

- A separate reviewer agent reviews every PR. It merges when CI is green, the PR
  meets its ticket's acceptance criteria, and it follows `CLAUDE.md`. Otherwise
  it sends the PR back with comments. The author never merges its own PR.
- PRs touching `infra/` (Terraform) or `.github/workflows/` (CI and deploy
  credentials) wait for the owner to merge. The owner: "Keep those for me". The
  reviewer still reviews them.

## 2026-11-18: MVP decisions

Decided by the owner ("All defaults" to the ten proposals). Full requirements, with
acceptance criteria, are in `docs/mvp-requirements.md`.

- **Billing ships in the MVP.** Stripe Checkout credit packs, and the $5 account
  fee taken from credits. Yearly card billing comes after the MVP.
- **Web sign-in:** emailed one-time codes, no passwords. The CLI signs in with a
  browser device flow; agents use API keys.
- **One person per account.** No team members in the MVP.
- **HTML emails are edited as code only.** No click-to-edit in the preview.
- **Agent launch confirmation:** `campaign launch` without confirmation returns a
  summary and a one-time code; `campaign launch --confirm <code>` launches after
  the person says yes in the agent's chat.
- **Send queue: SQS** (already in the Terraform), not River on Postgres.
- **One audience per account,** organized with tags. No separate lists.
- **CLI only.** No MCP server in the MVP.
- **Open and click tracking** on by default, with a per-campaign switch off.
- **Tech confirmed:** RDS Postgres, ECS Fargate, SES with SNS events, Terraform,
  one REST API with an OpenAPI spec shared by the web app, CLI and agents, and
  designs stored as JSON blocks rendered server-side.

## 2026-11-17: Domain, DNS, and first apply

Decided by the owner.

- **Domain: mailgoose.app** (registered at GoDaddy).
- **DNS stays at GoDaddy** for the root domain, which carries live Microsoft 365
  email and the website. Only `staging.mailgoose.app` is delegated to Route 53.
- **Staging only for now.** Prod waits until there's an app to run. The staging
  ECS services start at 0 tasks until the first image is in ECR.

Open: how prod gets DNS (move all of mailgoose.app to Route 53, or delegate
`api`, `app` and `mail` individually). See `infra/README.md`, "DNS".

Found during the first apply, not decided:

- **No GitHub OIDC deploys yet.** The AWS project's managed service control
  policy denied `iam:CreateOpenIDConnectProvider` (explicit deny, policy
  p-6tzxgdae), though AWS's published project SCPs don't list it. The OIDC
  provider and the staging deploy role are switched off
  (`create_github_oidc_provider`, `enable_github_deploy`). Reversible default:
  turn them back on once the project allows it (AWS Support, or activating
  advanced features). Open: how CI deploys until then, without storing AWS keys
  in GitHub.
- **AWS plan: Free** ($100 credits, expires 2027-05-18). Everything staging uses
  is on the Free plan's service list.

## 2026-11-17: AWS Region

Decided by the owner.

- **Region: us-east-2.** The Gremlin Technologies AWS project is assigned us-east-2, and the
  project allows Regional resources only there. All Terraform (state bucket,
  backends, provider defaults) now uses us-east-2. Global services such as
  CloudFront, and WAF or CloudWatch Logs tied to them, may use us-east-1 when
  required. Cross-Region features (replication, multi-Region keys, backups to a
  second Region, Route 53 latency/geo/failover routing) are not available.

## 2026-11-17

Decided by the owner in the PRD scoping call and follow-ups.

### Product

- **Fresh start.** An earlier mailgoose repo exists; it is not binding.
- **Surfaces.** Web app and a full CLI, both at launch, at parity.
- **Scope.** Designer, contacts with basic tags, campaign sending, signup forms,
  basic reports. Everything else out of scope at first. Very simple automations
  (such as a welcome email) may come later.
- **Tagging.** Basic tags only.
- **Designer.** Drag-and-drop block editor, plus bring-your-own HTML that gets
  parsed and validated. Imported HTML is edited in a code editor whose preview
  updates live.
- **CLI and blocks.** Designer blocks can be created and edited through the CLI,
  so LLMs can build emails block by block.
- **Signup forms.** We generate embed code; double opt-in on by default.
- **Launching.** Test send and launch from the CLI, with a confirm step. An agent
  may launch a campaign, but only after a quick confirmation from the person in
  the agent's chat window.
- **Reports.** Basic per-campaign numbers only.

### Pricing

- Free tier: 500 contacts and 1,000 credits a month, sending only from a
  verified domain.
- Paid accounts: $5 a month (includes the first 2,500 contacts, then $5 per
  extra 10,000 contacts) plus credits at $20 per 10,000.
- Credits last 24 months from purchase.
- The $5 fee comes out of credits by default; yearly card billing ($60) is the
  alternative.
- The first plan ($15 per 10,000, no account fee) was dropped as not viable once
  fully loaded costs were counted (about $0.70 per 1,000 emails). The chosen plan
  models at about 56% margin at 1,000 paying customers. See `docs/pricing.md`.

### Tech

- **Languages.** Go for backend, workers and CLI. TypeScript and React for the
  web app and editor (chosen over Rust).
- **Hosting.** AWS from day one (chosen over Render).
- **Recommended at the time, confirmed on 2026-11-18** (see that entry):
  - Postgres on RDS
  - ECS Fargate for the API and workers
  - SQS for the send queue
  - SES for sending, with bounce and complaint events through SNS
  - Terraform for infrastructure
  - One REST API with an OpenAPI spec, shared by the editor, CLI and agents
  - Designs stored as JSON blocks, rendered server-side to email-safe HTML

- **One AWS account** for staging and prod. The two are kept apart by name
  prefixes, separate state keys and VPCs, and `Environment` tags that scope each
  environment's SES permissions. Separate accounts can come later if needed.
- **No standby (Multi-AZ) for the production database for now.** Turn it on once
  there are paying customers; it roughly doubles the database cost.

### Process

- Dev plan and ticketing come after the PRD, not yet. (Superseded 2026-11-18:
  MVP tickets are in `docs/mvp-tickets.md`.)
- Cloud Claude writes code on branches and opens PRs; the owner's local Claude
  Code does anything needing the owner's AWS sign-in; the owner merges. See `CLAUDE.md`.
  (Merging changed 2026-11-18: a reviewer agent merges.)

## Open questions

- How does prod get DNS? Default is to delegate only `api`, `app` and `mail`
  (see 2026-11-18); awaiting the owner's confirmation and the GoDaddy step.
  (Blocks MG-60.)
- How does CI deploy without AWS keys in GitHub while the AWS project blocks
  the OIDC role? Activate advanced features, or run a self-hosted runner or
  CodeBuild in the account; default is manual `make push-ecr`. (See
  2026-11-17 and 2026-11-18. Blocks MG-52.)
- Monthly fee: does a partial block of 10,000 contacts round up? (Default: yes,
  2,501 to 12,500 counted contacts is one extra block. MG-40.)
- Anything in the earlier mailgoose repo worth reusing? (Default: no, fresh
  start.)
- Sending domains: can an account hold a domain it never verifies? Today an
  account that adds someone else's domain keeps it by running `domain verify`
  each time it fails, which restarts the 72 hours, and the real owner only
  gets `domain_taken`. (Default: unchanged for now. One option: once a
  domain's first 72 hours have passed unverified, any account may take it
  over, so whoever sets up the DNS wins. MG-13.)
- Deleting an account that still has bought credits: are they refunded or
  forfeited? (Default: forfeited; the ledger keeps them as billing records.
  MG-43, for MG-39 and MG-40 to settle.)
