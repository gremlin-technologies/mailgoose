# mailgoose pricing research

> Snapshot of the [mailgoose pricing research Claude Doc](https://claude.ai/artifact/WJ38cukTpgWbxFaNVoT1S4) as of Nov 17, 2026. The Claude Doc is the live version; if they differ, trust the doc and refresh this copy.

Nov 18, 2026 · Gremlin Technologies

## Recommendation

**Chosen on Nov 18, 2026: the revised plan below.**

$15 per 10,000 emails works as a price per email; the plan around it didn't. Delivering an email costs about $0.27 per 1,000, and about $0.70 per 1,000 once support, abuse review, the free tier and fixed infrastructure are spread over it. The real problem is per account: each paying account carries about $4.50 a month of support and abuse review plus $4 to $7 of free-tier subsidy, so a small list paying $4.50 a month loses money.

As first proposed, fully loaded margin is about 37% at 1,000 paying customers. With three changes it reaches about 56%, or 62% without Amazon's paid deliverability dashboard:

- **Free tier:** 1,000 contacts and 2,500 credits a month, at most 1,000 sends a day, with the required "Sent by mailgoose" footer (changed Nov 22, MG-120; it was 500 and 1,000). No domain needed (changed Nov 22; it first said "sending only from a verified domain"). Paid accounts keep the same monthly free credits too (decided Nov 22).
- **Paid account fee:** $5 a month, including the first 2,500 contacts; then $5 per extra 10,000. Billed by card as a subscription, monthly or yearly at $50 (changed Nov 18; it first came out of credits).
- **Credits:** $20 for 10,000, $75 for 50,000, $240 for 200,000, $900 for 1,000,000 ($2.00 down to $0.90 per 1,000), valid 24 months.

That's still under half of Brevo's credit prices. For comparison, Klaviyo's 2025 gross margin was 74.7%; mailgoose gets closer with scale and fewer tickets per account. Credits still suit the CLI: an agent can show the credit cost of a launch at the confirm step.

## What comparable tools charge

Most tools charge a monthly fee by list size; only Mailchimp and Brevo sell email credits. Brevo's credits cost $3.30 to $6.40 per 1,000 and never expire; Mailchimp's expire after 12 months. Subscriptions used to their full send cap work out to roughly $0.45 to $0.90 per 1,000 emails.

| Tool | Priced by | Free tier | Example paid prices | Credits or pay as you go |
| --- | --- | --- | --- | --- |
| [Mailchimp](https://mailchimp.com/pricing/) | Contacts, send cap 10 to 15x contacts | 250 contacts, 500 emails/mo | Essentials from $13/mo | Credits, 1 per email, expire after 12 months; block prices shown only inside the account ([help](https://mailchimp.com/help/about-the-pay-as-you-go-plan/)) |
| [Brevo](https://www.brevo.com/pricing/?tab=suite) | Emails sent; contacts mostly free | 300 emails/day, up to 100,000 contacts | Starter $9/mo from 5,000 emails | 5,000 for $32, 10,000 for $59, 50,000 for $165; never expire ([EmailToolTester](https://www.emailtooltester.com/en/reviews/brevo/pricing/), Aug 2026) |
| [Kit](https://kit.com/pricing) | Subscribers, no send limits | Up to 10,000 subscribers | Creator $33/mo at 1,000 | None |
| [MailerLite](https://www.mailerlite.com/pricing.md) | Subscribers, Comfort capped at 10x sends | 250 subscribers, 2,500 emails/mo | Comfort $19 at 1,000, $89 at 10,000, $319 at 50,000 (monthly) | None |
| [EmailOctopus](https://emailoctopus.com/pricing) | Subscribers, 10x sends | 2,500 subscribers, 10,000 emails/mo | Pro $20 at 2,500, $44.50 at 10,000, $178 at 50,000 ([EmailToolTester](https://www.emailtooltester.com/en/reviews/emailoctopus/pricing/), Dec 2025) | None |
| [Buttondown](https://buttondown.com/pricing?count=7500) | Active subscribers, about 1 send a day | First 100 subscribers | $9 to 1,000, $29 to 5,000, $79 to 10,000 ([Toolradar](https://toolradar.com/tools/buttondown/pricing), Oct 2026) | None |
| [Loops](https://loops.so/pricing) | Contacts, unlimited sends | 1,000 contacts, 4,000 sends per 30 days | Growth $49/mo at 5,000 | None |
| [Resend](https://resend.com/pricing.md) marketing | Contacts | 1,000 contacts | $40 at 5,000, $80 at 10,000, $250 at 50,000 | None for marketing |
| [Sendy](https://sendy.co) | One-time license | None | $69 once, self-hosted | You pay SES directly |

Prices are from each tool's own page, read on Nov 18, 2026, unless a dated review is linked. Mailchimp and Kit prices at larger list sizes weren't shown on their pages.

## Unit costs

Plan on about $0.15 per 1,000 emails sent: Amazon SES at $0.10, plus half again for event processing, open and click tracking, test sends and bounces. Storing contacts is close to free; payment fees are the cost to watch on small purchases.

| Cost | Rate | Source |
| --- | --- | --- |
| Amazon SES sending | $0.10 per 1,000 emails; $0.12 per GB of attachments | [AWS](https://aws.amazon.com/ses/pricing/) |
| SES dedicated IP, standard | $24.95/mo per IP | [AWS](https://aws.amazon.com/ses/pricing/) |
| SES dedicated IP, managed | $15/mo plus $0.08 per 1,000 emails | [AWS](https://aws.amazon.com/ses/pricing/) |
| Postmark (alternative) | $15/mo for 10,000; $1.15 to $1.80 per 1,000 over | [Postmark](https://postmarkapp.com/pricing.txt) |
| Resend (alternative) | $20/mo for 50,000; $0.46 to $0.90 per 1,000 over | [Resend](https://resend.com/pricing.md) |
| SendGrid (alternative) | From $19.95/mo for 50,000 | [Twilio](https://www.twilio.com/en-us/products/email-api/pricing.md) |
| Stripe card fee | 2.9% + $0.30 (US); +1.5% international; +1% currency conversion | [Stripe](https://stripe.com/pricing) |
| Stripe Billing (subscriptions) | 0.7% of subscription volume | [Stripe](https://stripe.com/pricing) |
| Contact storage (estimate) | About $0.01 per 1,000 contacts a month | Estimate below |
| Fixed infrastructure (estimate) | About $300 to $500 a month at launch | Estimate |

- **Storage estimate:** about 1 KB per contact plus about 150 bytes per tracked event, 3 events per email, 4 emails a month, kept 12 months. That's roughly 25 KB per contact, or 25 MB per 1,000 contacts, at approximate managed Postgres rates with backups.
- **What big free lists really cost:** not disk, but deliverability. Dumped or bought lists drive bounces and complaints that hurt the shared sending reputation for everyone, so cap free contacts for that reason.
- **Payment fees by pack size:** Stripe takes 5.9% of a $10 sale, 4.9% of $15, 3.4% of $60 and 3.1% of $200. Make the smallest pack $15.
- **Dedicated IPs:** managed IPs nearly double the per-email cost at small volume, so start on SES shared IPs and add them once a customer's volume justifies it.

## Fully loaded cost per send

Everything that grows with volume comes to about $0.27 per 1,000 emails, and SES is only $0.10 of it. The bigger costs come per account: support, abuse review and the free tier.

| Cost that grows with emails | Per 1,000 emails | Basis |
| --- | --- | --- |
| SES sending | $0.10 | [AWS](https://aws.amazon.com/ses/pricing/) |
| Deliverability dashboard (SES Virtual Deliverability Manager) | $0.07 | [AWS](https://aws.amazon.com/ses/pricing/); optional |
| Address checks on send (SES Auto Validation) | $0.01 | [AWS](https://aws.amazon.com/ses/pricing/) |
| Dedicated IP pool, plus 20% extra IPs for warmup | $0.02 | $24.95 per IP, about 1.5M emails per IP a month (estimate) |
| Bounce, complaint, open and click event processing | $0.02 | Estimate: queues, workers, database writes |
| Image hosting and tracking links on a CDN | $0.01 | About 100 MB per 1,000 emails at $0.085 per GB ([CloudFront rates](https://egresscost.com/aws/cloudfront-pricing), Sep 2026) |
| Waste: test sends, retries, bounced sends still charged | +15% | Estimate |
| **Total** | **about $0.27** |  |

| Cost per account, per month | Amount | Basis |
| --- | --- | --- |
| Support, paying account | $3.00 | 0.25 tickets a month at about $12 each (estimate) |
| Abuse and compliance, paying account | $1.50 | Signup screening, list review, help setting up DKIM and domains (estimate) |
| Free-tier subsidy, per paying account | $4.21 to $6.87 | 25 free accounts per paying one (4% conversion), each sending 300 to 600 emails a month |
| Contacts, events, reports and backups | $0.02 per 1,000 contacts | Estimate |
| Stripe | 3 to 5% of each purchase | 2.9% + $0.30; 0.7% Billing on monthly fees |
| Chargebacks, card fraud and sales tax tools | 1% of revenue | Estimate |

| Fixed costs per month | 100 paying | 1,000 paying | 10,000 paying |
| --- | --- | --- | --- |
| Compute, database, queues, backups | $600 | $2,500 | $25,000 |
| Logs and monitoring | $150 | $500 | $3,000 |
| Helpdesk, status page, other tools | $150 | $400 | $2,000 |
| **Total** | **$900** | **$3,400** | **$30,000** |

- **Bulk list cleaning** costs far more than sending: [ZeroBounce](https://www.zerobounce.net/email-validation-pricing) starts at $39 for 2,000 checks, about $20 per 1,000 contacts. Offer it as a paid add-on, not on every import.
- **Free-to-paid conversion** is the biggest unknown. 4% is a midpoint of [freemium benchmarks](https://medium.com/ada-chen-rekhi/freemium-and-free-trial-conversion-benchmarks-9fab38d096da) that run from 1 to 10%.
- **Labor** is priced as if you paid for it, at about $40 an hour loaded. Early on it's your own time, which still isn't free.

## Viability at scale

At 1,000 paying customers the first plan makes about 37% fully loaded margin and the revised plan about 56%. Covering launch infrastructure takes a few dozen customers either way; paying salaries is what takes volume.

Assumed mix: 60% small lists (1,500 contacts, 2 emails a month), 30% mid (10,000 contacts, 4 a month), 10% large (50,000 contacts, 4 a month).

|  | First plan ($15 per 10k, no account fee) | Revised plan ($20 per 10k, $5/mo account fee) |
| --- | --- | --- |
| Average revenue per paying customer | $41.10/mo | $54.60/mo |
| Small list pays / costs | $4.50 / $12.46 | $11.00 / $10.39 |
| Mid list pays / costs | $53 / $24.79 | $70 / $22.83 |
| Large list pays / costs | $225 / $74.82 | $270 / $73.95 |
| Margin at 100 paying | 24% | 46% |
| Margin at 1,000 paying | 37% | 56% |
| Margin at 10,000 paying | 38% | 57% |
| Profit at 1,000 paying | $15,300/mo | $30,700/mo |
| Break-even on $900/mo launch costs | 49 paying | 27 paying |
| Extra paying customers per $10k/mo of salary | 535 | 294 |

How the margin at 1,000 paying customers moves:

- **Free-to-paid conversion of 2% instead of 4%:** revised 49%, first plan 21%.
- **Conversion of 8%:** revised 60%, first plan 46%.
- **Twice the support tickets:** revised 51%, first plan 30%.
- **No paid deliverability dashboard:** revised 62%.
- **Keep $15 per 10k but add the $5 fee and smaller free tier:** 49%.
- **Only shrink the free tier:** 44%.

## Three pricing models

All three reach about 80% gross margin on a 10,000-contact customer; they differ in who pays for big lists and whether revenue recurs. Margins here count only sending, storage and Stripe fees at the first proposed prices; the fully loaded view above cuts them to 37 to 56%.

| Model | Free tier | Sending price | Contacts | Credit expiry | 10,000 contacts, 4 emails/mo pays | Margin there | 50,000 contacts, 4 emails/mo pays | Margin there |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A. Pure credits | 1,000 contacts, 2,000 credits/mo | $15 per 10k down to $750 per 1M | Free while you've bought credits in the last 12 months | 24 months | $48/mo | 84% | $200/mo | 82% |
| B. Subscription with included sends | 1,000 contacts, 5,000 emails/mo | $12 (2,500 contacts, 25k emails), $39 (10k, 100k), $129 (50k, 500k); extra emails as credits | Set by plan | No credits to expire | $39/mo | 80% (57% if they use the full cap) | $129/mo | 73% (38% at full cap) |
| C. Hybrid (recommended) | 1,000 contacts, 2,000 credits/mo | Same packs as A | First 2,500 included, then $5/mo per extra 10,000 | 24 months | $53/mo | 85% | $225/mo | 83% |

- **A, pure credits:** simplest and closest to what people like about credits, but large dormant lists sit for free and revenue arrives in lumps.
- **B, subscription:** steady revenue and familiar, undercutting EmailOctopus ($44.50) and MailerLite ($89) at 10,000. Margin falls fast for customers who use their whole cap, and it drops the credits idea.
- **C, hybrid:** credits for sending, a small fee only for big lists. It answers the storage concern directly and adds a recurring base that covers fixed costs.

Other numbers for model C:

- **A small list** (2,000 contacts, 2 emails/mo) buys a $15 pack every 2.5 months, about $6/mo, at 85% margin.
- **Each free account** costs about $0.31/mo, so 1,000 free accounts run about $310/mo.
- **Break-even** on $400/mo of fixed costs is about 9 customers the size of the 10,000-contact example.
- **Trade-off:** credits are best for people who send a few times a month. Someone mailing 10,000 contacts weekly pays a bit more than on EmailOctopus, though still well under MailerLite or Resend ($80).

## Decisions for the owner

Decided on Nov 18, 2026: the revised plan. That's a $5/mo fee on paid accounts including the first 2,500 contacts, credits at $20 per 10,000, and a free tier of 500 contacts and 1,000 credits a month.

Also decided on Nov 18, 2026:

- **Credit expiry:** credits last 24 months from purchase.
- **How the account fee is paid:** a card subscription, $5 a month or $50 a year, chosen at sign-up to the paid plan. This replaced taking the fee from the credit balance (with yearly card billing at $60 for accounts without credits), which the owner found confusing. Credit packs are unchanged.
- **What the change costs:** Stripe now takes its card fee on every renewal, about $0.48 of each $5 monthly payment (2.9% + $0.30, plus 0.7% for Stripe Billing) and about $2.10 of each $50 yearly one. The margins above weren't re-modelled for this; they come down slightly for accounts that pay monthly.

Decided on Nov 22, 2026 (see `docs/decisions.md`):

- **No domain requirement on the free tier.**
- **Paid accounts keep the free 1,000 credits a month**, so starting the plan never leaves an account with nothing to send. Each paying account now carries up to 1,000 free sends a month, about $0.27 to $0.70 at the costs above; the margins weren't re-modelled for this.

The cost assumptions marked as estimates are worth checking against real numbers once there's a beta: tickets per account, emails per free account and conversion move the margin most.

## Sources

All pages read on Nov 18, 2026. Review sites are used only where the official page didn't show the figure, with their own update dates noted in the tables.

- [Mailchimp pricing](https://mailchimp.com/pricing/) and [Mailchimp pay-as-you-go help](https://mailchimp.com/help/about-the-pay-as-you-go-plan/)
- [Brevo pricing](https://www.brevo.com/pricing/?tab=suite) and [EmailToolTester on Brevo](https://www.emailtooltester.com/en/reviews/brevo/pricing/)
- [Kit pricing](https://kit.com/pricing)
- [MailerLite pricing](https://www.mailerlite.com/pricing.md)
- [EmailOctopus pricing](https://emailoctopus.com/pricing) and [EmailToolTester on EmailOctopus](https://www.emailtooltester.com/en/reviews/emailoctopus/pricing/)
- [Buttondown pricing](https://buttondown.com/pricing?count=7500) and [Toolradar on Buttondown](https://toolradar.com/tools/buttondown/pricing)
- [Loops pricing](https://loops.so/pricing)
- [Resend pricing](https://resend.com/pricing.md)
- [Sendy](https://sendy.co)
- [Amazon SES pricing](https://aws.amazon.com/ses/pricing/)
- [Postmark pricing](https://postmarkapp.com/pricing.txt)
- [Twilio SendGrid pricing](https://www.twilio.com/en-us/products/email-api/pricing.md)
- [Stripe pricing](https://stripe.com/pricing)

Added for the fully loaded model:

- [Amazon SES pricing](https://aws.amazon.com/ses/pricing/) for Virtual Deliverability Manager and Auto Validation
- [CloudFront rates on EgressCost](https://egresscost.com/aws/cloudfront-pricing), verified Sep 2026
- [ZeroBounce pricing](https://www.zerobounce.net/email-validation-pricing)
- [Freemium and free trial conversion benchmarks](https://medium.com/ada-chen-rekhi/freemium-and-free-trial-conversion-benchmarks-9fab38d096da)
- [Klaviyo 10-K for 2025](https://www.sec.gov/Archives/edgar/data/1835830/000183583026000007/kvyo-20251231.htm)

Storage and fixed infrastructure costs are estimates, not taken from a price page.
