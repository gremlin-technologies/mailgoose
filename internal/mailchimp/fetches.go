package mailchimp

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Fetches from a connected account (MG-74): queued by the API, run by
// FetchJob in the API task, which holds the Mailchimp app's secret, and
// stored as a bundle the customer previews and confirms like an upload.

const (
	// FetchTTL is how long a finished fetch is kept.
	FetchTTL = 7 * 24 * time.Hour
	// fetchLease is how long a claim on a fetch lasts without being renewed.
	fetchLease = 5 * time.Minute
	// maxFetchAttempts is how many times a fetch whose task stopped is
	// started again.
	maxFetchAttempts = 3
)

// Statuses of a fetch.
const (
	FetchQueued  = "queued"
	FetchRunning = "running"
	FetchDone    = "done"
	FetchFailed  = "failed"
)

// Why a fetch failed, besides the reader's and Fetch's codes.
const (
	FetchErrNotConnected = "mailchimp_not_connected"
	FetchErrRevoked      = "mailchimp_access_revoked"
	FetchErrUnavailable  = "mailchimp_unavailable"
	FetchErrStopped      = "fetch_stopped"
	FetchErrTooMany      = "too_many_uploads"
)

// ErrFetchNotFound means no fetch with that id in the account.
var ErrFetchNotFound = errors.New("mailchimp fetch not found")

// ErrFetchRunning means the account already has a fetch queued or running.
var ErrFetchRunning = errors.New("a mailchimp fetch is already running")

// Fetch is one fetch from a connected account.
type Fetch struct {
	ID           string
	AccountID    string
	AudienceIDs  []string
	SegmentIDs   []string
	Campaigns    int
	Status       string
	Step         string
	Audience     *string
	MembersRead  int
	MembersTotal int
	ErrorCode    *string
	ErrorMessage *string
	BundleID     *string
	CreatedAt    time.Time
	FinishedAt   *time.Time
}

const fetchColumns = `id, account_id, audience_ids, segment_ids, campaigns, status, step, audience, members_read, members_total,
	error_code, error_message, bundle_id, created_at, finished_at`

func scanFetch(row pgx.Row) (*Fetch, error) {
	f := &Fetch{}
	err := row.Scan(&f.ID, &f.AccountID, &f.AudienceIDs, &f.SegmentIDs, &f.Campaigns, &f.Status, &f.Step, &f.Audience,
		&f.MembersRead, &f.MembersTotal, &f.ErrorCode, &f.ErrorMessage, &f.BundleID, &f.CreatedAt, &f.FinishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrFetchNotFound
	}
	return f, err
}

