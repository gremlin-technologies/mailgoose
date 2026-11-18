package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

func TestCampaignsAgainstAPI(t *testing.T) {
	h := liveAPI(t)
	h.app.Stdin = strings.NewReader(springDoc)
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("email: exit %d %s", code, h.errOut)
	}
	emailID := h.jsonOut(t)["id"].(string)
	for _, args := range [][]string{
		{"tags", "create", "VIP"},
		{"contacts", "add", "a@example.com", "--status", "subscribed"},
		{"contacts", "add", "b@example.com", "--status", "subscribed"},
		{"contacts", "add", "c@example.com", "--status", "unconfirmed"},
		{"tags", "add", "VIP", "a@example.com", "c@example.com"},
	} {
		if code := h.run(args...); code != ExitOK {
			t.Fatalf("%v: exit %d %s", args, code, h.errOut)
		}
	}

	if code := h.run("campaign", "count", "--all"); code != ExitOK || h.out.String() != "2 subscribed contacts.\n" {
		t.Errorf("count all: exit %d %q %s", code, h.out, h.errOut)
	}
	if code := h.run("campaign", "count", "--tag", "VIP", "--json"); code != ExitOK || h.jsonOut(t)["recipient_count"] != 1.0 {
		t.Errorf("count VIP: exit %d %s %s", code, h.out, h.errOut)
	}

	at := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Minute)
	if code := h.run("campaign", "create", "--name", "VIP preview", "--email", emailID, "--tag", "VIP", "--send-at", at.Format(time.RFC3339), "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	c := h.jsonOut(t)
	id := c["id"].(string)
	if c["status"] != "draft" || c["recipient_count"] != 1.0 || c["send_at"] != at.Format(time.RFC3339) || c["tracking"] != true {
		t.Errorf("created %v", c)
	}

	if code := h.run("campaign", "get", id); code != ExitOK {
		t.Fatalf("get: exit %d %s", code, h.errOut)
	}
	for _, want := range []string{"VIP preview", "Status:", "draft", "subscribed contacts tagged VIP", "Recipients now:  1", "Tracking:", "opens and clicks", emailID} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("get lacks %q:\n%s", want, h.out)
		}
	}

	if code := h.run("campaign", "update", id, "--all", "--send-at", "now", "--version", "1"); code != ExitOK {
		t.Fatalf("update: exit %d %s", code, h.errOut)
	}
	for _, want := range []string{"Updated campaign \"VIP preview\"", "version 2", "every subscribed contact", "Recipients now:  2", "when launched"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("update lacks %q:\n%s", want, h.out)
		}
	}
	if code := h.run("campaign", "update", id, "--tracking=false", "--json"); code != ExitOK || h.jsonOut(t)["tracking"] != false {
		t.Errorf("tracking off: exit %d %s %s", code, h.out, h.errOut)
	}
	if code := h.run("campaign", "create", "--name", "Untracked", "--email", emailID, "--all", "--tracking=false", "--json"); code != ExitOK || h.jsonOut(t)["tracking"] != false {
		t.Errorf("create untracked: exit %d %s %s", code, h.out, h.errOut)
	}
	if code := h.run("campaign", "update", id, "--name", "x", "--version", "1"); code != ExitError || !strings.Contains(h.errOut.String(), "changed since") {
		t.Errorf("stale: exit %d %s", code, h.errOut)
	}

	for _, args := range [][]string{
		{"campaign", "create", "--email", emailID, "--all"},
		{"campaign", "create", "--name", "x", "--all"},
		{"campaign", "create", "--name", "x", "--email", emailID},
		{"campaign", "create", "--name", "x", "--email", emailID, "--all", "--tag", "VIP"},
		{"campaign", "create", "--name", "x", "--email", emailID, "--all", "--send-at", "soon"},
		{"campaign", "update", id},
		{"campaign", "count"},
		{"campaign", "get", "spring"},
		{"campaign", "delete", id},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d %s", args, code, h.errOut)
		}
	}
	if code := h.run("campaign", "create", "--name", "x", "--email", emailID, "--tag", "nope"); code != ExitNotFound {
		t.Errorf("unknown tag: exit %d %s", code, h.errOut)
	}
	if code := h.run("campaign", "create", "--name", "x", "--email", emailID, "--all", "--send-at", "2001-01-01 09:00"); code == ExitOK || !strings.Contains(h.errOut.String(), "future") {
		t.Errorf("past: exit %d %s", code, h.errOut)
	}

	if code := h.run("campaign", "list", "--status", "draft"); code != ExitOK || !strings.Contains(h.out.String(), "VIP preview") || !strings.Contains(h.out.String(), "when launched") {
		t.Errorf("list: exit %d %s %s", code, h.out, h.errOut)
	}
	if code := h.run("campaign", "list", "--status", "sent"); code != ExitOK || !strings.Contains(h.out.String(), "No campaigns") {
		t.Errorf("list sent: exit %d %s", code, h.out)
	}
	if code := h.run("campaign", "delete", id, "--yes"); code != ExitOK || !strings.Contains(h.out.String(), "Deleted campaign "+id) {
		t.Errorf("delete: exit %d %s", code, h.errOut)
	}
	if code := h.run("campaign", "get", id); code != ExitNotFound {
		t.Errorf("get deleted: exit %d", code)
	}
}

