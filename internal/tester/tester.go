// Package tester holds the limits of tester accounts (MG-86): accounts of
// people invited to try mailgoose before launch.
//
// A tester account sends real campaigns, to people who agreed to get them,
// within tight limits. It has Credits sends for its life, counted from when
// it became a tester: campaign copies and test sends alike, except copies
// to SES's mailbox simulator, which use none. It sends at most Daily
// campaign copies a day, holds at most Contacts contacts, and is paused
// automatically sooner than other accounts (see sending.Rates.TesterUnhealthy). At
// its end date it stops sending: launches and test sends are refused, and
// copies still queued then wait, as they do for a paused account, until an
// admin extends it or the campaign is cancelled.
//
// Admins make an account a tester with `mailgoose admin tester grant`, which
// also gives it full sending, and end it with `mailgoose admin tester
// revoke`, which puts it back in test mode (MG-80).
package tester

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

const (
	// Credits is a tester account's sends for its life, unless an admin
	// grants another number.
	Credits = 500
	// MaxCredits is the most an admin can grant.
	MaxCredits = 10000
	// Contacts is the most counted contacts a tester account holds,
	// whatever its plan.
	Contacts = 500
	// Daily is the most campaign copies a tester account sends a day.
	Daily = 100
	// DefaultDays is how long a grant lasts when it names no end.
	DefaultDays = 30
	// MaxDays is the longest a grant can last.
	MaxDays = 366
)

// Querier is a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Status is a tester account's state.
type Status struct {
	StartedAt time.Time `json:"started_at"`
	Until     time.Time `json:"until"`
	// Credits is its sends for its life; Used is how many it has used:
	// campaign copies queued, sending or sent, and test sends, since
	// StartedAt, leaving out simulator addresses.
	Credits int `json:"credits"`
	Used    int `json:"used"`
	// Left is Credits less Used, never under 0.
	Left int `json:"left"`
	// Ended says whether Until has passed.
	Ended bool `json:"ended"`
}

// ErrNotTester is an account that isn't a tester.
var ErrNotTester = errors.New("the account isn't a tester account")

// Load reads the account's tester state at now, or returns nil when it
// isn't a tester. With lock, the account row is locked first (FOR UPDATE),
// so two sends at once can't both take the last credits; q must then be a
// transaction.
func Load(ctx context.Context, q Querier, accountID string, now time.Time, lock bool) (*Status, error) {
	sql := `SELECT tester_started_at, tester_until, tester_credits FROM accounts WHERE id = $1`
	if lock {
		sql += ` FOR UPDATE`
	}
	var started, until *time.Time
	var credits *int
	if err := q.QueryRow(ctx, sql, accountID).Scan(&started, &until, &credits); err != nil {
		return nil, err
	}
	if started == nil || until == nil || credits == nil {
		return nil, nil
	}
	s := &Status{StartedAt: started.UTC(), Until: until.UTC(), Credits: *credits, Ended: !now.Before(*until)}
	var err error
	if s.Used, err = Used(ctx, q, accountID, *started); err != nil {
		return nil, err
	}
	s.Left = max(0, s.Credits-s.Used)
	return s, nil
}

// Used counts the account's tester credits used since since: campaign
// copies of campaigns launched since then that are queued, sending or
// sent, and test sends, leaving out simulator addresses. A copy skipped or
// failed gives its credit back, as it gives the account's own credit back.
func Used(ctx context.Context, q Querier, accountID string, since time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM campaign_recipients r JOIN campaigns c ON c.id = r.campaign_id
				WHERE r.account_id = $1 AND c.launched_at >= $2 AND r.status IN ('queued', 'sending', 'sent')
				AND NOT r.simulated)
			+ (SELECT count(*) FROM test_sends WHERE account_id = $1 AND sent_at >= $2 AND NOT simulated)`,
		accountID, since).Scan(&n)
	return n, err
}

// Billable counts the addresses that use tester credits: all but
// simulator addresses.
func Billable(addresses []string) int {
	n := 0
	for _, a := range addresses {
		if !mail.IsSimulator(a) {
			n++
		}
	}
	return n
}

// Line is the state for people: "Tester account: 120 of 500 credits left,
// until Dec 21, 2026." or that it has ended.
func (s *Status) Line() string {
	if s.Ended {
		return fmt.Sprintf("Tester account: ended %s, so it can't send.", s.Until.Format("Jan 2, 2006"))
	}
	return fmt.Sprintf("Tester account: %d of %d credits left, until %s.", s.Left, s.Credits, s.Until.Format("Jan 2, 2006"))
}

// ShortError is a send that needs more tester credits than are left, or a
// tester account that has ended. Nothing was sent or queued.
type ShortError struct {
	Status *Status
	// Needed is the credits the send needs.
	Needed int
}

func (e *ShortError) Error() string {
	if e.Status.Ended {
		return "the tester account has ended"
	}
	return fmt.Sprintf("the send needs %d tester credits and %d are left", e.Needed, e.Status.Left)
}

// Message says what's wrong, for people.
func (e *ShortError) Message() string {
	if e.Status.Ended {
		return fmt.Sprintf("This tester account ended on %s, so it can't send any more.", e.Status.Until.Format("Jan 2, 2006"))
	}
	return fmt.Sprintf("This is a tester account with %d of its %d credits left, and this needs %d. Nothing was sent.",
		e.Status.Left, e.Status.Credits, e.Needed)
}

// Hint says what to do about it.
func (e *ShortError) Hint() string {
	if e.Status.Ended {
		return "Reply to your tester invitation if you'd like to keep testing."
	}
	return "Send to fewer people, or to SES mailbox simulator addresses (such as success@simulator.amazonses.com), " +
		"which use no credits. Reply to your tester invitation if you need more."
}

// Check returns a *ShortError when s can't send needed more credits:
// it has ended, or too few are left. A nil s (not a tester) always passes.
func (s *Status) Check(needed int) error {
	if s == nil {
		return nil
	}
	if s.Ended || needed > s.Left {
		return &ShortError{Status: s, Needed: needed}
	}
	return nil
}
