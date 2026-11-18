# mailgoose PRD

> Snapshot of the [mailgoose PRD Claude Doc](https://claude.ai/artifact/KSX9yAxv3jgmT1R1LqCUMq) as of Nov 17, 2026. The Claude Doc is the live version; if they differ, trust the doc and refresh this copy.

Nov 17, 2026 · Gremlin Technologies

## Summary

mailgoose is a deliberately small email marketing SaaS: design an email, send it to a list, and collect subscribers with signup forms. Nothing else. It ships on day one with a full-featured CLI at parity with the UI, so customers can drive it from whichever AI agent they already use.

How to read this doc: lines marked **(stated)** come straight from the product concept or the Nov 17 scoping call. Lines marked **(inferred)** are my proposals or assumptions and need a yes, no or edit. Unmarked lines are standard practice for any sender in this space.

## Problem and positioning

The big email tools have become platforms, and small senders pay for and wade through all of it to send one newsletter. Mailchimp, Klaviyo and HubSpot bundle CRM, journeys, ecommerce, ads and landing pages around the core job of writing an email and sending it to a list. **(inferred framing)**

- **Deliberately small:** a solid designer, a list with tags, a send button, a signup form. Simple, feature-poor, a non-platform. **(stated)**
- **Agent-native instead of AI-featured:** no built-in AI writer. A CLI at full parity with the UI lets the customer's own agent design and send. **(stated)**
- **Fresh start:** an earlier mailgoose repo exists on GitHub, but this PRD is not bound by it. **(stated)**
- **Nearest comparables:** Buttondown, EmailOctopus, Sendy, Loops and Kit's free tier are also small. None treats the designer's blocks as something an agent edits from a terminal, which is the wedge. **(inferred, not researched in depth)**

## Target users

The first customers are people who already work with an AI agent and want email without a suite. **(inferred)**

| Segment | Who | Why mailgoose |
| --- | --- | --- |
| Developer-founders | Indie makers and small SaaS teams living in Claude Code, Cursor or Codex | Launch notes and newsletters without leaving the terminal |
| Small businesses and creators | One or two people sending a monthly newsletter | Suites feel heavy and expensive for what they use |
| Freelancers and agencies | People producing client emails with LLMs | Agent builds the email, human checks the test send |

Not targeted in v1: ecommerce stores that need revenue attribution, and teams that need multi-user approval workflows. **(inferred)**

## Goals and non-goals

v1 wins by doing the core job well and staying out of everything else.

**Goals**

1. Everything doable in the web app is doable from the CLI, and both ship at launch. **(stated)**
2. An agent can build and edit an email in the designer, test it and launch it, all from the CLI. **(stated)**
3. A new account sends its first campaign in under 15 minutes. **(inferred target)**
4. Good deliverability by default, with compliance built in rather than configured. **(inferred)**
5. Simple credit-based pricing that doesn't give away unlimited storage. **(stated)**

**Non-goals for v1** (all out of scope initially **(stated)**; the exact list below is my reconstruction **(inferred)**)

- Automations and journeys. Very simple automations may come later, such as a welcome email. **(stated)**
- CRM, deal pipelines and contact scoring
- Ecommerce integrations and revenue attribution
- Segmentation beyond tags
- A/B testing and send-time optimization
- Landing pages, websites and hosted surveys
- SMS, push and social posting
- A built-in AI writing assistant (the customer's agent does this)
- Reporting beyond the basic campaign numbers

## v1 scope

v1 is five things: a designer, a contact list with tags, campaign sending, signup forms and basic reports, all on one public API.

**Email designer**

- Drag-and-drop block editor in the web app. **(stated)**
- Bring your own HTML: we parse it and check it is valid before it can be sent. **(stated)** Imported HTML is edited in a code editor whose WYSIWYG preview updates as they type. **(stated)** It stays HTML rather than being converted into blocks, and the CLI replaces it as a whole. **(inferred)** Checks: well-formed markup, unsubscribe link present, no scripts or forms, under Gmail's \~102 KB clipping size, warnings for CSS that major clients drop. **(inferred)**
- Blocks are creatable and editable from the CLI, not just the whole email. **(stated)** So the email is stored as a JSON block document that the UI and CLI edit alike, rendered server-side to email-safe HTML plus a plain-text part. **(inferred)**
- Starter block types: heading, text, image, button, divider, spacer, two-column row, social links, and a locked footer holding the postal address and unsubscribe link. **(inferred)**
- Desktop and mobile preview. **(inferred)**

**Contacts**

- Basic tagging. **(stated)**
- One audience per account, with tags doing the work lists do elsewhere. **(inferred)**
- CSV import and export, add, edit, delete. Fields: email, first name, last name. Statuses: unconfirmed, subscribed, unsubscribed, bounced, complained. **(inferred)**

**Sending**

- A campaign is an email, recipients (everyone or by tag) and a send time, now or scheduled. **(inferred)**
- Test send to yourself, then an explicit confirm to launch, in the UI and the CLI. **(stated)**
- Merge tags for first name and the unsubscribe link. **(inferred)**

**Signup forms**

- We generate form code for the customer to embed. **(stated)**
- Double opt-in on by default. **(stated)**
- Options: which fields, tags applied on signup, redirect URL, confirmation email copy. Bot protection with a honeypot field and rate limits. **(inferred)**

**Reports**

- Basic per-campaign numbers only. **(stated)** Delivered, opens, clicks, bounces, unsubscribes and complaints, with opens labeled as inflated by Apple Mail Privacy Protection. **(inferred list)**

**API**

- One public REST API that the web app and CLI both call, authenticated with API keys. **(inferred)**

## CLI requirements

The CLI's core job is letting an LLM build emails through the designer, block by block, then test and launch them. **(stated)** Everything else in the CLI follows from making that safe and predictable for an agent.

```bash
mailgoose email create --name "November update" --json
mailgoose email blocks add em_123 --type heading --text "What shipped"
mailgoose email blocks update em_123 blk_4 --text "Revised copy"
mailgoose email import-html em_123 ./draft.html   # parsed and validated
mailgoose email check em_123                       # lint before sending
mailgoose campaign create --email em_123 --to tag:customers
mailgoose campaign test cmp_456                    # test send to the account owner
mailgoose campaign launch cmp_456 --confirm
```

1. **Parity:** every API endpoint has a CLI command, and CI fails when one is missing. **(stated goal, inferred enforcement)**
2. **Auth:** `mailgoose login` with a browser device flow for humans, and an API key in an environment variable for headless agents. Keys can be scoped, for example draft-only with no send rights. **(inferred)**
3. **Output:** readable tables in a terminal, `--json` on every command with stable, versioned schemas, and documented exit codes. **(inferred)**
4. **No hidden prompts:** every interactive prompt has a flag equivalent, so an agent never hangs. **(inferred)**
5. **Safe launch:** launch needs an explicit `--confirm` on a named campaign, after a test send. **(stated)** The agent may launch, but only after a quick confirmation from the person in the agent's chat window. **(stated)** To make that more than a convention, `campaign launch` without `--confirm` returns a summary (subject, recipient count, send time) and a one-time code; the agent shows the summary in chat and relaunches with the code once the person says yes. **(inferred design)**
6. **Idempotency:** create and launch accept an idempotency key, and launching the same campaign twice is a no-op. **(inferred)**
7. **Validation before sending:** `email check` reports broken links, missing unsubscribe, size, missing alt text and unverified sending domain. **(inferred)**
8. **Agent docs:** complete `--help`, a `mailgoose docs` command printing Markdown, and an llms.txt on the docs site. **(inferred)**

## Deliverability and compliance

These are launch blockers, not features: one bad sender on a shared IP hurts every customer.

- **Domain authentication:** SPF and DKIM required to send from a custom domain, plus a DMARC record, in line with Gmail and Yahoo bulk-sender rules. Guided DNS setup with a verify command in the UI and CLI.
- **Unsubscribe:** a footer link the customer can't remove, plus RFC 8058 one-click List-Unsubscribe headers, processed immediately.
- **CAN-SPAM:** a physical postal address in every footer, honest From and subject lines.
- **Consent (GDPR):** double opt-in by default **(stated)**, with a stored consent record per contact: time, IP, form and the wording shown. Contact export and deletion on request, and a DPA for customers.
- **Bounces and complaints:** hard bounces suppressed at once, soft bounces after repeated failures, complaints suppressed via feedback loops. Accounts held when complaints near the 0.3% Gmail threshold; target under 0.1%.
- **Abuse controls:** sending limits that grow with good history, review of large first imports, a no-purchased-lists policy, and a per-account kill switch. **(inferred)**
- **Infrastructure:** start on Amazon SES with shared IPs, and offer dedicated IPs later. **(inferred)**

## Pricing

Pricing is decided: a small free tier, then a $5 monthly account fee plus prepaid email credits. **(stated, Nov 18)**

- **Free tier:** 500 contacts and 1,000 credits a month, sending only from a verified domain.
- **Paid account fee:** $5 a month, including the first 2,500 contacts, then $5 per extra 10,000 contacts. The fee comes out of credits by default, with yearly card billing as the alternative.
- **Credits:** prepaid at $20 per 10,000 emails, valid for 24 months.

Full reasoning and margin estimates are in the [mailgoose pricing research](https://claude.ai/artifact/WJ38cukTpgWbxFaNVoT1S4). The options below were weighed before deciding; the choice is closest to option 2.

| Option | How it works | Upside | Downside |
| --- | --- | --- | --- |
| 1. Pure credits | Buy send packs; contacts stored free | Simplest, and people like credits | Unlimited free storage, and dormant accounts cost us |
| 2. Credits plus a storage fee | Credits for sends; free up to a contact cap, then a small monthly fee | Keeps credits, covers storage | Two numbers to explain |
| 3. Credits with a dormancy rule | Free storage while the account sends; dormant accounts archived after notice | No fee for active senders | Archiving needs care to stay fair |
| 4. Contact-based tiers | Monthly plans by contact count | Familiar, predictable revenue | Drops the credit idea |

Credit expiry and how the fee is paid were settled on Nov 18.

## Tech direction

Go for the backend and CLI, TypeScript and React for the web editor, running on AWS from day one. **(stated, Nov 18)** Service choices inside AWS marked *recommended* are proposals, not decisions.

- **Languages:** Go for the backend, workers and CLI **(stated)**; TypeScript and React for the web app and editor **(accepted recommendation)**. The CLI ships as a single static binary. **(inferred)**
- **Cloud:** AWS, not a platform host. **(stated)**
- **Database:** Postgres on Amazon RDS.
- **Compute:** Go API and workers on ECS Fargate rather than App Runner. **(recommended)**
- **Send queue:** SQS, or River on Postgres to start with fewer moving parts. **(recommended, open)**
- **Sending:** Amazon SES, with bounce and complaint events delivered through SNS.
- **Infrastructure as code:** Terraform.
- **API:** one REST API with an OpenAPI spec, shared by the editor, CLI and agents, with a generated TypeScript client for the editor.
- **Email model:** designs stored as JSON blocks and rendered server-side to email-safe HTML.

A full development plan and ticketing come after this PRD. **(stated)**

## Success metrics

All targets below are starting guesses to argue with. **(inferred)**

| Metric | Target |
| --- | --- |
| Signups that send a first campaign within 7 days | 40% |
| Median time from signup to first send | Under 15 min |
| Campaigns created or launched through the CLI or API | 30% |
| Spam complaint rate | Under 0.1% |
| Bounce rate | Under 2% |
| Paying accounts still sending in month 3 | 60% |

## Risks

Abuse is the risk that can sink the product; the rest are manageable. **(inferred)**

| Risk | Mitigation |
| --- | --- |
| Spammers sign up and burn shared IP reputation | Graduated send limits, import review, fast suppression, kill switch |
| An agent sends the wrong thing to a whole list | Test send first, explicit confirm, draft-only API keys, send caps |
| Validating arbitrary imported HTML turns into a rendering rabbit hole | Validate for safety and compliance only; warn on rendering, don't promise pixel parity |
| Credits with free storage cost more than they earn | Free-tier contact cap and the $5 account fee |
| Steady pressure to add features erodes the "non-platform" pitch | Non-goals list as the bar; very simple automations as the only planned addition |
| Suites ship their own agent and MCP tools | Win on simplicity and CLI quality, not on being first |
| Opens are unreliable because of Apple Mail Privacy Protection | Label opens as estimated; lead reports with clicks |

## Open questions

The first two are answered (Nov 18); pricing is decided.

- [x] Can an agent launch a campaign on its own after a test send?

  Yes, after a quick confirmation from the person in the agent's chat window. Still open: whether the one-time-code check in CLI requirements is the right way to enforce it.
- [x] Does imported HTML become editable blocks, or stay locked?

  It's edited in a code editor with a live WYSIWYG preview. Still open: can people also click into the preview to edit text, or is the code the only way in?
- [ ] What is the credit price and the free contact cap? Decided: see Pricing, including 24-month credit expiry.
- [ ] Call answers I read without hearing the question: "Both" (read as web app and CLI at launch), "Yes to both" after forms, and "That sounds about right" on pricing. Which proposals were these?
- [ ] Is the non-goals list the same as what was ruled out on the call?
- [ ] One audience with tags, or separate lists per account?
- [ ] Which sending provider to start on? Decided: Amazon SES (see Tech direction). Still open: SQS or River for the send queue.
- [ ] Ship an MCP server alongside the CLI, or CLI only?
- [ ] Is anything in the earlier mailgoose repo worth reusing?
