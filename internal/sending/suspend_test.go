package sending

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// suspend suspends the harness's account in its own transaction.
func (h *harness) suspend(t *testing.T, by Suspender, reason string) (*Suspension, error) {
	t.Helper()
	ctx := context.Background()
	var s *Suspension
	err := pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		var err error
		s, err = Suspend(ctx, tx, h.acc, by, reason, nil, h.now)
		return err
	})
	return s, err
}

// TestSuspendStopsEverything suspends an account mid-batch (MG-65): the
// batch stops, the campaign is cancelled and its unsent reservations come
// back, keys and sessions are revoked, and it's logged.
func TestSuspendStopsEverything(t *testing.T) {
	h := launchedHarness(t, 19)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `
		INSERT INTO api_keys (account_id, kind, name, scope, secret_hash, secret_hint) VALUES
			($1, 'api_key', 'site', 'full', 'k1', 'k1'), ($1, 'cli_token', 'cli', 'full', 'k2', 'k2')`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO sessions (user_id, token_hash, expires_at) SELECT id, 's1', $2 FROM users WHERE account_id = $1`,
		h.acc, h.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	p := h.pipeline(fastLimits)
	before := h.balance(t)
	var sus *Suspension
	sender := &pausing{f: func(n int) {
		if n == 3 {
			var err error
			if sus, err = h.suspend(t, Suspender{UserID: "", Email: "staff@example.com"}, "Sent to a bought list."); err != nil {
				t.Error(err)
			}
		}
	}}
	p.Mail = sender
	q := &recorded{}
	dispatch(t, p, q)
	if err := p.HandleBatch(ctx, q.take()[0], 1); err != nil {
		t.Fatal(err)
	}
	if n := len(sender.Sent()); n != 3 {
		t.Errorf("%d sent, want 3", n)
	}
	c := h.counts(t)
	if c["sent"] != 3 || c["skipped"] != 17 || h.status(t) != campaigns.StatusCancelled {
		t.Errorf("%v, status %s", c, h.status(t))
	}
	if got := h.balance(t); got != before+17 {
		t.Errorf("balance %d, want %d: the 17 not sent come back, and nothing else changes", got, before+17)
	}
	if sus == nil {
		t.Fatal("not suspended")
	}
	if sus.KeysRevoked != 2 || sus.SessionsEnded != 1 || len(sus.CampaignsStopped) != 1 ||
		sus.OwnerEmail != "Pat@Home.example" || sus.AccountName != "Pat's Bakery" {
		t.Errorf("suspension = %+v", sus)
	}
	if !sus.ExportUntil().Equal(h.now.Add(30 * 24 * time.Hour)) {
		t.Errorf("export until %s", sus.ExportUntil())
	}
	var live int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM api_keys WHERE account_id = $1 AND revoked_at IS NULL`, h.acc).Scan(&live)
	if live != 0 {
		t.Errorf("%d keys still work", live)
	}
	var reason, by, action string
	var details []byte
	if err := h.db.QueryRow(ctx, `
		SELECT a.suspended_reason, a.suspended_by, x.action, x.details FROM accounts a
		JOIN admin_actions x ON x.account_id = a.id WHERE a.id = $1`, h.acc).Scan(&reason, &by, &action, &details); err != nil {
		t.Fatal(err)
	}
	if reason != "Sent to a bought list." || by != "staff@example.com" || action != ActionSuspend ||
		!strings.Contains(string(details), `"keys_revoked": 2`) {
		t.Errorf("%q %q %q %s", reason, by, action, details)
	}

	// Nothing more goes out, and it can't launch.
	dispatch(t, p, q)
	if len(q.take()) != 0 {
		t.Error("dispatched for a suspended account")
	}
	_, ps, err := h.svc.launchProblems(ctx, h.acc, h.campaign, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(ps, func(p Problem) bool { return p.Code == AccountSuspended }) {
		t.Errorf("launch problems %+v", ps)
	}

	// Once is enough.
	if _, err := h.suspend(t, Automatic, "Again."); !errors.Is(err, ErrAlreadySuspended) {
		t.Errorf("again: %v", err)
	}
}

func TestSuspendCancelsScheduled(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.buy(t, 10)
	at := time.Now().Add(48 * time.Hour) // the campaigns service checks against its own clock
	if _, err := h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{SendAt: &at}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatal(err)
	}
	if h.status(t) != campaigns.StatusScheduled {
		t.Fatalf("status %s", h.status(t))
	}
	before := h.balance(t)
	if _, err := h.suspend(t, Automatic, "Paused twice."); err != nil {
		t.Fatal(err)
	}
	if h.status(t) != campaigns.StatusCancelled || h.balance(t) != before+1 {
		t.Errorf("status %s, balance %d (was %d)", h.status(t), h.balance(t), before)
	}
	var action string
	_ = h.db.QueryRow(ctx, `SELECT action FROM admin_actions WHERE account_id = $1`, h.acc).Scan(&action)
	if action != ActionAutoSuspend {
		t.Errorf("logged %q", action)
	}
}

