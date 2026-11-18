// Package campaigns keeps an account's campaigns: what goes out, to whom
// and when (CAM-1), and the status each is in (CAM-9). Launching and
// sending are later tickets (MG-30 to MG-32).
package campaigns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

// Limits on a campaign.
const (
	MaxNameChars = 200
	MaxTags      = 20
	// MaxScheduleAhead is how far ahead a send time can be.
	MaxScheduleAhead = 365 * 24 * time.Hour
)

var (
	// ErrNotFound means no campaign with that id in the account.
	ErrNotFound = errors.New("campaign not found")
	// ErrUnknownTag means a tag id that isn't one of the account's tags.
	ErrUnknownTag = errors.New("unknown tag")
	// ErrUnknownEmail means an email id that isn't one of the account's emails.
	ErrUnknownEmail = errors.New("unknown email")
)

// InvalidError is a campaign that breaks a rule; Field names which.
type InvalidError struct {
	Field   string
	Message string
}

func (e *InvalidError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, format string, args ...any) *InvalidError {
	return &InvalidError{Field: field, Message: fmt.Sprintf(format, args...)}
}

// StatusError is a change the campaign's status doesn't allow: an edit or
// delete it's past, or a move CanMove refuses.
type StatusError struct {
	Status string
	// To is the status asked for; empty for an edit or delete.
	To string
}

func (e *StatusError) Error() string {
	if e.To != "" {
		return fmt.Sprintf("a %s campaign can't become %s", e.Status, e.To)
	}
	return "the campaign is " + e.Status
}

// VersionError means the campaign changed since the version given.
type VersionError struct{ Current int }

func (e *VersionError) Error() string { return fmt.Sprintf("the campaign is at version %d", e.Current) }

// Tag is one of a campaign's recipient tags.
type Tag struct {
	ID   string
	Name string
}

// Campaign is one campaign.
type Campaign struct {
	ID        string
	AccountID string
	Name      string
	// EmailID is nil when the email was deleted while the campaign was a
	// draft.
	EmailID *string
	// AllContacts sends to every subscribed contact; otherwise to the
	// contacts with any of Tags.
	AllContacts bool
	Tags        []Tag
	// SendAt is nil for "when launched".
	SendAt          *time.Time
	Status          string
	Version         int
	StatusChangedAt time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	// LastTest is the campaign's last test send; nil before the first.
	LastTest *Test
}

// Test is when a campaign's last test send went out, and the versions of
// the campaign and its email it was made from.
type Test struct {
	At           time.Time
	Version      int
	EmailVersion int
}

// TestedAsIs reports whether the campaign's last test was made from the
// campaign as it is now and from emailVersion of its email.
func (c *Campaign) TestedAsIs(emailVersion int) bool {
	return c.LastTest != nil && c.LastTest.Version == c.Version && c.LastTest.EmailVersion == emailVersion
}

// Audience is who the campaign goes to, for contacts.Recipients.
func (c *Campaign) Audience() contacts.Audience {
	ids := make([]string, len(c.Tags))
	for i, t := range c.Tags {
		ids[i] = t.ID
	}
	return contacts.Audience{All: c.AllContacts, TagIDs: ids}
}

// Recipients is who a campaign goes to: every subscribed contact, or those
// with any of the tags.
type Recipients struct {
	All    bool
	TagIDs []string
}

// Changes are the parts of a campaign to set; nil leaves a part as it is.
type Changes struct {
	Name       *string
	EmailID    *string
	Recipients *Recipients
	// SendAt sets a send time; SendNow clears it, for "when launched".
	SendAt  *time.Time
	SendNow bool
	// Version, when set, must be the campaign's current version.
	Version *int
}

