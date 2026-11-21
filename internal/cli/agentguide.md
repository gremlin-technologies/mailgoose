# mailgoose for AI agents

mailgoose designs emails, sends them to a list of contacts, and collects
subscribers with signup forms. Everything the web app does, the `mailgoose`
CLI does too, so you can work for the person you help from their terminal.

This guide covers one campaign from start to finish. Every command has more
in its `--help`, with examples. `mailgoose commands --json` lists them all.

## Ground rules

- **Add `--json` to every command.** Output is then one JSON value on stdout,
  and errors are `{"error": {"code", "message", "hint"}}` on stderr. Branch on
  `code`, never on the wording.
- **Read the exit code.** 0 ok, 1 error, 2 bad usage, 3 not signed in or not
  allowed, 4 not found, 5 needs an explicit confirmation. See
  `mailgoose help exit-codes`.
- **Nothing prompts.** A command that would delete or send something refuses
  until you pass `--yes` or `--confirm`. Pass them only when the person has
  agreed to that exact step.
- **Never launch a campaign on your own.** Launching sends real email to real
  people and spends credits. Show the person the launch summary and run the
  launch with `--confirm` only after they say yes in your chat.

## 1. Sign in with an API key

The person makes a key on the web app's API keys page, or with
`mailgoose keys create`, and gives it to you as an environment variable:

```sh
export MAILGOOSE_API_KEY=mg_live_...
mailgoose whoami --json
```

A key has a scope. A `draft` key can do everything except launch, cancel or
reschedule campaigns, change billing, buy credits and manage keys: it suits
an agent that drafts and tests, and leaves launching to the person in the
web app. A `full` key can do everything, launching included, always after
the person's yes. Ask for the scope the work needs, and no more.

`mailgoose status --json` shows which API and credential the CLI is using.

## 2. Check the account is ready to send

```sh
mailgoose account get --json      # missing_for_send lists what's still needed
mailgoose domain list --json      # a sending domain must be "verified"
mailgoose credits --json          # the balance; each email sent uses one credit
```

A campaign can't launch until the account has a postal address (it goes in
every email's footer, as anti-spam law requires) and a From address it may
send from. Best is a verified sending domain of its own: adding a domain
prints DNS records the person must add at their DNS host, and
`mailgoose domain verify <domain>` checks again once they have.

A domain is optional where `mailgoose account get` shows a `Shared From`.
Then a From address the person has proved they own can send without one:
their sign-in address, or one they confirm by the link emailed to it.

```sh
mailgoose sender-addresses add hello@example.com   # emails it a confirm link
mailgoose sender-addresses list --json
```

Such mail goes from the account's address on mailgoose's shared domain,
under the account's name (the email's From name is used only from a verified
domain), with replies to their own address. Tests and launch summaries say
so; tell the person, and suggest adding their own domain, which delivers
better and looks more trustworthy. Until they add one, the account stays at
the new-account daily limit (1,000): `mailgoose account get` says so, and
`send_limit.needs_domain` is true in `--json`.

Because of that, an account name with an `@`, one shaped like a domain, or
one carrying a well-known brand or a payment term is refused at sign-up and
rename (`account_name_not_allowed`, with why). Ask the person for another
name; don't try spellings that look the same.

## 3. Build an email from blocks

```sh
mailgoose email create --name "Spring menu" --json
```

Note the `id` it prints. An email is a list of blocks: `heading`, `text`,
`image`, `button`, `divider`, `spacer`, `columns` and `social`. The footer
block is added for you and can't be removed.

```sh
mailgoose email blocks add <email-id> --type heading --text "Spring is here" --json
mailgoose email blocks add <email-id> --type text --text "Our spring menu starts Monday, {{first_name|there}}." --json
mailgoose email blocks add <email-id> --type button --text "See the menu" --href https://example.com/menu --json
mailgoose email blocks list <email-id> --json
```

Each block has an id (`b_...`). Change one with `email blocks update`, move it
with `email blocks move`, remove it with `email blocks remove`. Put blocks
side by side with a `columns` block and `--in <columns-id> --column 0`.
Upload a picture first with `mailgoose image upload photo.jpg --json` and use
its `url` as an image block's `--src`.

Then set the subject and sender:

```sh
mailgoose email update <email-id> --subject "Spring is here" --preview-text "New bakes from Monday" --from-name "Pat's Bakery" --from-email news@example.com --json
```

To change many blocks at once, edit the whole document instead:
`mailgoose email get <email-id> --json > email.json`, edit the file, then
`mailgoose email replace <email-id> --file email.json --version <version>`.
The version makes the change fail, rather than overwrite, if someone edited
the email meanwhile.

