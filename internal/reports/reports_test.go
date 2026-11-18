package reports

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

type harness struct {
	db       *pgxpool.Pool
	svc      *Service
	now      time.Time
	acc      string
	campaign string
	rcpts    []int64
}

// newHarness is a campaign launched at launched to five contacts, all
// sent, with 5 credits reserved and 1 given back.
func newHarness(t *testing.T, launched time.Time, tracking bool) *harness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	h := &harness{db: db, now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	h.svc = &Service{DB: db, Now: func() time.Time { return h.now }}
	if err := db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Bakery') RETURNING id`).Scan(&h.acc); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at, tracking)
		VALUES ($1, 'Bread', true, 'sent', $2, $3) RETURNING id`, h.acc, launched, tracking).Scan(&h.campaign); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ctx, `
		WITH k AS (
			INSERT INTO contacts (account_id, email, status, source)
			SELECT $1, 'c' || i || '@example.com', 'subscribed', 'api' FROM generate_series(1, 6) i RETURNING id, email
		)
		INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status)
		SELECT $1, $2, id, CASE WHEN email = 'c6@example.com' THEN 'skipped' ELSE 'sent' END FROM k ORDER BY email RETURNING id`,
		h.acc, h.campaign)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		h.rcpts = append(h.rcpts, id)
	}
	rows.Close()
	h.exec(t, `INSERT INTO credit_transactions (account_id, type, amount, ref) VALUES ($1, 'reserve', -6, 'campaign:' || $2), ($1, 'release', 1, 'campaign:' || $2)`,
		h.acc, h.campaign)
	return h
}

func (h *harness) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := h.db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (h *harness) sesEvent(t *testing.T, i int, typ, subtype string, at time.Time) {
	t.Helper()
	h.exec(t, `INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, subtype, occurred_at)
		VALUES (gen_random_uuid()::text, $1, $2, $3, 'm', 'campaign', $4, nullif($5, ''), $6)`, h.acc, h.campaign, h.rcpts[i], typ, subtype, at)
}

func (h *harness) engagement(t *testing.T, i int, link int, url string, at time.Time) {
	t.Helper()
	typ := "open"
	var l *int
	var u *string
	if link > 0 {
		typ, l, u = "click", &link, &url
	}
	h.exec(t, `INSERT INTO engagement_events (account_id, campaign_id, recipient_id, type, link, url, occurred_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		h.acc, h.campaign, h.rcpts[i], typ, l, u, at)
}

// events is a campaign's worth: 4 delivered (one twice), 1 hard bounce,
// 1 soft bounce that then delivered, a complaint, 2 unsubscribes (one
// twice), 3 people opened, 2 clicked 4 times over two links, one of them
// personalized.
func (h *harness) events(t *testing.T, at time.Time) {
	t.Helper()
	for _, i := range []int{0, 1, 2, 3, 3} {
		h.sesEvent(t, i, "delivery", "", at)
	}
	h.sesEvent(t, 4, "bounce", "permanent/General", at)
	h.sesEvent(t, 3, "bounce", "transient/MailboxFull", at)
	h.sesEvent(t, 1, "complaint", "abuse", at)
	h.exec(t, `INSERT INTO unsubscribes (account_id, contact_id, campaign_id, source)
		SELECT account_id, contact_id, campaign_id, 'link' FROM campaign_recipients WHERE id = ANY($1)`, []int64{h.rcpts[0], h.rcpts[1], h.rcpts[1]})
	for _, i := range []int{0, 0, 1, 2} {
		h.engagement(t, i, 0, "", at)
	}
	h.engagement(t, 0, 1, "https://patsbakery.com/order", at)
	h.engagement(t, 0, 1, "https://patsbakery.com/order", at)
	h.engagement(t, 1, 1, "https://patsbakery.com/order", at)
	h.engagement(t, 1, 2, "https://patsbakery.com/u?who=c2@example.com", at)
	h.engagement(t, 0, 2, "https://patsbakery.com/u?who=c1@example.com", at)
}

func check(t *testing.T, r *Report) {
	t.Helper()
	if r.Status != "sent" || !r.Tracking || r.Recipients != 5 || r.Delivered != 4 || r.Bounced != 1 || r.Complaints != 1 ||
		r.Unsubscribes != 2 || r.CreditsUsed != 5 {
		t.Errorf("counts %+v", r)
	}
	if r.Opens == nil || r.Opens.Unique != 3 || !r.Opens.Estimated || r.Clicks == nil || r.Clicks.Unique != 2 || r.Clicks.Total != 5 {
		t.Errorf("opens %+v clicks %+v", r.Opens, r.Clicks)
	}
	want := []Link{
		{Position: 1, URL: "https://patsbakery.com/order", UniqueClicks: 2, TotalClicks: 3},
		{Position: 2, URL: "https://patsbakery.com", Varies: true, UniqueClicks: 2, TotalClicks: 2},
	}
	if len(r.Links) != len(want) {
		t.Fatalf("links %+v", r.Links)
	}
	for i := range want {
		if r.Links[i] != want[i] {
			t.Errorf("link %d: %+v, want %+v", i, r.Links[i], want[i])
		}
	}
}

