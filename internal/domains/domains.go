// Package domains manages customers' sending domains (DOM-1 to DOM-4).
//
// Adding a domain creates an Amazon SES identity for it, with Easy DKIM and
// a custom MAIL FROM subdomain, and returns the DNS records the person must
// add. A check looks each record up and asks SES whether the identity is
// verified for sending; a domain is verified when every required record is
// found and SES agrees. The API re-checks pending domains every 5 minutes
// for 72 hours, then marks them failed.
//
// Every campaign and test send must come from a verified domain of its
// account (RequireVerifiedFrom); mailgoose never sends customer mail from a
// shared domain, free plan or not.
package domains

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// Timing of the background checks.
const (
	// CheckEvery is how often a pending domain is re-checked.
	CheckEvery = 5 * time.Minute
	// CheckFor is how long a domain is re-checked before it is marked failed.
	CheckFor = 72 * time.Hour
)

// MailFromPrefix is the subdomain used as each domain's MAIL FROM (the
// envelope sender, where bounces go), so SPF aligns with the From domain.
const MailFromPrefix = "bounce"

// Domain statuses.
const (
	StatusPending  = "pending"
	StatusVerified = "verified"
	StatusFailed   = "failed"
)

// Record check statuses.
const (
	Found   = "found"
	Missing = "missing"
	Wrong   = "wrong"
)

// Record purposes.
const (
	PurposeDKIM     = "dkim"
	PurposeMailFrom = "mail_from_mx"
	PurposeSPF      = "mail_from_spf"
	PurposeDMARC    = "dmarc"
)

// Errors the API maps to responses.
var (
	ErrInvalidName = errors.New("invalid domain name")
	ErrReserved    = errors.New("domain is reserved")
	// ErrExists means this account already has the domain.
	ErrExists = errors.New("domain already added")
	// ErrTaken means another account has the domain, pending or verified.
	ErrTaken = errors.New("domain belongs to another account")
	// ErrUnavailable means SES already has an identity for the name that
	// isn't this account's.
	ErrUnavailable = errors.New("domain already set up in SES elsewhere")
	ErrNotFound    = errors.New("domain not found")
	// ErrFromNotVerified is RequireVerifiedFrom's refusal.
	ErrFromNotVerified = errors.New("from address is not on a verified domain")
)

// SESAPI is the part of the SES v2 client this package uses.
type SESAPI interface {
	CreateEmailIdentity(ctx context.Context, in *sesv2.CreateEmailIdentityInput, opts ...func(*sesv2.Options)) (*sesv2.CreateEmailIdentityOutput, error)
	PutEmailIdentityMailFromAttributes(ctx context.Context, in *sesv2.PutEmailIdentityMailFromAttributesInput, opts ...func(*sesv2.Options)) (*sesv2.PutEmailIdentityMailFromAttributesOutput, error)
	GetEmailIdentity(ctx context.Context, in *sesv2.GetEmailIdentityInput, opts ...func(*sesv2.Options)) (*sesv2.GetEmailIdentityOutput, error)
	DeleteEmailIdentity(ctx context.Context, in *sesv2.DeleteEmailIdentityInput, opts ...func(*sesv2.Options)) (*sesv2.DeleteEmailIdentityOutput, error)
}

