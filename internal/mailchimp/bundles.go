package mailchimp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Uploaded exports (MG-72). An upload is stored as it came, read again to
// show its preview and on confirm, and removed once its import has used it;
// an unconfirmed one is deleted after UploadTTL.

const (
	// MaxUploadBytes is the largest export one upload takes. A Mailchimp
	// account export holds report files the import doesn't read; an audience
	// export of a large account is much smaller.
	MaxUploadBytes = 100 << 20
	// UploadTTL is how long an unconfirmed upload is kept.
	UploadTTL = 7 * 24 * time.Hour
	// MaxGroupColumns is the most group columns an upload can name.
	MaxGroupColumns = 50
	// maxEmailAttempts is how many times the email job tries one upload.
	maxEmailAttempts = 3
	// emailLease is how long a claim on an upload's emails lasts without
	// being renewed; each email made renews it.
	emailLease = 5 * time.Minute
)

// Statuses of an upload's emails.
const (
	EmailsNone    = "none"
	EmailsQueued  = "queued"
	EmailsRunning = "running"
	EmailsDone    = "done"
	EmailsFailed  = "failed"
)

// ErrUploadNotFound means no upload with that id in the account, or it has
// expired.
var ErrUploadNotFound = errors.New("mailchimp upload not found")

// ErrUploadUsed means the upload was already confirmed, and its file
// removed, so it can't be read again.
var ErrUploadUsed = errors.New("mailchimp upload already imported")

// Upload is a stored export.
type Upload struct {
	ID           string
	AccountID    string
	FileName     string
	Bytes        int
	AudienceName string
	GroupColumns []string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	// Set once confirmed. ImportID is the contacts import, nil when the
	// export had no contacts.
	ConfirmedAt *time.Time
	ImportID    *string
	Campaigns   int
	// EmailsStatus is one of the Emails* statuses; Emails are those made so
	// far, of EmailsTotal.
	EmailsStatus string
	EmailsTotal  int
	Emails       []ImportedEmail
	EmailsError  *string
	// HasData is false once the file has been removed.
	HasData bool
}

// Options are how the upload is read.
func (u *Upload) Options(maxMembers int) Options {
	return Options{AudienceName: u.AudienceName, GroupColumns: u.GroupColumns, MaxMembers: maxMembers}
}

