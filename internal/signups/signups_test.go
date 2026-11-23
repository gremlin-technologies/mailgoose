package signups

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

const baseURL = "https://app.mailgoose.test"

type harness struct {
	db   *pgxpool.Pool
	svc  *Service
	fake *mail.Fake
	now  time.Time
	acc  string
	form *forms.Form
	tag  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	h := &harness{db: db, fake: &mail.Fake{}, now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	h.svc = &Service{
		DB:       db,
		Forms:    &forms.Service{DB: db},
		Settings: &auth.Service{DB: db},
		Domains:  &domains.Service{DB: db},
		Mail:     h.fake,
		Now:      func() time.Time { return h.now },
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO accounts (name, status, from_email) VALUES ('Pat''s Bakery', 'active', 'hello@patsbakery.com') RETURNING id`).Scan(&h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO domains (account_id, name, mail_from, created_at, check_until, status) VALUES ($1, 'patsbakery.com', 'bounce', now(), now(), 'verified')`, h.acc); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO tags (account_id, name) VALUES ($1, 'newsletter') RETURNING id`, h.acc).Scan(&h.tag); err != nil {
		t.Fatal(err)
	}
	name := "Footer"
	fields := []forms.Field{{Name: forms.FieldEmail, Required: true}, {Name: forms.FieldFirstName, Label: "First name"}}
	tags := []string{h.tag}
	var err error
	if h.form, err = h.svc.Forms.Create(ctx, h.acc, forms.Changes{Name: &name, Fields: &fields, TagIDs: &tags}); err != nil {
		t.Fatal(err)
	}
	return h
}

// post is a person's submission: stamped by the form and four seconds
// after the page opened.
func (h *harness) post(email string) Submission {
	elapsed := 4 * time.Second
	return Submission{Email: email, FirstName: "Ann", Stamp: forms.Stamp(h.form.SigningKey, h.form.ID, h.now.Add(-time.Hour)),
		Elapsed: &elapsed, IP: "192.0.2.10"}
}

func (h *harness) submit(sub Submission) (*Result, error) {
	return h.svc.Submit(context.Background(), h.form.ID, sub, baseURL+"/")
}

func (h *harness) contact(t *testing.T, email string) (status string, tags int) {
	t.Helper()
	err := h.db.QueryRow(context.Background(), `
		SELECT status, (SELECT count(*) FROM contact_tags ct WHERE ct.contact_id = c.id) FROM contacts c WHERE account_id = $1 AND email = $2`,
		h.acc, email).Scan(&status, &tags)
	if err != nil {
		t.Fatalf("contact %s: %v", email, err)
	}
	return status, tags
}

// confirmToken is the token in the last confirmation email.
func (h *harness) confirmToken(t *testing.T) string {
	t.Helper()
	sent, ok := h.fake.Last()
	if !ok {
		t.Fatal("no email sent")
	}
	i := strings.Index(sent.Message.Text, baseURL+ConfirmPrefix)
	if i < 0 {
		t.Fatalf("no confirm link in %q", sent.Message.Text)
	}
	rest := sent.Message.Text[i+len(baseURL+ConfirmPrefix):]
	return strings.Fields(rest)[0]
}

func (h *harness) counts(t *testing.T) (signups, confirms int) {
	t.Helper()
	f, err := h.svc.Forms.Get(context.Background(), h.acc, h.form.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f.Signups, f.Confirmations
}

func TestDoubleOptIn(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	res, err := h.submit(h.post("Ann@Example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped || !res.ConfirmSent {
		t.Fatalf("result %+v", res)
	}
	if status, tags := h.contact(t, "ann@example.com"); status != contacts.StatusUnconfirmed || tags != 1 {
		t.Errorf("contact %s with %d tags", status, tags)
	}
	sent := h.fake.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d", len(sent))
	}
	m := sent[0].Message
	if m.Kind != mail.KindConfirm || m.From.Email != "hello@patsbakery.com" || m.From.Name != "Pat's Bakery" ||
		m.To.Email != "ann@example.com" || m.Subject != forms.DefaultConfirmSubject || !strings.Contains(m.HTML, "Confirm my subscription") {
		t.Errorf("message %+v", m)
	}
	// No credits are used.
	var ledger int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM credit_transactions WHERE account_id = $1`, h.acc).Scan(&ledger)
	if ledger != 0 {
		t.Errorf("%d ledger rows", ledger)
	}

	// The consent record.
	var ip string
	var w wording
	var raw []byte
	var formID *string
	var confirmed *time.Time
	if err := h.db.QueryRow(ctx, `SELECT ip, wording, form_id, confirmed_at FROM consents WHERE account_id = $1`, h.acc).
		Scan(&ip, &raw, &formID, &confirmed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	if ip != "192.0.2.10" || formID == nil || *formID != h.form.ID || confirmed != nil || w.FormName != "Footer" ||
		strings.Join(w.Fields, ",") != "Email,First name" || w.ButtonText != forms.DefaultButtonText || w.ConfirmText != forms.DefaultConfirmText {
		t.Errorf("consent %s %v %v %+v", ip, formID, confirmed, w)
	}

	// Opening the link changes nothing; confirming subscribes.
	token := h.confirmToken(t)
	p, err := h.svc.Lookup(ctx, token)
	if err != nil || p.Confirmed || p.Email != "ann@example.com" || p.AccountName != "Pat's Bakery" {
		t.Fatalf("lookup %+v %v", p, err)
	}
	if status, _ := h.contact(t, "ann@example.com"); status != contacts.StatusUnconfirmed {
		t.Errorf("after lookup: %s", status)
	}
	h.now = h.now.Add(time.Hour)
	if p, err = h.svc.Confirm(ctx, token); err != nil || !p.Confirmed {
		t.Fatalf("confirm %+v %v", p, err)
	}
	if status, _ := h.contact(t, "ann@example.com"); status != contacts.StatusSubscribed {
		t.Errorf("after confirm: %s", status)
	}
	if err := h.db.QueryRow(ctx, `SELECT confirmed_at FROM consents WHERE account_id = $1`, h.acc).Scan(&confirmed); err != nil || confirmed == nil || !confirmed.Equal(h.now) {
		t.Errorf("confirmed_at %v %v", confirmed, err)
	}
	// Again: still confirmed, counted once.
	if p, err = h.svc.Confirm(ctx, token); err != nil || !p.Confirmed {
		t.Errorf("again %+v %v", p, err)
	}
	if s, c := h.counts(t); s != 1 || c != 1 {
		t.Errorf("counts %d %d", s, c)
	}

	// Signing up again when subscribed adds the form's tags and sends
	// nothing.
	if _, err := h.db.Exec(ctx, `DELETE FROM contact_tags`); err != nil {
		t.Fatal(err)
	}
	h.fake.Reset()
	if res, err := h.submit(h.post("ann@example.com")); err != nil || res.ConfirmSent {
		t.Fatalf("again: %+v %v", res, err)
	}
	if status, tags := h.contact(t, "ann@example.com"); status != contacts.StatusSubscribed || tags != 1 || len(h.fake.Sent()) != 0 {
		t.Errorf("again: %s %d tags, %d sent", status, tags, len(h.fake.Sent()))
	}
	if s, _ := h.counts(t); s != 2 {
		t.Errorf("signups %d", s)
	}
}

func TestWithoutDoubleOptIn(t *testing.T) {
	h := newHarness(t)
	off := false
	var err error
	if h.form, err = h.svc.Forms.Update(context.Background(), h.acc, h.form.ID, forms.Changes{DoubleOptIn: &off}); err != nil {
		t.Fatal(err)
	}
	if res, err := h.submit(h.post("ann@example.com")); err != nil || res.ConfirmSent {
		t.Fatalf("%+v %v", res, err)
	}
	if status, _ := h.contact(t, "ann@example.com"); status != contacts.StatusSubscribed {
		t.Errorf("status %s", status)
	}
	// An address that unsubscribed comes back only by confirming, even
	// through a form without double opt-in.
	if _, err := h.db.Exec(context.Background(), `UPDATE contacts SET status = 'unsubscribed'`); err != nil {
		t.Fatal(err)
	}
	if res, err := h.submit(h.post("ann@example.com")); err != nil || !res.ConfirmSent {
		t.Fatalf("unsubscribed: %+v %v", res, err)
	}
	if status, _ := h.contact(t, "ann@example.com"); status != contacts.StatusUnsubscribed {
		t.Errorf("before confirming: %s", status)
	}
	if _, err := h.svc.Confirm(context.Background(), h.confirmToken(t)); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.contact(t, "ann@example.com"); status != contacts.StatusSubscribed {
		t.Errorf("after confirming: %s", status)
	}
}

func TestUnsubscribedComesBackThroughConfirmation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO contacts (account_id, email, first_name, status, source) VALUES ($1, 'ann@example.com', 'Annie', 'unsubscribed', 'api')`,
		`INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'bob@example.com', 'bounced', 'api')`,
	} {
		if _, err := h.db.Exec(ctx, q, h.acc); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := h.submit(h.post("ann@example.com")); err != nil || !res.ConfirmSent {
		t.Fatalf("%+v %v", res, err)
	}
	// Still unsubscribed until confirmed, and the name it had stays.
	var first string
	_ = h.db.QueryRow(ctx, `SELECT first_name FROM contacts WHERE email = 'ann@example.com'`).Scan(&first)
	if status, _ := h.contact(t, "ann@example.com"); status != contacts.StatusUnsubscribed || first != "Annie" {
		t.Errorf("%s %s", status, first)
	}
	if _, err := h.svc.Confirm(ctx, h.confirmToken(t)); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.contact(t, "ann@example.com"); status != contacts.StatusSubscribed {
		t.Errorf("confirmed: %s", status)
	}

	// A bounced address gets no email and stays bounced.
	h.fake.Reset()
	if res, err := h.submit(h.post("bob@example.com")); err != nil || res.ConfirmSent || len(h.fake.Sent()) != 0 {
		t.Errorf("bounced: %+v %v", res, err)
	}
	if status, _ := h.contact(t, "bob@example.com"); status != contacts.StatusBounced {
		t.Errorf("bounced: %s", status)
	}

	// A deleted contact that unsubscribed keeps that through its
	// suppression entry, and needs confirming too.
	if _, err := h.db.Exec(ctx, `INSERT INTO suppressions (account_id, email_hash, reason) VALUES ($1::uuid, encode(sha256(convert_to($1::uuid::text, 'UTF8') || '\x00'::bytea || convert_to('cat@example.com', 'UTF8')), 'hex'), 'unsubscribed')`, h.acc); err != nil {
		t.Fatal(err)
	}
	if res, err := h.submit(h.post("cat@example.com")); err != nil || !res.ConfirmSent {
		t.Fatalf("suppressed: %+v %v", res, err)
	}
	if status, _ := h.contact(t, "cat@example.com"); status != contacts.StatusUnsubscribed {
		t.Errorf("suppressed: %s", status)
	}
}

func TestBots(t *testing.T) {
	h := newHarness(t)
	fast := 900 * time.Millisecond
	for name, sub := range map[string]Submission{
		"honeypot": func() Submission { s := h.post("ann@example.com"); s.Honeypot = "https://spam.example"; return s }(),
		"too fast": func() Submission { s := h.post("ann@example.com"); s.Elapsed = &fast; return s }(),
	} {
		res, err := h.submit(sub)
		if err != nil || !res.Dropped {
			t.Errorf("%s: %+v %v", name, res, err)
		}
	}
	var n int
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM contacts`).Scan(&n)
	if n != 0 || len(h.fake.Sent()) != 0 {
		t.Errorf("%d contacts, %d sent", n, len(h.fake.Sent()))
	}
	if s, _ := h.counts(t); s != 0 {
		t.Errorf("signups %d", s)
	}

	// Without the script there's no time to check: honeypot and rate
	// limits do the work.
	sub := h.post("ann@example.com")
	sub.Elapsed = nil
	if res, err := h.submit(sub); err != nil || res.Dropped {
		t.Errorf("no script: %+v %v", res, err)
	}
}

func TestRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for name, tc := range map[string]struct {
		sub  func() Submission
		want func(error) bool
	}{
		"no stamp":  {func() Submission { s := h.post("ann@example.com"); s.Stamp = ""; return s }, is(ErrBadStamp)},
		"forged":    {func() Submission { s := h.post("ann@example.com"); s.Stamp = "1700000000.abc"; return s }, is(ErrBadStamp)},
		"bad email": {func() Submission { return h.post("ann@") }, invalidField(forms.FieldEmail)},
		"no email":  {func() Submission { return h.post(" ") }, invalidField(forms.FieldEmail)},
		"long name": {func() Submission { s := h.post("ann@example.com"); s.FirstName = strings.Repeat("a", 101); return s }, invalidField(forms.FieldFirstName)},
		"provider down": {func() Submission { h.fake.Err = errors.New("down"); return h.post("dan@example.com") }, func(err error) bool {
			var se *SendError
			return errors.As(err, &se)
		}},
	} {
		_, err := h.submit(tc.sub())
		if !tc.want(err) {
			t.Errorf("%s: %v", name, err)
		}
		h.fake.Err = nil
	}
	if _, err := h.svc.Submit(ctx, "00000000-0000-0000-0000-000000000000", h.post("ann@example.com"), baseURL); !errors.Is(err, ErrFormNotFound) {
		t.Errorf("unknown form: %v", err)
	}

	var err error
	// A draft form from a Mailchimp import (MG-76) takes nothing until it's
	// saved, and says why.
	if _, err := h.db.Exec(ctx, `UPDATE forms SET draft = true WHERE id = $1`, h.form.ID); err != nil {
		t.Fatal(err)
	}
	if h.form, err = h.svc.Forms.Get(ctx, h.acc, h.form.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("ann@example.com")); !errors.Is(err, ErrFormDraft) {
		t.Errorf("draft: %v", err)
	}
	if ps, err := h.svc.Problems(ctx, h.form); err != nil || len(ps) != 1 || ps[0].Code != "draft" {
		t.Errorf("draft problems %+v %v", ps, err)
	}
	if h.form, err = h.svc.Forms.Update(ctx, h.acc, h.form.ID, forms.Changes{}); err != nil || h.form.Draft {
		t.Fatalf("saved draft = %+v, %v", h.form, err)
	}

	// A required name.
	fields := []forms.Field{{Name: forms.FieldEmail, Required: true}, {Name: forms.FieldFirstName, Label: "First name", Required: true}}
	if h.form, err = h.svc.Forms.Update(ctx, h.acc, h.form.ID, forms.Changes{Fields: &fields}); err != nil {
		t.Fatal(err)
	}
	sub := h.post("ann@example.com")
	sub.FirstName = ""
	if _, err := h.submit(sub); !invalidField(forms.FieldFirstName)(err) {
		t.Errorf("required name: %v", err)
	}

	// Double opt-in without a verified From: refused, and the form says why.
	if _, err := h.db.Exec(ctx, `UPDATE domains SET status = 'pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("ann@example.com")); !errors.Is(err, ErrNotReady) {
		t.Errorf("unverified: %v", err)
	}
	if ps, err := h.svc.Problems(ctx, h.form); err != nil || len(ps) != 1 || ps[0].Code != "no_verified_from" {
		t.Errorf("problems %+v %v", ps, err)
	}

	// A suspended account's form takes nothing (MG-65), before checking
	// anything else.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET suspended_at = now(), suspended_reason = 'Spam.', suspended_by = 'test'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("ann@example.com")); !errors.Is(err, ErrFormClosed) {
		t.Errorf("suspended: %v", err)
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET suspended_at = NULL, suspended_reason = NULL, suspended_by = NULL`); err != nil {
		t.Fatal(err)
	}

	// Nor does one waiting for its sign-up review (MG-66).
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = 'waiting', review_at = now()`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("ann@example.com")); !errors.Is(err, ErrFormClosed) {
		t.Errorf("waiting for review: %v", err)
	}
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET review = 'approved'`); err != nil {
		t.Fatal(err)
	}

	// A closed account's form is gone.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET status = 'closed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(h.post("ann@example.com")); !errors.Is(err, ErrFormNotFound) {
		t.Errorf("closed: %v", err)
	}
}

