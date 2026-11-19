#!/usr/bin/env bash
# Runs mailgoose locally: Postgres in Docker, migrations, then the API and the
# worker as local processes. Mail never goes to SES: the fake sender writes
# every message to .local/mail as an .eml file. Ctrl-C stops everything but
# Postgres (`make down` stops that).
#
# Set DATABASE_URL to use a Postgres you already run instead of Docker's.
# The API serves the web app from web/dist when it's built (`make web`); for
# live reload, run `make web-dev` alongside and use :5173.
set -euo pipefail
cd "$(dirname "$0")/.."

export APP_ENV=local
export PORT="${PORT:-8080}"
export PUBLIC_BASE_URL="${PUBLIC_BASE_URL:-http://localhost:$PORT}"
export MAIL_SENDER=fake
export MAIL_CAPTURE_DIR="$PWD/.local/mail"
# New accounts don't wait for an admin locally (MG-66), so a sign-up can
# launch straight away, as the end-to-end tests do. SIGNUP_REVIEW=all or
# flagged tries the review.
export SIGNUP_REVIEW="${SIGNUP_REVIEW:-off}"
if [ -f web/dist/index.html ]; then
  export WEB_DIR="$PWD/web/dist"
fi
# No AWS from a dev machine by accident: the worker idles without queue URLs.
unset SEND_QUEUE_URL SES_EVENTS_QUEUE_URL

if [ -z "${DATABASE_URL:-}" ]; then
  docker compose up -d --wait postgres
  export DATABASE_URL="postgres://mailgoose:mailgoose@localhost:5432/mailgoose?sslmode=disable"
fi

mkdir -p .local/bin "$MAIL_CAPTURE_DIR"
go build -o .local/bin/mailgoosed ./cmd/mailgoosed
.local/bin/mailgoosed migrate up

pids=()
stop() {
  trap - INT TERM EXIT
  kill "${pids[@]}" 2>/dev/null || true
  wait "${pids[@]}" 2>/dev/null || true
}
trap stop INT TERM EXIT

MIGRATE_ON_START=false .local/bin/mailgoosed serve &
pids+=($!)
.local/bin/mailgoosed worker &
pids+=($!)

web_line="not built (make web), or make web-dev for live reload on :5173"
if [ -n "${WEB_DIR:-}" ]; then web_line="$PUBLIC_BASE_URL"; fi

cat <<MSG

  API     $PUBLIC_BASE_URL  (try: curl $PUBLIC_BASE_URL/readyz)
  Web     $web_line
  Mail    $MAIL_CAPTURE_DIR  (make mail lists the newest messages)
  Stop    Ctrl-C

MSG
wait "${pids[@]}"