// Store keeps uploads.
type Store struct {
	DB  *pgxpool.Pool
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const uploadColumns = `id, account_id, coalesce(file_name, ''), bytes, audience_name, group_columns, created_at, expires_at,
	confirmed_at, import_id, campaigns, emails_status, emails_total, emails, emails_error, data IS NOT NULL`

func scanUpload(row pgx.Row) (*Upload, error) {
	u := &Upload{}
	var emails []byte
	err := row.Scan(&u.ID, &u.AccountID, &u.FileName, &u.Bytes, &u.AudienceName, &u.GroupColumns, &u.CreatedAt, &u.ExpiresAt,
		&u.ConfirmedAt, &u.ImportID, &u.Campaigns, &u.EmailsStatus, &u.EmailsTotal, &emails, &u.EmailsError, &u.HasData)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUploadNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(emails, &u.Emails); err != nil {
		return nil, fmt.Errorf("read upload emails: %w", err)
	}
	if u.Emails == nil {
		u.Emails = []ImportedEmail{}
	}
	return u, nil
}

// CleanOptions tidies how an upload is read: the audience name on one line
// of at most 100 characters, and the group columns trimmed, without blanks or
// repeats.
func CleanOptions(audienceName string, groupColumns []string) (string, []string, error) {
	name := strings.Join(strings.Fields(audienceName), " ")
	if utf8.RuneCountInString(name) > 100 || !utf8.ValidString(name) {
		return "", nil, &Error{Code: "invalid_audience_name", Message: "The audience name is at most 100 characters."}
	}
	cols := []string{}
	seen := map[string]bool{}
	for _, c := range groupColumns {
		c = strings.TrimSpace(c)
		if c == "" || seen[strings.ToLower(c)] {
			continue
		}
		if utf8.RuneCountInString(c) > 200 || !utf8.ValidString(c) {
			return "", nil, &Error{Code: "invalid_group_column", Message: "A group column's name is at most 200 characters."}
		}
		seen[strings.ToLower(c)] = true
		cols = append(cols, c)
	}
	if len(cols) > MaxGroupColumns {
		return "", nil, &Error{Code: "too_many_group_columns", Message: fmt.Sprintf("Name at most %d group columns.", MaxGroupColumns)}
	}
	return name, cols, nil
}

// Save stores an export that Read has accepted.
func (s *Store) Save(ctx context.Context, accountID, fileName string, data []byte, opts Options) (*Upload, error) {
	now := s.now().UTC()
	var name *string
	if n := strings.Join(strings.Fields(strings.ToValidUTF8(fileName, "")), " "); n != "" {
		if r := []rune(n); len(r) > 255 {
			n = string(r[:255])
		}
		name = &n
	}
	cols := opts.GroupColumns
	if cols == nil {
		cols = []string{}
	}
	return scanUpload(s.DB.QueryRow(ctx, `
		INSERT INTO mailchimp_bundles (account_id, file_name, bytes, data, audience_name, group_columns, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+uploadColumns,
		accountID, name, len(data), data, opts.AudienceName, cols, now, now.Add(UploadTTL)))
}

// Get returns an upload of the account. An unconfirmed upload past its
// expiry is gone.
func (s *Store) Get(ctx context.Context, accountID, id string) (*Upload, error) {
	return scanUpload(s.DB.QueryRow(ctx, `SELECT `+uploadColumns+` FROM mailchimp_bundles
		WHERE id = $1 AND account_id = $2 AND (confirmed_at IS NOT NULL OR expires_at > $3)`, id, accountID, s.now()))
}

// ForImport returns the upload a contacts import came from, if any.
func (s *Store) ForImport(ctx context.Context, accountID, importID string) (*Upload, error) {
	return scanUpload(s.DB.QueryRow(ctx, `SELECT `+uploadColumns+` FROM mailchimp_bundles
		WHERE import_id = $1 AND account_id = $2`, importID, accountID))
}

// Data returns an upload's file.
func (s *Store) Data(ctx context.Context, accountID, id string) ([]byte, error) {
	var data []byte
	err := s.DB.QueryRow(ctx, `SELECT data FROM mailchimp_bundles
		WHERE id = $1 AND account_id = $2 AND (confirmed_at IS NOT NULL OR expires_at > $3)`, id, accountID, s.now()).Scan(&data)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrUploadNotFound
	case err != nil:
		return nil, err
	case data == nil:
		return nil, ErrUploadUsed
	}
	return data, nil
}

// Confirm records that the upload was confirmed, with the contacts import
// made from it (nil when it had no contacts) and the emails it makes, and
// queues those emails. Confirming again returns the upload as it is.
func (s *Store) Confirm(ctx context.Context, accountID, id string, importID *string, campaigns, emails int) (*Upload, error) {
	status := EmailsQueued
	if emails == 0 {
		status = EmailsDone
	}
	_, err := s.DB.Exec(ctx, `
		UPDATE mailchimp_bundles SET confirmed_at = $3, import_id = $4, campaigns = $5, emails_total = $6, emails_status = $7,
			data = CASE WHEN $6 = 0 THEN NULL ELSE data END
		WHERE id = $1 AND account_id = $2 AND confirmed_at IS NULL`, id, accountID, s.now().UTC(), importID, campaigns, emails, status)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, accountID, id)
}

// EmailJob makes the emails of confirmed uploads. It runs in the API, which
// holds the image store the copies go to.
type EmailJob struct {
	Store    *Store
	Importer *EmailImporter
	// MaxMembers is the most members Read takes, as on upload.
	MaxMembers int
}

// Run makes emails until ctx ends, looking for work every poll, and deletes
// expired uploads.
func (j *EmailJob) Run(ctx context.Context, log *slog.Logger, poll time.Duration) {
	var pruned time.Time
	for ctx.Err() == nil {
		if time.Since(pruned) > time.Hour {
			if n, err := j.Store.Prune(ctx); err != nil && ctx.Err() == nil {
				log.ErrorContext(ctx, "pruning Mailchimp uploads failed", "err", err)
			} else if n > 0 {
				log.InfoContext(ctx, "deleted expired Mailchimp uploads", "count", n)
			}
			pruned = time.Now()
		}
		ran, err := j.RunNext(ctx)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "Mailchimp email import stopped; it will be retried", "err", err)
		}
		if ran && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(poll):
		}
	}
}

// Prune deletes unconfirmed uploads past their expiry.
func (s *Store) Prune(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM mailchimp_bundles WHERE confirmed_at IS NULL AND expires_at <= $1`, s.now())
	return tag.RowsAffected(), err
}

// RunNext makes the emails of one confirmed upload, carrying on from the
// last one made. It reports whether there was one.
func (j *EmailJob) RunNext(ctx context.Context) (bool, error) {
	s := j.Store
	now := s.now()
	// An upload whose job stopped too often fails.
	if _, err := s.DB.Exec(ctx, `
		UPDATE mailchimp_bundles SET emails_status = 'failed', data = NULL, lease_token = NULL, lease_until = NULL,
			emails_error = 'The emails couldn''t be made. Upload the export again to retry.'
		WHERE emails_status = 'running' AND lease_until < $1 AND attempts >= $2`, now, maxEmailAttempts); err != nil {
		return false, err
	}
	var id, accountID, token string
	var data []byte
	err := s.DB.QueryRow(ctx, `
		UPDATE mailchimp_bundles SET emails_status = 'running', lease_token = gen_random_uuid(), lease_until = $1,
			attempts = attempts + 1
		WHERE id = (SELECT id FROM mailchimp_bundles
			WHERE emails_status = 'queued' OR (emails_status = 'running' AND lease_until < $2)
			ORDER BY confirmed_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, account_id, lease_token, data`, now.Add(emailLease), now).Scan(&id, &accountID, &token, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	u, err := s.Get(ctx, accountID, id)
	if err != nil {
		return true, err
	}
	finish := func(status string, msg *string) error {
		_, err := s.DB.Exec(context.WithoutCancel(ctx), `
			UPDATE mailchimp_bundles SET emails_status = $3, emails_error = $4, data = NULL, lease_token = NULL, lease_until = NULL
			WHERE id = $1 AND lease_token = $2`, id, token, status, msg)
		return err
	}
	if data == nil {
		msg := "The export's file was gone before its emails were made. Upload it again."
		return true, finish(EmailsFailed, &msg)
	}
	b, err := Read(bytes.NewReader(data), int64(len(data)), u.FileName, u.Options(j.MaxMembers))
	if err != nil {
		msg := "The export couldn't be read again: " + err.Error()
		return true, finish(EmailsFailed, &msg)
	}
	data = nil
	err = j.Importer.ImportFrom(ctx, accountID, b, u.Campaigns, len(u.Emails), func(e ImportedEmail) error {
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		tag, err := s.DB.Exec(ctx, `
			UPDATE mailchimp_bundles SET emails = emails || jsonb_build_array($3::jsonb), lease_until = $4
			WHERE id = $1 AND lease_token = $2`, id, token, raw, s.now().Add(emailLease))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errLostLease
		}
		return nil
	})
	if errors.Is(err, errLostLease) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	return true, finish(EmailsDone, nil)
}

var errLostLease = errors.New("another task took over the Mailchimp email import")
