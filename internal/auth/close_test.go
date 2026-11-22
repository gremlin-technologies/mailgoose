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
			`INSERT INTO forms (account_id, name, fields, button_text, success_message, confirm_subject, confirm_text, signing_key)
				VALUES ($1, 'Footer', '[]', 'Go', 'Thanks', 'Confirm', 'Please', '\x00')`,
			`INSERT INTO campaigns (account_id, name, all_contacts) VALUES ($1, 'Spring', true)`,
			`INSERT INTO unsubscribe_keys (account_id, key) VALUES ($1, decode(repeat('ab', 32), 'hex'))`,
			`INSERT INTO unsubscribes (account_id, source) VALUES ($1, 'link')`,
			`INSERT INTO test_sends (account_id) VALUES ($1)`,
			`INSERT INTO consents (account_id, contact_id, form_id, ip, wording) SELECT $1, k.id, f.id, '192.0.2.1', '{}' FROM contacts k, forms f WHERE k.account_id = $1 AND f.account_id = $1`,
			`INSERT INTO send_batch_timings (account_id, campaign_id, started_at, finished_at, batch_size, sent, skipped, failed)
				SELECT $1, id, now(), now(), 1, 1, 0, 0 FROM campaigns WHERE account_id = $1`,
			`INSERT INTO send_queue (account_id, campaign_id, run_at) SELECT $1, id, now() FROM campaigns WHERE account_id = $1`,
			`INSERT INTO campaign_recipients (account_id, campaign_id, contact_id) SELECT $1, c.id, k.id FROM campaigns c, contacts k WHERE c.account_id = $1 AND k.account_id = $1`,
			`INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, occurred_at)
				SELECT 'k' || account_id, account_id, campaign_id, id, 'm', 'campaign', 'delivery', now() FROM campaign_recipients WHERE account_id = $1`,
			`INSERT INTO campaign_reports (campaign_id, account_id, report) SELECT id, account_id, '{}' FROM campaigns WHERE account_id = $1`,
			`INSERT INTO engagement_events (account_id, campaign_id, recipient_id, type) SELECT account_id, campaign_id, id, 'open' FROM campaign_recipients WHERE account_id = $1`,
			`INSERT INTO campaign_launch_codes (campaign_id, account_id, code_hash, campaign_version, email_version, expires_at) SELECT id, $1, '\x00', 1, 1, now() FROM campaigns WHERE account_id = $1`,
			`INSERT INTO domains (account_id, name, mail_from, created_at, check_until) VALUES ($1, 'd` + string(rune('0'+i)) + `.example', 'bounce', now(), now())`,
			`INSERT INTO images (account_id, key, content_type, bytes) SELECT id, id::text || '/a.png', 'image/png', 1 FROM accounts WHERE id = $1`,
			`INSERT INTO account_throttles (account_id, campaign_id, reason, until) SELECT $1, id, 'Early warning', now() + interval '7 days' FROM campaigns WHERE account_id = $1`,
			`INSERT INTO credit_transactions (account_id, type, amount, expires_at) VALUES ($1, 'grant', 1000, now() + interval '1 month')`,
			`INSERT INTO signup_ips (account_id, ip, created_at) VALUES ($1, '192.0.2.1', now())`,
			`INSERT INTO test_addresses (account_id, email, created_at) VALUES ($1, 'tester@example.com', now())`,
			`INSERT INTO test_address_emails (account_id, sent_at) VALUES ($1, now())`,
			`INSERT INTO sender_addresses (account_id, email, created_at) VALUES ($1, 'sender@example.com', now())`,
			`INSERT INTO sender_address_emails (account_id, sent_at) VALUES ($1, now())`,
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
		if n := h.count(t, `SELECT count(*) FROM `+table+` WHERE account_id = $1`, bob.Account.ID); n == 0 && table != "device_logins" && table != "imports" && table != "import_address_hashes" && table != "mailchimp_bundles" {
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
