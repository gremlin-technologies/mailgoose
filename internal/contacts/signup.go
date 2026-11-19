package contacts

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// SignUpInput is a signup through a form (FRM-5), for SignUp.
type SignUpInput struct {
	// Email is already normalized.
	Email     string
	FirstName *string
	LastName  *string
	// TagIDs are the form's tags.
	TagIDs []string
	// DoubleOptIn makes a new contact unconfirmed until it confirms.
	DoubleOptIn bool
	Now         time.Time
}

// SignUpResult is what a signup did.
type SignUpResult struct {
	ContactID string
	// Status is the contact's status after the signup.
	Status string
	// Created is true for a new contact.
	Created bool
	// NeedsConfirming is true when only a confirmation can subscribe the
	// contact: it's unconfirmed, or it unsubscribed before.
	NeedsConfirming bool
}

// SignUp adds or finds the contact for a form signup in tx, and puts the
// form's tags on it. A new address is subscribed, or unconfirmed with
// double opt-in. An existing contact keeps its status and any names it has:
// a subscribed one is done, and an unconfirmed or unsubscribed one can only
// be subscribed by confirming (Confirm). A bounced or complained address
// stays as it is. A new counted contact must fit the free contact limit
// (*LimitError).
func SignUp(ctx context.Context, tx pgx.Tx, accountID string, in SignUpInput) (*SignUpResult, error) {
	first, err := normalizeName("first_name", in.FirstName)
	if err != nil {
		return nil, err
	}
	last, err := normalizeName("last_name", in.LastName)
	if err != nil {
		return nil, err
	}
	// The account is locked first, before the contact, as Confirm does,
	// so signups and confirms for one account never wait on each other in
	// a circle. While the account is at its limit every signup is refused
	// alike, new address or not, so the answer doesn't tell a stranger
	// who's on the list.
	if err := ensureRoom(ctx, tx, accountID, 1); err != nil {
		return nil, err
	}
	res := &SignUpResult{}
	// Two signups for the same new address can race: both miss the lock
	// below, and the second insert finds the first's row. It then takes
	// the existing-contact path, once.
	for range 2 {
		err = tx.QueryRow(ctx, `SELECT id, status FROM contacts WHERE account_id = $1 AND email = $2 FOR UPDATE`,
			accountID, in.Email).Scan(&res.ContactID, &res.Status)
		if !errors.Is(err, pgx.ErrNoRows) {
			break
		}
		created, err := insertSignup(ctx, tx, accountID, in, first, last, res)
		if err != nil {
			return nil, err
		}
		if created {
			res.Created = true
			break
		}
	}
	switch {
	case res.Created:
	case err != nil:
		return nil, err
	default:
		// Names fill gaps only: anyone can type an address into a form.
		if _, err := tx.Exec(ctx, `
			UPDATE contacts SET first_name = COALESCE(first_name, $2), last_name = COALESCE(last_name, $3),
				updated_at = CASE WHEN (first_name IS NULL AND $2::text IS NOT NULL) OR (last_name IS NULL AND $3::text IS NOT NULL)
					THEN $4 ELSE updated_at END
			WHERE id = $1`, res.ContactID, first, last, in.Now); err != nil {
			return nil, err
		}
	}
	if len(in.TagIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO contact_tags (contact_id, tag_id, created_at)
			SELECT $1, t.id, $3 FROM tags t WHERE t.account_id = $4 AND t.id::text = ANY($2)
			ON CONFLICT DO NOTHING`, res.ContactID, in.TagIDs, in.Now, accountID); err != nil {
			return nil, err
		}
	}
	res.NeedsConfirming = res.Status == StatusUnconfirmed || res.Status == StatusUnsubscribed
	return res, nil
}

// insertSignup adds the new contact for a signup and reports whether it
// did. false means another signup added the address first; res is left
// for the caller to read the existing contact into.
func insertSignup(ctx context.Context, tx pgx.Tx, accountID string, in SignUpInput, first, last *string, res *SignUpResult) (bool, error) {
	reason, err := takeSuppression(ctx, tx, accountID, in.Email)
	if err != nil {
		return false, err
	}
	status := StatusSubscribed
	switch {
	case reason != "":
		// It unsubscribed, bounced or complained before it was deleted.
		status = reason
	case in.DoubleOptIn:
		status = StatusUnconfirmed
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO contacts (account_id, email, first_name, last_name, status, source, created_at, updated_at, status_changed_at)
		VALUES ($1, $2, $3, $4, $5, 'form', $6, $6, $6)
		ON CONFLICT (account_id, email) DO NOTHING RETURNING id`,
		accountID, in.Email, first, last, status, in.Now).Scan(&res.ContactID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	res.Status = status
	return true, nil
}

// Confirm subscribes a contact that confirmed a form signup, in tx, and
// returns its status afterwards. Only an unconfirmed or unsubscribed
// contact changes; a bounced or complained one doesn't. Becoming counted
// must fit the free contact limit (*LimitError).
func Confirm(ctx context.Context, tx pgx.Tx, accountID, contactID string, now time.Time) (string, error) {
	// The account first, then the contact: SignUp's order.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, accountID); err != nil {
		return "", err
	}
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM contacts WHERE account_id = $1 AND id = $2 FOR UPDATE`,
		accountID, contactID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if status != StatusUnconfirmed && status != StatusUnsubscribed {
		return status, nil
	}
	if !Counted(status) {
		if err := ensureRoom(ctx, tx, accountID, 1); err != nil {
			return "", err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE contacts SET status = 'subscribed', status_changed_at = $2, updated_at = $2 WHERE id = $1`,
		contactID, now); err != nil {
		return "", err
	}
	return StatusSubscribed, nil
}
