package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/policy"

	// Time zone names are checked against the embedded IANA database, so the
	// answer doesn't depend on what the container has installed.
	_ "time/tzdata"
)

// Limits on account settings.
const (
	maxFromNameLen      = 100
	maxPostalAddressLen = 500
	maxPostalLines      = 8
)

// ErrNoAccount is an account that doesn't exist.
var ErrNoAccount = errors.New("no such account")

// Settings are an account's editable settings (AUTH-5).
type Settings struct {
	ID            string
	Name          string
	Plan          string
	FromName      *string
	FromEmail     *string
	PostalAddress *string
	TimeZone      string
	CreatedAt     time.Time
	// TermsVersion is the latest version of the terms the account accepted,
	// at TermsAcceptedAt; nil if it never accepted any (MG-68).
	TermsVersion    *string
	TermsAcceptedAt *time.Time
}

// TermsNeeded reports whether the account has yet to accept the current
// version of the terms.
func (s *Settings) TermsNeeded() bool {
	return s.TermsVersion == nil || *s.TermsVersion != policy.Current().ID
}

// MissingForSend names the settings a campaign can't launch without, in the
// order to fix them; empty when nothing is missing. The email check (MG-26)
// reports them and launch (MG-30) refuses while any is missing.
func (s *Settings) MissingForSend() []string {
	var out []string
	if s.PostalAddress == nil {
		out = append(out, "postal_address")
	}
	return out
}

// Field is one setting in an update: Set when the request names it, and
// Value nil when the request clears it.
type Field struct {
	Set   bool
	Value *string
}

// SettingsUpdate is a partial update; fields that aren't Set are left alone.
// Name and TimeZone can't be cleared.
type SettingsUpdate struct {
	Name, FromName, FromEmail, PostalAddress, TimeZone Field
}

// AccountSettings returns the account's settings.
func (s *Service) AccountSettings(ctx context.Context, accountID string) (*Settings, error) {
	var out *Settings
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = readSettings(ctx, tx, accountID, false)
		return err
	})
	return out, err
}

// UpdateAccountSettings validates u, applies it and returns the result. A
// problem with the input is a *ValidationError naming the field.
func (s *Service) UpdateAccountSettings(ctx context.Context, accountID string, u SettingsUpdate) (*Settings, error) {
	if err := normalizeUpdate(&u); err != nil {
		return nil, err
	}
	var out *Settings
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := readSettings(ctx, tx, accountID, true)
		if err != nil {
			return err
		}
		apply := func(f Field, dst **string) {
			if f.Set {
				*dst = f.Value
			}
		}
		apply(u.FromName, &cur.FromName)
		apply(u.FromEmail, &cur.FromEmail)
		apply(u.PostalAddress, &cur.PostalAddress)
		if u.Name.Set {
			cur.Name = *u.Name.Value
		}
		if u.TimeZone.Set {
			cur.TimeZone = *u.TimeZone.Value
		}
		if _, err := tx.Exec(ctx, `
			UPDATE accounts
			SET name = $2, from_name = $3, from_email = $4, postal_address = $5, time_zone = $6, updated_at = now()
			WHERE id = $1`,
			accountID, cur.Name, cur.FromName, cur.FromEmail, cur.PostalAddress, cur.TimeZone); err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

func readSettings(ctx context.Context, tx pgx.Tx, accountID string, lock bool) (*Settings, error) {
	q := `SELECT id, name, plan, from_name, from_email, postal_address, time_zone, created_at, terms_version, terms_accepted_at FROM accounts WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	var st Settings
	err := tx.QueryRow(ctx, q, accountID).Scan(&st.ID, &st.Name, &st.Plan, &st.FromName, &st.FromEmail, &st.PostalAddress, &st.TimeZone, &st.CreatedAt,
		&st.TermsVersion, &st.TermsAcceptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// normalizeUpdate trims and checks every Set field in place.
func normalizeUpdate(u *SettingsUpdate) error {
	if u.Name.Set {
		if u.Name.Value == nil {
			return &ValidationError{Code: "invalid_account_name", Field: "name", Message: "The account name can't be removed."}
		}
		v := strings.TrimSpace(*u.Name.Value)
		if v == "" || utf8.RuneCountInString(v) > maxAccountNameLen || strings.ContainsAny(v, "\r\n") {
			return &ValidationError{Code: "invalid_account_name", Field: "name",
				Message: fmt.Sprintf("Account name must be 1 to %d characters on one line.", maxAccountNameLen)}
		}
		u.Name.Value = &v
	}
	if u.FromName.Set && u.FromName.Value != nil {
		v := strings.TrimSpace(*u.FromName.Value)
		if v == "" || utf8.RuneCountInString(v) > maxFromNameLen || strings.ContainsAny(v, "\r\n\"<>") {
			return &ValidationError{Code: "invalid_from_name", Field: "from_name",
				Message: fmt.Sprintf("From name must be 1 to %d characters on one line, without quotes or angle brackets; send null to remove it.", maxFromNameLen)}
		}
		u.FromName.Value = &v
	}
	if u.FromEmail.Set && u.FromEmail.Value != nil {
		v, err := NormalizeEmail(*u.FromEmail.Value)
		if err != nil {
			return &ValidationError{Code: "invalid_from_email", Field: "from_email",
				Message: "From address must be a valid email address, such as news@example.com; send null to remove it."}
		}
		u.FromEmail.Value = &v
	}
	if u.PostalAddress.Set && u.PostalAddress.Value != nil {
		v, err := normalizePostalAddress(*u.PostalAddress.Value)
		if err != nil {
			return err
		}
		u.PostalAddress.Value = &v
	}
	if u.TimeZone.Set {
		if u.TimeZone.Value == nil || !validTimeZone(*u.TimeZone.Value) {
			return &ValidationError{Code: "invalid_time_zone", Field: "time_zone",
				Message: "Time zone must be an IANA name, such as America/New_York or UTC."}
		}
	}
	return nil
}

// normalizePostalAddress trims each line, drops blank lines and joins them
// with "\n". The result is 1 to maxPostalLines lines and at most
// maxPostalAddressLen characters.
func normalizePostalAddress(raw string) (string, error) {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	v := strings.Join(lines, "\n")
	if v == "" || len(lines) > maxPostalLines || utf8.RuneCountInString(v) > maxPostalAddressLen || strings.ContainsAny(v, "\r<>") {
		return "", &ValidationError{Code: "invalid_postal_address", Field: "postal_address",
			Message: fmt.Sprintf("Postal address must be 1 to %d lines and at most %d characters, with no angle brackets.", maxPostalLines, maxPostalAddressLen)}
	}
	return v, nil
}

// validTimeZone accepts IANA zone names: a name time.LoadLocation knows,
// excluding "" and "Local", which mean the server's own zone.
func validTimeZone(name string) bool {
	if name == "" || name == "Local" || strings.TrimSpace(name) != name {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// inTx runs f in a transaction and commits it if f returns nil.
func (s *Service) inTx(ctx context.Context, f func(pgx.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