func TestContactLimit(t *testing.T) {
	h := newHarness(t)
	if _, err := h.db.Exec(context.Background(), `
		INSERT INTO contacts (account_id, email, status, source)
		SELECT $1, 'c' || i || '@example.com', 'subscribed', 'api' FROM generate_series(1, $2) i`, h.acc, contacts.FreeContactLimit+contacts.FreeFormOverflow); err != nil {
		t.Fatal(err)
	}
	var le *contacts.LimitError
	if _, err := h.submit(h.post("ann@example.com")); !errors.As(err, &le) {
		t.Errorf("over the limit: %v", err)
	}
	// While the list is full an existing contact is refused alike, so the
	// answer doesn't show who's on the list; its tags are left as they are.
	if _, err := h.submit(h.post("c1@example.com")); !errors.As(err, &le) {
		t.Errorf("existing: %v", err)
	}
	if _, tags := h.contact(t, "c1@example.com"); tags != 0 {
		t.Errorf("existing contact got %d tags", tags)
	}
}

// Posts for the same new address at once (a double click, or a bot from
// several IPs) add one contact and send one confirmation email.
func TestConcurrentSignups(t *testing.T) {
	h := newHarness(t)
	const n = 8
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := h.submit(h.post("ann@example.com"))
			errs <- err
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	var contactsN, consents int
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM contacts WHERE account_id = $1`, h.acc).Scan(&contactsN)
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM consents WHERE account_id = $1`, h.acc).Scan(&consents)
	if contactsN != 1 || consents != n || len(h.fake.Sent()) != 1 {
		t.Errorf("%d contacts, %d consents, %d emails", contactsN, consents, len(h.fake.Sent()))
	}
}

