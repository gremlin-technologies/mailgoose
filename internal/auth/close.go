package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrConfirmMismatch means the confirmation given isn't the account's name.
var ErrConfirmMismatch = errors.New("the confirmation doesn't match the account's name")

// ClosedAccountName replaces a deleted account's name.
const ClosedAccountName = "Deleted account"

// AccountTables lists every table holding an account's data, in the order
// DeleteAccount empties them (children before parents). Each has an
// account_id column. Rows in tables that hang off these (sessions, sign-in
// codes, contact tags, form and campaign tags, import skips) go with them by ON DELETE
// CASCADE.
var AccountTables = []string{
	"account_throttles",
	"engagement_events",
	"campaign_reports",
	"send_events",
	"unsubscribes",
	"unsubscribe_keys",
	"test_sends",
	"send_queue",
	"campaign_recipients",
	"campaign_launch_codes",
	"campaigns",
	"consents",
	"forms",
	"emails",
	"images",
	"import_address_hashes",
	"imports",
	"contacts",
	"tags",
	"suppressions",
	"domains",
	"device_logins",
	"api_keys",
	"signup_ips",
	"users",
}

// KeptAccountTables lists the tables with an account_id that DeleteAccount
// leaves alone: the billing records the law requires us to keep, the admin
// log (what staff did to the account, and why) and which terms it accepted.
// A test checks every table with an account_id is in one list or the other,
// so a new table can't be forgotten.
var KeptAccountTables = []string{
	"credit_transactions",
	"admin_actions",
	"terms_acceptances",
}

// DeleteAccount deletes an account and everything in it (AUTH-6), when
// confirm is the account's name exactly. Contacts, emails, domains, keys,
// sessions and the person's sign-in go now; the account row stays, closed,
// with its name and settings cleared, so the credit ledger keeps its owner.
// Sending domains' Amazon SES identities, and uploaded image files, are the
// caller's to remove first.
func (s *Service) DeleteAccount(ctx context.Context, accountID, confirm string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var name, status string
		err := tx.QueryRow(ctx, `SELECT name, status FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&name, &status)
		if errors.Is(err, pgx.ErrNoRows) || status == "closed" {
			return ErrNoAccount
		}
		if err != nil {
			return err
		}
		if confirm != name {
			return ErrConfirmMismatch
		}
		for _, table := range AccountTables {
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE account_id = $1`, accountID); err != nil {
				return fmt.Errorf("delete %s: %w", table, err)
			}
		}
		// Stored replies to the account's requests can hold its data.
		if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE scope LIKE $1`, "account:"+accountID+":%"); err != nil {
			return fmt.Errorf("delete idempotency keys: %w", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE accounts SET status = 'closed', closed_at = $2, updated_at = $2, name = $3,
				from_name = NULL, from_email = NULL, postal_address = NULL
			WHERE id = $1`, accountID, s.now(), ClosedAccountName)
		return err
	})
}
