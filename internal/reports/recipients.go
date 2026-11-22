package reports

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
)

// Activity picks which of a campaign's recipients to list (MG-112).
type Activity string

// The activities a campaign's recipients can be listed by.
const (
	Opened       Activity = "opened"
	NotOpened    Activity = "not_opened"
	Clicked      Activity = "clicked"
	NotClicked   Activity = "not_clicked"
	Bounced      Activity = "bounced"
	Complained   Activity = "complained"
	Unsubscribed Activity = "unsubscribed"
)

// Activities are every activity, in the order the docs list them.
var Activities = []Activity{Opened, NotOpened, Clicked, NotClicked, Bounced, Complained, Unsubscribed}

// Valid says whether a is one of Activities.
func (a Activity) Valid() bool {
	for _, x := range Activities {
		if a == x {
			return true
		}
	}
	return false
}

// Tracked says whether a needs the campaign's opens and clicks, which
// aren't recorded with tracking off.
func (a Activity) Tracked() bool {
	return a == Opened || a == NotOpened || a == Clicked || a == NotClicked
}

// Errors from Recipients.
var (
	// ErrArchived is a campaign whose raw events were deleted 12 months
	// after launch: only its saved numbers are left.
	ErrArchived = errors.New("reports: the campaign's events are archived")
	// ErrTrackingOff is an open or click list of a campaign sent with
	// tracking off.
	ErrTrackingOff = errors.New("reports: the campaign wasn't tracked")
)

// RecipientQuery is which recipients to list, and which page.
type RecipientQuery struct {
	Activity Activity
	// Link narrows Clicked to the link at this position in the email, from
	// 1; 0 is any link.
	Link int
	// After is the last recipient of the previous page (Recipient.ID), or 0.
	After int64
	// Limit is how many to give; 0 is all of them.
	Limit int
}

// Recipient is one contact a campaign went to, for a list by activity.
type Recipient struct {
	// ID is the recipient's row, the list's order and cursor.
	ID        int64   `json:"-"`
	ContactID string  `json:"contact_id"`
	Email     string  `json:"email"`
	FirstName *string `json:"first_name"`
	LastName  *string `json:"last_name"`
	// At is when it first happened: the first open or click, the bounce,
	// the complaint or the unsubscribe. Nil for not_opened and not_clicked.
	At *time.Time `json:"at"`
}

// activitySQL is when each activity first happened to the recipient r, or
// NULL when it didn't. Clicked's $3 is the link, 0 for any.
var activitySQL = map[Activity]string{
	Opened: `(SELECT min(g.occurred_at) FROM engagement_events g
		WHERE g.recipient_id = r.id AND g.type = 'open')`,
	Clicked: `(SELECT min(g.occurred_at) FROM engagement_events g
		WHERE g.recipient_id = r.id AND g.type = 'click' AND ($3::int = 0 OR g.link = $3::int))`,
	Bounced: `(SELECT min(e.occurred_at) FROM send_events e
		WHERE e.recipient_id = r.id AND e.kind = 'campaign' AND e.type = 'bounce' AND e.subtype LIKE 'permanent/%')`,
	Complained: `(SELECT min(e.occurred_at) FROM send_events e
		WHERE e.recipient_id = r.id AND e.kind = 'campaign' AND e.type = 'complaint')`,
	Unsubscribed: `(SELECT min(u.created_at) FROM unsubscribes u
		WHERE u.campaign_id = r.campaign_id AND u.contact_id = r.contact_id)`,
}

// notSQL is not_opened and not_clicked: delivered, with no open or click.
var notSQL = map[Activity]string{
	NotOpened:  "open",
	NotClicked: "click",
}

// Recipients lists the campaign's recipients with activity q.Activity, in
// the order they were queued, after q.After. A contact deleted since is
// left out. It's campaigns.ErrNotFound for a campaign that isn't the
// account's, ErrArchived once its events are gone, and ErrTrackingOff for
// opens or clicks of a campaign that wasn't tracked. A draft has none.
func (s *Service) Recipients(ctx context.Context, accountID, campaignID string, q RecipientQuery) ([]Recipient, error) {
	sql, err := s.recipientQuery(ctx, accountID, campaignID, q.Activity)
	if err != nil {
		return nil, err
	}
	args := []any{campaignID, q.After}
	if q.Activity == Clicked {
		args = append(args, q.Link)
	}
	if q.Limit > 0 {
		args = append(args, q.Limit)
		sql += fmt.Sprintf(` LIMIT $%d`, len(args))
	}
	rows, err := s.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Recipient, error) {
		var r Recipient
		err := row.Scan(&r.ID, &r.ContactID, &r.Email, &r.FirstName, &r.LastName, &r.At)
		return r, err
	})
}

