package contacts

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// engagementWorld is the contacts and campaigns the engagement filter tests
// share: campaign A and B tracked, U not.
type engagementWorld struct {
	h       *harness
	a, b, u string
}

func (w *engagementWorld) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := w.h.svc.DB.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

func (w *engagementWorld) campaign(t *testing.T, account, name string, tracking bool) string {
	t.Helper()
	var id string
	if err := w.h.svc.DB.QueryRow(context.Background(), `INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at, tracking)
		VALUES ($1, $2, true, 'sent', now() - interval '60 days', $3) RETURNING id`, account, name, tracking).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// send gives c a copy of campaign and, for "delivery" or "bounce", SES's
// event; then each of did ("open", "click") at ago.
func (w *engagementWorld) send(t *testing.T, campaign string, c *Contact, outcome string, ago time.Duration, did ...string) {
	t.Helper()
	var r int64
	if err := w.h.svc.DB.QueryRow(context.Background(), `INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status)
		VALUES ($1, $2, $3, 'sent') RETURNING id`, w.h.acc, campaign, c.ID).Scan(&r); err != nil {
		t.Fatal(err)
	}
	subtype := ""
	if outcome == "bounce" {
		subtype = "permanent/General"
	}
	w.exec(t, `INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, subtype, occurred_at)
		VALUES (gen_random_uuid()::text, $1, $2, $3, 'm', 'campaign', $4, nullif($5, ''), now())`, w.h.acc, campaign, r, outcome, subtype)
	for _, typ := range did {
		var link *int
		if typ == "click" {
			one := 1
			link = &one
		}
		w.exec(t, `INSERT INTO engagement_events (account_id, campaign_id, recipient_id, type, link, occurred_at)
			VALUES ($1, $2, $3, $4, $5, now() - make_interval(secs => $6))`, w.h.acc, campaign, r, typ, link, ago.Seconds())
	}
}

func newEngagementWorld(t *testing.T) *engagementWorld {
	t.Helper()
	w := &engagementWorld{h: newHarness(t)}
	w.a = w.campaign(t, w.h.acc, "A", true)
	w.b = w.campaign(t, w.h.acc, "B", true)
	w.u = w.campaign(t, w.h.acc, "U", false)
	c := map[string]*Contact{}
	for _, name := range []string{"ann", "bo", "cy", "di", "ed", "fa", "hal"} {
		c[name] = w.h.add(t, name+"@example.com", StatusSubscribed)
	}
	const hour, day = time.Hour, 24 * time.Hour
	w.send(t, w.a, c["ann"], "delivery", hour, "open", "click")
	w.send(t, w.b, c["ann"], "delivery", 0)
	w.send(t, w.a, c["bo"], "delivery", 40*day, "open")
	w.send(t, w.a, c["cy"], "delivery", 0)
	w.send(t, w.a, c["di"], "bounce", 0)
	// ed got nothing; fa only a campaign that wasn't tracked.
	w.send(t, w.u, c["fa"], "delivery", 0)
	// hal opened A, was deleted, and is back as a new contact.
	w.send(t, w.a, c["hal"], "delivery", hour, "open")
	if err := w.h.svc.Delete(context.Background(), w.h.acc, c["hal"].ID); err != nil {
		t.Fatal(err)
	}
	w.h.add(t, "hal@example.com", StatusSubscribed)
	return w
}

func (w *engagementWorld) names(t *testing.T, f Filter) string {
	t.Helper()
	cs, err := w.h.svc.List(context.Background(), w.h.acc, f, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		out = append(out, strings.TrimSuffix(c.Email, "@example.com"))
	}
	slices.Sort(out)
	n, err := w.h.svc.Count(context.Background(), w.h.acc, f)
	if err != nil || n != len(out) {
		t.Errorf("count %d, %v; listed %d", n, err, len(out))
	}
	return strings.Join(out, ",")
}

func TestEngagementFilters(t *testing.T) {
	w := newEngagementWorld(t)
	for _, c := range []struct {
		name string
		e    Engagement
		want string
	}{
		{"opened A", Engagement{Opened: []string{w.a}}, "ann,bo"},
		{"opened A and B", Engagement{Opened: []string{w.a, w.b}}, ""},
		{"clicked A", Engagement{Clicked: []string{w.a}}, "ann"},
		// di's copy bounced, so it wasn't delivered.
		{"not opened A", Engagement{NotOpened: []string{w.a}}, "cy"},
		{"not opened B", Engagement{NotOpened: []string{w.b}}, "ann"},
		{"not clicked A", Engagement{NotClicked: []string{w.a}}, "bo,cy"},
		{"opened A, not clicked A", Engagement{Opened: []string{w.a}, NotClicked: []string{w.a}}, "bo"},
		// ed got nothing, di nothing delivered, fa nothing tracked; the
		// new hal has no history.
		{"never opened", Engagement{NeverOpened: true}, "cy"},
		{"never clicked", Engagement{NeverClicked: true}, "bo,cy"},
		{"opened in 7 days", Engagement{OpenedWithinDays: 7}, "ann"},
		{"opened in 60 days", Engagement{OpenedWithinDays: 60}, "ann,bo"},
		{"clicked in 7 days", Engagement{ClickedWithinDays: 7}, "ann"},
		{"nothing", Engagement{}, "ann,bo,cy,di,ed,fa,hal"},
	} {
		if got := w.names(t, Filter{Engagement: c.e}); got != c.want {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}
	// With the older filters, all must match.
	if got := w.names(t, Filter{Email: "bo", Engagement: Engagement{NeverClicked: true}}); got != "bo" {
		t.Errorf("email and never clicked = %q", got)
	}
	w.exec(t, `UPDATE contacts SET status = 'unsubscribed' WHERE email = 'bo@example.com'`)
	if got := w.names(t, Filter{Status: StatusSubscribed, Engagement: Engagement{NeverClicked: true}}); got != "cy" {
		t.Errorf("subscribed and never clicked = %q", got)
	}

	// The export takes them too.
	var buf strings.Builder
	if n, err := w.h.svc.ExportCSV(context.Background(), w.h.acc, Filter{Engagement: Engagement{Clicked: []string{w.a}}}, &buf); err != nil || n != 1 ||
		!strings.Contains(buf.String(), "\nann@example.com,") {
		t.Errorf("export: %d, %v\n%s", n, err, buf.String())
	}
}

func TestCheckFilter(t *testing.T) {
	w := newEngagementWorld(t)
	ctx := context.Background()
	theirs := w.campaign(t, w.h.acc2, "Theirs", true)
	old := w.campaign(t, w.h.acc, "Old", true)
	w.exec(t, `INSERT INTO campaign_reports (campaign_id, account_id, report) VALUES ($1, $2, '{}')`, old, w.h.acc)

	if err := w.h.svc.CheckFilter(ctx, w.h.acc, Filter{Engagement: Engagement{Opened: []string{w.a}, NotClicked: []string{w.b, w.a}}}); err != nil {
		t.Errorf("usable: %v", err)
	}
	if err := w.h.svc.CheckFilter(ctx, w.h.acc, Filter{Engagement: Engagement{NeverOpened: true}}); err != nil {
		t.Errorf("no campaigns: %v", err)
	}
	for _, c := range []struct {
		id   string
		want error
	}{
		{"7a1b5f7e-0000-4000-8000-000000000000", ErrFilterCampaignUnknown},
		{theirs, ErrFilterCampaignUnknown},
		{w.u, ErrFilterTrackingOff},
		{old, ErrFilterArchived},
	} {
		err := w.h.svc.CheckFilter(ctx, w.h.acc, Filter{Engagement: Engagement{Clicked: []string{w.a}, NotOpened: []string{c.id}}})
		var fe *FilterCampaignError
		if !errors.Is(err, c.want) || !errors.As(err, &fe) || fe.CampaignID != c.id {
			t.Errorf("%s: %v, want %v", c.id, err, c.want)
		}
	}
}

// MG-113: each filter answers in under a second over 50,000 contacts and
// 200,000 events.
func TestEngagementFilterSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 50,000 contacts")
	}
	w := &engagementWorld{h: newHarness(t)}
	var camps []string
	for i := range 10 {
		camps = append(camps, w.campaign(t, w.h.acc, "C"+string(rune('0'+i)), true))
	}
	// 50,000 contacts, each sent 2 of the 10 campaigns (100,000 copies),
	// every copy delivered (100,000 events), and 100,000 opens and
	// clicks between them.
	w.exec(t, `INSERT INTO contacts (account_id, email, status, source, created_at, status_changed_at)
		SELECT $1, 'p' || i || '@example.com', 'subscribed', 'api', now(), now() FROM generate_series(1, 50000) i`, w.h.acc)
	w.exec(t, `INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status)
		SELECT $1, ($2::uuid[])[1 + (n + k * 3) % 10], k.id, 'sent'
		FROM (SELECT id, row_number() OVER (ORDER BY id) AS n FROM contacts WHERE account_id = $1) k, generate_series(0, 1) AS k2(k)`,
		w.h.acc, camps)
	w.exec(t, `INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, occurred_at)
		SELECT 'e' || r.id, $1, r.campaign_id, r.id, 'm', 'campaign', 'delivery', now() FROM campaign_recipients r WHERE r.account_id = $1`, w.h.acc)
	w.exec(t, `INSERT INTO engagement_events (account_id, campaign_id, recipient_id, type, link, occurred_at)
		SELECT $1, r.campaign_id, r.id, t.type, CASE WHEN t.type = 'click' THEN 1 END, now() - make_interval(days => (r.id % 90)::int)
		FROM campaign_recipients r
		JOIN (VALUES ('open'), ('click')) t(type) ON t.type = 'open' AND r.id % 3 > 0 OR t.type = 'click' AND r.id % 3 = 0
		WHERE r.account_id = $1`, w.h.acc)
	w.exec(t, `ANALYZE contacts, campaign_recipients, send_events, engagement_events`)

	ctx := context.Background()
	for _, c := range []struct {
		name string
		e    Engagement
	}{
		{"opened", Engagement{Opened: []string{camps[0]}}},
		{"not opened", Engagement{NotOpened: []string{camps[1]}}},
		{"clicked", Engagement{Clicked: []string{camps[2]}}},
		{"not clicked", Engagement{NotClicked: []string{camps[3]}}},
		{"never opened", Engagement{NeverOpened: true}},
		{"never clicked", Engagement{NeverClicked: true}},
		{"opened in 30 days", Engagement{OpenedWithinDays: 30}},
		{"clicked in 30 days", Engagement{ClickedWithinDays: 30}},
		{"all of them", Engagement{Opened: []string{camps[0]}, NotClicked: []string{camps[0]}, OpenedWithinDays: 60}},
	} {
		f := Filter{Engagement: c.e}
		start := time.Now()
		n, err := w.h.svc.Count(ctx, w.h.acc, f)
		took := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		if _, err := w.h.svc.List(ctx, w.h.acc, f, nil, 100); err != nil {
			t.Fatal(err)
		}
		listed := time.Since(start)
		t.Logf("%s: %d contacts, count %v, first page %v", c.name, n, took, listed)
		if took >= time.Second || listed >= time.Second {
			t.Errorf("%s took %v (count) and %v (page), want under a second", c.name, took, listed)
		}
	}
}
