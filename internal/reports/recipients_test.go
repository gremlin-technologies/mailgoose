package reports

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
)

func emails(rs []Recipient) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = strings.TrimSuffix(r.Email, "@example.com")
	}
	return strings.Join(out, ",")
}

// MG-112: who opened, clicked, bounced, complained or unsubscribed, and
// who didn't open or click.
func TestRecipientsByActivity(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), true)
	at := h.now.Add(-2 * time.Hour)
	h.events(t, at)
	// A later open by c1 doesn't move its first open.
	h.engagement(t, 0, 0, "", at.Add(time.Hour))

	for _, c := range []struct {
		a    Activity
		link int
		want string
	}{
		{Opened, 0, "c1,c2,c3"},
		{NotOpened, 0, "c4"},
		{Clicked, 0, "c1,c2"},
		{Clicked, 1, "c1,c2"},
		{Clicked, 2, "c1,c2"},
		{Clicked, 3, ""},
		{NotClicked, 0, "c3,c4"},
		{Bounced, 0, "c5"},
		{Complained, 0, "c2"},
		{Unsubscribed, 0, "c1,c2"},
	} {
		got, err := h.svc.Recipients(ctx, h.acc, h.campaign, RecipientQuery{Activity: c.a, Link: c.link})
		if err != nil {
			t.Fatalf("%s: %v", c.a, err)
		}
		if emails(got) != c.want {
			t.Errorf("%s link %d = %q, want %q", c.a, c.link, emails(got), c.want)
		}
		for _, r := range got {
			if r.ContactID == "" || (r.At == nil) != (c.a == NotOpened || c.a == NotClicked) {
				t.Errorf("%s row %+v", c.a, r)
			}
			if r.At != nil && !r.At.Equal(at) && c.a != Unsubscribed {
				t.Errorf("%s: %s at %v, want the first, %v", c.a, r.Email, r.At, at)
			}
		}
	}

	// Pages, in the order queued.
	first, err := h.svc.Recipients(ctx, h.acc, h.campaign, RecipientQuery{Activity: Opened, Limit: 2})
	if err != nil || emails(first) != "c1,c2" {
		t.Fatalf("first page = %q, %v", emails(first), err)
	}
	rest, err := h.svc.Recipients(ctx, h.acc, h.campaign, RecipientQuery{Activity: Opened, After: first[1].ID, Limit: 2})
	if err != nil || emails(rest) != "c3" {
		t.Fatalf("second page = %q, %v", emails(rest), err)
	}

	// A contact deleted since the send is left out.
	h.exec(t, `DELETE FROM contacts WHERE email = 'c2@example.com' AND account_id = $1`, h.acc)
	if got, err := h.svc.Recipients(ctx, h.acc, h.campaign, RecipientQuery{Activity: Opened}); err != nil || emails(got) != "c1,c3" {
		t.Errorf("after a delete = %q, %v", emails(got), err)
	}

	// Another account's campaign isn't found.
	var other string
	if err := h.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Other') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Recipients(ctx, other, h.campaign, RecipientQuery{Activity: Opened}); !errors.Is(err, campaigns.ErrNotFound) {
		t.Errorf("other account: %v", err)
	}
}

func TestRecipientsUntrackedAndArchived(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), false)
	h.sesEvent(t, 0, "delivery", "", h.now)
	h.sesEvent(t, 1, "bounce", "permanent/General", h.now)
	for _, a := range []Activity{Opened, NotOpened, Clicked, NotClicked} {
		if _, err := h.svc.Recipients(ctx, h.acc, h.campaign, RecipientQuery{Activity: a}); !errors.Is(err, ErrTrackingOff) {
			t.Errorf("%s untracked: %v", a, err)
		}
	}
	if got, err := h.svc.Recipients(ctx, h.acc, h.campaign, RecipientQuery{Activity: Bounced}); err != nil || emails(got) != "c2" {
		t.Errorf("bounced untracked = %q, %v", emails(got), err)
	}

	// Archived: launched 13 months ago and pruned.
	old := newHarness(t, time.Date(2025, 10, 1, 9, 0, 0, 0, time.UTC), true)
	old.events(t, time.Date(2025, 10, 2, 9, 0, 0, 0, time.UTC))
	if _, err := old.svc.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Activity{Opened, Unsubscribed} {
		if _, err := old.svc.Recipients(ctx, old.acc, old.campaign, RecipientQuery{Activity: a}); !errors.Is(err, ErrArchived) {
			t.Errorf("%s archived: %v", a, err)
		}
	}
	var buf bytes.Buffer
	if _, err := old.svc.RecipientsCSV(ctx, old.acc, old.campaign, RecipientQuery{Activity: Opened}, &buf); !errors.Is(err, ErrArchived) || buf.Len() != 0 {
		t.Errorf("archived CSV: %v, %q", err, buf.String())
	}
}

func TestRecipientsCSV(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), true)
	at := time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC)
	h.events(t, at)
	h.exec(t, `UPDATE contacts SET first_name = '=cmd', last_name = 'Baker' WHERE email = 'c1@example.com'`)

	var buf bytes.Buffer
	n, err := h.svc.RecipientsCSV(ctx, h.acc, h.campaign, RecipientQuery{Activity: Clicked, Link: 1}, &buf)
	if err != nil || n != 2 {
		t.Fatalf("CSV: %d, %v", n, err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if lines[0] != "email,first_name,last_name,contact_id,first_clicked_at" || len(lines) != 3 ||
		!strings.HasPrefix(lines[1], "c1@example.com,'=cmd,Baker,") || !strings.HasSuffix(lines[1], ",2026-11-02T10:30:00Z") ||
		!strings.HasPrefix(lines[2], "c2@example.com,,,") {
		t.Errorf("CSV =\n%s", buf.String())
	}

	buf.Reset()
	if n, err := h.svc.RecipientsCSV(ctx, h.acc, h.campaign, RecipientQuery{Activity: NotOpened}, &buf); err != nil || n != 1 ||
		!strings.HasPrefix(buf.String(), "email,first_name,last_name,contact_id\nc4@example.com,,,") {
		t.Errorf("not_opened CSV: %d, %v\n%s", n, err, buf.String())
	}
}