// TestAutoSuspendOnSecondPause: an account paused automatically for the
// second time in 30 days is suspended; one paused 40 days ago isn't.
func TestAutoSuspendOnSecondPause(t *testing.T) {
	for _, c := range []struct {
		name    string
		ago     time.Duration
		suspend bool
	}{
		{"second in 30 days", 10 * 24 * time.Hour, true},
		{"first was 40 days ago", 40 * 24 * time.Hour, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := launchedHarness(t, 999)
			ctx := context.Background()
			if _, err := h.db.Exec(ctx, `
				INSERT INTO admin_actions (actor, action, account_id, reason, created_at) VALUES ('automatic', 'auto_pause', $1, 'Earlier.', $2)`,
				h.acc, h.now.Add(-c.ago)); err != nil {
				t.Fatal(err)
			}
			h.markSent(t, h.now)
			h.eventsFor(t, "complaint", "abuse", 4, h.now)
			ap := h.check(t)
			if ap == nil {
				t.Fatal("not paused")
			}
			var at *time.Time
			var reason *string
			if err := h.db.QueryRow(ctx, `SELECT suspended_at, suspended_reason FROM accounts WHERE id = $1`, h.acc).Scan(&at, &reason); err != nil {
				t.Fatal(err)
			}
			if !c.suspend {
				if ap.Suspension != nil || at != nil {
					t.Errorf("suspended: %+v", ap.Suspension)
				}
				return
			}
			if ap.Suspension == nil || at == nil || ap.Suspension.By != Automatic.Email ||
				!strings.Contains(trimmed(reason), "paused automatically 2 times in 30 days") {
				t.Fatalf("suspension %+v, reason %q", ap.Suspension, trimmed(reason))
			}
			// The campaign that tipped it over is cancelled, not left paused.
			if h.status(t) != campaigns.StatusCancelled {
				t.Errorf("status %s", h.status(t))
			}
		})
	}
}

func TestSuspendedAlert(t *testing.T) {
	fake := &mail.Fake{}
	s := Suspension{AccountID: "acc-1", AccountName: "Pat's Bakery", OwnerEmail: "pat@example.com", Reason: "Sent to a bought list.",
		By: "staff@example.com", At: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC), KeysRevoked: 2, SessionsEnded: 1}
	a := &OpsAlert{Mail: fake, From: mail.Address{Name: "mailgoose", Email: "alerts@mail.mailgoose.test"}, To: "ops@example.com",
		AppURL: "https://app.mailgoose.test/", Log: slog.New(slog.DiscardHandler)}
	a.Suspended(context.Background(), s)
	sent := fake.Sent()
	if len(sent) != 2 {
		t.Fatalf("%d emails, want the owner's and ops'", len(sent))
	}
	owner := sent[0].Message
	if owner.To.Email != "pat@example.com" || len(owner.ReplyTo) != 1 || owner.ReplyTo[0].Email != "abuse@mail.mailgoose.test" ||
		owner.Subject != "Your mailgoose account is suspended" ||
		!strings.Contains(owner.Text, "Why: Sent to a bought list.") ||
		!strings.Contains(owner.Text, "https://app.mailgoose.test to download your contacts and campaigns, until December 19, 2026") ||
		!strings.Contains(owner.Text, "To appeal, reply to this email") || !strings.Contains(owner.Text, "credits are unchanged") {
		t.Errorf("owner's email = %+v", owner)
	}
	ops := sent[1].Message
	if ops.To.Email != "ops@example.com" || !strings.Contains(ops.Text, "mailgoose admin unsuspend acc-1") {
		t.Errorf("ops email = %+v", ops)
	}
	a.Abuse = "appeals@example.com"
	a.Suspended(context.Background(), s)
	if m, _ := fake.Last(); !strings.Contains(m.Message.Text, "appeals@example.com") {
		t.Errorf("abuse address not used: %s", m.Message.Text)
	}

	// An automatic suspension's alert comes with the pause's.
	before := len(fake.Sent())
	a.AutoPaused(context.Background(), AutoPause{AccountID: "acc-1", AccountName: "Pat's Bakery", CampaignName: "Bread week",
		Rates: Rates{Sent: 1000, Complaints: 4}, Reason: "Paused.", Suspension: &s})
	var subjects []string
	for _, m := range fake.Sent()[before:] {
		subjects = append(subjects, m.Message.Subject)
	}
	if len(subjects) != 3 || !strings.Contains(strings.Join(subjects, "|"), "suspended") {
		t.Errorf("emails after an automatic suspension: %v", subjects)
	}
}
