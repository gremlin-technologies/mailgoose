package senders

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// VerifiedFrom is what Resolver needs from the domains service: nil when
// email's domain is a verified domain of the account, or a subdomain of
// one, and domains.ErrFromNotVerified when it isn't.
type VerifiedFrom interface {
	RequireVerifiedFrom(ctx context.Context, accountID, email string) error
}

// Querier is a pool or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// IsVerified reports whether email is a verified sender of the account:
// its owner's own sign-in address, which they proved at sign-in, or one of
// its confirmed sender addresses. Case is ignored.
func IsVerified(ctx context.Context, q Querier, accountID, email string) (bool, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return false, nil
	}
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM users WHERE account_id = $1 AND lower(email) = lower($2))
		    OR EXISTS (SELECT 1 FROM sender_addresses WHERE account_id = $1 AND lower(email) = lower($2) AND confirmed_at IS NOT NULL)`,
		accountID, email).Scan(&ok)
	return ok, err
}

// ReplyToError is an explicit Reply-To that the shared domain can't carry:
// it's neither a verified sender of the account nor on one of its verified
// domains.
type ReplyToError struct{ Address string }

func (e *ReplyToError) Error() string {
	return "the Reply-To " + e.Address + " isn't a verified sender address of the account or on one of its verified domains"
}

// Sender is who a message really goes from.
type Sender struct {
	From    mail.Address
	ReplyTo []mail.Address
	// Shared is true when the message goes from the shared sending domain,
	// as the account's sending slug, because its From address isn't on a
	// verified domain of the account.
	Shared bool
	// Address is the address the email or account asked to send from: the
	// From itself, or, when Shared, the verified sender that replies go to
	// unless the email names its own Reply-To.
	Address string
	// Default is true when neither the email nor the account set a From,
	// so Address is the owner's sign-in address by default (MG-108).
	Default bool
}

// ErrNoFrom is Resolve's refusal of an email with no From, on an account
// with no default From, where no default applies: shared sending is off,
// or the account has a verified domain of its own (MG-108).
var ErrNoFrom = errors.New("there's no From address")

// Resolver works out who a customer's email goes from (MG-85). Launches,
// test sends, signup confirmations and the email check all use it, so they
// agree.
type Resolver struct {
	DB      *pgxpool.Pool
	Domains VerifiedFrom
	// Shared is the shared sending domain (SHARED_SENDING_DOMAIN); empty
	// turns shared sending off, and only a verified domain's From sends.
	Shared string
}

// SharedOn reports whether shared sending is on.
func (r *Resolver) SharedOn() bool { return r != nil && r.Shared != "" }

// SharedAddress is the account's address on the shared domain, or "" when
// shared sending is off.
func (r *Resolver) SharedAddress(slug string) string {
	if !r.SharedOn() || slug == "" {
		return ""
	}
	return slug + "@" + r.Shared
}

// Resolve works out who an email asking to go from from, with the explicit
// Reply-To replyTo ("" for none), really goes from:
//
//   - From a verified domain of the account, it goes as asked.
//   - Otherwise, with shared sending on and from a verified sender of the
//     account, it goes under the account's own name at
//     <sending slug>@<shared domain>, with replies to replyTo, or to from
//     when there's no replyTo. A replyTo has to be a verified sender too, or
//     on a verified domain: a *ReplyToError. The email's own From name is
//     used only on a verified domain (MG-98), so a name like "PayPal
//     Support" can't go out on mailgoose's domain; account names are
//     checked for that at sign-up and rename (package accountname).
//   - Otherwise it's domains.ErrFromNotVerified.
//
// The owner's own address never goes in From unless its domain is verified
// for the account.
//
// An empty from.Email (no From on the email or the account) takes
// DefaultFrom, the owner's sign-in address, and then goes as that address
// would, from the shared domain, with Default set (MG-108). Where no
// default applies it's ErrNoFrom, as before.
func (r *Resolver) Resolve(ctx context.Context, accountID string, from mail.Address, replyTo string) (*Sender, error) {
	replyTo = strings.TrimSpace(replyTo)
	from.Email = strings.TrimSpace(from.Email)
	var defaulted bool
	if from.Email == "" {
		d, err := r.DefaultFrom(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if d == "" {
			return nil, ErrNoFrom
		}
		from.Email, defaulted = d, true
	}
	s, err := r.resolve(ctx, accountID, from, replyTo)
	if s != nil {
		s.Default = defaulted
	}
	return s, err
}

// DefaultFrom is the address an email with no From, on an account with no
// default From, goes as (MG-108): with shared sending on and no verified
// domain on the account, the owner's own sign-in address. It's "" when
// shared sending is off or the account has a verified domain, where a
// missing From is still refused. It's worked out each time the From is
// read, not stored, so adding a domain or turning shared sending off
// takes effect at once.
func (r *Resolver) DefaultFrom(ctx context.Context, accountID string) (string, error) {
	if !r.SharedOn() {
		return "", nil
	}
	var email *string
	err := r.DB.QueryRow(ctx, `
		SELECT CASE WHEN EXISTS (SELECT 1 FROM domains WHERE account_id = $1 AND status = 'verified') THEN NULL
		       ELSE (SELECT email FROM users WHERE account_id = $1 ORDER BY created_at, id LIMIT 1) END`,
		accountID).Scan(&email)
	if err != nil || email == nil {
		return "", err
	}
	return strings.TrimSpace(*email), nil
}

// resolve is Resolve for a From that's set.
func (r *Resolver) resolve(ctx context.Context, accountID string, from mail.Address, replyTo string) (*Sender, error) {
	err := r.Domains.RequireVerifiedFrom(ctx, accountID, from.Email)
	if err == nil {
		s := &Sender{From: from, Address: from.Email}
		if replyTo != "" {
			s.ReplyTo = []mail.Address{{Email: replyTo}}
		}
		return s, nil
	}
	if !errors.Is(err, domains.ErrFromNotVerified) || !r.SharedOn() {
		return nil, err
	}
	ok, err := IsVerified(ctx, r.DB, accountID, from.Email)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domains.ErrFromNotVerified
	}
	if replyTo != "" && !strings.EqualFold(replyTo, from.Email) {
		if err := r.replyToAllowed(ctx, accountID, replyTo); err != nil {
			return nil, err
		}
	}
	var name, slug string
	if err := r.DB.QueryRow(ctx, `SELECT name, sending_slug FROM accounts WHERE id = $1`, accountID).Scan(&name, &slug); err != nil {
		return nil, err
	}
	s := &Sender{Shared: true, Address: from.Email, From: mail.Address{Name: name, Email: r.SharedAddress(slug)}}
	s.ReplyTo = []mail.Address{{Email: from.Email}}
	if replyTo != "" {
		s.ReplyTo = []mail.Address{{Email: replyTo}}
	}
	return s, nil
}

// replyToAllowed is nil when addr is a verified sender of the account or on
// one of its verified domains, and a *ReplyToError otherwise.
func (r *Resolver) replyToAllowed(ctx context.Context, accountID, addr string) error {
	ok, err := IsVerified(ctx, r.DB, accountID, addr)
	if err != nil || ok {
		return err
	}
	err = r.Domains.RequireVerifiedFrom(ctx, accountID, addr)
	if errors.Is(err, domains.ErrFromNotVerified) {
		return &ReplyToError{Address: addr}
	}
	return err
}

// ErrNotAccountSlug is CheckShared's refusal of a From whose local part
// isn't the account's own sending slug.
var ErrNotAccountSlug = errors.New("the From isn't the account's own address on the shared domain")

// ErrNoReplyTo is CheckShared's refusal of a message with no Reply-To: on
// the shared domain, replies always go to an address the account verified.
var ErrNoReplyTo = errors.New("a message from the shared domain needs a verified Reply-To")

// CheckShared is mail.Guard's check of a message on the shared domain: nil
// when local, the From's local part, is the account's own sending slug and
// every Reply-To address, of which there must be at least one, is a
// verified sender of the account or on one of its verified domains.
func (r *Resolver) CheckShared(ctx context.Context, accountID, local string, replyTo []string) error {
	if len(replyTo) == 0 {
		return ErrNoReplyTo
	}
	var slug string
	err := r.DB.QueryRow(ctx, `SELECT sending_slug FROM accounts WHERE id = $1`, accountID).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotAccountSlug
	}
	if err != nil {
		return err
	}
	if !strings.EqualFold(local, slug) {
		return ErrNotAccountSlug
	}
	for _, a := range replyTo {
		if err := r.replyToAllowed(ctx, accountID, a); err != nil {
			return err
		}
	}
	return nil
}

// UnverifiedMessage and UnverifiedHint, with UnverifiedDetails, say what to do
// about a From that's neither on a verified domain nor, with shared sending
// on, a verified sender. With shared sending off they keep the domain-only
// wording. The message names no command, so it reads anywhere; the hint
// names the CLI commands and API routes, and the web app words its own from
// the problem's code and details (MG-100).
func (r *Resolver) UnverifiedMessage(email string) string {
	if r.SharedOn() {
		return fmt.Sprintf("The From address %s isn't on a verified domain of this account, and isn't one of its verified sender addresses.", email)
	}
	return "The From address " + email + " isn't on a verified sending domain of this account."
}

// UnverifiedHint is the CLI commands, and the API routes, that fix
// UnverifiedMessage's problem. With shared sending on, the owner's own
// sign-in address is a fix too: it sends from the shared domain as it is.
func (r *Resolver) UnverifiedHint(email string) string {
	d := domainOf(email)
	if r.SharedOn() {
		return "Send from your own sign-in address, which works as it is; or verify this one with `mailgoose sender-addresses add " + email +
			"` (`POST /v1/sender-addresses`) and the link emailed to it, to send from mailgoose's shared domain with replies to it; or, better for deliverability, verify its domain with `mailgoose domain add " +
			d + "` (`POST /v1/domains`) and the DNS records it gives."
	}
	return "Verify its domain with `mailgoose domain add " + d + "` (`POST /v1/domains`) and the DNS records it gives, or use a From on a verified domain (`mailgoose domain list`)."
}

// UnverifiedDetails are the unverified_from problem's details: the From
// (from_email), and whether shared sending is on (shared_sending), so a
// client can say the fixes in its own words.
func (r *Resolver) UnverifiedDetails(email string) map[string]any {
	return map[string]any{"from_email": email, "shared_sending": r.SharedOn()}
}

// ReplyToHint says what to do about a *ReplyToError.
func ReplyToHint(addr string) string {
	return "Verify it with `mailgoose sender-addresses add " + addr + "`, or clear the email's Reply-To so replies go to the From address."
}

func domainOf(email string) string {
	if at := strings.LastIndexByte(email, '@'); at >= 0 {
		return email[at+1:]
	}
	return email
}
