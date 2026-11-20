package sending

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Test mode (MG-80): an account in test mode sends only to its confirmed
// test addresses and its owner's own address.

func (h *harness) testMode(t *testing.T) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET sending_mode = 'test' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
}

// testAddress adds a test address, confirmed or pending.
func (h *harness) testAddress(t *testing.T, email string, confirmed bool) {
	t.Helper()
	q := `INSERT INTO test_addresses (account_id, email, created_at) VALUES ($1, $2, now())`
	if confirmed {
		q = `INSERT INTO test_addresses (account_id, email, confirmed_at, created_at) VALUES ($1, $2, now(), now())`
	}
	if _, err := h.db.Exec(context.Background(), q, h.acc, email); err != nil {
		t.Fatal(err)
	}
}

func testModeProblem(t *testing.T, err error) Problem {
	t.Helper()
	var nr *NotReadyError
	if !errors.As(err, &nr) || len(nr.Problems) != 1 || nr.Problems[0].Code != TestMode {
		t.Fatalf("want only a test_mode problem, got %v (%s)", err, problemCodes(err))
	}
	return nr.Problems[0]
}

// TestTestModeLaunch: a launch reaching anyone but test addresses is
// refused with how many aren't, both when it's asked for and when it's
// confirmed; one reaching only test addresses and the owner launches.
func TestTestModeLaunch(t *testing.T) {
	h := readyHarness(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'qa@example.com', 'subscribed', 'api')`,
		`INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'pat@home.example', 'subscribed', 'api')`,
	} {
		if _, err := h.db.Exec(ctx, q, h.acc); err != nil {
			t.Fatal(err)
		}
	}
	h.testMode(t)
	h.testAddress(t, "qa@example.com", false) // pending: not yet allowed

	_, err := h.launch("")
	p := testModeProblem(t, err)
	if !strings.Contains(p.Message, "test mode") || !strings.Contains(p.Message, "2 of the 3 recipients aren't test addresses") ||
		p.Details["recipients"] != 3 || p.Details["not_test_addresses"] != 2 {
		t.Errorf("problem = %+v", p)
	}
	if _, err := h.db.Exec(ctx, `UPDATE test_addresses SET confirmed_at = now() WHERE account_id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	_, err = h.launch("")
	if p := testModeProblem(t, err); !strings.Contains(p.Message, "1 of the 3 recipients isn't a test address") {
		t.Errorf("problem = %+v", p)
	}
	if n := h.counts(t); len(n) != 0 {
		t.Errorf("refused launch queued rows: %v", n)
	}

	// Sam becomes a test address too (case doesn't matter); the owner's own
	// address needs none.
	h.testAddress(t, "SAM@PatsBakery.com", true)
	code := h.summary(t).Code
	// Removed between the summary and the confirm: refused at the confirm.
	if _, err := h.db.Exec(ctx, `DELETE FROM test_addresses WHERE account_id = $1 AND email = 'qa@example.com'`, h.acc); err != nil {
		t.Fatal(err)
	}
	_, err = h.launch(code)
	testModeProblem(t, err)
	if n := h.counts(t); len(n) != 0 {
		t.Errorf("refused confirm queued rows: %v", n)
	}
	h.testAddress(t, "qa@example.com", true)
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatalf("launch to test addresses: %v", err)
	}
	if n := h.counts(t); n["queued"] != 3 {
		t.Errorf("launched rows: %v", n)
	}
}

// TestTestModeWaitingForReview: a test-mode account waiting for its sign-up
// review may still launch to its test addresses; a full-sending one may not.
func TestTestModeWaitingForReview(t *testing.T) {
	h := readyHarness(t)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = 'waiting', review_at = now() WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(""); problemCodes(err) != AccountInReview {
		t.Fatalf("full sending, waiting: %v (%s)", err, problemCodes(err))
	}
	h.testMode(t)
	h.testAddress(t, "sam@patsbakery.com", true)
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatalf("test mode, waiting, to test addresses: %v", err)
	}
}

// TestTestModeTestSends: in test mode, test sends go only to the owner and
// confirmed test addresses, which need no verified domain.
func TestTestModeTestSends(t *testing.T) {
	h := newHarness(t)
	h.testMode(t)
	h.testAddress(t, "qa@example.com", true)
	h.testAddress(t, "pending@example.com", false)
	if _, err := h.test(); err != nil {
		t.Fatalf("to the owner: %v", err)
	}
	if _, err := h.test("QA@example.com"); err != nil {
		t.Fatalf("to a confirmed test address: %v", err)
	}
	for _, to := range []string{"pending@example.com", "sam@patsbakery.com", "someone@example.com"} {
		before := len(h.fake.Sent())
		_, err := h.test(to)
		var tm *TestModeError
		if !errors.As(err, &tm) || tm.Address != to || !strings.Contains(err.Error(), "test mode") {
			t.Errorf("%s: %v", to, err)
		}
		if len(h.fake.Sent()) != before {
			t.Errorf("%s: a copy went out", to)
		}
	}
}

// TestPipelineSkipsOutsideTestMode: copies queued before an account was put
// in test mode go only to its test addresses; the rest are skipped with
// their credits back.
func TestPipelineSkipsOutsideTestMode(t *testing.T) {
	h := launchedHarness(t, 2)
	ctx := context.Background()
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	h.testMode(t)
	h.testAddress(t, "Reader1@example.com", true)
	before := h.balance(t)
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	if got := perAddress(h.fake.Sent()); len(got) != 1 || got["reader1@example.com"] != 1 {
		t.Errorf("sent to %v", got)
	}
	if c := h.counts(t); c["sent"] != 1 || c["skipped"] != 2 {
		t.Errorf("counts %v", c)
	}
	if h.balance(t) != before+2 {
		t.Errorf("balance %d, want %d: two credits back", h.balance(t), before+2)
	}
	var why string
	if err := h.db.QueryRow(ctx, `SELECT r.error FROM campaign_recipients r JOIN contacts c ON c.id = r.contact_id
		WHERE r.campaign_id = $1 AND c.email = 'sam@patsbakery.com'`,
		h.campaign.ID).Scan(&why); err != nil || !strings.Contains(why, "test mode") {
		t.Errorf("skip reason %q, %v", why, err)
	}
}
