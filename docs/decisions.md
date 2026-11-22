# Decisions log

Newest first. Each entry says who decided and whether it's confirmed. Anything
marked **recommended** is a proposal the owner hasn't confirmed yet; treat it as
the default until the owner does, but don't build anything hard to undo on it.

## 2026-11-22: Moving from Mailchimp in the agent guide (MG-78; reversible defaults)

The agent guide (`mailgoose docs`, `docs/api/agent-guide.md`) gains a
"Moving from Mailchimp" section, and `llms.txt` lists it under Guides. These
defaults are each easy to change:

- **One section of the existing guide**, not a guide of its own, so
  `mailgoose docs` stays the one thing an agent reads.
- **The person gets the export and connects the account.** The guide tells
  agents to ask for the export's path and to hand the connect link to the
  person, never to sign in to Mailchimp themselves.
- **The checklist after confirming** is the web app's What's next list:
  verify a domain, check and save the draft forms and swap the embed code,
  fix emails with errors or warnings, send a test.
- **`import mailchimp` itself has examples too**, though command groups
  otherwise need none, so every `import mailchimp` command's `--help` has one.

## 2026-11-22: Judging Mailchimp imports in the admin tools and the digest (MG-77; reversible defaults)

Builder's picks for MG-77, open for the owner to change:

- **No new hold rules.** A Mailchimp import is held, or not, by the same
  rules as any import (CON-6, MG-63); its signals only help a person judge
  it.
- **What's kept.** On confirm, the preview's source, audiences with their
  members by Mailchimp status, opt-in count and newest opt-in are kept on the
  upload's row (`mailchimp_bundles.signals`), since the preview itself is
  cleared. An import confirmed before this shows no signals.
- **`mailgoose admin account`** shows them under each held Mailchimp import:
  file or connected account, audiences, members by status, the cleaned and
  unsubscribed shares (of all members), and the share of subscribed members
  with a Mailchimp opt-in time (of subscribed members), with the newest date.
- **The digest lists every Mailchimp import** confirmed in its 24 hours,
  held or not, so a day with one is never quiet during the beta. An upload
  with no contacts (only emails) isn't listed. A held one is listed under
  held imports too.
- **The ceiling** stays `MAILCHIMP_IMPORT_MAX_CONTACTS` (250,000), refused
  at upload or fetch with `too_many_members`; the message now says to
  contact support to have it raised.

## 2026-11-22: Draft signup forms from a Mailchimp import (MG-76; reversible defaults)

Mailchimp's embedded form code posts to Mailchimp, so confirming an import now
gives each audience with members a mailgoose signup form in its name: email,
first name and last name, double opt-in on, and the `audience: <name>` tag
when the export has more than one audience. The bundle lists them in `forms`;
the web app's import page links them, and the CLI prints their ids (and
`--json` gives the bundle as the API does). These defaults are each easy to
change:

- **Drafts until saved.** A new form is a draft (`draft: true` on the form):
  it takes no signups (its public endpoint answers 403 `form_draft`) and its
  problems list says so, until the customer saves it. Any save does it: the
  web editor's button reads "Save and take signups" and works with nothing
  changed, `PATCH /v1/forms/{id}` with `{}` works, and the CLI has
  `mailgoose form update <id> --live`.
- **Group tags, picked once.** The preview lists each audience's groups as
  the tags they become (`audiences[].groups`); confirming with `form_tags`
  (web: tick boxes; CLI: `--form-tag`) puts each picked group on the form of
  every audience that has it. None are picked by default. A tag that isn't
  one of the audiences' groups is refused (422 `unknown_form_tag`).
- **Mailchimp's double opt-in is shown, not copied.** A bundle fetched over
  OAuth shows each audience's own Mailchimp setting (`double_optin`) beside
  its form; mailgoose's form has double opt-in on either way. An uploaded
  export doesn't carry the setting, so nothing is shown.
- **One form per audience, by name.** A form remembers the audience it was
  made for, matched ignoring case, so importing again finds that form and
  leaves it as it is (`new: false`), saved or renamed or not. A deleted form
  is made again on the next import. An audience with no members gets no form.
- **Success message and button** are the usual defaults; nothing from the
  Mailchimp form's own wording comes over, since neither export nor API
  gives it.

## 2026-11-22: The web app's Mailchimp import page (MG-75; reversible defaults)

`/contacts/import/mailchimp`, linked from the contacts import page and the
getting-started checklist, takes a customer from a Mailchimp export or a
connected account to an import: upload or fetch, the preview in plain words,
confirm, progress, and a list of what to do next. These defaults are each easy
to change:

- **Connecting shows only where it's set up.** `getMailchimpConnection` now
  says `can_connect`, so the page leaves the connect option out where
  `connectMailchimp` would answer `mailchimp_not_configured`, without trying
  it. The CLI says the same and points to the upload.
- **Back from Mailchimp.** The page Mailchimp's sign-in returns to now links
  back to the import page and names the CLI command, so the customer carries
  on where they started.
- **Segments by id.** A fetch's saved segments are typed as Mailchimp's
  segment ids, as in the CLI; mailgoose has no endpoint that lists an
  audience's segments yet.
- **Campaigns.** Confirming defaults to the number of campaigns the upload
  was read with (10), at most the export's campaigns, and can be 1 to 100
  (the API takes no fewer than 1).
- **Permission.** The box reads as the CSV import's first sentence ("Everyone
  in this file has given me permission to email them."); a Mailchimp import
  can't go ahead without it, so there's no unconfirmed fallback to explain.
- **Skipped rows as CSV.** `GET /v1/imports/{id}/skipped-rows/export` (and
  `mailgoose import skipped <id>`) gives every skipped row in one file, with
  the reason in a sentence; the import report links to it, for CSV imports
  too.
