package sending

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/tester"
)

// Tester accounts (MG-86): full sending within a credit budget for their
// life, a daily cap, a contact cap and a stricter pause.

// makeTester makes h's account a tester since a day ago, with credits, until
// until.
func (h *harness) makeTester(t *testing.T, credits int, until time.Time) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		UPDATE accounts SET tester_started_at = $2, tester_until = $3, tester_credits = $4, sending_mode = 'full' WHERE id = $1`,
		h.acc, h.now.Add(-24*time.Hour), until, credits); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) testerStatus(t *testing.T) *tester.Status {
	t.Helper()
	st, err := tester.Load(context.Background(), h.db, h.acc, h.now, false)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (h *harness) subscribers(t *testing.T, prefix string, n int) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO contacts (account_id, email, status, source)
		SELECT $1, $2 || i || '@' || $3, 'subscribed', 'api' FROM generate_series(1, $4) i`,
		h.acc, prefix, map[bool]string{true: mail.SimulatorDomain, false: "example.com"}[strings.HasPrefix(prefix, "success")], n); err != nil {
		t.Fatal(err)
	}
}

func testerLimitProblem(t *testing.T, err error) Problem {
	t.Helper()
	var nr *NotReadyError
	if !errors.As(err, &nr) || len(nr.Problems) != 1 || nr.Problems[0].Code != TesterLimit {
		t.Fatalf("want only a tester_limit problem, got %v (%s)", err, problemCodes(err))
	}
	return nr.Problems[0]
}

