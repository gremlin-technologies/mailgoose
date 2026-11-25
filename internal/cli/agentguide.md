# mailgoose for AI agents

mailgoose designs emails, sends them to a list of contacts, and collects
subscribers with signup forms. Everything the web app does, the `mailgoose`
CLI does too, so you can work for the person you help from their terminal.

This guide covers one campaign from start to finish, and moving a
person's lists and emails over from Mailchimp. Every command has more
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
their sign-in address, or one they confirm by the link emailed to it. With
no From set on the email or the account, it goes as their sign-in address
(`default_from_email` in `mailgoose account get --json`), and tests and
launch summaries say so (`default_from`).

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

Merge tags fill in each contact's details: `{{first_name}}`, `{{last_name}}`,
`{{email}}`, `{{unsubscribe_url}}`, and each of the account's contact fields
by its key (`mailgoose fields list --json`), such as `{{company}}`. Every tag
but `{{unsubscribe_url}}` takes a fallback for contacts with no value:
`{{company|your team}}`. Give a contact field's tag one: the check warns
(`merge_tag_no_fallback`) when it has none, since that contact sees nothing
there. A tag that is neither built in nor a field's key is an error
(`merge_tag`), and its message lists the keys there are.

Each block has an id (`b_...`). Change one with `email blocks update`, move it
with `email blocks move`, remove it with `email blocks remove`. Put blocks
side by side with a `columns` block and `--in <columns-id> --column 0`.
Upload a picture first with `mailgoose image upload photo.jpg --json` and use
its `url` as an image block's `--src`.
Link to social profiles with a `social` block, which shows each network's
icon: `--type social --link instagram=https://instagram.com/... --link
website=https://...`, styled with `--display`, `--color`, `--size`,
`--spacing` and `--layout`.

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
data: the sample person has no contact field values, so field tags show their
fallbacks. `--contact <id-or-email>` fills it in from one contact instead,
their field values included.

## 5. Make a campaign and send a test

```sh
mailgoose campaign create --name "Spring menu" --email <email-id> --all --json
mailgoose campaign create --name "VIP preview" --email <email-id> --tag VIP --send-at "2026-12-01 09:00" --json
mailgoose campaign count --tag VIP --json     # how many contacts it reaches now
mailgoose campaign create --name "Win back" --email <email-id> --segment engaged --json
mailgoose campaign test <campaign-id> --json
```

A campaign goes to every subscribed contact (`--all`), to subscribed
contacts with any of the tags given, or to a saved segment's subscribed
contacts (`--segment`, by name or id; see section 7). A segment is worked out
when the campaign launches, and its contacts are fixed then. A segment with a
condition it can't use stops the launch with `segment_invalid`. Without `--send-at` it goes out as soon
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
can run high, since some mail apps load the tracking image without anyone
reading; clicks are exact. `mailgoose campaign list --json` gives each sent
campaign's rates side by side.

To see who is behind a number:

```sh
mailgoose campaign report <campaign-id> --who clicked --json
mailgoose campaign report <campaign-id> --who not_opened --csv --output not-opened.csv
```

`--who` takes `opened`, `not_opened`, `clicked` (with `--link N` for one link),
`not_clicked`, `bounced`, `complained` or `unsubscribed`. These lists are kept
12 months after launch; after that a campaign has only its numbers
(`report_archived`).

To find contacts by what they did across campaigns, filter the contact list:

```sh
mailgoose contacts count --never-opened --status subscribed --json
mailgoose contacts list --opened <campaign-id> --not-clicked <campaign-id> --json
mailgoose export --clicked-within-days 90 --output engaged.csv
```

`--opened`, `--not-opened`, `--clicked` and `--not-clicked` take a campaign id
and can repeat; `--never-opened` and `--never-clicked` are contacts delivered at
least one tracked campaign who opened or clicked none; `--opened-within-days`
and `--clicked-within-days` reach back up to 365. Every filter must match. A
campaign sent with tracking off answers `tracking_off`.

To keep a set of conditions, save it as a segment:

```sh
mailgoose segment create engaged --status subscribed --opened-within-days 90 --json
mailgoose segment contacts engaged --json
```

A segment takes `--tag` (repeat for more; every tag must match), `--status`
(repeat for any of several) and the engagement flags above. It's worked out
each time it's used. `"valid": false` means a condition names a tag or
campaign that's gone or can't be used now; `problems` says which, and
`mailgoose segment update <name>` with new condition flags fixes it. A segment
that a draft or scheduled campaign goes to can't be deleted (`segment_in_use`
names the campaign); give the campaign other recipients first.

## Contacts and signup forms

```sh
mailgoose contacts add pat@example.com --first-name Pat --json
mailgoose import preview contacts.csv --json
mailgoose import contacts.csv --list-source form --map status= --wait --json
mailgoose tags add VIP pat@example.com --json
mailgoose fields create "Company size" --json        # key: company_size
mailgoose contacts update pat@example.com --field company_size=50 --json
mailgoose form create --name "Footer" --json
mailgoose form embed <form-id> > signup.html
```

Contact fields are the account's own text fields beside name and tags, up to
`contact_field_limit` from `mailgoose plan --json` (20 today; more is
`field_limit`). Contacts carry them as `fields`, by key; the key comes from
the name and stays when it's renamed, so emails using `{{key}}` keep
working. `--field key=` clears a value, and deleting a field deletes its
values, so ask the person first. A field whose tag is in the email of a draft
or scheduled campaign can't be deleted (`field_in_use` names the emails);
take the tag out first.

Import only people who agreed to hear from the person you help.
`--list-source` is required and you can't guess it: ask the person where the
list came from (`form`, `customers`, `personal`, `export`, or `other` with
`--list-source-detail`). Without `--permission-confirmed`, imported contacts
start unconfirmed and get a confirmation email first. An import with many bad,
role or throwaway addresses, or one that looks like a shared list, is held for
review; `hold_message` says why. Signup forms ask people to confirm by email unless
the person turns that off.

Before importing a file, run `mailgoose import preview` and show the person
which column becomes which field, with the first values, and let them confirm
it or change it. Columns that become no field aren't imported. Name a column
with `--map field=header`, or `--map field=#3` by position for a blank or
repeated header; `--map status=` leaves a field out even when a column looks
like it. Pass the same `--map` flags to `mailgoose import`. Pasted text works
too: comma- or tab-separated lines piped in with `-`. Rows copied from a
spreadsheet often have no header row; the preview says when the first row
looks like a contact, and then `--no-header` on both commands imports it as
one, with columns named by position (`#1`, `#2` ...).

Other columns can fill the account's contact fields. The preview shows a
column whose header matches a field's name or key as `field <key>`; name one
with `--field key=header`, or leave it out with `--field key=`. For a column
with no field yet, offer to make one: `--new-field header`, or
`--new-field '#3=Name'` to name it. New fields count against the plan's
field limit, and an import over it is refused (`field_limit`) with nothing
made, so ask the person before adding fields. An existing contact's field
values are overwritten by the file's non-blank cells; say so before
importing a file of people who are already contacts.

## Moving from Mailchimp

mailgoose reads Mailchimp's own export, or a connected Mailchimp account, and
brings over each audience's contacts with their status, tags and groups, its
templates and newest campaigns as draft HTML emails, and a draft signup form
for each audience. mailgoose isn't part of Mailchimp; it only reads what
Mailchimp gives out.

**Get the export.** The person does this in Mailchimp: account settings,
Manage my data, Export data. Mailchimp emails them when the .zip is ready.
An export over 25 MB can be made one audience at a time instead, as a .zip or
CSV. Ask them for the file's path; don't sign in to Mailchimp for them.

```sh
mailgoose import mailchimp upload mailchimp-export.zip --json
```

**Or connect the account**, where it's set up (`can_connect` in
`mailgoose import mailchimp connection --json`). `connect` prints a link that
the person opens in their own browser and allows on Mailchimp's page; you
can't do that step. Then list the audiences and fetch the ones they want:

```sh
mailgoose import mailchimp connect --json
mailgoose import mailchimp connection --json
mailgoose import mailchimp audiences --json
mailgoose import mailchimp fetch --audience <audience-id> --json
```

A fetch reads every member, not only the subscribed ones, and turns each
`--segment` into a tag on the members it has now. It doesn't bring templates;
upload the export for those. mailgoose forgets the connection once the import
is confirmed, after 24 hours, or on `mailgoose import mailchimp disconnect`.

**Show the person the preview.** Both give an upload's `id` and a `preview`:
each audience's members by Mailchimp status, the new contacts by the status
they'll get here, existing contacts that change, tags to create, the merge
fields (`merge_fields`) and the contact field each would fill, Mailchimp's
own details mailgoose doesn't keep, the emails it would make, any `holds`,
and whether the contacts need the paid plan (`plan.upgrade`, with its price). Nothing is
imported yet. Read it again any time:

```sh
mailgoose import mailchimp preview <id> --json
```

Tell the person, in plain words, how many contacts would come in subscribed,
unconfirmed, unsubscribed and bounced, what it would cost them, and anything
held for review.

