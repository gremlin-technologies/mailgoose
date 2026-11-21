# mailgoose API reference

<!-- Generated from api/openapi.yaml by `make docs`. Don't edit it by hand. -->

Version 0.1.0. Every operation has a matching `mailgoose` CLI command, named under it. For a walk-through rather than a reference, run `mailgoose docs` or read [the agent guide](agent-guide.md).

One REST API shared by the web app, the CLI and AI agents. Every endpoint
here gets a matching `mailgoose` CLI command.

Conventions every endpoint follows:

- Errors use the `Error` schema: `{"error": {"code", "message", "hint"}}`,
  with a stable `code` to branch on.
- Lists take the `Limit` and `Cursor` parameters and return
  `{"data": [...], "next_cursor": "..." | null}`.
- Every response carries `X-Request-Id`.
- POST, PUT, PATCH and DELETE accept an `Idempotency-Key` header; a repeat
  within 24 hours returns the stored response with
  `Idempotent-Replayed: true`.
- Requests are rate limited per API key; responses carry `RateLimit-Limit`
  and `RateLimit-Remaining`, and a 429 carries `Retry-After`.

The web app signs in with emailed codes and a session cookie. Browser
requests that change something and carry no `Authorization` header must
come from the web app's own origin (a 403 `cross_origin_request`
otherwise). Everything else authenticates with an API key or CLI token:
API keys sent as bearer tokens, which the CLI reads from `MAILGOOSE_API_KEY`
or its config file. Operations marked `x-required-scope: full` refuse
draft-scope keys with a 403 `insufficient_scope`.

A suspended account (`account.suspension` on the session) has no API
keys or CLI logins; its owner can still sign in to the web app. Its
session may then read (GET) for 30 days after the suspension, to export
contacts and campaigns, and sign out; everything else is a 403
`account_suspended`, as is every read after the 30 days except the
session, `getAccount` and `whoami`.

Servers:

- Staging: `https://api.staging.mailgoose.app`
- Local: `http://localhost:8080`

## Contents

