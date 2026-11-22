// Package segments keeps an account's saved segments (MG-114): named sets of
// contact conditions (tags, statuses and the engagement filters of MG-113),
// all of which must match. A segment is worked out each time it's used, so
// it stays current, and a condition naming a tag or campaign that's gone, or
// one that can no longer be used, makes it invalid rather than letting it
// quietly match someone else.
package segments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

const (
	// MaxSegments is how many segments an account keeps.
	MaxSegments = 100
	// MaxNameLen is the most characters a segment's name may have.
	MaxNameLen = 100
	// MaxTags is how many tags one segment may name.
	MaxTags = 20
)

var (
	// ErrNotFound means no segment with that id in the account.
	ErrNotFound = errors.New("segments: not found")
	// ErrExists means the account has a segment with that name, ignoring
	// case.
	ErrExists = errors.New("segments: name taken")
)

// LimitError is an account that has MaxSegments segments already.
type LimitError struct{ Count int }

func (e *LimitError) Error() string {
	return fmt.Sprintf("segments: the account has %d segments", e.Count)
}

// Conditions are what a contact must match to be in a segment: every
// condition given. They're kept as given, ids and all.
type Conditions struct {
	// TagIDs are tags the contact must have, every one.
	TagIDs []string `json:"tag_ids,omitempty"`
	// Statuses are the statuses the contact may have, one of them.
	Statuses []string `json:"statuses,omitempty"`
	// Opened, NotOpened, Clicked and NotClicked are campaign ids, as the
	// contact list's filters of the same names take them.
	Opened     []string `json:"opened,omitempty"`
	NotOpened  []string `json:"not_opened,omitempty"`
	Clicked    []string `json:"clicked,omitempty"`
	NotClicked []string `json:"not_clicked,omitempty"`
	// NeverOpened, NeverClicked and the within-days conditions are the
	// contact list's filters of the same names.
	NeverOpened       bool `json:"never_opened,omitempty"`
	NeverClicked      bool `json:"never_clicked,omitempty"`
	OpenedWithinDays  int  `json:"opened_within_days,omitempty"`
	ClickedWithinDays int  `json:"clicked_within_days,omitempty"`
}

// Filter is the contact filter the conditions make.
func (c Conditions) Filter() contacts.Filter {
	return contacts.Filter{
		TagIDs:   c.TagIDs,
		Statuses: c.Statuses,
		Engagement: contacts.Engagement{
			Opened: c.Opened, NotOpened: c.NotOpened, Clicked: c.Clicked, NotClicked: c.NotClicked,
			NeverOpened: c.NeverOpened, NeverClicked: c.NeverClicked,
			OpenedWithinDays: c.OpenedWithinDays, ClickedWithinDays: c.ClickedWithinDays,
		},
	}
}

// campaignLists are the conditions that name campaigns, by their API names.
func (c Conditions) campaignLists() []struct {
	name string
	ids  []string
} {
	return []struct {
		name string
		ids  []string
	}{{"opened", c.Opened}, {"not_opened", c.NotOpened}, {"clicked", c.Clicked}, {"not_clicked", c.NotClicked}}
}

