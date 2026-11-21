package signups

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// alerts records the ops alerts sent.
type alerts struct {
	mu       sync.Mutex
	subjects []string
	texts    []string
}

func (a *alerts) notify(_ context.Context, subject, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subjects = append(a.subjects, subject)
	a.texts = append(a.texts, text)
}

func (a *alerts) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.subjects)
}

func (h *harness) capCount(t *testing.T, formID string) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(), `SELECT count(*) FROM form_signups WHERE form_id = $1`, formID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (h *harness) contactCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(context.Background(), `SELECT count(*) FROM contacts WHERE account_id = $1`, h.acc).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// notices gives the harness's service the notices, with the owner's
// address on the account, and returns the outbox and the alerts.
func (h *harness) notices(t *testing.T) (*mail.Fake, *alerts) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `INSERT INTO users (account_id, email) VALUES ($1, 'pat@example.com')`, h.acc); err != nil {
		t.Fatal(err)
	}
	outbox, ops := &mail.Fake{}, &alerts{}
	h.svc.CapNotices = &CapNotices{Mail: outbox, From: mail.Address{Name: "mailgoose", Email: "notices@mail.mailgoose.test"}, Alert: ops.notify}
	return outbox, ops
}

func wantCapped(t *testing.T, err error, retryAt time.Time) {
	t.Helper()
	var ce *CapError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want a CapError", err)
	}
	if !ce.RetryAt.Equal(retryAt) || ce.Wait() != retryAt.Sub(ce.At) {
		t.Errorf("retry at %s (wait %s), want %s", ce.RetryAt, ce.Wait(), retryAt)
	}
}