- **What's next.** Verify the sending domain, set up signup forms and swap
  the embed code (MG-76's draft forms are listed above it),
  fix each email with errors, and send a test. The steps aren't ticked off.
- **Hold wording.** The CLI's and the page's reasons for a held import now use
  the hold codes the server records (`large_first_import`,
  `invalid_addresses`, `risky_addresses`, `shared_list`,
  `over_contact_limit`); the CLI had older names for three of them.

## 2026-11-22: Fetching from a connected Mailchimp account (MG-74; reversible defaults)

With a connected account, `GET /v1/imports/mailchimp/audiences` lists its
audiences with Mailchimp's member counts and double opt-in setting, and
`POST /v1/imports/mailchimp/fetch` reads the chosen audiences, saved segments
and recent campaigns into a bundle like an uploaded export's, so preview,
confirm and the import are the same. `GET .../fetch/{id}` shows its progress
and, when done, the bundle and its preview. The CLI has
`mailgoose import mailchimp audiences|fetch|fetch-status`; `fetch` waits and
shows the preview unless `--no-wait`. These defaults are each easy to change:

- **Where it runs.** The fetch runs in the API task, not the worker, because
  only the API task holds the Mailchimp app's secret that unseals the token.
  It's a leased job like the email job: a fetch whose task stopped starts
  again from the beginning, up to 3 times.
- **One at a time.** An account has one fetch queued or running at a time
  (409 `fetch_in_progress`), and a fetched bundle counts toward the 3
  unconfirmed bundles an account may keep. Up to 20 audiences and 20 saved
  segments per fetch; campaigns 0 to 100, default 10. A finished fetch is
  kept 7 days.
- **Same as the export.** The fetch writes the files Mailchimp's own export
  has (each status's members CSV per audience, campaign HTML and text,
  `campaigns.csv`) and reads them with the upload's reader, so the upload's
  limits (the import's member cap, 25 MB zipped) and rules apply. Members of
  every status are read 1,000 at a time. Merge fields other than first and
  last name are dropped and listed, as for an upload; interest categories
  become group columns; opt-in and confirm times and IPs and GDPR
  permissions come over as the export has them.
- **Tags.** A member's tags are the ones the members endpoint returns, which
  is at most 50 per member. Each chosen saved segment adds a tag
  `segment: <name>` to the members it has at the time of the fetch; it
  doesn't follow later changes, and the preview says so.
- **Templates aren't fetched.** Mailchimp's API gives a user template's name
  but not its HTML, so a fetch counts the account's user templates
  (`templates_not_fetched` in the preview) and points to the account export
  for them. Campaign content is fetched.
- **Mailchimp's limits.** The client keeps to Mailchimp's 10 simultaneous
  connections per account, retries 429 and 5xx up to 5 times with backoff
  (honouring `Retry-After`, at most a minute), and stops on a 401: the
  fetch fails with `mailchimp_access_revoked` and the connection is
  forgotten.
- **The token.** Confirming a fetched bundle forgets the connection at once,
  so the token goes before the import finishes; otherwise it goes after 24
  hours, as MG-73 set, now deleted as soon as anything looks for it and by a
  sweep each minute rather than by the hourly prune. The token exchange with
  Mailchimp's login server follows no redirects, so the client secret can't
  be carried on to another address.

## 2026-11-21: Connecting a Mailchimp account with OAuth (MG-73; reversible defaults)

Customers can connect their Mailchimp account instead of uploading the
export. `POST /v1/imports/mailchimp/connect` answers with a link to
Mailchimp's sign-in page; after they allow access, Mailchimp sends the browser
back to `/oauth/mailchimp/callback`, which stores the connection.
`GET /v1/imports/mailchimp/connection` and `POST .../disconnect` show and
forget it. The CLI has `mailgoose import mailchimp connect|connection|disconnect`.
MG-74 reads the account with it. These defaults are each easy to change:

- **Set up by app secrets.** Connecting needs Gremlin Technologies' Mailchimp
  app: `MAILCHIMP_CLIENT_ID` and `MAILCHIMP_CLIENT_SECRET` in the
  environment's app secret, with the callback above registered with
  Mailchimp. Without them the app runs as before, and `connect` answers 503
  `mailchimp_not_configured`, pointing to the upload.
- **The link.** A connect link works once, for 10 minutes. A new link
  replaces the account's earlier one.
- **Who can finish it.** The callback needs the browser's web session for
  the account that made the link, so a link sent to someone else can't tie
  their Mailchimp to the sender's account. A CLI user signs in to the web app
  in the browser that opens the link.
- **The token.** Mailchimp's tokens don't expire and have no scopes, so
  mailgoose keeps one for at most 24 hours, deletes it on disconnect and once
  the import that read it is done (MG-74), and never shows it in the API,
  logs or errors. It's sealed in Postgres with AES-256-GCM under a key derived
  from the client secret; changing the secret only means connecting again.
- **Read only.** The Mailchimp client makes GET requests only; anything else
  is refused before it leaves the process.
- **Web app.** The web app's Mailchimp page, with a Connect button, comes in
  MG-75. Until then the callback's own page says what happened.

## 2026-11-21: Uploading and confirming a Mailchimp export (MG-72; reversible defaults)

`POST /v1/imports/mailchimp/bundles` takes Mailchimp's export as a
multipart upload (part `file`, with `audience_name` and repeated
`group_columns`), reads it (MG-69) and answers with an upload id and its
preview: audiences by status, the contacts and tags it would make (MG-70),
the emails it would make (MG-71) and the plan it needs. Nothing is imported
until `POST .../{id}/confirm` with `permission_confirmed: true`, which queues
the contacts import (kind `mailchimp`) and the emails. The CLI has
`mailgoose import mailchimp upload|preview|confirm|delete`. These defaults
are each easy to change:

- **Size and memory.** An upload is at most 25 MB; a larger account exports
  each audience on its own. An API task takes in at most two uploads at
  once (a third waits 20 seconds, then gets 503 `uploads_busy`), and reads,
  previews or stores one export at a time, a slot the email job and confirm
  take too; the job keeps only the emails once it has read the file. An
  upload may take 5 minutes to arrive, past the API's usual 30-second read
  timeout. Measured: a 24 MB audience CSV of 222,000 contacts (near the
  250,000 cap) holds about 235 MB live while it's read and previewed, and
  the heap peaked at about 310 MB. The server now sets Go's soft memory
  limit to 80% of the container's (unless `GOMEMLIMIT` is set), so the heap
  stays near what's live; on a 512 MB API task that's about 410 MB.
- **Per account.** An account sends one upload at a time on a task (409
  `upload_in_progress`) and keeps at most 3 unconfirmed uploads (409
  `too_many_uploads`); `DELETE /v1/imports/mailchimp/bundles/{id}`
  (`mailgoose import mailchimp delete`) frees one, and so does confirming.
  So an account holds at most 75 MB of unconfirmed files.
- **Storage.** The file is kept in Postgres (`mailchimp_bundles`), like a CSV
  import's file, with the preview worked out once on upload; `GET` returns
  that stored preview, against the account as it was then. An unconfirmed
  upload is deleted after 7 days; a confirmed one keeps its file until its
  emails are made.
- **Where the emails are made.** A job in the API, not the worker, since only
  the API's task may store images. It carries on where it stopped after a
  restart, and gives up after three tries with an error on the upload.
  Without image storage the emails are still made, keeping their Mailchimp
  image links with a warning each. The job renews its claim every minute
  while it works, and stops if another task has taken it. `MaxImportImages`
  counts per run, so an import that's retried fetches at most three times
  that.
- **An export with no contacts** (only templates or campaigns) can be
  confirmed; it makes emails and no contacts import.
- **Confirming again** answers with the same upload and import. The import's
  idempotency key is the export's own hash unless the request gives one.
- **Import status.** `GET /v1/imports/{id}` gives a Mailchimp import's
  `kind`, its existing-contact counts and `mailchimp` (the upload id and the
  emails made). The status `waiting` and the skip reasons `not_subscribed` and
  `archived` are new in the API, CLI and web app.
- **Stored rows.** A confirm whose contacts would take over 256 MB to store
  is refused (`export_too_large`), well under Postgres's 1 GB for one value;
  a 25 MB zip can unpack to much more. The customer exports the audiences
  one at a time.
- **Templates past the first 100** (MG-71's cap) are listed in the preview's
  `ignored_files` with the reason `too_many_templates`.
- **Waits that won't end.** A waiting import goes on only once the account is
  on the paid plan and isn't a tester, so a tester's never does, and deleting
  contacts on the free plan doesn't restart one. `waiting_note` on the
  import says so, and `POST /v1/imports/{id}/cancel` (`mailgoose import
  cancel`, and a button on the import's page) cancels a waiting import, to
  the new status `cancelled`. Only a waiting import can be cancelled (409
  `import_not_waiting`). What it already imported (its unsubscribed and
  bounced contacts) stays. A cancelled import, like a failed one, doesn't
  count toward the large-first-import check, and its idempotency key can be
  used again.
- **Closing an account** deletes its uploads with the rest of its data.

## 2026-11-21: Reports and segments (M12, MG-111 to MG-116): opens are a normal number (owner, confirmed); three defaults (recommended)

The owner asked for per-campaign reports of opens, clicks and bounces, and
for segments on engagement (did open or click a given email, never opened,
never clicked), built through the usual ticket queue after the Mailchimp
import. The tickets are issues #250 to #255.

- **Opens are shown as a normal number** (the owner, confirmed). Opens come
  from a tracking pixel, and some mail apps load it without anyone reading
  the email (Apple Mail Privacy Protection, some security scanners), so open
  rates run high. The owner's words: "let's de-emphasize the estimate part
  of it." So opens show as a plain count and share, with no "estimated"
  label, everywhere: the campaign list, the report page, `mailgoose campaign
  report`, filters and segments. The reason they run high goes in a quiet
  help line or tooltip at most. Machine opens aren't detected or left out:
  "opened" filters count every recorded open. Clicks remain the reliable
  signal. This replaces the "(estimated)" label from MG-38 and MG-48 (the
  report page entry of 2026-11-19) and REP-1's "labeled as estimated"; MG-111
  makes the change on the report page and in the CLI.
- **How far back "never" looks** (recommended): as far as raw events are kept
  (12 months), over campaigns sent with tracking on that the contact got. A
  contact who got none of those isn't "never opened".
- **Combining conditions** (recommended): every condition must match (AND);
  no OR or nesting at first.
- **When a segment is worked out** (recommended): at launch, or at the send
  time of a scheduled campaign, then fixed, as tag audiences are today.

## 2026-11-21: Social icons (MG-109; reversible defaults)

Asked for by the owner: social links that look like the big builders',
with the networks' logos instead of plain text links. This replaces MG-21's
"social links render as text".

- **The icons are ours to serve.** PNGs drawn from Font Awesome Free's
  brand glyphs (CC BY 4.0, attribution in `internal/socialicons/README.md`),
  96 pixels so they're sharp at twice their size, committed and served by
  the API at `PUBLIC_BASE_URL/assets/social/v1/<color>/<network>.png`,
  cached for a year. A redraw goes in a new version folder, so sent emails
  keep their icons. No third-party CDN. Without `PUBLIC_BASE_URL` the
  renderer falls back to text links.
- **Fields.** `display` (`icons`, `icons_and_text`, `text`; default
  icons), `color` (`brand`, `dark`, `light`; default brand), `size` (32, 40
  or 48 pixels; default 32), `spacing` (0 to 48; default 12), `layout`
  (`horizontal`, wrapping on phones, or `vertical`) and an optional
  `label` per link (shown with names and used as alt text; default the
  network's name). Blocks saved before read with these defaults.
- **Narrower than the issue first proposed, for the networks' brand
  rules.** Read on each network's own brand page (Nov 21): LinkedIn forbids
  putting its logo in a circle, Bluesky forbids combining its logo with other
  design elements, Facebook's logo is already a circle and allows only its
  blue or white, X only black or white, and only Instagram allows gray. So
  there's no `icon_style` (no circles or outlines drawn around logos), no
  gray, `dark` keeps Facebook blue, and the smallest size is 32 pixels
  (Instagram asks for at least 29). Sizes are pixel numbers rather than
  small/medium/large.
- **Open question for the owner: TikTok, YouTube and Threads.** Their
  brand pages didn't load or don't say whether a logo may link to a profile
  in an email; TikTok's developer pages restrict logo use. The icons ship
  for all three (it's how every email builder shows them), but if the owner
  wants to be strict, they can fall back to text for those three.
- **Open question for the owner: circles, outlines and gray.** The owner
  asked for something like Mailchimp's, which offers circles, outlines and
  gray icons. Offer those for the networks whose brand rules allow them?
  (Default: no; logos only, in brand, dark or white, at 32 to 48 pixels.
  MG-109.)
- **Outlook on Windows** gets a table around the icons (it ignores inline
  blocks), so it doesn't wrap; everywhere else they wrap on narrow screens.

## 2026-11-21: Column layouts (MG-110; reversible defaults)

Asked for by the owner: drop in column layouts the way other builders do,
not only a two-column row. This widens DES-2's "two-column row" to these
layouts, and DES-2 in `docs/mvp-requirements.md` now says so.

- **Layouts.** A columns block has a `layout`: `1`, `1:1`, `1:1:1`,
  `1:1:1:1`, `1:2`, `2:1`, `1:3` or `3:1`, each number a column's share
  of the width. A block without one reads as `1:1`, so every saved email
  keeps working. Columns still can't hold columns.
- **Adding one.** Given a layout and no columns, the API makes that many
  empty columns. The designer's palette has a Columns section with a tile
  per layout; the CLI takes `--type columns --layout 1:2`.
- **Changing the layout** (`fields.layout` on update, a picker in the
  designer's properties, `--layout` in the CLI) adds empty columns on the
  right when going to more, and drops right-hand columns only when they are
  empty. If one still holds blocks the change is refused with a message
  saying which column; nothing is ever dropped silently.
- **Rendering.** Columns are table cells with pixel widths split by the
  layout's ratio (the last column takes any rounding remainder), so Outlook
  gets fixed widths, and they still stack full width on phones.
- **Numbering.** `column` positions count from 0, the leftmost, up to 3.

## 2026-11-21: Turning Mailchimp templates and campaigns into HTML emails (MG-71; reversible defaults)

`mailchimp.PlanEmails` lists the emails an export would make, with each
one's errors and warnings, and fetches and writes nothing.
`mailchimp.EmailImporter.Import` makes them, copying Mailchimp-hosted images
into the account's image storage. The API and CLI come in MG-72. Each email
is made even when its checks fail; only a failure to store one stops the
import. These defaults are each easy to change:

- **Which emails.** The first 100 templates in file order, and the newest 10
  campaigns (the customer can choose up to 100), newest first by send date,
  then those with no date in reverse file order. `TemplatesLeftOut` lists
  the templates past 100, so the preview can name them; the cap keeps one
  export from making an unbounded number of emails. A campaign is named by its title, else
  its subject, else its file name; a template by its file name.
- **Merge tags.** `*|FNAME|*`, `*|LNAME|*`, `*|EMAIL|*` and `*|UNSUB|*`
  become `{{first_name}}`, `{{last_name}}`, `{{email}}` and
  `{{unsubscribe_url}}`. `*|MC:SUBJECT|*` and `*|MC_PREVIEW_TEXT|*` become
  the subject and preview text. The postal address tags are taken out
  quietly, since mailgoose adds the account's address. Any other tag is
  taken out with a warning (`mailchimp_tag_removed`), once per tag.
- **Conditional blocks** (`*|IF:...|*`, `*|INTERESTED:...|*`) are left as
  written with one error (`mailchimp_conditional`) at the first, since
  mailgoose has none and only the customer knows which part to keep.
- **The email checks now refuse Mailchimp merge tags.** Any `*|...|*` left in
  an HTML email is a new error, `mailchimp_merge_tag`, so a converted email
  can't go out with tags readers would see as written. This applies to all
  HTML emails, saved by any route.
- **Mailchimp editor attributes** (`mc:edit`, `mc:repeatable`,
  `mc:hideable`) are removed.
- **Images.** Images on `mcusercontent.com` (and its subdomains) and
  `gallery.mailchimp.com`, in `src`, `background` and CSS `url()`, are
  fetched over https only, from public addresses, at most 5 MB, and relinked.
  Each is fetched once per import, and one import fetches at most 1,000
  (the account limit). None is fetched for an email too large to keep,
  once the account reaches its 1,000-image limit, or where image storage
  isn't set up. One that isn't copied keeps its Mailchimp link with a
  warning (`image_not_copied`) that says why; an email gets at most 10 of
  these for single images, then one for each reason that counts the rest.
  Images on other hosts are left alone.
- **Empty links.** A removed merge tag that was a link's whole address
  (`*|ARCHIVE|*`, `*|UPDATE_PROFILE|*`, `*|FORWARD|*`) leaves `href=""`; its
  warning says the link was left empty.
- **Tracking links** through `list-manage.com` are kept, with one warning
  that counts them (`mailchimp_tracking_link`), since their target can't be
  read without following them.
- **Too large.** HTML over the 500 KB an email holds makes an empty email
  with a `too_large` error. Invalid UTF-8 is replaced, and subjects and
  preview text are cut to 200 characters on one line.

## 2026-11-21: Staging sends at 94% of the configured rate, so the send pool isn't built (MG-91 part 2; decided by the ticket's threshold)

The owner measured send throughput on staging with part 1's runbook (see
"Send throughput is measured per batch" below), with the SES mailbox
simulator only, on 2026-11-21 from 20:44:49 to 20:46:16 UTC, at commit
c5f66ca. `SEND_RATE_PER_SECOND` was 10; SES's `MaxSendRate` was 14 with
production access on, so SES didn't cap the run. Two campaigns of 200 copies
each went to the tag `throughput-test`: 400 copies in four batches of 100.

- **Overall:** 400 copies over 42.5 seconds of sending, 9.41 a second, which
  is 94% of the rate. No batches overlapped.
- **Per batch:** 8.41, 9.59, 9.87 and 9.96 a second. The slowest was 84%;
  none was under 80%. The first campaign's first batch was the slowest,
  perhaps warm-up; one run can't show.
- **Per campaign, end to end:** 8.95 a second (22.3 seconds) and 9.90 a
  second (20.2 seconds). A campaign's two batches were about 0.03 seconds
  apart; launch to the first batch took about 2.2 seconds (the dispatcher's
  tick and the queue receive).
- **Outcome:** both campaigns reached `sent` with 200 delivered each, and no
  skipped or failed copies, bounces, complaints, SES throttles or
  `retry_later`.

**Decision:** the pool (step 2) was to be built only if the measured rate
was well under `SEND_RATE_PER_SECOND`, which the runbook put at under 80%.
It's at 94%, so the pool isn't built and MG-91 is closed: one copy at a
time keeps up with the pace limit. Measure again (the tag and its 200
simulator contacts are kept on staging) if `SEND_RATE_PER_SECOND` goes up,
the send path gains round trips, or the Throughput line in `mailgoose admin
deliverability` falls well under the rate.

The same run showed that the deliverability report's "OVER WARNING: ..."
line read like an alert when no account was over the early warning; it was
the legend. It now says how many of the accounts shown are over it, and says
so when none are.

## 2026-11-21: Turning a Mailchimp export into contacts (MG-70; reversible defaults)

`contacts.PreviewMailchimpImport` says what importing a bundle (MG-69) would
do and writes nothing. `contacts.CreateMailchimpImport`, with the permission
box ticked, queues it as an import of kind `mailchimp`, which the import
worker runs like a CSV import: in batches of 1,000, resumable after a stop,
with the same holds and skip report. The API and CLI come in MG-72. This uses
the open Mailchimp decisions' defaults (1, 2, 3, 4, 6 and 7, below) and adds
these, each easy to change:

- **Statuses.** subscribed stays subscribed, with no confirmation email.
  pending becomes unconfirmed, with no email sent. unsubscribed stays
  unsubscribed, and cleaned becomes bounced. A member whose GDPR permissions
  refuse email marketing comes in unsubscribed. Non-subscribed
  (transactional) and archived members are skipped (`not_subscribed`,
  `archived`).
- **One row per address.** An address in several audiences takes the
  strictest status: cleaned, then unsubscribed, then subscribed, then
  pending. Its tags are every audience's tags, its groups as
  `Category: Group`, and `audience: <name>` for each audience when the
  export has more than one. Names come from the first member that has them.
  A tag name with a comma or line break, or longer than 50 characters, is
  renamed and listed in the preview. More than 1,000 different tags refuses
  the export (`too_many_tags`).
- **Contacts already here** only gain tags. One that's subscribed or
  unconfirmed is unsubscribed or bounced when Mailchimp says so; one that
  unsubscribed, bounced or complained is never put back on the list. An
  address on the suppression list (a deleted contact) is skipped
  (`suppressed`).
- **Consent.** Each new contact's consent record has list source `export`
  ("Mailchimp"), the file name and, for up to 20 of its audiences, its
  Mailchimp status, opt-in and confirm times and IPs, and GDPR permissions,
  as written.
- **Over the limit.** Suppressions import first. When the subscribed part
  would take a capped account (free, or a tester) past its contact limit, the
  import stops with status `waiting`, held for review as
  `over_contact_limit`, and nothing of that part imported, rather than the
  first 500. It carries on by itself once the account is on the paid plan and
  isn't a tester. Its contacts then stay held until an admin approves, like
  a CSV import over the limit. The preview shows the plan needed and its
  price.
- **Holds.** The list checks (MG-63) look at the addresses that would get
  campaigns and at the rows that aren't addresses. A first import of more
  than 2,000 such addresses is held, as for a CSV.
- **Confirming twice.** An idempotency key, by default the hash of the
  merged rows, makes a second confirm return the import already made. The
  same key for a different export is refused (`idempotency_key_reused`), and
  a failed import frees its key.
- **Skip report.** Skipped rows are listed with the Mailchimp file and row
  they came from. Rows the reader skipped past its first 1,000 per audience
  are counted as `invalid_row` but not listed.
- **Speed.** 100,000 members import in about a minute locally (without
  `-race`), well under the ticket's five minutes. The test runs only with
  `MAILGOOSE_SLOW_TESTS=1`.

## 2026-11-21: Reading Mailchimp's exports (MG-69; reversible defaults)

Picked while building the reader, `internal/mailchimp`. Mailchimp's public
help pages name the columns and kinds of file in its exports, but not exact
file names, how tags and groups are separated, or timestamp layouts, so these
are our reading of them. Each is easy to change, and the synthetic fixtures
should be checked against a real export when one is available.

- **What it reads.** The account export ZIP, an audience export ZIP or one
  audience CSV. An account export's reports, gallery, ecommerce, SMS, events
  and appointments folders are ignored and listed, never read.
- **Status from the file's name.** A member file's status comes from the
  word in its name (subscribed, unsubscribed, cleaned, nonsubscribed or
  transactional, archived, pending), or from a status column when it has
  one. A single CSV whose name and columns don't say is refused
  (`unknown_status`), rather than guessed as subscribed.
- **Audience names.** An account export's audience is the folder its files
  are in. An audience export or a single CSV doesn't name its audience, so
  the caller names it (default "Mailchimp").
- **Groups are named, not guessed.** A CSV doesn't say which columns are
  group categories and which are merge fields, so the reader reads as groups
  only the columns the caller names; any other column is dropped and listed
  (decision 6). MG-72 lets the customer name them.
- **Tags and groups** are split on commas, each name optionally in double
  quotes so it can hold a comma.
- **GDPR permissions.** A column named for marketing permissions, or
  starting "GDPR", lists the permissions a member granted. A member refuses
  email marketing only when that column has a value with no email
  permission in it; a blank cell is no information, not a refusal.
- **Timestamps** are read in several common layouts as UTC; one in a layout
  the reader doesn't know is kept as written, unparsed.
- **Size.** The 1 GB limit counts the bytes of the files the reader uses, so
  a large gallery folder doesn't refuse an export. A template or campaign
  file over 5 MB is listed as too large rather than read.
  `MAILCHIMP_IMPORT_MAX_CONTACTS` (default 250,000) caps members across the
  whole export (decision 4).
- **Other limits.** A member file with more than 1,000 columns, or an
  audience whose files have more than 1,000 different columns between them,
  is refused (`too_many_columns`); Mailchimp allows about 80 merge fields.
  Only the first 1,000 skipped rows of an audience are listed, with every
  skipped row counted by reason. A tags, group or permission cell keeps at
  most 500 names, and the member is marked when some were left out.

## 2026-11-21: A no-domain account's missing From is its sign-in address (MG-108, owner decision; the details are reversible defaults)

From the MG-85 staging check: with `SHARED_SENDING_DOMAIN` set, an account
with no verified domain could already send from the shared domain with its
owner's sign-in address as the From, but an email with no From, on an
account with no default From, was refused with `missing_from` and a hint
that didn't mention it. The owner said yes on Nov 21: there, a missing From
defaults to the owner's sign-in address. The mail goes from `<sending
slug>@<shared domain>` under the account's name (MG-98), with the sign-in
address as Reply-To, exactly as when that address is set.

- **One place.** `senders.Resolver` applies it (`DefaultFrom`, and
  `Resolve` with an empty From), so test sends, launches, the email check,
  form confirmation emails and a form's `no_verified_from` problem agree.
  Shared sending off, or an account with a verified domain: no default, and
  a missing From is still `missing_from` (and still stops a double opt-in
  form).
- **Worked out when the From is read, not stored.** `from_email` stays null
  on the account and the email, so adding a domain or turning shared
  sending off takes effect at the next read. A launch
  keeps the From it was confirmed with, as for any From.
- **Said.** The test-send answer and the launch summary gain `default_from`
  (true when the default was used; `from`, `reply_to` and `sender` already
  name the shared address and the sign-in address). The account answer
  gains `default_from_email`: the sign-in address when the default applies,
  null otherwise. The CLI's `campaign test` and launch summary print "No
  From is set on the email or the account, so it goes as your sign-in
  address, ..." with how to set one, above the shared-domain nudge, and
  `account get` shows the empty From as going as the sign-in address. The
  email check's `shared_domain` warning starts with the same sentence.
- **Web app.** The Settings page's empty Default From address shows the
  sign-in address as its placeholder, with a note under it; the test result
  and the launch summary's shared-domain note say no From is set, linking
  to Settings. The designer's own From field is unchanged.
- **Unchanged:** an explicit From or Reply-To is resolved and checked as
  before (MG-85); the email's own From name is still never used on the
  shared domain; no migration.

## 2026-11-21: Launch settings in Terraform, two SES alarms and a stricter system DMARC (MG-93, from the architecture review; details are reversible defaults)

The settings prod changes at launch had no Terraform variable, so launch day
would have meant a rushed `infra/` change. Nothing alarmed on SES sends
nearing the platform cap (MG-87), or on SES saying "not now" for a long time,
which is how a paused or suspended SES account shows since MG-87.

- **`app_settings`** (`map(string)`) on `modules/stack`, set as environment
  variables on the API and worker. Each root sets `NEW_ACCOUNTS_TEST_MODE`,
  `SIGNUP_REVIEW`, `ABUSE_EMAIL`, `SEND_RATE_PER_SECOND`,
  `ACCOUNT_DAILY_SEND_LIMIT` and `PLATFORM_DAILY_SEND_LIMIT` explicitly, at
  the app's defaults (test mode `on`, review `all`, `abuse@` the system
  sending domain, 10 a second, 10,000 and 45,000). Prod's comments mark what
  changes at launch: test mode `off`; review stays `all` for the launch
  month, then `flagged`. Those launch values are still the owner's to set.
- **Plain settings only (reversible default):** the map can't set a name
  Terraform already sets (the database, queues, SES, URLs,
  `OPS_ALERT_EMAIL`, images, `APP_SECRETS_JSON`), and names with `SECRET`,
  `PASSWORD`, `TOKEN` or ending in `_KEY` are refused, since secrets go in
  `<env>/app`. Terraform's own values win anyway.
- **SES sends alarm** (`<env>-ses-daily-sends`): SES's account-wide `Send`
  count summed over CloudWatch's one-day period, at 80% of
  `PLATFORM_DAILY_SEND_LIMIT` (36,000), or of SES's quota
  (`ses_daily_quota`, 50,000) when the cap is 0. It's on the environment
  with `ses_reputation_alarms` (staging until prod runs). It counts all of
  SES's mail, so it can fire a little early, which is the safe side.
  CloudWatch's day may not line up with SES's rolling 24 hours, so it's an
  early warning; the cap is the hard stop.
- **Retry-later alarm** (`<env>-ses-retry-later`, each environment): a log
  metric filter on the worker's WARN line "the mail provider asked to retry
  later; the batch's unsent copies wait", alarming when lines show up in
  every 5 minutes for 30 minutes. The filter matches the message exactly, so
  a change to that line in `internal/sending/pipeline.go` needs the same
  change in `modules/monitoring`.
- **DMARC:** `dmarc_policy` on the stack sets the system sending domain's
  policy. Prod's `mail.mailgoose.app` is `quarantine`, since it carries only
  mailgoose's own mail; staging stays `none`. The shared sending domain has
  its own policy, `none`, until its reports are clean.
- **Cost:** two more alarms on staging (daily sends and retry later), one
  on prod while its reputation alarms are off, and one log metric in each,
  about $0.10 a month apiece past CloudWatch's free tier.
- **Not here:** reading SES's real quota from `GetAccount` instead of a
  configured number (app code), and the prod deploy route, which belongs
  with the account split (MG-99).

## 2026-11-21: Terraform keeps old ECS task definition revisions (MG-102; reversible default)

An apply that changed the API or worker task definition replaced it and
deregistered the old revision (the staging API's `:5` went this way during
the MG-85 apply), which removed the revision a rollback would point at.

- **`skip_destroy = true`** on `aws_ecs_task_definition.this`
  (`infra/modules/service`), for both services in both environments. On a
  replace, Terraform registers the new revision and only forgets the old one,
  which stays registered. Inactive revisions cost nothing.
- **Rollback** is pointing a service at an earlier revision by hand, or
  redeploying an older image; `infra/README.md`, "Rolling back to an earlier
  revision", has the commands.
- **Not done:** no automatic clean-up of old revisions. To undo, remove the
  line and apply; revisions kept until then stay until deregistered by hand.

## 2026-11-21: `.goreleaser.yaml` waits for the owner's merge too (owner decision)

The owner said yes on Nov 21 to adding `.goreleaser.yaml` to the files only
the owner merges. It decides which CLI builds ship and where they are
published (GitHub Releases on mailgoose-public, the Homebrew tap, the AUR),
alongside the release workflow in `.github/workflows/`, which the owner
already merges.

- **Owner-merge paths now:** `infra/`, `.github/workflows/`, `scripts/ci/`,
  the `Dockerfile`, the `Makefile`, `compose.yaml` and `.goreleaser.yaml`.
  The reviewer still reviews those PRs and leaves them for the owner.
- `CLAUDE.md`, `docs/mvp-tickets.md` and `docs/releasing.md` say so.

## 2026-11-21: An ended tester's batches already on the send queue send nothing (MG-107, bug fix; reversible defaults)

When a tester account's period ends (`tester_until` passed, MG-86), the
dispatcher holds its copies like a paused account's, but batches already on
the send queue still went out, because the worker checked only the pause and
the suspension when it started a batch and when it claimed each copy. That
was up to 10 batches per campaign (MG-89), within the tester's 100-a-day
limit. `mailgoose admin account` and `admin find` also showed an ended
tester as "Sending: on". The details below are reversible defaults.

- **The worker treats an ended tester like a paused account.** The
  dispatcher, a worker starting a batch and the claim of each copy now use
  one check (paused, suspended, or a tester whose `tester_until` has come,
  by the same clock). A batch claimed after the end sends nothing and its
  rows are handed back unsent, waiting as for a pause; a period that ends
  partway through a batch stops it before its next copy, as a pause
  mid-batch does. Nothing is skipped or failed, and no credits move: when
  an admin extends the period (`mailgoose admin tester grant`), the
  dispatcher batches the rest again and each copy still goes out once.
  Revoking it puts the account in test mode, whose own rule (MG-80) then
  skips copies to anyone but its test addresses.
- **Admin views.** `mailgoose admin account` shows an ended tester as
  "Sending: OFF: the tester period ended <date>", pointing to `tester
  grant`, and `admin find` shows "off: tester ended <date>" in its SENDING
  column. A pause or a suspension is still shown first. For this the admin
  account summary (`GET /v1/admin/accounts` and every admin account answer)
  gains `tester_until`, null when the account isn't a tester.
- **Unchanged:** launches and test sends were already refused at the end
  (`tester_limit`), the customer's banner and `mailgoose account get`
  already said it had ended, and no migration is needed.

## 2026-11-21: A rejected sign-up stays held after its suspension is lifted (MG-106, bug fix; reversible defaults)

Only a waiting review held back launches and forms (MG-66), so after
`mailgoose admin reject-signup` and then `mailgoose admin unsuspend` the
account launched campaigns and took form signups as if it had been
approved, while `mailgoose admin tester grant` already refused it with
`signup_not_approved`. The two now agree. The details below are reversible
defaults.

- **A rejected review holds the account back like a waiting one**, whatever
  the suspension state: launches are refused with the problem
  `signup_rejected` ("This account's sign-up review was rejected ...", at
  the summary and again at the confirm), and forms answer 403
  `form_closed`, storing nothing and sending nothing. While the account is
  also suspended, only `account_suspended` is reported, as before.
- **Test mode** (MG-80) is treated as for a waiting review: a rejected
  account in test mode may still launch to its confirmed test addresses and
  take their form signups, since it reaches no one else. Test sends work as
  for a waiting account.
- **`unsuspend` doesn't approve.** It leaves the review rejected, and the CLI
  says so and points to `mailgoose admin approve-signup`; the admin log
  records `signup_review: rejected` on the unsuspend.
- **`approve-signup` takes a rejected sign-up too**, not only a waiting one,
  and records what the review was (`review_was`). It doesn't lift a
  suspension: approving one that's still suspended leaves it suspended, and
  the CLI points to `unsuspend`. `reject-signup` still takes only a waiting
  review. Approving one that's already approved still answers 409
  `signup_not_waiting`.
- **Unchanged:** the web app's review banner and `in_review` on the session
  still mean waiting only; a rejected account that's been unsuspended sees
  the reason when it tries to launch. Nothing is migrated: rows already
  rejected are read the new way.

## 2026-11-21: Only test copies that may have gone count toward the day's tests (MG-101, bug fix; the details are reversible defaults)

`reserveTests` counted every copy of a test toward the 50 a day (MG-29)
before any was sent, and nothing gave a place back: a copy the send guard
refused (`mail.ErrNotThisEnvironment`), one SES rejected or throttled, and
the copies after it, which weren't tried, all counted. Refusals before the
reservation (an unverified From, a recipient not allowed, test mode, a
suppressed address, an unsafe link) never counted. This replaces MG-29's
"a test that fails at the mail provider still counts".

- **What gives its place back.** A copy that certainly didn't go
  (`mail.NotSent`): the guard refused it, the provider asked to retry later
  (`ErrRetryLater`, MG-87; tests don't wait, so it's refused), or the
  provider rejected it (`mail.ErrRejected`: an SES client error, or an
  invalid message). The copies after a failed one always give theirs back.
- **What still counts.** A copy with no answer (a timeout, a server fault,
  any error not marked), since it may have gone, so retries can't get
  round the limit with mail that went. Copies before the failure went.
- **Race-safe.** Places are still reserved under the account row's lock and
  given back by deleting their rows afterwards, so tests at once can't pass
  the limit; a place given back is free for the next test. A tester's
  credits (MG-86) count the same rows, so they come back too.
- **Said.** `test_send_failed` has `details.address` and `details.counted`,
  and its hint says whether the copy counts.

## 2026-11-21: The unverified-From error says the fixes where it's read (MG-100; the wording is a reversible default)

A test or launch whose From was on no verified domain and wasn't a verified
sender (MG-85) gave a hint of CLI commands only, which the web app showed as
it was. It never said that the owner's own sign-in address sends from the
shared domain as it is, or that the web app has a Sender addresses page.

- **The problem carries details.** `unverified_from` in a `not_ready` answer
  now has `from_email` (the From) and `shared_sending` (true when
  `SHARED_SENDING_DOMAIN` is set). The message stays neutral, naming no
  command.
- **The hint, read by the CLI and agents,** names `mailgoose
  sender-addresses add` and `mailgoose domain add` with their API routes
  (`POST /v1/sender-addresses`, `POST /v1/domains`) and, with shared
  sending on, says the sign-in address works as it is. With it off, it names
  only the domain fixes, as before.
- **The web app words its own** from the code and details: with shared
  sending on, it names the signed-in owner's address as working as it is,
  links to Sender addresses and to Domains; off, it links to Domains only.
  An `unverified_from` without the details still shows the API's message
  and hint.
- **The email check's message** (shared sending on) also says the sign-in
  address sends as it is. Its web rendering has no links yet.

## 2026-11-21: A sending pause takes over the past-due pause, so the payment doesn't lift it (MG-105, bug fix; reversible defaults)

When an account was already paused for its plan's payment (code
`subscription_past_due`) and a sending pause landed on it, the account kept
the billing reason and code. The new pause was still logged and counted
toward suspension, but when the payment went through billing saw a
past-due pause and lifted the whole pause, so a complaints or bounce pause
was lifted with nobody looking. Until then, launches showed only the
past-due message.

- **A pause landing on a past-due pause takes it over.** Its reason
  replaces the billing one and the code goes, so the payment going through
  no longer lifts it; someone has to (`mailgoose admin unpause`). This
  holds for every automatic sending pause, all through `pauseAccount`
  (`internal/sending/health.go`): a campaign's complaints or hard bounces
  (and a slice's, and a tester's stricter limits), the account-level pause
  on the last 30 days (MG-88, whose check now treats a past-due pause as
  not paused), and the third throttle (MG-61); and for the unsafe-link
  pause at launch (MG-64, `linkrisk.go`), which before did nothing on a
  paused account and now takes over a past-due pause, logs it and sends
  its alert. Any other existing pause (a person's, or an earlier sending
  pause) keeps its reason as before.
- **A person's pause too.** `mailgoose admin pause` already replaced the
  reason and cleared the code, so a payment never lifted it; that's now
  tested.
- **A payment lifts only a pause still coded as past due**, as before. The
  reverse order was already right: a payment going past due during another
  pause doesn't replace it (billing pauses only an unpaused account), and
  the payment then leaves it alone.
- **The payment is still shown as owed.** The past-due state stays on the
  subscription, so the web app's "paused for an unpaid fee" banner and
  `mailgoose plan get` still say so. A launch refused for another pause now
  also lists `subscription_past_due` after `account_paused` while the
  subscription is `past_due` or `unpaid`, saying that sending stays paused
  for that other reason after the payment goes through. An admin unpause
  while the payment is still owed puts the past-due pause back, as before.
- **Nothing is rewritten.** Accounts paused before this keep their reason
  and code; an account whose sending pause was hidden under a past-due one
  and already lifted by a payment isn't paused again.
## 2026-11-21: Table sizes in the CLI and the daily digest (MG-95; details are reversible defaults)

Every copy sent is one `campaign_recipients` row and one or two
`send_events` rows, kept 12 months, so those two tables are the first
growth cost (RDS storage, then the 30-day account queries), and nothing
showed how big they were. The details below were picked while building it
and are easy to change.

- **Where.** `mailgoose admin storage` (`GET /v1/admin/storage`, admins
  only): `campaign_recipients` and `send_events` first, then the largest
  other tables by size, 10 in all by default (`--limit` 1 to 50). `total`
  and `total_bytes` count every table in the schema.
- **What.** Size is `pg_total_relation_size` (the table with its indexes
  and TOAST). Rows are the planner's estimate, `pg_class.reltuples`, shown
  as an estimate; it's null ("not analysed") for a table never analysed
  (reltuples -1). No exact `count(*)`: on these tables it would read
  millions of rows.
- **History.** A new table, `table_size_snapshots` (migration 00045), gets
  every table's size and row estimate once a UTC day, written by the daily
  digest job in the transaction that claims the day, quiet day or not.
  Rows older than 8 weeks are deleted by the same job. So history exists
  only where the digest job runs (`OPS_ALERT_EMAIL` set).
- **The change over 7 days** is today's size less the size recorded on the
  UTC day exactly 7 days before. With no row from that day (the first week,
  or a day the job didn't run), the command shows "-", the API null, and
  the digest says "no size from 7 days ago to compare" rather than a
  number from another day.
- **The digest line** is the last line of the digest, one line for both
  tables: "Storage: campaign_recipients 1.2 GB (+80 MB in 7 days),
  send_events 640 MB (...)". It doesn't make a quiet day's digest go.
- **Not here:** the NAT gateway's per-GB cost on SES calls in prod and the
  SES interface endpoint as the switch past a few million sends a month
  belong in `infra/README.md`'s cost notes, which the owner merges, so they
  go with MG-93 or MG-99.

## 2026-11-21: Send throughput is measured per batch; the send pool waits for staging's numbers (MG-91 part 1; details are reversible defaults)

A worker takes one send message at a time (`MaxMessages: 1`) and
`HandleBatch` sends its rows one after another: claim, pace slot, SES call,
update. So each copy waits a whole SES round trip plus its database round
trips, and real throughput may sit well under `SEND_RATE_PER_SECOND` (the
architecture review guessed 3 to 5 a second against 10; never measured).
MG-91 measures first. This part adds the measurement; nothing about how a
batch sends has changed.

- **Logged.** At the end of every batch's send loop the worker logs `send
  batch finished` at INFO, with `campaign_id`, `account_id`, `attempt`,
  `rows` (in the batch), `sent`, `skipped` (contact gone or unsubscribed,
  or left out by test mode), `failed` (marked failed on the last attempt),
  `ended` (`done`, `halted` for a pause or stop, `retry_later` for an SES
  "not now", or `error`), `seconds` (wall clock from the loop's start to its
  end) and `copies_per_second` (`sent` over `seconds`), and the worker's
  `send_rate_per_second`. No addresses. A batch for a campaign that's
  already paused or stopped hands its rows back before the loop, as
  before, and logs nothing.
- **Recorded.** Each batch that sent at least one copy gets a row in
  `send_batch_timings` (migration 00044): its start and end, batch size,
  and copies sent, skipped and failed. The worker prunes rows older than 7
  days as it writes. Recording is best effort: a failure is logged as a
  warning and the batch's outcome stands. Deleting an account deletes its
  rows (they're in `AccountTables`).
- **Shown.** `mailgoose admin deliverability` (and `send_throughput` in
  `GET /v1/admin/deliverability`) ends with the last 24 hours across every
  account: batches, copies, the seconds when at least one batch was sending
  (overlapping batches merged), copies a second over that time, and the
  configured `SEND_RATE_PER_SECOND`. It also gives the batches' seconds
  added up and copies a second over those, which differs only when
  batches ran at once on several workers and shared the rate. Time between
  batches (the queue receive, the dispatcher's 2-second tick) isn't in
  either number; a test campaign's end-to-end rate can be read from the
  first and last log lines (see below).
- **SES mailbox simulator copies count** for throughput, since they're real
  SES calls, though they stay out of the bounce and complaint rates
  (MG-86). So a simulator-only run shows its throughput even when the
  report lists no accounts.
- **What's next (open):** the pool (step 2 of MG-91: a batch's rows sent
  by a small pool, default 4, each copy taking its own pace slot, keeping
  `MaxMessages: 1` and "miss one, never send two") is built only if the
  measured rate is well under `SEND_RATE_PER_SECOND`. The measured numbers
  are in the follow-up entry, "Staging sends at 94% of the configured
  rate": no pool.

**Measuring on staging** (needs the owner's AWS sign-in; after this is
deployed):

1. Use an account that's in full sending mode (`mailgoose admin account
   <id>` says `Mode: full`), not a tester account (MG-86 caps testers at
   100 copies a day), with at least 400 credits and room for 200 more
   contacts. Lists of 200 or fewer aren't sliced (MG-62), so each test
   campaign goes to at most 200.
2. Make a tag (`mailgoose tags create throughput-test`) and import 200
   simulator addresses with it: a CSV with an `email` column of
   `success+tp001@simulator.amazonses.com` to `success+tp200@...`, then
   `mailgoose import tp.csv --list-source other --list-source-detail "SES
   mailbox simulator addresses for a send throughput test"
   --permission-confirmed --tag throughput-test --wait`.
3. Make a campaign to that tag only (`mailgoose campaign create --name
   "Throughput test 1" --email <ready email> --tag throughput-test`), send
   it a test (`mailgoose campaign test <id>`), and launch it (`mailgoose
   campaign launch <id>`, then `--confirm` with the code once the owner
   says yes). Repeat once for a second campaign to the same tag, after the
   first has finished, for 400 copies in all.
4. Read `mailgoose admin deliverability` (the Throughput line), the worker's
   `send batch finished` lines (`aws logs filter-log-events --log-group-name
   /ecs/mailgoose-staging-worker --filter-pattern '{ $.msg = "send batch
   finished" }'`), and SES's own `MaxSendRate` (`aws sesv2 get-account`).
   Compare copies a second with `SEND_RATE_PER_SECOND` and with SES's rate.

## 2026-11-21: A daily cap on each form's new signups (MG-92, from the architecture review; reversible defaults)

Form signups were limited only to 10 a minute per IP and 600 a minute per
form, held in memory in each API task (MG-36), so a botnet that finds a form
could add tens of thousands of made-up contacts an hour across tasks. With
double opt-in off they become recipients who bounce and pause the account.

- **Cap (reversible default):** each form adds at most 1,000 new contacts in
  any rolling 24 hours, set by `FORM_DAILY_SIGNUP_LIMIT` (`0` means no cap).
  It's counted in Postgres (`form_signups`, one row per counted signup),
  under the account's row lock that `contacts.SignUp` already takes, so it
  holds across API tasks. Each form's rows older than a day are pruned as it
  takes new ones, so a form keeps at most a day's worth.
- **What counts (reversible default):** a submission that adds a contact.
  One from an address already on the list (subscribed, unconfirmed,
  unsubscribed, bounced) doesn't, since it adds nobody and its confirmation
  emails are already held to one an hour per address. Bots' dropped posts
  don't either. Consent records aren't counted instead, because deleting a
  contact deletes them, so deleting bot signups would reopen the form at
  once. Signups in the day before the migration are copied in.
- **Past the cap** every submission is refused alike, new address or not, so
  the answer doesn't tell who's on the list: 429 `form_signup_limit` with
  `Retry-After` (when enough signups age out), as JSON or the usual page
  ("Please try again tomorrow"). Nothing is stored and no email goes. Bots
  still get the success answer. `form get` lists the problem
  `signup_cap_reached`, saying when it lifts.
- **Notices (reversible defaults):** the first refusal emails the owner, from
  `notices@` the system sending domain (`KindSystem`), with what to check and
  how to turn on double opt-in if it's off, and alerts `OPS_ALERT_EMAIL`.
  They go at most once in a rolling 24 hours per form, stamped on the form
  (`signup_cap_noticed_at`) before sending, so two API tasks don't both send.
  A notice that fails to send is logged and not retried that day.
- **Unchanged:** the per-IP and per-minute limits stay as they are.
- **Open for the owner:** an optional captcha per form (the review's other
  suggestion) is new scope, not built. Whether owners can raise one form's
  cap is open too; for now it's one value for every form.

## 2026-11-21: The send guard checks the kind of mail from the system domain (MG-90; the zero-kind rule is a reversible default)

`mail.Guard` (2026-11-20, SES send isolation) let any message whose From was
on the system sending domain through before looking at its account or kind.
Launches and test sends already need a From on a domain the account
verified (or the shared domain), and the system domain can't be added as an
account's domain, so customer mail couldn't reach it; but the guard is meant
to check every message, and a later path that built a campaign with a
system From would have sent customer content as `mail.<domain>`.

- **The rule.** From the system domain, the guard lets through only
  `KindSystem` (sign-in codes, test and sender address confirm links, ops
  alerts and the daily digest, throttle and suspension notices) and
  `KindConfirm` (double opt-in confirmations). `KindTest` and
  `KindCampaign` from it are refused with `ErrNotThisEnvironment`, whatever
  the account, and the error names the kind. Other domains are checked as
  before, whatever the kind.
- **No kind (reversible default).** A message with no kind, or one that
  isn't one of the four, is refused from the system domain too: it isn't
  known to be mailgoose's own. Every place that builds a message sets its
  kind, and `Message.Validate` already refuses an unset one, so no flow
  relied on it.
- **Checked.** Every caller that sends from the system domain sets
  `KindSystem`, and their tests now send through the real guard. Signup
  confirmations go from the account's domain or the shared domain, not the
  system domain, so allowing `KindConfirm` there changes nothing today.

## 2026-11-21: The past-due pause doesn't count toward automatic suspension (MG-104, bug fix; reversible default)

An account is suspended on its second automatic pause in 30 days (MG-65),
counted from the `auto_pause` rows in the admin log. Billing logs its
past-due pause (`subscription_past_due`) as `auto_pause` too, so one failed
card and one complaints pause in a month suspended the account.

- **Only sending pauses count** (`suspendOnRepeat` in
  `internal/sending/suspend.go`): complaints and bounces, an account-level
  health pause, and an unsafe link at launch. The count leaves out
  `auto_pause` rows whose details `code` is `subscription_past_due`, so any
  new kind of sending pause counts without a change here. (Corrected
  2026-11-21, MG-96: an unsafe-link pause counts as an earlier pause, but it
  doesn't call `suspendOnRepeat` itself, so it never suspends on its own;
  whether it should is the open phishing-link question.)
- **Nothing is rewritten.** Existing rows stay as they are and are read the
  new way. The past-due pause is still logged as `auto_pause`, and the daily
  digest, `mailgoose admin log` and `mailgoose admin account` still show it.

## 2026-11-21: A campaign keeps at most 10 batches on the send queue (MG-89; details are reversible defaults)

The dispatcher put up to 2,000 of a campaign's rows on the send queue every
2-second tick, up to the account's daily room, so a 10,000-copy launch put
about 100 batches on the queue within seconds. The workers drain it at
`SEND_RATE_PER_SECOND` (10 by default), so the last batch waited well over
15 minutes and the send queue's age alarm, meant for a worker that's down,
stuck or too slow, fired on an ordinary send.

- **The cap** (`MaxWaitingBatches`, `internal/sending/pipeline.go`): a
  campaign may have 10 batches' worth of rows waiting, meaning enqueued and
  still `queued` (not yet claimed by a worker) within `RequeueAfter`. A
  batch is about 10 seconds of sending, so that's about 100 seconds of
  work per campaign at 10 a second (1,000 copies). The dispatcher tops it up
  on every tick, in whole batches (no trickle of small ones), so the
  workers never run dry and a campaign sends at the same rate as before.
  The account's daily limit, the platform cap (MG-87) and the 2,000-a-tick
  limit still apply; the smallest room wins. The 2,000 no longer binds
  (10 batches are at most 1,000) and stays as a backstop.
- **Rows held back after an SES "not now" (MG-87) count while they wait.**
  So a throttled campaign puts no more copies in front of SES meanwhile:
  each waiting campaign tries at most 10 copies a minute, one a batch. They
  stop counting at the very moment they can be batched again (their
  `enqueued_at` reaches `RequeueAfter` ago: the count takes later times,
  the batching earlier or equal ones), so a row is never both counted and
  waiting to be taken, and the held rows go out a minute later as before.
- **Recovery is unchanged:** rows whose batch never came stop counting and
  are batched again after `RequeueAfter` (an hour). If every batch of a
  campaign is lost, it waits that hour; before, its other rows were already
  on the queue anyway.
- **Slices, pauses and finishing are unchanged.** A full queue batches
  nothing, which the dispatcher already checks against the open rows
  before it calls a slice out or a campaign done.
- **The alarm's threshold stays 15 minutes.** The cap is per campaign, so
  ten or more big campaigns sending at once at 10 a second could still age
  the queue past it; that's a real backlog for the alarm to report. A
  platform-wide cap on waiting batches is the follow-up if it fires that
  way.

## 2026-11-21: Account-level pause on the last 30 days (MG-88; the details are reversible defaults)

The automatic pause (CMP-6, MG-79) judged one campaign at a time, and only
from 500 copies, so an account sending many campaigns under 500 each was
never paused automatically once its slicing window (MG-62) was over,
whatever its rates; its 30-day rates only held its daily limit down. MG-88
adds a second trigger beside the campaign check, which stays as it is. The
details below were picked while building it and are easy to change.

- **When.** On every hard bounce and complaint SES reports, after the
  campaign check, unless that paused the account. The campaign's
  throttle (MG-61) still happens first, so one event can throttle and then
  pause.
- **What's judged.** The account's copies over the last 30 days, counted
  the way the daily limit's "clean" check counts them, by the same limits:
  over 0.1% complaints (from 2 complaints) or 3% hard bounces, from 500
  copies over the 30 days. SES mailbox simulator copies are left out, as
  everywhere (MG-86).
- **How.** The same automatic pause as a campaign's: the account is paused,
  an `auto_pause` is logged as `automatic` (its target the campaign whose
  event was the last straw, its details the account's rates with
  `"scope": "account"`), the ops alert goes out, and it counts toward the
  automatic suspension at the second automatic pause in 30 days (MG-65).
  No campaign is marked as having paused it, so each can still pause the
  account on its own later.
- **Reason.** Account-level in its own words, so `mailgoose admin account`,
  the digest and the launch refusal read right: "It was paused
  automatically: over the last 30 days, 0.33% of the people sent this
  account's campaigns marked them as spam (2 of 600), over the 0.1% limit
  for an account." (or the hard-bounce version). The ops alert names the
  window and the campaign of the latest event instead of one campaign's
  numbers.
- **An account paused already**, for any reason, keeps its pause and reason,
  and nothing is logged or sent, so a run of events can't stack up pauses
  toward a suspension. Two events at once can't both pause it: the account
  row is locked before the pause, and only when the rates are over the
  line, so healthy accounts take no extra lock.
- **Tester accounts** keep their stricter per-campaign pause (MG-86) and
  get this one too, like the usual campaign limits. With at most 500
  credits for life it can only fire on a tester's whole budget.
- **No re-pause loop.** After an admin unpauses an account (an `unpause` by
  a person), or after any automatic pause logged with a campaign (an
  `auto_pause` with a `target_id`: a campaign's own pause, and also an
  account-level pause, which logs the campaign whose event tipped it), only
  copies sent since then count, and only bounces and
  complaints on those copies, so late complaints on copies already looked
  at don't count against new ones. The account needs 500 new copies over
  the line to be paused again; the reason then says "since sending was last
  unpaused (date)". An automatic unpause (the plan's payment going through)
  doesn't restart the window, since nobody looked at the sending. The
  daily limit's clean check is unchanged and still reads the full 30 days.
- **Merging.** App-only, no migration, so the reviewer merges.

Open for the owner: whether the account-level line should be stricter than
the per-campaign one (it's the same 0.1% and 3% now), and whether its
early warning should throttle an account the same way (not built; a
throttle still comes only from one campaign of 500 or more).

## 2026-11-21: Shared-domain mail goes under the account's name, and doesn't ramp without a domain (MG-98, owner decision; the lists and normalisation are reversible defaults)

The owner said yes (Nov 21) to closing MG-85's open question 1 ("tighter
limits for shared-domain senders?"). On the shared sending domain, the From
was `<the email's From name> <slug@shared domain>`, so an account could send
as "PayPal Support" from mailgoose's domain, and shared-domain senders grew
to the same 10,000 a day as accounts with a verified domain.

- **Name (owner decision).** On the shared domain the display name is always
  the account's name; an email's own From name (or the account's default
  From name) is used only on a verified domain of the account.
  `senders.Resolver` does it, so test sends, launches, signup confirmations
  and the email check agree. The email check's `shared_domain` warning names
  the account name it goes under.
- **Refused account names (owner decision; the details are reversible
  defaults).** Sign-up and rename refuse, with a 422
  `account_name_not_allowed` whose message says why, a name that contains
  `@`, one shaped like a domain (two or more labels and a top-level domain of
  letters, such as `paypal.com`, so `St.Louis` is refused too and `St. Louis`
  isn't), or one carrying a term on these lists (`internal/accountname`):
  - Brands refused anywhere in the name, even inside a word: PayPal,
    Mastercard, American Express, Wells Fargo, Bank of America, Citibank,
    JPMorgan, Chase Bank, Capital One, Western Union, MoneyGram, Cash App,
    Venmo, Coinbase, Binance, Apple Pay, Apple ID, Apple Support, iCloud,
    iTunes, Google Pay, Microsoft, Facebook, Instagram, WhatsApp, LinkedIn,
    Netflix, DocuSign, QuickBooks, Mailchimp, mailgoose, Mailer Daemon,
    Postmaster, Amazon Pay, Amazon Prime, Amazon Support.
  - Brands refused as a word or words of the name ("Amazon Store", not
    "Amazonia Coffee"): Amazon, Google, Gmail, eBay, Zelle, Stripe, Shopify,
    Klarna, Afterpay, Revolut, Amex, Intuit, FedEx, DHL, USPS, IRS, HMRC,
    Yahoo.
  - Brands that are everyday words, refused only when the name is the brand
    with at most generic words such as support, team, billing, account,
    security, service, store, official, the, inc or llc ("Apple Support",
    not "Big Apple Bagels"): Apple, Chase, Visa, Square, Meta, Discover,
    Citi, Target, Wise, Uber.
  - Payment and account-security terms, refused as a word or words: Billing,
    Invoice(s), Payment(s), Refund(s), Chargeback, Fraud, Wire Transfer, Bank
    Transfer, Direct Deposit, Tax Refund, Account Security, Security Alert,
    Account Verification, Verification, Password.
- **Normalisation (reversible default).** Done in Go, not SQL, so it doesn't
  depend on the database's encoding (the PR 192 review's point about
  `translate()`): NFKC then NFKD, combining accents and invisible format
  characters (zero-width spaces, joiners, soft hyphens) dropped, case
  folded, the ideographic full stop read as a dot, common lookalikes mapped
  (Cyrillic and Greek letters that look Latin, `0` to o, `1`, `i` and `|` to
  l, `3` e, `4` a, `5` and `$` s, `7` t, `8` b, `rn` m, `vv` w), then
  anything that isn't a letter or digit splits words. A brand spelt across
  words is caught ("Pay Pal", "P.a.y.P.a.l").
- **Names taken before this stay** until they're changed: sending the current
  name back unchanged in an update isn't refused, so the rest of the settings
  can still be saved. Nothing checks existing accounts' names.
- **Limit (owner decision).** With `SHARED_SENDING_DOMAIN` set, an account
  with no verified domain of its own stays at the new-account daily limit
  (`NEW_ACCOUNT_DAILY_SEND_LIMIT`, 1,000) and doesn't ramp. Its send limit
  says why, "Add your own domain to send more", with `needs_domain: true` in
  `GET /v1/account` (and in `mailgoose admin account --json`; corrected
  2026-11-21, MG-96: the plain admin view doesn't show it), a link to Domains in the
  web app's Settings, and `mailgoose domain add <your-domain>` in
  `mailgoose account get`. With the variable unset, nothing changes.
- **Details picked (reversible defaults):**
  - "Verified domain" means any domain of the account with status
    `verified`, whichever From a campaign uses; one that later fails puts the
    hold back.
  - Once it has one, the limit grows from when the account verified its
    first still-verified domain, if that's later than when it started (or
    than a lifted throttle), so a new domain starts at 1,000 too and doubles
    each clean week. An account that verified a domain long ago ramps as
    before.
  - A throttle (MG-61) still wins, and a limit staff set by hand
    (`daily_send_limit`, including a tester's 100) still stands over the
    hold.
- **The nudge stays** in test results, launch summaries and the email check.
- **MG-85's open question 1 is closed**; question 2 (which domain is the
  shared one in prod) stays open.

## 2026-11-21: SES throttling and quota errors wait; a platform daily send cap (MG-87, from the architecture review; details are reversible defaults)

The architecture review (#195, F3) found that any SES error failed a
campaign's whole tail in about ten minutes: the batch was retried, and on
the fifth try every unsent copy was marked `failed` and refunded. When SES
is only saying "not now", that loses copies that would have gone an hour
later. The SES quota (50,000 a day) belongs to the AWS account, so this
still matters after prod gets its own account.

- **Which errors wait** (`internal/mail/ses.go`): `TooManyRequestsException`,
  `LimitExceededException`, `SendingPausedException`,
  `AccountSuspendedException`, AWS's generic `Throttling` and
  `ThrottlingException`, and `MessageRejected` whose message says the quota
  or sending rate is exceeded ("Daily message quota exceeded", "Maximum
  sending rate exceeded"; matched on "quota exceeded" or "sending rate
  exceeded", any case). They're marked `mail.ErrRetryLater`, with the SES
  error still wrapped and the same text. Every other error is unchanged, so
  it still fails the copy on the fifth try.
- **What waiting does** (`internal/sending/pipeline.go`): the copy's claim is
  taken back with no error written, the batch's rows still `queued` are held
  back, and the batch returns success, so no SQS attempt is used and nothing
  is failed or refunded. Copies already sent in the batch stay sent. A send
  cut off by shutdown still keeps its claim, so a copy is never sent twice.
  One WARN log line per batch, with the campaign id and the error.
- **Backoff: one minute** (`RetryLaterAfter`). Handing the rows back as a
  pause does (clearing `enqueued_at`) would have the dispatcher batch them
  again on its next 2-second tick, asking a throttled SES again and again.
  Instead the rows are marked as enqueued `RequeueAfter` less a minute ago,
  so the dispatcher takes them again a minute later, and meanwhile they
  count against the daily limits as copies on their way. While SES stays
  over quota, each waiting campaign tries one copy a minute.
- **Platform daily cap:** `PLATFORM_DAILY_SEND_LIMIT`, default 45,000 (under
  the 50,000 SES quota, leaving room for sign-in, confirmation and test
  mail, which it doesn't count); `0` means no cap. The dispatcher counts
  campaign copies sent in the last 24 hours across every account, plus
  those enqueued and still on their way, the same way it counts an
  account's, and enqueues no more than the smaller room. Copies past the cap
  wait, as for the account's limit. Mailbox simulator copies are counted
  too, which errs on the safe side. Migration 00042 adds an index on
  `sent_at` so the count stays cheap. Two dispatchers running at the same
  moment could each fill the last of the room, so the cap can be passed by
  up to one tick's worth; the 5,000 of headroom covers that.
- **Infra follow-ups (MG-93, #202):** the CloudWatch alarm on SES sends
  nearing the quota, and reading the real quota from SES `GetAccount`
  instead of a configured number.

## 2026-11-21: More paths wait for the owner's merge (owner decision, "fine for now")

From the architecture review (#195, F2; ticket MG-97, #206). Every merge to
`main` deploys staging through CodeBuild, and the deploy runs
`scripts/ci/deploy-ecs.sh` with a role that can start an ECS task. A change to
the deploy script or the image build reached that role with only the
reviewer's check.

- **Owner-merge paths:** `scripts/ci/`, the `Dockerfile`, the `Makefile` and
  `compose.yaml` join `infra/` and `.github/workflows/`. The reviewer still
  reviews those PRs, checks the diff's file list before merging anything, and
  leaves a PR that touches any of them for the owner. The owner: "Yes, fine
  for now."
- **Authors (default, not yet confirmed):** the reviewer merges only PRs opened
  from a branch of this repository by the owner or the project's own agents,
  never from a fork or another author. Making the repository private, which
  the owner plans, makes this mostly moot; it's easy to drop.
- **The owner's settings, outside the repo:** branch protection on `main`
  (require a pull request and the four CI checks, Build and test, End-to-end,
  Lint and Docker build (no push); block force-pushes and deletion), and
  making the repository private. The public site moves to its own public
  repository first (see the entry below), since a private repository on
  GitHub's free plan can't publish GitHub Pages.
- Prod's own deploy route (a tag or branch the owner moves) is in MG-52 (#61)
  and MG-99 (#208).

## 2026-11-21: Public site moved to its own repository; CLI releases through package managers (owner decisions; the details are reversible defaults)

The owner wants this repository private. GitHub Pages on a free personal
account serves only public repositories, so the site moved out first, and
`go install` from this repository stops working once it's private, so the CLI
needs published downloads.

- **The site lives in gremlin-technologies/mailgoose-public (owner decision).**
  Public, published by its own Pages workflow, custom domain moved there on
  Nov 21 with no DNS change. It will also hold the knowledge base, under
  `site/docs/` (owner ask). This repository's `site/`, its Pages workflow and
  the two tests that checked the site's copies (`siteTokens.test.ts`,
  `sitePolicies.test.ts`) are gone. Supersedes "Where it lives" in the Nov 20
  public site entry below.
- **Generated pages stay generated from here (default).** `scripts/site/*.mjs`
  now write into a checkout of mailgoose-public next to this one
  (`SITE_DIR` overrides), and the result goes there as a PR. Nothing checks
  for drift automatically any more; `tokens.mjs --check` still does by hand.
- **The CLI ships through package managers (owner ask).** GoReleaser from this
  repository publishes GitHub Releases on mailgoose-public (tar.gz, zip, .deb,
  .rpm), a Homebrew cask in gremlin-technologies/homebrew-tap, and the AUR
  package `mailgoose-bin`. Scoop and winget wait. See `docs/releasing.md`.
- **Release workflow, run by hand (default).** Actions > Release CLI with a
  version, or a pushed `v*` tag. Its token is a fine-grained one with Contents
  write on mailgoose-public and homebrew-tap only; the AUR key is optional.
- **Keep this repository public until the first release works and the alpha
  guide points at it (owner's call to flip).**

## 2026-11-21: Alpha tester guide at mailgoose.app/alpha/ (owner ask; the details are reversible defaults)

The owner asked for a guide for alpha testers: how to test each area of the
app, as a page on mailgoose.app. It's `site/alpha/index.html`, in the site's
style and light only like the rest of the site.

- **Unlisted (default).** Nothing on the site links to it, and it carries
  `noindex, nofollow`; tester invites link to it. Adding it to the site's nav
  is one line in `site/index.html`.
- **Bug reports go to `#user-feedback` in the team Slack (owner decision).**
  Tester invites include the Slack access, so the page names the channel but
  carries no invite link or workspace address. Security problems go by direct
  message to whoever invited the tester, not to the channel.
- **It documents only what's on `main`.** The Mailchimp import, the shared
  sending domain on staging (MG-85's infra, PR 193) and a CLI installer are
  under "Coming soon"; the CLI is installed with `go install` meanwhile.

## 2026-11-21: Staging auto-deploy is live; how the GitHub connection is set up (confirmed on staging)

The owner set up the CodeBuild deploy from the runbook on Nov 21. Every merge
to `main` now deploys staging and posts to `#deployments` (first build
SUCCEEDED in about 6 minutes; `/readyz` answered with the merged commit).

- **What went wrong first:** CodeBuild couldn't create its GitHub webhook
  (`OAuthProviderException`). The connection had been finished as a "GitHub
  user" connection with the App installation box left blank, so the AWS
  Connector for GitHub app was never installed and couldn't see the repo.
  Fixed by recreating the connection and choosing **Install a new app** at
  that step. The runbook (`infra/README.md`) now says so.
- **The repo stays where it is:** `gremlin-technologies` is a personal GitHub
  account, not an organization, and that works. The move to a
  `gremlin-technologies-dev` organization, prepared for this, isn't needed and
  was dropped; it can come back if the owner wants an organization for other
  reasons.
- **IAM timing:** an apply that changes the CodeBuild role's policy can fail
  right after on the project update with "User is not authorized to access
  connection". Applying again a minute later worked with no changes.
- **Open, watch for it:** the role's policy names the connection only by its
  old-style ARN (`arn:aws:codestar-connections:...`). Real builds work, but
  IAM's policy simulator reported an explicit deny on the connection actions,
  which contradicts them, so the simulator isn't a reliable check here. If a
  later build fails with an authorization error naming the new-style ARN
  (`arn:aws:codeconnections:...`), add that form to the role's
  `GitHubConnection` statement in a small PR.
- The connection `mailgoose-deployment`, made by hand while testing, is
  unused. Deleting it is the owner's call.

## 2026-11-21: Optional custom domains: a shared sending domain with verified sender addresses (MG-85, owner ask; details are reversible defaults)

The owner asked (Nov 21) for custom domains to be optional: encouraged
heavily for deliverability and trust, since they're a barrier for small
business owners, while still verifying that the sender owns their email
address.

- **Design (MG-85):** with `SHARED_SENDING_DOMAIN` set, a From that isn't on
  a verified domain of the account sends when it's a verified sender of the
  account: the owner's sign-in address (proved at every sign-in, needs no
  slot) or a sender address confirmed by an emailed link. The message goes
  from the From name, or the account's name, at
  `<sending_slug>@<shared domain>`, with Reply-To the From address. A From on
  a verified domain is unchanged, and the owner's address never goes in the
  From unless its domain is verified. Unset, nothing changes.
- **Details picked (reversible defaults):**
  - `accounts.sending_slug` is made from the account's name at creation
    (accents folded, apostrophes dropped, anything else to hyphens, at most 32
    characters before a suffix), backfilled oldest account first, and never
    changed by a rename. A taken or reserved slug (postmaster, abuse, admin,
    administrator, hostmaster, webmaster, mailer-daemon, root, noreply,
    no-reply, bounce, bounces, support, security, info, mail, mailgoose), or
    one under 3 characters, gets `-` and 4 random hex characters.
  - Sender addresses copy test addresses' limits: at most 10 an account
    (pending included), links valid 7 days, a resend at most once a minute,
    at most 10 confirm emails an account in 24 hours.
  - An email's own Reply-To must also be a verified sender or on a verified
    domain, or the test, launch or email check refuses it with
    `unverified_reply_to`.
  - `mail.Guard` lets the shared domain through only for the account's own
    slug with at least one Reply-To and every Reply-To verified; its one-minute pass cache is keyed by
    the exact local part and Reply-Tos, so one pass can't cover another.
  - The nudge, in test results, launch summaries (web and CLI) and as an
    email check warning (`shared_domain`): "Sending from <slug>@<shared>,
    mailgoose's shared domain, with replies to <you>. Add your own domain for
    better deliverability and trust", with the `mailgoose domain add` command
    or a link to Domains.
  - Locally (`scripts/dev.sh`, `make run-api`, `compose.yaml`) the shared
    domain is `shared.localhost`.
- **Open questions for the owner:**
  1. ~~**Tighter limits for shared-domain senders?** Default: the same send
     limits, review and test mode as any other account for now; nothing extra
     is built.~~ Closed Nov 21 (MG-98, above): mail there goes under the
     account's name, and accounts without a verified domain stay at the
     new-account limit.
  2. **Which domain is the shared one?** Default: `SHARED_SENDING_DOMAIN`
     stays unset in prod, so prod is unchanged. Staging gets a subdomain of
     `staging.mailgoose.app` (assumed `shared.staging.mailgoose.app`) in a
     separate infra PR, which has to verify it in SES. For prod we recommend
     a separately registered domain, so a shared-domain reputation problem
     can't touch mailgoose.app or its sign-in mail.
## 2026-11-21: Light by default, dark mode a switch in Settings (owner decision; storage is a reversible default)

The owner decided: "light-only by default with a 'dark mode' toggle in
settings". This replaces "dark mode follows the system" in the two entries
below.

- **The public site is light only.** It has no settings page, so it has no
  switch. `scripts/site/tokens.mjs` copies only the app's light tokens, the
  site pages drop the light-text logo, and `siteTokens.test.ts` fails if any
  dark styling comes back.
- **The app is light until dark mode is turned on**, whatever the system
  setting. Settings has an Appearance panel with a "Dark mode" checkbox. It
  sets `data-theme="dark"` on `<html>`, which switches to the same dark tokens
  as before, and the logo swaps to its light-text version. The sign-in, CLI
  approval and policy pages follow it too.
- **Kept in the browser (default, reversible).** The choice is saved in
  `localStorage` (`mailgoose.theme`), so it holds per browser and needs no
  API, database or CLI change; other open tabs follow it. Keeping it on the
  account instead would mean a field on `/v1/account` and a CLI flag.

## 2026-11-21: "Save and exit" in the email editors (MG-83, owner ask; details are reversible defaults)

The owner found it unclear what to do when done editing an email, with going
"back" feeling unnatural, and asked for a "Save and exit" button even though
the editors save as you go. Both editors now have it as their primary button.
It sends any edit not yet saved at once, without the autosave pause, and once
everything is saved goes back to where the editor was opened from. Autosave
is unchanged.

Details picked while building:

- **Where it goes back to.** A link from a campaign's page (the email's name,
  and "Fix it in the editor") tells the editor which campaign it came from,
  and Save and exit goes back there; from anywhere else, it goes to the email
  list. Only a campaign page's own path is taken, so a crafted link can't send
  it elsewhere. It's carried in the browser's navigation state, not the URL,
  so reloading the editor forgets it and falls back to the email list.
- **A failed save stays.** On a version conflict or an error, it stays in the
  editor and shows why, and the edit is kept. Pressing it again sends edits
  whose save failed once more.
- **No pause to show "saved".** The save state turns to "All changes saved"
  and it leaves straight away, rather than lingering to show it.
- **The HTML editor's "All emails" link** is replaced by Save and exit. The
  block designer keeps its small back arrow, which now goes back to the same
  place.

## 2026-11-21: Tester accounts (MG-86, owner decision; limits and details are reversible defaults)

The owner asked for a tester mode: accounts for people invited to try
mailgoose before launch, held to a 500-credit budget "and any other
safeguards we think are necessary". It's separate from test mode (MG-80),
which keeps an account to its own test addresses; a tester sends real
campaigns to people who agreed to get them. The details below were picked
while building it and are easy to change.

- **Who.** Admins grant it with `mailgoose admin tester grant <id>`
  (`POST /v1/admin/accounts/{id}/grant-tester`), only to an account whose
  sign-up was approved or never held (MG-66) and that isn't suspended. A
  grant gives full sending. `mailgoose admin tester revoke` ends it and puts
  the account in test mode. Both are logged with a reason.
- **How long.** 30 days unless `--until` names an end (a date means the end
  of that day, UTC), at most 366 days. A second grant extends it, keeping its
  start, so credits used stay used; a grant after a revoke starts afresh. At
  its end the account can't launch or test, and copies still queued wait, as
  for a paused account, rather than being skipped; an admin extends or
  revokes it. (Corrected 2026-11-21, MG-96: the dispatcher holds them, but
  batches already on the send queue still go out, and the admin views say
  "Sending: on"; filed as MG-107, #225.)
- **Credits.** 500 for its life by default (`--credits` 1 to 10,000),
  counted from its start: campaign copies queued, sending or sent, and test
  sends. A skipped or failed copy gives its credit back. They're counted
  apart from the credit ledger, which still debits the account's own
  credits as for anyone; the tester budget is a cap on top. A launch whose
  audience needs more than are left is refused with `tester_limit` (when the
  summary is asked for and again at the confirm, with the account row
  locked), and so is a test send; nothing is queued or sent.
- **Contacts.** At most 500 counted contacts, whatever the plan; adds and
  CSV imports stop there. The Mailchimp import (MG-69 to MG-78) isn't built
  yet; it has to go through the same check.
- **Daily cap.** At most 100 campaign copies a day (the account's own lower
  limit, if any, stays), with the reason shown in its send limit.
- **Stricter pause.** A tester's campaign is paused, and the account with
  it, at 2 complaints on the campaign at any size, or with 20 or more copies
  at over 2% hard bounces and at least 3 of them. Other accounts' limits
  (MG-79) still apply too.
- **SES mailbox simulator.** Copies and test sends to
  `*@simulator.amazonses.com` use no tester credits, and testers may test to
  them without a verified domain. For every account, events for simulator
  copies are marked `simulated` and left out of bounce and complaint rates,
  auto-pauses, throttles and the deliverability report, because SES leaves
  them out of the AWS account's reputation. They still show in the
  campaign's own report and still change their contact (a simulated bounce
  marks the contact bounced), so a tester sees what a bounce does. They
  count toward the 100-a-day cap and the 50-a-day test cap, and the ledger
  debits them as for any copy.
- **Where it shows.** A banner across the web app ("Tester account: N of 500
  credits left", or that it has ended), the same in `mailgoose account get`
  and `GET /v1/account` (`tester`), the admin account view, and "tester
  until" in the deliverability report.
- **Merging.** App-only, so the reviewer merges; no infra change.

Proposals for the owner, not built: a pause that looks at a tester's whole
life, not one campaign; leaving simulator copies out of the daily cap and
the ledger; a `mailgoose admin tester list`; an email a few days before a
tester account ends; and a link in the banner for reporting bugs.

## 2026-11-21: Staging deploys on merge from CodeBuild, not GitHub OIDC (owner ask; the route is a recommended, reversible default)

The owner asked what it takes to deploy staging automatically on merge.

- **Confirmed against AWS's published policies (Nov 21):** the managed
  service control policy of both the Free Tier and the Paid Plan carries
  `DenyIAMRestrictedActions`, which denies `iam:*Provider*` on every resource
  with no exception. That is `iam:CreateOpenIDConnectProvider`, so upgrading
  to the Paid plan does not lift it. AWS says neither policy can be modified;
  the only documented way out is activating advanced features, which needs the
  Paid plan first, cannot be reversed, removes the spend limit for good and
  makes the owner the administrator of the AWS organization. Matches the
  explicit deny seen on Nov 17 (policy p-6tzxgdae) and the Nov 18 entry below.
- **Recommended route, built behind `enable_codebuild_deploy`:** a CodeBuild
  project in the account (`infra/modules/codebuild-deploy`). A GitHub webhook
  filtered to `PUSH` on `^refs/heads/main$` starts it, so only merged code
  runs with its role, and pull requests (including from forks of this public
  repository) never do. It reaches GitHub through a CodeConnections connection
  (a GitHub App), runs `scripts/ci/codebuild-deploy.sh` and then the same
  `scripts/ci/deploy-ecs.sh`, and posts its result as a check on the commit.
  CodeBuild, CodeConnections, ECR and ECS are all on the Free plan's service
  list and allowed by its policy. No AWS keys exist anywhere. Its role gets the
  same permissions as the GitHub deploy role (now shared in
  `infra/modules/deploy-policy`), plus its log group and the connection.
- **Ruled out:** a CodeBuild-hosted GitHub Actions runner (any workflow in a
  public repository, including a fork's pull request, could ask for the runner
  and its AWS role; CodeBuild's runner webhooks can't filter by branch), an IAM
  user with access keys in GitHub (long-lived keys), IAM Roles Anywhere (not on
  the plan's service list, and its private key would live in GitHub anyway).
- **Overlapping merges:** up to two builds run at once; each checks that its
  commit is still `main`'s head right before it deploys and stops quietly if
  not, so the newer merge wins.
- **Slack posts (owner ask, Nov 21):** each CodeBuild deploy posts deployed or
  failed to `#deployments` through a Slack incoming webhook. The URL is a
  secret, so it sits in Secrets Manager (empty until set by hand, read by the
  build role only), not in the repo, Terraform state or GitHub: GitHub
  secrets would not reach a CodeBuild build.
- **After every deploy (both routes, and by hand):** `deploy-ecs.sh` waits
  for `/readyz` to answer 200 with the new commit when `READYZ_URL` and
  `EXPECT_COMMIT` are set, and fails the deploy otherwise.
- **Cost (inferred, check the AWS calculator):** a deploy takes about 10
  build minutes on ARM small. AWS's free tier covers 100 minutes a month on
  that size; past it the ARM small rate is roughly a third of a cent a
  minute, so a few cents a deploy, paid from the credits.
- **Not decided here:** whether to activate advanced features later and
  switch to `.github/workflows/deploy-staging.yml` with OIDC (still dormant,
  now also checks `/readyz`), and how prod deploys. Prod moves to its own AWS
  account before beta, where the same module works unchanged.

## 2026-11-21: Public site restyled to match the app, and new logos (owner ask; the details are reversible defaults)

The owner asked for another design pass on mailgoose.app so it matches the
redesigned app. The copy is unchanged; this is a styling pass.

- **One set of tokens.** `site/assets/tokens.css` is generated from the app's
  `web/src/styles.css` by `scripts/site/tokens.mjs`, and
  `web/src/pages/siteTokens.test.ts` fails if the two drift. `site.css` only
  lays them out, with the app's rules: white pages, near-black text, the blue
  for actions and state, hairline-and-shadow cards, 120ms color changes.
- **Inter**, self-hosted from the same `@fontsource-variable/inter` file the
  app uses (latin subset), with its licence beside it.
- **Dark mode follows the system**, as in the app, because the tokens carry
  it. Default; dropping it means a light-only tokens file for the site.
  (Superseded the same day: the site is light only, see above.)
- **New logos (owner's, added at the owner's ask).** The wordmark (goose
  head and word) replaces the text wordmark in the app's side menu and the
  site header, with a light-text version in dark mode. The whole goose
  replaces the old mascot on the site's hero, 404 page and social card. The
  head, cropped square, is the favicon for both. Files in `docs/brand/`. The
  sign-in, CLI approval and policy pages in the app use it too (the owner's
  yes). This covers the logo part of MG-84.
- **Small markup changes:** a skip link, Lucide icons on the feature cards
  (the ones the app's menu uses), the CLI sample in a terminal card, and the
  goose on the 404 page (as `docs/brand.md` suggests).

## 2026-11-20: Per-customer deliverability report (MG-82, owner decision; details are reversible defaults)

The owner decided to show each customer's bounce and complaint rates from
data mailgoose already records (`campaign_recipients` and `send_events`),
instead of paying for AWS's deliverability dashboard. The details below
were picked while building MG-82 and are easy to change.

- **Where.** `mailgoose admin deliverability` (`GET
  /v1/admin/deliverability`, admins only, `--limit` 1 to 100, default 50,
  and `--json`), and a section in the daily abuse digest (MG-67). No web UI.
- **Who is listed.** Every account that sent campaign email in the last 7
  days, or had a hard bounce or complaint reported in them; closed accounts
  too, if they sent. `total` counts them all.
- **How it's counted.** Exactly as the digest counts the platform's rates:
  copies sent in the window, and the distinct recipients whose permanent
  bounce or complaint (campaign email only) was reported in it, over the last
  24 hours and 7 days. The platform and the per-account report share the same
  SQL, so the accounts add up to the platform. A complaint reported this week
  on a copy sent earlier counts this week, so an account can show complaints
  with nothing sent (its rate is then null).
- **Rates** are percentages rounded to 4 places in the API, null when
  nothing was sent in the window.
- **Worst first.** Accounts over the early warning over 7 days first (the
  configured `EARLY_WARNING_*` thresholds, 0.05% complaints or 2% hard bounces
  by default, with the same minimums as one account: 500 copies and 2
  complaints), then by how far their 7-day rates are over those thresholds
  (the larger of the two, as a multiple of its threshold), then their
  24-hour rates the same way, then the most copies sent.
- **State.** Each row says whether the account is suspended, paused,
  throttled (until when) or in test mode.
- **The digest section** lists the accounts over the early warning over 7
  days, by the rule above, so a small account with one complaint isn't
  flagged; the section states the rule and is left out when none are. It
  lists at most 20, worst first, and says how many more there are. Each
  names `mailgoose admin account` and, unless it's paused or suspended,
  `mailgoose admin pause`. An account over the early warning sends the
  digest on an otherwise quiet day; the subject says how many there are.
  They aren't counted as digest items (`ops_digests.items`), which stay
  throttles, pauses, suspensions, held imports and waiting sign-ups.

## 2026-11-20: Block designer rebuilt around typing on the email (owner ask; details are reversible defaults)

The owner asked for the drag-and-drop builder to be rebuilt with a modern look
and a smooth feel, using Mailchimp's editor as the reference for dragging and
inline editing, but keeping mailgoose's own simple blocks and options. Only
that editor's on-screen behavior was studied. Details:
`docs/design-system.md`, "The email editor".

- **Inline editing.** Headings, text blocks and button labels are typed on the
  canvas. Text blocks still store the MG-19 Markdown subset; the editor
  converts to and from it (`web/src/designer/richtext.ts`). Bold inside italic
  (and the reverse) can't be said in the subset, so only the outer one is kept.
- **Formatting uses the browser's own editing commands**
  (`document.execCommand`), which every current browser supports but which is
  marked obsolete. Default; swapping in an editing library later only touches
  `inline.tsx`.
- **Full-window editor**, with the app's side menu hidden while designing.
- **No API changes.** Every edit still goes through the block operations.
- **Open:** undo and redo, which need history the API doesn't keep.

## 2026-11-20: SES account alarms on staging, with an early warning (owner decision; levels are reversible defaults, MG-81)

The owner chose CloudWatch alarms on the SES account's own rates, emailed to
the alert address, over AWS's paid deliverability dashboard (Virtual
Deliverability Manager), which stays off.

- **Staging watches them now.** `Reputation.BounceRate` and
  `Reputation.ComplaintRate` belong to the AWS account that staging and prod
  share, and prod isn't running yet, so until now nothing watched them.
  Staging turns `ses_reputation_alarms` on and prod turns it off. When prod
  goes live, flip both, so one set of alarms fires, not two.
- **Two levels each:** an early warning at 2.5% bounces and 0.05%
  complaints, and the alarm at 4% and 0.08% (unchanged from MG-53). All sit
  under AWS's review line of 5% and 0.1%. Each emails when it fires and
  again when it clears.
- **Cost:** SES publishes these metrics to CloudWatch at no charge; each
  alarm is about $0.10 a month, so the four are about $0.40.

## 2026-11-20: SES ignores tag conditions on sends; the app checks the sending identity (owner decision)

Found while bringing up staging. The owner chose the fix on Nov 20, and that
a separate prod AWS account comes before beta.

- **What happened.** Every sign-up on staging failed with a 403 from SES: the
  API's task role wasn't allowed `ses:SendEmail` on the system sending
  identity. The role allowed sends only from identities and the configuration
  set tagged `Environment=<env>`, and both were tagged. PR #172 worked around
  it for the system identity by naming it and the configuration set by exact
  ARN with no tag condition.
- **The test.** A throwaway role in the AWS project, with one send each to a
  verified address, tried the tag condition on both resources, on the
  configuration set only and on identities only. All three were denied, each
  naming the resource that carried the condition. So SES never matches
  `aws:ResourceTag/...` for `ses:SendEmail`, on identities or configuration
  sets, even when the tags are there. Only the system identity was tested,
  and no AWS page found says why.
- **What it broke.** The worker sends campaigns, and the API sends test
  emails and double opt-in confirmations, from customers' own domain
  identities, whose ARNs aren't known ahead of time. Their only send
  permission was the tag-conditioned one, so those sends were denied, in
  staging and in prod.
- **The choice.** Each environment's API and worker roles may send
  (`ses:SendEmail`, `ses:SendRawEmail`, and `ses:SendBulkEmail` for the
  worker) as any identity in the account, with no tag condition, but only
  through that environment's own configuration set, named by exact ARN. The
  app checks every message before it goes out (`mail.Guard`, wrapped around
  the sender in the API and the worker): the From domain must be the system
  sending domain, or a domain (or a subdomain of one) that the message's
  account verified in this environment's database. Anything else is refused
  and never reaches SES. The other options were a separate AWS account for
  prod now, or accepting the shared scope with no check.
- **What it means.** Staging and prod share one AWS account (2026-11-17), and
  the Environment tag was meant to keep each environment's role from sending
  as the other's identities. For sends, that isolation now rests on our code,
  not on IAM: a bug in the check, or a staging task that's compromised, could
  send as a prod customer's domain. The check holds because SES keeps one
  identity per name in the account and an environment won't adopt an
  identity tagged for another account (internal/domains), so a domain
  verified in one environment's database belongs to that environment. One
  gap: a send from a subdomain uses the most specific identity SES has, so a
  staging account that verified `acme.com` sending from `news.acme.com`
  would use prod's identity if prod had verified `news.acme.com`. That needs
  control of `acme.com`'s DNS to set up.
- **Not changed.** Creating, reading, changing and deleting identities keep
  their tag conditions (`aws:RequestTag` and `aws:ResourceTag`). After the
  fix was applied to staging on Nov 20, the owner added, checked and deleted
  a made-up domain through the CLI (`infra/README.md`, "Bringing up staging
  for testing", step 8): every call succeeded with no `AccessDenied`, so SES
  does match tag conditions on these calls, unlike sends. A call with the
  wrong environment's tag wasn't tried.
- **Not tested yet.** A send from a customer's domain, end to end: it needs
  a domain with real DNS records verified in staging.
- **Follow-up before beta (owner, Nov 20).** The shared account stays for now,
  but prod must have an AWS account of its own before the first beta
  customer. Shared SES reputation and quotas mean staging's bounces or
  complaints could get prod's sending paused, and send isolation rests on app
  code. The likely shape: keep the current account (SES production access in
  review) as prod, and move staging to a new account that can stay in the SES
  sandbox. No work on it until then.

## 2026-11-20: Web app UI overhaul: white, neutral, shadow-based (owner decision; the details are reversible defaults)

The owner asked for a full UI overhaul of the web app, modeled on the restraint
of Stripe, Airbnb and Shopify, in mailgoose's colors: white pages, near-black
text, the blue only for actions and state, a minimal box-shadow look, simple
interactions and simple icons in the sidebar. The email editor is being
redesigned separately and is left alone. Reasoning and tokens:
`docs/design-system.md`.

- **Palette changes.** Pages and cards are white and text is near-black, where
  the earlier tokens used cream surfaces and charcoal text. Cream and the warm
  shade are no longer used in the app. The blue stays the deep periwinkle
  (`#5562a3`); beak orange survives only as the dot in the wordmark.
- **Inter, self-hosted** (`@fontsource-variable/inter`), and **Lucide icons**
  (`lucide-react`), both pinned. The CSP needs no change.
- **Side menu** gets icons and two headings (Marketing, Account).
- **Dark mode is kept**, since the app already followed the system setting.
  (Nov 21: now a switch in Settings, light by default; see the Nov 21 entry.)
- **Open:** whether "the blue" is the periwinkle used here. Default: yes.

## 2026-11-20: Public site at mailgoose.app on GitHub Pages (owner ask; hosting is a reversible default)

The owner asked for a page at https://mailgoose.app that explains the product,
lightly branded, so AWS sees a real site in the SES production access request.
It replaces GoDaddy's "coming soon" page.

- **Where it lives.** `site/` in this repository: plain HTML and CSS, no build
  step. A landing page plus static copies of the four policy pages, generated
  from `web/src/pages/Policies.tsx` by `scripts/site/policies.mjs` and checked
  by `web/src/pages/sitePolicies.test.ts`. The policy text is still the
  placeholder outline.
- **Hosting (default).** GitHub Pages, deployed by
  `.github/workflows/pages.yml` on pushes to `main` that touch `site/`. Free
  for a public repository, HTTPS on the apex, no AWS and no CloudFront, so the
  CDN and OIDC blocks don't hold it up. DNS stays at GoDaddy: apex `A`/`AAAA`
  records and a `www` CNAME change; the Microsoft 365 records and the
  `staging` delegation don't. Owner steps: `docs/public-site.md`.
- **Not decided here.** Whether the marketing site moves into AWS or another
  host later, and the final design. This page doesn't need to match it.

## 2026-11-20: Pause and early-warning limits tightened below AWS's review line (owner decision; numbers are reversible defaults, MG-79)

AWS reviews the whole SES account, which every customer shares, at 0.1%
complaints or 5% bounces, and may pause it at 0.5% or 10%. The old
per-customer pause (0.3% complaints) let one customer pull the account into
review before mailgoose stopped it. The owner asked to tighten it; the exact
numbers below were picked while building MG-79 and are easy to change.

- **Pause:** over 0.1% complaints or 3% hard bounces on a campaign of 500 or
  more (was 0.3% and 5%). The same limits decide whether an account's last
  30 days are clean for its limit to grow.
- **Early warning (throttle):** over 0.05% complaints or 2% hard bounces
  (complaints were 0.1%). `EARLY_WARNING_COMPLAINT_PERCENT` now takes 0.01
  to under 0.1, and `EARLY_WARNING_HARD_BOUNCE_PERCENT` 0.01 to under 3, so
  the warning still fires before the pause.
- **At least 2 complaints.** At 0.1%, a single complaint on any campaign
  under 1,000 copies would pause it, and one complaint is chance. A full
  campaign's (and an account's 30-day) complaint rate counts only from two
  complaints. Hard bounces need no minimum: 2% of 500 is already 10.
- **Slices are unchanged in kind:** a new account's first slice (MG-62) is
  still judged on any number of copies and complaints, so one complaint in
  a 200-copy slice still pauses, as before. Whether a slice should need a
  few complaints first stays an open question.
- **The daily abuse digest** (MG-67) judges the platform's rates by the same
  early warning, two-complaint minimum included.
- **Account-wide alarms** on SES's own rates are a separate infra change for
  the owner to merge.


## 2026-11-20: Test mode (MG-80, owner decision; details are reversible defaults)

The owner asked for a test mode that only allows sending to a set of up to 5
test addresses: internal testing accounts are locked to it by default, and
full sending is turned on for the people who help test it.

- **Two modes.** Each account's sending mode is `test` or `full`
  (`accounts.sending_mode`, `sending_mode` on the account in the API).
  Accounts made before MG-80 keep full sending.
- **New accounts.** `NEW_ACCOUNTS_TEST_MODE` (`on` or `off`, default `on`)
  decides whether new accounts start in test mode. **Prod must set it to
  `off` before launch.** The local stack sets it `off`.
- **Who test mode sends to.** Confirmed test addresses and the owner's own
  sign-in address, which is always allowed and takes no slot (it can't be
  added as a test address). Addresses match ignoring case.
- **Test addresses.** Up to 5, pending ones included. Each is confirmed by
  an emailed link (hashed token, valid 7 days; a resend replaces the link and
  is allowed at most once a minute). Listed, added, resent and removed in
  the web app, the API (`/v1/test-addresses`) and the CLI (`mailgoose
  test-addresses`); confirming needs no sign-in. Adding an address and
  resending its link email someone, so they need a full-scope key or a
  sign-in; a draft-scope key can list and remove them (changed in review).
- **Confirm email cap.** An account sends at most 10 confirm emails in any
  24 hours, adds and resends alike (429 `test_address_rate_limited` with
  `Retry-After`). Each is logged in `test_address_emails`, which outlives
  the address, so removing and re-adding addresses can't be used to mail
  strangers from mailgoose's own sender. A send that fails still counts.
  The email's subject is fixed ("Confirm a test address on mailgoose");
  the account's name appears only in the body. (Added in review.)
- **What test mode refuses.** A launch is refused, not queued (problem code
  `test_mode`, with how many recipients aren't test addresses). A test send
  to anyone else is refused (422 `test_mode`); in test mode a test send to a
  confirmed test address doesn't need a verified-domain address. A queued
  copy for anyone else is skipped at send time and its credit given back,
  with the reason in the recipient's `error`. Forms take signups only from
  test addresses (403 `test_mode`). Every refusal says it's because of test
  mode.
- **Sign-up review.** A test-mode account still waiting for its review may
  launch to its test addresses and take their form signups. Allowing full
  sending doesn't approve the review (open question below).
- **Admin.** `mailgoose admin test-mode` lists accounts in test mode (not
  closed ones); `allow-full-sending` and `set-test-mode` switch one, logged
  in `admin_actions` as `allow_full_sending` and `set_test_mode`.
- **Visible.** The web app shows a banner in test mode; `mailgoose account`
  and `mailgoose admin account` show the mode.

## 2026-11-20: Import from Mailchimp (owner decision)

Bending Spoons announced on 2026-11-19 that it will buy Mailchimp, closing in
early 2027. The owner asked for an "import from Mailchimp" option so Mailchimp
customers can bring over their audiences, templates and so on, and for
marketing to focus on them.

- **In scope.** An import from Mailchimp joins v1's scope. It runs once (or
  again, idempotently); it is not a sync.
- **Spec and tickets.** The spec is the [Import from Mailchimp Claude
  Doc](https://claude.ai/artifact/8Me7gKuU325VinXAoiNqYF); tickets MG-69 to
  MG-78 are issues #153 to #162, label `M11 Mailchimp import`. The build
  picks them up after its current queue.
- **Public formats only.** It is built from Mailchimp's public export formats
  and Marketing API documentation.
- **Every existing rule applies.** Imported contacts go through the same
  limits, holds and checks as a CSV import (CON-5, CON-6, CON-9, MG-61 to
  MG-66). The import adds signals for review, never exceptions.

The product choices inside it are open questions for the owner, listed under
Open questions with the default the tickets build on.

## 2026-11-19: Ops alert address and daily abuse digest (MG-67, reversible defaults)

Picked while building MG-67; easy to change.

- **One address for alarms and app alerts.** Terraform sets `OPS_ALERT_EMAIL`
  on the API and worker tasks from the existing `alert_email` variable (set
  in each environment's git-ignored `alerts.auto.tfvars`), so CloudWatch
  alarms, the app's alerts and the digest all reach the same place. Empty
  leaves `OPS_ALERT_EMAIL` unset.
- **The digest goes at 13:00 UTC** (`ABUSE_DIGEST_HOUR`, 0 to 23): 8 AM US
  Eastern in winter, 9 AM in summer. A worker started after the hour sends
  that day's digest late rather than skipping the day.
- **What it lists.** Throttles, pauses and suspensions logged in
  `admin_actions` in the 24 hours before it's built, automatic and by an
  admin alike, each with whether it still holds; every import held for
  review and every sign-up waiting for review, however old, since they wait
  on a person; and the platform's campaign complaint and hard-bounce rates
  over 24 hours and 30 days, counted as for one account.
- **Rates alone send it only when over the early warning** (0.1% complaints
  or 2% hard bounces by default, the `EARLY_WARNING_*` settings), and only on
  500 or more copies, the same floor as one account's check. Otherwise a
  quiet day (no items) sends nothing.
- **At most once a day.** The first worker task to look after the hour claims
  the day in `ops_digests` and commits that before sending, so a crash or a
  failed send loses that day's digest (the failure is logged as an error)
  rather than sending it twice. A quiet day is claimed too: anything later
  that day is in the next day's digest.
- **No digest without an address.** With `OPS_ALERT_EMAIL` unset the job
  doesn't run and claims nothing.

## 2026-11-19: Sign-up review (MG-66, reversible defaults)

Picked while building MG-66; easy to change.

- **`SIGNUP_REVIEW` defaults to `all`**, as the ticket says, for the beta and
  launch month; `flagged` and `off` are the other values. The setting applies
  at sign-up: changing it doesn't release or hold accounts already made.
  Accounts made before this change have no review and aren't held.
- **The local stack (`make dev`) runs with `SIGNUP_REVIEW=off`** unless it's
  set, so a local sign-up can launch straight away. The end-to-end tests in
  CI use that stack and launch campaigns; the review itself is covered by the
  Go tests.
- **Flags.** A disposable address (the vendored list, parent domains
  included), the 4th or later account from one IP in 24 hours, or an IP or
  company domain shared with a suspended account. Free-mail domains (a short
  hand-made list in `internal/emaillists`) never count against each other.
  Unconfirmed sign-ups count towards the IP limit too.
- **The ops email** goes when the waiting account confirms its address (its
  first sign-in), not when the sign-up form is sent, so abandoned sign-ups
  don't email anyone.
- **What a waiting account can't do** is launch campaigns (problem
  `account_in_review`) and take form signups (403 `form_closed`). Everything
  else, test sends included, works as for any account.
- **Approve** takes a reason like every admin action; the CLI fills in
  "Sign-up approved." when none is given. **Reject** logs `reject_signup` and
  suspends the account through the same code as `mailgoose admin suspend`,
  with the rejection reason as the suspension's. Rejecting an account that's
  suspended already leaves its suspension as it was.
- The owner isn't emailed when their account is approved; the note in the
  web app goes away on their next sign-in or reload.

## 2026-11-19: Terms versions (MG-68, reversible defaults)

Picked while building MG-68; easy to change.

- **One version for all four policy pages.** Terms, privacy policy,
  anti-spam policy and DPA share one version, because sign-up accepts the
  terms and anti-spam policy together and the DPA comes with the terms. The
  versions, their effective dates and what changed in each are kept in
  `internal/policy/policy.go`; `make generate` copies them to the web app.
- **A version is a date, `YYYY-MM-DD`.** The first is `2026-11-19`, the day
  the policy pages were published (MG-57). Accounts that signed up before
  this change were backfilled with it, at the time they signed up, with
  `via` = `backfill`.
- **Every new version asks everyone again.** Adding a version to
  `internal/policy` makes every account accept it before its next launch
  (`new_terms_not_accepted`). A typo fix that shouldn't ask anyone again
  shouldn't get a new version. Telling accounts by email ahead of a change
  isn't built.
- **Only launch is blocked.** Test sends, editing and campaigns already
  launched or scheduled carry on. The web app shows a prompt in place of
  every page until the person accepts, with links to what changed
  (`/policies/changes`) and the terms; it doesn't stop sign-out.
- **Accepting takes the current version only**, named exactly
  (`mailgoose account accept-terms --version <v>`), so nobody accepts a
  version they didn't read. Accepting the version already accepted adds no
  row. It needs a session or a full-scope key.
- **How it was accepted** is recorded as `web` (sign-up or the prompt in the
  web app), `cli` (the CLI's sign-up, or any request the CLI makes, known by
  its User-Agent), `api` (another API key) or `backfill`.
- **The history is kept when an account is deleted**, like the admin log,
  since it's the record of what the account agreed to.

## 2026-11-19: Link reputation check (MG-64, reversible defaults)

Picked while building MG-64; easy to change. The vendor is an open question
for the owner (below).

- **Google Web Risk, looked up per link** with `uris:search` on the malware,
  social engineering and unwanted software lists, by API key
  (`WEB_RISK_API_KEY` in the `<env>/app` secret). The ticket's default vendor.
- **Fails open.** No key, an error or no answer within 2 seconds (for every
  lookup together) lets the links through with a `link_safety_not_checked`
  warning and a log line. Sending never waits on Web Risk.
- **Cached in each API task's memory**, keyed by a hash of the link: a listed
  link for the time Web Risk gives (at most a day), a clean one for 30
  minutes, since Web Risk gives no time for those. That's long enough for one
  campaign's check, test, summary and launch to share one lookup. A cache in
  Postgres, shared between tasks, can come later if lookups grow.
- **Checked at the email check, the test send and the launch.** A listed link
  is an `unsafe_link` error; it stops a test send (422) and a launch, and at
  launch (either the summary or the confirm) also pauses the account like an
  automatic pause: the reason names the campaign and the link, the admin log
  gets an `auto_pause` entry with the links, and the ops address an alert. An
  account already paused keeps its pause and reason, and no second entry or
  alert is made.
- **A tracked link is looked up by its target.** One of mailgoose's own
  `/t/c/` links pasted into an email is read for where it goes.
- **Locally, without a key**, a fake lists Google's test pages
  (`https://testsafebrowsing.appspot.com/s/malware.html`, `phishing.html`,
  `unwanted.html`), so the check can be tried. Tests use the fake; nothing in
  CI calls Google.
- **A merge tag can't decide where a link goes.** A link that was only a
  tag, such as `{{first_name|https://shop.example}}`, went wherever each
  contact's field pointed, and the check only ever saw the fallback. Now
  only `{{unsubscribe_url}}` may be a whole link. Other tags may come only
  after a fixed `https://host/` (in the path, query or fragment) or in a
  `mailto:` address. This covers button and image links, text-block links,
  and every `href` in your own HTML (a `merge_tag_link` markup error).
  Saving refuses such a link, and the email check, test send and launch
  refuse one saved earlier. This replaces the "or a merge tag" in the MG-19
  links rule below. Checking each contact's links at send time instead
  would cost a lookup for each distinct link in every batch.

## 2026-11-19: Import list checks and list source (MG-63, reversible defaults)

Picked while building MG-63; easy to change.

- **Five list sources**, stored as `form`, `customers`, `personal`, `export`
  and `other` (with a description of up to 200 characters). The API, the CLI
  (`mailgoose import --list-source`, a required flag) and the import page all
  require one. Imports made before this have none.
- **The source is each contact's consent record.** Every contact an import adds
  gets a consent record with source `import`, no IP, the import's id and time,
  and the list source, its description and the file name as its wording. The
  CSV export's signup columns stay for form signups only.
- **The thresholds are config values** (`IMPORT_HOLD_*`, see the README),
  defaulting to the ticket's numbers: role plus disposable over 10% of valid
  rows, invalid over 5% of all rows, shared at least 50% of the file's distinct
  addresses and at least 200. They're first guesses to tune with real imports.
- **The percentages apply to small files too.** A 20-row file with 2 bad rows
  is held. Easy to add a minimum size if that proves noisy.
- **One reason is recorded,** the first that applies: shared list, then risky
  addresses, then invalid rows, then a large first import. The report still
  counts everything.
- **Role addresses** are a vendored list of 65 local parts (info, sales,
  admin, support, noreply ...), matched ignoring `+tags`, dots, dashes and
  underscores. **Disposable domains** are the CC0
  `disposable-email-domains` list (about 10,300 domains, taken 2026-11-19),
  matched on the domain or any parent domain. Both live in
  `internal/emaillists` with their source and date; refreshing them is a file
  swap.
- **The shared-list check compares hashes.** Each import keeps a SHA-256 of
  each distinct address (with a fixed prefix, the same for every account, so
  accounts can be compared), for as long as the import exists. "Shared" counts
  the file's addresses that are in any other account's imports, and records the
  account with most of them. Only admins see the shared count and that account's
  id; the customer sees a plain message and never another account's contacts.
  Hashes of common addresses can be guessed, so they're treated as personal
  data and deleted with the import, the account, or an erased contact.

## 2026-11-19: Sliced first sends (MG-62, reversible defaults)

Picked while building MG-62; each is easy to change.

- **Who is sliced.** An account in its first 30 days, or before its fourth
  launched campaign, whichever ends later. Decided at launch, so a scheduled
  campaign keeps the slice it was launched with.
- **The slice.** 10% of the recipients, rounded up, at least 200, picked at
  random so a bad batch of contacts added last can't hide at the end of the
  list. Lists of 200 or fewer go out whole. `SLICE_PERCENT`, `SLICE_MIN` and
  `SLICE_WAIT` (default 60m) change them.
- **The check uses the slice alone, whatever its size.** The usual check
  needs 500 copies; a slice of 200 would never reach it. So one complaint in
  a slice of 200 (0.5%) pauses the account. Still open for the owner (see
  Open questions).
- **Over the pause limits:** the account is paused through the automatic
  pause, the rest is skipped and its credits come back, and the campaign
  ends as cancelled, with its slice marked `stopped`.
- **Over the early-warning limits only:** the account is throttled (MG-61)
  and the rest held until the throttle lifts, then checked again against
  the pause limits and released or stopped. A campaign throttled already
  waits for the account's current throttle.
- **The campaign's status stays `sending` while it waits;** where it stands
  is in a new `slice` object on the campaign (`sending_slice`, `slice_hold`,
  `held`, `released`, `stopped` or `cancelled`, with the release time).
  This keeps the status list as it is for clients that switch on it.
- **The account can cancel the rest while it waits,** with the usual cancel.
  Nobody else gets it, and the credits come back. A campaign cancelled,
  stopped or suspended before the rest went has its slice marked
  `cancelled`, so the page and the CLI don't still say the rest is coming.

## 2026-11-19: Account suspension (MG-65, reversible defaults)

Picked while building MG-65; easy to change. Suspension leaves credits alone,
as the ticket says.

- **What a suspended account can still do.** Its owner can sign in to the web
  app (new sign-ins work; existing sessions end), which shows only a page with
  why, how to appeal and two downloads: contacts as CSV and campaigns as JSON.
  For 30 days the API answers its session's GET requests, so the export works;
  after that only who's signed in, the account and signing out. Everything
  else is 403 `account_suspended`. API keys and CLI logins are revoked, and a
  CLI login (code or device) is refused while it's suspended.
- **Campaigns.** Scheduled and sending campaigns are cancelled, the same as an
  admin stopping them: the unsent reservations come back. A batch on its way
  stops before its next email. Forms answer 403 `form_closed`.
- **Unsuspend** restores sign-in and sending only. Keys stay revoked, stopped
  campaigns stay stopped, and a pause stays until `mailgoose admin unpause`.
- **Automatic suspension** happens in the same transaction as the second
  automatic pause in 30 days, counted from the `auto_pause` log (sending
  pauses only, not the past-due one, since MG-104). Its actor is
  `automatic`, logged as `auto_suspend`. Other triggers call the same
  `sending.Suspend`.
- **Appeals** go by replying to the suspension email, whose Reply-To is
  `ABUSE_EMAIL` (default `abuse@` the system sending domain). The operators
  get a copy at `OPS_ALERT_EMAIL`.

## 2026-11-19: Early-warning throttle (MG-61, reversible defaults)

Picked while building MG-61; each is easy to change.

- **Thresholds.** 0.1% spam complaints or 2% hard bounces on a campaign of
  500 or more, set by `EARLY_WARNING_COMPLAINT_PERCENT` and
  `EARLY_WARNING_HARD_BOUNCE_PERCENT`. Each must sit under the 0.3% and 5%
  that pause. (Tightened on 2026-11-20 to 0.05% and 2%, under 0.1% and 3%;
  see that entry.) Still open for the owner (see Open questions).
- **When it's checked.** On every bounce and complaint SES reports, like the
  pause check, so a campaign is throttled once it's past 500 copies and over
  the line, while it's still sending, and one whose complaints come in days
  later is still caught. Each campaign throttles its account at most once.
- **How long.** Seven days from the last campaign over the line; another one
  meanwhile starts the seven days again (and counts towards the pause). The
  ramp then starts over at the new-account limit and doubles weekly.
- **Staff-set limits.** A throttle lowers an account's own daily limit to the
  new-account limit too, unless its own is lower already.
- **Third throttle.** The third within 30 days pauses the account through the
  automatic pause, with a reason that says so, instead of throttling it.
- **Notices.** The owner is emailed from `notices@` the system sending domain;
  the ops address gets the same alert as a pause, marked as a throttle.

## 2026-11-19: The image CDN can be turned off; off on staging for now (owner decision)

AWS refused staging's CloudFront distribution ("Your account must be verified
before you can add new CloudFront resources"); only AWS Support can lift that.
The owner is opening a case and chose, in his words, "Make the CDN optional".

- **`images_cdn`** (stack variable, default true) turns CloudFront on or off
  per environment. Staging sets it to false until AWS allows CloudFront;
  prod keeps the default.
- **Without the CDN the bucket stays private.** No public access, no public
  bucket policy. The API serves each image at `PUBLIC_BASE_URL/images/<key>` by
  redirecting to a presigned GET URL that lasts a few minutes, so the URLs in
  sent emails never expire. The API role gains `s3:GetObject` on the bucket.
  (Reversible default: a redirect rather than streaming the file through the
  API, so the API sends no image bytes.)
- **Turning it back on is the one variable.** With the CDN, `IMAGES_BASE_URL`
  is the CloudFront address and new uploads use it; images uploaded without it
  keep their API URLs, which the API goes on serving by presigned redirects
  with the CDN on (MG-24 review).

## 2026-11-19: Accessibility and browser pass (MG-59, reversible defaults)

Picked while building MG-59; easy to change.

- **Every web unit test also runs axe.** `renderApp` checks the rendered
  page against WCAG 2.2 AA and axe's best practices after each test, so a
  new screen can't merge with a violation. jsdom does no layout, so color
  contrast is left to the browser check below; preview frames (the
  customer's own email or form) are skipped.
- **A real-browser axe pass in CI.** After the end-to-end flow, Chromium
  runs axe, contrast included, on every main page, signed in and out.
- **The manual browser pass is automated.** The end-to-end flow runs in
  Chromium, Firefox, WebKit (standing in for Safari) and Edge on every pull
  request. A check on a real Mac and iPhone is left for the launch
  checklist.
- **Fixes it found:** an empty designer column was an empty list, and a
  heading block with no text was an empty heading. On the sign-in, sign-up
  and policy pages the logo and footer sat outside any landmark; they're now
  in a header and footer.
- **The session cookie isn't Secure on the local stack.** WebKit won't keep a
  Secure cookie from `http://localhost`, so sign-in failed there. The cookie
  drops Secure only for a plain-HTTP request straight to localhost or a
  loopback address with no `X-Forwarded-Proto`; anything through the load
  balancer, staging and prod included, stays Secure.

## 2026-11-19: End-to-end tests (MG-58, reversible defaults)

Picked while building MG-58; easy to change.

- **The local worker plays SES's mailbox simulator.** With the fake sender
  and no SES events queue, mail to `success@`, `bounce@`, `ooto@`,
  `suppressionlist@` or `complaint@simulator.amazonses.com` (a `+label` is
  allowed) gets the event SES would publish, a second later, through the
  same handler as the real queue. So bounces and complaints are tested end
  to end with no AWS, and the same addresses work against staging's real
  SES.
- **The CLI test is a shell script** (`scripts/e2e-cli.sh`, `make e2e-cli`)
  that runs only `mailgoose ... --json`, plus `curl` for the one-click
  unsubscribe a mail client would make. It signs up with the emailed code,
  makes a full API key and uses only that from then on.
- **The browser test is Playwright, Chromium only**
  (`web/e2e/`, `make e2e-web`). The other browsers are MG-59's manual pass.
  (Changed in MG-59: it runs in four browsers.)
- **Both run in a new `End-to-end` CI job** against `scripts/dev.sh`, with
  Postgres as a service and mail written to `.local/mail`. The runner's own
  Node is used, as in the Go job.
- **The test emails have no links to other sites,** because the email check
  follows every link and the tests shouldn't depend on another site being
  up. Click tracking stays covered by the unit tests.
- **Against staging,** the CLI script needs the mail it reads from
  `.local/mail`; running it there waits for a way to read staging's mail.

## 2026-11-19: Monitoring and alarms (MG-53, reversible defaults)

Picked while building MG-53; easy to change. The owner applies it and picks
the address alarms go to.

- **Error tracking is CloudWatch, not Sentry.** The app already logs JSON
  lines that ECS ships to CloudWatch Logs; a metric filter counts the
  `ERROR` lines per service and alarms at 5 in 5 minutes. No new vendor,
  account, secret or data leaving AWS. Sentry (or similar) can be added
  later for stack traces and grouping without changing the alarms.
- **Worker log lines carry `queue` and `message_id`**, the worker's
  counterpart to the API's `request_id`, added from the context so a
  handler's own lines carry them too.
- **Alarms go to one SNS topic per environment, by email.** The address is
  set in a git-ignored `alerts.auto.tfvars`, so no personal address is
  committed. With none set, alarms only show in the CloudWatch console.
- **Thresholds**: a queue's oldest message over 15 minutes; any message on
  a dead-letter queue; over 5% of API requests failing with a 5xx in 5
  minutes, judged from 20 requests; no healthy API task for 5 minutes; no
  api or worker task reporting for 10 minutes.
- **SES bounce and complaint rate alarms are in prod only**, since those
  metrics cover the whole AWS account that staging shares. They fire at
  4% bounces and 0.08% complaints, under AWS's review levels of 5% and
  0.1%. They start when prod is first applied. (Moved to staging until prod
  runs, with an earlier warning level, on 2026-11-20: see that entry.)
- **"Task health" uses metrics ECS and the load balancer send for free**
  (the target group's healthy hosts, and whether each service reports CPU),
  rather than turning on Container Insights, which costs extra.
- **Cost**: 10 alarms (12 in prod) and 2 log metrics per environment, roughly $2
  a month each beyond CloudWatch's free tier (10 alarms, 10 metrics).

## 2026-11-19: Policy pages (MG-57, reversible defaults)

Picked while building MG-57; easy to change. The legal text itself is the
owner's to supply.

- **Four pages in the web app, open to anyone:** `/terms`, `/privacy`,
  `/anti-spam` and `/dpa`. Each starts with a "Placeholder" notice and lists,
  section by section, what the final text must cover. The anti-spam page
  already states the rules the app enforces (confirmed opt-in by default, no
  bought lists, unsubscribe link, postal address, held imports, account
  pauses on bounces and complaints).
- **Sign-up links to all four**, in a new tab so the form isn't lost. The
  terms and anti-spam policy are what the checkbox accepts; the privacy
  policy and DPA are linked under it. `mailgoose signup --help` names the
  pages too.
- **The DPA is a page, not a document to sign.** Customers accept it with
  the terms. A countersigned PDF for customers who ask can come later.
- **Which version of the terms was accepted isn't recorded yet;** accounts
  keep `terms_accepted_at` only. Once the real text exists, a version (or
  date) per acceptance is worth adding, so a later change can ask people to
  accept again. (Done in MG-68, above.)

## 2026-11-19: Agent docs (MG-55, reversible defaults)

Picked while building MG-55; easy to change.

- **The agent guide is one Markdown file in the CLI**
  (`internal/cli/agentguide.md`), printed by `mailgoose docs` and copied
  to `docs/api/agent-guide.md`. A test checks every command it shows
  exists, so it can't drift from the CLI.
- **The API reference and `llms.txt` are generated by our own small Go
  tool** (`make docs`) into `docs/api/`, committed, and a test fails when
  they fall behind `api/openapi.yaml` or the CLI. No new npm or hosted docs
  service. Each operation names the CLI command that calls it.
- **They aren't served on a public site yet.** Publishing them (for
  example at mailgoose.app/llms.txt) waits on the marketing site and prod
  DNS.
- **"Every command has an example" means every command that runs.**
  Command groups (`mailgoose email`) only list their subcommands, and help
  topics (`exit-codes`) run nothing.

## 2026-11-19: Dashboard (MG-51, reversible defaults)

Picked while building MG-51; easy to change.

- **Each checklist step is read from the account itself**: a verified
  domain, at least one contact, at least one email, a campaign with a test
  sent (or launched), and a launched campaign. The last two look at the 50
  newest campaigns. Nothing is stored about the checklist, so it can't
  drift from what's true.
- **The checklist hides once all five steps are done.**
- **The latest five campaigns are listed**, linking to the report once one
  has been launched, and to the campaign otherwise.

## 2026-11-19: Credits and billing page (MG-50, reversible defaults)

Picked while building MG-50; easy to change.

- **The ticket's "next fee date and amount" is the plan's next payment**,
  since the fee became a card subscription on Nov 18: the renewal date and
  what the contacts' plan units cost then, before sales tax. A cancelled
  plan shows when it ends instead.
- **The "paused for an unpaid fee" banner shows while the subscription is
  `past_due` or `unpaid`**, on every signed-in page, and links to this page.
  It reads the plan again on each page change, so it goes away once the
  payment goes through. A plan in any status other than `active` or
  `trialing` (such as `incomplete`, a first payment waiting on the bank) is
  marked "Payment pending" rather than "Active".
- **Payments open Stripe's own pages.** The page only goes to an https
  address on stripe.com, and says so if the API answers with anything else.
- **The pack buttons list the four decided packs** (BIL-3). A server set up
  with other packs still prices the purchase itself; the buttons would need
  an API that lists packs to follow it.
- **The page answers at both `/credits` (the menu, and where a credit pack's
  checkout returns) and `/billing` (where the plan's checkout returns)**, and
  the menu item is now "Credits and billing".

## 2026-11-19: Signup form pages (MG-49, reversible defaults)

Picked while building MG-49; easy to change.

- **A new form is made from its name alone**, with the API's defaults
  (email only, double opt-in on), and opened to set the rest.
- **Form options are saved with a button, not autosaved**, because a saved
  change reaches the live form on a site at once (its tags, message and
  confirmation email).
- **The preview is the saved form's own embed code**, in a sandboxed frame
  that runs no scripts and can't submit. It refreshes after each save, so it
  shows exactly what a site gets.
- **Fields are email (always), then first and last name**, each with its own
  label and a required switch. A form keeps the order it was made with.
- **A form that goes to a page keeps its saved message.** The message box is
  hidden in that mode, but the API still needs a message, so an emptied one
  is left as it was saved rather than failing on a field nobody can see.

## 2026-11-19: Report page (MG-48, reversible defaults)

Picked while building MG-48; easy to change.

- **The report shows the CLI's numbers in the CLI's words**: each count with
  its share of the copies sent to one decimal place, opens labeled
  "(estimated)" with a line on why, and clicks per link in the email's order.
- **Numbers are tiles and links a list, not tables**, so the page reads on a
  phone without scrolling sideways.
- **While a campaign is sending, the report counts again every 5 seconds**,
  and a "Count again" button does it on demand. An archived report has no
  button, since its numbers no longer change.
- **The report is linked from the campaign's page** once it has been
  launched and not cancelled.

## 2026-11-19: Campaign pages (MG-47, reversible defaults)

Picked while building MG-47; easy to change.

- **A campaign is made as a draft first**, on its own page (name, email,
  recipients, send time, tracking), then opened to check, test and launch.
  Nothing is sent until the launch summary is confirmed.
- **The recipient count is live**: 0.3 seconds after the recipients change,
  the page asks the API how many subscribed contacts they reach now.
- **Send times are picked in the viewer's own time zone** and sent to the API
  in UTC.
- **The email picker lists the 100 newest emails.** An account with more can
  rename or duplicate one to bring it to the top; a search can come later.
- **Launch uses the same two calls as the CLI and agents**: the first answers
  with the summary and a code, the page shows the summary, and its confirm
  button sends the code back. The code itself is never shown, since the
  button is the person's yes.
- **Cancelling a scheduled campaign takes a second press**, because it can't
  be undone; moving its send time doesn't.
- **A sending campaign's page looks again every 5 seconds** until it has
  finished. Its numbers are the report page's job (MG-48).

## 2026-11-19: HTML code editor (MG-46, reversible defaults)

Picked while building MG-46; easy to change.

- **The editor is CodeMirror 6** (the `codemirror`, `@codemirror/lang-html`,
  `@codemirror/lint`, `@codemirror/state` and `@codemirror/view` packages, at
  pinned versions), not Monaco, which is several times bigger. It's loaded
  only when an HTML email is opened, so other pages don't download it.
- **The live preview renders the typed code in the browser**, in a sandboxed
  frame that runs no scripts, with merge tags filled from the same sample
  contact as the server's preview. It updates 0.3 seconds after typing
  stops, so it never waits on the API.
- **The API checks the markup by saving it.** Each pause in typing saves the
  code (0.8 seconds, as in the designer). Errors come back with their lines,
  are marked in the editor and listed under it, and leave the saved email as
  it was, so a broken draft is never what gets sent. CSS warnings show the
  same way but don't stop the save.
- **An empty HTML email offers a small starter template** with the
  unsubscribe link in it. An uploaded `.html` file replaces the code.
- **The Emails page asks blocks or HTML** when an email is created.
- **HTML emails hide the background color and content width settings**,
  which only blocks emails use.

## 2026-11-19: Designer properties, preview and autosave (MG-45, reversible defaults)

Picked while building MG-45; easy to change.

- **Text blocks are edited on the email as their Markdown**, with Bold,
  Italic, Link, list and merge tag buttons that write it, not as a
  word-processor-style editor. The stored format is that Markdown, so what
  people see while typing is what the CLI and agents see. A richer editor can
  replace it later without changing the API.
- **Autosave runs 0.8 seconds after the last edit**, and straight away when
  another block is selected, the view changes, a block is added, moved or
  deleted, or the page is left. Saves go one at a time, each with the version
  the last one returned. The browser warns before closing a tab with unsaved
  edits.
- **A refused save keeps the edit on screen**, shows the API's reason and
  reads "Not saved" until the next edit saves. A save that meets a newer
  version (someone else saved) drops the edit and reloads, as in MG-44.
- **The merge tag menu takes an optional fallback** and doesn't offer the
  unsubscribe link in the subject, preview text or button text.
- **The preview is the server's render (MG-25)** in a sandboxed frame that
  runs no scripts, filled in with the API's sample contact.
- **The image library shows 24 images at a time**, newest first, with a
  More button.

## 2026-11-19: Designer canvas (MG-44, reversible defaults)

Picked while building MG-44; easy to change.

- **Drag and drop uses the browser's own drag events**, with no library.
  Every block also has Move up, Move down, Duplicate and Delete buttons, which
  are the keyboard and touch way to do the same things.
- **Each change is saved at once** through the block operations API, with the
  email's version. If the email changed elsewhere (another tab, the CLI, an
  agent), the designer says so and reloads instead of overwriting it.
- **Image, button and social blocks ask for their details before they're
  added**, because the API won't take them empty. Headings, text and columns
  go in with starter content to edit.
- **The canvas draws its own preview of each block**, not the server's HTML
  render, so it stays quick. The exact render comes with the preview in MG-45
  and later tickets.
- **An Emails page lists emails and creates block emails**, so the designer
  can be reached. HTML emails open to a note until their editor (MG-46).

## 2026-11-19: Image upload (MG-24, reversible defaults)

Picked while building MG-24; easy to change.

- **The file goes base64-encoded in JSON** (`POST /v1/images`), like every
  other request, so the CLI, agents and the web app share one way in.
  Multipart or a raw body can be added later without breaking it.
- **Up to 1,000 images per account**, so the CDN isn't free file hosting.
  The image's row is added under the account's lock before the file is
  stored, so uploads at once can't pass the cap, and removed again if
  storing fails.
- **No deleting single images.** Sent emails keep pointing at their images,
  so a delete would break them in people's inboxes. An account's images are
  deleted with the account.
- **SVG isn't accepted.** Many mail clients don't show it, and it can carry
  scripts.
- **Images aren't resized or re-encoded**; they're served as uploaded.
- **Storage is an S3 bucket in us-east-2 behind CloudFront**, files cached
  for a year. The bucket and CDN are MG-23 (merged and applied by the
  owner). The API task role has put, delete and list access there (deleting
  an account deletes its images), and the tasks get `IMAGES_BUCKET`, plus
  `IMAGES_BASE_URL` when the CDN is on. Without `IMAGES_BUCKET` the API
  answers 503 to uploads. Local dev stores them in `.local/images`.
- **Without a CDN** (no `IMAGES_BASE_URL`, as on staging while AWS withholds
  CloudFront; see "The image CDN can be turned off"), the API serves images at
  `PUBLIC_BASE_URL/images/<key>` with a 302 to a presigned URL that lasts five
  minutes; the redirect may be cached for a minute. The API role has
  `s3:GetObject` there to presign.

## 2026-11-18: Contacts and tags pages (MG-42, reversible defaults)

Picked while building MG-42; easy to change.

- **Search looks in one field at a time**, email or name, picked next to the
  search box (email by default). The API's filters combine with AND, so one
  box can't search both.
- **A contact added by hand is unconfirmed unless "I have their permission"
  is ticked**, the same choice the import asks for, so nobody is mailed
  without it.
- **Bulk tagging works on the contacts ticked on the page** (up to 50 at a
  time), not on everything a filter matches. The API takes a filter too, so
  "tag all matching" can come later.
- **Import column menus start from a guess** at the usual headers (Email,
  E-mail Address, First name, Surname, Tags and the like). A field left
  unmatched is left out of the request, so the API looks for it under its
  usual headers, as it does for the CLI.
- **Menus list up to 1,000 tags.**

## 2026-11-18: The paid plan is a card subscription (owner decision, confirmed)

The owner: "Deducting credits feels confusing. Maybe we just stick to a
monthly fee with the option to set up annual at sign up for a slight
discount", and "$50 a year is good".

- **The paid plan is a Stripe card subscription:** $5 a month, or $50 a
  year (two months free) chosen when starting it. This replaces the earlier
  decision to take the $5 fee from credits at launch with yearly card
  billing later (2026-11-17 and the MVP decisions below).
- **Unchanged:** credit packs at $20 per 10,000, lasting 24 months; buying
  credits doesn't touch the plan. The free tier stays at 500 contacts and
  1,000 credits a month. The plan still covers the first 2,500 contacts,
  with $5 a month ($50 a year) for each further 10,000 or part of it.
- **Growth mid-period is billed from the next renewal, with no proration**
  (the owner, Nov 19: "Yes to both defaults", to charging at renewal).

## 2026-11-18: Paid plan subscription (MG-40, reversible defaults)

Picked while building the subscription; easy to change.

- **Plan units.** The subscription is one price times a quantity: one unit
  for up to 2,500 counted contacts, one more per further 10,000 or part of
  it, so 2,501 to 12,500 contacts is two units ($10 a month or $100 a year).
- **Prices live in config** (`PLAN_PRICES`, default `month:500,year:5000`)
  and each Checkout session carries the price, like the packs: no Stripe
  products to set up. Tax is added on top, as for packs.
- **Units follow contacts hourly, billed from the next renewal** (Stripe
  `proration_behavior: none`). Growth mid-period isn't charged until the
  renewal; the owner confirmed this on Nov 19 (see the paid plan entry).
- **Credit packs need the paid plan:** a free account gets 409
  `plan_required` and is pointed at `mailgoose plan upgrade`.
- **A live subscription makes the account paid** (no free grant, no contact
  cap), replacing "paid from the first purchase" (MG-39). Past due and unpaid
  still count as live while Stripe retries the card.
- **Past due pauses sending** (code `subscription_past_due`, its own launch
  problem code) and the pause lifts by itself when the payment goes through
  or the subscription ends. A pause for another reason isn't touched. Both
  are in the admin log as automatic.
- **Cancelling is in Stripe's customer portal** (`mailgoose plan manage`)
  and runs to the end of the paid period; then the account is free again
  and keeps its bought credits. Switching between monthly and yearly is
  cancel and start again for now, since the portal can only switch between
  Stripe products.
- **Coming back** after cancelling reuses the same Stripe customer.
- **Deleting an account cancels its subscription at once**, with no refund
  for the rest of the period, before anything is deleted; if Stripe can't be
  reached the delete fails (502) and can be retried.
- **Event order doesn't matter:** on each subscription event the webhook
  reads the subscription from Stripe as it is now and applies that, so two
  events in one second arriving the wrong way round can't leave a paying
  account free. The read happens before the webhook's database
  transaction, so a slow Stripe holds no connection or account lock; a read
  older than the one already applied changes nothing, and a redelivered
  event isn't read again. If Stripe doesn't answer, the webhook answers 503
  and Stripe sends the event again. Without a Stripe client (or if Stripe
  doesn't know the subscription), the event's own copy is applied: an older
  event changes nothing, and a new subscription's `incomplete` state doesn't
  replace a live one from the same second. An old subscription ending
  doesn't touch the current one. A second live subscription for one account
  isn't applied.
- **Past due under another pause:** when staff lift another pause while
  the plan's payment is past due, the past-due pause takes over. Staff
  lifting the past-due pause itself stands.
- **Unit changes** use a Stripe idempotency key naming the units before and
  after and the run, so a change back to an earlier quantity later the same
  day isn't answered from the earlier request.
- **Payments to sort out by hand** (a payment that adds no credits, one for
  a closed account, a second live subscription, a closed account with a
  live subscription) are logged as errors and emailed to `OPS_ALERT_EMAIL`
  when it's set. The email goes once the event is recorded, so a failed
  attempt that Stripe retries doesn't send it twice.
- **The worker checks hourly** for unit changes and for credits past their
  expiry.

## 2026-11-18: Buying credits with Stripe Checkout (MG-39, reversible defaults)

Picked while building MG-39; easy to change.

- **No Stripe products.** Pack prices live in config (`CREDIT_PACKS`,
  defaulting to the decided packs) and each Checkout session carries its
  pack's price, so Stripe needs no products or prices set up, and changing
  a price is a config change.
- **A small Stripe client** of our own (one form post), not the Stripe SDK.
- **Sales tax is added on top** of the pack price (`tax_behavior:
  exclusive`), worked out by Stripe Tax from the billing address Checkout
  asks for. Whether prices should include tax instead is left open below.
- **Receipts** go to the account's sign-in address, set on the session.
- **Keys** come from the `<env>/app` secret. Only prod may use a live key
  and prod only a live one; a wrong key turns buying credits off (with a log
  line) instead of stopping the server.
- **The webhook trusts the pack, not the session alone:** a paid session
  adds credits only if its amount and currency match the pack's price. One
  that doesn't is logged as an error and adds nothing.
- **Price changes apply to new checkouts.** Each session carries the price
  and credits it was opened at, and a paid session is credited at those, so
  changing `CREDIT_PACKS` while a checkout is open (they last up to 24
  hours) doesn't strand a payment. Only mailgoose's own key can set a
  session's metadata.
- **A payment for an account that's gone or closed** adds nothing, is
  logged as an error to refund by hand, and answers Stripe with 200 so it
  isn't retried for days.
- **Paid from the first purchase:** the account becomes paid when its first
  pack is paid for, and `paid_since` records when (the monthly fee, MG-40,
  counts from it). It gets no more free monthly grants. (Replaced the same
  day: the subscription makes an account paid; see the paid plan entries
  above.)

## 2026-11-18: Campaign reports (MG-38, reversible defaults)

Picked while building MG-38; easy to change.

- **Counted when asked**, not rolled up: the report is one query over the
  stored events, so it's as fresh as they are (well inside 5 minutes).
- **Recipients** are the copies actually sent; skipped ones aren't counted.
- **Bounced** counts hard bounces only. Soft bounces that later delivered
  aren't bounces.
- **Unsubscribes** count people who used this campaign's link, once each.
- **Credits used** is what the campaign holds net of what came back; while
  it's sending that includes credits for copies still to go.
- **Untracked campaigns** report opens and clicks as null, not 0.
- **Per-link clicks** are by the link's position in the email. A link
  whose address differs per copy (merge tags) shows only its site, so no
  one person's link is in the report.
- **12-month retention** is per campaign: once a finished campaign was
  launched more than 12 months ago, its report is saved and all its raw
  events deleted together, so its numbers don't drift. Other events are
  deleted when they're 12 months old. Unsubscribe and consent records and
  the send records themselves are not raw events and are kept.
- **A campaign still sending or paused** past 12 months keeps all its raw
  events, so its live report doesn't drift; it's archived once it's sent,
  cancelled or failed. Two pruners archiving the same campaign at once save
  it once; the second skips it.

## 2026-11-18: Open and click tracking (MG-37, reversible defaults)

Picked while building MG-37; easy to change.

- **What's tracked:** links in the HTML part to `http` or `https`
  addresses, up to 2,000 characters. The unsubscribe link, `mailto:` and
  other links, and the plain-text part are left alone. Test sends aren't
  tracked.
- **Tracked after personalizing**, per copy, so links filled in by merge
  tags are tracked too.
- **The link carries its destination**, signed with the account's
  unsubscribe key under its own label, so the redirect needs no lookup and
  a changed link can't redirect anywhere.
- **A click counts as an open** when the copy has none yet, since many mail
  apps block the pixel.
- **No IP address or browser** is stored with an open or click.
- **A click keeps only the link's site and path,** not its query, fragment
  or user info, where merge tags usually land; a path holding an address
  keeps only the site. The reader still goes to the whole link.
- **An account with no link key** is remembered for a minute (up to 10,000
  of them), so forged tokens naming it don't each read the database.
- **The pixel always loads**, even for a bad token, so mail apps show no
  broken image; only valid tokens are recorded.
- **Per-IP limit** of 20,000 a minute on `/t/`, well above other recipient
  pages, because mail providers fetch images through a few proxies.
- **Turning SES's OPEN and CLICK events off** in `infra/` is a separate PR
  for the owner to merge and apply; until then the worker drops them, as
  it already does.

## 2026-11-18: Send limits, account health and admin tools (MG-34, reversible defaults)

Picked while building MG-34; easy to change.

- **Limit schedule:** 1,000 a day for the first 7 days, then doubling each
  week (2,000, 4,000, 8,000) up to the 10,000 default, while the account's
  last 30 days are clean. An account that isn't clean is held at 1,000.
- **Clean** means the account's campaigns over 30 days are under 0.3%
  complaints and 5% hard bounces (judged on 500 or more sent), and none of
  them paused it. (0.1% and 3% since 2026-11-20, MG-79.)
- **Rates count people**, not events: two complaints from one recipient
  count once.
- **A campaign pauses its account once.** After an admin lifts the pause,
  more bounces on the same campaign don't pause it again. An account an
  admin had already paused keeps that pause and its reason.
- **Ops alert:** `OPS_ALERT_EMAIL` is optional; unset, an automatic pause is
  only logged as an error. Setting it on the ECS tasks is an infra change
  for the owner.
- **Who is an admin:** a flag on the user, granted only with `mailgoosed
  admin grant` from the server. Admin endpoints take a person's sign-in (CLI
  token or web session), never an API key.
- **Every admin action needs a reason** (500 characters at most), logged
  with who and when in `admin_actions`, which is kept when the account is
  deleted.
- **Credit grants:** at most 1,000,000 at a time, lasting 24 months like
  bought credits. They don't stand in for the free monthly grant.
- **Stopping a campaign** ends it `cancelled`, its unsent rows `skipped`
  ("stopped by mailgoose") with their credits back.

## 2026-11-18: SES events (MG-33, reversible defaults)

Picked while building MG-33; easy to change.

- **Soft bounces "in a row"** are counted per contact, across campaigns,
  and a delivery to that contact clears the count. `Undetermined` bounces
  count as soft.
- **A complaint outranks a bounce:** a complained contact never becomes
  `bounced`. Both stop all sending to the address.
- **Which mail changes a contact:** campaign copies, and double opt-in
  confirmations (matched to the account's contact by address). Test sends
  and mailgoose's own mail (sign-in codes) are stored but change no
  contact.
- **A copy with no recorded message id** (sent, but the record failed, or
  the event came first) is matched by its campaign tag and the bounced or
  complained address, among that campaign's recipients only.
- **An unsubscribed contact:** soft bounces leave it unsubscribed, so a
  form can still bring it back. A hard bounce marks it `bounced`, since
  the address is gone.
- **Duplicates:** an event is stored once, keyed by a hash of its body;
  SNS and SQS can deliver the same one more than once.
- **Kept events:** `send_events` holds no address and goes when the
  account is deleted. Reports (MG-38) and account health (MG-34) read it.

## 2026-11-18: Scheduled campaigns (MG-32, reversible defaults)

Picked while building MG-32; easy to change.

- **Cancel and reschedule need a full-scope key,** like launch, since they
  act on a launched campaign. Cancel asks for `--yes` in the CLI.
- **Only before sending starts.** A campaign already `sending` can't be
  cancelled or moved here; stopping one in flight is the kill switch
  (MG-34).
- **Cancelling can't be undone.** The recipients are marked skipped and
  every reserved credit comes back; to send it after all, make a new
  campaign.
- **Starting due campaigns:** the worker checks every 15 seconds, and the
  send dispatcher (which runs in the API when there's an SQS queue) also
  starts one it reaches first. Either way a conditional update starts it
  once.
- **Time zones:** times are stored and returned in UTC. The CLI shows them
  in the account's time zone and reads a time without an offset in it. The
  web app has no campaign pages yet; they'll do the same.

## 2026-11-18: Send pipeline (MG-31, reversible defaults)

Picked while building MG-31; easy to change.

- **Limits:** 10 emails a second across all workers
  (`SEND_RATE_PER_SECOND`), and per account 10,000 in any 24 hours, 1,000 in
  its first 7 days (CMP-7's number). An account's own `daily_send_limit`
  column overrides both; MG-34 (account health) is what will raise or lower
  it. The 24 hours are a rolling window, not a calendar day.
- **The rate is global,** kept by one row in Postgres that every worker
  takes its next slot from, so adding workers never raises it. SES's own
  account quota stays the outer limit. Only a copy that will really go out
  takes a slot: a row is claimed first, so skipped rows and a redelivered
  batch's sent rows don't use one (MG-31 review follow-up, 2026-11-19).
- **A batch stays invisible on the queue while it runs.** The rate is
  shared, so a batch can outlast the queue's 120-second visibility timeout;
  the worker extends it every 60 seconds until the batch is done, rather
  than letting SQS deliver it again and use up a try (MG-31 review
  follow-up, 2026-11-19).
- **A send cut off mid-way is not retried.** If a worker dies between
  claiming a row and recording it sent, the copy may have gone out, so
  after 10 minutes the row ends `failed` and its credit comes back. Missing
  one email is preferred to sending two (CAM-8). A copy that went out but
  couldn't be recorded is logged at error level with its message id, so it
  can be matched up by hand.
- **A refused send is retried** with the batch, up to 5 tries (the queue's
  redrive limit); then the row is `failed`, its credit back, and the message
  lands in the dead-letter queue.
- **The dispatcher runs in the API** when there's an SQS queue, because only
  the API's role may send to it (no infra change), and in the worker when
  there isn't (local).
- **MG-31 starts due scheduled campaigns.** MG-32 keeps cancelling and
  rescheduling.
- **A campaign where nothing went out ends `failed`;** if any copy went out
  it ends `sent`, with the failures counted on its rows.

## 2026-11-18: Form submissions and double opt-in (MG-36, reversible defaults)

Picked while building MG-36; easy to change.

- **Bots learn nothing.** A filled honeypot or a post under 2 seconds after
  the page opened gets the same success answer as a person and is dropped.
  The 2-second check needs the embed script; a plain HTML post has only the
  honeypot and the signed stamp. A missing or forged stamp is a visible 400,
  since it means the site's code is broken, not a bot.
- **Limits:** 10 posts a minute per IP and 600 a minute per form, in memory
  per API task like the API's own limits. Over them is a 429.
- **The answer doesn't depend on the address.** New, already subscribed,
  unsubscribed, bounced: the person sees the form's success message or
  redirect, so a form can't be used to learn who's on a list.
- **A double opt-in form needs a verified From.** Without one it refuses
  signups (503 `form_not_ready`) rather than adding people it can't confirm,
  and `form get` lists the problem.
- **Unsubscribed addresses come back only by confirming,** through any form,
  with or without double opt-in. A contact deleted after unsubscribing is the
  same (its suppression hash). Bounced and complained addresses get no
  email and stay as they are.
- **Existing contacts:** the form's tags are added and names only fill in
  blanks; a signup never overwrites a name.
- **At most one confirmation email per contact an hour**; a resubmission in
  that hour gets the normal answer and no email. Confirm links last 7 days.
  The hour is stamped in the signup's transaction, before the email goes,
  and cleared if the send fails, so posts that arrive together still send
  one (MG-36 review follow-up, 2026-11-19).
- **Opening a confirm link changes nothing;** the page's button confirms,
  because mail scanners open links.
- **Over the free contact limit** a signup or a confirmation is refused
  (409); the person is asked to try later. Nothing tells the account owner
  yet; MG-51's dashboard can. While the account is full, every signup gets
  that answer, including one from an address already on the list, so the
  answer still doesn't show who's on it; an existing contact's tags aren't
  added then (MG-36 review follow-up, 2026-11-19).
- **Counts are columns on the form** (`signups`: posts taken, a repeat
  counts again; `confirmations`: links used that left the contact
  subscribed), not computed from consent records, so deleting contacts
  doesn't change them. A link used by a contact that has since bounced or
  complained is recorded on its consent but not counted (MG-36 review
  follow-up, 2026-11-19).
- **Consent records** keep the time, IP, form, the form's wording (name,
  field labels, button, and with double opt-in the confirmation subject and
  text) and the confirmation time. Deleting the form keeps them; deleting or
  erasing the contact deletes them. The data request lists them all and the
  CSV export adds four `signup_` columns from the latest.

## 2026-11-18: Three open questions settled (owner decision, confirmed)

The owner accepted the defaults:

- **Sending domains stay as they are for now.** An unverified domain isn't
  taken over by another account after 72 hours; an account keeps a domain
  it added until it removes it. (MG-13)
- **An account can't move an unsubscribed contact back by hand.** People
  who unsubscribed come back only by signing up again through a form and
  confirming (MG-14, MG-36).
- **A partial block of 10,000 contacts rounds up** in the monthly fee:
  2,501 to 12,500 counted contacts is one extra block (MG-40). (Still holds
  for the paid plan's units.)

## 2026-11-18: Launch (MG-30, reversible defaults)

Picked while building MG-30; easy to change.

- **Only drafts can be changed, tested or launched.** A scheduled campaign
  has been confirmed as it is, so it's locked like a sending one. This
  replaces MG-27's "a scheduled campaign keeps a send time" (it can't be
  edited at all now) and MG-29's "only draft and scheduled campaigns can be
  tested". Changing a scheduled campaign's time, or taking it back to a
  draft, waits for MG-32's reschedule and cancel.
- **Recipients are fixed at launch**, scheduled campaigns included: the
  credits reserved match the people it goes to. Contacts who unsubscribe or
  bounce before their copy goes are skipped and their credit given back
  (MG-31).
- **The send queue starts as a Postgres outbox** (`send_queue`), written in
  the launch transaction. MG-31 moves due rows onto SQS, so a launch can't
  be lost between the database and the queue.
- **One code per campaign**, 8 characters from an alphabet without look-alike
  letters, shown as `XXXX-XXXX`. A new summary replaces the old code. Codes
  allow 5 wrong tries, then a new summary is needed.
- **Links are checked at the summary, not at the confirm**, so confirming
  doesn't wait on other sites. Link problems are warnings anyway.
- **The recipient count may change between summary and confirm** (a new
  signup, say). The confirm reserves for the count at that moment and
  answers with it. Contact changes don't make the code stale, unless the
  audience grew by more than a tenth (next bullet).
- **A code is refused when the audience grew by more than a tenth** of the
  summary's count, rounded up (MG-30 review follow-up, added 2026-11-19).
  The code remembers the summary's count; past the allowance the confirm
  answers `invalid_confirmation` with reason `audience_grew`, the count
  summarized and the count now, and nothing is launched. Fewer recipients
  than summarized always launch. The allowance lets a few signups during
  the 15 minutes through without a new summary. Open for the owner (see
  Open questions).
- **A campaign that reaches nobody, or whose send time has passed, is
  refused** rather than launched to nothing or sent late.
- **Accounts get `sending_paused_at` and `sending_paused_reason`** now, so
  launch can refuse a paused account; MG-34 sets them.

## 2026-11-18: Test sends (MG-29, reversible defaults)

Picked while building MG-29; easy to change.

- **Who a test can go to:** the account owner's address, or any address on
  a verified sending domain of the account, subdomains included, up to 5
  per test. Anything else is refused, so a test can't be used to mail
  strangers.
- **Tests skip no one silently; a suppressed address is refused.** An
  address whose contact unsubscribed, bounced or complained, or that a
  deleted contact left on the suppression list, gets
  `test_recipient_suppressed` rather than a test, since a send there costs
  reputation like any other. The owner's own address is never refused.
  (Added after the MG-29 review.)
- **50 a day is a rolling 24 hours**, counted per message, not per request.
  ~~A test that's counted and then fails at the mail provider still counts,
  so retries can't get round the limit.~~ Changed Nov 21 (MG-101, above):
  only a copy that may have gone still counts.
- **Only draft and scheduled campaigns can be tested.** A campaign that's
  sending or done gets a 409, before any readiness check.
- **A test must pass the same checks as a launch** (verified From, subject,
  postal address, merge tags), with every problem listed at once, so what's
  tested is what will send.
- **Merge tags come from the contact with the test address** when there is
  one; otherwise only the address is known and fallbacks show.
- **A test's unsubscribe link goes to `/u/test`**, which explains it was a
  test and changes nothing, rather than unsubscribing the tester.
- **The campaign records its last test's time and the campaign and email
  versions it was made from** (`last_test`), so MG-30 can tell whether
  anything changed since.
- **The API server now builds its mail sender whenever it has a database**,
  not only when it signs people in with its own sending domain, because
  test sends need it. Locally it's still the fake sender, which keeps mail
  in a folder.

## 2026-11-18: Unsubscribe links (MG-28, reversible defaults)

Picked while building MG-28; easy to change.

- **Opening the link doesn't unsubscribe; one click on its page does.**
  Mail security scanners open links in emails, so a GET that unsubscribed
  would remove people who never clicked. The mail app's one-click button
  (RFC 8058) posts and needs no page.
- **Tokens never expire** and name the account, contact and campaign,
  signed with a per-account key kept in the database (`unsubscribe_keys`),
  so no new server secret is needed and the API and workers can both make
  and check them. A tampered token fails its signature before anything is
  looked up.
- **Unsubscribe pages sit on the app's URL** (`WebURL`, `/u/<token>`), the
  same origin as the web app. The send workers (MG-31) need that URL to
  build links.
- **Unsubscribing records** the time, campaign and source (`link` or
  `one_click`). Records outlive the contact and the campaign, so a report's
  unsubscribe count (MG-38) stays right. Bounced and complained contacts
  stay as they are; an unconfirmed contact becomes unsubscribed.
- **The done page points to the account's signup form to come back.** No
  "resubscribe" button, which keeps the open question on resubscribing
  open.
- **The pages show the address and the account's name**, so a person with
  more than one address knows which one left.
- **The recipient pages are limited per IP only** (1,200 a minute), whatever
  Authorization header a request carries, so a client can't get a fresh
  bucket per header. A recipient over the limit gets a page with
  Retry-After, not the API's JSON, and a limited POST is logged as a
  warning so blocked one-click unsubscribes show up. (Added after the MG-28
  review.)

## 2026-11-18: Campaigns (MG-27, reversible defaults)

Picked while building MG-27; easy to change.

- **Recipients** are `{"all": true}` or `{"tag_ids": [...]}`, up to 20 tags,
  matched as any. A new campaign must say which; there's no default, so
  nothing goes to everyone by accident. A campaign whose only tags are
  deleted reaches nobody until it's given new recipients; it never falls
  back to everyone.
- **Status moves:** draft to scheduled or sending (launch); scheduled to
  sending, back to draft (unschedule) or cancelled; sending to sent,
  paused, failed or cancelled; paused to sending, cancelled or failed.
  Sent, cancelled and failed are final. Launch, the scheduler and pausing
  (MG-30 to MG-34) make the moves through `campaigns.SetStatus`; no
  endpoint changes status yet.
- **Only drafts can be deleted.** Anything past draft stays for its record;
  a scheduled campaign is cancelled instead (MG-32).
- **A scheduled campaign keeps a send time.** Setting it to "when launched"
  is refused; unscheduling comes with MG-32.
- **Send times** must be in the future and at most a year ahead.
- **Every edit moves `version` on**, and deleting a tag moves the version
  of the draft and scheduled campaigns that had it, so a launch
  confirmation code (MG-30) can go stale when the campaign changes.
  `version` on an update is optional; when given, a stale one is refused.
- **Deleting an email leaves its draft campaigns with no email**
  (`email_id: null`), which launch will refuse. Launched campaigns get a
  copy of the email (MG-30), so they're not affected.
- **`recipient_count`** is counted on every single-campaign response, not
  stored. Lists leave it out to stay fast.

## 2026-11-18: Signup forms (MG-35, reversible defaults)

Picked while building MG-35; easy to change.

- **Form posts go to `/f/{form_id}`** on the app's URL. MG-36 takes them.
  A post that asks for JSON gets `{message, redirect_url}` back, or the
  usual error; a plain post gets a redirect.
- **Each form has its own signing key** for the embed's timestamp, so no
  new server secret or infrastructure is needed. Deleting the form drops
  the key.
- **The timestamp is signed when the embed code is made**, not when the
  page loads, since a form with no script can't ask for one. The optional
  script also sends how long the page was open (`_mg_ms`), which MG-36 can
  use for the minimum time to submit. MG-36 may add a token endpoint if
  that proves too weak.
- **The honeypot field is named `website`**, hidden off screen and skipped
  by keyboards and screen readers.
- **Redirects must be https.**
- **Field order and labels** are set with `--file` (or the API); the flags
  cover the common cases: `--first-name` and `--last-name` take off,
  optional or required.

## 2026-11-19: Image storage bucket and CDN (MG-23, reversible defaults)

The owner asked for this as a PR to merge (Nov 19). Picked while writing it;
easy to change.

- **CDN address:** the distribution's own `*.cloudfront.net` name. A name
  like `img.mailgoose.app` needs an ACM certificate in us-east-1, which the
  AWS project's Region rule doesn't allow, so it waits.
- **Price class 100** (North America and Europe edges), the cheapest. Images
  still load everywhere, just from farther away outside those regions.
- **SSE-S3 encryption**, public access blocked, bucket owner enforced, and a
  bucket policy that refuses anything but TLS. Only the distribution may read.
- **The API task role** may put, delete and list objects in the bucket
  (uploads, and deleting an account's images). The worker gets no access.
- **Staging's bucket can be destroyed with images in it**; prod's can't
  (`force_destroy` follows `deletion_protection`).
- **No versioning:** an image's name never changes what it holds, and
  images are only deleted with their account.

## 2026-11-19: Email check hardening (MG-26 follow-up, reversible defaults)

From the MG-26 review; easy to change.

- **Link checks dial ports 80 and 443 only.** A link on another port is
  reported as not checked, so the check can't be used to probe other
  services from our addresses.
- **One account's checks fetch links at most 10 times a minute.** Checks
  past that still run and report everything else; their links come back as
  not checked, with when to try again. The limit is on the check endpoint
  only.
- **More ranges count as internal:** 240.0.0.0/4 (reserved), 2002::/16
  (6to4) and fec0::/10 (old site-local).
- **On a blocks email**, `missing_unsubscribe` reports the path `blocks`.

## 2026-11-18: Email check (MG-26, reversible defaults)

Picked while building MG-26; easy to change.

- **Which link problems block a launch:** a link that isn't a full web
  address, a page that answers 404 or 410, or a site whose name doesn't
  exist. Other error statuses (403, 429, 5xx) and timeouts are warnings,
  since many sites refuse automated requests. `http://` links are a
  warning.
- **Images without alt text are a warning**, not an error.
- **Size:** over 102 KB as rendered is an error; over 90 KB is a warning,
  since link tracking adds a little at send time.
- **Link limits:** 3 seconds per link, 10 seconds in all, 8 at a time, up
  to 50 links and 5 redirects. Links beyond those are reported as not
  checked.
- **Private addresses are never fetched.** The guard runs on the address
  each connection dials, with no proxy, so DNS names and redirects can't
  get around it.
- **The From address checked** is the email's own, else the account's
  default.
- **The check answers 200 with `ok: false`** when there are errors; a
  draft-scoped key may run it.

## 2026-11-18: Email preview (MG-25, reversible defaults)

Picked while building MG-25; easy to change.

- **Sample contact:** Alex Rivera, alex@example.com, with the unsubscribe
  link pointing at `#unsubscribe`. Previewing as a real contact can come
  with the email check or the designer.
- **No postal address yet** shows a placeholder footer in the preview
  instead of refusing it, so an email can be designed before Settings is
  filled in. Sending still refuses (CMP-2).
- **Mobile** is a wrapper page with the email in a 375 pixel iframe, so the
  email's own media queries apply in any browser.
- **Merge tag problems refuse the preview** (422 `invalid_merge_tags`)
  rather than showing the raw tag, so the same mistake can't slip through
  to a send.
- **The preview page is sandboxed** by its Content-Security-Policy: no
  scripts, forms or requests other than images, styles and fonts.
- **`--open` only at a terminal**, so an agent running the CLI never
  launches a browser on the person's machine by accident.

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

- ~~**Social links render as text** ("Instagram · Website"), not icons, until
  image hosting lands (MG-23). Icons need hosted images.~~ Replaced by the
  icons of MG-109 (2026-11-21).
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
  with MG-36 and should add their columns here (done: the `signup_`
  columns).
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
  (Done in MG-27.)
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
  resubscribe someone who unsubscribed, with a consent note? (Settled
  2026-11-18: no.)
- **The suppression list keeps a SHA-256 of the account id and the address**,
  not the address. Re-adding a suppressed address, or changing a contact's
  address to one, gives it the suppressed status and removes the entry,
  since the contact now carries the status.
- **Search:** `email` and `name` match any part, ignoring case; `status` is
  exact. Lists are newest first. The tag filter comes with tags in MG-15.
  (Done in MG-15: `tag_id`.)
- **Campaigns pick recipients only through `contacts.Recipients`,** which
  returns subscribed contacts and nothing else. (MG-27 added
  `contacts.CountRecipients`, which counts the same way.)

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
  with MG-57. (Done in MG-57, Nov 19.)

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
  fee taken from credits. Yearly card billing comes after the MVP. (Replaced
  the same day: the paid plan is a card subscription, $5 a month or $50 a
  year. See the entry at the top.)
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
  alternative. (Replaced 2026-11-18: a card subscription, $5 a month or $50 a
  year.)
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

- Test mode: should the owner's own sign-in address be allowed without
  taking one of the 5 test-address slots? (Default: yes, always allowed and
  takes no slot. MG-80.)
- Test mode and sign-up review: may a test-mode account still waiting for
  review launch to its test addresses, and should allowing full sending also
  approve the review? (Default: it may launch to test addresses; allowing
  full sending doesn't approve the review. MG-80.)
- Mailchimp import: do imported subscribed contacts skip re-confirmation?
  (Default: yes, once the permission box is ticked, with Mailchimp's opt-in
  and confirm times and IPs in each consent record. MG-70.)
- Mailchimp import: how are unsubscribed and cleaned contacts kept? (Default:
  as ordinary contacts with status unsubscribed or bounced, not counted, and
  imported even when the rest is held. Other option: a hashed suppression
  entry only. MG-70.)
- Mailchimp import: several Mailchimp audiences into mailgoose's one audience.
  (Default: merged, with an `audience: <name>` tag; an address unsubscribed or
  cleaned in any audience is suppressed. MG-70.)
- Mailchimp import: size and pricing. (Default: no special pricing; the
  preview shows the plan needed; an import over the limit is held until the
  subscription covers it; at most 250,000 contacts per import during the beta.
  A switcher offer is a marketing call. MG-70, MG-77.)
- Mailchimp import: does a large import need the owner's approval? (Default:
  yes, through the existing holds, unchanged, with Mailchimp signals shown on
  held imports. MG-77.)
- Mailchimp import: merge fields beyond name (address, phone, birthday,
  custom). (Default: dropped and listed in the preview. MG-69, MG-70.)
- Mailchimp import: pending contacts. (Default: unconfirmed, no email sent.
  MG-70.)
- Mailchimp import: the OAuth path needs a registered Mailchimp app. (Default:
  the upload path ships first; the owner registers the app before MG-73 goes
  live. MG-73.)
- Mailchimp import: contacts already in mailgoose. (Default: they only gain
  tags, and a subscribed or unconfirmed one is unsubscribed or bounced when
  Mailchimp says so; nothing is put back on the list. MG-70.)
- Mailchimp import: after an import that waited for the paid plan carries
  on, are its contacts still held for an admin's approval? (Default: yes,
  like a CSV import over the limit. Other option: release them once the plan
  covers them. MG-70.)
- Mailchimp import: which CSV columns are groups. (Default: only the columns
  the customer names; any other column is dropped and listed. MG-69, MG-72.)
- Mailchimp import: how many past campaigns come over as emails, and what
  happens to `*|IF:...|*` blocks. (Default: the newest 10, up to 100 if the
  customer asks; conditionals are left in with an error to fix by hand.
  MG-71.)
- How does prod get DNS? Default is to delegate only `api`, `app` and `mail`
  (see 2026-11-18); awaiting the owner's confirmation and the GoDaddy step.
  (Blocks MG-60.)
- Keep CodeBuild for CI deploys, or activate advanced features (irreversible)
  and switch to GitHub OIDC? Default: CodeBuild deploys staging on every push
  to `main` (2026-11-21). Prod's deploy route is open until it has its own
  account.
- Launch confirm: how far may the audience grow between the summary and
  the confirm before the code is refused? (Default: a tenth of the
  summary's count, rounded up. Other options: refuse any growth, or allow
  any growth the credits cover. MG-30 review follow-up.)
- Anything in the earlier mailgoose repo worth reusing? (Default: no, fresh
  start.)
- Deleting an account that still has bought credits: are they refunded or
  forfeited? (Default: forfeited; the ledger keeps them as billing records.
  MG-43, for MG-39 and MG-40 to settle.)
- Early-warning thresholds that throttle an account: 0.05% complaints and 2%
  hard bounces, under a pause at 0.1% and 3%, counting complaints from two?
  (Default: those; the warning ones are config values. The owner asked to
  tighten them below AWS's review line; the numbers are MG-79's pick, see
  2026-11-20.)
- A new account's first slice is checked against the pause limits however
  small it is, so one complaint in 200 pauses the account: is that strict
  enough to keep, or should a slice need a few complaints first? (Default:
  as the ticket says, the same limits on the slice alone. MG-62, see
  2026-11-19.)
- Should pack prices include sales tax, or have it added at checkout?
  (Default: added on top, which is usual for US software. See 2026-11-18,
  MG-39.)
- Which address takes suspension appeals? (Default: `ABUSE_EMAIL`, else
  `abuse@` the system sending domain, which has to be a real mailbox or
  alias. MG-65.)
- Which vendor checks links for phishing and malware? (Default: Google Web
  Risk, licensed for commercial use and free up to 100,000 lookups a month.
  MG-64.)