// Service manages domains.
type Service struct {
	DB  *pgxpool.Pool
	SES SESAPI
	DNS Resolver
	// Region is the SES Region, which names the MAIL FROM MX host.
	Region string
	// IdentityTags go on every SES identity created; IAM allows each
	// environment to touch only identities tagged with its own name.
	IdentityTags map[string]string
	// ConfigurationSet becomes each identity's default configuration set.
	ConfigurationSet string
	// Reserved names, and their subdomains, can't be added (mailgoose's own).
	Reserved []string
	Now      func() time.Time
	Log      *slog.Logger
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Domain is one sending domain.
type Domain struct {
	ID         string
	AccountID  string
	Name       string
	MailFrom   string
	Status     string
	DKIMTokens []string
	HadDMARC   bool
	// Checks is the last check, one per record; nil before the first.
	Checks []RecordCheck
	// SESStatus is SES's DKIM verification status at the last check, in
	// lower case (pending, success, failed, temporary_failure, not_started,
	// or not_found); empty before the first check.
	SESStatus     string
	CreatedAt     time.Time
	LastCheckedAt *time.Time
	CheckUntil    time.Time
	VerifiedAt    *time.Time
	FailedAt      *time.Time
}

// Record is a DNS record the person adds.
type Record struct {
	Purpose  string
	Type     string
	Name     string
	Value    string
	Priority *int
	// Required records must be found for the domain to verify; the DMARC
	// record is only suggested.
	Required bool
}

// RecordCheck is what one lookup found.
type RecordCheck struct {
	Purpose string   `json:"purpose"`
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Found   []string `json:"found,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

// Records lists the DNS records for d: three DKIM CNAMEs, the MAIL FROM MX
// and SPF TXT, and a DMARC TXT when the domain had none.
func (s *Service) Records(d *Domain) []Record {
	var out []Record
	for _, t := range d.DKIMTokens {
		out = append(out, Record{Purpose: PurposeDKIM, Type: "CNAME", Name: t + "._domainkey." + d.Name,
			Value: t + ".dkim.amazonses.com", Required: true})
	}
	prio := 10
	out = append(out,
		Record{Purpose: PurposeMailFrom, Type: "MX", Name: d.MailFrom, Value: s.feedbackHost(), Priority: &prio, Required: true},
		Record{Purpose: PurposeSPF, Type: "TXT", Name: d.MailFrom, Value: spfValue, Required: true},
	)
	if !d.HadDMARC {
		out = append(out, Record{Purpose: PurposeDMARC, Type: "TXT", Name: "_dmarc." + d.Name, Value: "v=DMARC1; p=none;"})
	}
	return out
}

const spfValue = "v=spf1 include:amazonses.com ~all"

func (s *Service) feedbackHost() string {
	region := s.Region
	if region == "" {
		region = "us-east-2"
	}
	return "feedback-smtp." + region + ".amazonses.com"
}

// NormalizeName returns the ASCII, lower-case form of a domain name, or
// ErrInvalidName. It must be a registrable domain or a subdomain of one, not
// a bare public suffix such as "com" or "co.uk".
func NormalizeName(raw string) (string, error) {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if n == "" || strings.ContainsAny(n, "@/: ") {
		return "", ErrInvalidName
	}
	ascii, err := idna.Lookup.ToASCII(n)
	if err != nil || ascii == "" {
		return "", ErrInvalidName
	}
	// Leave room for the MAIL FROM and DKIM names under it.
	if len(ascii) > 200 || !strings.Contains(ascii, ".") {
		return "", ErrInvalidName
	}
	labels := strings.Split(ascii, ".")
	if _, icann := publicsuffix.PublicSuffix(labels[len(labels)-1]); !icann {
		return "", ErrInvalidName
	}
	if _, err := publicsuffix.EffectiveTLDPlusOne(ascii); err != nil {
		return "", ErrInvalidName
	}
	return ascii, nil
}

func (s *Service) reserved(name string) bool {
	for _, r := range s.Reserved {
		r = strings.ToLower(strings.TrimSuffix(r, "."))
		if r != "" && (name == r || strings.HasSuffix(name, "."+r)) {
			return true
		}
	}
	return false
}

const domainColumns = `id, account_id, name, mail_from, status, dkim_tokens, had_dmarc, checks, ses_status,
	created_at, last_checked_at, check_until, verified_at, failed_at`

func scanDomain(row pgx.Row) (*Domain, error) {
	var d Domain
	var ses *string
	err := row.Scan(&d.ID, &d.AccountID, &d.Name, &d.MailFrom, &d.Status, &d.DKIMTokens, &d.HadDMARC, &d.Checks, &ses,
		&d.CreatedAt, &d.LastCheckedAt, &d.CheckUntil, &d.VerifiedAt, &d.FailedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if ses != nil {
		d.SESStatus = *ses
	}
	return &d, nil
}

// Add adds a domain to accountID: it creates the SES identity and returns
// the domain with the records to add. If another account has the name and
// its checks failed, this account takes it over.
func (s *Service) Add(ctx context.Context, accountID, rawName string) (*Domain, error) {
	name, err := NormalizeName(rawName)
	if err != nil {
		return nil, err
	}
	if s.reserved(name) {
		return nil, ErrReserved
	}
	now := s.now()

	var id string
	takeover := false
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var oldID, oldAccount, oldStatus string
		err := tx.QueryRow(ctx, `SELECT id, account_id, status FROM domains WHERE name = $1 FOR UPDATE`, name).
			Scan(&oldID, &oldAccount, &oldStatus)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case oldAccount == accountID:
			return ErrExists
		case oldStatus != StatusFailed:
			return ErrTaken
		default:
			if _, err := tx.Exec(ctx, `DELETE FROM domains WHERE id = $1`, oldID); err != nil {
				return err
			}
			takeover = true
		}
		return tx.QueryRow(ctx, `
			INSERT INTO domains (account_id, name, mail_from, created_at, check_until)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			accountID, name, MailFromPrefix+"."+name, now, now.Add(CheckFor)).Scan(&id)
	})
	if isUniqueViolation(err) {
		// Another Add of the same name committed between our read and our
		// insert; report it the way a later Add would see it.
		return nil, s.raceLoser(ctx, accountID, name)
	}
	if err != nil {
		return nil, err
	}

	tokens, err := s.createIdentity(ctx, accountID, name, takeover)
	if err != nil {
		if _, derr := s.DB.Exec(ctx, `DELETE FROM domains WHERE id = $1`, id); derr != nil {
			s.log().ErrorContext(ctx, "removing domain after SES failure", "domain", name, "err", derr)
		}
		return nil, err
	}
	hadDMARC := s.hasDMARC(ctx, name)
	if _, err := s.DB.Exec(ctx, `
		UPDATE domains SET dkim_tokens = $2, had_dmarc = $3, next_check_at = $4 WHERE id = $1`,
		id, tokens, hadDMARC, now.Add(CheckEvery)); err != nil {
		return nil, fmt.Errorf("save domain: %w", err)
	}
	return s.Get(ctx, accountID, id)
}