// Cursor is the keyset position of the last campaign on a page.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Service stores campaigns.
type Service struct {
	DB *pgxpool.Pool
	// Now is the clock for send times; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

const columns = `id, account_id, name, email_id, all_contacts, send_at, status, version, status_changed_at, created_at, updated_at,
	last_test_at, last_test_version, last_test_email_version`

func scan(row pgx.Row) (*Campaign, error) {
	var c Campaign
	var testAt *time.Time
	var testVersion, testEmailVersion *int
	err := row.Scan(&c.ID, &c.AccountID, &c.Name, &c.EmailID, &c.AllContacts, &c.SendAt, &c.Status, &c.Version,
		&c.StatusChangedAt, &c.CreatedAt, &c.UpdatedAt, &testAt, &testVersion, &testEmailVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if testAt != nil && testVersion != nil && testEmailVersion != nil {
		c.LastTest = &Test{At: *testAt, Version: *testVersion, EmailVersion: *testEmailVersion}
	}
	return &c, nil
}

// RecordTest notes a test send of the campaign, made from its version
// version and its email's emailVersion. It's not an edit: the campaign's
// version stays.
func (s *Service) RecordTest(ctx context.Context, accountID, id string, t Test) error {
	tag, err := s.DB.Exec(ctx, `
		UPDATE campaigns SET last_test_at = $3, last_test_version = $4, last_test_email_version = $5
		WHERE account_id = $1 AND id = $2`, accountID, id, t.At, t.Version, t.EmailVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// apply checks the changes that need no lookups and sets them on c.
func (s *Service) apply(c *Campaign, ch Changes) error {
	if ch.Name != nil {
		c.Name = strings.TrimSpace(*ch.Name)
	}
	if c.Name == "" || utf8.RuneCountInString(c.Name) > MaxNameChars || strings.ContainsAny(c.Name, "\r\n") {
		return invalid("name", "a name is one line of 1 to %d characters", MaxNameChars)
	}
	if ch.SendNow {
		c.SendAt = nil
	}
	if ch.SendAt != nil {
		now := s.now()
		at := ch.SendAt.UTC()
		if !at.After(now) {
			return invalid("send_at", "the send time must be in the future")
		}
		if at.After(now.Add(MaxScheduleAhead)) {
			return invalid("send_at", "the send time can be at most a year ahead")
		}
		c.SendAt = &at
	}
	if ch.Recipients != nil {
		c.AllContacts = ch.Recipients.All
		if ch.Recipients.All && len(ch.Recipients.TagIDs) > 0 {
			return invalid("recipients", "pick all contacts or tags, not both")
		}
		if !ch.Recipients.All && len(ch.Recipients.TagIDs) == 0 {
			return invalid("recipients", "pick all contacts, or at least one tag")
		}
	}
	return nil
}

// checkEmail refuses an email that isn't the account's.
func checkEmail(ctx context.Context, tx pgx.Tx, accountID, emailID string) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT true FROM emails WHERE account_id = $1 AND id::text = $2`, accountID, emailID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrUnknownEmail
	}
	return err
}

// setTags replaces a campaign's tags, refusing any that isn't the account's.
func setTags(ctx context.Context, tx pgx.Tx, accountID, campaignID string, ids []string) error {
	uniq := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) > MaxTags {
		return invalid("recipients", "a campaign can go to up to %d tags", MaxTags)
	}
	if len(uniq) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tags WHERE account_id = $1 AND id::text = ANY($2)`, accountID, uniq).Scan(&n); err != nil {
			return err
		}
		if n != len(uniq) {
			return ErrUnknownTag
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM campaign_tags WHERE campaign_id = $1`, campaignID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO campaign_tags (campaign_id, tag_id) SELECT $1, unnest($2::uuid[])`, campaignID, uniq)
	return err
}

// Create makes a draft campaign. It needs a name, an email and recipients;
// without a send time it goes out when launched.
func (s *Service) Create(ctx context.Context, accountID string, ch Changes) (*Campaign, error) {
	c := &Campaign{}
	if err := s.apply(c, ch); err != nil {
		return nil, err
	}
	if ch.EmailID == nil || *ch.EmailID == "" {
		return nil, invalid("email_id", "a campaign needs an email")
	}
	if ch.Recipients == nil {
		return nil, invalid("recipients", "pick all contacts, or at least one tag")
	}
	var id string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := checkEmail(ctx, tx, accountID, *ch.EmailID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO campaigns (account_id, name, email_id, all_contacts, send_at)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			accountID, c.Name, *ch.EmailID, c.AllContacts, c.SendAt).Scan(&id); err != nil {
			return err
		}
		return setTags(ctx, tx, accountID, id, ch.Recipients.TagIDs)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, accountID, id)
}

// Update changes a draft or scheduled campaign, and moves its version on.
func (s *Service) Update(ctx context.Context, accountID, id string, ch Changes) (*Campaign, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		c, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM campaigns WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, id))
		if err != nil {
			return err
		}
		if !Editable(c.Status) {
			return &StatusError{Status: c.Status}
		}
		if ch.Version != nil && *ch.Version != c.Version {
			return &VersionError{Current: c.Version}
		}
		if err := s.apply(c, ch); err != nil {
			return err
		}
		if c.Status == StatusScheduled && c.SendAt == nil {
			return invalid("send_at", "a scheduled campaign keeps a send time; unschedule it to send it when launched")
		}
		if ch.EmailID != nil {
			if *ch.EmailID == "" {
				return invalid("email_id", "a campaign needs an email")
			}
			if err := checkEmail(ctx, tx, accountID, *ch.EmailID); err != nil {
				return err
			}
			c.EmailID = ch.EmailID
		}
		if _, err := tx.Exec(ctx, `
			UPDATE campaigns SET name = $3, email_id = $4, all_contacts = $5, send_at = $6,
				version = version + 1, updated_at = now()
			WHERE account_id = $1 AND id = $2`,
			accountID, id, c.Name, c.EmailID, c.AllContacts, c.SendAt); err != nil {
			return err
		}
		if ch.Recipients != nil {
			return setTags(ctx, tx, accountID, id, ch.Recipients.TagIDs)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, accountID, id)
}

