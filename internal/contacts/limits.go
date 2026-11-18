package contacts

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// FreeContactLimit is the most counted contacts a free account may hold
// (CON-9, BIL-2). Paid accounts have no hard cap; their count feeds the
// monthly fee (MG-40).
const FreeContactLimit = 500

// LimitError means a change would take a free account over its contact
// limit. Count is the counted contacts before the change.
type LimitError struct {
	Limit int
	Count int
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
// account would take a free account over FreeContactLimit. It locks the
// account row, so concurrent adds to one account wait for each other and
// can't both slip under the limit. Import (MG-17) and form signups (MG-36)
// call it too, in the transaction that adds the contacts.
func ensureRoom(ctx context.Context, tx pgx.Tx, accountID string, adding int) error {
	if adding <= 0 {
		return nil
	}
	var plan string
	if err := tx.QueryRow(ctx, `SELECT plan FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&plan); err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	if plan != "free" {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM contacts WHERE account_id = $1 AND status IN ('unconfirmed', 'subscribed')`,
		accountID).Scan(&n); err != nil {
		return err
	}
	if n+adding > FreeContactLimit {
		return &LimitError{Limit: FreeContactLimit, Count: n}
	}
	return nil
}
