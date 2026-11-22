package sending

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/segments"
)

// MG-115: a segment of the contacts who clicked one campaign gets the next
// one, worked out at launch, and nobody else does.
func TestLaunchToSegment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.buy(t, 10)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := h.db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Sam is in the harness already; Ann and Cy click the last campaign,
	// Bo only gets it, and Cy has unsubscribed since.
	ids := map[string]string{}
	for _, c := range []struct{ name, status string }{{"ann", "subscribed"}, {"bo", "subscribed"}, {"cy", "unsubscribed"}} {
		var id string
		if err := h.db.QueryRow(ctx, `INSERT INTO contacts (account_id, email, status, source) VALUES ($1, $2, $3, 'api') RETURNING id`,
			h.acc, c.name+"@example.com", c.status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids[c.name] = id
	}
	var last string
	if err := h.db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at, tracking)
		VALUES ($1, 'Last week', true, 'sent', now() - interval '3 days', true) RETURNING id`, h.acc).Scan(&last); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"ann", "bo", "cy"} {
		var r int64
		if err := h.db.QueryRow(ctx, `INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status)
			VALUES ($1, $2, $3, 'sent') RETURNING id`, h.acc, last, ids[who]).Scan(&r); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, occurred_at)
			VALUES (gen_random_uuid()::text, $1, $2, $3, 'm', 'campaign', 'delivery', now())`, h.acc, last, r)
		if who != "bo" {
			exec(`INSERT INTO engagement_events (account_id, campaign_id, recipient_id, type, link, occurred_at)
				VALUES ($1, $2, $3, 'click', 1, now() - interval '1 hour')`, h.acc, last, r)
		}
	}
	segs := &segments.Service{DB: h.db, Contacts: &contacts.Service{DB: h.db}}
	sg, err := segs.Create(ctx, h.acc, "Clicked last week", segments.Conditions{Clicked: []string{last}})
	if err != nil {
		t.Fatal(err)
	}
	if h.campaign, err = h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{
		Recipients: &campaigns.Recipients{SegmentID: sg.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	sum := h.summary(t)
	if sum.Summary.RecipientCount != 1 {
		t.Errorf("summary counts %d", sum.Summary.RecipientCount)
	}
	l, err := h.launch(sum.Code)
	if err != nil {
		t.Fatal(err)
	}
	if l.RecipientCount != 1 || l.CreditsReserved != 1 {
		t.Errorf("launched to %d, %d credits", l.RecipientCount, l.CreditsReserved)
	}
	var got []string
	rows, err := h.db.Query(ctx, `SELECT contact_id::text FROM campaign_recipients WHERE campaign_id = $1`, h.campaign.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		got = append(got, id)
	}
	rows.Close()
	if len(got) != 1 || got[0] != ids["ann"] {
		t.Errorf("recipients %v, want only Ann (%s)", got, ids["ann"])
	}

	// Fixed at launch: the segment can be deleted once the campaign is
	// sending, and the campaign keeps its recipients.
	if err := segs.Delete(ctx, h.acc, sg.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1`, h.campaign.ID).Scan(&n)
	if n != 1 {
		t.Errorf("%d recipients after the segment was deleted", n)
	}
}

// A segment with a condition it can't use stops the launch, at the summary
// and at the confirm, naming the condition.
func TestLaunchToInvalidSegment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.buy(t, 10)
	var tag string
	if err := h.db.QueryRow(ctx, `INSERT INTO tags (account_id, name) VALUES ($1, 'regulars') RETURNING id`, h.acc).Scan(&tag); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO contact_tags (contact_id, tag_id) SELECT id, $2 FROM contacts WHERE account_id = $1`, h.acc, tag); err != nil {
		t.Fatal(err)
	}
	segs := &segments.Service{DB: h.db, Contacts: &contacts.Service{DB: h.db}}
	sg, err := segs.Create(ctx, h.acc, "Regulars", segments.Conditions{TagIDs: []string{tag}})
	if err != nil {
		t.Fatal(err)
	}
	if h.campaign, err = h.svc.Campaigns.Update(ctx, h.acc, h.campaign.ID, campaigns.Changes{
		Recipients: &campaigns.Recipients{SegmentID: sg.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	code := h.summary(t).Code
	if _, err := h.db.Exec(ctx, `DELETE FROM tags WHERE id = $1`, tag); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"confirm", "summary"} {
		c := code
		if step == "summary" {
			c = ""
		}
		_, err := h.launch(c)
		if got := problemCodes(err); got != SegmentInvalid {
			t.Fatalf("%s: %s", step, got)
		}
		var nr *NotReadyError
		if !errors.As(err, &nr) {
			t.Fatalf("%s: %v", step, err)
		}
		p := nr.Problems[0]
		if !strings.Contains(p.Message, `"Regulars"`) || !strings.Contains(p.Message, "tag "+tag+" in tag_ids was deleted") ||
			p.Details["segment_id"] != sg.ID {
			t.Errorf("%s: %+v", step, p)
		}
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaign_recipients WHERE campaign_id = $1`, h.campaign.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d recipients after a refused launch", n)
	}
}
