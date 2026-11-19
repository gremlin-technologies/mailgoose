package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/policy"
)

type acceptance struct {
	version, via string
	at           time.Time
}

func (h *harness) acceptances(t *testing.T, accountID string) []acceptance {
	t.Helper()
	rows, err := h.db.Query(context.Background(), `
		SELECT version, via, accepted_at FROM terms_acceptances WHERE account_id = $1 ORDER BY id`, accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []acceptance
	for rows.Next() {
		var a acceptance
		if err := rows.Scan(&a.version, &a.via, &a.at); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func (h *harness) accountOf(t *testing.T, email string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(context.Background(), `SELECT account_id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestSignUpRecordsTermsVersion checks sign-up records the current version
// with the time and how, and that signing up again while pending adds a
// row rather than overwriting the first (MG-68).
func TestSignUpRecordsTermsVersion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cur := policy.Current().ID

	in := signUp("pat@example.com")
	in.Via = TermsViaCLI
	if err := h.svc.SignUp(ctx, in); err != nil {
		t.Fatal(err)
	}
	acc := h.accountOf(t, "pat@example.com")
	first := h.clk.now()
	got := h.acceptances(t, acc)
	if len(got) != 1 || got[0].version != cur || got[0].via != "cli" || !got[0].at.Equal(first) {
		t.Fatalf("acceptances %+v", got)
	}
	st, err := h.svc.AccountSettings(ctx, acc)
	if err != nil {
		t.Fatal(err)
	}
	if st.TermsVersion == nil || *st.TermsVersion != cur || !st.TermsAcceptedAt.Equal(first) || st.TermsNeeded() {
		t.Errorf("settings terms %v at %v", st.TermsVersion, st.TermsAcceptedAt)
	}

	// Again, from the web app, after the resend wait.
	h.clk.t = h.clk.t.Add(ResendInterval + time.Second)
	in.Via = TermsViaWeb
	if err := h.svc.SignUp(ctx, in); err != nil {
		t.Fatal(err)
	}
	got = h.acceptances(t, acc)
	if len(got) != 2 || got[0].via != "cli" || !got[0].at.Equal(first) || got[1].via != "web" || !got[1].at.Equal(h.clk.now()) {
		t.Fatalf("acceptances after a second sign-up %+v", got)
	}
}

func TestAcceptTerms(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cur := policy.Current().ID
	if err := h.svc.SignUp(ctx, signUp("pat@example.com")); err != nil {
		t.Fatal(err)
	}
	acc := h.accountOf(t, "pat@example.com")
	if got := h.acceptances(t, acc); len(got) != 1 || got[0].via != "web" {
		t.Fatalf("sign-up with no via is the web app's: %+v", got)
	}

	// Only the current version can be accepted.
	for _, v := range []string{"2000-01-01", "", "latest"} {
		_, err := h.svc.AcceptTerms(ctx, acc, v, TermsViaCLI)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Code != "terms_version_not_current" || ve.Field != "version" {
			t.Errorf("version %q: %v", v, err)
		}
	}

	// Accepting the version already accepted adds nothing.
	st, err := h.svc.AcceptTerms(ctx, acc, cur, TermsViaCLI)
	if err != nil || st.TermsNeeded() {
		t.Fatalf("accept again: %v", err)
	}
	if got := h.acceptances(t, acc); len(got) != 1 {
		t.Errorf("accepting the same version again added a row: %+v", got)
	}

	// An account on an older version accepts the new one: a new row, and
	// the account's latest moves on.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET terms_version = '2000-01-01' WHERE id = $1`, acc); err != nil {
		t.Fatal(err)
	}
	st, err = h.svc.AccountSettings(ctx, acc)
	if err != nil || !st.TermsNeeded() {
		t.Fatalf("an older version should need accepting: %v", err)
	}
	h.clk.t = h.clk.t.Add(time.Hour)
	st, err = h.svc.AcceptTerms(ctx, acc, cur, TermsViaAPI)
	if err != nil || st.TermsNeeded() || !st.TermsAcceptedAt.Equal(h.clk.now()) {
		t.Fatalf("accept: %v %+v", err, st)
	}
	got := h.acceptances(t, acc)
	if len(got) != 2 || got[1].version != cur || got[1].via != "api" || !got[1].at.Equal(h.clk.now()) {
		t.Errorf("acceptances %+v", got)
	}

	if _, err := h.svc.AcceptTerms(ctx, "00000000-0000-0000-0000-000000000000", cur, TermsViaWeb); !errors.Is(err, ErrNoAccount) {
		t.Errorf("no account: %v", err)
	}
}
