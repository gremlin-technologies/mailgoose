package sending

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/render"
	"github.com/gremlin-technologies/mailgoose/internal/tester"
	"github.com/gremlin-technologies/mailgoose/internal/testmode"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

// Limits on test sends (CAM-2).
const (
	// MaxTestRecipients is how many addresses one test can go to.
	MaxTestRecipients = 5
	// TestsPerDay is how many test messages an account can send in any 24
	// hours.
	TestsPerDay = 50
	// TestSubjectPrefix starts every test's subject.
	TestSubjectPrefix = "[Test] "
)

// TestUnsubscribePath is where a test's unsubscribe link and header point:
// a page that explains it's a test and changes nothing. Real tokens start
// "u1", so it can't be mistaken for one.
const TestUnsubscribePath = unsubscribe.PathPrefix + "test"

// InvalidError is a request Test refuses before sending anything.
type InvalidError struct {
	Field   string
	Message string
}

func (e *InvalidError) Error() string { return e.Field + ": " + e.Message }

// RecipientError names a test address that isn't allowed.
type RecipientError struct {
	Address string
}

// SuppressedError names a test address that unsubscribed, bounced or
// complained, or was deleted after one of those. Tests don't go to it.
type SuppressedError struct {
	Address string
	// Status is the contact's status, or "" for an address only on the
	// suppression list.
	Status string
}

func (e *SuppressedError) Error() string {
	return e.Address + " is suppressed, so tests don't go to it"
}

func (e *RecipientError) Error() string {
	return e.Address + " isn't the account owner's address or on a verified sending domain of the account"
}

// TestModeError names a test address refused because the account is in
// test mode (MG-80) and it's neither the owner's address nor a confirmed
// test address.
type TestModeError struct {
	Address string
}

func (e *TestModeError) Error() string {
	return "the account is in test mode, and " + e.Address + " isn't one of its confirmed test addresses"
}

// LimitError means the test would go over TestsPerDay; RetryAfter is when
// enough of the day's tests have aged out.
type LimitError struct {
	Sent       int
	RetryAfter time.Duration
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%d test emails sent in the last 24 hours; the limit is %d", e.Sent, TestsPerDay)
}

// SendError means the mail provider refused a test copy or didn't answer.
// Copies before it went out.
type SendError struct {
	Address string
	Err     error
}

func (e *SendError) Error() string { return "send test to " + e.Address + ": " + e.Err.Error() }

func (e *SendError) Unwrap() error { return e.Err }

// TestResult is what a test send did.
type TestResult struct {
	Campaign *campaigns.Campaign
	// To is who each copy went to, in order.
	To []string
	// SentToday is the account's test messages in the last 24 hours, this
	// test's included.
	SentToday int
	// From and ReplyTo are who the copies went from. SharedDomain is true
	// when that's the shared sending domain (MG-85), and Sender is then the
	// verified address the email was set to go from.
	From         mail.Address
	ReplyTo      []mail.Address
	SharedDomain bool
	Sender       string
}