// recipientQuery checks the campaign and gives the list's query, with $1
// the campaign, $2 the recipient to start after and, for Clicked, $3 the
// link.
func (s *Service) recipientQuery(ctx context.Context, accountID, campaignID string, a Activity) (string, error) {
	if !a.Valid() {
		return "", fmt.Errorf("reports: unknown activity %q", a)
	}
	var tracking, archived bool
	err := s.DB.QueryRow(ctx, `
		SELECT c.tracking, EXISTS (SELECT 1 FROM campaign_reports s WHERE s.campaign_id = c.id)
		FROM campaigns c WHERE c.id = $1 AND c.account_id = $2`, campaignID, accountID).Scan(&tracking, &archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", campaigns.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	switch {
	case archived:
		return "", ErrArchived
	case a.Tracked() && !tracking:
		return "", ErrTrackingOff
	}
	at, from := "a.at", `campaign_recipients r JOIN contacts k ON k.id = r.contact_id`
	where := `AND a.at IS NOT NULL`
	if ev, ok := notSQL[a]; ok {
		at = "NULL::timestamptz"
		where = `AND EXISTS (SELECT 1 FROM send_events e
				WHERE e.recipient_id = r.id AND e.kind = 'campaign' AND e.type = 'delivery')
			AND NOT EXISTS (SELECT 1 FROM engagement_events g WHERE g.recipient_id = r.id AND g.type = '` + ev + `')`
	} else {
		from += ` CROSS JOIN LATERAL (SELECT ` + activitySQL[a] + ` AS at) a`
	}
	return `SELECT r.id, k.id, k.email, k.first_name, k.last_name, ` + at + `
		FROM ` + from + `
		WHERE r.campaign_id = $1 AND r.id > $2::bigint ` + where + `
		ORDER BY r.id`, nil
}

// csvAtColumn names the time column of each activity's CSV.
var csvAtColumn = map[Activity]string{
	Opened:       "first_opened_at",
	Clicked:      "first_clicked_at",
	Bounced:      "bounced_at",
	Complained:   "complained_at",
	Unsubscribed: "unsubscribed_at",
}

// RecipientsHeader is the header row of a recipient list's CSV: the
// contact, then, but for not_opened and not_clicked, when it happened.
func RecipientsHeader(a Activity) []string {
	h := []string{"email", "first_name", "last_name", "contact_id"}
	if c, ok := csvAtColumn[a]; ok {
		h = append(h, c)
	}
	return h
}

// csvPage is how many recipients RecipientsCSV reads at a time.
const csvPage = 1000

// RecipientsCSV writes the whole list of q.Activity (q.After and q.Limit
// are ignored) to w as CSV, a page at a time, and returns how many rows it
// wrote. Its errors before writing anything are those of Recipients. A cell
// a spreadsheet would read as a formula gets a ' in front.
func (s *Service) RecipientsCSV(ctx context.Context, accountID, campaignID string, q RecipientQuery, w io.Writer) (int, error) {
	q.After, q.Limit = 0, csvPage
	page, err := s.Recipients(ctx, accountID, campaignID, q)
	if err != nil {
		return 0, err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(RecipientsHeader(q.Activity)); err != nil {
		return 0, err
	}
	_, timed := csvAtColumn[q.Activity]
	n := 0
	for {
		for _, r := range page {
			row := []string{guardCell(r.Email), guardCell(deref(r.FirstName)), guardCell(deref(r.LastName)), r.ContactID}
			if timed {
				row = append(row, r.At.UTC().Format(time.RFC3339))
			}
			if err := cw.Write(row); err != nil {
				return n, err
			}
			n++
		}
		if len(page) < csvPage {
			break
		}
		q.After = page[len(page)-1].ID
		if page, err = s.Recipients(ctx, accountID, campaignID, q); err != nil {
			return n, err
		}
	}
	cw.Flush()
	return n, cw.Error()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// guardCell puts a ' before a cell a spreadsheet would read as a formula,
// as a contact export does.
func guardCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