// TestTesterLaunchCredits: a launch needing more tester credits than are
// left is refused, when asked for and when confirmed, and queues nothing;
// simulator addresses need none, and a launch within the budget uses it.
func TestTesterLaunchCredits(t *testing.T) {
	h := readyHarness(t)
	h.buy(t, 100)
	h.makeTester(t, 5, h.now.AddDate(0, 0, 30))
	// The ready harness's test to the owner, at h.now, came after the
	// tester's start: one credit used.
	if st := h.testerStatus(t); st.Used != 1 || st.Left != 4 {
		t.Fatalf("status %+v", st)
	}
	h.subscribers(t, "reader", 4)    // with Sam, 5 billable
	h.subscribers(t, "success+", 10) // simulator: free

	_, err := h.launch("")
	p := testerLimitProblem(t, err)
	if p.Details["needed"] != 5 || p.Details["left"] != 4 || p.Details["credits"] != 5 ||
		!strings.Contains(p.Message, "4 of its 5 credits left, and this needs 5") || !strings.Contains(p.Hint, "simulator") {
		t.Errorf("problem %+v", p)
	}

	// Within the budget when asked, short when confirmed (a contact joined
	// in between): refused, nothing queued.
	if _, err := h.db.Exec(context.Background(), `DELETE FROM contacts WHERE account_id = $1 AND email = 'reader4@example.com'`, h.acc); err != nil {
		t.Fatal(err)
	}
	code := h.summary(t).Code
	h.subscribers(t, "late", 1)
	_, err = h.launch(code)
	testerLimitProblem(t, err)
	if n := h.counts(t); len(n) != 0 {
		t.Errorf("refused confirm queued rows: %v", n)
	}

	if _, err := h.db.Exec(context.Background(), `DELETE FROM contacts WHERE account_id = $1 AND email = 'late1@example.com'`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatalf("launch within the budget: %v", err)
	}
	if n := h.counts(t); n["queued"] != 14 {
		t.Errorf("queued %v", n)
	}
	var sim int
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND simulated`, h.campaign.ID).Scan(&sim)
	if sim != 10 {
		t.Errorf("%d simulated rows, want 10", sim)
	}
	if st := h.testerStatus(t); st.Used != 5 || st.Left != 0 {
		t.Errorf("after launch %+v", st)
	}
	// The ledger still debits a credit for every copy, simulator or not.
	if b := h.balance(t); b != 110-14 {
		t.Errorf("balance %d", b)
	}
}

// TestTesterEnded: an ended tester can neither launch nor test, and its
// queued copies wait, as a paused account's do.
func TestTesterEnded(t *testing.T) {
	h := launchedHarness(t, 2)
	ctx := context.Background()
	h.makeTester(t, 500, h.now.Add(time.Hour))
	p := h.pipeline(fastLimits)
	q := &recorded{}
	h.now = h.now.Add(2 * time.Hour)
	dispatch(t, p, q)
	if b := q.take(); len(b) != 0 {
		t.Errorf("dispatched %d batches for an ended tester", len(b))
	}
	if n := h.counts(t); n["queued"] != 3 {
		t.Errorf("counts %v", n)
	}

	_, err := h.test()
	var se *tester.ShortError
	if !errors.As(err, &se) || !se.Status.Ended || !strings.Contains(se.Message(), "ended") {
		t.Errorf("test: %v", err)
	}
	if len(h.fake.Sent()) != 0 {
		t.Error("a test went out")
	}

	name := "Another"
	c, err := h.svc.Campaigns.Create(ctx, h.acc, campaigns.Changes{Name: &name, EmailID: &h.email.ID,
		Recipients: &campaigns.Recipients{All: true}})
	if err != nil {
		t.Fatal(err)
	}
	h.campaign = c
	_, err = h.launch("")
	var nr *NotReadyError
	found := false
	if errors.As(err, &nr) {
		for _, p := range nr.Problems {
			if p.Code == TesterLimit {
				found = p.Details["ended"] == true && strings.Contains(p.Message, "ended")
			}
		}
	}
	if !found {
		t.Errorf("launch after the end: %v (%s)", err, problemCodes(err))
	}
}

// TestTesterTestSends: a tester may test to simulator addresses, which use
// no credits and skip the suppression list; a test needing more credits
// than are left is refused and sends nothing.
func TestTesterTestSends(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.makeTester(t, 2, h.now.AddDate(0, 0, 30))
	sim := "success@" + mail.SimulatorDomain
	bounce := "bounce@" + mail.SimulatorDomain
	if _, err := h.db.Exec(ctx, `INSERT INTO suppressions (account_id, email_hash, reason)
		VALUES ($1::uuid, encode(sha256(convert_to($1::uuid::text, 'UTF8') || '\x00'::bytea || convert_to($2, 'UTF8')), 'hex'), 'bounced')`,
		h.acc, bounce); err != nil {
		t.Fatal(err)
	}
	if _, err := h.test(sim, bounce, "Pat@Home.example"); err != nil {
		t.Fatalf("to the simulator and the owner: %v", err)
	}
	if got := perAddress(h.fake.Sent()); got[sim] != 1 || got[bounce] != 1 || got["pat@home.example"] != 1 {
		t.Errorf("sent %v", got)
	}
	if st := h.testerStatus(t); st.Used != 1 || st.Left != 1 {
		t.Errorf("status %+v", st)
	}
	if n := h.testsCounted(t); n != 3 {
		t.Errorf("%d tests counted toward the daily cap, want 3", n)
	}

	h.fake.Reset()
	_, err := h.test("Pat@Home.example", "sam@patsbakery.com")
	var se *tester.ShortError
	if !errors.As(err, &se) || se.Needed != 2 || se.Status.Left != 1 {
		t.Fatalf("short: %v", err)
	}
	if len(h.fake.Sent()) != 0 || h.testsCounted(t) != 3 {
		t.Error("a refused test was sent or counted")
	}
	// Simulator addresses still pass with one credit left.
	if _, err := h.test(sim); err != nil {
		t.Errorf("simulator with one left: %v", err)
	}

	// Simulator addresses are for testers only: other accounts' tests to
	// them need a verified domain, as before.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET tester_started_at = NULL, tester_until = NULL, tester_credits = NULL WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	var re *RecipientError
	if _, err := h.test(sim); !errors.As(err, &re) {
		t.Errorf("non-tester to the simulator: %v", err)
	}
}

// TestTesterDailyLimit: a tester sends at most 100 copies a day, whatever
// the account's own limit, and says why.
func TestTesterDailyLimit(t *testing.T) {
	h := launchedHarness(t, 1)
	ctx := context.Background()
	limits := Limits{PerSecond: 100, Daily: 10000, NewDaily: 1000}
	got, err := limits.Account(ctx, h.db, h.acc, h.now)
	if err != nil || got.Daily != 10000 || got.Reason != nil {
		t.Fatalf("before: %+v %v", got, err)
	}
	h.makeTester(t, 500, h.now.AddDate(0, 0, 30))
	got, err = limits.Account(ctx, h.db, h.acc, h.now)
	if err != nil || got.Daily != tester.Daily || got.Reason == nil || !strings.Contains(*got.Reason, "Tester accounts send to at most 100") {
		t.Fatalf("tester: %+v %v", got, err)
	}
	// A lower limit of the account's own stays.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET daily_send_limit = 40 WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if got, _ := limits.Account(ctx, h.db, h.acc, h.now); got.Daily != 40 {
		t.Errorf("own lower limit: %+v", got)
	}
}

func TestTesterUnhealthy(t *testing.T) {
	cases := []struct {
		r    Rates
		want bool
	}{
		{Rates{Sent: 5, Complaints: 1}, false},
		{Rates{Sent: 5, Complaints: 2}, true},
		{Rates{Sent: 19, HardBounces: 10}, false}, // too few copies
		{Rates{Sent: 20, HardBounces: 2}, false},  // too few bounces
		{Rates{Sent: 20, HardBounces: 3}, true},
		{Rates{Sent: 150, HardBounces: 3}, false}, // 2% is the limit, not over it
		{Rates{Sent: 149, HardBounces: 3}, true},
	}
	for _, c := range cases {
		if got := c.r.TesterUnhealthy(); got != c.want {
			t.Errorf("%+v: %v, want %v", c.r, got, c.want)
		}
	}
}

// TestTesterPause: two complaints pause a tester's campaign and account,
// far below other accounts' limits; simulator events don't count.
func TestTesterPause(t *testing.T) {
	h := launchedHarness(t, 49)
	ctx := context.Background()
	h.makeTester(t, 500, h.now.AddDate(0, 0, 30))
	h.markSent(t, h.now)
	h.eventsFor(t, "complaint", "abuse", 1, h.now)
	if ap := h.check(t); ap != nil {
		t.Fatalf("paused at one complaint: %+v", ap)
	}
	// A simulator complaint is SES's, not a person's.
	if _, err := h.db.Exec(ctx, `
		INSERT INTO send_events (event_key, account_id, campaign_id, message_id, kind, type, subtype, occurred_at, simulated)
		VALUES ('sim', $1, $2, 'sim', 'campaign', 'complaint', 'abuse', $3, true)`, h.acc, h.campaign.ID, h.now); err != nil {
		t.Fatal(err)
	}
	if ap := h.check(t); ap != nil {
		t.Fatalf("paused on a simulator complaint: %+v", ap)
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM send_events`); err != nil {
		t.Fatal(err)
	}
	h.eventsFor(t, "complaint", "abuse", 2, h.now)
	ap := h.check(t)
	if ap == nil {
		t.Fatal("not paused at two complaints")
	}
	paused, why := h.paused(t)
	if !paused || !strings.Contains(why, "Tester accounts are paused at 2 complaints") {
		t.Errorf("paused %v: %q", paused, why)
	}

	// The same rates on an account that isn't a tester don't pause it.
	h2 := launchedHarness(t, 49)
	h2.markSent(t, h2.now)
	h2.eventsFor(t, "complaint", "abuse", 2, h2.now)
	if ap := h2.check(t); ap != nil {
		t.Errorf("non-tester paused: %+v", ap)
	}
}