// Test sends one copy of the campaign's email to each address in to, or to
// the account owner when to is empty. Each copy's subject starts
// "[Test] "; no credits are used. baseURL is the app's public URL, for the
// test's unsubscribe link. The campaign then records the test and the
// versions it was made from.
func (s *Service) Test(ctx context.Context, accountID, campaignID string, to []string, baseURL string) (*TestResult, error) {
	owner, err := s.ownerEmail(ctx, accountID)
	if err != nil {
		return nil, err
	}
	st, err := tester.Load(ctx, s.DB, accountID, s.now(), false)
	if err != nil {
		return nil, err
	}
	recipients, err := s.testRecipients(ctx, accountID, owner, to, st != nil)
	if err != nil {
		return nil, err
	}
	// A tester account that has ended can't test (MG-86); its credits are
	// checked again when the test is counted, below.
	if st != nil && st.Ended {
		return nil, st.Check(0)
	}
	// A campaign that's sending or sent can't be tested, whatever else is
	// wrong with it.
	c, err := s.Campaigns.Get(ctx, accountID, campaignID)
	if err != nil {
		return nil, err
	}
	if !campaigns.Editable(c.Status) {
		return nil, &campaigns.StatusError{Status: c.Status}
	}
	p, err := s.Prepare(ctx, accountID, campaignID)
	if err != nil {
		return nil, err
	}
	// A link on the phishing or malware lists stops the test too (MG-64).
	// Links that couldn't be looked up pass, as in the check.
	unsubURL := strings.TrimRight(baseURL, "/") + TestUnsubscribePath
	sample := s.testRecipient(ctx, accountID, recipients[0])
	sample.UnsubscribeURL = unsubURL
	if errs, _ := emailcheck.Safety(ctx, p.Template.Personalize(sample).HTML, s.LinkSafety, unsubURL); len(errs) > 0 {
		problems := make([]Problem, len(errs))
		for i, e := range errs {
			problems[i] = unsafeProblem(e)
		}
		return nil, &NotReadyError{Problems: problems}
	}
	sentToday, err := s.reserveTests(ctx, accountID, campaignID, recipients)
	if err != nil {
		return nil, err
	}

	for _, addr := range recipients {
		r := s.testRecipient(ctx, accountID, addr)
		r.UnsubscribeURL = unsubURL
		m := p.Template.Personalize(r)
		msg := mail.Message{
			From:       p.From,
			To:         mail.Address{Email: addr},
			ReplyTo:    p.ReplyTo,
			Subject:    TestSubjectPrefix + m.Subject,
			HTML:       m.HTML,
			Text:       m.Text,
			Kind:       mail.KindTest,
			AccountID:  accountID,
			CampaignID: campaignID,
		}
		for _, h := range m.Headers {
			msg.Headers = append(msg.Headers, mail.Header{Name: h.Name, Value: h.Value})
		}
		if _, err := s.Mail.Send(ctx, msg); err != nil {
			return nil, &SendError{Address: addr, Err: err}
		}
	}

	t := campaigns.Test{At: s.now(), Version: p.Campaign.Version, EmailVersion: p.Email.Version}
	if err := s.Campaigns.RecordTest(ctx, accountID, campaignID, t); err != nil {
		return nil, err
	}
	p.Campaign.LastTest = &t
	return &TestResult{Campaign: p.Campaign, To: recipients, SentToday: sentToday,
		From: p.From, ReplyTo: p.ReplyTo, SharedDomain: p.Shared, Sender: p.Sender}, nil
}

