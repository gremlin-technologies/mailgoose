#!/usr/bin/env bash
# End-to-end test of the CLI (MG-58): from a new account and an API key to a
# delivered campaign, then a one-click unsubscribe and a bounce. Nothing
# prompts; every step uses --json and checks the result.
#
# It needs a local stack with the fake sender writing mail to a folder and no
# SES events queue, which is what `make dev` (scripts/dev.sh) runs. Mail to
# SES's mailbox simulator (success@, bounce@simulator.amazonses.com) then
# gets the delivery and bounce events SES would send.
#
#   make dev            # in one terminal
#   make e2e-cli        # in another
#
# MAILGOOSE_API_URL     the API (default http://localhost:8080)
# MAIL_CAPTURE_DIR      where the fake sender writes mail (default .local/mail)
# MAILGOOSE             the CLI binary (default: built into .local/bin)
set -euo pipefail
cd "$(dirname "$0")/.."

export MAILGOOSE_API_URL="${MAILGOOSE_API_URL:-http://localhost:8080}"
MAIL_DIR="${MAIL_CAPTURE_DIR:-$PWD/.local/mail}"
if [ -z "${MAILGOOSE:-}" ]; then
  mkdir -p .local/bin
  go build -o .local/bin/mailgoose ./cmd/mailgoose
  MAILGOOSE="$PWD/.local/bin/mailgoose"
fi

# A config of its own, so the run never touches the developer's sign-in.
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export MAILGOOSE_CONFIG="$work/config.json"
unset MAILGOOSE_API_KEY

start=$(date +%s)
run_id="$(date +%s)-$RANDOM"
owner="e2e-$run_id@example.com"
domain="e2e-$run_id.example.com"
delivered="success+$run_id@simulator.amazonses.com"
leaver="success+leave-$run_id@simulator.amazonses.com"
bouncer="bounce+$run_id@simulator.amazonses.com"