func TestParseSendAt(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{zone: chicago}
	ctx := context.Background()
	if at, err := a.parseSendAt(ctx, "send-at", "NOW"); at != nil || err != nil {
		t.Errorf("now: %v %v", at, err)
	}
	if at, err := a.parseSendAt(ctx, "send-at", "2026-12-01T09:00:00Z"); err != nil || !at.Equal(time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("rfc3339: %v %v", at, err)
	}
	// Without an offset, the time is in the account's zone.
	if at, err := a.parseSendAt(ctx, "at", "2026-12-01 09:00"); err != nil || !at.Equal(time.Date(2026, 12, 1, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("account zone: %v %v", at, err)
	}
	if _, err := a.parseSendAt(ctx, "at", "tomorrow"); err == nil || !strings.Contains(err.Error(), "--at") {
		t.Errorf("tomorrow: %v", err)
	}
}

func TestCampaignTestAgainstAPI(t *testing.T) {
	h := liveAPI(t)
	h.app.Stdin = strings.NewReader(strings.Replace(springDoc, "news@bakery.example", "hello@patsbakery.com", 1))
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("email: exit %d %s", code, h.errOut)
	}
	emailID := h.jsonOut(t)["id"].(string)
	if code := h.run("campaign", "create", "--name", "Spring", "--email", emailID, "--all", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)

	// Not ready: every problem is listed with its fix, and nothing is sent.
	if code := h.run("campaign", "test", id); code != ExitError {
		t.Fatalf("not ready: exit %d %s", code, h.errOut)
	}
	for _, want := range []string{"2 things to fix", "patsbakery.com isn't on a verified sending domain", "mailgoose domain add patsbakery.com", "no postal address", "mailgoose account update --postal-address"} {
		if !strings.Contains(h.errOut.String(), want) {
			t.Errorf("not ready lacks %q:\n%s", want, h.errOut)
		}
	}
	if code := h.run("campaign", "test", id, "--json"); code != ExitError || !strings.Contains(h.errOut.String(), `"problems"`) {
		t.Errorf("not ready json: exit %d %s", code, h.errOut)
	}

	if code := h.run("account", "update", "--postal-address", "12 Main St"); code != ExitOK {
		t.Fatalf("postal: exit %d %s", code, h.errOut)
	}
	v, _ := liveDBs.Load(t)
	if _, err := v.(*pgxpool.Pool).Exec(t.Context(), `
		INSERT INTO domains (account_id, name, mail_from, created_at, check_until, status)
		SELECT id, 'patsbakery.com', 'bounce', now(), now(), 'verified' FROM accounts`); err != nil {
		t.Fatal(err)
	}

	if code := h.run("campaign", "test", id); code != ExitOK {
		t.Fatalf("test: exit %d %s", code, h.errOut)
	}
	if got := h.out.String(); got != "Sent a test of \"Spring\" to pat@example.com.\n1 of 50 test emails used in the last 24 hours.\n" {
		t.Errorf("test: %q", got)
	}
	if code := h.run("campaign", "test", id, "--to", "sam@patsbakery.com", "--to", "pat@example.com", "--json"); code != ExitOK {
		t.Fatalf("test --to: exit %d %s", code, h.errOut)
	}
	if out := h.jsonOut(t); out["sent_today"] != 3.0 || out["campaign"].(map[string]any)["last_test"] == nil {
		t.Errorf("test --to: %v", out)
	}
	o, _ := liveOutboxes.Load(t)
	sent := o.(*mail.Fake).Sent()
	if len(sent) != 3 || sent[1].Message.To.Email != "sam@patsbakery.com" || sent[0].Message.Subject != "[Test] Spring menu" {
		t.Errorf("sent %+v", sent)
	}

	if code := h.run("campaign", "test", id, "--to", "someone@gmail.com"); code != ExitError || !strings.Contains(h.errOut.String(), "someone@gmail.com is neither") {
		t.Errorf("stranger: exit %d %s", code, h.errOut)
	}
	if code := h.run("campaign", "test", "spring"); code != ExitUsage {
		t.Errorf("bad id: exit %d", code)
	}
}

