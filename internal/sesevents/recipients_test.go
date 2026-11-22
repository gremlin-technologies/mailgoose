package sesevents

import (
	"context"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/reports"
	"github.com/gremlin-technologies/mailgoose/internal/tracking"
)

// MG-112: a campaign goes out through the fake sender to three contacts;
// SES reports two deliveries and a hard bounce, one reader opens and
// clicks, another unsubscribes, and each recipient list says who.
func TestRecipientListsAfterASend(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	ann, bo := "ann@patsbakery.com", "bo@patsbakery.com"
	rcpts := map[string]int64{sam: h.rcpt}
	for _, addr := range []string{ann, bo} {
		id := h.addContact(t, addr, "subscribed")
		var r int64
		if err := h.db.QueryRow(ctx, `INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status) VALUES ($1, $2, $3, 'sent') RETURNING id`,
			h.acc, h.campaign, id).Scan(&r); err != nil {
			t.Fatal(err)
		}
		rcpts[addr] = r
	}

	// Send each copy, and keep its message id as the pipeline does.
	fake := &mail.Fake{}
	ids := map[string]string{}
	for addr, r := range rcpts {
		id, err := fake.Send(ctx, mail.Message{
			From: mail.Address{Email: "hello@patsbakery.com"}, To: mail.Address{Email: addr},
			Subject: "Bread week", Text: "Fresh loaves", Kind: mail.KindCampaign, AccountID: h.acc, CampaignID: h.campaign,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[addr] = id
		if _, err := h.db.Exec(ctx, `UPDATE campaign_recipients SET message_id = $1, sent_at = now() WHERE id = $2`, id, r); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.Sent()) != 3 {
		t.Fatalf("sent %d", len(fake.Sent()))
	}

	// SES's events for each copy.
	for addr, name := range map[string]string{sam: "delivery", ann: "delivery", bo: "bounce-permanent"} {
		if out := h.handle(t, h.payload(t, name, "{{MESSAGE_ID}}", ids[addr], "{{ADDRESS}}", addr)); out != Stored {
			t.Fatalf("%s for %s: %s", name, addr, out)
		}
	}
	// Sam opens and clicks the first link; Ann unsubscribes.
	tr := &tracking.Service{DB: h.db}
	for _, hit := range []tracking.Hit{
		{AccountID: h.acc, CampaignID: h.campaign, RecipientID: rcpts[sam]},
		{AccountID: h.acc, CampaignID: h.campaign, RecipientID: rcpts[sam], Link: 1, URL: "https://patsbakery.com/menu"},
	} {
		if err := tr.Record(ctx, &hit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.db.Exec(ctx, `INSERT INTO unsubscribes (account_id, contact_id, campaign_id, source)
		SELECT account_id, contact_id, campaign_id, 'link' FROM campaign_recipients WHERE id = $1`, rcpts[ann]); err != nil {
		t.Fatal(err)
	}

	rs := &reports.Service{DB: h.db}
	for _, c := range []struct {
		q    reports.RecipientQuery
		want string
	}{
		{reports.RecipientQuery{Activity: reports.Opened}, sam},
		{reports.RecipientQuery{Activity: reports.NotOpened}, ann},
		{reports.RecipientQuery{Activity: reports.Clicked}, sam},
		{reports.RecipientQuery{Activity: reports.Clicked, Link: 1}, sam},
		{reports.RecipientQuery{Activity: reports.Clicked, Link: 2}, ""},
		{reports.RecipientQuery{Activity: reports.NotClicked}, ann},
		{reports.RecipientQuery{Activity: reports.Bounced}, bo},
		{reports.RecipientQuery{Activity: reports.Complained}, ""},
		{reports.RecipientQuery{Activity: reports.Unsubscribed}, ann},
	} {
		got, err := rs.Recipients(ctx, h.acc, h.campaign, c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.q.Activity, err)
		}
		var addrs []string
		for _, r := range got {
			addrs = append(addrs, r.Email)
		}
		if strings.Join(addrs, ",") != c.want {
			t.Errorf("%s (link %d) = %v, want %q", c.q.Activity, c.q.Link, addrs, c.want)
		}
	}
	// The report counts the same people.
	rep, err := rs.Campaign(ctx, h.acc, h.campaign)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Delivered != 2 || rep.Bounced != 1 || rep.Unsubscribes != 1 || rep.Opens.Unique != 1 || rep.Clicks.Unique != 1 {
		t.Errorf("report %+v", rep)
	}
}
