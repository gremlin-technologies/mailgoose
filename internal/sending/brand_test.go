package sending

import (
	"context"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/render"
)

// The "Sent by mailgoose" footer (MG-119) is shown in test sends, on every
// copy of a free account (and a tester's), not on a paid account's copies;
// it's decided for each copy, so an upgrade mid-send ends it from the next
// copy, and its link isn't click tracked.
func TestBrandFooterOnFreeAccountsCopies(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.svc.AssetsURL = baseURL
	link := render.SignupURL(baseURL)

	// A test send shows what will be sent.
	h.buy(t, 10)
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	if sent := h.fake.Sent(); len(sent) != 1 || !strings.Contains(sent[0].Message.HTML, `href="`+link+`"`) ||
		!strings.Contains(sent[0].Message.Text, "Sent by mailgoose: "+link) {
		t.Fatalf("the test send lacks the footer: %+v", sent)
	}

	h = launchTo(t, h, 1, h.now.AddDate(0, -1, 0))
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	batches := q.take()
	if len(batches) != 1 {
		t.Fatalf("%d batches", len(batches))
	}
	// Two copies in one batch: the account upgrades between them.
	b := batches[0]
	first := Batch{AccountID: b.AccountID, CampaignID: b.CampaignID, IDs: b.IDs[:1]}
	second := Batch{AccountID: b.AccountID, CampaignID: b.CampaignID, IDs: b.IDs[1:]}
	if err := p.HandleBatch(ctx, first, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET plan = 'paid' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if err := p.HandleBatch(ctx, second, 1); err != nil {
		t.Fatal(err)
	}
	sent := h.fake.Sent()
	if len(sent) != 2 {
		t.Fatalf("sent %d", len(sent))
	}
	free, paid := sent[0].Message, sent[1].Message
	if strings.Count(free.HTML, `href="`+link+`"`) != 1 || !strings.Contains(free.Text, "Sent by mailgoose: "+link) {
		t.Errorf("the free copy lacks the footer, untracked:\n%s", free.HTML)
	}
	if strings.Contains(paid.HTML, "Sent by mailgoose") || strings.Contains(paid.Text, "Sent by mailgoose") {
		t.Errorf("the paid copy has the footer:\n%s", paid.HTML)
	}

	// The plan ending brings it back.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET plan = 'free' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if on, err := Branded(ctx, h.db, h.acc); err != nil || !on {
		t.Errorf("free again: branded %v, %v", on, err)
	}
	// A tester account carries it on any plan.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET plan = 'paid', tester_started_at = now(), tester_until = now() + interval '30 days', tester_credits = 500 WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if on, err := Branded(ctx, h.db, h.acc); err != nil || !on {
		t.Errorf("tester: branded %v, %v", on, err)
	}
}