**Merge fields become contact fields.** Each merge field beyond first and
last name (phone, address, birthday, the person's own) fills one of the
account's contact fields (`custom_field`) when the names match, or gets a new
field (`new_field`) in Mailchimp's order while the plan's field limit
(`fields.limit`, 20) has room. The rest are left out, with `skip_reason`
saying why (`field_limit`, or a name that can't be a field's), and
`fields.left_out` counts those the limit left out. Audiences with the same
merge field share one field. When some are left out, ask the person which
matter: put one into an existing field with `--field key=MERGE` (this works
past the limit; `key=` leaves that field out), choose the new fields with
`--new-field MERGE` or `--new-field 'MERGE=Field name'` (with any, only
those are made), or make none with `--no-new-fields`. Contacts already here
take the merge fields' values too.

**Merge tags in the emails become field tags.** A Mailchimp merge tag in an
imported email (`*|COMPANY|*`, `*|MMERGE3|*`) becomes the tag of the contact
field it stands for, `{{company}}`, in the HTML, subject and preview text:
the field this import puts that merge field into (matched by its Mailchimp
tag on a connected account, by name on an upload), a field an earlier
import put it into, or a field whose key or name matches it. Each email's
`merge_tags` lists them, with the `field` each became, or `null` when it was
taken out with a `mailchimp_tag_removed` warning saying which field to add.
To keep one, make the field before confirming (`mailgoose fields create
Company`; the preview follows) or bring its merge field in with `--field` or
`--new-field`. A converted tag has no fallback, so `mailgoose email check`
warns `merge_tag_no_fallback`; offer to add one (`{{company|your team}}`).

**Confirm only after the person says yes.** `--permission-confirmed` states
that the person has permission to email these contacts: Mailchimp's
subscribed contacts come in subscribed, with no confirmation email. Run it
only once they have seen the preview and agreed in your chat, never on your
own. Ask them which groups (as the preview lists them) the new signup forms
should tag signups with; none is fine.

```sh
mailgoose import mailchimp confirm <id> --permission-confirmed --wait --json
mailgoose import mailchimp confirm <id> --permission-confirmed --form-tag "Interests: Baking" --json
mailgoose import mailchimp confirm <id> --permission-confirmed --field company=Company --new-field "Phone Number=Phone" --json
mailgoose import status <import-id> --json
```

Pending contacts come in unconfirmed, unsubscribed ones unsubscribed and
cleaned ones bounced. `--campaigns` brings the newest 10 campaigns unless you
say otherwise (up to 100). Confirming again shows the same import, and
importing again makes no second form. An upload is kept 7 days; delete one
nobody will confirm with `mailgoose import mailchimp delete <id> --yes`.

**Then, with the person:**

1. Verify their sending domain (`mailgoose domain add`), so campaigns come
   from their own address. Adding the DNS records is theirs to do.
2. Check each new signup form with the person
   (`mailgoose form get <form-id>`). It's a draft and takes no signups until
   it's saved (`mailgoose form update <form-id> --live`). Then give them its
   embed code (`mailgoose form embed <form-id>`) to put on their site in
   place of Mailchimp's form.
3. Fix any imported email with `errors` or `warnings` in the confirm's
   `emails`, such as a Mailchimp merge tag or conditional block mailgoose
   can't fill, and run `mailgoose email check` on each one you'll send.
4. Send a test of a campaign before it goes to everyone (step 5 above).

**What doesn't come over.** Segment rules (a fetched segment is a tag of
who's in it today), automations and journeys, reports and their history,
signup form designs and wording, Mailchimp's own details such as member
rating and location, and any merge field left out. Tell the person, so they
aren't surprised.

**When it fails:**

- `too_many_uploads`: the account has 3 uploads waiting; confirm or delete
  one.
- `too_many_members`: over the import's ceiling; export one audience at a
  time, or ask the person to contact support to have it raised.
- `mailchimp_not_configured`: connecting isn't set up here; upload the
  export instead.
- `mailchimp_not_connected` or `mailchimp_access_revoked`: the connection is
  gone; ask the person to connect again.
- `mailchimp_unavailable`: Mailchimp's API didn't answer; try again in a few
  minutes.
- `unknown_form_tag`: a `--form-tag` isn't one of the preview's groups.
- `field_limit`: more new fields than the plan's limit has room for; leave
  some out or put them into existing fields.
- `invalid_mapping`, `unknown_field` or `field_exists`: a `--field` or
  `--new-field` names a merge field the preview doesn't list, a field the
  account doesn't have, or a name that's taken.

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