// TestTesterPauseOnBounces: 3 hard bounces in 20 copies pause a tester.
func TestTesterPauseOnBounces(t *testing.T) {
	h := launchedHarness(t, 19)
	h.makeTester(t, 500, h.now.AddDate(0, 0, 30))
	h.markSent(t, h.now)
	h.eventsFor(t, "bounce", "permanent/general", 3, h.now)
	if ap := h.check(t); ap == nil {
		t.Fatal("not paused")
	}
	if _, why := h.paused(t); !strings.Contains(why, "limit for tester accounts") {
		t.Errorf("reason %q", why)
	}
}

// endsAt is a mail sender that runs end after its n-th send: a tester
// period that ends while a worker is partway through a batch.
type endsAt struct {
	mail.Fake
	n   int
	end func()
}

func (e *endsAt) Send(ctx context.Context, m mail.Message) (string, error) {
	id, err := e.Fake.Send(ctx, m)
	if e.n--; e.n == 0 {
		e.end()
	}
	return id, err
}

// TestTesterEndsMidCampaign: a batch already on the send queue when a
// tester period ends (MG-107) sends nothing once claimed and hands its rows
// back, as for a paused account; a period that ends partway through a
// batch stops it before its next copy; and once an admin extends the
// period, the rest go out, each once.
func TestTesterEndsMidCampaign(t *testing.T) {
	h := launchedHarness(t, 9)
	ctx := context.Background()
	h.makeTester(t, 500, h.now.Add(time.Hour))
	p := h.pipeline(fastLimits)
	q := &recorded{}
	waiting := func() int {
		t.Helper()
		var n int
		if err := h.db.QueryRow(ctx, `
			SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1 AND status = 'queued' AND enqueued_at IS NULL`,
			h.campaign.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Batched while the period runs, claimed after it ends.
	dispatch(t, p, q)
	batches := q.take()
	if len(batches) != 1 || len(batches[0].IDs) != 10 {
		t.Fatalf("batches %+v", batches)
	}
	h.now = h.now.Add(2 * time.Hour)
	if err := p.HandleBatch(ctx, batches[0], 1); err != nil {
		t.Fatal(err)
	}
	if len(h.fake.Sent()) != 0 || waiting() != 10 {
		t.Fatalf("after the end: sent %d, %d rows handed back (counts %v)", len(h.fake.Sent()), waiting(), h.counts(t))
	}
	dispatch(t, p, q)
	if b := q.take(); len(b) != 0 {
		t.Errorf("batched %d again for an ended tester", len(b))
	}
	if h.status(t) != campaigns.StatusSending {
		t.Errorf("campaign %s, want it still sending", h.status(t))
	}

	// Extended: batched again, and the new end comes after 3 copies of it.
	until := h.now.Add(time.Hour)
	h.makeTester(t, 500, until)
	dispatch(t, p, q)
	batches = q.take()
	if len(batches) != 1 || len(batches[0].IDs) != 10 {
		t.Fatalf("batches after extending %+v", batches)
	}
	e := &endsAt{n: 3, end: func() { h.now = until }}
	p.Mail = e
	if err := p.HandleBatch(ctx, batches[0], 1); err != nil {
		t.Fatal(err)
	}
	if c := h.counts(t); len(e.Sent()) != 3 || c["sent"] != 3 || c["queued"] != 7 || waiting() != 7 {
		t.Fatalf("ended mid-batch: sent %d, counts %v, %d handed back", len(e.Sent()), c, waiting())
	}
	dispatch(t, p, q)
	if b := q.take(); len(b) != 0 {
		t.Errorf("batched %d after the end mid-batch", len(b))
	}

	// Extended again, the other 7 go out, and nobody gets two.
	h.makeTester(t, 500, h.now.Add(time.Hour))
	dispatch(t, p, q)
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	got := perAddress(e.Sent())
	if len(got) != 10 {
		t.Errorf("%d addresses got mail, want 10", len(got))
	}
	for addr, n := range got {
		if n != 1 {
			t.Errorf("%s got %d copies", addr, n)
		}
	}
	dispatch(t, p, q)
	if h.status(t) != campaigns.StatusSent {
		t.Errorf("campaign %s once all went out", h.status(t))
	}
}
