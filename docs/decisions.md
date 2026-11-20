# Decisions log

Newest first. Each entry says who decided and whether it's confirmed. Anything
marked **recommended** is a proposal the owner hasn't confirmed yet; treat it as
the default until the owner does, but don't build anything hard to undo on it.

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
  automatic pause in 30 days, counted from the `auto_pause` log. Its actor is
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
  0.1%. They start when prod is first applied.
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
  A test that's counted and then fails at the mail provider still counts,
  so retries can't get round the limit.
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
- How does prod get DNS? Default is to delegate only `api`, `app` and `mail`
  (see 2026-11-18); awaiting the owner's confirmation and the GoDaddy step.
  (Blocks MG-60.)
- How does CI deploy without AWS keys in GitHub while the AWS project blocks
  the OIDC role? Activate advanced features, or run a self-hosted runner or
  CodeBuild in the account; default is manual `make push-ecr`. (See
  2026-11-17 and 2026-11-18. Blocks MG-52.)
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
