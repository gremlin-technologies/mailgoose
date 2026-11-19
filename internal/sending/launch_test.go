package sending

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/render"
)

// buy gives the account n credits that last a year.
func (h *harness) buy(t *testing.T, n int) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO credit_transactions (account_id, type, amount, expires_at, created_at) VALUES ($1, 'purchase', $2, $3, $4)`,
		h.acc, n, h.now.AddDate(1, 0, 0), h.now); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) balance(t *testing.T) int {
	t.Helper()
	var b int
	_ = h.db.QueryRow(context.Background(), `SELECT COALESCE(sum(amount), 0) FROM credit_transactions WHERE account_id = $1`, h.acc).Scan(&b)
	return b
}

func (h *harness) launch(code string) (*Launched, error) {
	return h.svc.Launch(context.Background(), h.acc, h.campaign.ID, code, nil)
}

// summary asks for a launch and expects a summary and code.
func (h *harness) summary(t *testing.T) *ConfirmationRequired {
	t.Helper()
	_, err := h.launch("")
	var cr *ConfirmationRequired
	if !errors.As(err, &cr) {
		t.Fatalf("want a summary, got %v", err)
	}
	return cr
}

// ready is a harness whose campaign can launch: tested, with credits.
func readyHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.buy(t, 10)
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	return h
}

func problemCodes(err error) string {
	var nr *NotReadyError
	if !errors.As(err, &nr) {
		return "not a NotReadyError: " + errString(err)
	}
	codes := make([]string, len(nr.Problems))
	for i, p := range nr.Problems {
		codes[i] = p.Code
	}
	return strings.Join(codes, ",")
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestLaunch(t *testing.T) {
	h := readyHarness(t)
	ctx := context.Background()
	h.svc.Mail.(*mail.Fake).Reset()

	cr := h.summary(t)
	s := cr.Summary
	if s.Subject != "Fresh bread, {{first_name|friend}}" || s.From.Email != "hello@patsbakery.com" || s.RecipientCount != 1 ||
		s.Credits != 1 || s.Balance != 10 || s.SendAt != nil || s.CampaignVersion != 1 || s.EmailVersion != h.email.Version {
		t.Errorf("summary %+v", s)
	}
	if len(cr.Code) != 9 || cr.Code[4] != '-' || !cr.ExpiresAt.Equal(h.now.Add(CodeTTL)) {
		t.Errorf("code %q expires %v", cr.Code, cr.ExpiresAt)
	}
	// Asking for the summary changes nothing.
	if h.balance(t) != 10 {
		t.Errorf("summary used credits")
	}

	// The code works typed any way.
	l, err := h.launch(strings.ToLower(strings.ReplaceAll(cr.Code, "-", " ")))
	if err != nil {
		t.Fatal(err)
	}
	c := l.Campaign
	if c.Status != campaigns.StatusSending || c.LaunchedAt == nil || !c.LaunchedAt.Equal(h.now) || l.RecipientCount != 1 || l.CreditsReserved != 1 {
		t.Errorf("launched %+v %+v", l, c)
	}
	if h.balance(t) != 9 {
		t.Errorf("balance %d after reserving 1", h.balance(t))
	}
	var ref string
	_ = h.db.QueryRow(ctx, `SELECT ref FROM credit_transactions WHERE account_id = $1 AND type = 'reserve'`, h.acc).Scan(&ref)
	if ref != "campaign:"+h.campaign.ID {
		t.Errorf("reserved under %q", ref)
	}
	var recipients, queued int
	var runAt time.Time
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND status = 'queued' AND contact_id IS NOT NULL`, h.campaign.ID).Scan(&recipients)
	_ = h.db.QueryRow(ctx, `SELECT count(*), max(run_at) FROM send_queue WHERE campaign_id = $1`, h.campaign.ID).Scan(&queued, &runAt)
	if recipients != 1 || queued != 1 || !runAt.Equal(h.now) {
		t.Errorf("%d recipients, %d queued at %v", recipients, queued, runAt)
	}
	if len(h.svc.Mail.(*mail.Fake).Sent()) != 0 {
		t.Error("launch sent mail itself")
	}

	// The campaign keeps the compiled email; editing the email now changes
	// nothing that goes out.
	var raw []byte
	_ = h.db.QueryRow(ctx, `SELECT launch FROM campaigns WHERE id = $1`, h.campaign.ID).Scan(&raw)
	var cp launchCopy
	if err := json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	m := cp.Template.Personalize(render.Recipient{Email: "sam@patsbakery.com", FirstName: "Sam"})
	if m.Subject != "Fresh bread, Sam" || !strings.Contains(m.HTML, "Hi Sam") || cp.From.Email != "hello@patsbakery.com" ||
		cp.PostalAddress != "12 Main St" || cp.EmailID != h.email.ID || cp.EmailVersion != h.email.Version {
		t.Errorf("copy %+v: %s", cp, m.Subject)
	}

	// Launching again, with the code or without, returns the same launch
	// and reserves nothing more.
	for _, code := range []string{cr.Code, "", "WRNG-CODE"} {
		again, err := h.launch(code)
		if err != nil || again.RecipientCount != 1 || again.CreditsReserved != 1 || !again.Campaign.LaunchedAt.Equal(*c.LaunchedAt) {
			t.Errorf("again with %q: %+v %v", code, again, err)
		}
	}
	if h.balance(t) != 9 {
		t.Errorf("relaunch changed the balance to %d", h.balance(t))
	}
	// A launched campaign can't be edited or tested.
	name := "x"
	var se *campaigns.StatusError
	if _, err := h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{Name: &name}); !errors.As(err, &se) {
		t.Errorf("edit after launch: %v", err)
	}
	if _, err := h.test(); !errors.As(err, &se) {
		t.Errorf("test after launch: %v", err)
	}
}

