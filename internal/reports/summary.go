package reports

import (
	"context"
)

// Summary is a launched campaign's headline numbers for the campaign list
// (MG-111): its report's counts, and each as a share. Opens and clicks are
// nil, with their rates, when the campaign wasn't tracked.
type Summary struct {
	Recipients int  `json:"recipients"`
	Delivered  int  `json:"delivered"`
	Bounced    int  `json:"bounced"`
	Opens      *int `json:"opens"`
	Clicks     *int `json:"clicks"`
	// DeliveredRate and BounceRate are of the copies sent; OpenRate and
	// ClickRate are of the copies delivered. Each is from 0 to 1, and 0
	// when there's nothing to divide by.
	DeliveredRate float64  `json:"delivered_rate"`
	BounceRate    float64  `json:"bounce_rate"`
	OpenRate      *float64 `json:"open_rate"`
	ClickRate     *float64 `json:"click_rate"`
	// Archived means these are the numbers saved 12 months after launch.
	Archived bool `json:"archived"`
}

// Summary is the report's headline numbers, or nil for a campaign that
// hasn't been launched.
func (r *Report) Summary() *Summary {
	if r == nil || r.LaunchedAt == nil {
		return nil
	}
	sm := &Summary{Recipients: r.Recipients, Delivered: r.Delivered, Bounced: r.Bounced, Archived: r.Archived,
		DeliveredRate: rate(r.Delivered, r.Recipients), BounceRate: rate(r.Bounced, r.Recipients)}
	if r.Opens != nil {
		n, f := r.Opens.Unique, rate(r.Opens.Unique, r.Delivered)
		sm.Opens, sm.OpenRate = &n, &f
	}
	if r.Clicks != nil {
		n, f := r.Clicks.Unique, rate(r.Clicks.Unique, r.Delivered)
		sm.Clicks, sm.ClickRate = &n, &f
	}
	return sm
}

func rate(n, of int) float64 {
	if of <= 0 {
		return 0
	}
	return float64(n) / float64(of)
}

// Summaries are the summaries of the account's launched campaigns among
// ids, by campaign id, worked out in one query from the same counts as the
// report. A campaign that hasn't been launched, or isn't the account's,
// has none.
func (s *Service) Summaries(ctx context.Context, accountID string, ids []string) (map[string]*Summary, error) {
	out := make(map[string]*Summary, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `
		SELECT c.id, `+countColumns+`
		FROM campaigns c LEFT JOIN campaign_reports s ON s.campaign_id = c.id
		WHERE c.account_id = $1 AND c.id = ANY($2::uuid[]) AND c.launched_at IS NOT NULL`, accountID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		r, err := s.scanCounts(rows, "", true, &id)
		if err != nil {
			return nil, err
		}
		out[id] = r.Summary()
	}
	return out, rows.Err()
}
