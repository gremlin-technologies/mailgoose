package auth

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestDeleteAccount(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	pat := activeUser(t, h, "pat@example.com")
	bob := activeUser(t, h, "bob@example.com")
	accounts := []string{pat.Account.ID, bob.Account.ID}
	for i, id := range accounts {
		if _, err := h.svc.CreateKey(ctx, id, "", "agent", ScopeFull); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{
			`INSERT INTO contacts (account_id, email, status, source) VALUES ($1, 'ann@example.com', 'subscribed', 'manual')`,
			`INSERT INTO suppressions (account_id, email_hash, reason) VALUES ($1, 'h', 'bounced')`,
			`INSERT INTO tags (account_id, name) VALUES ($1, 'vip')`,
			`INSERT INTO emails (account_id, name, kind, document) VALUES ($1, 'Spring', 'html', '{}')`,
			`INSERT INTO domains (account_id, name, mail_from, created_at, check_until) VALUES ($1, 'd` + string(rune('0'+i)) + `.example', 'bounce', now(), now())`,
			`INSERT INTO credit_transactions (account_id, type, amount, expires_at) VALUES ($1, 'grant', 1000, now() + interval '1 month')`,
			`INSERT INTO idempotency_keys (scope, key, request_hash) VALUES ('account:' || $1 || ':user:x', 'k', 'h')`,
		} {
			if _, err := h.db.Exec(ctx, q, id); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}

	if err := h.svc.DeleteAccount(ctx, pat.Account.ID, "pat"); !errors.Is(err, ErrConfirmMismatch) {
		t.Fatalf("wrong confirmation: %v", err)
	}
	ledger := h.count(t, `SELECT count(*) FROM credit_transactions WHERE account_id = $1`, pat.Account.ID)
	var name string
	_ = h.db.QueryRow(ctx, `SELECT name FROM accounts WHERE id = $1`, pat.Account.ID).Scan(&name)
	if err := h.svc.DeleteAccount(ctx, pat.Account.ID, name); err != nil {
		t.Fatal(err)
	}

	for _, table := range AccountTables {
		if n := h.count(t, `SELECT count(*) FROM `+table+` WHERE account_id = $1`, pat.Account.ID); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
		if n := h.count(t, `SELECT count(*) FROM `+table+` WHERE account_id = $1`, bob.Account.ID); n == 0 && table != "device_logins" && table != "imports" {
			t.Errorf("%s: the other account lost its rows", table)
		}
	}
	if n := h.count(t, `SELECT count(*) FROM sessions WHERE user_id = $1`, pat.User.ID); n != 0 {
		t.Error("session left")
	}
	if n := h.count(t, `SELECT count(*) FROM idempotency_keys WHERE scope LIKE 'account:' || $1 || ':%'`, pat.Account.ID); n != 0 {
		t.Error("idempotency keys left")
	}
	if n := h.count(t, `SELECT count(*) FROM credit_transactions WHERE account_id = $1`, pat.Account.ID); n != ledger {
		t.Errorf("credit ledger: %d rows, want all %d kept", n, ledger)
	}
	if n := h.count(t, `SELECT count(*) FROM accounts WHERE id = $1 AND status = 'closed' AND closed_at IS NOT NULL AND name = $2 AND postal_address IS NULL`,
		pat.Account.ID, ClosedAccountName); n != 1 {
		t.Error("account row isn't closed and cleared")
	}

	// It's gone: a second delete finds nothing, and the address can sign up again.
	if err := h.svc.DeleteAccount(ctx, pat.Account.ID, ClosedAccountName); !errors.Is(err, ErrNoAccount) {
		t.Errorf("second delete: %v", err)
	}
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
		t.Errorf("sign up again: %v", err)
	}
}

// TestAccountTablesCoverEveryTable makes sure each table with an account_id
// is either emptied or deliberately kept when an account is deleted.
func TestAccountTablesCoverEveryTable(t *testing.T) {
	h := newHarness(t)
	rows, err := h.db.Query(context.Background(), `
		SELECT table_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND column_name = 'account_id'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(AccountTables, table) && !slices.Contains(KeptAccountTables, table) {
			t.Errorf("table %s has an account_id but isn't in AccountTables or KeptAccountTables", table)
		}
	}
}
