package contacts

import (
	"context"
	"errors"
	"fmt"
)

// Engagement filters (MG-113): what contacts did with past campaigns. They
// read the same events a campaign's report does, so they reach back as far
// as those are kept (12 months), and only campaigns sent with tracking on
// record opens and clicks.
type Engagement struct {
	// Opened, NotOpened, Clicked and NotClicked are campaign ids; the
	// contact must match each. Not opened and not clicked mean the contact
	// was delivered that campaign and didn't open or click it.
	Opened, NotOpened, Clicked, NotClicked []string
	// NeverOpened and NeverClicked mean the contact was delivered at least
	// one campaign sent with tracking on and opened or clicked none.
	NeverOpened, NeverClicked bool
	// OpenedWithinDays and ClickedWithinDays, from 1, mean the contact
	// opened or clicked any campaign in the last that many days; 0 doesn't
	// filter.
	OpenedWithinDays, ClickedWithinDays int
}

// MaxFilterCampaigns is how many campaigns the engagement filters may name
// in all, and MaxFilterDays how far back the within-days filters reach,
// as far as events are kept.
const (
	MaxFilterCampaigns = 20
	MaxFilterDays      = 365
)

// Campaigns are every campaign the filters name, once each.
func (e Engagement) Campaigns() []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range [][]string{e.Opened, e.NotOpened, e.Clicked, e.NotClicked} {
		for _, id := range l {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// IsZero says whether e filters nothing.
func (e Engagement) IsZero() bool {
	return len(e.Campaigns()) == 0 && !e.NeverOpened && !e.NeverClicked && e.OpenedWithinDays == 0 && e.ClickedWithinDays == 0
}

// FilterCampaignError is a filter naming a campaign it can't use.
type FilterCampaignError struct {
	CampaignID string
	// Reason is ErrFilterCampaignUnknown, ErrFilterTrackingOff or
	// ErrFilterArchived.
	Reason error
}

func (e *FilterCampaignError) Error() string { return fmt.Sprintf("%v: %s", e.Reason, e.CampaignID) }
func (e *FilterCampaignError) Unwrap() error { return e.Reason }

// Reasons a FilterCampaignError gives.
var (
	ErrFilterCampaignUnknown = errors.New("contacts: no such campaign")
	ErrFilterTrackingOff     = errors.New("contacts: the campaign wasn't tracked")
	ErrFilterArchived        = errors.New("contacts: the campaign's events are archived")
)

// CheckFilter checks the campaigns f's engagement filters name: each must
// be the account's, sent with tracking on, and not yet archived. Call it
// before List, Count or ExportCSV, which take any campaign id and match
// nobody for one that isn't usable.
func (s *Service) CheckFilter(ctx context.Context, accountID string, f Filter) error {
	ids := f.Engagement.Campaigns()
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.DB.Query(ctx, `
		SELECT c.id::text, c.tracking, EXISTS (SELECT 1 FROM campaign_reports s WHERE s.campaign_id = c.id)
		FROM campaigns c WHERE c.account_id = $1 AND c.id::text = ANY($2)`, accountID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	type state struct{ tracking, archived bool }
	found := map[string]state{}
	for rows.Next() {
		var id string
		var st state
		if err := rows.Scan(&id, &st.tracking, &st.archived); err != nil {
			return err
		}
		found[id] = st
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		st, ok := found[id]
		switch {
		case !ok:
			return &FilterCampaignError{CampaignID: id, Reason: ErrFilterCampaignUnknown}
		case !st.tracking:
			return &FilterCampaignError{CampaignID: id, Reason: ErrFilterTrackingOff}
		case st.archived:
			return &FilterCampaignError{CampaignID: id, Reason: ErrFilterArchived}
		}
	}
	return nil
}

// The pieces of the engagement conditions, on a query over contacts. A
// copy is the contact's campaign_recipients row r.
const (
	copyOf    = `FROM campaign_recipients r WHERE r.contact_id = contacts.id`
	delivered = `EXISTS (SELECT 1 FROM send_events e WHERE e.recipient_id = r.id AND e.kind = 'campaign' AND e.type = 'delivery')`
)

func did(typ string) string {
	return `EXISTS (SELECT 1 FROM engagement_events g WHERE g.recipient_id = r.id AND g.type = '` + typ + `')`
}

// where appends e's conditions, as Filter.where does.
func (e Engagement) where(add func(cond string, v any), raw func(cond string)) {
	for _, l := range []struct {
		ids  []string
		cond string
	}{
		{e.Opened, `EXISTS (SELECT 1 ` + copyOf + ` AND r.campaign_id = ?::uuid AND ` + did("open") + `)`},
		{e.Clicked, `EXISTS (SELECT 1 ` + copyOf + ` AND r.campaign_id = ?::uuid AND ` + did("click") + `)`},
		{e.NotOpened, `EXISTS (SELECT 1 ` + copyOf + ` AND r.campaign_id = ?::uuid AND ` + delivered + ` AND NOT ` + did("open") + `)`},
		{e.NotClicked, `EXISTS (SELECT 1 ` + copyOf + ` AND r.campaign_id = ?::uuid AND ` + delivered + ` AND NOT ` + did("click") + `)`},
	} {
		for _, id := range l.ids {
			add(l.cond, id)
		}
	}
	// Never: delivered a tracked campaign, and no open or click of any.
	never := func(typ string) string {
		return `EXISTS (SELECT 1 ` + copyOf + ` AND ` + delivered + `
				AND EXISTS (SELECT 1 FROM campaigns c WHERE c.id = r.campaign_id AND c.tracking))
			AND NOT EXISTS (SELECT 1 ` + copyOf + ` AND ` + did(typ) + `)`
	}
	if e.NeverOpened {
		raw(never("open"))
	}
	if e.NeverClicked {
		raw(never("click"))
	}
	within := func(typ string) string {
		return `EXISTS (SELECT 1 ` + copyOf + ` AND EXISTS (SELECT 1 FROM engagement_events g
				WHERE g.recipient_id = r.id AND g.type = '` + typ + `' AND g.occurred_at > now() - make_interval(days => ?::int)))`
	}
	if e.OpenedWithinDays > 0 {
		add(within("open"), e.OpenedWithinDays)
	}
	if e.ClickedWithinDays > 0 {
		add(within("click"), e.ClickedWithinDays)
	}
}

// Count is how many of the account's contacts match f.
func (s *Service) Count(ctx context.Context, accountID string, f Filter) (int, error) {
	q, args := f.where(`SELECT count(*) FROM contacts WHERE account_id = $1`, []any{accountID})
	var n int
	err := s.DB.QueryRow(ctx, q, args...).Scan(&n)
	return n, err
}