// A confirmation email that fails to send doesn't hold back the next try
// for an hour.
func TestFailedSendCanRetry(t *testing.T) {
	h := newHarness(t)
	h.fake.Err = errors.New("ses is down")
	var se *SendError
	if _, err := h.submit(h.post("ann@example.com")); !errors.As(err, &se) {
		t.Fatalf("%v", err)
	}
	var stamped int
	_ = h.db.QueryRow(context.Background(), `SELECT count(*) FROM consents WHERE account_id = $1 AND confirm_sent_at IS NOT NULL`, h.acc).Scan(&stamped)
	if stamped != 0 {
		t.Errorf("%d consents stamped as sent", stamped)
	}
	h.fake.Err = nil
	h.now = h.now.Add(time.Minute)
	res, err := h.submit(h.post("ann@example.com"))
	if err != nil || !res.ConfirmSent || len(h.fake.Sent()) != 1 {
		t.Errorf("retry: %+v %v, %d sent", res, err, len(h.fake.Sent()))
	}
	// And the hour holds after a send that worked.
	h.now = h.now.Add(time.Minute)
	if res, err := h.submit(h.post("ann@example.com")); err != nil || res.ConfirmSent {
		t.Errorf("third: %+v %v", res, err)
	}
}

// A contact that bounced after signing up still has its click recorded,
// but it stays bounced and the form doesn't count a confirmation.
func TestConfirmBouncedNotCounted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.submit(h.post("ann@example.com")); err != nil {
		t.Fatal(err)
	}
	token := h.confirmToken(t)
	if _, err := h.db.Exec(ctx, `UPDATE contacts SET status = 'bounced' WHERE account_id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Confirm(ctx, token); err != nil {
		t.Fatal(err)
	}
	status, _ := h.contact(t, "ann@example.com")
	var clicked int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM consents WHERE account_id = $1 AND confirmed_at IS NOT NULL`, h.acc).Scan(&clicked)
	if _, confirms := h.counts(t); status != contacts.StatusBounced || confirms != 0 || clicked != 1 {
		t.Errorf("status %s, %d confirmations counted, %d clicks recorded", status, confirms, clicked)
	}
}

