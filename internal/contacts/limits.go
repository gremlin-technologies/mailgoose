package contacts

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/tester"
)

// FreeContactLimit is the most counted contacts a free account may hold
// (CON-9, BIL-2). Paid accounts have no hard cap; their count sets the
// paid plan's units (MG-40).
const FreeContactLimit = 1000

// FreeFormOverflow is how many counted contacts past FreeContactLimit a
// free account's signup forms keep taking (MG-120): a form never turns a
// subscriber away at the cap, but the account's sends are held until it
// upgrades or is back at the limit. Imports and the API stop at the limit.
const FreeFormOverflow = 100

// LimitError means a change would take a free or tester account over its
// contact limit. Count is the counted contacts before the change. Tester
// says the limit is a tester account's (MG-86).
type LimitError struct {
	Limit  int
	Count  int
	Tester bool
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("contact limit: %d of %d counted contacts", e.Count, e.Limit)
}

// Counted reports whether a contact with this status counts toward the
// account's contact limit and fee: unconfirmed and subscribed do;
// unsubscribed, bounced and complained don't.
func Counted(status string) bool {
	return status == StatusUnconfirmed || status == StatusSubscribed
}

// CountedContacts returns how many of the account's contacts count toward its
// limit and fee.
func (s *Service) CountedContacts(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `
		SELECT count(*) FROM contacts WHERE account_id = $1 AND status IN ('unconfirmed', 'subscribed')`,
		accountID).Scan(&n)
	return n, err
}

// ensureRoom returns a *LimitError when adding more counted contacts to the
// account would take a free account over FreeContactLimit, or a tester
// account over tester.Contacts (MG-86). It locks the
// account row, so concurrent adds to one account wait for each other and
// can't both slip under the limit. Import (MG-17) and form signups (MG-36)
// call it, or lockedCount, in the transaction that adds the contacts.
func ensureRoom(ctx context.Context, tx pgx.Tx, accountID string, adding int) error {
	return ensureRoomUpTo(ctx, tx, accountID, adding, false)
}

// ensureFormRoom is ensureRoom for a signup form (MG-36, MG-120): a free
// account's form takes signups up to FreeFormOverflow past the limit. A
// tester account has no overflow.
func ensureFormRoom(ctx context.Context, tx pgx.Tx, accountID string, adding int) error {
	return ensureRoomUpTo(ctx, tx, accountID, adding, true)
}

func ensureRoomUpTo(ctx context.Context, tx pgx.Tx, accountID string, adding int, form bool) error {
	if adding <= 0 {
		return nil
	}
	l, err := lockedCount(ctx, tx, accountID)
	if err != nil {
		return err
	}
	if !l.capped() {
		return nil
	}
	limit := l.limit
	if form && !l.tester {
		limit += FreeFormOverflow
	}
	if l.count+adding > limit {
		e := l.err()
		e.Limit = limit
		return e
	}
	return nil
}

// contactLimit is an account's counted contacts and its cap.
type contactLimit struct {
	count int
	// limit is the cap; 0 for a paid account, which has none.
	limit  int
	tester bool
}

func (l contactLimit) capped() bool { return l.limit > 0 }

func (l contactLimit) err() *LimitError {
	return &LimitError{Limit: l.limit, Count: l.count, Tester: l.tester}
}

// lockedCount locks the account row and returns its counted contacts and
// cap: FreeContactLimit for a free account, tester.Contacts for a tester
// account on any plan, none for a paid one.
func lockedCount(ctx context.Context, tx pgx.Tx, accountID string) (contactLimit, error) {
	var plan string
	var l contactLimit
	if err := tx.QueryRow(ctx, `SELECT plan, tester_started_at IS NOT NULL FROM accounts WHERE id = $1 FOR UPDATE`,
		accountID).Scan(&plan, &l.tester); err != nil {
		return l, fmt.Errorf("read plan: %w", err)
	}
	switch {
	case l.tester:
		l.limit = tester.Contacts
		if plan == "free" {
			l.limit = min(l.limit, FreeContactLimit)
		}
	case plan == "free":
		l.limit = FreeContactLimit
	default:
		return l, nil
	}
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM contacts WHERE account_id = $1 AND status IN ('unconfirmed', 'subscribed')`,
		accountID).Scan(&l.count)
	return l, err
}