step() { printf '\n== %s (%ss)\n' "$*" "$(($(date +%s) - start))"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# mg runs the CLI with --json and prints its output. It fails the test on
# any exit code but the ones in OK_EXIT (default 0); for an allowed error it
# prints the error JSON from stderr instead.
mg() {
  local out code=0
  out="$("$MAILGOOSE" "$@" --json 2>"$work/err")" || code=$?
  if [[ " ${OK_EXIT:-0} " != *" $code "* ]]; then
    printf 'mailgoose %s exited %s\n%s\n%s\n' "$*" "$code" "$out" "$(cat "$work/err")" >&2
    exit 1
  fi
  if [ "$code" != 0 ]; then out="$(cat "$work/err")"; fi
  printf '%s' "$out"
}

# expect FIELD WANT JSON: the jq FIELD of JSON must equal WANT.
expect() {
  local got
  got="$(jq -r "$1" <<<"$3")"
  [ "$got" = "$2" ] || fail "$1 is '$got', want '$2' in: $3"
}

# wait_for SECONDS CMD...: run CMD every second until it succeeds.
wait_for() {
  local limit=$1
  shift
  for _ in $(seq "$limit"); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  fail "gave up after ${limit}s waiting for: $*"
}

# mail_to ADDRESS [SUBJECT]: the newest captured message to ADDRESS.
mail_to() {
  local f
  f="$(grep -l -i -F "To: <$1>" "$MAIL_DIR"/*.eml 2>/dev/null | xargs -r grep -l -F "${2:-}" | xargs -r ls -1t | head -1)"
  [ -n "$f" ] && printf '%s' "$f"
}

step "sign up with an emailed code"
out="$(mg signup --email "$owner" --account-name "E2E Bakery $run_id" --accept-terms)"
expect .status code_sent "$out"
wait_for 20 mail_to "$owner" "Your mailgoose code"
code="$(grep -m1 '^Subject: Your mailgoose code' "$(mail_to "$owner" "Your mailgoose code")" | grep -oE '[0-9]{6}')"
out="$(mg signup --email "$owner" --code "$code")"
expect .account.status active "$out"

step "make a full API key, and use only it from here on"
key="$(mg keys create --name "e2e $run_id" --scope full | jq -r .secret)"
mg logout >/dev/null
export MAILGOOSE_API_KEY="$key"
expect .user.email "$owner" "$(mg whoami)"

step "set up the account and a sending domain"
mg account update --postal-address "E2E Bakery\n12 Main St\nSpringfield, IL 62701" \
  --from-name "E2E Bakery" --from-email "news@$domain" >/dev/null
mg domain add "$domain" >/dev/null
# The local stack's fake SES verifies a domain on its first check.
wait_for 20 "$MAILGOOSE" domain verify "$domain" --json
expect '.missing_for_send | length' 0 "$(mg account get)"

step "add contacts on SES's mailbox simulator"
for a in "$delivered" "$leaver" "$bouncer"; do
  expect .status subscribed "$(mg contacts add "$a" --first-name Sam)"
done

step "build an email from blocks"
email="$(mg email create --name "Spring menu $run_id" | jq -r .id)"
mg email blocks add "$email" --type heading --text "Spring is here" >/dev/null
mg email blocks add "$email" --type text --text "Our spring menu starts Monday, {{first_name|there}}." >/dev/null
# No button: the check follows every link, and the test shouldn't depend on
# another site.
mg email update "$email" --subject "Spring is here" --from-name "E2E Bakery" --from-email "news@$domain" >/dev/null
expect '.errors | length' 0 "$(mg email check "$email")"

step "make a campaign and send a test"
campaign="$(mg campaign create --name "Spring menu $run_id" --email "$email" --all | jq -r .id)"
expect .recipient_count 3 "$(mg campaign count --all)"
mg campaign test "$campaign" >/dev/null
wait_for 20 mail_to "$owner" "Spring is here"

step "launch: a summary and code first, then --confirm"
# Exit 5, with the summary and code in the error's details.
summary="$(OK_EXIT=5 mg campaign launch "$campaign")"
expect .error.code confirmation_required "$summary"
expect .error.details.summary.recipient_count 3 "$summary"
confirm="$(jq -r .error.details.code <<<"$summary")"
[ -n "$confirm" ] && [ "$confirm" != null ] || fail "no confirmation code in: $summary"
mg campaign launch "$campaign" --confirm "$confirm" >/dev/null

step "wait until it's sent and the simulator's events are in"
sent() { [ "$(mg campaign get "$campaign" | jq -r .status)" = sent ]; }
wait_for 60 sent
for a in "$delivered" "$leaver" "$bouncer"; do mail_to "$a" "Spring is here" >/dev/null || fail "no campaign mail to $a"; done
counted() { local r; r="$(mg campaign report "$campaign")"; [ "$(jq -r .delivered <<<"$r")" = 2 ] && [ "$(jq -r .bounced <<<"$r")" = 1 ]; }
wait_for 30 counted

step "the bounce suppressed its contact"
expect .status bounced "$(mg contacts get "$bouncer")"
expect .status subscribed "$(mg contacts get "$delivered")"

step "one-click unsubscribe from the campaign mail"
msg="$(mail_to "$leaver" "Spring is here")"
unsub="$(grep -m1 -i '^List-Unsubscribe:' "$msg" | grep -oE 'https?://[^>]+')"
[ -n "$unsub" ] || fail "no List-Unsubscribe link in $msg"
status="$(curl -s -o /dev/null -w '%{http_code}' -X POST --data 'List-Unsubscribe=One-Click' "$unsub")"
[ "$status" = 200 ] || fail "one-click unsubscribe returned $status"
expect .status unsubscribed "$(mg contacts get "$leaver")"
expect .unsubscribes 1 "$(mg campaign report "$campaign")"

printf '\nPASS: CLI end to end in %ss\n' "$(($(date +%s) - start))"
