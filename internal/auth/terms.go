package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/policy"
)

// How the terms were accepted, as terms_acceptances.via records it (MG-68).
const (
	// TermsViaWeb is sign-up or the prompt in the web app.
	TermsViaWeb = "web"
	// TermsViaCLI is the CLI: its sign-up, or accept-terms.
	TermsViaCLI = "cli"
	// TermsViaAPI is an API key that isn't the CLI's.
	TermsViaAPI = "api"
)

// recordTerms adds an acceptance to the account's history. The history is
// never changed or overwritten; accounts.terms_version is the latest.
func recordTerms(ctx context.Context, tx pgx.Tx, accountID, version, via string, at time.Time) error {
	switch via {
	case TermsViaWeb, TermsViaCLI, TermsViaAPI:
	default:
		via = TermsViaWeb
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO terms_acceptances (account_id, version, accepted_at, via) VALUES ($1, $2, $3, $4)`,
		accountID, version, at, via); err != nil {
		return fmt.Errorf("record terms acceptance: %w", err)
	}
	return nil
}

// AcceptTerms records that the account accepts version, which must be the
// current version of the terms (a *ValidationError otherwise), and returns
// the account's settings. Accepting the version already accepted changes
// nothing.
func (s *Service) AcceptTerms(ctx context.Context, accountID, version, via string) (*Settings, error) {
	cur := policy.Current()
	if version != cur.ID {
		msg := fmt.Sprintf("The current terms are version %s, so version %q can't be accepted.", cur.ID, version)
		if !policy.Known(version) {
			msg = fmt.Sprintf("There's no version %q of the terms; the current one is %s.", version, cur.ID)
		}
		return nil, &ValidationError{Code: "terms_version_not_current", Field: "version", Message: msg}
	}
	var out *Settings
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		st, err := readSettings(ctx, tx, accountID, true)
		if err != nil {
			return err
		}
		if !st.TermsNeeded() {
			out = st
			return nil
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE accounts SET terms_version = $2, terms_accepted_at = $3, updated_at = $3 WHERE id = $1`,
			accountID, version, now); err != nil {
			return err
		}
		if err := recordTerms(ctx, tx, accountID, version, via, now); err != nil {
			return err
		}
		st.TermsVersion, st.TermsAcceptedAt = &version, &now
		out = st
		return nil
	})
	return out, err
}
