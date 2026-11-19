package sending

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

const baseURL = "https://app.mailgoose.example"

type harness struct {
	db       *pgxpool.Pool
	svc      *Service
	fake     *mail.Fake
	now      time.Time
	acc      string
	email    *emails.Email
	campaign *campaigns.Campaign
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	h := &harness{db: db, fake: &mail.Fake{}, now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	h.svc = &Service{
		DB:        db,
		Campaigns: &campaigns.Service{DB: db},
		Emails:    &emails.Service{DB: db},
		Settings:  &auth.Service{DB: db},
		Domains:   &domains.Service{DB: db},
		Mail:      h.fake,
		Now:       func() time.Time { return h.now },
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO accounts (name, from_name, from_email, postal_address)
		VALUES ('Pat''s Bakery', 'Pat''s Bakery', 'hello@patsbakery.com', '12 Main St') RETURNING id`).Scan(&h.acc); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (account_id, email) VALUES ($1, 'Pat@Home.example')`,
		`INSERT INTO domains (account_id, name, mail_from, created_at, check_until, status) VALUES ($1, 'patsbakery.com', 'bounce', now(), now(), 'verified')`,
		`INSERT INTO domains (account_id, name, mail_from, created_at, check_until) VALUES ($1, 'pendingbakery.com', 'bounce', now(), now())`,
		`INSERT INTO contacts (account_id, email, first_name, status, source) VALUES ($1, 'sam@patsbakery.com', 'Sam', 'subscribed', 'api')`,
	} {
		if _, err := db.Exec(ctx, q, h.acc); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	doc, err := emails.ParseDocument(emails.KindBlocks, []byte(`{"settings":{"subject":"Fresh bread, {{first_name|friend}}"},
		"blocks":[{"id":"t","type":"text","text":"Hi {{first_name|there}}"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if h.email, err = h.svc.Emails.Create(ctx, h.acc, "Bread", emails.KindBlocks, doc); err != nil {
		t.Fatal(err)
	}
	name := "Bread week"
	if h.campaign, err = h.svc.Campaigns.Create(ctx, h.acc, campaigns.Changes{Name: &name, EmailID: &h.email.ID,
		Recipients: &campaigns.Recipients{All: true}}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) test(to ...string) (*TestResult, error) {
	return h.svc.Test(context.Background(), h.acc, h.campaign.ID, to, baseURL+"/")
}

func (h *harness) testsCounted(t *testing.T) int {
	t.Helper()
	var n int
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM test_sends WHERE account_id = $1`, h.acc).Scan(&n)
	return n
}

func TestTestSendToOwner(t *testing.T) {
	h := newHarness(t)
	res, err := h.test()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.To) != 1 || res.To[0] != "pat@home.example" || res.SentToday != 1 {
		t.Errorf("result %+v", res)
	}
	sent := h.fake.Sent()
	if len(sent) != 1 {
		t.Fatalf("%d sent", len(sent))
	}
	m := sent[0].Message
	if m.Subject != "[Test] Fresh bread, friend" || m.To.Email != "pat@home.example" ||
		m.From != (mail.Address{Name: "Pat's Bakery", Email: "hello@patsbakery.com"}) ||
		m.Kind != mail.KindTest || m.AccountID != h.acc || m.CampaignID != h.campaign.ID {
		t.Errorf("message %+v", m)
	}
	if !strings.Contains(m.HTML, "Hi there") || !strings.Contains(m.HTML, baseURL+"/u/test") || !strings.Contains(m.Text, "12 Main St") {
		t.Errorf("body:\n%s", m.HTML)
	}
	if len(m.Headers) != 2 || m.Headers[0] != (mail.Header{Name: "List-Unsubscribe", Value: "<" + baseURL + "/u/test>"}) {
		t.Errorf("headers %+v", m.Headers)
	}

	// The campaign records the test and what it was made from; a test isn't
	// an edit, so its version stays.
	c, _ := h.svc.Campaigns.Get(context.Background(), h.acc, h.campaign.ID)
	if c.LastTest == nil || !c.LastTest.At.Equal(h.now) || c.LastTest.Version != 1 || c.LastTest.EmailVersion != h.email.Version ||
		c.Version != 1 || !c.TestedAsIs(h.email.Version) {
		t.Errorf("campaign %+v test %+v", c, c.LastTest)
	}
	if res.Campaign.LastTest == nil || res.Campaign.LastTest.EmailVersion != h.email.Version {
		t.Errorf("result campaign %+v", res.Campaign.LastTest)
	}

	// Editing the email or the campaign leaves the test out of date.
	e, err := h.svc.Emails.Update(context.Background(), h.acc, h.email.ID, h.email.Version, emails.Change{Settings: json.RawMessage(`{"subject":"New"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if c.TestedAsIs(e.Version) {
		t.Error("tested as is after an email edit")
	}
	name := "Renamed"
	c, _ = h.svc.Campaigns.Update(context.Background(), h.acc, h.campaign.ID, campaigns.Changes{Name: &name})
	if c.TestedAsIs(h.email.Version) {
		t.Error("tested as is after a campaign edit")
	}

	// Tests use no credits.
	var credits int
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM credit_transactions WHERE account_id = $1`, h.acc).Scan(&credits)
	if credits != 0 {
		t.Errorf("%d credit transactions", credits)
	}
}

func TestTestRecipients(t *testing.T) {
	h := newHarness(t)
	res, err := h.test("Sam@PatsBakery.com", "sam@patsbakery.com", "pat@home.example", "a@news.patsbakery.com", "b@patsbakery.com", "c@patsbakery.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.To, ",") != "sam@patsbakery.com,pat@home.example,a@news.patsbakery.com,b@patsbakery.com,c@patsbakery.com" {
		t.Errorf("to %v", res.To)
	}
	// A contact's own names fill the merge tags.
	if m := h.fake.Sent()[0].Message; m.Subject != "[Test] Fresh bread, Sam" {
		t.Errorf("subject %q", m.Subject)
	}

	for _, to := range [][]string{
		{"a@patsbakery.com", "b@patsbakery.com", "c@patsbakery.com", "d@patsbakery.com", "e@patsbakery.com", "f@patsbakery.com"},
		{"not an address"},
	} {
		var ie *InvalidError
		if _, err := h.test(to...); !errors.As(err, &ie) || ie.Field != "to" {
			t.Errorf("%v: %v", to, err)
		}
	}
	for _, addr := range []string{"x@gmail.com", "x@pendingbakery.com", "x@notbakery.com"} {
		var re *RecipientError
		if _, err := h.test(addr); !errors.As(err, &re) || re.Address != addr {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if len(h.fake.Sent()) != 5 || h.testsCounted(t) != 5 {
		t.Errorf("refused tests sent or counted: %d %d", len(h.fake.Sent()), h.testsCounted(t))
	}
}

func TestTestLimit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// 48 tests over the last day, the oldest 23 hours ago.
	for i := range 48 {
		at := h.now.Add(-23*time.Hour + time.Duration(i)*time.Minute)
		if _, err := h.db.Exec(ctx, `INSERT INTO test_sends (account_id, sent_at) VALUES ($1, $2)`, h.acc, at); err != nil {
			t.Fatal(err)
		}
	}
	// And one from more than a day ago, which no longer counts.
	if _, err := h.db.Exec(ctx, `INSERT INTO test_sends (account_id, sent_at) VALUES ($1, $2)`, h.acc, h.now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if res, err := h.test("a@patsbakery.com", "b@patsbakery.com"); err != nil || res.SentToday != 50 {
		t.Fatalf("up to the limit: %+v %v", res, err)
	}
	var le *LimitError
	if _, err := h.test(); !errors.As(err, &le) || le.Sent != 50 || le.RetryAfter != time.Hour {
		t.Fatalf("over the limit: %v %+v", err, le)
	}
	// An hour later the oldest has aged out, leaving 49: four more would be
	// three over, so the wait is for the third oldest left (3 minutes).
	h.now = h.now.Add(time.Hour)
	if _, err := h.test("a@patsbakery.com", "b@patsbakery.com", "c@patsbakery.com", "d@patsbakery.com"); !errors.As(err, &le) || le.Sent != 49 || le.RetryAfter != 3*time.Minute {
		t.Fatalf("partly aged out: %v %+v", err, le)
	}
	if _, err := h.test(); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
	if len(h.fake.Sent()) != 3 {
		t.Errorf("%d sent", len(h.fake.Sent()))
	}
}

func TestTestNotReady(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// An unverified From says how to fix it.
	_, err := h.svc.Emails.Update(ctx, h.acc, h.email.ID, h.email.Version, emails.Change{Settings: json.RawMessage(`{"from_email":"hi@pendingbakery.com"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var nr *NotReadyError
	if _, err := h.test(); !errors.As(err, &nr) || len(nr.Problems) != 1 || nr.Problems[0].Code != UnverifiedFrom ||
		!strings.Contains(nr.Problems[0].Message, "hi@pendingbakery.com") || !strings.Contains(nr.Problems[0].Hint, "mailgoose domain add pendingbakery.com") {
		t.Fatalf("unverified: %v %+v", err, nr)
	}

	// Everything missing is listed at once.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET from_email = NULL, postal_address = NULL WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	e, _ := h.svc.Emails.Get(ctx, h.acc, h.email.ID)
	if _, err := h.svc.Emails.Update(ctx, h.acc, h.email.ID, e.Version, emails.Change{Settings: json.RawMessage(`{"from_email":null,"subject":""}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.test(); !errors.As(err, &nr) || codes(nr) != "missing_from,missing_postal_address,missing_subject" {
		t.Fatalf("missing: %v", err)
	}

	// An email deleted from under a draft.
	if err := h.svc.Emails.Delete(ctx, h.acc, h.email.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.test(); !errors.As(err, &nr) || codes(nr) != "no_email" {
		t.Fatalf("no email: %v", err)
	}
	if len(h.fake.Sent()) != 0 || h.testsCounted(t) != 0 {
		t.Error("a refused test was sent or counted")
	}
}

// HTML that stopped compiling (saved before a check was added, say) comes
// back as invalid_html, with the fix, not as a server error.
func TestTestInvalidHTML(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	doc := `{"settings":{"subject":"Menu"},"html":"<html><body><p>Hi</p><script>alert(1)</script><a href=\"{{unsubscribe_url}}\">Unsubscribe</a></body></html>"}`
	if _, err := h.db.Exec(ctx, `UPDATE emails SET kind = 'html', document = $2 WHERE id = $1`, h.email.ID, doc); err != nil {
		t.Fatal(err)
	}
	var nr *NotReadyError
	if _, err := h.test(); !errors.As(err, &nr) || codes(nr) != InvalidHTML ||
		!strings.Contains(nr.Problems[0].Hint, "mailgoose email import-html "+h.email.ID) {
		t.Fatalf("%v %+v", err, nr)
	}
	if len(h.fake.Sent()) != 0 || h.testsCounted(t) != 0 {
		t.Error("a refused test was sent or counted")
	}
}

func codes(e *NotReadyError) string {
	var out []string
	for _, p := range e.Problems {
		out = append(out, p.Code)
	}
	return strings.Join(out, ",")
}

func TestTestStatusAndFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Campaigns.SetStatus(ctx, h.acc, h.campaign.ID, campaigns.StatusSending); err != nil {
		t.Fatal(err)
	}
	var se *campaigns.StatusError
	if _, err := h.test(); !errors.As(err, &se) {
		t.Fatalf("sending campaign: %v", err)
	}
	if _, err := h.svc.Test(ctx, h.acc, "00000000-0000-4000-8000-000000000001", nil, baseURL); !errors.Is(err, campaigns.ErrNotFound) {
		t.Fatalf("no campaign: %v", err)
	}

	// A failed send is reported, still counts, and isn't recorded as the
	// campaign's test.
	h2 := newHarness(t)
	h2.fake.Err = errors.New("ses is down")
	if _, err := h2.test(); err == nil || !strings.Contains(err.Error(), "ses is down") {
		t.Fatalf("failure: %v", err)
	}
	c, _ := h2.svc.Campaigns.Get(ctx, h2.acc, h2.campaign.ID)
	if c.LastTest != nil || h2.testsCounted(t) != 1 {
		t.Errorf("after failure: %+v %d", c.LastTest, h2.testsCounted(t))
	}
}