func TestLaunchScheduled(t *testing.T) {
	h := newHarness(t)
	h.buy(t, 10)
	at := h.now.Add(48 * time.Hour)
	c, err := h.svc.Campaigns.Update(context.Background(), h.acc, h.campaign.ID, campaigns.Changes{SendAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	h.campaign = c
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	cr := h.summary(t)
	if cr.Summary.SendAt == nil || !cr.Summary.SendAt.Equal(at) {
		t.Errorf("summary send time %v", cr.Summary.SendAt)
	}
	l, err := h.launch(cr.Code)
	if err != nil {
		t.Fatal(err)
	}
	var runAt time.Time
	_ = h.db.QueryRow(context.Background(), `SELECT run_at FROM send_queue WHERE campaign_id = $1`, h.campaign.ID).Scan(&runAt)
	if l.Campaign.Status != campaigns.StatusScheduled || !runAt.Equal(at) {
		t.Errorf("status %s, runs at %v", l.Campaign.Status, runAt)
	}

	// The recipients are fixed now, not when it sends: someone who joins
	// before the send time isn't added, and nothing is reserved for them.
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'lee@patsbakery.com', 'subscribed', 'api')`, h.acc); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1`, h.campaign.ID).Scan(&n)
	if n != 1 || h.balance(t) != 9 {
		t.Errorf("%d recipients, balance %d", n, h.balance(t))
	}
}