// IsZero says whether c has no condition at all.
func (c Conditions) IsZero() bool {
	return len(c.TagIDs) == 0 && len(c.Statuses) == 0 && c.Filter().Engagement.IsZero()
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

var validStatuses = map[string]bool{
	contacts.StatusUnconfirmed: true, contacts.StatusSubscribed: true, contacts.StatusUnsubscribed: true,
	contacts.StatusBounced: true, contacts.StatusComplained: true,
}

func invalid(field, msg string) error {
	return &auth.ValidationError{Code: "invalid_conditions", Field: field, Message: msg}
}

// ids checks a list of ids, lowercases them and drops repeats.
func ids(field, what string, in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, id := range in {
		if !uuidPattern.MatchString(id) {
			return nil, invalid(field, fmt.Sprintf("conditions.%s must hold %s ids, and %q isn't one.", field, what, id))
		}
		id = strings.ToLower(id)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// normalize checks c and returns it tidied: ids lowercased, repeats gone.
func normalize(c Conditions) (Conditions, error) {
	var out Conditions
	var err error
	if out.TagIDs, err = ids("tag_ids", "tag", c.TagIDs); err != nil {
		return out, err
	}
	if len(out.TagIDs) > MaxTags {
		return out, invalid("tag_ids", fmt.Sprintf("A segment can name at most %d tags.", MaxTags))
	}
	seen := map[string]bool{}
	for _, st := range c.Statuses {
		if !validStatuses[st] {
			return out, invalid("statuses", fmt.Sprintf("%q isn't a status. They're unconfirmed, subscribed, unsubscribed, bounced and complained.", st))
		}
		if !seen[st] {
			seen[st] = true
			out.Statuses = append(out.Statuses, st)
		}
	}
	for _, l := range []struct {
		field   string
		in, out *[]string
	}{
		{"opened", &c.Opened, &out.Opened}, {"not_opened", &c.NotOpened, &out.NotOpened},
		{"clicked", &c.Clicked, &out.Clicked}, {"not_clicked", &c.NotClicked, &out.NotClicked},
	} {
		if *l.out, err = ids(l.field, "campaign", *l.in); err != nil {
			return out, err
		}
	}
	if n := len(out.Filter().Engagement.Campaigns()); n > contacts.MaxFilterCampaigns {
		return out, invalid("opened", fmt.Sprintf("A segment's conditions can name at most %d campaigns, and these name %d.", contacts.MaxFilterCampaigns, n))
	}
	out.NeverOpened, out.NeverClicked = c.NeverOpened, c.NeverClicked
	for _, d := range []struct {
		field   string
		in, out *int
	}{
		{"opened_within_days", &c.OpenedWithinDays, &out.OpenedWithinDays},
		{"clicked_within_days", &c.ClickedWithinDays, &out.ClickedWithinDays},
	} {
		if *d.in < 0 || *d.in > contacts.MaxFilterDays {
			return out, invalid(d.field, fmt.Sprintf("conditions.%s is a number of days from 1 to %d.", d.field, contacts.MaxFilterDays))
		}
		*d.out = *d.in
	}
	if out.IsZero() {
		return out, &auth.ValidationError{Code: "no_conditions", Field: "conditions",
			Message: "A segment needs at least one condition; every contact is already the whole audience."}
	}
	return out, nil
}

func normalizeName(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if n == "" || utf8.RuneCountInString(n) > MaxNameLen || strings.ContainsAny(n, "\r\n") {
		return "", &auth.ValidationError{Code: "invalid_segment_name", Field: "name",
			Message: fmt.Sprintf("A segment's name is 1 to %d characters on one line.", MaxNameLen)}
	}
	return n, nil
}

// Problem is a condition the segment can't use any more.
type Problem struct {
	// Condition is the condition's name: tag_ids, opened, not_opened,
	// clicked or not_clicked.
	Condition string
	// ID is the tag or campaign it names.
	ID string
	// Code is ProblemTagDeleted, ProblemCampaignDeleted, ProblemTrackingOff
	// or ProblemReportArchived.
	Code string
}

// The problems a condition can have.
const (
	ProblemTagDeleted      = "tag_deleted"
	ProblemCampaignDeleted = "campaign_deleted"
	ProblemTrackingOff     = "tracking_off"
	ProblemReportArchived  = "report_archived"
)

// Segment is one of an account's segments.
type Segment struct {
	ID         string
	Name       string
	Conditions Conditions
	// Problems are the conditions that can't be used now; a segment with
	// any is invalid, and listing its contacts is refused.
	Problems  []Problem
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Valid says whether every condition can be used.
func (s *Segment) Valid() bool { return len(s.Problems) == 0 }

// ConditionError is a condition a create or update can't use. Code is
// unknown_tag, unknown_campaign, tracking_off or report_archived.
type ConditionError struct {
	Condition, ID, Code string
}

func (e *ConditionError) Error() string {
	return fmt.Sprintf("segments: %s %s: %s", e.Condition, e.ID, e.Code)
}

// InvalidError is a segment whose conditions have problems, so its contacts
// can't be worked out.
type InvalidError struct{ Segment *Segment }

func (e *InvalidError) Error() string {
	return fmt.Sprintf("segments: %s has %d problems", e.Segment.ID, len(e.Segment.Problems))
}

// Service keeps segments.
type Service struct {
	DB       *pgxpool.Pool
	Contacts *contacts.Service
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

const columns = `id, name, conditions, created_at, updated_at`

func scan(row pgx.Row) (*Segment, error) {
	var sg Segment
	var raw []byte
	err := row.Scan(&sg.ID, &sg.Name, &raw, &sg.CreatedAt, &sg.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &sg.Conditions); err != nil {
		return nil, fmt.Errorf("segments: reading %s's conditions: %w", sg.ID, err)
	}
	return &sg, nil
}

// querier is a pool or a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// problems works out each segment's problems, looking every tag and
// campaign they name up at once.
func problems(ctx context.Context, db querier, accountID string, segs ...*Segment) error {
	var tagIDs, campaignIDs []string
	for _, sg := range segs {
		tagIDs = append(tagIDs, sg.Conditions.TagIDs...)
		campaignIDs = append(campaignIDs, sg.Conditions.Filter().Engagement.Campaigns()...)
	}
	tags := map[string]bool{}
	if len(tagIDs) > 0 {
		rows, err := db.Query(ctx, `SELECT id::text FROM tags WHERE account_id = $1 AND id::text = ANY($2)`, accountID, tagIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			tags[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	type campaign struct{ tracking, archived bool }
	camps := map[string]campaign{}
	if len(campaignIDs) > 0 {
		rows, err := db.Query(ctx, `
			SELECT c.id::text, c.tracking, EXISTS (SELECT 1 FROM campaign_reports s WHERE s.campaign_id = c.id)
			FROM campaigns c WHERE c.account_id = $1 AND c.id::text = ANY($2)`, accountID, campaignIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var c campaign
			if err := rows.Scan(&id, &c.tracking, &c.archived); err != nil {
				rows.Close()
				return err
			}
			camps[id] = c
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for _, sg := range segs {
		sg.Problems = nil
		for _, id := range sg.Conditions.TagIDs {
			if !tags[id] {
				sg.Problems = append(sg.Problems, Problem{Condition: "tag_ids", ID: id, Code: ProblemTagDeleted})
			}
		}
		for _, l := range sg.Conditions.campaignLists() {
			for _, id := range l.ids {
				c, ok := camps[id]
				switch {
				case !ok:
					sg.Problems = append(sg.Problems, Problem{Condition: l.name, ID: id, Code: ProblemCampaignDeleted})
				case !c.tracking:
					sg.Problems = append(sg.Problems, Problem{Condition: l.name, ID: id, Code: ProblemTrackingOff})
				case c.archived:
					sg.Problems = append(sg.Problems, Problem{Condition: l.name, ID: id, Code: ProblemReportArchived})
				}
			}
		}
	}
	return nil
}

// usable checks that conditions about to be saved can all be used now.
func usable(ctx context.Context, db querier, accountID string, c Conditions) error {
	probe := &Segment{Conditions: c}
	if err := problems(ctx, db, accountID, probe); err != nil {
		return err
	}
	if len(probe.Problems) == 0 {
		return nil
	}
	p := probe.Problems[0]
	code := p.Code
	switch code {
	case ProblemTagDeleted:
		code = "unknown_tag"
	case ProblemCampaignDeleted:
		code = "unknown_campaign"
	}
	return &ConditionError{Condition: p.Condition, ID: p.ID, Code: code}
}

// Create saves a new segment.
func (s *Service) Create(ctx context.Context, accountID, name string, c Conditions) (*Segment, error) {
	name, err := normalizeName(name)
	if err != nil {
		return nil, err
	}
	if c, err = normalize(c); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var sg *Segment
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		// The account row is locked, so two creates at once can't both take
		// the last place.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, accountID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM segments WHERE account_id = $1`, accountID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxSegments {
			return &LimitError{Count: n}
		}
		if err := usable(ctx, tx, accountID, c); err != nil {
			return err
		}
		now := s.now()
		sg, err = scan(tx.QueryRow(ctx, `INSERT INTO segments (account_id, name, conditions, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4) RETURNING `+columns, accountID, name, raw, now))
		if isUniqueViolation(err) {
			return ErrExists
		}
		return err
	})
	return sg, err
}

// Update changes a segment's name, its conditions, or both; a nil one stays
// as it is. New conditions replace the old ones whole.
func (s *Service) Update(ctx context.Context, accountID, id string, name *string, c *Conditions) (*Segment, error) {
	if !uuidPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	var newName string
	var raw []byte
	var err error
	if name != nil {
		if newName, err = normalizeName(*name); err != nil {
			return nil, err
		}
	}
	var conds Conditions
	if c != nil {
		if conds, err = normalize(*c); err != nil {
			return nil, err
		}
		if raw, err = json.Marshal(conds); err != nil {
			return nil, err
		}
	}
	var sg *Segment
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM segments WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, id)); err != nil {
			return err
		}
		if c != nil {
			if err := usable(ctx, tx, accountID, conds); err != nil {
				return err
			}
		}
		sg, err = scan(tx.QueryRow(ctx, `
			UPDATE segments SET name = coalesce($3, name), conditions = coalesce($4, conditions), updated_at = $5
			WHERE id = $2 AND account_id = $1 RETURNING `+columns,
			accountID, id, nullable(name != nil, newName), raw, s.now()))
		if isUniqueViolation(err) {
			return ErrExists
		}
		if err != nil {
			return err
		}
		return problems(ctx, tx, accountID, sg)
	})
	return sg, err
}

func nullable(set bool, v string) *string {
	if !set {
		return nil
	}
	return &v
}

// Delete removes a segment. Contacts aren't touched.
func (s *Service) Delete(ctx context.Context, accountID, id string) error {
	if !uuidPattern.MatchString(id) {
		return ErrNotFound
	}
	res, err := s.DB.Exec(ctx, `DELETE FROM segments WHERE account_id = $1 AND id = $2`, accountID, id)
	if err == nil && res.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Get returns one of the account's segments, with its problems.
func (s *Service) Get(ctx context.Context, accountID, id string) (*Segment, error) {
	if !uuidPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	sg, err := scan(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM segments WHERE account_id = $1 AND id = $2`, accountID, id))
	if err != nil {
		return nil, err
	}
	return sg, problems(ctx, s.DB, accountID, sg)
}

// Cursor is the keyset position after a segment in List order.
type Cursor struct {
	Name string
	ID   string
}

// List returns up to limit of the account's segments by name, after the
// cursor when one is given, each with its problems. A name, when given,
// picks the segment with that name, ignoring case.
func (s *Service) List(ctx context.Context, accountID, name string, after *Cursor, limit int) ([]*Segment, error) {
	q := `SELECT ` + columns + ` FROM segments WHERE account_id = $1`
	args := []any{accountID}
	if n := strings.TrimSpace(name); n != "" {
		args = append(args, n)
		q += fmt.Sprintf(` AND lower(name) = lower($%d)`, len(args))
	}
	if after != nil {
		args = append(args, after.Name, after.ID)
		q += fmt.Sprintf(` AND (lower(name), id) > (lower($%d), $%d::uuid)`, len(args)-1, len(args))
	}
	args = append(args, limit)
	q += fmt.Sprintf(` ORDER BY lower(name), id LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var out []*Segment
	for rows.Next() {
		sg, err := scan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, sg)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, problems(ctx, s.DB, accountID, out...)
}

// Resolve returns a segment and the contact filter it makes now, or an
// *InvalidError when a condition can't be used.
func (s *Service) Resolve(ctx context.Context, accountID, id string) (*Segment, contacts.Filter, error) {
	sg, err := s.Get(ctx, accountID, id)
	if err != nil {
		return nil, contacts.Filter{}, err
	}
	if !sg.Valid() {
		return sg, contacts.Filter{}, &InvalidError{Segment: sg}
	}
	return sg, sg.Conditions.Filter(), nil
}

// MemberPage is a page of a segment's contacts and how many it has in all.
type MemberPage struct {
	Contacts []*contacts.Contact
	Count    int
}

// Members lists up to limit of the segment's contacts now, newest first as
// the contact list is, after the cursor when one is given, and counts them
// all.
func (s *Service) Members(ctx context.Context, accountID, id string, after *contacts.Cursor, limit int) (*MemberPage, error) {
	_, f, err := s.Resolve(ctx, accountID, id)
	if err != nil {
		return nil, err
	}
	var m MemberPage
	if m.Contacts, err = s.Contacts.List(ctx, accountID, f, after, limit); err != nil {
		return nil, err
	}
	if m.Count, err = s.Contacts.Count(ctx, accountID, f); err != nil {
		return nil, err
	}
	return &m, nil
}