// raceLoser is the error for an Add that lost a race to insert name.
func (s *Service) raceLoser(ctx context.Context, accountID, name string) error {
	var owner string
	err := s.DB.QueryRow(ctx, `SELECT account_id FROM domains WHERE name = $1`, name).Scan(&owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The winner's SES call failed and it removed its row; retrying works.
		return ErrTaken
	case err != nil:
		return err
	case owner == accountID:
		return ErrExists
	default:
		return ErrTaken
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Get returns one domain of accountID.
func (s *Service) Get(ctx context.Context, accountID, id string) (*Domain, error) {
	return scanDomain(s.DB.QueryRow(ctx, `SELECT `+domainColumns+` FROM domains WHERE id = $1 AND account_id = $2`, id, accountID))
}

// List returns accountID's domains in the order added, after afterID, at
// most limit. A non-empty name returns only that domain.
func (s *Service) List(ctx context.Context, accountID, name, afterID string, limit int) ([]*Domain, error) {
	var after, only *string
	if afterID != "" {
		after = &afterID
	}
	if name != "" {
		n, err := NormalizeName(name)
		if err != nil {
			return nil, nil
		}
		only = &n
	}
	rows, err := s.DB.Query(ctx, `
		SELECT `+domainColumns+` FROM domains d
		WHERE d.account_id = $1 AND ($2::text IS NULL OR d.name = $2)
		  AND ($3::uuid IS NULL OR (d.created_at, d.id) > (
		      SELECT a.created_at, a.id FROM domains a WHERE a.id = $3::uuid AND a.account_id = $1))
		ORDER BY d.created_at, d.id
		LIMIT $4`, accountID, only, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}
	defer rows.Close()
	var out []*Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, fmt.Errorf("list domains: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Delete removes a domain and its SES identity.
func (s *Service) Delete(ctx context.Context, accountID, id string) error {
	d, err := s.Get(ctx, accountID, id)
	if err != nil {
		return err
	}
	if err := s.deleteIdentity(ctx, d.Name); err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM domains WHERE id = $1 AND account_id = $2`, id, accountID)
	return err
}

// Verify checks a domain now, for the API's verify endpoint. A domain whose
// background checks failed starts a fresh 72 hours; if SES had given up on
// its DKIM records, the identity is made again, with new DKIM records, and
// recreated is true.
func (s *Service) Verify(ctx context.Context, accountID, id string) (d *Domain, recreated bool, err error) {
	d, err = s.Get(ctx, accountID, id)
	if err != nil {
		return nil, false, err
	}
	if d.Status == StatusVerified {
		// Look again anyway, so the person sees the records as they are.
		d, err = s.checkAndSave(ctx, d, false)
		return d, false, err
	}
	if d.Status == StatusFailed && d.SESStatus == "failed" {
		if err := s.deleteIdentity(ctx, d.Name); err != nil {
			return nil, false, err
		}
		tokens, err := s.createIdentity(ctx, accountID, d.Name, false)
		if err != nil {
			return nil, false, err
		}
		if _, err := s.DB.Exec(ctx, `UPDATE domains SET dkim_tokens = $2 WHERE id = $1`, d.ID, tokens); err != nil {
			return nil, false, err
		}
		d.DKIMTokens = tokens
		recreated = true
	}
	d, err = s.checkAndSave(ctx, d, false)
	return d, recreated, err
}

// RecheckDue checks up to limit pending domains whose next check is due,
// and returns how many it checked. Several API tasks can run it at once:
// each claims its domains by moving their next check forward.
func (s *Service) RecheckDue(ctx context.Context, limit int) (int, error) {
	now := s.now()
	rows, err := s.DB.Query(ctx, `
		UPDATE domains SET next_check_at = $2
		WHERE id IN (
			SELECT id FROM domains
			WHERE status = 'pending' AND next_check_at <= $1
			ORDER BY next_check_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED)
		RETURNING `+domainColumns, now, now.Add(CheckEvery), limit)
	if err != nil {
		return 0, fmt.Errorf("claim due domains: %w", err)
	}
	var due []*Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, d := range due {
		if _, err := s.checkAndSave(ctx, d, true); err != nil {
			s.log().WarnContext(ctx, "domain re-check failed", "domain", d.Name, "err", err)
		}
	}
	return len(due), nil
}

// RecheckLoop runs RecheckDue every interval until ctx ends.
func (s *Service) RecheckLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for {
				n, err := s.RecheckDue(ctx, 20)
				if err != nil {
					s.log().WarnContext(ctx, "domain re-checks failed", "err", err)
				}
				if err != nil || n < 20 {
					break
				}
			}
		}
	}
}

// checkAndSave looks a domain's records up, asks SES about the identity and
// saves the result. background is true for the timed re-checks, which mark
// the domain failed once its 72 hours are up.
func (s *Service) checkAndSave(ctx context.Context, d *Domain, background bool) (*Domain, error) {
	checks := s.checkRecords(ctx, d)
	sesStatus, verifiedForSending := s.sesStatus(ctx, d.Name)
	now := s.now()

	allFound := true
	for i, r := range s.Records(d) {
		if r.Required && checks[i].Status != Found {
			allFound = false
		}
	}

	status := d.Status
	switch {
	case allFound && verifiedForSending:
		status = StatusVerified
	case d.Status == StatusVerified:
		// Stay verified: SES keeps sending until it notices a change itself.
	case background && !now.Before(d.CheckUntil):
		status = StatusFailed
	case !background:
		status = StatusPending
	}

	tag, err := s.DB.Exec(ctx, `
		UPDATE domains SET
			status = $2,
			checks = $3,
			ses_status = $4,
			last_checked_at = $5,
			verified_at = CASE WHEN $2 = 'verified' THEN coalesce(verified_at, $5) ELSE NULL END,
			failed_at = CASE WHEN $2 = 'failed' THEN coalesce(failed_at, $5) ELSE NULL END,
			-- A manual check of a failed domain starts a fresh 72 hours.
			check_until = CASE WHEN status = 'failed' AND $2 = 'pending' THEN $5 + $6::interval ELSE check_until END,
			next_check_at = CASE WHEN $2 = 'pending' THEN $5 + $7::interval ELSE NULL END
		WHERE id = $1`,
		d.ID, status, checks, sesStatus, now, CheckFor, CheckEvery)
	if err != nil {
		return nil, fmt.Errorf("save check: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.Get(ctx, d.AccountID, d.ID)
}

// RequireVerifiedFrom returns ErrFromNotVerified unless email's domain is a
// verified domain of accountID, or a subdomain of one. Test sends and
// launches call it.
func (s *Service) RequireVerifiedFrom(ctx context.Context, accountID, email string) error {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return ErrFromNotVerified
	}
	name, err := NormalizeName(email[at+1:])
	if err != nil {
		return ErrFromNotVerified
	}
	var ok bool
	if err := s.DB.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM domains
			WHERE account_id = $1 AND status = 'verified' AND ($2 = name OR right($2, length(name) + 1) = '.' || name))`,
		accountID, name).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrFromNotVerified
	}
	return nil
}

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