// TestDailySignupCap: a form adds at most its cap of new contacts in a
// rolling 24 hours. Past it every submission is refused, new address or
// not, and nothing is stored; a submission from an address already on the
// list isn't counted; the cap lifts as the window rolls.
func TestDailySignupCap(t *testing.T) {
	h := newHarness(t)
	h.svc.DailyCap = 3
	t0 := h.now

	if _, err := h.submit(h.post("a@example.com")); err != nil {
		t.Fatal(err)
	}
	h.now = t0.Add(time.Hour)
	if _, err := h.submit(h.post("b@example.com")); err != nil {
		t.Fatal(err)
	}
	// Ann again adds nobody, so it doesn't count.
	if _, err := h.submit(h.post("a@example.com")); err != nil {
		t.Fatal(err)
	}
	h.now = t0.Add(2 * time.Hour)
	if _, err := h.submit(h.post("c@example.com")); err != nil {
		t.Fatal(err)
	}
	if n := h.capCount(t, h.form.ID); n != 3 {
		t.Fatalf("%d counted, want 3", n)
	}
	signups, _ := h.counts(t)
	sent := len(h.fake.Sent())

	h.now = t0.Add(3 * time.Hour)
	_, err := h.submit(h.post("d@example.com"))
	wantCapped(t, err, t0.Add(CapWindow))
	// An address on the list gets the same answer, so it can't be told apart.
	_, err = h.submit(h.post("b@example.com"))
	wantCapped(t, err, t0.Add(CapWindow))
	if n := h.contactCount(t); n != 3 {
		t.Errorf("%d contacts, want 3", n)
	}
	if after, _ := h.counts(t); after != signups {
		t.Errorf("signups %d, want %d", after, signups)
	}
	if len(h.fake.Sent()) != sent {
		t.Error("a refused signup sent a confirmation email")
	}
	// A bot is still answered as if it signed up, and adds nothing.
	bot := h.post("bot@example.com")
	bot.Honeypot = "spam"
	if res, err := h.submit(bot); err != nil || !res.Dropped {
		t.Errorf("bot: %+v %v", res, err)
	}

	// The cap is per form: another of the account's forms takes signups.
	name := "Sidebar"
	other, err := h.svc.Forms.Create(context.Background(), h.acc, forms.Changes{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := 4 * time.Second
	if _, err := h.svc.Submit(context.Background(), other.ID, Submission{Email: "e@example.com",
		Stamp: forms.Stamp(other.SigningKey, other.ID, h.now.Add(-time.Hour)), Elapsed: &elapsed, IP: "192.0.2.10"}, baseURL); err != nil {
		t.Fatalf("other form: %v", err)
	}

	// Just before Ann's signup is a day old the form is still full; just
	// after, it takes one more, and the rows that left the window go.
	h.now = t0.Add(CapWindow - time.Second)
	_, err = h.submit(h.post("d@example.com"))
	wantCapped(t, err, t0.Add(CapWindow))
	h.now = t0.Add(CapWindow + time.Second)
	if _, err := h.submit(h.post("d@example.com")); err != nil {
		t.Fatalf("after the window rolled: %v", err)
	}
	if n := h.capCount(t, h.form.ID); n != 3 {
		t.Errorf("%d rows kept, want 3 (Ann's pruned)", n)
	}
	_, err = h.submit(h.post("f@example.com"))
	wantCapped(t, err, t0.Add(time.Hour+CapWindow))
}

// TestDailySignupCapOff: a negative cap means none, and nothing is
// counted.
func TestDailySignupCapOff(t *testing.T) {
	h := newHarness(t)
	h.svc.DailyCap = -1
	for i := range 3 {
		if _, err := h.submit(h.post(fmt.Sprintf("p%d@example.com", i))); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.capCount(t, h.form.ID); n != 0 {
		t.Errorf("%d counted with no cap", n)
	}
	if h.svc.dailyCap() != 0 || (&Service{}).dailyCap() != DefaultDailyCap {
		t.Error("dailyCap")
	}
}

// TestDailySignupCapAcrossTasks: two API tasks (services on one database)
// taking signups for one form at once add no more than the cap between
// them.
func TestDailySignupCapAcrossTasks(t *testing.T) {
	h := newHarness(t)
	h.svc.DailyCap = 5
	tasks := []*Service{h.svc, {DB: h.db, Forms: h.svc.Forms, Settings: h.svc.Settings, Domains: h.svc.Domains,
		Mail: h.fake, DailyCap: 5, Now: h.svc.Now}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, capped := 0, 0
	var errs []error
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := tasks[i%2].Submit(context.Background(), h.form.ID, h.post(fmt.Sprintf("r%d@example.com", i)), baseURL)
			var ce *CapError
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.As(err, &ce):
				capped++
			default:
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if ok != 5 || capped != 15 || h.contactCount(t) != 5 || h.capCount(t, h.form.ID) != 5 {
		t.Errorf("%d taken, %d refused, %d contacts, %d counted; want 5, 15, 5, 5", ok, capped, h.contactCount(t), h.capCount(t, h.form.ID))
	}
}

// TestDailySignupCapNotices: the first refusal in a day emails the owner
// (from mailgoose's system address) and alerts ops, once per form across
// API tasks, and again only once the last notice is a day old.
func TestDailySignupCapNotices(t *testing.T) {
	h := newHarness(t)
	h.svc.DailyCap = 2
	outbox, ops := h.notices(t)
	t0 := h.now

	if _, err := h.submit(h.post("a@example.com")); err != nil {
		t.Fatal(err)
	}
	h.now = t0.Add(12 * time.Hour)
	if _, err := h.submit(h.post("b@example.com")); err != nil {
		t.Fatal(err)
	}
	if len(outbox.Sent()) != 0 || ops.count() != 0 {
		t.Fatal("told before the cap was reached")
	}

	// Ten refusals at once, from two tasks: one notice each.
	other := *h.svc
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc := h.svc
			if i%2 == 1 {
				svc = &other
			}
			_, err := svc.Submit(context.Background(), h.form.ID, h.post(fmt.Sprintf("x%d@example.com", i)), baseURL)
			var ce *CapError
			if !errors.As(err, &ce) {
				t.Errorf("refusal %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	sent := outbox.Sent()
	if len(sent) != 1 || ops.count() != 1 {
		t.Fatalf("%d owner emails, %d alerts; want 1 and 1", len(sent), ops.count())
	}
	m := sent[0].Message
	if m.Kind != mail.KindSystem || m.From.Email != "notices@mail.mailgoose.test" || m.To.Email != "pat@example.com" ||
		m.AccountID != h.acc || !strings.Contains(m.Subject, `"Footer"`) ||
		!strings.Contains(m.Text, "2 new contacts in the last 24 hours") ||
		!strings.Contains(m.Text, t0.Add(CapWindow).Format("Jan 2, 2006 at 15:04 UTC")) ||
		!strings.Contains(m.HTML, "Footer") {
		t.Errorf("owner email %+v", m)
	}
	// The form uses double opt-in, so the email doesn't suggest turning it on.
	if strings.Contains(m.Text, "--double-opt-in") {
		t.Errorf("owner email suggests double opt-in: %s", m.Text)
	}
	if !strings.Contains(ops.subjects[0], "Pat's Bakery") || !strings.Contains(ops.texts[0], h.form.ID) ||
		!strings.Contains(ops.texts[0], "mailgoose admin account "+h.acc) || !strings.Contains(ops.texts[0], "Double opt-in is on") {
		t.Errorf("alert %q: %s", ops.subjects[0], ops.texts[0])
	}

	// A day after Ann the form takes Dee, and turns Eve away, but its last
	// notice is only 12 hours old.
	h.now = t0.Add(CapWindow + time.Minute)
	if _, err := h.submit(h.post("d@example.com")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("e@example.com")); err == nil {
		t.Fatal("eve was taken")
	}
	if len(outbox.Sent()) != 1 || ops.count() != 1 {
		t.Fatalf("told again within a day: %d, %d", len(outbox.Sent()), ops.count())
	}

	// A day after the notice, the next refusal tells them again.
	h.now = t0.Add(12*time.Hour + CapWindow + time.Minute)
	if _, err := h.submit(h.post("f@example.com")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("g@example.com")); err == nil {
		t.Fatal("gee was taken")
	}
	if len(outbox.Sent()) != 2 || ops.count() != 2 {
		t.Fatalf("%d owner emails, %d alerts after a day; want 2 and 2", len(outbox.Sent()), ops.count())
	}

	// A form without double opt-in is told to turn it on.
	off := false
	if _, err := h.svc.Forms.Update(context.Background(), h.acc, h.form.ID, forms.Changes{DoubleOptIn: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(context.Background(), `UPDATE forms SET signup_cap_noticed_at = NULL WHERE id = $1`, h.form.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("h@example.com")); err == nil {
		t.Fatal("h was taken")
	}
	last, _ := outbox.Last()
	if !strings.Contains(last.Message.Text, "mailgoose form update "+h.form.ID+" --double-opt-in") {
		t.Errorf("owner email without the double opt-in step: %s", last.Message.Text)
	}
}

// TestDailySignupCapNoticeFailure: a notice that can't be sent doesn't
// change the answer, and without notices the cap still holds.
func TestDailySignupCapNoticeFailure(t *testing.T) {
	h := newHarness(t)
	h.svc.DailyCap = 1
	outbox, _ := h.notices(t)
	outbox.Err = errors.New("ses is down")
	if _, err := h.submit(h.post("a@example.com")); err != nil {
		t.Fatal(err)
	}
	_, err := h.submit(h.post("b@example.com"))
	var ce *CapError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v", err)
	}
	h.svc.CapNotices = nil
	if _, err := h.submit(h.post("c@example.com")); !errors.As(err, &ce) {
		t.Fatalf("without notices: %v", err)
	}
}

// TestDailySignupCapProblem: form get says when a form is at its cap.
func TestDailySignupCapProblem(t *testing.T) {
	h := newHarness(t)
	h.svc.DailyCap = 1
	ctx := context.Background()
	if probs, err := h.svc.Problems(ctx, h.form); err != nil || len(probs) != 0 {
		t.Fatalf("problems before: %+v %v", probs, err)
	}
	if _, err := h.submit(h.post("a@example.com")); err != nil {
		t.Fatal(err)
	}
	probs, err := h.svc.Problems(ctx, h.form)
	if err != nil || len(probs) != 1 || probs[0].Code != "signup_cap_reached" ||
		!strings.Contains(probs[0].Message, t0Format(h.now.Add(CapWindow))) || strings.Contains(probs[0].Hint, "--double-opt-in") {
		t.Fatalf("problems at the cap: %+v %v", probs, err)
	}
	h.now = h.now.Add(CapWindow)
	if probs, err := h.svc.Problems(ctx, h.form); err != nil || len(probs) != 0 {
		t.Fatalf("problems a day later: %+v %v", probs, err)
	}
}

func t0Format(at time.Time) string { return at.UTC().Format("Jan 2, 2006 15:04 UTC") }