Prefer your own HTML? `mailgoose email create --name "..." --kind html`, then
`mailgoose email import-html <email-id> email.html`. The markup is checked
first, and every problem is listed with its line. It must contain
`{{unsubscribe_url}}`.

## 4. Check it

```sh
mailgoose email check <email-id> --json
mailgoose email preview <email-id> --out preview.html
```

The check lists errors, which stop a send (no subject, a From that isn't on
a verified domain, no postal address, merge tags that can't be filled), and
warnings, which some readers would trip on (such as images without alt text).
Fix the errors, and tell the person about the warnings. The preview is the email as it will be sent, filled in with sample
data.

## 5. Make a campaign and send a test

```sh
mailgoose campaign create --name "Spring menu" --email <email-id> --all --json
mailgoose campaign create --name "VIP preview" --email <email-id> --tag VIP --send-at "2026-12-01 09:00" --json
mailgoose campaign count --tag VIP --json     # how many contacts it reaches now
mailgoose campaign test <campaign-id> --json
```

A campaign goes to every subscribed contact (`--all`), or to subscribed
contacts with any of the tags given. Without `--send-at` it goes out as soon
as it is launched; a send time is read in the account's time zone.

The test goes to the account owner, or with `--to` to up to five addresses
on the account. Ask the person to look at it in their inbox. A campaign can
only launch once it has been tested as it is now, so send a new test after
any change to the campaign or its email.

## 6. Launch, with the person's yes

Launching takes a `full` key, and two steps. With a `draft` key, stop
after the test and ask the person to launch it in the web app.

```sh
mailgoose campaign launch <campaign-id> --json
```

Without `--confirm` nothing is sent. It prints a summary (subject, From, how
many contacts it reaches, when it goes, the credits it uses) and a
confirmation code, and exits with code 5.

**Show the person that summary in your chat and ask them to confirm.** Only
when they say yes, run:

```sh
mailgoose campaign launch <campaign-id> --confirm <code> --json
```

The code works for 15 minutes, for the campaign exactly as it was
summarized. If anything changed, or the code ran out, ask for a new summary
and ask the person again. Never reuse a yes for a different summary.

A scheduled campaign can be moved with `campaign reschedule` or stopped with
`campaign cancel --yes` (both need a `full` key).

If `mailgoose account get` says the account is in test mode, it sends only to its
confirmed test addresses (`mailgoose test-addresses list`) and the owner's own
address, and a launch to anyone else is refused with `test_mode`. Tell the
person; only mailgoose can allow full sending.

## 7. Read the report

```sh
mailgoose campaign report <campaign-id> --json
```

Sent, delivered, bounced, complaints, unsubscribes, opens and clicks. Opens
are estimates: many mail apps load images on their own, or never.

## Contacts and signup forms

```sh
mailgoose contacts add pat@example.com --first-name Pat --json
mailgoose import contacts.csv --list-source form --wait --json
mailgoose tags add VIP pat@example.com --json
mailgoose form create --name "Footer" --json
mailgoose form embed <form-id> > signup.html
```

Import only people who agreed to hear from the person you help.
`--list-source` is required and you can't guess it: ask the person where the
list came from (`form`, `customers`, `personal`, `export`, or `other` with
`--list-source-detail`). Without `--permission-confirmed`, imported contacts
start unconfirmed and get a confirmation email first. An import with many bad,
role or throwaway addresses, or one that looks like a shared list, is held for
review; `hold_message` says why. Signup forms ask people to confirm by email unless
the person turns that off.

## When something fails

- Exit 3 with `unauthenticated`: the key is missing or wrong. Exit 3 with
  `insufficient_scope`: the key is `draft` and the step needs `full`; ask the
  person to do it or to give you a full key.
- `version_conflict`: someone changed the thing since you read it. Read it
  again and redo your change.
- `rate_limited`: wait for the seconds in the hint, then retry.
- `unverified_from`: the From is on no verified domain and, where shared
  sending is on (`shared_sending`), isn't a verified sender address. The
  hint names the fixes; there, the person's own sign-in address sends as it
  is. Confirming an address or adding DNS records is the person's to do.
- `unverified_reply_to`: on the shared domain, the email's Reply-To must be
  a verified sender address or on a verified domain too.
- `not_ready` on launch: `details` lists every reason the campaign can't go
  yet, such as no test of this version, no postal address, or too few
  credits. Fix what you can and tell the person the rest; credits are theirs
  to buy (`mailgoose credits buy`).
- `new_terms_not_accepted` among them: mailgoose's terms changed. Accepting
  them is the person's, never yours: point them to `/policies/changes` in the
  web app and let them accept there or run
  `mailgoose account accept-terms --version <v>` themselves.

The full API reference, with every field, is generated from the OpenAPI spec
in `docs/api/reference.md` in the mailgoose repository.
