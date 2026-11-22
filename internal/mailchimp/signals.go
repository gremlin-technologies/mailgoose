package mailchimp

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Signals are what an admin reads to judge a Mailchimp import (MG-77), in
// `mailgoose admin account` for a held one and in the daily abuse digest:
// where it came from, its audiences, its members by Mailchimp status, and
// how many of the subscribed ones have a Mailchimp opt-in time. They decide
// nothing: the import's holds are the same as any other import's.
type Signals struct {
	// Source is "file" for an uploaded export and "oauth" for one fetched
	// from a connected Mailchimp account.
	Source string `json:"source"`
	// FileName is the uploaded file's name; nil for a fetch.
	FileName  *string          `json:"file_name"`
	Audiences []SignalAudience `json:"audiences"`
	// ByStatus counts every audience's members by Mailchimp status, and
	// Members is their total.
	ByStatus StatusCounts `json:"by_status"`
	Members  int          `json:"members"`
	// CleanedShare and UnsubscribedShare are of Members; OptinShare is of
	// the subscribed members, those with a Mailchimp opt-in time. Each is
	// from 0 to 1.
	CleanedShare      float64 `json:"cleaned_share"`
	UnsubscribedShare float64 `json:"unsubscribed_share"`
	WithOptinTime     int     `json:"with_optin_time"`
	OptinShare        float64 `json:"optin_share"`
	// NewestOptin is the newest Mailchimp opt-in time; nil when none had one.
	NewestOptin *time.Time `json:"newest_optin"`
}

// SignalAudience is one audience in Signals.
type SignalAudience struct {
	Name    string `json:"name"`
	Members int    `json:"members"`
}

// StatusCounts counts members by Mailchimp status.
type StatusCounts struct {
	Subscribed    int `json:"subscribed"`
	Unsubscribed  int `json:"unsubscribed"`
	Cleaned       int `json:"cleaned"`
	Pending       int `json:"pending"`
	Transactional int `json:"transactional"`
	Archived      int `json:"archived"`
}

func (c *StatusCounts) add(o StatusCounts) {
	c.Subscribed += o.Subscribed
	c.Unsubscribed += o.Unsubscribed
	c.Cleaned += o.Cleaned
	c.Pending += o.Pending
	c.Transactional += o.Transactional
	c.Archived += o.Archived
}

// ParseSignals reads the signals kept on a confirmed upload's row
// (mailchimp_bundles.signals, copied from its preview) and works out the
// totals and shares. It gives nil for an upload confirmed before they were
// kept.
func ParseSignals(fileName *string, raw []byte) (*Signals, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var kept struct {
		Source    string `json:"source"`
		Audiences []struct {
			Name     string       `json:"name"`
			Members  int          `json:"members"`
			ByStatus StatusCounts `json:"by_status"`
		} `json:"audiences"`
		WithOptinTime int        `json:"with_optin_time"`
		NewestOptin   *time.Time `json:"newest_optin"`
	}
	if err := json.Unmarshal(raw, &kept); err != nil {
		return nil, err
	}
	s := &Signals{Source: "file", FileName: fileName, Audiences: []SignalAudience{},
		WithOptinTime: kept.WithOptinTime, NewestOptin: kept.NewestOptin}
	if kept.Source == "connected_account" {
		s.Source, s.FileName = "oauth", nil
	}
	for _, a := range kept.Audiences {
		s.Audiences = append(s.Audiences, SignalAudience{Name: a.Name, Members: a.Members})
		s.ByStatus.add(a.ByStatus)
		s.Members += a.Members
	}
	s.CleanedShare = share(s.ByStatus.Cleaned, s.Members)
	s.UnsubscribedShare = share(s.ByStatus.Unsubscribed, s.Members)
	s.OptinShare = share(s.WithOptinTime, s.ByStatus.Subscribed)
	return s, nil
}

func share(n, of int) float64 {
	if of == 0 {
		return 0
	}
	return float64(n) / float64(of)
}

// Lines are the signals as text, one line each, for the digest:
//
//	From file mailchimp-export.zip: Newsletter (1,200), Wholesale (40).
//	Members: 900 subscribed, 200 unsubscribed, 100 cleaned, 40 pending.
//	Cleaned 8.0%, unsubscribed 16.1%; 95.0% of subscribed have a Mailchimp opt-in time, the newest 2026-11-01.
func (s Signals) Lines() []string {
	from := "From a connected Mailchimp account"
	if s.Source == "file" {
		from = "From an uploaded file"
		if s.FileName != nil && *s.FileName != "" {
			from = "From file " + *s.FileName
		}
	}
	var auds []string
	for _, a := range s.Audiences {
		auds = append(auds, fmt.Sprintf("%s (%s)", a.Name, commas(a.Members)))
	}
	if len(auds) == 0 {
		auds = []string{"no audiences"}
	}
	var by []string
	for _, c := range []struct {
		n    int
		name string
	}{
		{s.ByStatus.Subscribed, "subscribed"}, {s.ByStatus.Unsubscribed, "unsubscribed"}, {s.ByStatus.Cleaned, "cleaned"},
		{s.ByStatus.Pending, "pending"}, {s.ByStatus.Transactional, "transactional"}, {s.ByStatus.Archived, "archived"},
	} {
		if c.n > 0 || c.name == "subscribed" {
			by = append(by, fmt.Sprintf("%s %s", commas(c.n), c.name))
		}
	}
	optin := fmt.Sprintf("%s of subscribed have a Mailchimp opt-in time", pct(s.OptinShare))
	if s.NewestOptin != nil {
		optin += ", the newest " + s.NewestOptin.UTC().Format(time.DateOnly)
	}
	return []string{
		from + ": " + strings.Join(auds, ", ") + ".",
		"Members: " + strings.Join(by, ", ") + ".",
		fmt.Sprintf("Cleaned %s, unsubscribed %s; %s.", pct(s.CleanedShare), pct(s.UnsubscribedShare), optin),
	}
}

func pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }
