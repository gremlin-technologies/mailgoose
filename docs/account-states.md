# Account states and how they interact

Several things can hold back what an account sends. Each was built in its
own ticket, and the rules for when two apply at once were spread across
`docs/decisions.md`. This page puts them together, as the code on `main`
behaves (checked Nov 21, after MG-104). Where it and the decision log
disagree, the code is what's described here.

`mailgoose admin account <account-id>` shows most of it in one place: the
Sign-up, Mode, Sending, Daily limit, Tester, Suspended and Throttled lines.
Every admin action takes `--reason`, which is logged with who did it
(`mailgoose admin log --account <account-id>`). The commands below are
`mailgoose admin ...`; `mailgoose admin <command> --help` has the details.

## The states

| State | Stored in | Set by | Cleared by | What it blocks | Admin commands |
| --- | --- | --- | --- | --- | --- |
| **Sign-up review** (MG-66) | `accounts.review = 'waiting'` (then `approved` or `rejected`; null means never reviewed) | Sign-up, while `SIGNUP_REVIEW` is `all` (the default), or `flagged` and the sign-up was flagged. Only at sign-up: changing the setting doesn't touch existing accounts. | An admin approving or rejecting it. Rejecting also suspends. A rejected review stays until it's approved, even after `unsuspend` (MG-106). | With full sending, while waiting or rejected: launches (`account_in_review`, or `signup_rejected` once rejected) and form signups (403 `form_closed`). Building, imports and test sends work. In test mode it blocks nothing more than test mode does. | `admin signups`; `admin approve-signup <id>`; `admin reject-signup <id> --reason` |
| **Test mode** (MG-80) | `accounts.sending_mode = 'test'` | Sign-up, while `NEW_ACCOUNTS_TEST_MODE` is on (the default); `admin set-test-mode`; `admin tester revoke`. | `admin allow-full-sending`; `admin tester grant`. | Sending to anyone but its confirmed test addresses (at most 5) and the owner's sign-in address: launches (`test_mode`), test sends (422 `test_mode`) and form signups (403 `test_mode`) are refused, and a queued copy for anyone else is skipped at send time with its credit back. | `admin test-mode`; `admin allow-full-sending <id>`; `admin set-test-mode <id>` |
| **Tester account** (MG-86) | `accounts.tester_started_at`, `tester_until`, `tester_credits` | `admin tester grant <id>` (30 days and 500 credits unless `--until` and `--credits` say otherwise; granting again extends it and keeps its start). Refused while the review is waiting or rejected, or the account is suspended. | `admin tester revoke`. Reaching `tester_until` doesn't clear it: it's then an ended tester. | Launches and test sends that need more tester credits than are left, or after its end (`tester_limit`). After its end, queued copies wait as for a pause. At most 100 campaign copies a day and 500 contacts. A stricter pause (below). SES mailbox simulator addresses use no tester credits. | `admin tester grant <id> [--until] [--credits]`; `admin tester revoke <id>` |
| **Sliced first send** (MG-62), per campaign | `campaigns.slice_state`, `campaign_recipients.in_slice` | The launch, when the account is in its first 30 days from activation or has launched fewer than 3 other campaigns (whichever ends later), and the campaign has more than `SLICE_MIN` (200) recipients. The slice is `SLICE_PERCENT` (10%) rounded up, at least 200. | The send pipeline, `SLICE_WAIT` (an hour) after the slice is out: `released`, `held` (until the account's throttle lifts), or `stopped` (over the pause limits: the account is paused, the rest skipped and the campaign cancelled). A cancel, stop or suspension marks it `cancelled`. | The rest of that campaign, until its slice has been checked. | None to see or release it. The owner sees it in `mailgoose campaign get`; `admin stop-campaign <id> --reason --yes` ends the campaign. |
| **Throttle** (MG-61) | A row in `account_throttles` whose `until` is still ahead; `campaigns.throttled_at` | SES bounce and complaint events, when a campaign of 500 or more copies goes over the early warning (`EARLY_WARNING_COMPLAINT_PERCENT` 0.05%, from 2 complaints, or `EARLY_WARNING_HARD_BOUNCE_PERCENT` 2%), or a slice does at any size when its wait is over. Each campaign throttles once. | Time only: 7 days after the latest throttle. No admin command lifts one. | The daily limit drops to the new-account limit (1,000, or the account's own limit if lower) until it lifts; the ramp then starts again from there. A held slice waits for it. | `admin account` ("THROTTLED until", Throttled lines for 30 days); `admin deliverability` |
| **Pause** (CMP-8, MG-34, MG-79, MG-88, MG-64) | `accounts.sending_paused_at`, `sending_paused_reason`, `sending_paused_code` (`subscription_past_due` for the billing pause, otherwise null) | `admin pause`, or automatically: a campaign over the pause limits (500 or more copies and over 0.1% complaints from 2, or over 3% hard bounces; a slice at any size; a tester's stricter limits), the account's own last 30 days over the same limits (MG-88), a third throttle in 30 days, an unsafe link found at launch (MG-64), or the paid plan's payment going past due. | `admin unpause` (any pause). The payment going through lifts the past-due pause only. | Launches (`account_paused`, or `subscription_past_due`). Nothing more is batched, and a batch on its way stops before its next copy; campaigns stay `sending` and carry on where they stopped once it's lifted. Test sends and form signups still work. | `admin pause <id> --reason`; `admin unpause <id> --reason`; `admin find` (SENDING column) |
| **Suspension** (MG-65) | `accounts.suspended_at`, `suspended_reason`, `suspended_by` | `admin suspend`; `admin reject-signup`; automatically, with the second automatic sending pause in 30 days (logged `auto_suspend`). | `admin unsuspend`. | Nearly everything: API keys and CLI logins are revoked, web sessions end, scheduled and sending campaigns are cancelled with their credits back, forms close (403 `form_closed`), and the API answers 403 `account_suspended`, except signing out, the account and, for 30 days, its session's reads (the suspension page's export). Credits stay. | `admin suspend <id> --reason`; `admin unsuspend <id> --reason` |

Beside these, the account's daily limit decides how much goes out, but
never refuses a launch: copies over it wait for the next day's room.

- **Daily limit and ramp** (CMP-7): 1,000 a day (`NEW_ACCOUNT_DAILY_SEND_LIMIT`)
  for the first week from activation, then doubling each clean week up to
  10,000 (`ACCOUNT_DAILY_SEND_LIMIT`), held at 1,000 while the last 30 days
  aren't clean. A limit staff set by hand (`accounts.daily_send_limit`) stands
  in for the ramp; no command sets it.
- **Shared-domain hold** (MG-98): with `SHARED_SENDING_DOMAIN` set, an account
  with no verified domain of its own stays at 1,000 a day (`needs_domain` in
  the account's send limit; in `admin account --json`, not the plain view).
  Once it verifies one, the ramp counts from then.
- **Platform daily cap** (MG-87): `PLATFORM_DAILY_SEND_LIMIT` (45,000) caps
  campaign copies across all accounts. With it, the account's limit and the
  per-campaign queue cap (MG-89), the smallest room wins.
- **Test sends** have their own cap of 50 a day per account.

## When two apply at once

**Sign-up review and test mode**
- A waiting account in test mode may launch to its test addresses and take
  their form signups; with full sending it can do neither until approved.
- `allow-full-sending` doesn't approve the review, and `approve-signup`
  doesn't leave test mode. Sending to the whole audience needs both.

**Sign-up review and tester accounts, suspensions**
- `tester grant` is refused (`signup_not_approved`) while the review is
  waiting or was rejected, and (`account_suspended`) while suspended.
- `reject-signup` suspends the account with the rejection as the reason. A
  suspended account rejected keeps its suspension and reason as they were.
- `approve-signup` works on a suspended account; it stays suspended. It
  also takes a rejected sign-up (logged with `review_was`), while
  `reject-signup` takes only a waiting one.
- `unsuspend` leaves a rejected review rejected, so the account still can't
  launch or take form signups with full sending until `approve-signup`.
  While it's suspended, only `account_suspended` is reported.

**Test mode and tester accounts**
- `tester grant` gives full sending (the log keeps the mode it had);
  `tester revoke` puts the account in test mode. To make an ex-tester a
  customer, use `allow-full-sending` after the revoke.
- `set-test-mode` on a tester works, and both apply: only test addresses,
  within the tester's credits and limits.
- Either way, copies of a campaign already sending to anyone but the test
  addresses are skipped at send time, giving back the account's credit and
  the tester credit.
- A tester's end date doesn't change its mode: an ended tester is still in
  full sending, but sends nothing (below).

**Tester accounts and pauses, suspensions, limits**
- A tester's campaign also pauses the account at 2 complaints at any size,
  or at 20 or more copies with over 2% hard bounces and at least 3. The usual
  campaign and 30-day checks apply too. All count toward suspension.
- An ended tester's queued copies wait in the dispatcher like a paused
  account's, but `sending_paused_at` isn't set: `admin account` says
  "Sending: on" and the Tester line says ENDED. Granting again releases them.
- A suspension leaves the tester state alone; `unsuspend` doesn't end it.
- Its daily limit is the lower of 100 and any limit staff set. That stands
  over the shared-domain hold, and a throttle leaves it where it is.

**Slices, throttles and pauses**
- A slice over the pause limits pauses the account (an automatic pause that
  counts toward suspension), skips the rest and cancels the campaign.
- A slice over only the early warning throttles the account and holds the
  rest until the account's latest throttle lifts. It's then checked against
  the pause limits only, and released or stopped.
- A slice is checked when its wait is over even while the account is paused;
  if released, the rest then waits for the unpause.
- Test-mode accounts are in practice never sliced: lists of 200 or fewer go
  out whole. Tester accounts are sliced like any new account.

**Throttles and pauses**
- The third throttle in 30 days pauses the account instead of throttling it
  (logged `auto_pause`, so it counts toward suspension).
- A campaign that has paused its account doesn't throttle it afterwards.
- `unpause` doesn't lift a throttle, and a throttle doesn't pause.
- A throttle lowers a staff-set limit too, unless that's lower already.

**Pauses with pauses**
- An account has one pause and one reason. An automatic pause on an account
  that's paused already keeps the reason it has. A campaign's pause (and a
  third throttle) is still logged as `auto_pause` and still counts toward
  suspension; the 30-day check (MG-88) and the unsafe-link pause add nothing
  to the admin log.
- The exception is the past-due pause (MG-105): any sending pause landing on
  it takes it over, with its own reason and no past-due code, and is logged
  as `auto_pause`. The payment going through then leaves it in place. While
  the payment is still owed, a refused launch lists `subscription_past_due`
  after `account_paused`, and the web banner and `plan get` still show it.
- `admin pause` on a paused account replaces the reason and drops the
  past-due code.
- The past-due pause is only put on an account that isn't paused. If an
  admin then lifts another pause while the payment is still past due, the
  past-due pause takes its place at once. An admin lifting the past-due pause
  itself stands.
- After `admin unpause`, or after any automatic pause logged with a campaign
  (every kind but the past-due one), the 30-day check counts only copies sent
  since. The payment's automatic unpause doesn't restart it.

**Pauses and suspension**
- The second automatic pause in 30 days suspends the account in the same
  transaction. The past-due pause doesn't count (MG-104), though it's still
  logged as `auto_pause`. An unsafe-link pause counts toward a later one, but
  doesn't suspend on its own.
- A suspension cancels live campaigns rather than holding them, and keeps
  any pause. `unsuspend` leaves the pause, keeps old API keys revoked, and
  doesn't restart cancelled campaigns.
- Late bounces and complaints can still pause a suspended account; it isn't
  suspended twice.

## Known gaps

Found while checking this page against the code, and not fixed yet.

- An ended tester's batches already on the send queue when it ends still go
  out; only new batches are held.
