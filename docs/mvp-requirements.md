# mailgoose MVP requirements

> Snapshot of the [mailgoose MVP requirements Claude Doc](https://claude.ai/artifact/3HoHQGD5cGMo8SCjwmVqpZ) as of Nov 18, 2026. The Claude Doc is the live version; if they differ, trust the doc and refresh this copy.

Nov 18, 2026 · @Carlton

## How to read this

This turns the PRD into what the MVP must do, each piece with acceptance criteria that a ticket can be checked against. Signed off on Nov 18, 2026. The ticket breakdown is in `docs/mvp-tickets.md`.

- **(stated)** comes from Carlton: the PRD call, the decisions log or the pricing choice.
- **(inferred)** started as Claude's proposal or standard practice. Since sign-off it is agreed scope; the tag only shows where the idea came from.
- **[D1] to [D10]** mark spots tied to one of the decisions at the end. All ten were settled on Nov 18.
- IDs like **AUTH-1** are there so tickets can point back to a requirement.

## What the MVP is

The MVP is the smallest mailgoose a stranger could sign up for, verify a domain, build an email (in the UI or through their agent with the CLI), send it to a tagged list, collect new subscribers with a form and see basic results, all without us touching anything by hand. (inferred definition)

It is done when:

1. A new account goes from signup to a delivered campaign in under 15 minutes, in the web app and separately with only the CLI. (inferred target, from the PRD)
2. Every API endpoint has a CLI command, and CI fails when one is missing. (stated goal, inferred enforcement)
3. Unsubscribes, bounces and complaints are handled automatically, and no message goes to a suppressed address. (inferred)
4. It runs on staging and prod in us-east-2, with SES out of the sandbox for prod. (stated region, inferred launch gate)
5. Credits are counted and enforced, and the free tier limits hold. [D1] (stated pricing)

## What is out of the MVP

Everything here is out, on purpose. (stated: extras out of scope at first; exact list inferred)

- Automations of any kind, including a welcome email. Very simple automations are the first candidate after MVP. (stated)
- Team members, roles and approval flows. One person per account. [D3]
- Segments beyond tags, custom contact fields beyond first and last name, and contact activity timelines.
- A/B tests, send-time optimization, resends to non-openers.
- Landing pages, hosted signup pages beyond the confirm and unsubscribe pages that compliance needs, and popups.
- Templates gallery. One or two starter layouts at most.
- Dedicated IPs, custom tracking domains, and a deliverability dashboard.
- Integrations, webhooks to customers, Zapier, ecommerce.
- An MCP server. [D8]
- Built-in AI writing.
- Reports beyond the per-campaign numbers below, and any cross-campaign analytics.
- Yearly card billing for the account fee. [D1]
- Click-to-edit text in the HTML preview. [D4]

## 1. Accounts and sign-in

One account is one sender with one person signed in. [D3]

- **AUTH-1 Sign up.** Email address, account name and acceptance of the terms and anti-spam policy. Signing up sends a sign-in code to that address; the account isn't active until the code is used. [D2]
- **AUTH-2 Sign in to the web app** with an emailed one-time code. Codes expire after 10 minutes, work once and are rate limited by address and IP. Sessions last 30 days and can be signed out everywhere. [D2] (inferred)
- **AUTH-3 CLI sign-in.** `mailgoose login` starts a device flow: it prints a short code and a URL, the person approves it in a signed-in browser, and the CLI stores a token in the OS keychain, or a config file with 0600 permissions where there's no keychain. (inferred)
- **AUTH-4 API keys.** Created, named, listed and revoked in the UI and CLI. Two scopes: full, and draft-only (everything except launching campaigns, changing billing and managing keys). The secret is shown once and stored hashed. `MAILGOOSE_API_KEY` in the environment wins over a stored login. (inferred)
- **AUTH-5 Account settings.** Account name, default From name and address, postal address (required before any send), time zone. (inferred)
- **AUTH-6 Delete account.** Deletes contacts, emails and campaigns within 30 days, keeps only billing records the law requires. Needs a typed confirmation in the UI and a `--confirm` flag in the CLI. (inferred)

Acceptance:

- A new address can sign up, receive a code, sign in and land on an empty dashboard.
- A used or expired code is refused with a clear message.
- A draft-only key gets a 403 on launch and on key management, and every other call works.
- A revoked key fails on the next request.

## 2. Sending domain

No account sends from an address it hasn't proven it controls, on the free tier or paid. (stated for free tier, inferred for all)

- **DOM-1 Add a domain** in the UI or `mailgoose domain add example.com`. We create an SES identity with Easy DKIM and a custom MAIL FROM subdomain, and show the exact DNS records to add: 3 DKIM CNAMEs, the MAIL FROM MX and SPF TXT, and a suggested DMARC TXT if none exists. (inferred)
- **DOM-2 Verify.** `mailgoose domain verify` and a Verify button check the records and report each one as found, missing or wrong. We also re-check in the background every few minutes for 72 hours. (inferred)
- **DOM-3 From addresses** must be on a verified domain. A campaign can't be launched with an unverified From. (inferred)
- **DOM-4 Free tier** may send only from a verified domain, never from a shared mailgoose domain. (stated)

Acceptance:

- With correct DNS, a domain turns verified without support help, and the UI and CLI both show it.
- With a missing DKIM record, verify names the missing record.
- Launching from an unverified domain fails with an error that says how to fix it.

## 3. Contacts and tags

One audience per account, with tags doing the work that lists do elsewhere. [D7]

- **CON-1 Fields:** email (unique per account, stored lowercase), first name, last name, tags, status, created date, source (form, import, API, manual). (inferred)
- **CON-2 Statuses:** unconfirmed, subscribed, unsubscribed, bounced, complained. Only subscribed contacts receive campaigns. (inferred)
- **CON-3 Add, edit, delete** single contacts in the UI, CLI and API. Delete is a real delete, leaving only a hashed suppression entry so a deleted address that unsubscribed or bounced isn't mailed again by mistake. (inferred)
- **CON-4 Tags:** create, rename, delete, add to and remove from contacts, one at a time or in bulk. Basic tags only. (stated)
- **CON-5 CSV import:** maps columns to fields, applies tags to everyone in the file, skips invalid and duplicate addresses with a report. Imported contacts are subscribed only if the person ticks a box confirming they have permission; otherwise they stay unconfirmed. (inferred)
- **CON-6 Import review:** a first import over 2,000 contacts, or any import that would put the account over its contact limit, is held for review before those contacts can be mailed. (inferred, from PRD abuse controls)
- **CON-7 CSV export** of all contacts or a tag, including status and consent fields. (inferred)
- **CON-8 Search and filter** the list by email, name, tag and status, paginated. (inferred)
- **CON-9 Contact limits:** the free tier holds at most 500 contacts. Paid accounts include 2,500, and each extra 10,000 adds $5 a month to the fee. Unsubscribed, bounced and complained contacts don't count. (stated limits, inferred counting)

Acceptance:

- Importing a 10,000-row CSV with 3% bad rows finishes in under a minute and reports every skipped row and why.
- A free account can't go past 500 counted contacts by any route: UI, CLI, API, form or import.
- Exported CSV re-imports cleanly.

## 4. Email designer

An email is either a block document or a raw HTML email. Both end up as email-safe HTML plus a plain-text part, rendered on the server. (stated designer, inferred model)

### Blocks

- **DES-1 Block document.** Stored as versioned JSON: email settings (subject, preview text, From, reply-to, background, content width) and an ordered list of blocks, each with a stable ID. The JSON schema is published in the OpenAPI spec. (inferred)
- **DES-2 Block types:** heading, text (bold, italic, links, lists), image, button, divider, spacer, two-column row (blocks inside each column), social links, and a locked footer with postal address and unsubscribe link. (inferred)
- **DES-3 Drag-and-drop editor** in the web app: add, reorder, duplicate, delete and edit blocks, with a properties panel and autosave. (stated)
- **DES-4 Blocks from the CLI:** list, add (at a position), update, move and remove blocks by ID, plus get and replace the whole document as JSON. (stated)
- **DES-5 Images** upload to our storage and are served from our CDN. JPEG, PNG, GIF and WebP, up to 5 MB each. (inferred)
- **DES-6 Preview** at desktop and mobile widths in the UI, and `mailgoose email preview` writes the rendered HTML to a file or opens it in a browser. (inferred)
- **DES-7 Merge tags:** first name, last name and email with a fallback, plus the unsubscribe link. Unknown tags fail the check. (inferred)
- **DES-8 Rendering** produces table-based HTML that holds up in Gmail, Apple Mail, Outlook (desktop and web) and Yahoo, at both widths. (inferred)

### Bring your own HTML

- **DES-9 Import HTML** in the UI (paste or upload) or `mailgoose email import-html`. It stays HTML and is not converted into blocks; the CLI replaces it as a whole. (stated import, inferred handling)
- **DES-10 Code editor** with a live preview that updates as they type. Code is the only way to edit it. (stated) [D4]
- **DES-11 Validation** before it can be saved as sendable: well-formed markup, no scripts, forms, iframes or external CSS, an unsubscribe merge tag present, size under 102 KB, and warnings (not errors) for CSS that major clients drop. We add the postal address footer if it's missing. (inferred)

### Checks

- **DES-12 Email check** in the UI and `mailgoose email check` for both kinds: missing subject, missing unsubscribe, missing postal address, broken or unsafe links, images without alt text, unknown merge tags, unverified From, size. Errors block a launch; warnings don't. (inferred)

Acceptance:

- An agent can build a four-block email from an empty draft using only CLI commands, and the UI shows the same email.
- An edit made in the UI shows up in `mailgoose email get --json` straight away, and the reverse.
- An imported HTML file with a `<script>` is refused with the line number.
- The starter blocks render without broken layout in the four client families above (checked by hand or with a rendering service before launch).

## 5. Campaigns and sending

A campaign is one email, a set of recipients and a send time. (inferred)

- **CAM-1 Create** a campaign from an email: name, recipients (all subscribed, or contacts with any of the chosen tags), send now or at a scheduled time. The email is copied into the campaign at launch so later edits don't change what went out. (inferred)
- **CAM-2 Test send** to the account's own email address, or up to 5 addresses on its verified domain. Test sends are free, up to 50 a day. (stated test send, inferred limits)
- **CAM-3 Launch summary.** Launching first shows the subject, From, recipient count, send time and credit cost, and refuses if no test send has gone out since the last edit. (stated confirm step, inferred summary)
- **CAM-4 Confirm to launch.** In the UI, a confirm button on that summary. In the CLI, `campaign launch` without confirmation returns the summary and a one-time code; `campaign launch --confirm <code>` launches. An agent shows the summary to its person and relaunches with the code after they say yes. Codes last 15 minutes and go stale if the campaign changes. [D5] (stated rule, inferred mechanism)
- **CAM-5 Credits are reserved** at launch for every recipient and refunded for any that we skip before sending (suppressed or invalid). A launch with too few credits is refused and says how many are short. (inferred)
- **CAM-6 Scheduled campaigns** can be cancelled or rescheduled until they start. (inferred)
- **CAM-7 Sending pipeline:** launch enqueues batches; workers render per recipient, add List-Unsubscribe headers, and send through SES within the account's send limit and our SES quota. A campaign to 10,000 contacts finishes within an hour on a warmed account. [D6] (inferred)
- **CAM-8 Exactly once per recipient:** retries never send a second copy, and launching the same campaign twice is a no-op. Create and launch accept an idempotency key. (inferred)
- **CAM-9 Statuses:** draft, scheduled, sending, sent, cancelled, paused (by us), failed. (inferred)

Acceptance:

- Launching without a test send, without a confirm, or without enough credits all fail with clear errors in the UI, CLI and API.
- A worker killed mid-campaign resumes without duplicates.
- A contact who unsubscribes after launch but before their batch goes out is skipped.

## 6. Compliance and deliverability

These are launch blockers. One bad sender on shared IPs hurts every customer. (inferred, from PRD)

- **CMP-1 Unsubscribe.** Every campaign email has a footer link the customer can't remove and RFC 8058 one-click List-Unsubscribe headers. Both take effect immediately, show a hosted confirmation page, and need no sign-in. (inferred)
- **CMP-2 Postal address** in every footer, taken from account settings. (inferred, CAN-SPAM)
- **CMP-3 Consent record** per contact: time, IP, source, form ID and the wording shown, plus when they confirmed. (inferred, GDPR)
- **CMP-4 Bounces:** a hard bounce suppresses the address at once. Three soft bounces in a row suppress it. (inferred)
- **CMP-5 Complaints:** a complaint suppresses the address at once. (inferred)
- **CMP-6 Account health:** each campaign's bounce and complaint rates are tracked. An account is paused automatically when complaints pass 0.3% or hard bounces pass 5% on a campaign of 500 or more, and we're notified. (inferred thresholds)
- **CMP-7 Send limits** start low and grow with clean history: new accounts can send to at most 1,000 recipients a day for the first week, then the limit grows. (inferred numbers)
- **CMP-8 Kill switch:** we can pause any account's sending at once, and stop a campaign in flight. (inferred)
- **CMP-9 Data requests:** export and delete a single contact's data on request, from the UI and CLI. (inferred)
- **CMP-10 Policies:** terms, privacy policy, anti-spam policy (no bought lists) and a DPA, linked from signup. Content, not code. (inferred)

Acceptance:

- One-click unsubscribe works when tested with Gmail's header check and a plain POST.
- An SES bounce or complaint notification turns the contact's status within a minute.
- A paused account can't launch, and its in-flight campaign stops before the next batch.

## 7. Signup forms

We generate embed code; double opt-in is on by default. (stated)

- **FRM-1 Create forms** in the UI, CLI and API: name, fields (email required, first and last name optional), tags applied on signup, button text, success message or redirect URL, and double opt-in on or off. (inferred options)
- **FRM-2 Embed code:** a plain HTML form that posts to our endpoint and works without JavaScript, plus an optional small script for inline success messages. Basic styling that inherits the site's fonts. (inferred)
- **FRM-3 Double opt-in:** the confirmation email is sent from the account's verified domain with editable subject and text. Confirming moves the contact to subscribed and records it. Confirmation emails don't use credits. (stated DOI, inferred rest)
- **FRM-4 Bot protection:** a honeypot field, a minimum time to submit, and rate limits per IP and per form. (inferred)
- **FRM-5 Existing contacts:** signing up again with a subscribed address adds the form's tags and doesn't send a second confirmation. An unsubscribed address can re-subscribe only through double opt-in. (inferred)
- **FRM-6 Counts:** each form shows signups and confirmations. (inferred)

Acceptance:

- Pasting the embed code into a static HTML page on another domain collects a signup with no extra setup.
- With double opt-in on, the contact is unconfirmed until they click, and gets no campaigns until then.
- 100 submissions a minute from one IP are throttled.

## 8. Reports

Basic per-campaign numbers only. (stated)

- **REP-1 Numbers per campaign:** recipients, delivered, bounced, opens (unique, labeled as estimated because of Apple Mail Privacy Protection), clicks (unique and total), unsubscribes, complaints, and credits used. (inferred list)
- **REP-2 Clicks by link** for each campaign. (inferred)
- **REP-3 Tracking:** open pixel and click redirects, on by default, with a per-campaign switch to turn both off. [D9]
- **REP-4 Same in the CLI:** `mailgoose campaign report` prints the table, or JSON with `--json`. (inferred)
- **REP-5 Freshness:** numbers update within 5 minutes of the event. Raw events are kept 12 months. (inferred)

Acceptance:

- Numbers in the UI, CLI and API match for the same campaign.
- A click on a tracked link redirects in under 200 ms and is counted once per contact for unique clicks.

## 9. Credits and billing

The pricing is decided; this is how the MVP enforces it. (stated prices) [D1]

- **BIL-1 Credit ledger:** every grant, purchase, reservation, refund, fee and expiry is a ledger entry; the balance is their sum. One credit is one email to one recipient. Test sends and confirmation emails are free. (inferred)
- **BIL-2 Free tier:** 1,000 credits a month that don't roll over, and 500 contacts. (stated)
- **BIL-3 Buying credits** through Stripe Checkout: $20 for 10,000, $75 for 50,000, $240 for 200,000, $900 for 1,000,000. Bought credits last 24 months and are used oldest first. (stated prices and expiry, inferred order)
- **BIL-4 Account fee:** a paid account pays $5 a month, including 2,500 contacts, plus $5 per extra 10,000 contacts, taken from credits on the billing date at the credit price (2,500 credits per $5). If the balance can't cover it, sending pauses until it can; contacts and emails are kept. (stated fee and default, inferred mechanics)
- **BIL-5 When an account becomes paid:** on its first purchase. It then stops getting the monthly free grant. (inferred)
- **BIL-6 Balance and history** in the UI and `mailgoose credits` in the CLI. Purchases happen in the browser; the CLI prints a link. (inferred)
- **BIL-7 Receipts** from Stripe. Sales tax handled by Stripe Tax. (inferred)

Acceptance:

- A test purchase in Stripe test mode adds the right credits within a minute of payment.
- The ledger always sums to the shown balance, and a launch can never take the balance below zero.
- Credits bought 24 months ago expire on the day, and expiry shows in history.

## 10. API

One public REST API that the web app, CLI and agents all use. (inferred) [D10]

- **API-1** Versioned under `/v1`, JSON in and out, described by an OpenAPI 3.1 spec that is the source of truth. The web app's TypeScript client and the CLI's commands are generated from or checked against it. (inferred)
- **API-2** Auth by session for the web app and by bearer token (API key or CLI token) for everything else. (inferred)
- **API-3** One error shape with a stable code, a message and a hint. Cursor pagination on every list. (inferred)
- **API-4** Idempotency keys on creates and on launch. (inferred)
- **API-5** Rate limits per key, with limit headers. (inferred)
- **API-6** Public docs generated from the spec, plus an llms.txt. (inferred)

Acceptance:

- The spec validates in CI, and a contract test calls every endpoint in it.
- No web app page calls an endpoint that isn't in the spec.

## 11. CLI

The CLI's core job is letting an agent build emails block by block, then test and launch them. (stated)

- **CLI-1 Parity:** every API endpoint has a command, and CI fails when one is missing. (stated goal, inferred enforcement)
- **CLI-2 Command groups:** login, logout, whoami, account, keys, domain, contacts, tags, import, export, email (including blocks, import-html, check, preview), campaign (including test, launch, cancel, report), form (including embed), credits, docs. (inferred)
- **CLI-3 Output:** tables in a terminal, `--json` on every command with stable schemas, and documented exit codes (0 ok, 1 error, 2 usage, 3 auth, 4 not found, 5 needs confirmation). (inferred)
- **CLI-4 No hidden prompts:** every prompt has a flag, and with no terminal attached the CLI never waits for input. (inferred)
- **CLI-5 Agent docs:** full `--help` with examples on every command, and `mailgoose docs` prints a Markdown guide for agents, including the launch confirmation flow. (inferred)
- **CLI-6 Distribution:** single static binaries for macOS (arm64, amd64), Linux (arm64, amd64) and Windows (amd64) on GitHub Releases, a Homebrew tap and an install script. `mailgoose version` and an update notice. (inferred)
- **CLI-7 Environments:** `--api-url` or `MAILGOOSE_API_URL` to point at staging or local. (inferred)

Acceptance:

- A scripted run with only the CLI goes from login to a delivered campaign, with no prompts once a key is set.
- Every command's `--json` output has a schema test.

## 12. Web app

The web app covers the same ground as the CLI, with nothing extra. (stated parity)

- **WEB-1 Pages:** sign up and sign in, a dashboard with a getting-started checklist (verify domain, add contacts, build an email, test, launch), contacts, tags, emails (designer and HTML editor), campaigns, campaign report, forms, domains, API keys, credits and billing, settings. (inferred)
- **WEB-2** Works in current Chrome, Safari, Firefox and Edge on desktop. Mobile is readable for reports and contacts; the designer is desktop only. (inferred)
- **WEB-3** Keyboard reachable and labeled for screen readers to WCAG 2.1 AA on the main flows. (inferred)

Acceptance:

- A new person finishes the getting-started checklist without docs.

## 13. Running it

(all inferred)

- **OPS-1 Environments:** staging and prod from the Terraform on main, in one AWS account in us-east-2, with no prod database standby until there are paying customers. (stated)
- **OPS-2 Deploys** from GitHub Actions through the OIDC deploy role on merge to main (staging) and on a tag (prod). No agent holds AWS keys. (stated rule, inferred triggers)
- **OPS-3 Migrations** run before the new API version takes traffic, and are reversible or additive.
- **OPS-4 Monitoring:** health checks, error tracking, logs with request IDs, and alarms on queue age, dead-letter queue depth, SES bounce and complaint rates and 5xx rate.
- **OPS-5 Backups:** daily database snapshots kept 7 days, and one restore tested before launch.
- **OPS-6 Admin tools:** internal CLI commands behind an admin role to find an account, pause or unpause sending, approve a held import and grant credits. No admin web UI in the MVP.
- **OPS-7 Security:** TLS everywhere, secrets in Secrets Manager, API keys and tokens hashed, contact data encrypted at rest, dependency and secret scanning in CI.
- **OPS-8 SES production access** requested and granted before public launch.
- **OPS-9 Local development:** one command brings up Postgres and the services locally, with a fake SES that captures mail.

## Decisions

Decided by Carlton on Nov 18, 2026: all ten as below.

1. **[D1] Billing in the MVP:** Stripe credit packs and the $5 fee from credits ship in the MVP; yearly card billing comes after.
2. **[D2] Web sign-in:** emailed one-time codes, no passwords. Google sign-in and passwords come later if people ask.
3. **[D3] Team members:** one person per account in the MVP.
4. **[D4] Click-to-edit in the HTML preview:** no, code only, for the MVP.
5. **[D5] Agent launch confirmation:** the one-time code flow in CAM-4.
6. **[D6] Send queue:** SQS, which the Terraform already has, rather than River on Postgres.
7. **[D7] Audience:** one audience per account, with tags.
8. **[D8] MCP server:** CLI only for the MVP.
9. **[D9] Tracking:** opens and clicks tracked by default, with a per-campaign switch off.
10. **[D10] Tech:** RDS Postgres, ECS Fargate, SES with SNS events, Terraform, one REST API with OpenAPI, and designs as JSON blocks rendered server-side.