- [Health](#tag-health)
- [Signing in](#tag-auth)
- [Account](#tag-account)
- [API keys](#tag-api-keys)
- [Plan and billing](#tag-plan)
- [Credits](#tag-credits)
- [Sending domains](#tag-domains)
- [Contacts](#tag-contacts)
- [Tags](#tag-tags)
- [Imports and exports](#tag-imports)
- [Images](#tag-images)
- [Emails](#tag-emails)
- [Campaigns](#tag-campaigns)
- [Signup forms](#tag-forms)
- [Operator tools (mailgoose staff only)](#tag-admin)
- [test-addresses](#tag-test-addresses)
- [sender-addresses](#tag-sender-addresses)
- [Schemas](#schemas)

<a id="tag-health"></a>

## Health

<a id="op-gethealth"></a>

### Liveness check

`GET /healthz`

- Operation: `getHealth`
- CLI: `mailgoose status`

Says the process is up. The load balancer polls it, so it does not
check the database. The CLI's `mailgoose status` calls it.

Responses:

- `200`: The API is up. [Health](#schema-health)

<a id="op-getreadiness"></a>

### Readiness check

`GET /readyz`

- Operation: `getReadiness`

Like /healthz, and also checks that the database answers.

Responses:

- `200`: The API and its database are up. [Health](#schema-health)
- `503`: The database is not configured or not reachable. [Error](#schema-error)

<a id="tag-auth"></a>

## Signing in

<a id="op-createdevicelogin"></a>

### Start a CLI login

`POST /v1/device-logins`

- Operation: `createDeviceLogin`
- CLI: `mailgoose login`

Starts the device login that `mailgoose login` uses. Show the person
`user_code` and `verification_url`; they sign in to the web app and
approve the code there. Meanwhile poll `pollDeviceLogin` with
`device_code` every `interval` seconds. The request expires after
`expires_in` seconds (10 minutes).

Request body (`application/json`, optional): [CreateDeviceLoginRequest](#schema-createdeviceloginrequest)

Responses:

- `201`: The request. [DeviceLoginStart](#schema-deviceloginstart)
- `400`: The request is malformed. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-polldevicelogin"></a>

### Collect a CLI login token

`POST /v1/device-logins/token`

- Operation: `pollDeviceLogin`
- CLI: `mailgoose login`

Polled by `mailgoose login`. Answers 202 while the request awaits
approval, and 200 with a full-scope CLI token (`mg_cli_…`) once it's
approved; the token is given out only once. A denied request is a 403
`device_login_denied`; an expired, unknown or already collected one
is a 410 `device_login_expired`. Polling faster than `interval` is a
429 `slow_down`.

Request body (`application/json`, required): [PollDeviceLoginRequest](#schema-polldeviceloginrequest)

Responses:

- `200`: Approved. The token is shown only in this response. [DeviceLoginToken](#schema-devicelogintoken)
- `202`: Not approved yet; poll again after `interval` seconds. [DeviceLoginPending](#schema-deviceloginpending)
- `403`: The person denied the request. [Error](#schema-error)
- `410`: The request expired, doesn't exist or was already collected. [Error](#schema-error)
- `400`: The request is malformed. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getdevicelogin"></a>

### Show a CLI login awaiting approval

`GET /v1/device-logins/{user_code}`

- Operation: `getDeviceLogin`

For the approval page: the request under this code, if it's still
awaiting approval. Needs a signed-in web session; API keys and CLI
tokens get a 401.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `user_code` | path | string | yes | The code `mailgoose login` printed, such as `BCDF-GHJK`. Case, spaces and the dash don't matter. |

Responses:

- `200`: The request. [DeviceLogin](#schema-devicelogin)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-approvedevicelogin"></a>

### Approve a CLI login

`POST /v1/device-logins/{user_code}/approve`

- Operation: `approveDeviceLogin`

Lets the CLI that started this request sign in as the signed-in
person, with a full-scope token. Needs a signed-in web session.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `user_code` | path | string | yes | The code `mailgoose login` printed, such as `BCDF-GHJK`. Case, spaces and the dash don't matter. |

Responses:

- `204`: Approved; the CLI collects its token on its next poll.
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-denydevicelogin"></a>

### Deny a CLI login

`POST /v1/device-logins/{user_code}/deny`

- Operation: `denyDeviceLogin`

The CLI that started this request stops waiting and signs nothing in. Needs a signed-in web session.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `user_code` | path | string | yes | The code `mailgoose login` printed, such as `BCDF-GHJK`. Case, spaces and the dash don't matter. |

Responses:

- `204`: Denied.
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-revokecurrentclitoken"></a>

### Sign out the CLI

`DELETE /v1/cli-tokens/current`

- Operation: `revokeCurrentCliToken`
- CLI: `mailgoose logout`

Revokes the CLI token this request is made with, for `mailgoose
logout`. Only CLI tokens (`mg_cli_…`); revoke API keys with
`revokeApiKey`.

Responses:

- `204`: Revoked.
- `400`: The request wasn't made with a CLI token (code `not_a_cli_token`). [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getwhoami"></a>

### Who is calling

`GET /v1/whoami`

- Operation: `getWhoami`
- CLI: `mailgoose whoami`

The account, the person and the kind of credential behind this request.

Responses:

- `200`: The caller. [Whoami](#schema-whoami)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createsignup"></a>

### Sign up

`POST /v1/signup`

- Operation: `createSignup`
- CLI: `mailgoose signup`

Creates a pending account on the free plan and emails a six-digit code
to the address; using the code with `createSession` activates the
account. The answer is the same whether or not the address already
has an account: an address with an active account gets a sign-in code
instead, and no new account is made. A repeat within a minute sends no
new email.

Request body (`application/json`, required): [SignupRequest](#schema-signuprequest)

Responses:

- `202`: A code is on its way, if the address can receive one. [CodeSent](#schema-codesent)
- `400`: The request is malformed. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createsignin"></a>

### Email a sign-in code

`POST /v1/sign-in`

- Operation: `createSignIn`

Emails a six-digit code to an address with an account; use it with
`createSession`. The answer is the same whether or not the address has
an account, and an address with no account gets no email. An account
that hasn't finished signing up gets its sign-up code instead. A repeat
within a minute sends no new email. Code requests (here and in
`createSignup`) are limited to 5 an hour per address and 20 an hour
per IP.

Request body (`application/json`, required): [SignInRequest](#schema-signinrequest)

Responses:

- `202`: A code is on its way, if the address has an account. [CodeSent](#schema-codesent)
- `400`: The request is malformed. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createclitoken"></a>

### Get a CLI token with an emailed code

`POST /v1/cli-tokens`

- Operation: `createCliToken`
- CLI: `mailgoose signup`

Uses a code from `createSignup` or `createSignIn`, like
`createSession`, but answers with a full-scope CLI token instead of
setting a session cookie. This is how `mailgoose signup` finishes
without a browser: a person still has to read the code from the email
and type it in. On success a pending account becomes active. A wrong,
expired, used or locked code gets the same `invalid_code` error; a
code stops working after 5 wrong tries. A suspended account gets
`account_suspended` (403): it has no CLI logins.

Request body (`application/json`, required): [CliTokenRequest](#schema-clitokenrequest)

Responses:

- `201`: Signed in; the token is shown only here. [DeviceLoginToken](#schema-devicelogintoken)
- `400`: The request is malformed. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createsession"></a>

### Start a web session with an emailed code

`POST /v1/sessions`

- Operation: `createSession`

Uses a code from `createSignup` or `createSignIn`. On success a
pending account becomes active, and the response sets an HttpOnly
session cookie that lasts 30 days. A wrong, expired, used or locked
code gets the same `invalid_code` error; a code stops working after 5
wrong tries. After 10 wrong codes for one address within an hour,
across all its codes and whether or not it has an account, even the
right code gets 429 with `Retry-After` until the hour passes.

Request body (`application/json`, required): [SessionRequest](#schema-sessionrequest)

Responses:

- `201`: Signed in. [Session](#schema-session)
- `400`: The request is malformed. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-deleteallsessions"></a>

### Sign out everywhere

`DELETE /v1/sessions`

- Operation: `deleteAllSessions`

Ends every web session of the signed-in person, this one included, and
clears the session cookie.

Responses:

- `204`: Signed out everywhere.
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getcurrentsession"></a>

### Who is signed in

`GET /v1/sessions/current`

- Operation: `getCurrentSession`

The account and person behind the session cookie.

Responses:

- `200`: The current session. [Session](#schema-session)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-deletecurrentsession"></a>

### Sign out

`DELETE /v1/sessions/current`

- Operation: `deleteCurrentSession`

Ends this session and clears the session cookie.

Responses:

- `204`: Signed out.
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-account"></a>

## Account

<a id="op-getaccount"></a>

### Show account settings

`GET /v1/account`

- Operation: `getAccount`
- CLI: `mailgoose account get`

The account's name, plan, default From name and address, postal
address and time zone. `missing_for_send` lists the settings a
campaign can't launch without, `send_limit` its daily send limit now
and why it's lower than usual, and `terms` the version of the terms
the account accepted against the current one.

Responses:

- `200`: The account. [AccountSettings](#schema-accountsettings)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-updateaccount"></a>

### Change account settings

`PATCH /v1/account`

- Operation: `updateAccount`
- CLI: `mailgoose account update`

Changes only the fields sent. Send `null` to remove the default From
name, From address or postal address; the name and time zone can't be
removed. The postal address goes in the footer of every email, as
anti-spam law requires, and campaigns can't launch without it. The
From address is checked for format here; a campaign can only send
from a domain verified for the account.

Request body (`application/json`, required): [UpdateAccountRequest](#schema-updateaccountrequest)

Responses:

- `200`: The account after the change. [AccountSettings](#schema-accountsettings)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-deleteaccount"></a>

### Delete the account

`DELETE /v1/account`

- Operation: `deleteAccount`
- CLI: `mailgoose account delete`
- Needs a `full`-scope key

Deletes the account and everything in it, now: contacts, emails,
sending domains (and their Amazon SES identities), API keys, CLI
logins and web sessions, and the person's sign-in. Only the billing
records the law requires stay, under a closed account with its name
and settings cleared. A paid plan's subscription is cancelled first,
at once, with no refund for the rest of the period; if Stripe can't
be reached the account isn't deleted (502), so try again. The
address can sign up again afterwards. `confirm` must be the
account's name exactly, as `getAccount` shows it. This can't be
undone.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `confirm` | query | string |  | The account's name, exactly. Required; a request without it is a 400. (It's checked after the key's scope, so a draft key always gets a 403.) |

Responses:

- `204`: Deleted. A web session's cookie is cleared.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `502`: `payment_provider_error`: Stripe couldn't make the Checkout page; try again shortly. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-acceptterms"></a>

### Accept the current terms

`POST /v1/account/terms`

- Operation: `acceptTerms`
- CLI: `mailgoose account accept-terms`
- Needs a `full`-scope key

Accepts the current version of mailgoose's terms of service,
anti-spam policy, privacy policy and data processing agreement for
the account. `version` must be the current version, as
`getAccount` shows it in `terms.current_version`; anything else is a
422 `terms_version_not_current`, so a person only accepts the version
they read. When the terms change, campaigns can't launch until the
new version is accepted (`new_terms_not_accepted`). Each acceptance
is kept, with its time and whether it came from the web app, the CLI
or an API key. Accepting the version already accepted changes
nothing.

Request body (`application/json`, required): [AcceptTermsRequest](#schema-accepttermsrequest)

Responses:

- `200`: The account, with the terms it has now accepted. [AccountSettings](#schema-accountsettings)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-api-keys"></a>

## API keys

<a id="op-listapikeys"></a>

### List API keys

`GET /v1/api-keys`

- Operation: `listApiKeys`
- CLI: `mailgoose keys list`
- Needs a `full`-scope key

The account's active API keys, oldest first. Secrets are never shown again.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |

Responses:

- `200`: A page of keys. [ApiKeyList](#schema-apikeylist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createapikey"></a>

### Create an API key

`POST /v1/api-keys`

- Operation: `createApiKey`
- CLI: `mailgoose keys create`
- Needs a `full`-scope key

Makes a key and answers with its secret, which is shown only this
once; store it somewhere safe. Keys made in production start
`mg_live_`, and keys made anywhere else start `mg_test_`. A `draft` key
can do everything except launch campaigns, change billing and manage
keys. An account can have 50 active keys.

Request body (`application/json`, required): [CreateApiKeyRequest](#schema-createapikeyrequest)

Responses:

- `201`: The new key, with its secret. [CreatedApiKey](#schema-createdapikey)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-revokeapikey"></a>

### Revoke an API key

`DELETE /v1/api-keys/{key_id}`

- Operation: `revokeApiKey`
- CLI: `mailgoose keys revoke`
- Needs a `full`-scope key

The key stops working on its next request. This can't be undone.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `key_id` | path | string (uuid) | yes | The key's `id`. |

Responses:

- `204`: Revoked.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-plan"></a>

## Plan and billing

<a id="op-getplan"></a>

### Show the account's plan

`GET /v1/plan`

- Operation: `getPlan`
- CLI: `mailgoose plan`

The account's plan (BIL-4): free, or paid by a Stripe subscription
at $5 a month or $50 a year for each plan unit. One unit covers the
first 2,500 counted contacts (subscribed and unconfirmed), and each
further 10,000, or part of one, is another. `units_needed` is what
the contacts make now; the subscription moves to it at its next
renewal.

Responses:

- `200`: The plan. [Plan](#schema-plan)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createplancheckout"></a>

### Start the paid plan

`POST /v1/plan/checkout`

- Operation: `createPlanCheckout`
- CLI: `mailgoose plan upgrade`
- Needs a `full`-scope key

Makes a Stripe Checkout page that starts the paid plan's card
subscription, billed monthly ($5 a unit) or yearly ($50 a unit), and
returns its URL; the person pays there, in a browser. Once Stripe
confirms it, the account is paid: up to its plan units' contacts, no
more free monthly credits, and credit packs to buy (BIL-5). Sales
tax is worked out at checkout. The page expires after 24 hours. An
account already on the paid plan gets a 409 `already_subscribed`;
createPlanPortal changes or cancels it.

Request body (`application/json`, required): [PlanCheckoutRequest](#schema-plancheckoutrequest)

Responses:

- `201`: The Checkout page to pay at. [PlanCheckout](#schema-plancheckout)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `409`: `plan_required`: credit packs are for the paid plan; start it with createPlanCheckout. `already_subscribed`: the account is already on the paid plan. `not_subscribed`: the account has no subscription to manage. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `502`: `payment_provider_error`: Stripe couldn't make the Checkout page; try again shortly. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createplanportal"></a>

### Manage the paid plan

`POST /v1/plan/portal`

- Operation: `createPlanPortal`
- CLI: `mailgoose plan manage`
- Needs a `full`-scope key

Makes a Stripe customer portal page for the account's subscription
and returns its URL: change the card, see invoices, or cancel. A
cancelled plan runs to the end of the period it's paid for, then the
account is free again; bought credits stay. An account that never
subscribed gets a 409 `not_subscribed`.

Responses:

- `201`: The portal page. [PlanPortal](#schema-planportal)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `409`: `plan_required`: credit packs are for the paid plan; start it with createPlanCheckout. `already_subscribed`: the account is already on the paid plan. `not_subscribed`: the account has no subscription to manage. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `502`: `payment_provider_error`: Stripe couldn't make the Checkout page; try again shortly. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-credits"></a>

## Credits

<a id="op-getcredits"></a>

### Show the credit balance

`GET /v1/credits`

- Operation: `getCredits`
- CLI: `mailgoose credits`

The account's balance, its plan, and when its next free grant comes.
One credit sends one email to one recipient. Free accounts get 1,000
credits when they activate and on each monthly anniversary of that,
and unused free credits expire when the next grant arrives.

Responses:

- `200`: The balance. [CreditSummary](#schema-creditsummary)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createcreditcheckout"></a>

### Start buying a credit pack

`POST /v1/credits/checkout`

- Operation: `createCreditCheckout`
- CLI: `mailgoose credits buy`
- Needs a `full`-scope key

Makes a Stripe Checkout page for a credit pack and returns its URL;
the person pays there, in a browser (BIL-3, BIL-6). Nothing is
charged until they do. Once Stripe says the payment went through,
the pack's credits are added, lasting 24 months. Packs are for the
paid plan (createPlanCheckout); a free account gets a 409
`plan_required`. Sales tax is worked out at checkout and Stripe
emails the receipt. The page expires after 24 hours. Packs: `10k`
($20), `50k` ($75), `200k` ($240) and `1m` ($900), unless the
server's configured otherwise.

Request body (`application/json`, required): [CreditCheckoutRequest](#schema-creditcheckoutrequest)

Responses:

- `201`: The Checkout page to pay at. [CreditCheckout](#schema-creditcheckout)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `409`: `plan_required`: credit packs are for the paid plan; start it with createPlanCheckout. `already_subscribed`: the account is already on the paid plan. `not_subscribed`: the account has no subscription to manage. [Error](#schema-error)
- `422`: `unknown_pack`: there's no pack by that name; `details.packs` lists the packs with their credits and prices. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `502`: `payment_provider_error`: Stripe couldn't make the Checkout page; try again shortly. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-listcredittransactions"></a>

### List credit transactions

`GET /v1/credits/transactions`

- Operation: `listCreditTransactions`
- CLI: `mailgoose credits history`

Every change to the balance, newest first: grants, purchases,
reservations for a send and their release, spends, fees and expiries.
Amounts are signed, so the balance is their sum.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |

Responses:

- `200`: A page of transactions. [CreditTransactionList](#schema-credittransactionlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-domains"></a>

## Sending domains

<a id="op-listdomains"></a>

### List sending domains

`GET /v1/domains`

- Operation: `listDomains`
- CLI: `mailgoose domain delete`, `mailgoose domain get`, `mailgoose domain list`, `mailgoose domain verify`

The account's sending domains, oldest first, each with its DNS records and the result of its last check.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |
| `name` | query | string |  | Only the domain with this name, such as `example.com`. |

Responses:

- `200`: A page of domains. [DomainList](#schema-domainlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createdomain"></a>

### Add a sending domain

`POST /v1/domains`

- Operation: `createDomain`
- CLI: `mailgoose domain add`

Sets the domain up in Amazon SES, with DKIM signing and a MAIL FROM
subdomain (`bounce.<domain>`), and answers with the DNS records to
add: three DKIM CNAMEs, the MAIL FROM MX and SPF TXT, and a suggested
DMARC TXT when the domain has no DMARC policy. Campaigns can only be
sent from a verified domain. The domain is checked every 5 minutes for
72 hours; `verifyDomain` checks it at once.

Request body (`application/json`, required): [CreateDomainRequest](#schema-createdomainrequest)

Responses:

- `201`: The new domain, with the records to add. [Domain](#schema-domain)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `409`: `domain_exists`: this account already has the domain. `domain_taken`: another mailgoose account has it. `domain_unavailable`: the domain is already set up in Amazon SES outside mailgoose's reach. Contact support for either of the last two. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getdomain"></a>

### Show a sending domain

`GET /v1/domains/{domain_id}`

- Operation: `getDomain`
- CLI: `mailgoose domain get`

The domain, its DNS records and the result of its last check.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `domain_id` | path | string (uuid) | yes | The domain's `id`. |

Responses:

- `200`: The domain. [Domain](#schema-domain)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-deletedomain"></a>

### Remove a sending domain

`DELETE /v1/domains/{domain_id}`

- Operation: `deleteDomain`
- CLI: `mailgoose domain delete`

Removes the domain and its Amazon SES identity. Mail can no longer be
sent from it. The DNS records can then be deleted.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `domain_id` | path | string (uuid) | yes | The domain's `id`. |

Responses:

- `204`: Removed.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-verifydomain"></a>

### Check a sending domain now

`POST /v1/domains/{domain_id}/verify`

- Operation: `verifyDomain`
- CLI: `mailgoose domain verify`

Looks up each DNS record and reports it as `found`, `missing` or
`wrong`, and asks Amazon SES whether it has verified the domain. The
domain is verified when every required record is found and SES
agrees. Checking a failed domain starts another 72 hours of background
checks; if SES had given up on its DKIM records, the domain is set up
again with new ones, and `records_changed` is true.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `domain_id` | path | string (uuid) | yes | The domain's `id`. |

Responses:

- `200`: The domain, as just checked. [Domain](#schema-domain)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-contacts"></a>

## Contacts

<a id="op-listcontacts"></a>

### List contacts

`GET /v1/contacts`

- Operation: `listContacts`
- CLI: `mailgoose contacts delete`, `mailgoose contacts export-data`, `mailgoose contacts get`, `mailgoose contacts list`, `mailgoose contacts update`, `mailgoose tags add`, `mailgoose tags remove`

The account's contacts, newest first. Filters combine; empty ones are ignored.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |
| `email` | query | string |  | Only contacts whose address contains this, ignoring case. |
| `name` | query | string |  | Only contacts whose first, last or full name contains this, ignoring case. |
| `status` | query | [ContactStatus](#schema-contactstatus) |  | Only contacts with this status. |
| `tag_id` | query | string (uuid) |  | Only contacts with this tag. |

Responses:

- `200`: A page of contacts. [ContactList](#schema-contactlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createcontact"></a>

### Add a contact

`POST /v1/contacts`

- Operation: `createContact`
- CLI: `mailgoose contacts add`

Adds one contact. The address is stored in lower case and must be new
to the account. An address that was deleted after it unsubscribed,
bounced or complained comes back with that status, whatever
`status` asks for, so it isn't mailed again by mistake. A free
account, or a tester account (MG-86) on any plan, holds at most 500
counted contacts (unconfirmed and subscribed); one more gets 402
`contact_limit`.

Request body (`application/json`, required): [CreateContactRequest](#schema-createcontactrequest)

Responses:

- `201`: The new contact. [Contact](#schema-contact)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `402`: `contact_limit`: a free account, or a tester account (MG-86) on any plan, holds at most 500 counted contacts (unconfirmed and subscribed). `details.limit` is the limit and `details.count` the counted contacts now. Other paid accounts have no cap. [Error](#schema-error)
- `409`: `contact_exists`: the account already has a contact with that address. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-exportcontacts"></a>

### Export contacts as CSV

`GET /v1/contacts/export`

- Operation: `exportContacts`
- CLI: `mailgoose export`

The account's contacts as a CSV file, oldest first, with the same
filters as listing them; with none, every contact. Columns: `email`,
`first_name`, `last_name`, `status`, `tags` (names separated by
commas), `source`, `created_at`, `status_changed_at` and
`permission_confirmed` (true or false for contacts an import added,
whether the account confirmed it had their permission; empty for
others). Importing the file back gives the same contacts and tags. A
cell that starts with `=`, `+`, `-` or `@` gets a `'` in front so a
spreadsheet doesn't run it as a formula; imports take it off again.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email` | query | string |  | Only contacts whose address contains this, ignoring case. |
| `name` | query | string |  | Only contacts whose first, last or full name contains this, ignoring case. |
| `status` | query | [ContactStatus](#schema-contactstatus) |  | Only contacts with this status. |
| `tag_id` | query | string (uuid) |  | Only contacts with this tag. |

Responses:

- `200`: The contacts, as CSV with a header row.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getcontact"></a>

### Show a contact

`GET /v1/contacts/{contact_id}`

- Operation: `getContact`
- CLI: `mailgoose contacts get`

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `contact_id` | path | string (uuid) | yes | The contact's `id`. |

Responses:

- `200`: The contact. [Contact](#schema-contact)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-updatecontact"></a>

### Change a contact

`PATCH /v1/contacts/{contact_id}`

- Operation: `updateContact`
- CLI: `mailgoose contacts update`

Changes only the fields sent; `null` removes a first or last name. A
contact that unsubscribed, bounced or complained can't be subscribed
again here (422 `contact_suppressed`): the person subscribes again
through a signup form. Bounced and complained come only from Amazon
SES.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `contact_id` | path | string (uuid) | yes | The contact's `id`. |

Request body (`application/json`, required): [UpdateContactRequest](#schema-updatecontactrequest)

Responses:

- `200`: The contact as changed. [Contact](#schema-contact)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `contact_exists`: the account already has a contact with that address. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-deletecontact"></a>

### Delete a contact

`DELETE /v1/contacts/{contact_id}`

- Operation: `deleteContact`
- CLI: `mailgoose contacts delete`

Deletes the contact and everything about it. If it had unsubscribed,
bounced or complained, only a hash of its address stays, on the
account's suppression list, so adding the address again keeps that
status.

With `erase=true`, for a person's request to erase their data, the
address also goes from import skip reports, and a hash of it always
stays on the suppression list (as unsubscribed, unless it bounced or
complained), so it isn't added back and mailed from an old list.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `contact_id` | path | string (uuid) | yes | The contact's `id`. |
| `erase` | query | boolean |  | Erase every trace of the address, keeping only a hash on the suppression list. |

Responses:

- `204`: Deleted.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getcontactdata"></a>

### Everything stored about a contact

`GET /v1/contacts/{contact_id}/data`

- Operation: `getContactData`
- CLI: `mailgoose contacts export-data`

For a person's data request: the contact, when each tag was put on
it, the import that added it and whether the account confirmed
permission for it, and import rows with its address that weren't
added. Delete with `erase=true` removes all of it.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `contact_id` | path | string (uuid) | yes | The contact's `id`. |

Responses:

- `200`: The contact's data. [ContactData](#schema-contactdata)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-tags"></a>

## Tags

<a id="op-listtags"></a>

### List tags

`GET /v1/tags`

- Operation: `listTags`
- CLI: `mailgoose contacts list`, `mailgoose export`, `mailgoose tags add`, `mailgoose tags delete`, `mailgoose tags list`, `mailgoose tags remove`, `mailgoose tags rename`

The account's tags by name, each with how many contacts have it.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |
| `name` | query | string |  | Only the tag with this name, ignoring case. |

Responses:

- `200`: A page of tags. [TagList](#schema-taglist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createtag"></a>

### Create a tag

`POST /v1/tags`

- Operation: `createTag`
- CLI: `mailgoose tags create`

Request body (`application/json`, required): [CreateTagRequest](#schema-createtagrequest)

Responses:

- `201`: The new tag. [Tag](#schema-tag)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `409`: `tag_exists`: the account already has a tag with that name, ignoring case. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-renametag"></a>

### Rename a tag

`PATCH /v1/tags/{tag_id}`

- Operation: `renameTag`
- CLI: `mailgoose tags rename`

Contacts keep the tag under its new name.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `tag_id` | path | string (uuid) | yes | The tag's `id`. |

Request body (`application/json`, required): [RenameTagRequest](#schema-renametagrequest)

Responses:

- `200`: The renamed tag. [Tag](#schema-tag)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `tag_exists`: the account already has a tag with that name, ignoring case. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-deletetag"></a>

### Delete a tag

`DELETE /v1/tags/{tag_id}`

- Operation: `deleteTag`
- CLI: `mailgoose tags delete`

Takes the tag off every contact and off the recipients of draft and
scheduled campaigns, then deletes it. The response counts both.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `tag_id` | path | string (uuid) | yes | The tag's `id`. |

Responses:

- `200`: What deleting the tag changed. [TagDeleted](#schema-tagdeleted)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-tagcontacts"></a>

### Tag contacts

`POST /v1/tags/{tag_id}/contacts`

- Operation: `tagContacts`
- CLI: `mailgoose tags add`

Puts the tag on the contacts listed in `contact_ids` (up to 1,000), or
on every contact matching `filter`; an empty filter is every contact.
Ids that aren't this account's are skipped.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `tag_id` | path | string (uuid) | yes | The tag's `id`. |

Request body (`application/json`, required): [ContactSelection](#schema-contactselection)

Responses:

- `200`: How many contacts were selected and how many gained the tag. [TagChange](#schema-tagchange)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-untagcontacts"></a>

### Untag contacts

`POST /v1/tags/{tag_id}/contacts/remove`

- Operation: `untagContacts`
- CLI: `mailgoose tags remove`

Takes the tag off the selected contacts, chosen as in `tagContacts`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `tag_id` | path | string (uuid) | yes | The tag's `id`. |

Request body (`application/json`, required): [ContactSelection](#schema-contactselection)

Responses:

- `200`: How many contacts were selected and how many lost the tag. [TagChange](#schema-tagchange)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `422`: The request is well formed but a field is wrong. `code` says which problem, such as `invalid_email`, `invalid_account_name`, `terms_not_accepted` or `invalid_code`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-imports"></a>

## Imports and exports

<a id="op-listimports"></a>

### List CSV imports

`GET /v1/imports`

- Operation: `listImports`
- CLI: `mailgoose import list`

The account's imports, newest first.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |

Responses:

- `200`: A page of imports. [ImportList](#schema-importlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createimport"></a>

### Import contacts from a CSV file

`POST /v1/imports`

- Operation: `createImport`
- CLI: `mailgoose import`

Checks the file and queues it; the import runs in the background, so
poll `getImport` until `status` is `done` or `failed`. The first row
is the header. Columns for email, first name and last name are found
from common headers ("Email", "Email Address", "First Name",
"Surname" and so on), or named in `columns`. Commas, semicolons and
tabs all work as separators.

Imported contacts are `unconfirmed` unless `permission_confirmed` says
the account has these people's permission to mail them; then they're
`subscribed`. Rows with a bad address, a repeat of an earlier row, an
address already in the account, a suppressed address, or one past a
free account's 500-contact limit are skipped and listed by
`listImportSkippedRows`. Existing contacts aren't changed.

Every import says where its people came from in `list_source`; an
import carries no proof that they asked for mail, the way a form
signup does. Each added contact's consent record (`getContactData`)
keeps the source and when it was given.

The file's addresses are checked as it's queued: `checks` counts rows
that aren't addresses, role addresses (info@, sales@ ...) and
addresses at disposable (throwaway) mail domains. An import is held
for review when role and disposable addresses are over 10% of its
valid rows, rows that aren't addresses are over 5% of all rows, or at
least half of its addresses (and at least 200) are already on lists
imported to other mailgoose accounts. So is an account's first import
of more than 2,000 rows, and an import that would take a free account
past its limit. A held import's contacts are added but not mailed
until mailgoose approves it; `hold_message` says why it's held.

The request body can be up to 10 MB.

Request body (`application/json`, required): [CreateImportRequest](#schema-createimportrequest)

Responses:

- `202`: The import, queued. [Import](#schema-import)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `413`: `request_too_large`: the request body is over the operation's limit. [Error](#schema-error)
- `422`: The file can't be imported, and nothing was queued. `code` says why: `no_rows`, `no_email_column` (name the column in `columns.email`), `invalid_mapping` (a named column isn't in the header), `invalid_encoding` (not UTF-8), `invalid_csv` (a quote never closes), `too_many_rows` (over 100,000), `file_too_large`, `tag_not_found`, `too_many_tags`, `list_source_required` (say where the list came from), `list_source_detail_required` (describe an `other` source) or `invalid_list_source`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getimport"></a>

### Get an import

`GET /v1/imports/{import_id}`

- Operation: `getImport`
- CLI: `mailgoose import`, `mailgoose import status`

How far the import got, what it added and how many rows it skipped, by reason.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `import_id` | path | string (uuid) | yes | The import's `id`. |

Responses:

- `200`: The import. [Import](#schema-import)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-listimportskippedrows"></a>

### List an import's skipped rows

`GET /v1/imports/{import_id}/skipped-rows`

- Operation: `listImportSkippedRows`
- CLI: `mailgoose import`, `mailgoose import status`

Every row the import didn't add and why, in file order.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `import_id` | path | string (uuid) | yes | The import's `id`. |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |

Responses:

- `200`: A page of skipped rows. [SkippedRowList](#schema-skippedrowlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-images"></a>

## Images

<a id="op-listimages"></a>

### List uploaded images

`GET /v1/images`

- Operation: `listImages`
- CLI: `mailgoose image list`

The account's uploaded images, newest first.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |

Responses:

- `200`: A page of images. [ImageList](#schema-imagelist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-uploadimage"></a>

### Upload an image

`POST /v1/images`

- Operation: `uploadImage`
- CLI: `mailgoose image upload`

Stores a picture for image blocks and answers with its `url`, which
goes in an image block's `src`. JPEG, PNG, GIF and WebP up to 5 MB
are accepted, checked by what the file holds, not its name, and an
account keeps up to 1,000. The file
is sent base64-encoded in `data`, so the request body can be up to
7 MB. `mailgoose image upload ./pic.png` does the encoding.

Request body (`application/json`, required): [UploadImageRequest](#schema-uploadimagerequest)

Responses:

- `201`: The stored image. [Image](#schema-image)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `413`: `request_too_large`: the request body is over the operation's limit. [Error](#schema-error)
- `422`: `invalid_image`: the file is empty, over 5 MB, or isn't a JPEG, PNG, GIF or WebP; `details.detected` says what it looks like when it's another kind of file. `too_many_images`: the account already keeps 1,000 images. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-emails"></a>

## Emails

<a id="op-listemails"></a>

### List emails

`GET /v1/emails`

- Operation: `listEmails`
- CLI: `mailgoose email list`

The account's emails, newest first, without their content.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |

Responses:

- `200`: A page of emails. [EmailList](#schema-emaillist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createemail"></a>

### Create an email

`POST /v1/emails`

- Operation: `createEmail`
- CLI: `mailgoose email create`

Makes an email of kind `blocks` (the default) or `html`. Without a
`document`, a blocks email starts with only its footer and an html
email with empty HTML; both get default settings. The kind can't
change later.

Request body (`application/json`, required): [CreateEmailRequest](#schema-createemailrequest)

Responses:

- `201`: The new email. [Email](#schema-email)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `422`: `invalid_document`: the document breaks the schema; `message` names the place, such as `blocks[2].href`, and `details.path` holds it. `invalid_name` (empty or over 200 characters), `invalid_kind` (not blocks or html) or `kind_mismatch` (a document of the other kind). `invalid_html`: an html document's markup has errors, listed in `details.issues` as importEmailHtml describes; empty markup is allowed. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getemail"></a>

### Get an email

`GET /v1/emails/{email_id}`

- Operation: `getEmail`
- CLI: `mailgoose email blocks add`, `mailgoose email blocks move`, `mailgoose email blocks remove`, `mailgoose email blocks update`, `mailgoose email get`, `mailgoose email import-html`, `mailgoose email replace`, `mailgoose email update`

The email with its whole document.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |

Responses:

- `200`: The email. [Email](#schema-email)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-updateemail"></a>

### Rename an email or change its settings

`PATCH /v1/emails/{email_id}`

- Operation: `updateEmail`
- CLI: `mailgoose email update`

Changes the name, some settings, or both. Settings left out keep their
values. `version` must be the email's current version; if someone
saved it since, nothing changes and the answer is a 409
`version_conflict`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |

Request body (`application/json`, required): [UpdateEmailRequest](#schema-updateemailrequest)

Responses:

- `200`: The email as saved, with its new version. [Email](#schema-email)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `version_conflict`: the email was saved since the version given was read, and nothing changed. `details.current_version` is its version now; read it again and redo the change. [Error](#schema-error)
- `422`: `invalid_document`: the document breaks the schema; `message` names the place, such as `blocks[2].href`, and `details.path` holds it. `invalid_name` (empty or over 200 characters), `invalid_kind` (not blocks or html) or `kind_mismatch` (a document of the other kind). `invalid_html`: an html document's markup has errors, listed in `details.issues` as importEmailHtml describes; empty markup is allowed. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-deleteemail"></a>

### Delete an email

`DELETE /v1/emails/{email_id}`

- Operation: `deleteEmail`
- CLI: `mailgoose email delete`

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |

Responses:

- `204`: Deleted.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-replaceemaildocument"></a>

### Replace an email's whole document

`PUT /v1/emails/{email_id}/document`

- Operation: `replaceEmailDocument`
- CLI: `mailgoose email replace`

Swaps the settings and content for the `document` given, checked
whole. A document read with getEmail and sent back unchanged saves
unchanged. Blocks keep the ids they bring; a block without an `id`
gets a new one, and a blocks document without a footer gets one at
the end. `version` works as in updateEmail.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |

Request body (`application/json`, required): [ReplaceEmailDocumentRequest](#schema-replaceemaildocumentrequest)

Responses:

- `200`: The email as saved, with its new version. [Email](#schema-email)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `version_conflict`: the email was saved since the version given was read, and nothing changed. `details.current_version` is its version now; read it again and redo the change. [Error](#schema-error)
- `422`: `invalid_document`: the document breaks the schema; `message` names the place, such as `blocks[2].href`, and `details.path` holds it. `invalid_name` (empty or over 200 characters), `invalid_kind` (not blocks or html) or `kind_mismatch` (a document of the other kind). `invalid_html`: an html document's markup has errors, listed in `details.issues` as importEmailHtml describes; empty markup is allowed. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-importemailhtml"></a>

### Replace an html email's markup

`PUT /v1/emails/{email_id}/html`

- Operation: `importEmailHtml`
- CLI: `mailgoose email import-html`

Swaps an html email's markup for `html`, whole, after checking it.
Errors stop the import and nothing changes: malformed markup,
script elements, forms, frames, embedded objects, external
stylesheets, inline event handlers, `javascript:` addresses, no
`{{unsubscribe_url}}`, or more than 102 KB. CSS that major clients
drop is only a warning; the import goes ahead and returns them.
Every problem has the line it's on. Settings stay as they are.
When sent, an email that doesn't show the account's postal address
gets it in a footer at the bottom. `version` works as in
updateEmail.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |

Request body (`application/json`, required): [ImportEmailHtmlRequest](#schema-importemailhtmlrequest)

Responses:

- `200`: The email as saved, with its new version, and any warnings. [ImportEmailHtmlResult](#schema-importemailhtmlresult)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `version_conflict`: the email was saved since the version given was read, and nothing changed. `details.current_version` is its version now; read it again and redo the change. [Error](#schema-error)
- `422`: `invalid_html`: the markup has errors and nothing changed. `details.issues` lists each, with its `line`, `code` and `message`; `message` names the first. `kind_mismatch`: the email is a blocks email, which has no markup. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-previewemail"></a>

### Preview an email as it will look

`GET /v1/emails/{email_id}/preview`

- Operation: `previewEmail`
- CLI: `mailgoose email preview`

Renders the email as it will be sent, filled in with sample data:
first name Alex, last name Rivera, email alex@example.com, and an
unsubscribe link that goes nowhere. `desktop` returns the email
itself; `mobile` returns a page that shows it in a 375 pixel frame,
so its narrow-screen rules apply. Until the account has a postal
address, the footer shows a placeholder where it goes. The page
runs no scripts.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |
| `width` | query | string: one of `desktop`, `mobile` |  | desktop (the default) or mobile. |

Responses:

- `200`: The rendered email, as HTML.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `422`: `invalid_merge_tags`: a merge tag can't be filled or a link is unsafe; `details.problems` lists each with its `path` (such as `blocks[2].text`, or `html`) and `message`. `empty_email`: an html email with no markup yet. `invalid_html`: the html email's markup fails the import check (possible only for markup saved before a check was added); `details.problems` lists each with its `line`, `code` and `message`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-checkemail"></a>

### Check an email before sending it

`POST /v1/emails/{email_id}/check`

- Operation: `checkEmail`
- CLI: `mailgoose email check`

Looks the email over and answers what would stop it going
(`errors`) and what some readers would trip on (`warnings`). Errors
block a launch; warnings don't. The answer is 200 either way; `ok`
is false when there are errors.

Error codes: `missing_subject`, `missing_from` (no From on the email
or the account), `unverified_from` (the From isn't on a verified
sending domain, nor, with shared sending on, a verified sender
address), `unverified_reply_to` (the email would go from the shared
sending domain, and its Reply-To isn't a verified sender or on a
verified domain), `missing_postal_address`, `empty_email`,
`merge_tag` (a tag that can't be filled), `missing_unsubscribe`,
`broken_link` (not a full web address, or the page or site doesn't
exist), `too_large` (over 102 KB as rendered), `invalid_html`, and
`unsafe_link` (on Google Web Risk's malware, phishing or unwanted
software lists; `url` names it).

Warning codes: `insecure_link` (http://), `link_unreachable` (an
error status or a timeout), `link_not_checked` (a private address,
a port other than 80 or 443, the check ran out of time, or links were
checked too often), `link_safety_not_checked` (links weren't looked
up on the phishing and malware lists: the lookup isn't set up, or
didn't answer within 2 seconds; they pass), `missing_alt`,
`near_size_limit` (over 90 KB), `unsupported_css`, and
`shared_domain` (the email goes from mailgoose's shared sending
domain because its From isn't on a verified domain; adding the
domain is better for deliverability and trust).

Each link is fetched once with a HEAD request (GET if the site
refuses HEAD), 3 seconds per link and 10 seconds in all, up to 50
links. Links to private, loopback or link-local addresses, or to ports
other than 80 and 443, aren't fetched, whichever way they get there.
One account's checks fetch links at most 10 times a minute; later
checks still run, with their links reported as not checked.

Every web link is also looked up on Google Web Risk's lists, by
where it goes (one of mailgoose's own tracked links by its
target). Answers are cached, so a campaign's links are looked up
once. A listed link also stops a test send, and stops a launch and
pauses the account's sending.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |

Responses:

- `200`: What the check found. [EmailCheck](#schema-emailcheck)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-duplicateemail"></a>

### Duplicate an email

`POST /v1/emails/{email_id}/duplicate`

- Operation: `duplicateEmail`
- CLI: `mailgoose email duplicate`

Copies the email's settings and content into a new email, named
"Copy of" the original unless a `name` is given. Block ids stay the
same in the copy.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |

Request body (`application/json`, optional): [DuplicateEmailRequest](#schema-duplicateemailrequest)

Responses:

- `201`: The new copy. [Email](#schema-email)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `422`: `invalid_document`: the document breaks the schema; `message` names the place, such as `blocks[2].href`, and `details.path` holds it. `invalid_name` (empty or over 200 characters), `invalid_kind` (not blocks or html) or `kind_mismatch` (a document of the other kind). `invalid_html`: an html document's markup has errors, listed in `details.issues` as importEmailHtml describes; empty markup is allowed. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-listemailblocks"></a>

### List an email's blocks

`GET /v1/emails/{email_id}/blocks`

- Operation: `listEmailBlocks`
- CLI: `mailgoose email blocks list`

Every block of a blocks email in document order, a columns block's
contents right after it, each with where it is. `version` is the
email's version, to name in the next change.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |

Responses:

- `200`: The blocks. [EmailBlockList](#schema-emailblocklist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `422`: `kind_mismatch`: the email is html, which has no blocks. `invalid_document`: the block or the fields break the schema, or the change would (such as columns inside a column); `details.path` names the place. `footer_locked`: the change would remove or move the footer, add a second one, or put a block after it. `invalid_position`: the place can't be used, such as `after` a block that isn't there or `column` without `parent_id`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-addemailblock"></a>

### Add a block

`POST /v1/emails/{email_id}/blocks`

- Operation: `addEmailBlock`
- CLI: `mailgoose email blocks add`

Adds one block to a blocks email. Where it goes: `after` a block's id
(in that block's list), or `index` in the top level, or in column
`column` (0 left, 1 right) of the columns block `parent_id`. With no
position the block goes last, which at the top level is just before
the footer. The block is checked as in a whole document; leave out
its `id` to get a new one. `version` works as in updateEmail.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |

Request body (`application/json`, required): [AddEmailBlockRequest](#schema-addemailblockrequest)

Responses:

- `201`: The block as saved, and the email with its new version. [EmailBlockChange](#schema-emailblockchange)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `version_conflict`: the email was saved since the version given was read, and nothing changed. `details.current_version` is its version now; read it again and redo the change. [Error](#schema-error)
- `422`: `kind_mismatch`: the email is html, which has no blocks. `invalid_document`: the block or the fields break the schema, or the change would (such as columns inside a column); `details.path` names the place. `footer_locked`: the change would remove or move the footer, add a second one, or put a block after it. `invalid_position`: the place can't be used, such as `after` a block that isn't there or `column` without `parent_id`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-updateemailblock"></a>

### Change a block's fields

`PATCH /v1/emails/{email_id}/blocks/{block_id}`

- Operation: `updateEmailBlock`
- CLI: `mailgoose email blocks update`

Sets the `fields` given on one block, wherever it is. Fields left out
keep their values. The block's `id` and `type` can't change.
`version` works as in updateEmail.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |
| `block_id` | path | [BlockId](#schema-blockid) | yes | The block's `id`. |

Request body (`application/json`, required): [UpdateEmailBlockRequest](#schema-updateemailblockrequest)

Responses:

- `200`: The block as saved, and the email with its new version. [EmailBlockChange](#schema-emailblockchange)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such email in this account. `block_not_found`: the email has no block with that id. [Error](#schema-error)
- `409`: `version_conflict`: the email was saved since the version given was read, and nothing changed. `details.current_version` is its version now; read it again and redo the change. [Error](#schema-error)
- `422`: `kind_mismatch`: the email is html, which has no blocks. `invalid_document`: the block or the fields break the schema, or the change would (such as columns inside a column); `details.path` names the place. `footer_locked`: the change would remove or move the footer, add a second one, or put a block after it. `invalid_position`: the place can't be used, such as `after` a block that isn't there or `column` without `parent_id`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-removeemailblock"></a>

### Remove a block

`DELETE /v1/emails/{email_id}/blocks/{block_id}`

- Operation: `removeEmailBlock`
- CLI: `mailgoose email blocks remove`

Removes one block, and a columns block's contents with it. The footer
can't be removed. `version` is the email's version when you read it,
as in updateEmail.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |
| `block_id` | path | [BlockId](#schema-blockid) | yes | The block's `id`. |
| `version` | query | integer | yes | The email's version when you read it. |

Responses:

- `200`: The email as saved, with its new version. [Email](#schema-email)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such email in this account. `block_not_found`: the email has no block with that id. [Error](#schema-error)
- `409`: `version_conflict`: the email was saved since the version given was read, and nothing changed. `details.current_version` is its version now; read it again and redo the change. [Error](#schema-error)
- `422`: `kind_mismatch`: the email is html, which has no blocks. `invalid_document`: the block or the fields break the schema, or the change would (such as columns inside a column); `details.path` names the place. `footer_locked`: the change would remove or move the footer, add a second one, or put a block after it. `invalid_position`: the place can't be used, such as `after` a block that isn't there or `column` without `parent_id`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-moveemailblock"></a>

### Move a block

`POST /v1/emails/{email_id}/blocks/{block_id}/move`

- Operation: `moveEmailBlock`
- CLI: `mailgoose email blocks move`

Moves one block, and a columns block's contents with it, to a new
place, given as in addEmailBlock. `index` is the place the block
ends up at. The footer can't move, and nothing can go after it.
`version` works as in updateEmail.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `email_id` | path | string (uuid) | yes | The email's `id`. |
| `block_id` | path | [BlockId](#schema-blockid) | yes | The block's `id`. |

Request body (`application/json`, required): [MoveEmailBlockRequest](#schema-moveemailblockrequest)

Responses:

- `200`: The block as saved, and the email with its new version. [EmailBlockChange](#schema-emailblockchange)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such email in this account. `block_not_found`: the email has no block with that id. [Error](#schema-error)
- `409`: `version_conflict`: the email was saved since the version given was read, and nothing changed. `details.current_version` is its version now; read it again and redo the change. [Error](#schema-error)
- `422`: `kind_mismatch`: the email is html, which has no blocks. `invalid_document`: the block or the fields break the schema, or the change would (such as columns inside a column); `details.path` names the place. `footer_locked`: the change would remove or move the footer, add a second one, or put a block after it. `invalid_position`: the place can't be used, such as `after` a block that isn't there or `column` without `parent_id`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-campaigns"></a>

## Campaigns

<a id="op-listcampaigns"></a>

### List campaigns

`GET /v1/campaigns`

- Operation: `listCampaigns`
- CLI: `mailgoose campaign list`

The account's campaigns, newest first. List items leave out `recipient_count`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |
| `status` | query | [CampaignStatus](#schema-campaignstatus) |  | Only campaigns in this status. |

Responses:

- `200`: A page of campaigns. [CampaignList](#schema-campaignlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createcampaign"></a>

### Create a campaign

`POST /v1/campaigns`

- Operation: `createCampaign`
- CLI: `mailgoose campaign create`

Makes a draft campaign: an email, who it goes to and when. Nothing is
sent until it's launched. `recipients` is `{"all": true}` for every
subscribed contact, or `{"tag_ids": [...]}` for the subscribed
contacts with any of the tags. Leave `send_at` out (or null) to send
when launched.

Request body (`application/json`, required): [CreateCampaignRequest](#schema-createcampaignrequest)

Responses:

- `201`: The new campaign. [Campaign](#schema-campaign)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `422`: `invalid_campaign`: a part breaks a rule; `details.field` names it (`name`, `email_id`, `recipients` or `send_at`). `unknown_email` and `unknown_tag`: an id isn't one of the account's. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-countcampaignrecipients"></a>

### Count recipients

`POST /v1/campaigns/recipient-count`

- Operation: `countCampaignRecipients`
- CLI: `mailgoose campaign count`

Counts who a choice of recipients would reach now: subscribed
contacts only, the same way a campaign picks them. Changes nothing.

Request body (`application/json`, required): [RecipientCountRequest](#schema-recipientcountrequest)

Responses:

- `200`: The count. [RecipientCount](#schema-recipientcount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `422`: `invalid_campaign`: a part breaks a rule; `details.field` names it (`name`, `email_id`, `recipients` or `send_at`). `unknown_email` and `unknown_tag`: an id isn't one of the account's. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getcampaign"></a>

### Get a campaign

`GET /v1/campaigns/{campaign_id}`

- Operation: `getCampaign`
- CLI: `mailgoose campaign get`

The campaign, with `recipient_count` counted now.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `campaign_id` | path | string (uuid) | yes | The campaign's `id`. |

Responses:

- `200`: The campaign. [Campaign](#schema-campaign)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-updatecampaign"></a>

### Change a campaign

`PATCH /v1/campaigns/{campaign_id}`

- Operation: `updateCampaign`
- CLI: `mailgoose campaign update`

Changes the parts given; the rest stay. Only drafts can be changed:
a launched campaign was confirmed as it is. `send_at: null` sends
when launched. With `version`, the change is
refused if the campaign changed since that version was read.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `campaign_id` | path | string (uuid) | yes | The campaign's `id`. |

Request body (`application/json`, required): [UpdateCampaignRequest](#schema-updatecampaignrequest)

Responses:

- `200`: The changed campaign. [Campaign](#schema-campaign)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `campaign_locked`: only drafts can be changed, tested, launched or deleted; `details.status` is the campaign's status. `version_conflict`: the campaign changed since the version given; `details.current_version` is its version now. [Error](#schema-error)
- `422`: `invalid_campaign`: a part breaks a rule; `details.field` names it (`name`, `email_id`, `recipients` or `send_at`). `unknown_email` and `unknown_tag`: an id isn't one of the account's. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-deletecampaign"></a>

### Delete a draft campaign

`DELETE /v1/campaigns/{campaign_id}`

- Operation: `deleteCampaign`
- CLI: `mailgoose campaign delete`

Deletes a draft. Campaigns past draft stay, for their record.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `campaign_id` | path | string (uuid) | yes | The campaign's `id`. |

Responses:

- `204`: Deleted.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `campaign_locked`: only drafts can be changed, tested, launched or deleted; `details.status` is the campaign's status. `version_conflict`: the campaign changed since the version given; `details.current_version` is its version now. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-testcampaign"></a>

### Send a test of a campaign

`POST /v1/campaigns/{campaign_id}/test`

- Operation: `testCampaign`
- CLI: `mailgoose campaign test`

Sends the campaign's email, rendered as recipients will get it, to
the account owner, or to up to 5 addresses (`to`) that are the
owner's or on one of the account's verified sending domains. Each
subject starts `[Test] `, and the unsubscribe link and header point
at a page that only says it's a test. Tests use no credits; an
account can send 50 test emails in any 24 hours. The campaign
records the test in `last_test`, which launch checks. Drafts only.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `campaign_id` | path | string (uuid) | yes | The campaign's `id`. |

Request body (`application/json`, optional): [TestCampaignRequest](#schema-testcampaignrequest)

Responses:

- `200`: Sent. [TestSendResult](#schema-testsendresult)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `campaign_locked`: only drafts can be changed, tested, launched or deleted; `details.status` is the campaign's status. `version_conflict`: the campaign changed since the version given; `details.current_version` is its version now. [Error](#schema-error)
- `422`: `not_ready`: something stops the email being sent; `details.problems` lists each with its `code` (`no_email`, `missing_from`, `unverified_from`, `unverified_reply_to`, `missing_postal_address`, `missing_subject`, `empty_email`, `merge_tag`, `invalid_html`, `unsafe_link` with the link in `url`), `message` and `hint`. `invalid_test`: `to` has an address that isn't one, or more than 5. `test_recipient_not_allowed`: an address in `to` isn't the owner's or on a verified sending domain; `details.address` names it. `test_recipient_suppressed`: an address in `to` unsubscribed, bounced or complained (the owner's own address is never refused); `details.address` names it. `test_mode`: the account is in test mode (MG-80) and an address in `to` is neither the owner's nor a confirmed test address, whatever its domain; `details.address` names it. `tester_limit`: a tester account (MG-86) has ended, or the test needs more tester credits than are left (simulator addresses need none); `details` has `needed`, `credits`, `used`, `left`, `until` and `ended`. A tester account may also test to SES mailbox simulator addresses (`*@simulator.amazonses.com`). [Error](#schema-error)
- `429`: `test_limit`: the account has sent 50 test emails in the last 24 hours; Retry-After says when enough have aged out. `rate_limited`: the API's request limit. [Error](#schema-error)
- `502`: `test_send_failed`: the mail provider refused or didn't answer. It still counts toward the day's tests. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-launchcampaign"></a>

### Launch a campaign, after confirmation

`POST /v1/campaigns/{campaign_id}/launch`

- Operation: `launchCampaign`
- CLI: `mailgoose campaign launch`
- Needs a `full`-scope key

Launching takes two calls (CAM-3, CAM-4).

1. Without `confirm`, nothing is launched. If the campaign can go
   out, the answer is a 409 `confirmation_required` whose
   `details.summary` has the subject, From, recipient count, send
   time and credit cost, and `details.code` a one-time code. If it
   can't, a 422 `not_ready` lists every reason.
2. Show the summary to the person. Only after they say yes, call
   again with `confirm` set to the code. That launches it: in one
   transaction the compiled email is copied into the campaign, its
   recipients are fixed, one credit each is reserved, and the send
   is queued. A campaign with a send time becomes `scheduled`;
   otherwise `sending`.

A code lasts 15 minutes, works once, allows 5 wrong tries, and
stops working if the campaign or its email changes; ask for a new
summary then. Launching a campaign that's already launched, or a
retry with the same `Idempotency-Key`, changes nothing and returns
the same result. A draft-scope key gets a 403.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `campaign_id` | path | string (uuid) | yes | The campaign's `id`. |

Request body (`application/json`, optional): [LaunchCampaignRequest](#schema-launchcampaignrequest)

Responses:

- `200`: Launched, now or by an earlier call. [LaunchResult](#schema-launchresult)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `confirmation_required`: nothing was launched. `details.summary` (`campaign_id`, `name`, `subject`, `from`, `reply_to`, `shared_domain`, `sender`, `recipient_count`, `send_at`, `credits`, `balance`, `warnings`) is what the person confirms. `shared_domain` is true when the campaign goes from mailgoose's shared sending domain (MG-85) because its From isn't on a verified domain: `from` is then the account's shared address, and `sender` the verified address it was set to go from (null otherwise). Show it, with a nudge to add the sender's domain; `details.code` the code that launches it once they say yes, and `details.expires_at` when the code stops working. `campaign_locked`: the campaign isn't a draft and wasn't launched; `details.status` is its status. [Error](#schema-error)
- `422`: `not_ready`: the campaign can't launch; `details.problems` lists each reason with its `code`, `message` and `hint`: Prepare's (`no_email`, `missing_from`, `unverified_from`, `unverified_reply_to` (on the shared sending domain, the Reply-To must be a verified sender or on a verified domain), `missing_postal_address`, `missing_subject`, `empty_email`, `merge_tag`), `check_failed` (an email check error), `unsafe_link` (a link on Google Web Risk's malware, phishing or unwanted software lists, named in `url`; it also pauses the account's sending), `untested` (no test since the last change), `send_at_passed`, `account_paused`, `account_suspended`, `account_in_review` (a new account waiting for its sign-up review; not for an account in test mode), `test_mode` (the account is in test mode and the campaign reaches anyone but its confirmed test addresses and its owner; `details.recipients` and `details.not_test_addresses`), `new_terms_not_accepted` (the terms changed and the account hasn't accepted the new version; `details.current_version` and `details.accepted_version`, then `acceptTerms`), `subscription_past_due` (sending is paused until the paid plan's payment goes through), `no_recipients`, `credits_short` with `details.needed`, `details.balance` and `details.short`, and `tester_limit` (a tester account, MG-86, that has ended, or whose campaign needs more tester credits than are left; `details.needed`, `details.credits`, `details.used`, `details.left`, `details.until` and `details.ended`). `invalid_confirmation`: the code can't be used; `details.reason` is `no_code`, `expired`, `wrong`, `too_many_attempts`, `changed`, or `audience_grew` (the campaign now reaches more than a tenth more contacts than the summary counted, with `details.summarized_count` and `details.recipient_count`). Ask for a new summary. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-cancelcampaign"></a>

### Cancel a scheduled campaign, or a waiting slice's rest

`POST /v1/campaigns/{campaign_id}/cancel`

- Operation: `cancelCampaign`
- CLI: `mailgoose campaign cancel`
- Needs a `full`-scope key

Stops a scheduled campaign before its send time (CAM-6). Nobody gets
it, and the credits reserved at launch come back. A campaign whose
first slice is out and whose rest is waiting (`slice.state`
`slice_hold` or `held`) can be cancelled the same way: the rest
never goes and its credits come back. Cancelling a cancelled
campaign changes nothing. Once it is sending it can't be cancelled
(409 `campaign_locked`).

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `campaign_id` | path | string (uuid) | yes | The campaign's `id`. |

Responses:

- `200`: Cancelled, now or before. [Campaign](#schema-campaign)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `campaign_locked`: only drafts can be changed, tested, launched or deleted; `details.status` is the campaign's status. `version_conflict`: the campaign changed since the version given; `details.current_version` is its version now. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getcampaignreport"></a>

### Get a campaign's report

`GET /v1/campaigns/{campaign_id}/report`

- Operation: `getCampaignReport`
- CLI: `mailgoose campaign report`

What happened to a campaign (REP-1, REP-2): copies sent, delivered,
hard bounced, complaints, unsubscribes, unique opens (always an
estimate), unique and total clicks, credits used, and clicks per
link. It's counted from the stored events when asked, so it's as
fresh as they are (deliveries, bounces and complaints arrive from
SES within a minute or two). A draft's numbers are all 0. Raw events
are kept 12 months after launch; after that the report is the one
saved then, with `archived` true.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `campaign_id` | path | string (uuid) | yes | The campaign's `id`. |

Responses:

- `200`: The report. [CampaignReport](#schema-campaignreport)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-reschedulecampaign"></a>

### Move a scheduled campaign's send time

`POST /v1/campaigns/{campaign_id}/reschedule`

- Operation: `rescheduleCampaign`
- CLI: `mailgoose campaign reschedule`
- Needs a `full`-scope key

Moves a scheduled campaign to a new send time (CAM-6), in the future
and at most a year ahead. Nothing else about it changes. Once it has
started sending it can't be moved (409 `campaign_locked`).

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `campaign_id` | path | string (uuid) | yes | The campaign's `id`. |

Request body (`application/json`, required): [RescheduleCampaignRequest](#schema-reschedulecampaignrequest)

Responses:

- `200`: Moved. [Campaign](#schema-campaign)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `campaign_locked`: only drafts can be changed, tested, launched or deleted; `details.status` is the campaign's status. `version_conflict`: the campaign changed since the version given; `details.current_version` is its version now. [Error](#schema-error)
- `422`: `invalid_campaign`: a part breaks a rule; `details.field` names it (`name`, `email_id`, `recipients` or `send_at`). `unknown_email` and `unknown_tag`: an id isn't one of the account's. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-forms"></a>

## Signup forms

<a id="op-listforms"></a>

### List signup forms

`GET /v1/forms`

- Operation: `listForms`
- CLI: `mailgoose form list`

The account's signup forms, newest first.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |

Responses:

- `200`: A page of forms. [FormList](#schema-formlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-createform"></a>

### Create a signup form

`POST /v1/forms`

- Operation: `createForm`
- CLI: `mailgoose form create`

Makes a signup form. Parts left out take their defaults: an email
field only, a "Subscribe" button, double opt-in on, and a success
message and confirmation email that fit it. Everyone who signs up
gets the form's tags. `mailgoose form embed` (getFormEmbed) gives
the code to put it on a site.

Request body (`application/json`, required): [CreateFormRequest](#schema-createformrequest)

Responses:

- `201`: The new form. [Form](#schema-form)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `422`: `invalid_form`: a part of the form breaks a rule; `details.field` names it (such as `redirect_url` or `fields[1].name`). `unknown_tag`: a tag id isn't one of the account's tags. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getform"></a>

### Get a signup form

`GET /v1/forms/{form_id}`

- Operation: `getForm`
- CLI: `mailgoose form get`, `mailgoose form update`

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `form_id` | path | string (uuid) | yes | The form's `id`. |

Responses:

- `200`: The form. [Form](#schema-form)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-updateform"></a>

### Change a signup form

`PATCH /v1/forms/{form_id}`

- Operation: `updateForm`
- CLI: `mailgoose form update`

Changes the parts given; the rest stay. `fields` and `tag_ids`
replace the whole list. An empty `redirect_url` removes it, so the
form shows its success message instead. Embed code already on a
site keeps working, but shows the fields it was made with until
it's replaced.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `form_id` | path | string (uuid) | yes | The form's `id`. |

Request body (`application/json`, required): [UpdateFormRequest](#schema-updateformrequest)

Responses:

- `200`: The changed form. [Form](#schema-form)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `422`: `invalid_form`: a part of the form breaks a rule; `details.field` names it (such as `redirect_url` or `fields[1].name`). `unknown_tag`: a tag id isn't one of the account's tags. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-deleteform"></a>

### Delete a signup form

`DELETE /v1/forms/{form_id}`

- Operation: `deleteForm`
- CLI: `mailgoose form delete`

Deletes the form. Contacts who signed up with it stay, and its embed
code stops taking signups.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `form_id` | path | string (uuid) | yes | The form's `id`. |

Responses:

- `204`: Deleted.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `cross_origin_request`: a browser sent this from another site. Only the web app's own origin may change things with a session cookie. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getformembed"></a>

### Get a signup form's embed code

`GET /v1/forms/{form_id}/embed`

- Operation: `getFormEmbed`
- CLI: `mailgoose form embed`

The code to paste into a site. `html` is a plain HTML form that
posts to `action_url` and works without JavaScript; it carries a
hidden honeypot field (`website`) and a signed timestamp (`_mg_ts`)
that submissions are checked against. `script` is optional: pasted
after the form, it posts in the page and shows the success message
there instead of leaving the page. Labels and the button take the
site's own fonts.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `form_id` | path | string (uuid) | yes | The form's `id`. |

Responses:

- `200`: The embed code. [FormEmbed](#schema-formembed)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-admin"></a>

## Operator tools (mailgoose staff only)

<a id="op-findadminaccounts"></a>

### Find accounts (admin)

`GET /v1/admin/accounts`

- Operation: `findAdminAccounts`
- CLI: `mailgoose admin find`
- Needs a `full`-scope key

mailgoose staff only (OPS-6): accounts whose id is `q`, or whose
owner's address, name or sending domain contains it. At most `limit`
of them, newest first; there's no next page. Admin endpoints need a
signed-in admin (a CLI token or web session, not an API key) and
answer 403 `admin_only` to anyone else.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `q` | query | string |  | An account id, or part of an address, name or domain. Required; the endpoint answers 400 `invalid_parameter` without it (it's checked after the caller is, so others get 401 or 403 first). |
| `limit` | query | integer |  | Items per page. |

Responses:

- `200`: The accounts found. [AdminAccountList](#schema-adminaccountlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getadminaccount"></a>

### Look at an account (admin)

`GET /v1/admin/accounts/{account_id}`

- Operation: `getAdminAccount`
- CLI: `mailgoose admin account`
- Needs a `full`-scope key

mailgoose staff only: an account's owner, pause, send limit, credits,
last 30 days' bounces and complaints, held imports and live
campaigns.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-pauseaccount"></a>

### Pause an account's sending (admin)

`POST /v1/admin/accounts/{account_id}/pause`

- Operation: `pauseAccount`
- CLI: `mailgoose admin pause`
- Needs a `full`-scope key

mailgoose staff only, the kill switch for an account (CMP-8): nothing
more is sent from it, from the next copy on. Its campaigns stay as
they are and carry on when it's unpaused. `reason` is logged and
shown to the account as why. Pausing a paused account replaces the
reason.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-unpauseaccount"></a>

### Let an account send again (admin)

`POST /v1/admin/accounts/{account_id}/unpause`

- Operation: `unpauseAccount`
- CLI: `mailgoose admin unpause`
- Needs a `full`-scope key

mailgoose staff only: lifts a pause, an admin's or an automatic one.
The account's sending campaigns carry on where they stopped.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-suspendaccount"></a>

### Suspend an account (admin)

`POST /v1/admin/accounts/{account_id}/suspend`

- Operation: `suspendAccount`
- CLI: `mailgoose admin suspend`
- Needs a `full`-scope key

mailgoose staff only (MG-65): for abuse, when a pause isn't enough.
Revokes every API key and CLI login, ends web sessions, cancels
scheduled campaigns and stops sending ones before their next batch
(their unsent reservations come back); signup forms stop taking
signups. The owner can still sign in to a read-only page that says
why and exports their contacts and campaigns for 30 days. The owner
gets an email saying why and how to appeal; the operators get one
too. Credits stay as they are. The reason is logged and shown to the
account. An account that's suspended already answers 409
`account_suspended`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `account_suspended`: the account is suspended already. `account_not_suspended`: the account isn't suspended. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-unsuspendaccount"></a>

### Lift a suspension (admin)

`POST /v1/admin/accounts/{account_id}/unsuspend`

- Operation: `unsuspendAccount`
- CLI: `mailgoose admin unsuspend`
- Needs a `full`-scope key

mailgoose staff only (MG-65): the account can sign in and send
again. Its API keys and CLI logins stay revoked, so the owner makes
new ones; its stopped campaigns stay stopped, and a pause stays
until it's lifted with `unpauseAccount`. An account that isn't
suspended answers 409 `account_not_suspended`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `account_suspended`: the account is suspended already. `account_not_suspended`: the account isn't suspended. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-listsignupreviews"></a>

### List sign-ups waiting for review (admin)

`GET /v1/admin/signups`

- Operation: `listSignupReviews`
- CLI: `mailgoose admin signups`
- Needs a `full`-scope key

mailgoose staff only (MG-66): accounts waiting for their sign-up
review, oldest first, with what flagged them (`review_flags`). At
most `limit` of them; there's no next page. Which new accounts wait
depends on the server's `SIGNUP_REVIEW` setting: all of them, only
flagged ones, or none.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |

Responses:

- `200`: The accounts waiting. [AdminAccountList](#schema-adminaccountlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-approvesignup"></a>

### Approve a sign-up (admin)

`POST /v1/admin/accounts/{account_id}/approve-signup`

- Operation: `approveSignup`
- CLI: `mailgoose admin approve-signup`
- Needs a `full`-scope key

mailgoose staff only (MG-66): the account stops waiting for review
and can launch campaigns and take form signups. An account that
isn't waiting answers 409 `signup_not_waiting`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `signup_not_waiting`: the account isn't waiting for its sign-up review. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-rejectsignup"></a>

### Reject a sign-up (admin)

`POST /v1/admin/accounts/{account_id}/reject-signup`

- Operation: `rejectSignup`
- CLI: `mailgoose admin reject-signup`
- Needs a `full`-scope key

mailgoose staff only (MG-66): the account is rejected and suspended,
exactly as `suspendAccount` does, with the reason shown to its owner.
An account that isn't waiting answers 409 `signup_not_waiting`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `signup_not_waiting`: the account isn't waiting for its sign-up review. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-listtestmodeaccounts"></a>

### List accounts in test mode (admin)

`GET /v1/admin/test-mode`

- Operation: `listTestModeAccounts`
- CLI: `mailgoose admin test-mode`
- Needs a `full`-scope key

mailgoose staff only (MG-80): accounts in test mode, oldest first,
with how many test addresses each has confirmed and pending. Closed
accounts are left out. At most `limit` of them; there's no next
page. New accounts start in test mode while the server's
`NEW_ACCOUNTS_TEST_MODE` setting is on.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |

Responses:

- `200`: The accounts in test mode. [AdminAccountList](#schema-adminaccountlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-getdeliverabilityreport"></a>

### Per-customer deliverability report (admin)

`GET /v1/admin/deliverability`

- Operation: `getDeliverabilityReport`
- CLI: `mailgoose admin deliverability`
- Needs a `full`-scope key

mailgoose staff only (MG-82): every account that sent campaign email
in the last 7 days, or had a hard bounce or complaint reported in
them, with its copies sent, hard bounces and complaints (and their
rates) over the last 24 hours and 7 days. Counted from `send_events`
and `campaign_recipients` exactly as the daily digest counts the
platform's rates: copies sent in the window, and the distinct
recipients whose hard bounce or complaint was reported in it.

Worst first: the accounts over the early warning over 7 days
(`over_warning`) first, then by how far their 7-day rates are over
the early-warning thresholds, then their 24-hour rates, then the
most copies sent. Each row says whether the account is paused,
suspended, throttled or in test mode. At most `limit` rows; `total`
counts them all, and there's no next page.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `limit` | query | integer |  | Items per page. |

Responses:

- `200`: The report. [AdminDeliverabilityReport](#schema-admindeliverabilityreport)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-allowfullsending"></a>

### Allow full sending (admin)

`POST /v1/admin/accounts/{account_id}/allow-full-sending`

- Operation: `allowFullSending`
- CLI: `mailgoose admin allow-full-sending`
- Needs a `full`-scope key

mailgoose staff only (MG-80): takes the account out of test mode,
so it can send to its whole audience. It doesn't approve a sign-up
review: an account still waiting for one can't launch to anyone but
its test addresses until it's approved too. An account with full
sending already answers 409 `sending_mode_unchanged`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `sending_mode_unchanged`: the account is in that sending mode already. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-settestmode"></a>

### Put an account in test mode (admin)

`POST /v1/admin/accounts/{account_id}/set-test-mode`

- Operation: `setTestMode`
- CLI: `mailgoose admin set-test-mode`
- Needs a `full`-scope key

mailgoose staff only (MG-80): from now on the account sends only to
its confirmed test addresses and its owner's own address. Copies of
a campaign already sending to anyone else are skipped, with their
credits given back. An account in test mode already answers 409
`sending_mode_unchanged`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `sending_mode_unchanged`: the account is in that sending mode already. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-granttester"></a>

### Make an account a tester account (admin)

`POST /v1/admin/accounts/{account_id}/grant-tester`

- Operation: `grantTester`
- CLI: `mailgoose admin tester grant`
- Needs a `full`-scope key

mailgoose staff only (MG-86): makes the account a tester account,
or sets a new end and credits for one already. A tester account
gets full sending and sends real campaigns within its limits (see
`TesterStatus`) until `until`, 30 days from now unless given. Its
credits are counted from when it first became a tester, so a
second grant extends it rather than starting afresh. Only an
account whose sign-up was approved, or never held for review, and
that isn't suspended can be one.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [GrantTesterRequest](#schema-granttesterrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `signup_not_approved`: the account's sign-up review (MG-66) waits or was rejected, so it can't be a tester. `account_suspended`: the account is suspended. `not_tester`: revoking an account that isn't a tester account. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-revoketester"></a>

### End a tester account (admin)

`POST /v1/admin/accounts/{account_id}/revoke-tester`

- Operation: `revokeTester`
- CLI: `mailgoose admin tester revoke`
- Needs a `full`-scope key

mailgoose staff only (MG-86): ends the account's tester state and
puts it in test mode (MG-80), so from now on it sends only to its
confirmed test addresses and its owner's own address; copies of a
campaign already sending to anyone else are skipped, with their
credits given back. `allowFullSending` makes it a customer
afterwards. An account that isn't a tester answers 409 `not_tester`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: The account. [AdminAccount](#schema-adminaccount)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `signup_not_approved`: the account's sign-up review (MG-66) waits or was rejected, so it can't be a tester. `account_suspended`: the account is suspended. `not_tester`: revoking an account that isn't a tester account. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-grantcredits"></a>

### Grant credits (admin)

`POST /v1/admin/accounts/{account_id}/credits`

- Operation: `grantCredits`
- CLI: `mailgoose admin grant-credits`
- Needs a `full`-scope key

mailgoose staff only: gives the account credits, which last 24
months like bought ones. They show in its history as a `grant`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | path | string (uuid) | yes | The account's `id`. |

Request body (`application/json`, required): [GrantCreditsRequest](#schema-grantcreditsrequest)

Responses:

- `200`: Granted. [CreditGrant](#schema-creditgrant)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-stopcampaign"></a>

### Stop a campaign in flight (admin)

`POST /v1/admin/campaigns/{campaign_id}/stop`

- Operation: `stopCampaign`
- CLI: `mailgoose admin stop-campaign`
- Needs a `full`-scope key

mailgoose staff only, the kill switch for a campaign (CMP-8): a
scheduled, sending or paused campaign is cancelled where it stands.
Nobody else gets it, from the next copy on, and the credits for who's
left come back. Stopping a cancelled campaign changes nothing; one
that's a draft or finished answers 409 `campaign_locked`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `campaign_id` | path | string (uuid) | yes | The campaign's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: Stopped, now or before. [Campaign](#schema-campaign)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `campaign_locked`: only drafts can be changed, tested, launched or deleted; `details.status` is the campaign's status. `version_conflict`: the campaign changed since the version given; `details.current_version` is its version now. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-approveimport"></a>

### Approve a held import (admin)

`POST /v1/admin/imports/{import_id}/approve`

- Operation: `approveImport`
- CLI: `mailgoose admin approve-import`
- Needs a `full`-scope key

mailgoose staff only (CON-6): a held import's contacts can be mailed.
An import that isn't held answers 409 `import_not_held`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `import_id` | path | string (uuid) | yes | The import's `id`. |

Request body (`application/json`, required): [AdminReasonRequest](#schema-adminreasonrequest)

Responses:

- `200`: Approved. [ApprovedImport](#schema-approvedimport)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `import_not_held`: the import isn't waiting for approval. [Error](#schema-error)
- `422`: `invalid_admin_request`: `details.field` is `reason` (missing, or over 500 characters), `amount` (not 1 to 1,000,000), or for a tester grant `until` (passed, or over 366 days away) or `credits` (not 1 to 10,000). [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-listadminactions"></a>

### The admin log (admin)

`GET /v1/admin/actions`

- Operation: `listAdminActions`
- CLI: `mailgoose admin log`
- Needs a `full`-scope key

mailgoose staff only: every admin action and automatic pause, newest
first, with who did it and why; for one account with `account_id`.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `account_id` | query | string (uuid) |  | Only this account's actions. |
| `limit` | query | integer |  | Items per page. |
| `cursor` | query | string |  | The `next_cursor` of the previous page. Leave it out for the first page. |

Responses:

- `200`: A page of the log. [AdminActionList](#schema-adminactionlist)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-test-addresses"></a>

## test-addresses

<a id="op-listtestaddresses"></a>

### List test addresses

`GET /v1/test-addresses`

- Operation: `listTestAddresses`
- CLI: `mailgoose test-addresses list`, `mailgoose test-addresses remove`, `mailgoose test-addresses resend`

The account's test addresses (MG-80), oldest first: at most 5,
pending ones included, so there's no next page. While the account
is in test mode (`sending_mode` on the account) it can send only to
its `confirmed` test addresses and its owner's own sign-in address,
which is always allowed and isn't listed.

Responses:

- `200`: The account's test addresses. [TestAddressList](#schema-testaddresslist)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-addtestaddress"></a>

### Add a test address

`POST /v1/test-addresses`

- Operation: `addTestAddress`
- CLI: `mailgoose test-addresses add`
- Needs a `full`-scope key

Adds a test address (MG-80) and emails it a link to confirm it; it
counts once the link is used, within 7 days. An account keeps up to
5, pending ones included; remove one to free its slot. Addresses
are compared ignoring case. The owner's own sign-in address is
always allowed and can't be added.

Adding one emails someone, so it needs a full-scope key or a
sign-in (403 `insufficient_scope` for a draft-scope key). An
account sends at most 10 confirm emails in any 24 hours, adds and
resends alike, and removing an address gives none back (429
`test_address_rate_limited` with `Retry-After`).

Request body (`application/json`, required): [AddTestAddressRequest](#schema-addtestaddressrequest)

Responses:

- `201`: The new test address, pending until its link is used. [TestAddress](#schema-testaddress)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `409`: `test_address_exists`: the account has that address already. `test_address_limit`: the account has 5 test addresses; remove one first. `test_address_confirmed`: the address is confirmed, so there's no link to resend. [Error](#schema-error)
- `422`: `invalid_email`: that isn't an email address. `test_address_is_owner`: the owner's own sign-in address is always allowed, so it needs no slot. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `502`: `test_address_email_failed`: the confirm email couldn't be sent. The address is kept, pending; resend its link in a minute. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-confirmtestaddress"></a>

### Confirm a test address

`POST /v1/test-addresses/confirm`

- Operation: `confirmTestAddress`
- CLI: `mailgoose test-addresses confirm`

Uses the token from a test address's confirm link (MG-80); the web
app's page at the link calls it. No credentials are needed: the
token is the proof. Using it again changes nothing and answers the
same. A token that isn't one is a 400 `invalid_token`; one for no
address (removed, or replaced by a newer link) a 404
`test_address_link_not_found`; one older than 7 days a 410
`test_address_link_expired`.

Request body (`application/json`, required): [ConfirmTestAddressRequest](#schema-confirmtestaddressrequest)

Responses:

- `200`: Confirmed. [ConfirmedTestAddress](#schema-confirmedtestaddress)
- `400`: The request is malformed. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `410`: `test_address_link_expired`: the link is older than 7 days. Resend it from the web app or with `mailgoose test-addresses resend`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-removetestaddress"></a>

### Remove a test address

`DELETE /v1/test-addresses/{test_address_id}`

- Operation: `removeTestAddress`
- CLI: `mailgoose test-addresses remove`

The address frees its slot, its link stops working, and test mode no longer sends to it.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `test_address_id` | path | string (uuid) | yes | The test address's `id`. |

Responses:

- `204`: Removed.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-resendtestaddress"></a>

### Resend a test address's confirm link

`POST /v1/test-addresses/{test_address_id}/resend`

- Operation: `resendTestAddress`
- CLI: `mailgoose test-addresses resend`
- Needs a `full`-scope key

Emails a pending test address a new confirm link, good for 7 days;
the old link stops working. At most once a minute per address (429
`test_address_resend_too_soon` with `Retry-After`), and it counts
toward the account's 10 confirm emails in any 24 hours (429
`test_address_rate_limited`). A confirmed address answers 409
`test_address_confirmed`. Needs a full-scope key or a sign-in.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `test_address_id` | path | string (uuid) | yes | The test address's `id`. |

Responses:

- `200`: The test address, with the new link's times. [TestAddress](#schema-testaddress)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `test_address_exists`: the account has that address already. `test_address_limit`: the account has 5 test addresses; remove one first. `test_address_confirmed`: the address is confirmed, so there's no link to resend. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `502`: `test_address_email_failed`: the confirm email couldn't be sent. The address is kept, pending; resend its link in a minute. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="tag-sender-addresses"></a>

## sender-addresses

<a id="op-listsenderaddresses"></a>

### List sender addresses

`GET /v1/sender-addresses`

- Operation: `listSenderAddresses`
- CLI: `mailgoose sender-addresses list`, `mailgoose sender-addresses remove`, `mailgoose sender-addresses resend`

The account's sender addresses (MG-85), oldest first: at most 10,
pending ones included, so there's no next page. A custom domain is
optional: when the server has shared sending on (`shared_address`
on the account isn't null), an email whose From isn't on a verified
domain of the account still sends if its From is a `confirmed`
sender address or the owner's own sign-in address, which always
counts and isn't listed. It then goes from the account's
`shared_address`, with replies to that address. A verified domain
of your own is still better for deliverability and trust.

Responses:

- `200`: The account's sender addresses. [SenderAddressList](#schema-senderaddresslist)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-addsenderaddress"></a>

### Add a sender address

`POST /v1/sender-addresses`

- Operation: `addSenderAddress`
- CLI: `mailgoose sender-addresses add`
- Needs a `full`-scope key

Adds a sender address (MG-85) and emails it a link to confirm the
account may send as it; it counts once the link is used, within 7
days. An account keeps up to 10, pending ones included; remove one
to free its slot. Addresses are compared ignoring case. The owner's
own sign-in address always counts and can't be added.

Adding one emails someone, so it needs a full-scope key or a
sign-in (403 `insufficient_scope` for a draft-scope key). An
account sends at most 10 sender address confirm emails in any 24
hours, adds and resends alike, and removing an address gives none
back (429 `sender_address_rate_limited` with `Retry-After`).

Request body (`application/json`, required): [AddSenderAddressRequest](#schema-addsenderaddressrequest)

Responses:

- `201`: The new sender address, pending until its link is used. [SenderAddress](#schema-senderaddress)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `409`: `sender_address_exists`: the account has that address already. `sender_address_limit`: the account has 10 sender addresses; remove one first. `sender_address_confirmed`: the address is confirmed, so there's no link to resend. [Error](#schema-error)
- `422`: `invalid_email`: that isn't an email address. `sender_address_is_owner`: the owner's own sign-in address always counts, so it needs no slot. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `502`: `sender_address_email_failed`: the confirm email couldn't be sent. The address is kept, pending; resend its link in a minute. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-confirmsenderaddress"></a>

### Confirm a sender address

`POST /v1/sender-addresses/confirm`

- Operation: `confirmSenderAddress`
- CLI: `mailgoose sender-addresses confirm`

Uses the token from a sender address's confirm link (MG-85); the
web app's page at the link calls it. No credentials are needed: the
token is the proof. Using it again changes nothing and answers the
same. A token that isn't one is a 400 `invalid_token`; one for no
address (removed, or replaced by a newer link) a 404
`sender_address_link_not_found`; one older than 7 days a 410
`sender_address_link_expired`.

Request body (`application/json`, required): [ConfirmSenderAddressRequest](#schema-confirmsenderaddressrequest)

Responses:

- `200`: Confirmed. [ConfirmedSenderAddress](#schema-confirmedsenderaddress)
- `400`: The request is malformed. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `410`: `sender_address_link_expired`: the link is older than 7 days. Resend it from the web app or with `mailgoose sender-addresses resend`. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-removesenderaddress"></a>

### Remove a sender address

`DELETE /v1/sender-addresses/{sender_address_id}`

- Operation: `removeSenderAddress`
- CLI: `mailgoose sender-addresses remove`

The address frees its slot, its link stops working, and the account
can no longer send as it from the shared domain. Campaigns already
sending from it stop going out.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `sender_address_id` | path | string (uuid) | yes | The sender address's `id`. |

Responses:

- `204`: Removed.
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="op-resendsenderaddress"></a>

### Resend a sender address's confirm link

`POST /v1/sender-addresses/{sender_address_id}/resend`

- Operation: `resendSenderAddress`
- CLI: `mailgoose sender-addresses resend`
- Needs a `full`-scope key

Emails a pending sender address a new confirm link, good for 7
days; the old link stops working. At most once a minute per address
(429 `sender_address_resend_too_soon` with `Retry-After`), and it
counts toward the account's 10 sender address confirm emails in any
24 hours (429 `sender_address_rate_limited`). A confirmed address
answers 409 `sender_address_confirmed`. Needs a full-scope key or a
sign-in.

| Parameter | In | Type | Required | Description |
| --- | --- | --- | --- | --- |
| `sender_address_id` | path | string (uuid) | yes | The sender address's `id`. |

Responses:

- `200`: The sender address, with the new link's times. [SenderAddress](#schema-senderaddress)
- `400`: The request is malformed. [Error](#schema-error)
- `401`: `unauthenticated`: no API key or session cookie, or the session has ended. `invalid_api_key`: the key is wrong or revoked. [Error](#schema-error)
- `403`: `insufficient_scope`: a draft-scope key can't do this; use a full-scope key or the web app. `cross_origin_request`: a browser sent this from another site. `admin_only`: only a signed-in mailgoose admin can do this. `account_suspended`: the account is suspended; see the API's description for what it can still do. [Error](#schema-error)
- `404`: `not_found`: there's no such thing in this account. [Error](#schema-error)
- `409`: `sender_address_exists`: the account has that address already. `sender_address_limit`: the account has 10 sender addresses; remove one first. `sender_address_confirmed`: the address is confirmed, so there's no link to resend. [Error](#schema-error)
- `429`: Rate limited (`rate_limited`). [Error](#schema-error)
- `502`: `sender_address_email_failed`: the confirm email couldn't be sent. The address is kept, pending; resend its link in a minute. [Error](#schema-error)
- `503`: `service_unavailable`: a dependency the endpoint needs (the database, or Amazon SES for domains) isn't configured or reachable. Try again later. [Error](#schema-error)

<a id="schemas"></a>

## Schemas

<a id="schema-contactstatus"></a>

### ContactStatus

Only `subscribed` contacts receive campaigns. `unconfirmed` is waiting
for double opt-in or permission; `bounced` and `complained` come from
Amazon SES.

Type: string: one of `unconfirmed`, `subscribed`, `unsubscribed`, `bounced`, `complained`

<a id="schema-contactsource"></a>

### ContactSource

How the contact was added. `manual` is the web app.

Type: string: one of `form`, `import`, `api`, `manual`

<a id="schema-contact"></a>

### Contact

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `email` | string (email) | yes | Stored in lower case. |
| `first_name` | string or null | yes |  |
| `last_name` | string or null | yes |  |
| `status` | [ContactStatus](#schema-contactstatus) | yes |  |
| `source` | [ContactSource](#schema-contactsource) | yes |  |
| `tags` | array of [TagRef](#schema-tagref) | yes | The contact's tags, by name. |
| `created_at` | string (date-time) | yes |  |
| `updated_at` | string (date-time) | yes |  |
| `status_changed_at` | string (date-time) | yes |  |

<a id="schema-contactlist"></a>

### ContactList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [Contact](#schema-contact) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-createcontactrequest"></a>

### CreateContactRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string | yes |  |
| `first_name` | string or null |  |  |
| `last_name` | string or null |  |  |
| `status` | string: one of `subscribed`, `unconfirmed`, `unsubscribed` |  | Use `unconfirmed` for someone who hasn't yet given permission to be mailed. |
| `source` | string: one of `api`, `manual` |  | `manual` is for contacts a person typed in; agents and scripts leave it out. |

<a id="schema-updatecontactrequest"></a>

### UpdateContactRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string |  |  |
| `first_name` | string or null |  |  |
| `last_name` | string or null |  |  |
| `status` | string: one of `subscribed`, `unconfirmed`, `unsubscribed` |  |  |

<a id="schema-contactdata"></a>

### ContactData

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `contact` | [Contact](#schema-contact) | yes |  |
| `tags` | array of object | yes |  |
| `import` | null or object | yes | The import that added the contact; null when none did. |
| `skipped_rows` | array of object | yes | Rows of the account's imports with this address that weren't added. |
| `consents` | array of object | yes | The contact's consent records, oldest first: each signup through a signup form, and for a contact an import added, the list source the import gave and when. |

<a id="schema-tagref"></a>

### TagRef

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |

<a id="schema-tag"></a>

### Tag

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `contact_count` | integer | yes | How many contacts have the tag, whatever their status. |
| `created_at` | string (date-time) | yes |  |

<a id="schema-taglist"></a>

### TagList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [Tag](#schema-tag) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-createtagrequest"></a>

### CreateTagRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | Unique in the account, ignoring case; one line, no commas. |

<a id="schema-renametagrequest"></a>

### RenameTagRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes |  |

<a id="schema-tagdeleted"></a>

### TagDeleted

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `contacts_untagged` | integer | yes | How many contacts had the tag. |
| `campaigns_updated` | integer | yes | How many draft or scheduled campaigns had the tag among their recipients. |

<a id="schema-contactfilter"></a>

### ContactFilter

Matches contacts as `listContacts` does; fields left out don't filter.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string |  |  |
| `name` | string |  |  |
| `status` | [ContactStatus](#schema-contactstatus) |  |  |
| `tag_id` | string (uuid) |  |  |

<a id="schema-contactselection"></a>

### ContactSelection

Exactly one of `contact_ids` and `filter`.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `contact_ids` | array of string (uuid) |  |  |
| `filter` | [ContactFilter](#schema-contactfilter) |  |  |

<a id="schema-tagchange"></a>

### TagChange

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `matched` | integer | yes | How many of the account's contacts were selected. |
| `changed` | integer | yes | How many gained or lost the tag; the others already had it or didn't. |

<a id="schema-importcolumns"></a>

### ImportColumns

The header of the column each field comes from. Leave a field out to have it found from common headers.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string |  |  |
| `first_name` | string |  |  |
| `last_name` | string |  |  |
| `status` | string |  | A status column can only hold contacts back: `unsubscribed`, `bounced` and `complained` rows are added with that status, and `unconfirmed` rows stay unconfirmed. Other values are ignored. Found under the header `status`. |
| `tags` | string |  | A column of tag names separated by commas, put on each row's contact. Tags the account doesn't have are created; a file may name at most 200 different tags. Found under the header `tags`. |

<a id="schema-createimportrequest"></a>

### CreateImportRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `csv` | string | yes | The whole CSV file as UTF-8 text, header row first. |
| `file_name` | string or null |  | The file's name, to tell imports apart later. |
| `columns` | [ImportColumns](#schema-importcolumns) |  |  |
| `tags` | array of string |  | Tags, by name or id, put on every contact the import adds. They must exist. |
| `permission_confirmed` | boolean |  | True says the account has these people's permission to mail them, so they're added `subscribed`. Otherwise they're `unconfirmed` and don't receive campaigns. |
| `list_source` | [ListSource](#schema-listsource) | yes |  |
| `list_source_detail` | string or null |  | Where the people came from, in a few words. Required when `list_source` is `other`. |

<a id="schema-listsource"></a>

### ListSource

Where an import's people came from. `form`: they signed up on my
site or form. `customers`: they bought from me. `personal`: people I
know personally. `export`: exported from another email tool.
`other`: described in `list_source_detail`.

Type: string: one of `form`, `customers`, `personal`, `export`, `other`

<a id="schema-importchecks"></a>

### ImportChecks

What the checks found in the file when the import was queued.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `invalid_rows` | integer | yes | Rows that aren't an email address, or couldn't be read. |
| `role_addresses` | integer | yes | Valid rows with a role address, such as info@, sales@ or admin@. |
| `disposable_addresses` | integer | yes | Valid rows with an address at a disposable (throwaway) mail domain. |

<a id="schema-importstatus"></a>

### ImportStatus

Type: string: one of `queued`, `running`, `done`, `failed`

<a id="schema-skipreason"></a>

### SkipReason

`invalid_email`: not an email address. `invalid_row`: the row
couldn't be read as CSV (a stray quote, say). `duplicate`: the same
address is on an earlier row. `exists`: the account already has the
contact, which is left as it is. `suppressed`: the address
unsubscribed, bounced or complained before. `over_limit`: a free
account, or a tester account (MG-86), was full.

Type: string: one of `invalid_email`, `invalid_row`, `duplicate`, `exists`, `suppressed`, `over_limit`

<a id="schema-import"></a>

### Import

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `file_name` | string or null | yes |  |
| `status` | [ImportStatus](#schema-importstatus) | yes |  |
| `contact_status` | string: one of `subscribed`, `unconfirmed` | yes | The status added contacts get; `subscribed` only when permission was confirmed. |
| `columns` | object | yes | The header of the column each field came from; null when the file has none. |
| `tags` | array of [TagRef](#schema-tagref) | yes | Tags put on the added contacts. A tag deleted since is left out. |
| `total_rows` | integer | yes | Contact rows in the file, not counting the header and blank rows. |
| `processed_rows` | integer | yes |  |
| `imported` | integer | yes | Contacts added. |
| `skipped` | integer | yes |  |
| `skipped_by_reason` | object | yes |  |
| `review` | string: one of `none`, `held`, `approved` | yes | A `held` import's contacts aren't mailed until mailgoose approves it. |
| `hold_reason` | string or null | yes | Why a held import is held: `shared_list` (many of its addresses are on other accounts' lists), `risky_addresses` (many role or disposable addresses), `invalid_addresses` (many rows that aren't addresses), `large_first_import` or `over_contact_limit`. |
| `hold_message` | string or null | yes | Why the import is held, in plain words, for showing to the account; null when it isn't held. |
| `list_source` | null or [ListSource](#schema-listsource) | yes | Where the account said the people came from; null for imports made before it was asked. |
| `list_source_detail` | string or null | yes |  |
| `checks` | [ImportChecks](#schema-importchecks) | yes |  |
| `error` | string or null | yes | Why a `failed` import stopped. |
| `created_at` | string (date-time) | yes |  |
| `started_at` | string (date-time) or null | yes |  |
| `finished_at` | string (date-time) or null | yes |  |

<a id="schema-importlist"></a>

### ImportList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [Import](#schema-import) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-skippedrow"></a>

### SkippedRow

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `row` | integer | yes | The row's line in the file, where the header is 1. |
| `email` | string | yes | The email cell as written; empty when the row couldn't be read. |
| `reason` | [SkipReason](#schema-skipreason) | yes |  |

<a id="schema-skippedrowlist"></a>

### SkippedRowList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [SkippedRow](#schema-skippedrow) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-createdomainrequest"></a>

### CreateDomainRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | The domain to send from, such as `example.com` or `news.example.com`. Internationalized names are stored in their ASCII (punycode) form. |

<a id="schema-domainstatus"></a>

### DomainStatus

`pending` until every required record is found and Amazon SES has
verified the domain; `verified` after that; `failed` when 72 hours of
checks passed without it.

Type: string: one of `pending`, `verified`, `failed`

<a id="schema-dnsrecord"></a>

### DnsRecord

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `purpose` | string: one of `dkim`, `mail_from_mx`, `mail_from_spf`, `dmarc` | yes |  |
| `type` | string: one of `CNAME`, `MX`, `TXT` | yes |  |
| `name` | string | yes | The full record name, such as `abc._domainkey.example.com`. |
| `value` | string | yes |  |
| `priority` | integer or null | yes | The MX priority; null for other types. |
| `required` | boolean | yes | Whether the domain needs this record to verify. The DMARC record is only suggested. |
| `status` | string: one of `found`, `missing`, `wrong`, `unchecked` | yes | What the last check found; `unchecked` before the first check. |
| `found` | array of string | yes | The values the lookup returned, when there were any. |
| `detail` | string or null | yes | What is wrong, or why the lookup failed. |

<a id="schema-domain"></a>

### Domain

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `status` | [DomainStatus](#schema-domainstatus) | yes |  |
| `mail_from_domain` | string | yes | The MAIL FROM subdomain, where bounces go. |
| `records` | array of [DnsRecord](#schema-dnsrecord) | yes |  |
| `ses_status` | string or null | yes | Amazon SES's DKIM verification status at the last check: `pending`, `success`, `failed`, `temporary_failure`, `not_started`, or `not_found` and `unknown` when SES couldn't say. Null before the first check. |
| `created_at` | string (date-time) | yes |  |
| `last_checked_at` | string (date-time) or null | yes |  |
| `check_until` | string (date-time) | yes | When background checks of a pending domain stop. |
| `verified_at` | string (date-time) or null | yes |  |
| `failed_at` | string (date-time) or null | yes |  |
| `records_changed` | boolean |  | Only in `verifyDomain` responses. True when the domain was set up again with new DKIM records, which replace the old ones in DNS. |

<a id="schema-domainlist"></a>

### DomainList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [Domain](#schema-domain) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-createapikeyrequest"></a>

### CreateApiKeyRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes |  |
| `scope` | [ApiKeyScope](#schema-apikeyscope) | yes |  |

<a id="schema-accountsettings"></a>

### AccountSettings

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `plan` | string: one of `free`, `paid` | yes |  |
| `from_name` | string or null | yes | The default From name for new campaigns. |
| `from_email` | string or null | yes | The default From address for new campaigns. |
| `postal_address` | string or null | yes | The mailing address in every email's footer, one line per `\n`. |
| `time_zone` | string | yes | An IANA time zone name. |
| `missing_for_send` | array of string | yes | Settings a campaign can't launch without, such as `postal_address`. Empty when none are missing. |
| `send_limit` | [SendLimit](#schema-sendlimit) | yes |  |
| `terms` | [TermsStatus](#schema-termsstatus) | yes |  |
| `sending_mode` | [SendingMode](#schema-sendingmode) | yes |  |
| `tester` | [TesterStatus](#schema-testerstatus) or null | yes | The account's tester state when it's a tester account (MG-86); null otherwise. |
| `sending_slug` | string | yes | The account's name on mailgoose's shared sending domain (MG-85), made from the account's name when it was created. It doesn't change when the account is renamed. |
| `shared_address` | string or null | yes | Where the account's email goes from when its From isn't on a verified domain of its own (MG-85): `sending_slug` at mailgoose's shared sending domain, with replies to the From address, which has to be the owner's own sign-in address or a confirmed sender address (`listSenderAddresses`). Null when the server has shared sending off, and every From needs a verified domain. A custom domain is optional, but better for deliverability and trust. |
| `created_at` | string (date-time) | yes |  |

<a id="schema-testerstatus"></a>

### TesterStatus

A tester account (MG-86) is one of the people invited to try
mailgoose before launch. It sends real campaigns within tight
limits: `credits` sends for its life (campaign copies and test sends
alike; SES mailbox simulator addresses, `*@simulator.amazonses.com`,
use none), 100 campaign copies a day, 500 contacts and a stricter
automatic pause. A launch or test send that needs more than `left`,
or comes after `until`, is refused with the code `tester_limit` and
nothing is sent.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `started_at` | string (date-time) | yes | When it became a tester; credits are counted from then. |
| `until` | string (date-time) | yes | When it stops sending. |
| `credits` | integer | yes |  |
| `used` | integer | yes | Campaign copies queued, sending or sent, and test sends, since `started_at`, leaving out simulator addresses. |
| `left` | integer | yes |  |
| `ended` | boolean | yes | Whether `until` has passed. |

<a id="schema-sendingmode"></a>

### SendingMode

`test` while the account is in test mode (MG-80): it sends only to
its confirmed test addresses (`listTestAddresses`) and its owner's
own sign-in address. Launches and test sends to anyone else are
refused with the code `test_mode`, and its forms take signups only
from those addresses. mailgoose staff allow `full` sending.

Type: string: one of `test`, `full`

<a id="schema-testaddress"></a>

### TestAddress

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `email` | string | yes |  |
| `status` | string: one of `pending`, `confirmed` | yes | `pending` until the emailed link is used; only `confirmed` addresses can be sent to in test mode. |
| `confirm_sent_at` | string (date-time) or null | yes | When the latest confirm link was emailed; null once confirmed, or when the email couldn't be sent. |
| `confirm_expires_at` | string (date-time) or null | yes | When the latest confirm link stops working; null once confirmed. |
| `confirmed_at` | string (date-time) or null | yes |  |
| `created_at` | string (date-time) | yes |  |

<a id="schema-testaddresslist"></a>

### TestAddressList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [TestAddress](#schema-testaddress) | yes |  |
| `next_cursor` | null | yes |  |

<a id="schema-addtestaddressrequest"></a>

### AddTestAddressRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string | yes |  |

<a id="schema-confirmtestaddressrequest"></a>

### ConfirmTestAddressRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `token` | string | yes | The `token` in the confirm link's query. |

<a id="schema-confirmedtestaddress"></a>

### ConfirmedTestAddress

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string | yes |  |
| `account_name` | string | yes | The account the address now gets test mode's emails from. |
| `confirmed_at` | string (date-time) | yes |  |

<a id="schema-senderaddress"></a>

### SenderAddress

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `email` | string | yes |  |
| `status` | string: one of `pending`, `confirmed` | yes | `pending` until the emailed link is used; only a `confirmed` address can send from the shared domain. |
| `confirm_sent_at` | string (date-time) or null | yes | When the latest confirm link was emailed; null once confirmed, or when the email couldn't be sent. |
| `confirm_expires_at` | string (date-time) or null | yes | When the latest confirm link stops working; null once confirmed. |
| `confirmed_at` | string (date-time) or null | yes |  |
| `created_at` | string (date-time) | yes |  |

<a id="schema-senderaddresslist"></a>

### SenderAddressList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [SenderAddress](#schema-senderaddress) | yes |  |
| `next_cursor` | null | yes |  |

<a id="schema-addsenderaddressrequest"></a>

### AddSenderAddressRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string | yes |  |

<a id="schema-confirmsenderaddressrequest"></a>

### ConfirmSenderAddressRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `token` | string | yes | The `token` in the confirm link's query. |

<a id="schema-confirmedsenderaddress"></a>

### ConfirmedSenderAddress

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string | yes |  |
| `account_name` | string | yes | The account that may now send with this address as its sender. |
| `confirmed_at` | string (date-time) | yes |  |

<a id="schema-sendlimit"></a>

### SendLimit

How many campaign recipients the account may send to in 24 hours.
New accounts start low and double each week of clean sending up to
the usual limit. A campaign whose spam complaints or hard bounces
pass the early-warning limits (0.05% and 2% by default) lowers it to
the new-account limit for 7 days.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `daily` | integer | yes |  |
| `usual` | integer | yes | The limit for an established account in good standing. |
| `reason` | string or null | yes | Why `daily` is under `usual` (or set by mailgoose); null at the usual limit. |
| `lowered_until` | string (date-time) or null | yes | When a lowered limit lifts; null when it isn't lowered. |

<a id="schema-termsstatus"></a>

### TermsStatus

Which version of mailgoose's terms the account accepted, and the
version in force now. Versions are dates, `YYYY-MM-DD`; what changed
in each is on the web app's `/policies/changes` page.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `accepted_version` | string or null | yes | The latest version the account accepted; null if it never accepted one. |
| `accepted_at` | string (date-time) or null | yes | When it accepted that version. |
| `current_version` | string | yes | The version in force now. |
| `current_effective` | string (date) | yes | The day the current version took effect. |
| `acceptance_needed` | boolean | yes | True when the account hasn't accepted the current version. Campaigns can't launch until it does; accept with `acceptTerms`. |

<a id="schema-accepttermsrequest"></a>

### AcceptTermsRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | string | yes | The version being accepted. It must be the current one. |

<a id="schema-updateaccountrequest"></a>

### UpdateAccountRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string |  |  |
| `from_name` | string or null |  | One line, without quotes or angle brackets. `null` removes it. |
| `from_email` | string (email) or null |  | `null` removes it. |
| `postal_address` | string or null |  | Up to 8 lines separated by `\n`; blank lines are dropped. `null` removes it. |
| `time_zone` | string |  | An IANA time zone name, such as `America/New_York` or `UTC`. |

<a id="schema-apikeyscope"></a>

### ApiKeyScope

`full` can do everything. `draft` can do everything except launch
campaigns, change billing and manage keys.

Type: string: one of `full`, `draft`

<a id="schema-apikey"></a>

### ApiKey

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `scope` | [ApiKeyScope](#schema-apikeyscope) | yes |  |
| `hint` | string | yes | The secret's prefix and last four characters, to tell keys apart. |
| `created_at` | string (date-time) | yes |  |
| `last_used_at` | string (date-time) or null | yes | When the key last made a request, to the minute; null if never. |

<a id="schema-createdapikey"></a>

### CreatedApiKey

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `key` | [ApiKey](#schema-apikey) | yes |  |
| `secret` | string | yes | The key itself. It's shown only in this response. |

<a id="schema-apikeylist"></a>

### ApiKeyList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [ApiKey](#schema-apikey) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-plancheckoutrequest"></a>

### PlanCheckoutRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `interval` | string: one of `month`, `year` | yes | Bill each month ($5 a unit) or each year ($50 a unit). |

<a id="schema-plancheckout"></a>

### PlanCheckout

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `url` | string (uri) | yes | Stripe's Checkout page; open it in a browser to pay. |
| `interval` | string: one of `month`, `year` | yes |  |
| `units` | integer | yes | Plan units the subscription starts with, from the account's contacts now. |
| `unit_price_cents` | integer | yes | One unit's price each interval, before any sales tax, in cents. |
| `currency` | string: one of `usd` | yes |  |
| `expires_at` | string (date-time) | yes | When the page stops working. |

<a id="schema-planportal"></a>

### PlanPortal

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `url` | string (uri) | yes | Stripe's customer portal page; open it in a browser. |

<a id="schema-plan"></a>

### Plan

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `plan` | string: one of `free`, `paid` | yes |  |
| `status` | string or null | yes | Stripe's subscription status: `active`, `past_due` or `unpaid` (a renewal failed; sending is paused while Stripe retries the card), `canceled`, and so on. Null for an account that never subscribed. |
| `interval` | string or null | yes | `month` or `year`; null without a subscription. |
| `units` | integer | yes | Plan units the subscription is billed for; 0 without one. |
| `current_period_end` | string (date-time) or null | yes | When the paid period ends and the subscription renews, or ends if it's cancelled. |
| `cancel_at_period_end` | boolean | yes | True when the plan was cancelled and ends at `current_period_end`. |
| `contacts` | integer | yes | Counted contacts now (subscribed and unconfirmed). |
| `units_needed` | integer | yes | The plan units those contacts make. |
| `prices` | object | yes |  |

<a id="schema-creditcheckoutrequest"></a>

### CreditCheckoutRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `pack` | string | yes | The pack's name, such as `10k`; case doesn't matter. |

<a id="schema-creditcheckout"></a>

### CreditCheckout

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `url` | string (uri) | yes | Stripe's Checkout page; open it in a browser to pay. |
| `pack` | string | yes |  |
| `credits` | integer | yes |  |
| `price_cents` | integer | yes | The pack's price before any sales tax, in cents. |
| `currency` | string: one of `usd` | yes |  |
| `expires_at` | string (date-time) | yes | When the page stops working. |

<a id="schema-creditsummary"></a>

### CreditSummary

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `balance` | integer | yes | Credits available now, not counting any reserved for a send in progress. |
| `plan` | string: one of `free`, `paid` | yes |  |
| `next_grant_at` | string (date-time) or null | yes | When the next 1,000 free credits arrive; null on the paid plan. |

<a id="schema-credittransactiontype"></a>

### CreditTransactionType

`grant` is a free monthly grant and `purchase` a bought credit pack.
`reserve` holds credits for a send and `release` gives back what it
didn't use. `spend` is credits used directly and `expiry` credits
that ran out. `fee` isn't used: the paid plan is billed to a card.

Type: string: one of `grant`, `purchase`, `reserve`, `release`, `spend`, `fee`, `expiry`

<a id="schema-credittransaction"></a>

### CreditTransaction

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string | yes |  |
| `type` | [CreditTransactionType](#schema-credittransactiontype) | yes |  |
| `amount` | integer | yes | Credits added (positive) or taken (negative). |
| `expires_at` | string (date-time) or null | yes | When a grant's or purchase's credits expire; null for other types. |
| `ref` | string | yes | What the transaction is for, such as a campaign id; empty when there's nothing to point to. |
| `created_at` | string (date-time) | yes |  |

<a id="schema-credittransactionlist"></a>

### CreditTransactionList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [CreditTransaction](#schema-credittransaction) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-createdeviceloginrequest"></a>

### CreateDeviceLoginRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `client_name` | string |  | Where the login comes from, such as the computer's name; shown on the approval page and used as the token's name. |

<a id="schema-deviceloginstart"></a>

### DeviceLoginStart

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `device_code` | string | yes | The CLI's secret for polling. Don't show it. |
| `user_code` | string | yes | The code the person confirms in the browser. |
| `verification_url` | string (uri) | yes | Where the person approves the code. |
| `verification_url_complete` | string (uri) | yes | `verification_url` with the code filled in. |
| `expires_in` | integer | yes | Seconds until the request expires. |
| `interval` | integer | yes | Seconds to wait between polls. |

<a id="schema-polldeviceloginrequest"></a>

### PollDeviceLoginRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `device_code` | string | yes |  |

<a id="schema-deviceloginpending"></a>

### DeviceLoginPending

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `status` | string: one of `pending` | yes |  |
| `interval` | integer | yes | Seconds to wait before polling again. |

<a id="schema-devicelogintoken"></a>

### DeviceLoginToken

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `token` | string | yes | A full-scope CLI token (`mg_cli_…`), shown only in this response. |
| `key_id` | string (uuid) | yes |  |
| `account` | [Account](#schema-account) | yes |  |
| `user` | [User](#schema-user) | yes |  |

<a id="schema-devicelogin"></a>

### DeviceLogin

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `user_code` | string | yes |  |
| `client_name` | string | yes | Where the login comes from, as the CLI described it; may be empty. |
| `created_at` | string (date-time) | yes |  |
| `expires_at` | string (date-time) | yes |  |

<a id="schema-whoami"></a>

### Whoami

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `account` | [Account](#schema-account) | yes |  |
| `user` | [User](#schema-user) or null | yes | The person behind the request; null for an API key whose maker was removed. |
| `auth_method` | string: one of `session`, `api_key`, `cli_token` | yes |  |
| `scope` | [ApiKeyScope](#schema-apikeyscope) | yes |  |
| `key_id` | string (uuid) or null | yes | The API key or CLI token used; null for a web session. |

<a id="schema-signuprequest"></a>

### SignupRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string (email) | yes |  |
| `account_name` | string | yes |  |
| `accepted_terms` | boolean | yes | Must be `true`: the person accepts the current version of the terms of service and the anti-spam policy, which is recorded with the time. Anything else is a 422 `terms_not_accepted`. |

<a id="schema-signinrequest"></a>

### SignInRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string (email) | yes |  |

<a id="schema-codesent"></a>

### CodeSent

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `status` | string: one of `code_sent` | yes |  |
| `message` | string | yes |  |

<a id="schema-sessionrequest"></a>

### SessionRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string (email) | yes |  |
| `code` | string | yes | The six-digit code from the email. |

<a id="schema-clitokenrequest"></a>

### CliTokenRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | string (email) | yes |  |
| `code` | string | yes | The six-digit code from the email. |
| `client_name` | string |  | Names the token, such as the computer's name. Defaults to "CLI sign-up". |

<a id="schema-session"></a>

### Session

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `account` | [Account](#schema-account) | yes |  |
| `user` | [User](#schema-user) | yes |  |
| `expires_at` | string (date-time) | yes |  |

<a id="schema-account"></a>

### Account

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `status` | string: one of `pending`, `active` | yes |  |
| `plan` | string: one of `free`, `paid` | yes |  |
| `suspension` | [AccountSuspension](#schema-accountsuspension) or null | yes | Set while the account is suspended; null otherwise. |
| `in_review` | boolean | yes | True while a new account waits for its sign-up review (MG-66). Until it's approved it can build emails, import contacts and send tests, but can't launch campaigns or take form signups. |
| `sending_mode` | [SendingMode](#schema-sendingmode) | yes |  |
| `created_at` | string (date-time) | yes |  |

<a id="schema-accountsuspension"></a>

### AccountSuspension

Why and since when the account is suspended. Nothing is sent from it
and its forms take no signups. Its owner appeals by replying to the
email about it.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `suspended_at` | string (date-time) | yes |  |
| `reason` | string | yes |  |
| `export_until` | string (date-time) | yes | Until when the owner can still sign in to export contacts and campaigns. |

<a id="schema-user"></a>

### User

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `email` | string (email) | yes |  |
| `created_at` | string (date-time) | yes |  |

<a id="schema-health"></a>

### Health

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `status` | string: one of `ok` | yes |  |
| `version` | string | yes |  |
| `commit` | string |  |  |
| `build_date` | string |  | When the server binary was built (RFC 3339), if known. |

<a id="schema-emailkind"></a>

### EmailKind

`blocks`: built from blocks in the designer or with block commands.
`html`: hand-written HTML, edited as code only.

Type: string: one of `blocks`, `html`

<a id="schema-color"></a>

### Color

Type: string

<a id="schema-align"></a>

### Align

Type: string: one of `left`, `center`, `right`

<a id="schema-blockid"></a>

### BlockId

Stable for the block's life: edits and moves keep it. Leave it out
when adding a block to get a new one (`b_` and ten letters and
digits). Unique within the document.

Type: string

<a id="schema-emailsettings"></a>

### EmailSettings

The parts of an email outside its content. In a request, fields left
out take their defaults.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `subject` | string | yes |  |
| `preview_text` | string | yes | The line inboxes show after the subject. |
| `from_name` | string | yes |  |
| `from_email` | string (email) or null | yes | Must be at one of the account's verified domains to send (the email check says so). |
| `reply_to` | string (email) or null | yes |  |
| `background_color` | [Color](#schema-color) | yes |  |
| `content_width` | integer | yes | Pixels, on desktop. Blocks emails only; html emails ignore it. |

<a id="schema-emailsettingsupdate"></a>

### EmailSettingsUpdate

Some or all settings. Fields left out keep their values.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `subject` | string |  |  |
| `preview_text` | string |  |  |
| `from_name` | string |  |  |
| `from_email` | string (email) or null |  |  |
| `reply_to` | string (email) or null |  |  |
| `background_color` | [Color](#schema-color) |  |  |
| `content_width` | integer |  |  |

<a id="schema-headingblock"></a>

### HeadingBlock

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | [BlockId](#schema-blockid) | yes |  |
| `type` | string | yes |  |
| `text` | string | yes | One line; merge tags allowed. |
| `level` | integer: one of `1`, `2`, `3` | yes |  |
| `align` | [Align](#schema-align) | yes |  |

<a id="schema-textblock"></a>

### TextBlock

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | [BlockId](#schema-blockid) | yes |  |
| `type` | string | yes |  |
| `text` | string | yes | A small Markdown subset: `**bold**`, `*italic*`, `[link text](https://...)`, lists with `- ` or `1. `, and a blank line between paragraphs. Merge tags allowed. |
| `align` | [Align](#schema-align) | yes |  |

<a id="schema-imageblock"></a>

### ImageBlock

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | [BlockId](#schema-blockid) | yes |  |
| `type` | string | yes |  |
| `src` | string (uri) | yes | An http or https address, usually one `mailgoose image upload` gave. |
| `alt` | string | yes |  |
| `href` | string or null | yes | Where a click on the image goes, if anywhere. The same rules as a button's link. |
| `width` | integer or null | yes | Pixels; null for the full content width. |
| `align` | [Align](#schema-align) | yes |  |

<a id="schema-buttonblock"></a>

### ButtonBlock

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | [BlockId](#schema-blockid) | yes |  |
| `type` | string | yes |  |
| `text` | string | yes |  |
| `href` | string | yes | An http, https or mailto address, or exactly `{{unsubscribe_url}}`. Other merge tags may come only after a fixed `https://host/` or in a mailto address, so a contact's field can't decide where the link goes. |
| `align` | [Align](#schema-align) | yes |  |
| `background_color` | [Color](#schema-color) | yes |  |
| `text_color` | [Color](#schema-color) | yes |  |

<a id="schema-dividerblock"></a>

### DividerBlock

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | [BlockId](#schema-blockid) | yes |  |
| `type` | string | yes |  |
| `color` | [Color](#schema-color) | yes |  |
| `thickness` | integer | yes |  |

<a id="schema-spacerblock"></a>

### SpacerBlock

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | [BlockId](#schema-blockid) | yes |  |
| `type` | string | yes |  |
| `height` | integer | yes |  |

<a id="schema-socialblock"></a>

### SocialBlock

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | [BlockId](#schema-blockid) | yes |  |
| `type` | string | yes |  |
| `links` | array of object | yes |  |
| `align` | [Align](#schema-align) | yes |  |

<a id="schema-footerblock"></a>

### FooterBlock

The locked last block of every blocks email. Rendering adds the
account's postal address and the unsubscribe link; `text` is an
optional line above them.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | [BlockId](#schema-blockid) | yes |  |
| `type` | string | yes |  |
| `text` | string | yes |  |

<a id="schema-columnsblock"></a>

### ColumnsBlock

A row of two columns, each holding blocks other than columns and the footer.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | [BlockId](#schema-blockid) | yes |  |
| `type` | string | yes |  |
| `columns` | array of object | yes |  |

<a id="schema-columncontentblock"></a>

### ColumnContentBlock

A block that can go inside a column.

Type: [HeadingBlock](#schema-headingblock) or [TextBlock](#schema-textblock) or [ImageBlock](#schema-imageblock) or [ButtonBlock](#schema-buttonblock) or [DividerBlock](#schema-dividerblock) or [SpacerBlock](#schema-spacerblock) or [SocialBlock](#schema-socialblock)

<a id="schema-block"></a>

### Block

One block of a document, by `type` (DES-2).

Type: [HeadingBlock](#schema-headingblock) or [TextBlock](#schema-textblock) or [ImageBlock](#schema-imageblock) or [ButtonBlock](#schema-buttonblock) or [DividerBlock](#schema-dividerblock) or [SpacerBlock](#schema-spacerblock) or [ColumnsBlock](#schema-columnsblock) or [SocialBlock](#schema-socialblock) or [FooterBlock](#schema-footerblock)

<a id="schema-blockdocument"></a>

### BlockDocument

A blocks email's settings and content, schema version 1. Blocks come
in order, at most 200 counting those inside columns, and the last is
the footer. Unknown block types and fields are refused. In a
request, `schema_version` may be left out, and so may block ids and
fields that have defaults.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `schema_version` | integer | yes |  |
| `settings` | [EmailSettings](#schema-emailsettings) | yes |  |
| `blocks` | array of [Block](#schema-block) | yes |  |

<a id="schema-htmldocument"></a>

### HtmlDocument

An html email's settings and its markup. importEmailHtml lists the checks the markup must pass.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `schema_version` | integer | yes |  |
| `settings` | [EmailSettings](#schema-emailsettings) | yes |  |
| `html` | string | yes |  |

<a id="schema-emaildocument"></a>

### EmailDocument

A BlockDocument for a blocks email, an HtmlDocument for an html one.

Type: [BlockDocument](#schema-blockdocument) or [HtmlDocument](#schema-htmldocument)

<a id="schema-email"></a>

### Email

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes | For the account's own use; recipients never see it. |
| `kind` | [EmailKind](#schema-emailkind) | yes |  |
| `version` | integer | yes | Goes up by one with every save. Updates name the version they read. |
| `document` | [EmailDocument](#schema-emaildocument) | yes |  |
| `created_at` | string (date-time) | yes |  |
| `updated_at` | string (date-time) | yes |  |

<a id="schema-emailsummary"></a>

### EmailSummary

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `kind` | [EmailKind](#schema-emailkind) | yes |  |
| `version` | integer | yes |  |
| `subject` | string | yes |  |
| `block_count` | integer | yes | Blocks at the top level, footer included; 0 for an html email. |
| `created_at` | string (date-time) | yes |  |
| `updated_at` | string (date-time) | yes |  |

<a id="schema-formfield"></a>

### FormField

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string: one of `email`, `first_name`, `last_name` | yes |  |
| `label` | string |  | What the field is called on the form. Empty takes the default ("Email", "First name", "Last name"). |
| `required` | boolean |  | Email is always required. |

<a id="schema-form"></a>

### Form

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `fields` | array of [FormField](#schema-formfield) | yes | The fields the form asks for, in order. |
| `tags` | array of [TagRef](#schema-tagref) | yes | Tags put on everyone who signs up with the form. |
| `button_text` | string | yes |  |
| `success_message` | string | yes | Shown after someone signs up, unless there's a redirect_url. |
| `redirect_url` | string or null | yes | Where people go after signing up, instead of seeing the success message. |
| `double_opt_in` | boolean | yes | When on, people get an email to confirm before they're subscribed. |
| `confirm_subject` | string | yes | The confirmation email's subject. |
| `confirm_text` | string | yes | The confirmation email's text, above its confirm link. |
| `signups` | integer | yes | How many submissions the form has taken, bots' left out. A person who signs up twice counts twice. |
| `confirmations` | integer | yes | How many confirm links from the form's confirmation emails have been used. |
| `problems` | array of [FormProblem](#schema-formproblem) |  | Only on getting one form: what stops it taking signups, empty when it can. `no_verified_from`: the form uses double opt-in and the account has no From address on a verified sending domain to send the confirmation email from. |
| `created_at` | string (date-time) | yes |  |
| `updated_at` | string (date-time) | yes |  |

<a id="schema-formproblem"></a>

### FormProblem

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `code` | string | yes |  |
| `message` | string | yes |  |
| `hint` | string | yes |  |

<a id="schema-image"></a>

### Image

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `url` | string (uri) | yes | Where the image is served from; use it as an image block's `src`. |
| `file_name` | string | yes | The uploaded file's name, for showing; empty if none was given. |
| `content_type` | string: one of `image/jpeg`, `image/png`, `image/gif`, `image/webp` | yes |  |
| `bytes` | integer | yes |  |
| `created_at` | string (date-time) | yes |  |

<a id="schema-imagelist"></a>

### ImageList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [Image](#schema-image) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-uploadimagerequest"></a>

### UploadImageRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | string (byte) | yes | The file, in standard base64 with padding. Anything else is a 400. |
| `file_name` | string |  | The file's name, for showing in lists. It doesn't decide the type. |

<a id="schema-formlist"></a>

### FormList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [Form](#schema-form) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-createformrequest"></a>

### CreateFormRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes |  |
| `fields` | array of [FormField](#schema-formfield) |  |  |
| `tag_ids` | array of string (uuid) |  |  |
| `button_text` | string |  |  |
| `success_message` | string |  |  |
| `redirect_url` | string |  | A full https:// address, or empty for none. |
| `double_opt_in` | boolean |  |  |
| `confirm_subject` | string |  |  |
| `confirm_text` | string |  |  |

<a id="schema-updateformrequest"></a>

### UpdateFormRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string |  |  |
| `fields` | array of [FormField](#schema-formfield) |  |  |
| `tag_ids` | array of string (uuid) |  |  |
| `button_text` | string |  |  |
| `success_message` | string |  |  |
| `redirect_url` | string |  |  |
| `double_opt_in` | boolean |  |  |
| `confirm_subject` | string |  |  |
| `confirm_text` | string |  |  |

<a id="schema-formembed"></a>

### FormEmbed

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `action_url` | string | yes | Where the form posts. |
| `html` | string | yes | The form, with its styles. Works without JavaScript. |
| `script` | string | yes | Optional. Paste it after the form to show the result in the page. |

<a id="schema-campaignstatus"></a>

### CampaignStatus

`draft` is being set up; `scheduled` is launched and waiting for its
send time; `sending`, then `sent`. `cancelled` was stopped by the
account, `paused` by us mid-send, and `failed` couldn't finish.

Type: string: one of `draft`, `scheduled`, `sending`, `sent`, `cancelled`, `paused`, `failed`

<a id="schema-campaignrecipients"></a>

### CampaignRecipients

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `all` | boolean | yes | True sends to every subscribed contact; tags is then empty. |
| `tags` | array of [TagRef](#schema-tagref) | yes | Otherwise the subscribed contacts with any of these tags. |

<a id="schema-recipientsinput"></a>

### RecipientsInput

`{"all": true}` for every subscribed contact, or `{"tag_ids": [...]}` for those with any of the tags.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `all` | boolean |  |  |
| `tag_ids` | array of string (uuid) |  |  |

<a id="schema-campaign"></a>

### Campaign

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `email_id` | string or null | yes | The email it sends. Null when a draft's email was deleted. |
| `recipients` | [CampaignRecipients](#schema-campaignrecipients) | yes |  |
| `recipient_count` | integer |  | How many contacts the recipients reach now. Single-campaign responses only. |
| `send_at` | string (date-time) or null | yes | When it goes out; null for when it's launched. |
| `tracking` | boolean | yes | Whether opens and clicks are counted, with mailgoose's own open pixel and link redirects. On by default; off means no pixel and no rewritten links. The unsubscribe link is never rewritten. |
| `status` | [CampaignStatus](#schema-campaignstatus) | yes |  |
| `version` | integer | yes | Goes up with every change. |
| `last_test` | [CampaignTest](#schema-campaigntest) or null | yes | The campaign's last test send; null before the first. |
| `launch` | [CampaignLaunch](#schema-campaignlaunch) or null | yes | When the campaign was launched, to how many and for how many credits; null for a draft. |
| `slice` | [CampaignSlice](#schema-campaignslice) |  |  |
| `created_at` | string (date-time) | yes |  |
| `updated_at` | string (date-time) | yes |  |

<a id="schema-campaignslice"></a>

### CampaignSlice

A new account's campaign goes out first to a slice of its
recipients, picked at random, then waits for bounces and complaints
before the rest is released (MG-62). An account is new for its
first 30 days or its first 3 launched campaigns, whichever ends
later. Present only on a campaign launched in a slice.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `state` | string: one of `sending_slice`, `slice_hold`, `held`, `released`, `stopped`, `cancelled` | yes | `sending_slice`: the slice is going out. `slice_hold`: the slice is out and the rest waits until `release_at`. `held`: the slice went over the early-warning limits for complaints or hard bounces, so the account's limit is lowered and the rest waits until `release_at`, when it's checked again. `released`: the rest was let go. `stopped`: the slice went over the limits that pause an account, so the rest was never sent and its credits came back. `cancelled`: the campaign was cancelled, stopped or its account suspended before the rest went, so the rest was never sent and its credits came back. While the rest waits, the campaign can be cancelled. |
| `size` | integer | yes | How many recipients the slice went to. |
| `release_at` | string (date-time) or null | yes | When the rest is checked and released, while it waits; null otherwise. |

<a id="schema-campaignreport"></a>

### CampaignReport

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `campaign_id` | string (uuid) | yes |  |
| `status` | [CampaignStatus](#schema-campaignstatus) | yes |  |
| `launched_at` | string (date-time) or null | yes | When it was launched; null for a draft. |
| `tracking` | boolean | yes | Whether opens and clicks were counted. |
| `recipients` | integer | yes | Copies sent. |
| `delivered` | integer | yes | Copies SES delivered to the receiving mail server. |
| `bounced` | integer | yes | Copies that hard bounced. |
| `complaints` | integer | yes | Copies marked as spam. |
| `unsubscribes` | integer | yes | People who unsubscribed from this campaign's link. |
| `opens` | [ReportOpens](#schema-reportopens) or null | yes | Null when the campaign wasn't tracked. |
| `clicks` | [ReportClicks](#schema-reportclicks) or null | yes | Null when the campaign wasn't tracked. |
| `credits_used` | integer | yes | Credits the campaign took. While it's sending, this includes the credits held for copies still to go. |
| `links` | array of [ReportLink](#schema-reportlink) | yes | Clicks per link, in the email's order; only links someone clicked. |
| `archived` | boolean | yes | True when the raw events are gone and these are the numbers saved 12 months after launch. |
| `as_of` | string (date-time) | yes | When these numbers were counted. |

<a id="schema-reportopens"></a>

### ReportOpens

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `unique` | integer | yes | Copies opened at least once (or clicked). |
| `estimated` | boolean | yes | Always true: mail apps that load images for privacy open every copy, and those that block images open none. |

<a id="schema-reportclicks"></a>

### ReportClicks

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `unique` | integer | yes | Copies with at least one click. |
| `total` | integer | yes | All clicks. |

<a id="schema-reportlink"></a>

### ReportLink

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `position` | integer | yes | The link's place among the email's links, from 1. |
| `url` | string | yes | Where it goes. When merge tags make it differ between copies, only the site (scheme and host), so no one person's link shows. |
| `varies` | boolean | yes | True when the address differs between copies. |
| `unique_clicks` | integer | yes |  |
| `total_clicks` | integer | yes |  |

<a id="schema-campaignlaunch"></a>

### CampaignLaunch

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `at` | string (date-time) | yes | When it was launched. |
| `recipient_count` | integer | yes | Who it goes to, fixed at launch. |
| `credits_reserved` | integer | yes | Credits held for them; any not used are given back. |

<a id="schema-launchcampaignrequest"></a>

### LaunchCampaignRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `confirm` | string |  | The code from the launch summary, given only after the person said yes to it. Any case; the dash is optional. |

<a id="schema-reschedulecampaignrequest"></a>

### RescheduleCampaignRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `send_at` | string (date-time) | yes | The new send time, with its offset; stored in UTC. |

<a id="schema-launchresult"></a>

### LaunchResult

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `campaign` | [Campaign](#schema-campaign) | yes |  |
| `recipient_count` | integer | yes | Who the campaign goes to, fixed at launch. |
| `credits_reserved` | integer | yes | Credits held for them now. |

<a id="schema-campaigntest"></a>

### CampaignTest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `at` | string (date-time) | yes | When the test went out. |
| `version` | integer | yes | The campaign's version it was made from. |
| `email_version` | integer | yes | The email's version it was made from. |

<a id="schema-testcampaignrequest"></a>

### TestCampaignRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `to` | array of string (email) |  | Who gets the test. Left out or empty, the account owner. |

<a id="schema-testsendresult"></a>

### TestSendResult

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `campaign` | [Campaign](#schema-campaign) | yes |  |
| `to` | array of string | yes | Who each copy went to. |
| `sent_today` | integer | yes | Test emails the account sent in the last 24 hours, these included. |
| `daily_limit` | integer | yes | Test emails an account can send in any 24 hours. |
| `from` | [MailAddress](#schema-mailaddress) | yes |  |
| `reply_to` | array of [MailAddress](#schema-mailaddress) | yes |  |
| `shared_domain` | boolean | yes | True when the test went from mailgoose's shared sending domain (MG-85), because the email's From isn't on a verified domain of the account: `from` is the account's shared address and replies go to `sender`. Adding the sender's domain would do better for deliverability and trust. |
| `sender` | string or null | yes | The verified address the email was set to go from, when `shared_domain`; null otherwise. |

<a id="schema-mailaddress"></a>

### MailAddress

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes |  |
| `email` | string | yes |  |

<a id="schema-campaignlist"></a>

### CampaignList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [Campaign](#schema-campaign) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-createcampaignrequest"></a>

### CreateCampaignRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes |  |
| `email_id` | string (uuid) | yes |  |
| `recipients` | [RecipientsInput](#schema-recipientsinput) | yes |  |
| `send_at` | string (date-time) or null |  | A time in the next year; null or left out sends when launched. |
| `tracking` | boolean |  | Count opens and clicks (the default, true), or not. |

<a id="schema-updatecampaignrequest"></a>

### UpdateCampaignRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string |  |  |
| `email_id` | string (uuid) |  |  |
| `recipients` | [RecipientsInput](#schema-recipientsinput) |  |  |
| `send_at` | string (date-time) or null |  | A time in the next year; null sends when launched. |
| `tracking` | boolean |  | Count opens and clicks (the default, true), or not. |
| `version` | integer |  | The version you read; the change is refused if the campaign changed since. |

<a id="schema-recipientcountrequest"></a>

### RecipientCountRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `recipients` | [RecipientsInput](#schema-recipientsinput) | yes |  |

<a id="schema-recipientcount"></a>

### RecipientCount

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `recipient_count` | integer | yes |  |

<a id="schema-emaillist"></a>

### EmailList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [EmailSummary](#schema-emailsummary) | yes |  |
| `next_cursor` | string or null | yes |  |

<a id="schema-createemailrequest"></a>

### CreateEmailRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes |  |
| `kind` | [EmailKind](#schema-emailkind) |  |  |
| `document` | [EmailDocument](#schema-emaildocument) |  |  |

<a id="schema-updateemailrequest"></a>

### UpdateEmailRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | integer | yes | The version you read. |
| `name` | string |  |  |
| `settings` | [EmailSettingsUpdate](#schema-emailsettingsupdate) |  |  |

<a id="schema-replaceemaildocumentrequest"></a>

### ReplaceEmailDocumentRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | integer | yes | The version you read. |
| `document` | [EmailDocument](#schema-emaildocument) | yes |  |

<a id="schema-importemailhtmlrequest"></a>

### ImportEmailHtmlRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | integer | yes | The version you read. |
| `html` | string | yes | The whole markup. Over 102 KB is refused as `too_large`. |

<a id="schema-importemailhtmlresult"></a>

### ImportEmailHtmlResult

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email` | [Email](#schema-email) | yes |  |
| `warnings` | array of [HtmlIssue](#schema-htmlissue) | yes | CSS that some clients drop; the email still sends. |

<a id="schema-htmlissue"></a>

### HtmlIssue

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `line` | integer | yes | The line it's on, counting from 1. |
| `code` | string: one of `malformed_markup`, `script`, `form`, `iframe`, `embedded_object`, `external_stylesheet`, `event_handler`, `javascript_url`, `data_url`, `redirect`, `missing_unsubscribe`, `merge_tag_link`, `too_large`, `unsupported_css` | yes |  |
| `message` | string | yes |  |

<a id="schema-emailcheck"></a>

### EmailCheck

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `ok` | boolean | yes | True when there are no errors, so the email can be sent. |
| `errors` | array of [EmailCheckIssue](#schema-emailcheckissue) | yes |  |
| `warnings` | array of [EmailCheckIssue](#schema-emailcheckissue) | yes |  |

<a id="schema-emailcheckissue"></a>

### EmailCheckIssue

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `code` | string: one of `missing_subject`, `missing_from`, `unverified_from`, `unverified_reply_to`, `missing_postal_address`, `empty_email`, `merge_tag`, `missing_unsubscribe`, `broken_link`, `too_large`, `invalid_html`, `unsafe_link`, `insecure_link`, `link_unreachable`, `link_not_checked`, `link_safety_not_checked`, `missing_alt`, `near_size_limit`, `unsupported_css`, `shared_domain` | yes |  |
| `message` | string | yes |  |
| `path` | string |  | The block field or setting it's about, such as `blocks[2].alt` or `settings.subject`. |
| `line` | integer |  | The line of an html email's markup it's on. |
| `url` | string |  | The link it's about. |

<a id="schema-duplicateemailrequest"></a>

### DuplicateEmailRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string |  |  |

<a id="schema-addemailblockrequest"></a>

### AddEmailBlockRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | integer | yes | The version you read. |
| `block` | object | yes | A Block with at least its `type`. `id` and fields with defaults may be left out. A footer can't be added; every blocks email already has one. |
| `after` | [BlockId](#schema-blockid) |  |  |
| `index` | integer |  |  |
| `parent_id` | [BlockId](#schema-blockid) |  |  |
| `column` | integer |  |  |

<a id="schema-updateemailblockrequest"></a>

### UpdateEmailBlockRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | integer | yes | The version you read. |
| `fields` | object | yes | The block's fields to set, by name, as in its Block schema. Not `id` or `type`. |

<a id="schema-moveemailblockrequest"></a>

### MoveEmailBlockRequest

The new place, as in BlockPlace.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `version` | integer | yes | The version you read. |
| `after` | [BlockId](#schema-blockid) |  |  |
| `index` | integer |  |  |
| `parent_id` | [BlockId](#schema-blockid) |  |  |
| `column` | integer |  |  |

<a id="schema-emailblockentry"></a>

### EmailBlockEntry

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `block` | [Block](#schema-block) | yes |  |
| `path` | string | yes | The block's place, as in an `invalid_document` error, such as `blocks[2].columns[0].blocks[1]`. |
| `parent_id` | string or null | yes | The columns block holding it; null at the top level. |
| `column` | integer or null | yes | Which column of `parent_id` holds it; null at the top level. |
| `index` | integer | yes | Its place in its list, 0 first. |

<a id="schema-emailblocklist"></a>

### EmailBlockList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `email_id` | string (uuid) | yes |  |
| `version` | integer | yes |  |
| `data` | array of [EmailBlockEntry](#schema-emailblockentry) | yes |  |

<a id="schema-emailblockchange"></a>

### EmailBlockChange

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `block` | [EmailBlockEntry](#schema-emailblockentry) | yes |  |
| `email` | [Email](#schema-email) | yes |  |

<a id="schema-error"></a>

### Error

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `error` | object | yes |  |

<a id="schema-adminreasonrequest"></a>

### AdminReasonRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `reason` | string | yes | Why, for the admin log (and, for a pause, the account). At most 500 characters. |

<a id="schema-granttesterrequest"></a>

### GrantTesterRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `until` | string (date-time) |  | When it stops sending; at most 366 days away. Left out, it's 30 days from now. |
| `credits` | integer |  | Its sends for its life, counted from when it first became a tester. Left out, it's 500. |
| `reason` | string | yes | Why, for the admin log. At most 500 characters. |

<a id="schema-grantcreditsrequest"></a>

### GrantCreditsRequest

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `amount` | integer | yes |  |
| `reason` | string | yes |  |

<a id="schema-adminrates"></a>

### AdminRates

Copies sent, and the recipients who hard bounced or complained.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `sent` | integer | yes |  |
| `hard_bounces` | integer | yes |  |
| `complaints` | integer | yes |  |

<a id="schema-adminaccountsummary"></a>

### AdminAccountSummary

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `status` | string | yes |  |
| `plan` | string | yes |  |
| `owner_email` | string or null | yes |  |
| `sending_paused_at` | string (date-time) or null | yes |  |
| `sending_paused_reason` | string or null | yes |  |
| `suspended_at` | string (date-time) or null | yes | When the account was suspended (MG-65); null when it isn't. |
| `suspended_reason` | string or null | yes |  |
| `suspended_by` | string or null | yes | The admin's address, `mailgoosed`, or `automatic`. |
| `review` | string or null: one of `waiting`, `approved`, `rejected`, `<nil>` | yes | The sign-up review (MG-66); null for an account that never needed one. |
| `review_flags` | array of string: one of `disposable_domain`, `ip_burst`, `suspended_ip`, `suspended_domain` | yes | What looked risky at sign-up: a disposable address, more than 3 accounts from its IP in 24 hours, or an IP or company domain shared with a suspended account. Empty when nothing did. |
| `review_at` | string (date-time) or null | yes | When the account started waiting for review. |
| `sending_mode` | [SendingMode](#schema-sendingmode) | yes |  |
| `test_addresses_confirmed` | integer | yes | Test addresses whose link was used (MG-80). |
| `test_addresses_pending` | integer | yes | Test addresses whose link hasn't been used yet. |
| `created_at` | string (date-time) | yes |  |

<a id="schema-adminaccountlist"></a>

### AdminAccountList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [AdminAccountSummary](#schema-adminaccountsummary) | yes |  |
| `next_cursor` | null | yes |  |

<a id="schema-admindeliverabilityrates"></a>

### AdminDeliverabilityRates

Campaign copies sent in the window, and the recipients whose hard
bounce or complaint was reported in it, with the rates as
percentages (rounded to 4 places; null when nothing was sent in the
window).

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `sent` | integer | yes |  |
| `hard_bounces` | integer | yes |  |
| `complaints` | integer | yes |  |
| `hard_bounce_percent` | number or null | yes |  |
| `complaint_percent` | number or null | yes |  |

<a id="schema-adminaccountdeliverability"></a>

### AdminAccountDeliverability

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `account_id` | string (uuid) | yes |  |
| `account_name` | string | yes |  |
| `owner_email` | string or null | yes |  |
| `sending_mode` | [SendingMode](#schema-sendingmode) | yes |  |
| `tester_until` | string (date-time) or null | yes | When its tester account (MG-86) ends, or ended; null when it isn't a tester account. |
| `sending_paused_at` | string (date-time) or null | yes | When its sending was paused; null when it isn't. |
| `suspended_at` | string (date-time) or null | yes | When it was suspended; null when it isn't. |
| `throttled_until` | string (date-time) or null | yes | When its early-warning throttle lifts; null when it isn't throttled. |
| `last_24_hours` | [AdminDeliverabilityRates](#schema-admindeliverabilityrates) | yes |  |
| `last_7_days` | [AdminDeliverabilityRates](#schema-admindeliverabilityrates) | yes |  |
| `over_warning` | boolean | yes | Whether its last 7 days are over the early warning, as the daily digest judges them: see `early_warning`. |

<a id="schema-adminearlywarning"></a>

### AdminEarlyWarning

The early warning: over `complaint_percent` complaints (and at least
`min_complaints` of them) or over `hard_bounce_percent` hard
bounces, on `min_copies` or more copies.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `complaint_percent` | number | yes |  |
| `hard_bounce_percent` | number | yes |  |
| `min_complaints` | integer | yes |  |
| `min_copies` | integer | yes |  |

<a id="schema-admindeliverabilityreport"></a>

### AdminDeliverabilityReport

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [AdminAccountDeliverability](#schema-adminaccountdeliverability) | yes |  |
| `next_cursor` | null | yes |  |
| `total` | integer | yes | Every account in the report, including those over `limit`. |
| `early_warning` | [AdminEarlyWarning](#schema-adminearlywarning) | yes |  |

<a id="schema-adminheldimport"></a>

### AdminHeldImport

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `file_name` | string or null | yes |  |
| `total_rows` | integer | yes |  |
| `hold_reason` | string | yes |  |
| `list_source` | string or null | yes |  |
| `list_source_detail` | string or null | yes |  |
| `checks` | object | yes | What the import's checks found. `shared_addresses` is how many of its distinct addresses other accounts imported, and `shared_account_id` the account with most of them; no addresses are shown. |
| `status` | string | yes |  |
| `created_at` | string (date-time) | yes |  |

<a id="schema-adminlivecampaign"></a>

### AdminLiveCampaign

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `name` | string | yes |  |
| `status` | string | yes |  |
| `send_at` | string (date-time) or null | yes |  |
| `recipients` | integer | yes |  |
| `rates` | [AdminRates](#schema-adminrates) | yes |  |

<a id="schema-adminaccount"></a>

### AdminAccount

Type: [AdminAccountSummary](#schema-adminaccountsummary) and object

<a id="schema-adminthrottle"></a>

### AdminThrottle

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | integer | yes |  |
| `campaign_id` | string (uuid) or null | yes |  |
| `reason` | string | yes |  |
| `created_at` | string (date-time) | yes |  |
| `until` | string (date-time) | yes | When it lifts, or lifted. |

<a id="schema-creditgrant"></a>

### CreditGrant

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `account_id` | string (uuid) | yes |  |
| `amount` | integer | yes |  |
| `expires_at` | string (date-time) | yes |  |
| `credit_balance` | integer | yes |  |

<a id="schema-approvedimport"></a>

### ApprovedImport

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | string (uuid) | yes |  |
| `account_id` | string (uuid) | yes |  |
| `contacts_released` | integer | yes | How many of its contacts can now be mailed. |

<a id="schema-adminaction"></a>

### AdminAction

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `id` | integer | yes |  |
| `actor` | string | yes | The admin's address, `mailgoosed` (the server's command line) or `automatic`. |
| `action` | string: one of `grant_admin`, `revoke_admin`, `pause`, `unpause`, `auto_pause`, `stop_campaign`, `approve_import`, `grant_credits`, `throttle`, `suspend`, `unsuspend`, `auto_suspend`, `approve_signup`, `reject_signup`, `allow_full_sending`, `set_test_mode`, `grant_tester`, `revoke_tester` | yes |  |
| `account_id` | string (uuid) or null | yes |  |
| `target_id` | string or null | yes | The campaign, import or user acted on. |
| `reason` | string | yes |  |
| `details` | object | yes |  |
| `created_at` | string (date-time) | yes |  |

<a id="schema-adminactionlist"></a>

### AdminActionList

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `data` | array of [AdminAction](#schema-adminaction) | yes |  |
| `next_cursor` | string or null | yes |  |