// SetStatus moves a campaign to another status, if CanMove allows it.
// Launching, scheduling, sending and pausing (MG-30 to MG-34) go through
// here.
func (s *Service) SetStatus(ctx context.Context, accountID, id, to string) (*Campaign, error) {
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		c, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM campaigns WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, id))
		if err != nil {
			return err
		}
		if !CanMove(c.Status, to) {
			return &StatusError{Status: c.Status, To: to}
		}
		_, err = tx.Exec(ctx, `UPDATE campaigns SET status = $3, status_changed_at = now(), updated_at = now()
			WHERE account_id = $1 AND id = $2`, accountID, id, to)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, accountID, id)
}

// Get returns one campaign with its tags.
func (s *Service) Get(ctx context.Context, accountID, id string) (*Campaign, error) {
	c, err := scan(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM campaigns WHERE account_id = $1 AND id = $2`, accountID, id))
	if err != nil {
		return nil, err
	}
	if err := s.loadTags(ctx, []*Campaign{c}); err != nil {
		return nil, err
	}
	return c, nil
}

// List returns up to limit campaigns, newest first, after the cursor, with
// the given status when one is set.
func (s *Service) List(ctx context.Context, accountID, status string, after *Cursor, limit int) ([]*Campaign, error) {
	q := `SELECT ` + columns + ` FROM campaigns WHERE account_id = $1`
	args := []any{accountID}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(` AND status = $%d`, len(args))
	}
	if after != nil {
		args = append(args, after.CreatedAt, after.ID)
		q += fmt.Sprintf(` AND (created_at, id) < ($%d, $%d)`, len(args)-1, len(args))
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit)
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Campaign
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.loadTags(ctx, out)
}

func (s *Service) loadTags(ctx context.Context, cs []*Campaign) error {
	if len(cs) == 0 {
		return nil
	}
	byID := make(map[string]*Campaign, len(cs))
	ids := make([]string, len(cs))
	for i, c := range cs {
		byID[c.ID] = c
		ids[i] = c.ID
		c.Tags = []Tag{}
	}
	rows, err := s.DB.Query(ctx, `
		SELECT ct.campaign_id, t.id, t.name FROM campaign_tags ct JOIN tags t ON t.id = ct.tag_id
		WHERE ct.campaign_id::text = ANY($1) ORDER BY lower(t.name), t.id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var campaignID string
		var t Tag
		if err := rows.Scan(&campaignID, &t.ID, &t.Name); err != nil {
			return err
		}
		byID[campaignID].Tags = append(byID[campaignID].Tags, t)
	}
	return rows.Err()
}

// Delete removes a draft campaign. Anything past draft stays, for its
// record: a scheduled one is cancelled instead (MG-32).
func (s *Service) Delete(ctx context.Context, accountID, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		c, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM campaigns WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, id))
		if err != nil {
			return err
		}
		if c.Status != StatusDraft {
			return &StatusError{Status: c.Status}
		}
		_, err = tx.Exec(ctx, `DELETE FROM campaigns WHERE id = $1`, id)
		return err
	})
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