func TestCampaignLaunchAgainstAPI(t *testing.T) {
	h := liveAPI(t)
	h.app.Stdin = strings.NewReader(strings.Replace(springDoc, "news@bakery.example", "hello@patsbakery.com", 1))
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("email: exit %d %s", code, h.errOut)
	}
	emailID := h.jsonOut(t)["id"].(string)
	if code := h.run("campaign", "create", "--name", "Spring", "--email", emailID, "--all", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)
	if code := h.run("account", "update", "--postal-address", "12 Main St"); code != ExitOK {
		t.Fatalf("postal: exit %d %s", code, h.errOut)
	}
	v, _ := liveDBs.Load(t)
	db := v.(*pgxpool.Pool)
	if _, err := db.Exec(t.Context(), `
		INSERT INTO domains (account_id, name, mail_from, created_at, check_until, status)
		SELECT id, 'patsbakery.com', 'bounce', now(), now(), 'verified' FROM accounts`); err != nil {
		t.Fatal(err)
	}

	// Untested: the problem and its fix, and nothing launched.
	if code := h.run("campaign", "launch", id); code != ExitError ||
		!strings.Contains(h.errOut.String(), "nothing was launched") || !strings.Contains(h.errOut.String(), "mailgoose campaign test "+id) {
		t.Fatalf("untested: exit %d %s", code, h.errOut)
	}
	if code := h.run("campaign", "test", id); code != ExitOK {
		t.Fatalf("test: exit %d %s", code, h.errOut)
	}
	if code := h.run("contact", "add", "sam@patsbakery.com"); code != ExitOK {
		t.Fatalf("contact: exit %d %s", code, h.errOut)
	}
	if _, err := db.Exec(t.Context(), `
		INSERT INTO credit_transactions (account_id, type, amount, expires_at)
		SELECT id, 'purchase', 100, now() + interval '1 year' FROM accounts`); err != nil {
		t.Fatal(err)
	}

	// The summary, the code and exit 5.
	if code := h.run("campaign", "launch", id); code != ExitNeedsConfirmation {
		t.Fatalf("summary: exit %d %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{"Subject:", "Spring menu", "From:", "hello@patsbakery.com", "To:", "1 contact\n", "as soon as it's launched", "Confirmation code: "} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	code := out[strings.Index(out, "Confirmation code: ")+len("Confirmation code: "):]
	code = strings.TrimSpace(code)
	if !strings.Contains(h.errOut.String(), "--confirm "+code) || !strings.Contains(h.errOut.String(), "Only if they say yes") {
		t.Errorf("summary hint: %s", h.errOut)
	}

	// With --json the summary and code are in the error's details.
	if c := h.run("campaign", "launch", id, "--json"); c != ExitNeedsConfirmation || !strings.Contains(h.errOut.String(), `"confirmation_required"`) {
		t.Fatalf("summary json: exit %d %s", c, h.errOut)
	}
	var e struct {
		Error struct {
			Details struct {
				Code    string `json:"code"`
				Summary struct {
					RecipientCount int `json:"recipient_count"`
				} `json:"summary"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(h.errOut.Bytes(), &e); err != nil || e.Error.Details.Code == "" || e.Error.Details.Summary.RecipientCount != 1 {
		t.Fatalf("summary json: %v %s", err, h.errOut)
	}
	// A new summary replaces the code: the first one no longer works.
	if c := h.run("campaign", "launch", id, "--confirm", code); c != ExitError || !strings.Contains(h.errOut.String(), "Nothing was launched") {
		t.Errorf("old code: exit %d %s", c, h.errOut)
	}
	if c := h.run("campaign", "launch", id, "--confirm", e.Error.Details.Code); c != ExitOK {
		t.Fatalf("launch: exit %d %s", c, h.errOut)
	}
	if got := h.out.String(); got != "Launched \"Spring\" to 1 contact, with 1 credit reserved. It's sending now.\n" {
		t.Errorf("launch: %q", got)
	}
	if c := h.run("campaign", "get", id); c != ExitOK || !strings.Contains(h.out.String(), "Launched:") || !strings.Contains(h.out.String(), "sending") {
		t.Errorf("get: exit %d %s", c, h.out)
	}
}

func TestCampaignScheduleAgainstAPI(t *testing.T) {
	h := liveAPI(t)
	h.app.Stdin = strings.NewReader(strings.Replace(springDoc, "news@bakery.example", "hello@patsbakery.com", 1))
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("email: exit %d %s", code, h.errOut)
	}
	emailID := h.jsonOut(t)["id"].(string)
	if code := h.run("account", "update", "--postal-address", "12 Main St", "--time-zone", "America/Chicago"); code != ExitOK {
		t.Fatalf("account: exit %d %s", code, h.errOut)
	}
	// A send time without an offset is in the account's zone: 9:00 in
	// Chicago, stored in UTC and shown back in Chicago time.
	at := time.Now().AddDate(0, 0, 3).Format("2006-01-02") + " 09:00"
	if code := h.run("campaign", "create", "--name", "Spring", "--email", emailID, "--all", "--send-at", at, "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	c := h.jsonOut(t)
	id := c["id"].(string)
	stored, _ := time.Parse(time.RFC3339, c["send_at"].(string))
	if _, off := stored.In(mustZone(t, "America/Chicago")).Zone(); stored.UTC().Hour() != 9-off/3600 {
		t.Errorf("stored %s", c["send_at"])
	}
	if code := h.run("campaign", "get", id); code != ExitOK || !strings.Contains(h.out.String(), at+" CST") {
		t.Errorf("get: exit %d %s", code, h.out)
	}

	v, _ := liveDBs.Load(t)
	db := v.(*pgxpool.Pool)
	for _, sql := range []string{
		`INSERT INTO domains (account_id, name, mail_from, created_at, check_until, status) SELECT id, 'patsbakery.com', 'bounce', now(), now(), 'verified' FROM accounts`,
		`INSERT INTO credit_transactions (account_id, type, amount, expires_at) SELECT id, 'purchase', 100, now() + interval '1 year' FROM accounts`,
	} {
		if _, err := db.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	if code := h.run("contact", "add", "sam@patsbakery.com"); code != ExitOK {
		t.Fatalf("contact: exit %d %s", code, h.errOut)
	}
	if code := h.run("campaign", "test", id); code != ExitOK {
		t.Fatalf("test: exit %d %s", code, h.errOut)
	}

	// Not launched yet: there's nothing to cancel or move.
	if code := h.run("campaign", "cancel", id, "--yes"); code != ExitError || !strings.Contains(h.errOut.String(), "campaign delete") {
		t.Errorf("cancel draft: exit %d %s", code, h.errOut)
	}
	if code := h.run("campaign", "launch", id, "--json"); code != ExitNeedsConfirmation {
		t.Fatalf("summary: exit %d %s", code, h.errOut)
	}
	var e struct {
		Error struct {
			Details struct {
				Code string `json:"code"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(h.errOut.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if code := h.run("campaign", "launch", id, "--confirm", e.Error.Details.Code); code != ExitOK ||
		!strings.Contains(h.out.String(), "It sends at "+at+" CST.") {
		t.Fatalf("launch: exit %d %s %s", code, h.out, h.errOut)
	}

	// Moved, in the account's zone.
	later := time.Now().AddDate(0, 0, 5).Format("2006-01-02") + " 14:30"
	if code := h.run("campaign", "reschedule", id); code != ExitUsage {
		t.Errorf("no --at: exit %d", code)
	}
	if code := h.run("campaign", "reschedule", id, "--at", "2020-01-01 09:00"); code != ExitError || !strings.Contains(h.errOut.String(), "in the future") {
		t.Errorf("past: exit %d %s", code, h.errOut)
	}
	if code := h.run("campaign", "reschedule", id, "--at", later); code != ExitOK ||
		h.out.String() != "Campaign \"Spring\" now sends at "+later+" CST.\n" {
		t.Fatalf("reschedule: exit %d %q %s", code, h.out, h.errOut)
	}
	var runAt time.Time
	_ = db.QueryRow(t.Context(), `SELECT run_at FROM send_queue WHERE campaign_id = $1`, id).Scan(&runAt)
	if got := runAt.In(mustZone(t, "America/Chicago")).Format("2006-01-02 15:04"); got != later {
		t.Errorf("queue runs at %s", got)
	}

	// Cancelled: the credit comes back, and a second cancel changes nothing.
	if code := h.run("campaign", "cancel", id); code != ExitUsage {
		t.Errorf("no --yes: exit %d", code)
	}
	for range 2 {
		if code := h.run("campaign", "cancel", id, "--yes"); code != ExitOK || !strings.Contains(h.out.String(), "Cancelled campaign") {
			t.Fatalf("cancel: exit %d %s %s", code, h.out, h.errOut)
		}
	}
	var balance int
	_ = db.QueryRow(t.Context(), `SELECT sum(amount) FROM credit_transactions`).Scan(&balance)
	if balance != 100 {
		t.Errorf("balance %d after cancelling", balance)
	}
	if code := h.run("campaign", "reschedule", id, "--at", later); code != ExitError || !strings.Contains(h.errOut.String(), "cancelled") {
		t.Errorf("move cancelled: exit %d %s", code, h.errOut)
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