// QueueFetch queues a fetch for the account. It refuses with
// ErrFetchRunning while another is queued or running.
func (s *Store) QueueFetch(ctx context.Context, accountID string, req FetchRequest) (*Fetch, error) {
	segs := req.SegmentIDs
	if segs == nil {
		segs = []string{}
	}
	f, err := scanFetch(s.DB.QueryRow(ctx, `
		INSERT INTO mailchimp_fetches (account_id, audience_ids, segment_ids, campaigns, created_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+fetchColumns, accountID, req.AudienceIDs, segs, req.Campaigns, s.now().UTC()))
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return nil, ErrFetchRunning
	}
	return f, err
}

// GetFetch returns a fetch of the account.
func (s *Store) GetFetch(ctx context.Context, accountID, id string) (*Fetch, error) {
	return scanFetch(s.DB.QueryRow(ctx, `SELECT `+fetchColumns+` FROM mailchimp_fetches WHERE id = $1 AND account_id = $2`,
		id, accountID))
}

// SaveFetchedFunc stores a fetch's result as a bundle and returns its id:
// the API reads it, works out its preview and calls Store.SaveFetched.
// Refusals the customer can act on are *Error or ErrTooManyUploads.
type SaveFetchedFunc func(ctx context.Context, accountID string, f *Fetched) (bundleID string, err error)

// FetchJob runs queued fetches.
type FetchJob struct {
	Store       *Store
	Connections *Connections
	Save        SaveFetchedFunc
	// MaxMembers is the most members a fetch reads, as for an upload.
	MaxMembers int
}

// Run runs fetches until ctx ends, looking for work every poll, and
// deletes expired Mailchimp tokens each minute.
func (j *FetchJob) Run(ctx context.Context, log *slog.Logger, poll time.Duration) {
	var cleared time.Time
	for ctx.Err() == nil {
		if time.Since(cleared) > time.Minute {
			if _, err := j.Connections.ClearExpired(ctx); err != nil && ctx.Err() == nil {
				log.ErrorContext(ctx, "deleting expired Mailchimp tokens failed", "err", err)
			}
			cleared = time.Now()
		}
		ran, err := j.RunNext(ctx)
		if err != nil && ctx.Err() == nil {
			log.ErrorContext(ctx, "Mailchimp fetch stopped; it will be retried", "err", err)
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

// RunNext runs one queued fetch, or one whose task stopped, and reports
// whether there was one. A fetch starts again from the beginning.
func (j *FetchJob) RunNext(ctx context.Context) (bool, error) {
	s := j.Store
	now := s.now()
	if _, err := s.DB.Exec(ctx, `
		UPDATE mailchimp_fetches SET status = 'failed', finished_at = $1, lease_token = NULL, lease_until = NULL,
			error_code = $3, error_message = 'The fetch stopped too often. Start it again.'
		WHERE status = 'running' AND lease_until < $1 AND attempts >= $2`, now, maxFetchAttempts, FetchErrStopped); err != nil {
		return false, err
	}
	var id, accountID, token string
	var req FetchRequest
	err := s.DB.QueryRow(ctx, `
		UPDATE mailchimp_fetches SET status = 'running', lease_token = gen_random_uuid(), lease_until = $1,
			attempts = attempts + 1, started_at = coalesce(started_at, $2), members_read = 0
		WHERE id = (SELECT id FROM mailchimp_fetches
			WHERE status = 'queued' OR (status = 'running' AND lease_until < $2)
			ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id, account_id, lease_token, audience_ids, segment_ids, campaigns`, now.Add(fetchLease), now).
		Scan(&id, &accountID, &token, &req.AudienceIDs, &req.SegmentIDs, &req.Campaigns)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx, stop := s.keepLease(ctx, "mailchimp_fetches", id, token, fetchLease/5)
	defer stop()
	done := context.WithoutCancel(ctx)
	fail := func(code, msg string) error {
		_, err := s.DB.Exec(done, `
			UPDATE mailchimp_fetches SET status = 'failed', error_code = $3, error_message = $4, finished_at = $5,
				lease_token = NULL, lease_until = NULL
			WHERE id = $1 AND lease_token = $2`, id, token, code, msg, s.now().UTC())
		return err
	}

	client, err := j.Connections.Client(ctx, accountID)
	switch {
	case errors.Is(err, ErrNotConnected), errors.Is(err, ErrNotConfigured):
		return true, fail(FetchErrNotConnected, "The Mailchimp connection ended before the fetch. Connect again: `mailgoose import mailchimp connect`.")
	case err != nil:
		return true, err
	}
	req.MaxMembers = j.MaxMembers
	var last time.Time
	out, err := client.Fetch(ctx, req, func(p FetchProgress) error {
		// Progress is written at most once a second, and each write renews
		// the claim.
		if time.Since(last) < time.Second && p.Step == StepMembers {
			return nil
		}
		last = time.Now()
		var aud *string
		if p.Audience != "" {
			aud = &p.Audience
		}
		tag, err := s.DB.Exec(ctx, `
			UPDATE mailchimp_fetches SET step = $3, audience = $4, members_read = $5, members_total = $6, lease_until = $7
			WHERE id = $1 AND lease_token = $2`, id, token, p.Step, aud, p.MembersRead, p.MembersTotal, s.now().Add(fetchLease))
		if err == nil && tag.RowsAffected() == 0 {
			return errLostLease
		}
		return err
	})
	if errors.Is(err, errLostLease) || errors.Is(context.Cause(ctx), errLostLease) {
		return true, nil
	}
	var re *Error
	switch {
	case errors.As(err, &re):
		return true, fail(re.Code, re.Message)
	case errors.Is(err, ErrAccessRevoked):
		// The token is no good now; forget it.
		if ferr := j.Connections.Forget(done, accountID); ferr != nil {
			return true, ferr
		}
		return true, fail(FetchErrRevoked, "Mailchimp no longer allows mailgoose to read this account. Connect again: `mailgoose import mailchimp connect`.")
	case ctx.Err() != nil:
		return true, ctx.Err()
	case err != nil:
		var ae *APIError
		msg := "Mailchimp's API didn't answer just now. Start the fetch again in a few minutes."
		if errors.As(err, &ae) && ae.Status < 500 && ae.Status != 429 {
			msg = "Mailchimp's API refused a request (" + ae.Error() + "). Start the fetch again, or upload the export instead."
		}
		return true, fail(FetchErrUnavailable, msg)
	}

	members := out.Members
	bundleID, err := j.Save(ctx, accountID, out)
	switch {
	case errors.As(err, &re):
		return true, fail(re.Code, re.Message)
	case errors.Is(err, ErrTooManyUploads):
		return true, fail(FetchErrTooMany, "This account already has 3 exports waiting to be confirmed. Confirm or delete one, then fetch again.")
	case err != nil:
		return true, err
	}
	_, err = s.DB.Exec(done, `
		UPDATE mailchimp_fetches SET status = 'done', step = '', audience = NULL, bundle_id = $3, finished_at = $4,
			members_read = $5, lease_token = NULL, lease_until = NULL
		WHERE id = $1 AND lease_token = $2`, id, token, bundleID, s.now().UTC(), members)
	return true, err
}