func TestCampaignReport(t *testing.T) {
	h := newHarness(t, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), true)
	h.events(t, h.now.Add(-time.Hour))
	r, err := h.svc.Campaign(context.Background(), h.acc, h.campaign)
	if err != nil {
		t.Fatal(err)
	}
	check(t, r)
	if r.Archived || !r.AsOf.Equal(h.now) {
		t.Errorf("archived %v as of %v", r.Archived, r.AsOf)
	}

	// Another account's id, or none, is not found.
	if _, err := h.svc.Campaign(context.Background(), "00000000-0000-4000-8000-000000000000", h.campaign); !errors.Is(err, campaigns.ErrNotFound) {
		t.Errorf("other account: %v", err)
	}
}

func TestUntrackedReport(t *testing.T) {
	h := newHarness(t, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), false)
	r, err := h.svc.Campaign(context.Background(), h.acc, h.campaign)
	if err != nil {
		t.Fatal(err)
	}
	if r.Opens != nil || r.Clicks != nil || r.Links == nil || len(r.Links) != 0 {
		t.Errorf("untracked: %+v", r)
	}
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, time.Date(2025, 10, 1, 9, 0, 0, 0, time.UTC), true)
	h.events(t, time.Date(2025, 10, 2, 9, 0, 0, 0, time.UTC))
	before, err := h.svc.Campaign(ctx, h.acc, h.campaign)
	if err != nil {
		t.Fatal(err)
	}
	// Mail with no campaign: one old event, one recent.
	h.exec(t, `INSERT INTO send_events (event_key, message_id, kind, type, occurred_at) VALUES ('old', 'm', 'system', 'delivery', $1), ('new', 'm', 'system', 'delivery', $2)`,
		h.now.AddDate(-1, 0, -1), h.now.AddDate(0, -11, 0))

	// A recent campaign keeps its events.
	recent := newHarnessIn(t, h, time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	recent.events(t, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC))

	p, err := h.svc.Prune(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The old campaign's 8 SES and 9 engagement events, and the old system event.
	if p.Archived != 1 || p.Events != 18 {
		t.Errorf("pruned %+v", p)
	}
	var left int
	_ = h.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM send_events WHERE campaign_id = $1) + (SELECT count(*) FROM engagement_events WHERE campaign_id = $1)`, h.campaign).Scan(&left)
	if left != 0 {
		t.Errorf("%d events left", left)
	}
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM send_events WHERE event_key IN ('old', 'new')`).Scan(&left)
	if left != 1 {
		t.Errorf("%d system events left, want the recent one", left)
	}
	_ = h.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM send_events WHERE campaign_id = $1) + (SELECT count(*) FROM engagement_events WHERE campaign_id = $1)`, recent.campaign).Scan(&left)
	if left != 17 {
		t.Errorf("the recent campaign has %d events, want 17", left)
	}

	// The report reads the same, from what was saved.
	after, err := h.svc.Campaign(ctx, h.acc, h.campaign)
	if err != nil {
		t.Fatal(err)
	}
	check(t, after)
	if !after.Archived || !after.AsOf.Equal(h.now) || after.Recipients != before.Recipients {
		t.Errorf("archived report %+v", after)
	}

	// Pruning again does nothing.
	if p, err := h.svc.Prune(ctx); err != nil || p != (Pruned{}) {
		t.Errorf("second prune %+v %v", p, err)
	}
}

// newHarnessIn adds a second campaign, launched at launched, to h's
// database and account.
func newHarnessIn(t *testing.T, h *harness, launched time.Time) *harness {
	t.Helper()
	ctx := context.Background()
	o := &harness{db: h.db, svc: h.svc, now: h.now, acc: h.acc}
	if err := h.db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at) VALUES ($1, 'Cake', true, 'sent', $2) RETURNING id`,
		h.acc, launched).Scan(&o.campaign); err != nil {
		t.Fatal(err)
	}
	rows, err := h.db.Query(ctx, `
		INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status)
		SELECT $1, $2, contact_id, status FROM campaign_recipients WHERE campaign_id = $3 ORDER BY id RETURNING id`, h.acc, o.campaign, h.campaign)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		o.rcpts = append(o.rcpts, id)
	}
	return o
}
