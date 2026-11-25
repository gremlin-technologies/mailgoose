package sending

import (
	"context"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

// TestFieldTags: contact fields' merge tags (MG-133) fill in from each
// recipient's values in test sends and campaign sends, and take their
// fallback, or nothing, without one.
func TestFieldTags(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO contact_fields (account_id, key, name, created_at, updated_at) VALUES ($1, 'company', 'Company', now(), now()), ($1, 'plan', 'Plan', now(), now())`,
		`UPDATE contacts SET fields = '{"company":"Acme & Co","plan":"Pro"}' WHERE account_id = $1 AND email = 'sam@patsbakery.com'`,
		`INSERT INTO contacts (account_id, email, status, source, fields) VALUES ($1, 'lee@patsbakery.com', 'subscribed', 'api', '{"plan":"Free"}')`,
	} {
		if _, err := h.db.Exec(ctx, q, h.acc); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	doc, err := emails.ParseDocument(emails.KindBlocks, []byte(`{"settings":{"subject":"News for {{company|your team}}"},
		"blocks":[{"id":"t","type":"text","text":"Hi {{first_name|there}}, plan [{{plan}}]."}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Emails.Replace(ctx, h.acc, h.email.ID, h.email.Version, doc); err != nil {
		t.Fatal(err)
	}

	// The owner's address isn't a contact: fallbacks, or nothing. Sam's is.
	if _, err := h.test("pat@home.example", "sam@patsbakery.com"); err != nil {
		t.Fatal(err)
	}
	sent := h.fake.Sent()
	if len(sent) != 2 {
		t.Fatalf("%d tests sent", len(sent))
	}
	if m := sent[0].Message; m.Subject != "[Test] News for your team" || !strings.Contains(m.HTML, "Hi there, plan [].") {
		t.Errorf("owner's test: %q\n%s", m.Subject, m.Text)
	}
	if m := sent[1].Message; m.Subject != "[Test] News for Acme & Co" || !strings.Contains(m.HTML, "Hi Sam, plan [Pro].") {
		t.Errorf("Sam's test: %q\n%s", m.Subject, m.Text)
	}

	// The campaign's copies take each recipient's own values.
	h.buy(t, 10)
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatal(err)
	}
	h.fake.Reset()
	p := h.pipeline(fastLimits)
	q := &recorded{}
	dispatch(t, p, q)
	for _, b := range q.take() {
		if err := p.HandleBatch(ctx, b, 1); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]string{}
	for _, s := range h.fake.Sent() {
		got[s.Message.To.Email] = s.Message.Subject + " | " + s.Message.Text
	}
	if g := got["sam@patsbakery.com"]; !strings.HasPrefix(g, "News for Acme & Co | ") || !strings.Contains(g, "Hi Sam, plan [Pro].") {
		t.Errorf("Sam's copy: %q", g)
	}
	if g := got["lee@patsbakery.com"]; !strings.HasPrefix(g, "News for your team | ") || !strings.Contains(g, "Hi there, plan [Free].") {
		t.Errorf("Lee's copy: %q", g)
	}
}