// A campaign picked by tag keeps the people it launched with when the tag
// is deleted, so a scheduled send can't quietly reach nobody.
func TestLaunchKeepsTaggedRecipients(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.buy(t, 10)
	var tag string
	if err := h.db.QueryRow(ctx, `INSERT INTO tags (account_id, name) VALUES ($1, 'regulars') RETURNING id`, h.acc).Scan(&tag); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO contact_tags (contact_id, tag_id) SELECT id, $2 FROM contacts WHERE account_id = $1`, h.acc, tag); err != nil {
		t.Fatal(err)
	}
	at := h.now.Add(48 * time.Hour)
	c, err := h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{SendAt: &at,
		Recipients: &campaigns.Recipients{TagIDs: []string{tag}}})
	if err != nil {
		t.Fatal(err)
	}
	h.campaign = c
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM tags WHERE id = $1`, tag); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND status = 'queued'`, h.campaign.ID).Scan(&n)
	if n != 1 {
		t.Errorf("%d recipients after the tag was deleted", n)
	}
}

func TestLaunchCodes(t *testing.T) {
	ctx := context.Background()

	t.Run("none issued", func(t *testing.T) {
		h := readyHarness(t)
		if _, err := h.launch("ABCD-EFGH"); !isCode(err, CodeMissing) {
			t.Errorf("%v", err)
		}
	})

	t.Run("wrong, then too many tries", func(t *testing.T) {
		h := readyHarness(t)
		cr := h.summary(t)
		for i := range MaxCodeAttempts {
			if _, err := h.launch("ZZZZ-ZZZZ"); !isCode(err, CodeWrong) {
				t.Fatalf("try %d: %v", i, err)
			}
		}
		if _, err := h.launch(cr.Code); !isCode(err, CodeUsedUp) {
			t.Errorf("right code after 5 wrong: %v", err)
		}
		// A new summary gives a new code that works.
		if _, err := h.launch(h.summary(t).Code); err != nil {
			t.Errorf("new code: %v", err)
		}
	})

	// The person agreed to the summary's count. A few more who join before
	// the confirm still launch; more than a tenth more needs a new summary.
	t.Run("audience grew", func(t *testing.T) {
		h := readyHarness(t)
		cr := h.summary(t)
		if cr.Summary.RecipientCount != 1 || AllowedGrowth(1) != 1 {
			t.Fatalf("summary of %d, allowance %d", cr.Summary.RecipientCount, AllowedGrowth(1))
		}
		for _, email := range []string{"lee@patsbakery.com", "kim@patsbakery.com"} {
			if _, err := h.db.Exec(ctx, `
				INSERT INTO contacts (account_id, email, status, source) VALUES ($1, $2, 'subscribed', 'api')`, h.acc, email); err != nil {
				t.Fatal(err)
			}
		}
		_, err := h.launch(cr.Code)
		var ce *CodeError
		if !errors.As(err, &ce) || ce.Reason != CodeGrew || ce.Summarized != 1 || ce.Recipients != 3 {
			t.Fatalf("%v", err)
		}
		if !strings.Contains(err.Error(), "now reaches 3 contacts, more than the 1 in the summary") {
			t.Errorf("message %q", err.Error())
		}
		var rows int
		_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1`, h.campaign.ID).Scan(&rows)
		if rows != 0 || h.balance(t) != 10 {
			t.Errorf("refused launch left %d recipients, balance %d", rows, h.balance(t))
		}
		// A new summary counts everyone, and its code launches.
		cr = h.summary(t)
		if cr.Summary.RecipientCount != 3 {
			t.Errorf("new summary of %d", cr.Summary.RecipientCount)
		}
		if l, err := h.launch(cr.Code); err != nil || l.RecipientCount != 3 {
			t.Errorf("%+v %v", l, err)
		}
	})

	t.Run("audience grew a little", func(t *testing.T) {
		h := readyHarness(t)
		cr := h.summary(t)
		if _, err := h.db.Exec(ctx, `
			INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'lee@patsbakery.com', 'subscribed', 'api')`, h.acc); err != nil {
			t.Fatal(err)
		}
		if l, err := h.launch(cr.Code); err != nil || l.RecipientCount != 2 || l.CreditsReserved != 2 {
			t.Errorf("%+v %v", l, err)
		}
	})

	t.Run("audience shrank", func(t *testing.T) {
		h := readyHarness(t)
		cr := h.summary(t)
		if _, err := h.db.Exec(ctx, `UPDATE contacts SET status = 'unsubscribed' WHERE account_id = $1`, h.acc); err != nil {
			t.Fatal(err)
		}
		// Fewer is never refused by the code; reaching nobody still is.
		if _, err := h.launch(cr.Code); isCode(err, CodeGrew) || problemCodes(err) != NoRecipients {
			t.Errorf("%v", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		h := readyHarness(t)
		cr := h.summary(t)
		h.now = h.now.Add(CodeTTL)
		if _, err := h.launch(cr.Code); !isCode(err, CodeExpired) {
			t.Errorf("%v", err)
		}
	})

	t.Run("campaign changed", func(t *testing.T) {
		h := readyHarness(t)
		cr := h.summary(t)
		name := "Bread week, final"
		if _, err := h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{Name: &name}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.launch(cr.Code); !isCode(err, CodeOutdated) {
			t.Errorf("%v", err)
		}
	})

	t.Run("email changed", func(t *testing.T) {
		h := readyHarness(t)
		cr := h.summary(t)
		if _, err := h.db.Exec(ctx, `UPDATE emails SET version = version + 1 WHERE id = $1`, h.email.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.launch(cr.Code); !isCode(err, CodeOutdated) {
			t.Errorf("%v", err)
		}
	})

	t.Run("one code per campaign", func(t *testing.T) {
		h := readyHarness(t)
		first := h.summary(t)
		second := h.summary(t)
		if _, err := h.launch(first.Code); first.Code != second.Code && !isCode(err, CodeWrong) {
			t.Errorf("first code after a second summary: %v", err)
		}
		if _, err := h.launch(second.Code); err != nil {
			t.Errorf("second code: %v", err)
		}
	})
}

func isCode(err error, reason string) bool {
	var ce *CodeError
	return errors.As(err, &ce) && ce.Reason == reason
}

type missingLinks struct{}

func (missingLinks) CheckLinks(_ context.Context, urls []string) map[string]emailcheck.LinkStatus {
	out := map[string]emailcheck.LinkStatus{}
	for _, u := range urls {
		out[u] = emailcheck.LinkStatus{Result: emailcheck.ResultMissing, Detail: "404 Not Found"}
	}
	return out
}

func TestLaunchRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("untested, then edited since the test", func(t *testing.T) {
		h := newHarness(t)
		h.buy(t, 10)
		if _, err := h.launch(""); problemCodes(err) != Untested {
			t.Errorf("untested: %s", problemCodes(err))
		}
		if _, err := h.test(); err != nil {
			t.Fatal(err)
		}
		name := "Bread week 2"
		if _, err := h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{Name: &name}); err != nil {
			t.Fatal(err)
		}
		_, err := h.launch("")
		var nr *NotReadyError
		if problemCodes(err) != Untested || !errors.As(err, &nr) || !strings.Contains(nr.Problems[0].Message, "since") {
			t.Errorf("edited: %v", err)
		}
	})

	t.Run("credits short says how many", func(t *testing.T) {
		h := newHarness(t)
		if _, err := h.test(); err != nil {
			t.Fatal(err)
		}
		_, err := h.launch("")
		var nr *NotReadyError
		if problemCodes(err) != CreditsShort || !errors.As(err, &nr) || nr.Problems[0].Details["short"] != int64(1) ||
			!strings.Contains(nr.Problems[0].Message, "1 short") {
			t.Errorf("%v", err)
		}
	})

	t.Run("credits spent between summary and confirm", func(t *testing.T) {
		h := readyHarness(t)
		cr := h.summary(t)
		if _, err := h.db.Exec(ctx, `INSERT INTO credit_transactions (account_id, type, amount) VALUES ($1, 'fee', -10)`, h.acc); err != nil {
			t.Fatal(err)
		}
		if _, err := h.launch(cr.Code); problemCodes(err) != CreditsShort {
			t.Errorf("%s", problemCodes(err))
		}
		var n int
		_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients`).Scan(&n)
		if n != 0 {
			t.Error("a refused launch left recipients")
		}
	})

	t.Run("several at once", func(t *testing.T) {
		h := readyHarness(t)
		// Due an hour after the harness's clock, which then moves past it.
		if _, err := h.db.Exec(ctx, `UPDATE campaigns SET send_at = $2 WHERE id = $1`, h.campaign.ID, h.now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		h.now = h.now.Add(2 * time.Hour)
		for _, q := range []string{
			`UPDATE accounts SET sending_paused_at = now(), sending_paused_reason = 'Too many bounces.', postal_address = NULL WHERE id = $1`,
			`UPDATE contacts SET status = 'unsubscribed' WHERE account_id = $1`,
		} {
			if _, err := h.db.Exec(ctx, q, h.acc); err != nil {
				t.Fatal(err)
			}
		}
		_, err := h.launch("")
		if got := problemCodes(err); got != "missing_postal_address,send_at_passed,account_paused" {
			t.Errorf("problems %s", got)
		}
		if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_paused_at = NULL, postal_address = '12 Main St' WHERE id = $1`, h.acc); err != nil {
			t.Fatal(err)
		}
		h.now = h.now.Add(-2 * time.Hour)
		if _, err := h.launch(""); problemCodes(err) != NoRecipients {
			t.Errorf("no recipients: %s", problemCodes(err))
		}
	})

	t.Run("subscription past due", func(t *testing.T) {
		h := readyHarness(t)
		if _, err := h.db.Exec(ctx, `UPDATE accounts SET sending_paused_at = now(), sending_paused_code = 'subscription_past_due',
			sending_paused_reason = 'The paid plan''s payment didn''t go through.' WHERE id = $1`, h.acc); err != nil {
			t.Fatal(err)
		}
		_, err := h.launch("")
		var nr *NotReadyError
		if problemCodes(err) != SubscriptionPastDue || !errors.As(err, &nr) || !strings.Contains(nr.Problems[0].Message, "payment didn't go through") ||
			!strings.Contains(nr.Problems[0].Hint, "mailgoose plan manage") {
			t.Errorf("%v", err)
		}
	})

	t.Run("unverified From", func(t *testing.T) {
		h := readyHarness(t)
		if _, err := h.db.Exec(ctx, `UPDATE domains SET status = 'pending' WHERE account_id = $1`, h.acc); err != nil {
			t.Fatal(err)
		}
		_, err := h.launch("")
		var nr *NotReadyError
		if problemCodes(err) != UnverifiedFrom || !errors.As(err, &nr) || !strings.Contains(nr.Problems[0].Hint, "mailgoose domain add patsbakery.com") {
			t.Errorf("%v", err)
		}
	})

	t.Run("email check errors", func(t *testing.T) {
		h := newHarness(t)
		h.buy(t, 10)
		doc := `{"settings":{"subject":"Menu"},"blocks":[{"id":"b","type":"button","text":"Order","href":"https://patsbakery.com/gone"}]}`
		if _, err := h.db.Exec(ctx, `UPDATE emails SET document = $2 WHERE id = $1`, h.email.ID, doc); err != nil {
			t.Fatal(err)
		}
		if _, err := h.test(); err != nil {
			t.Fatal(err)
		}
		_, err := h.svc.Launch(ctx, h.acc, h.campaign.ID, "", missingLinks{})
		if problemCodes(err) != CheckFailed {
			t.Errorf("%s", problemCodes(err))
		}
	})

	t.Run("not a draft", func(t *testing.T) {
		h := readyHarness(t)
		if _, err := h.db.Exec(ctx, `UPDATE campaigns SET status = 'cancelled' WHERE id = $1`, h.campaign.ID); err != nil {
			t.Fatal(err)
		}
		var se *campaigns.StatusError
		if _, err := h.launch(""); !errors.As(err, &se) {
			t.Errorf("%v", err)
		}
	})
}

func TestLaunchCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		c, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != 9 || c[4] != '-' || strings.ContainsAny(c, "01ILOU") || seen[c] {
			t.Fatalf("code %q", c)
		}
		seen[c] = true
	}
	if NormalizeCode(" ab2c-d3ef ") != "AB2CD3EF" {
		t.Error("normalize")
	}
}