func TestConfirmLinks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.submit(h.post("ann@example.com")); err != nil {
		t.Fatal(err)
	}
	token := h.confirmToken(t)

	// A second signup within the hour sends nothing more.
	h.fake.Reset()
	if res, err := h.submit(h.post("ann@example.com")); err != nil || res.ConfirmSent || len(h.fake.Sent()) != 0 {
		t.Errorf("resend within the hour: %+v %v", res, err)
	}
	// After it, it does, and the first link still works.
	h.now = h.now.Add(ResendGap)
	if res, err := h.submit(h.post("ann@example.com")); err != nil || !res.ConfirmSent {
		t.Errorf("resend after the hour: %+v %v", res, err)
	}

	for name, tc := range map[string]struct {
		token string
		want  error
	}{
		"garbage":   {"nope", ErrBadToken},
		"cut short": {token[:20], ErrBadToken},
		"unknown":   {tokenPrefix + strings.Repeat("A", 43), ErrNotFound},
	} {
		if _, err := h.svc.Confirm(ctx, tc.token); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	h.now = h.now.Add(ConfirmTTL)
	if _, err := h.svc.Confirm(ctx, token); !errors.Is(err, ErrExpired) {
		t.Errorf("expired: %v", err)
	}
	if status, _ := h.contact(t, "ann@example.com"); status != contacts.StatusUnconfirmed {
		t.Errorf("status %s", status)
	}

	// Deleting the contact deletes its records and links.
	if _, err := h.db.Exec(ctx, `DELETE FROM contacts`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Lookup(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted: %v", err)
	}
}

func is(target error) func(error) bool { return func(err error) bool { return errors.Is(err, target) } }

func invalidField(field string) func(error) bool {
	return func(err error) bool {
		var ie *InvalidError
		return errors.As(err, &ie) && ie.Field == field
	}
}