func (s *Service) ownerEmail(ctx context.Context, accountID string) (string, error) {
	var email string
	err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE account_id = $1`, accountID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", campaigns.ErrNotFound
	}
	return strings.ToLower(email), err
}

// testRecipients checks the addresses a test may go to: the owner's, or
// any on a verified domain of the account. In test mode (MG-80) they're the
// owner's and the account's confirmed test addresses instead, whatever
// their domain. A tester account (MG-86) may also test to SES mailbox
// simulator addresses, which reach no one, whatever they did before.
// Repeats count once.
func (s *Service) testRecipients(ctx context.Context, accountID, owner string, to []string, isTester bool) ([]string, error) {
	if len(to) == 0 {
		return []string{owner}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range to {
		addr, err := contacts.NormalizeEmail(raw)
		if err != nil {
			return nil, &InvalidError{Field: "to", Message: fmt.Sprintf("%q isn't an email address", raw)}
		}
		if seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, addr)
	}
	if len(out) > MaxTestRecipients {
		return nil, &InvalidError{Field: "to", Message: fmt.Sprintf("a test goes to at most %d addresses", MaxTestRecipients)}
	}
	gate, err := testmode.Load(ctx, s.DB, accountID)
	if err != nil {
		return nil, err
	}
	for _, addr := range out {
		if addr == owner || isTester && mail.IsSimulator(addr) {
			continue
		}
		if gate.Test() {
			if !gate.Allows(addr) {
				return nil, &TestModeError{Address: addr}
			}
			if err := s.notSuppressed(ctx, accountID, addr); err != nil {
				return nil, err
			}
			continue
		}
		err := s.Domains.RequireVerifiedFrom(ctx, accountID, addr)
		if errors.Is(err, domains.ErrFromNotVerified) {
			return nil, &RecipientError{Address: addr}
		}
		if err != nil {
			return nil, err
		}
		if err := s.notSuppressed(ctx, accountID, addr); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// notSuppressed refuses an address that unsubscribed, bounced or
// complained, as a contact or on the suppression list a deleted contact
// leaves. A test there would cost reputation like any other send. The
// owner's own address is never checked: it's where tests go by default.
func (s *Service) notSuppressed(ctx context.Context, accountID, addr string) error {
	var status string
	err := s.DB.QueryRow(ctx, `SELECT status FROM contacts WHERE account_id = $1 AND email = $2`, accountID, addr).Scan(&status)
	switch {
	case err == nil:
		if contacts.Suppressing(status) {
			return &SuppressedError{Address: addr, Status: status}
		}
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	suppressed, err := (&contacts.Service{DB: s.DB}).IsSuppressed(ctx, accountID, addr)
	if err != nil {
		return err
	}
	if suppressed {
		return &SuppressedError{Address: addr}
	}
	return nil
}

// testRecipient fills a test's merge tags: from the contact with that
// address when there is one, so the test looks as it will for them.
func (s *Service) testRecipient(ctx context.Context, accountID, addr string) render.Recipient {
	r := render.Recipient{Email: addr}
	var first, last *string
	if err := s.DB.QueryRow(ctx, `SELECT first_name, last_name FROM contacts WHERE account_id = $1 AND email = $2`,
		accountID, addr).Scan(&first, &last); err == nil {
		r.FirstName, r.LastName = trimmed(first), trimmed(last)
	}
	return r
}

// reserveTests counts a test message to each of recipients against the
// account's day, or returns a *LimitError when they'd go over. A tester
// account's (MG-86) also count against its credits, but for simulator
// addresses, or it gets a *tester.ShortError when too few are left. The
// account row is locked, so two tests at once can't both take the last
// places. A reserved test that then fails to send still counts.
func (s *Service) reserveTests(ctx context.Context, accountID, campaignID string, recipients []string) (int, error) {
	n := len(recipients)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	now := s.now()
	st, err := tester.Load(ctx, tx, accountID, now, true)
	if err != nil {
		return 0, err
	}
	if err := st.Check(tester.Billable(recipients)); err != nil {
		return 0, err
	}
	since := now.Add(-24 * time.Hour)
	var sent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM test_sends WHERE account_id = $1 AND sent_at > $2`,
		accountID, since).Scan(&sent); err != nil {
		return 0, err
	}
	if sent+n > TestsPerDay {
		// Wait until enough have aged out: the (sent+n-TestsPerDay)th
		// oldest in the window.
		var at time.Time
		if err := tx.QueryRow(ctx, `
			SELECT sent_at FROM test_sends WHERE account_id = $1 AND sent_at > $2
			ORDER BY sent_at OFFSET $3 LIMIT 1`, accountID, since, sent+n-TestsPerDay-1).Scan(&at); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
		retry := at.Add(24 * time.Hour).Sub(now)
		if at.IsZero() || retry < time.Second {
			retry = time.Second
		}
		return 0, &LimitError{Sent: sent, RetryAfter: retry}
	}
	simulated := make([]bool, n)
	for i, a := range recipients {
		simulated[i] = mail.IsSimulator(a)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO test_sends (account_id, campaign_id, sent_at, simulated)
		SELECT $1, $2, $3, s FROM unnest($4::boolean[]) AS s`, accountID, campaignID, now, simulated); err != nil {
		return 0, err
	}
	return sent + n, tx.Commit(ctx)
}
