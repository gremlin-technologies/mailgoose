package segments

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

type harness struct {
	svc       *Service
	acc, acc2 string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { now = now.Add(time.Second); return now }
	h := &harness{svc: &Service{DB: db, Contacts: &contacts.Service{DB: db, Now: clock}, Now: clock}}
	for _, p := range []*string{&h.acc, &h.acc2} {
		if err := db.QueryRow(context.Background(), `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *harness) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := h.svc.DB.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) contact(t *testing.T, name, status string, tags ...string) *contacts.Contact {
	t.Helper()
	c, err := h.svc.Contacts.Create(context.Background(), h.acc, contacts.CreateInput{Email: name + "@example.com", Status: status})
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if _, err := h.svc.Contacts.TagContacts(context.Background(), h.acc, tag, contacts.Selection{IDs: []string{c.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func (h *harness) tag(t *testing.T, account, name string) string {
	t.Helper()
	tg, err := h.svc.Contacts.CreateTag(context.Background(), account, name)
	if err != nil {
		t.Fatal(err)
	}
	return tg.ID
}

func (h *harness) campaign(t *testing.T, account string, tracking bool) string {
	t.Helper()
	var id string
	if err := h.svc.DB.QueryRow(context.Background(), `INSERT INTO campaigns (account_id, name, all_contacts, status, launched_at, tracking)
		VALUES ($1, 'C', true, 'sent', now() - interval '3 days', $2) RETURNING id`, account, tracking).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// delivered gives c a delivered copy of campaign, and each of did.
func (h *harness) delivered(t *testing.T, campaign string, c *contacts.Contact, did ...string) {
	t.Helper()
	var r int64
	if err := h.svc.DB.QueryRow(context.Background(), `INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status)
		VALUES ($1, $2, $3, 'sent') RETURNING id`, h.acc, campaign, c.ID).Scan(&r); err != nil {
		t.Fatal(err)
	}
	h.exec(t, `INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, occurred_at)
		VALUES (gen_random_uuid()::text, $1, $2, $3, 'm', 'campaign', 'delivery', now())`, h.acc, campaign, r)
	for _, typ := range did {
		var link *int
		if typ == "click" {
			one := 1
			link = &one
		}
		h.exec(t, `INSERT INTO engagement_events (account_id, campaign_id, recipient_id, type, link, occurred_at)
			VALUES ($1, $2, $3, $4, $5, now() - interval '1 hour')`, h.acc, campaign, r, typ, link)
	}
}

// members is the segment's contacts by name, checking the count matches.
func (h *harness) members(t *testing.T, id string) string {
	t.Helper()
	m, err := h.svc.Members(context.Background(), h.acc, id, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range m.Contacts {
		out = append(out, strings.TrimSuffix(c.Email, "@example.com"))
	}
	slices.Sort(out)
	if m.Count != len(out) {
		t.Errorf("count %d, listed %d", m.Count, len(out))
	}
	return strings.Join(out, ",")
}

func code(err error) string {
	var ve *auth.ValidationError
	var ce *ConditionError
	switch {
	case errors.As(err, &ve):
		return ve.Code
	case errors.As(err, &ce):
		return ce.Code + " " + ce.Condition
	case errors.As(err, new(*LimitError)):
		return "limit"
	case errors.Is(err, ErrExists):
		return "exists"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case err == nil:
		return ""
	}
	return err.Error()
}

func TestCreateUpdateAndMembers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	vip, eu := h.tag(t, h.acc, "vip"), h.tag(t, h.acc, "eu")
	h.contact(t, "ann", contacts.StatusSubscribed, vip, eu)
	h.contact(t, "bo", contacts.StatusSubscribed, vip)
	h.contact(t, "cy", contacts.StatusUnconfirmed, vip, eu)
	h.contact(t, "di", contacts.StatusUnsubscribed, vip, eu)
	h.contact(t, "ed", contacts.StatusSubscribed)

	// Tags must all match; statuses are any of them. Repeats are dropped
	// and ids lowercased.
	sg, err := h.svc.Create(ctx, h.acc, "  VIPs in the EU ", Conditions{
		TagIDs:   []string{vip, strings.ToUpper(eu), eu},
		Statuses: []string{contacts.StatusSubscribed, contacts.StatusUnconfirmed, contacts.StatusSubscribed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sg.Name != "VIPs in the EU" || !slices.Equal(sg.Conditions.TagIDs, []string{vip, eu}) ||
		!slices.Equal(sg.Conditions.Statuses, []string{"subscribed", "unconfirmed"}) || !sg.Valid() {
		t.Fatalf("created %+v", sg)
	}
	if got := h.members(t, sg.ID); got != "ann,cy" {
		t.Errorf("members %q", got)
	}

	// The segment stays current: a contact tagged later is in it.
	h.contact(t, "fa", contacts.StatusSubscribed, vip, eu)
	if got := h.members(t, sg.ID); got != "ann,cy,fa" {
		t.Errorf("members after a new contact %q", got)
	}

	// New conditions replace the old ones whole; a rename keeps them.
	sg, err = h.svc.Update(ctx, h.acc, sg.ID, nil, &Conditions{TagIDs: []string{vip}, Statuses: []string{contacts.StatusSubscribed}})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.members(t, sg.ID); got != "ann,bo,fa" {
		t.Errorf("members after update %q", got)
	}
	renamed := "Subscribed VIPs"
	sg, err = h.svc.Update(ctx, h.acc, sg.ID, &renamed, nil)
	if err != nil || sg.Name != renamed || !slices.Equal(sg.Conditions.TagIDs, []string{vip}) {
		t.Fatalf("rename: %+v, %v", sg, err)
	}

	// Names are unique ignoring case, in the account.
	if _, err := h.svc.Create(ctx, h.acc, "subscribed vips", Conditions{NeverOpened: true}); code(err) != "exists" {
		t.Errorf("same name: %v", err)
	}
	other, err := h.svc.Create(ctx, h.acc, "Cold", Conditions{NeverOpened: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Update(ctx, h.acc, other.ID, &renamed, nil); code(err) != "exists" {
		t.Errorf("rename to a taken name: %v", err)
	}
	if _, err := h.svc.Get(ctx, h.acc2, sg.ID); code(err) != "not_found" {
		t.Errorf("another account's segment: %v", err)
	}

	// Listed by name; the name filter ignores case.
	all, err := h.svc.List(ctx, h.acc, "", nil, 10)
	if err != nil || len(all) != 2 || all[0].Name != "Cold" || all[1].Name != renamed {
		t.Fatalf("list %v, %v", all, err)
	}
	page, err := h.svc.List(ctx, h.acc, "", &Cursor{Name: all[0].Name, ID: all[0].ID}, 10)
	if err != nil || len(page) != 1 || page[0].ID != sg.ID {
		t.Errorf("second page %v, %v", page, err)
	}
	if named, err := h.svc.List(ctx, h.acc, "COLD", nil, 10); err != nil || len(named) != 1 || named[0].ID != other.ID {
		t.Errorf("by name %v, %v", named, err)
	}

	if err := h.svc.Delete(ctx, h.acc, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Delete(ctx, h.acc, other.ID); code(err) != "not_found" {
		t.Errorf("delete again: %v", err)
	}
}

func TestEngagementConditions(t *testing.T) {
	h := newHarness(t)
	a, b := h.campaign(t, h.acc, true), h.campaign(t, h.acc, true)
	ann := h.contact(t, "ann", contacts.StatusSubscribed)
	bo := h.contact(t, "bo", contacts.StatusSubscribed)
	cy := h.contact(t, "cy", contacts.StatusUnsubscribed)
	h.contact(t, "di", contacts.StatusSubscribed)
	h.delivered(t, a, ann, "open", "click")
	h.delivered(t, a, bo, "open")
	h.delivered(t, a, cy)
	h.delivered(t, b, ann)
	h.delivered(t, b, bo, "open")
	for _, c := range []struct {
		name  string
		conds Conditions
		want  string
	}{
		{"opened A, didn't click", Conditions{Opened: []string{a}, NotClicked: []string{a}}, "bo"},
		{"opened A and B", Conditions{Opened: []string{a, b}}, "bo"},
		{"didn't open B", Conditions{NotOpened: []string{b}}, "ann"},
		{"never opened", Conditions{NeverOpened: true}, "cy"},
		{"never opened, subscribed", Conditions{NeverOpened: true, Statuses: []string{contacts.StatusSubscribed}}, ""},
		{"clicked in the last week", Conditions{ClickedWithinDays: 7}, "ann"},
		{"opened in the last day, didn't click", Conditions{OpenedWithinDays: 1, NeverClicked: true}, "bo"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sg, err := h.svc.Create(context.Background(), h.acc, c.name, c.conds)
			if err != nil {
				t.Fatal(err)
			}
			if got := h.members(t, sg.ID); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestCreateRefusesConditionsItCantUse(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	theirTag := h.tag(t, h.acc2, "theirs")
	theirCampaign := h.campaign(t, h.acc2, true)
	untracked := h.campaign(t, h.acc, false)
	archived := h.campaign(t, h.acc, true)
	h.exec(t, `INSERT INTO campaign_reports (campaign_id, account_id, report) VALUES ($1, $2, '{}')`, archived, h.acc)
	const missing = "0b6f5f1e-6c1e-4d39-9a43-3a3a3a3a3a3a"
	many := make([]string, MaxTags+1)
	for i := range many {
		many[i] = fmt.Sprintf("0b6f5f1e-6c1e-4d39-9a43-%012d", i)
	}
	for _, c := range []struct {
		name  string
		conds Conditions
		want  string
	}{
		{"nothing", Conditions{}, "no_conditions"},
		{"bad tag id", Conditions{TagIDs: []string{"vip"}}, "invalid_conditions"},
		{"too many tags", Conditions{TagIDs: many}, "invalid_conditions"},
		{"bad status", Conditions{Statuses: []string{"active"}}, "invalid_conditions"},
		{"bad campaign id", Conditions{Clicked: []string{"x"}}, "invalid_conditions"},
		{"days out of range", Conditions{OpenedWithinDays: 366}, "invalid_conditions"},
		{"too many campaigns", Conditions{Opened: many[:11], NotClicked: many[11:]}, "invalid_conditions"},
		{"missing tag", Conditions{TagIDs: []string{missing}}, "unknown_tag tag_ids"},
		{"another account's tag", Conditions{TagIDs: []string{theirTag}}, "unknown_tag tag_ids"},
		{"missing campaign", Conditions{NotOpened: []string{missing}}, "unknown_campaign not_opened"},
		{"another account's campaign", Conditions{Opened: []string{theirCampaign}}, "unknown_campaign opened"},
		{"untracked campaign", Conditions{Clicked: []string{untracked}}, "tracking_off clicked"},
		{"archived campaign", Conditions{NotClicked: []string{archived}}, "report_archived not_clicked"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := h.svc.Create(ctx, h.acc, c.name, c.conds); code(err) != c.want {
				t.Errorf("got %v, want %s", err, c.want)
			}
		})
	}
	for _, name := range []string{"", "  ", strings.Repeat("x", MaxNameLen+1), "two\nlines"} {
		if _, err := h.svc.Create(ctx, h.acc, name, Conditions{NeverOpened: true}); code(err) != "invalid_segment_name" {
			t.Errorf("name %q: %v", name, err)
		}
	}
	// An update checks its conditions the same way, and a bad one changes
	// nothing.
	sg, err := h.svc.Create(ctx, h.acc, "ok", Conditions{NeverOpened: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Update(ctx, h.acc, sg.ID, nil, &Conditions{Clicked: []string{untracked}}); code(err) != "tracking_off clicked" {
		t.Errorf("update: %v", err)
	}
	if got, err := h.svc.Get(ctx, h.acc, sg.ID); err != nil || !got.Conditions.NeverOpened || len(got.Conditions.Clicked) != 0 {
		t.Errorf("after a refused update %+v, %v", got, err)
	}
}

func TestDeletedTagMakesTheSegmentInvalid(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	vip, eu := h.tag(t, h.acc, "vip"), h.tag(t, h.acc, "eu")
	h.contact(t, "ann", contacts.StatusSubscribed, vip, eu)
	h.contact(t, "bo", contacts.StatusSubscribed, vip)
	sg, err := h.svc.Create(ctx, h.acc, "VIPs in the EU", Conditions{TagIDs: []string{vip, eu}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Contacts.DeleteTag(ctx, h.acc, eu); err != nil {
		t.Fatal(err)
	}
	// Without the tag the segment would be every VIP; it's invalid instead,
	// naming the condition.
	got, err := h.svc.Get(ctx, h.acc, sg.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []Problem{{Condition: "tag_ids", ID: eu, Code: ProblemTagDeleted}}
	if got.Valid() || !slices.Equal(got.Problems, want) {
		t.Errorf("problems %+v", got.Problems)
	}
	if list, err := h.svc.List(ctx, h.acc, "", nil, 10); err != nil || len(list) != 1 || !slices.Equal(list[0].Problems, want) {
		t.Errorf("listed %+v, %v", list, err)
	}
	var ie *InvalidError
	if _, err := h.svc.Members(ctx, h.acc, sg.ID, nil, 10); !errors.As(err, &ie) || !slices.Equal(ie.Segment.Problems, want) {
		t.Errorf("members: %v", err)
	}
	if _, _, err := h.svc.Resolve(ctx, h.acc, sg.ID); !errors.As(err, &ie) {
		t.Errorf("resolve: %v", err)
	}
	// Updating the conditions makes it valid again.
	if sg, err = h.svc.Update(ctx, h.acc, sg.ID, nil, &Conditions{TagIDs: []string{vip}}); err != nil || !sg.Valid() {
		t.Fatalf("update %+v, %v", sg, err)
	}
	if got := h.members(t, sg.ID); got != "ann,bo" {
		t.Errorf("members %q", got)
	}
}

func TestCampaignProblems(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	gone, untracked, archived := h.campaign(t, h.acc, true), h.campaign(t, h.acc, true), h.campaign(t, h.acc, true)
	sg, err := h.svc.Create(ctx, h.acc, "s", Conditions{Opened: []string{gone}, NotOpened: []string{untracked}, Clicked: []string{archived}})
	if err != nil {
		t.Fatal(err)
	}
	h.exec(t, `DELETE FROM campaigns WHERE id = $1`, gone)
	h.exec(t, `UPDATE campaigns SET tracking = false WHERE id = $1`, untracked)
	h.exec(t, `INSERT INTO campaign_reports (campaign_id, account_id, report) VALUES ($1, $2, '{}')`, archived, h.acc)
	got, err := h.svc.Get(ctx, h.acc, sg.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []Problem{
		{Condition: "opened", ID: gone, Code: ProblemCampaignDeleted},
		{Condition: "not_opened", ID: untracked, Code: ProblemTrackingOff},
		{Condition: "clicked", ID: archived, Code: ProblemReportArchived},
	}
	if !slices.Equal(got.Problems, want) {
		t.Errorf("problems %+v", got.Problems)
	}
}

func TestSegmentLimit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.exec(t, `INSERT INTO segments (account_id, name, conditions, created_at, updated_at)
		SELECT $1, 'segment ' || n, '{"never_opened": true}', now(), now() FROM generate_series(1, $2) n`, h.acc, MaxSegments)
	if _, err := h.svc.Create(ctx, h.acc, "one more", Conditions{NeverOpened: true}); code(err) != "limit" {
		t.Errorf("past the limit: %v", err)
	}
	// Another account has its own.
	if _, err := h.svc.Create(ctx, h.acc2, "one more", Conditions{NeverOpened: true}); err != nil {
		t.Errorf("another account: %v", err)
	}
}
