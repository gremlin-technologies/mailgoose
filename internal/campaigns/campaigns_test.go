package campaigns

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

type harness struct {
	db        *pgxpool.Pool
	svc       *Service
	acc, acc2 string
	email     string
	now       time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	h := &harness{db: db, now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	h.svc = &Service{DB: db, Now: func() time.Time { return h.now }}
	for _, p := range []*string{&h.acc, &h.acc2} {
		if err := db.QueryRow(context.Background(), `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	h.email = h.addEmail(t, h.acc)
	return h
}

func (h *harness) addEmail(t *testing.T, account string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(context.Background(), `INSERT INTO emails (account_id, name, kind, document) VALUES ($1, 'Spring', 'html', '{}') RETURNING id`, account).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (h *harness) tag(t *testing.T, account, name string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(context.Background(), `INSERT INTO tags (account_id, name) VALUES ($1, $2) RETURNING id`, account, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func ptr[T any](v T) *T { return &v }

func field(err error) string {
	var ie *InvalidError
	if errors.As(err, &ie) {
		return ie.Field
	}
	return ""
}

func TestCreateAndGet(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	news, vip := h.tag(t, h.acc, "news"), h.tag(t, h.acc, "VIP")
	c, err := h.svc.Create(ctx, h.acc, Changes{Name: ptr(" Spring sale "), EmailID: &h.email,
		Recipients: &Recipients{TagIDs: []string{vip, news, vip}}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Spring sale" || *c.EmailID != h.email || c.AllContacts || c.SendAt != nil || c.Status != StatusDraft ||
		c.Version != 1 || len(c.Tags) != 2 || c.Tags[0].Name != "news" || c.Tags[1].Name != "VIP" {
		t.Errorf("created %+v", c)
	}
	if aud := c.Audience(); aud.All || len(aud.TagIDs) != 2 {
		t.Errorf("audience %+v", aud)
	}

	at := h.now.Add(48 * time.Hour)
	all, err := h.svc.Create(ctx, h.acc, Changes{Name: ptr("All"), EmailID: &h.email, Recipients: &Recipients{All: true}, SendAt: &at})
	if err != nil || !all.AllContacts || len(all.Tags) != 0 || !all.SendAt.Equal(at) || !all.Audience().All {
		t.Errorf("all: %+v %v", all, err)
	}
	if _, err := h.svc.Get(ctx, h.acc2, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other account: %v", err)
	}
}

func TestValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	all := &Recipients{All: true}
	past, far := h.now.Add(-time.Minute), h.now.Add(MaxScheduleAhead+time.Hour)
	for want, ch := range map[string]Changes{
		"name":               {Name: ptr(" "), EmailID: &h.email, Recipients: all},
		"name (long)":        {Name: ptr(strings.Repeat("x", MaxNameChars+1)), EmailID: &h.email, Recipients: all},
		"email_id":           {Name: ptr("x"), Recipients: all},
		"recipients":         {Name: ptr("x"), EmailID: &h.email},
		"recipients (empty)": {Name: ptr("x"), EmailID: &h.email, Recipients: &Recipients{}},
		"recipients (both)":  {Name: ptr("x"), EmailID: &h.email, Recipients: &Recipients{All: true, TagIDs: []string{"x"}}},
		"send_at":            {Name: ptr("x"), EmailID: &h.email, Recipients: all, SendAt: &past},
		"send_at (far)":      {Name: ptr("x"), EmailID: &h.email, Recipients: all, SendAt: &far},
	} {
		if _, err := h.svc.Create(ctx, h.acc, ch); field(err) != strings.Split(want, " ")[0] {
			t.Errorf("%s: %v", want, err)
		}
	}
	theirs := h.addEmail(t, h.acc2)
	for _, id := range []string{theirs, "not-a-uuid"} {
		if _, err := h.svc.Create(ctx, h.acc, Changes{Name: ptr("x"), EmailID: &id, Recipients: all}); !errors.Is(err, ErrUnknownEmail) {
			t.Errorf("email %s: %v", id, err)
		}
	}
	other := h.tag(t, h.acc2, "theirs")
	for _, ids := range [][]string{{other}, {"not-a-uuid"}} {
		if _, err := h.svc.Create(ctx, h.acc, Changes{Name: ptr("x"), EmailID: &h.email, Recipients: &Recipients{TagIDs: ids}}); !errors.Is(err, ErrUnknownTag) {
			t.Errorf("tags %v: %v", ids, err)
		}
	}
	var many []string
	for i := 0; i <= MaxTags; i++ {
		many = append(many, h.tag(t, h.acc, "t"+strings.Repeat("x", i)))
	}
	if _, err := h.svc.Create(ctx, h.acc, Changes{Name: ptr("x"), EmailID: &h.email, Recipients: &Recipients{TagIDs: many}}); field(err) != "recipients" {
		t.Errorf("too many tags: %v", err)
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM campaigns`).Scan(&n)
	if n != 0 {
		t.Errorf("%d campaigns saved by failed creates", n)
	}
}

func TestUpdate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	vip := h.tag(t, h.acc, "VIP")
	c, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("A"), EmailID: &h.email, Recipients: &Recipients{All: true}})

	other := h.addEmail(t, h.acc)
	at := h.now.Add(time.Hour)
	u, err := h.svc.Update(ctx, h.acc, c.ID, Changes{EmailID: &other, Recipients: &Recipients{TagIDs: []string{vip}}, SendAt: &at, Version: ptr(1)})
	if err != nil || u.Name != "A" || *u.EmailID != other || u.AllContacts || len(u.Tags) != 1 || !u.SendAt.Equal(at) || u.Version != 2 {
		t.Fatalf("update: %+v %v", u, err)
	}
	if u, err = h.svc.Update(ctx, h.acc, c.ID, Changes{SendNow: true, Recipients: &Recipients{All: true}}); err != nil || u.SendAt != nil || len(u.Tags) != 0 || !u.AllContacts {
		t.Errorf("send now: %+v %v", u, err)
	}
	var verErr *VersionError
	if _, err := h.svc.Update(ctx, h.acc, c.ID, Changes{Name: ptr("B"), Version: ptr(1)}); !errors.As(err, &verErr) || verErr.Current != 3 {
		t.Errorf("stale version: %v", err)
	}
	if _, err := h.svc.Update(ctx, h.acc, c.ID, Changes{EmailID: ptr("")}); field(err) != "email_id" {
		t.Errorf("no email: %v", err)
	}
	if _, err := h.svc.Update(ctx, h.acc2, c.ID, Changes{Name: ptr("B")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("other account: %v", err)
	}

	// Deleting the email leaves a draft with none.
	if _, err := h.db.Exec(ctx, `DELETE FROM emails WHERE id = $1`, other); err != nil {
		t.Fatal(err)
	}
	if g, _ := h.svc.Get(ctx, h.acc, c.ID); g.EmailID != nil {
		t.Errorf("email after delete: %v", *g.EmailID)
	}
}

// TestTransitions checks every pair of statuses against the table, through
// SetStatus, and that only drafts and scheduled campaigns can be edited and
// only drafts deleted.
func TestTransitions(t *testing.T) {
	allowed := map[[2]string]bool{
		{StatusDraft, StatusScheduled}: true, {StatusDraft, StatusSending}: true,
		{StatusScheduled, StatusSending}: true, {StatusScheduled, StatusDraft}: true, {StatusScheduled, StatusCancelled}: true,
		{StatusSending, StatusSent}: true, {StatusSending, StatusPaused}: true, {StatusSending, StatusFailed}: true, {StatusSending, StatusCancelled}: true,
		{StatusPaused, StatusSending}: true, {StatusPaused, StatusCancelled}: true, {StatusPaused, StatusFailed}: true,
	}
	for _, from := range Statuses {
		for _, to := range Statuses {
			if CanMove(from, to) != allowed[[2]string{from, to}] {
				t.Errorf("CanMove(%s, %s) = %v", from, to, !allowed[[2]string{from, to}])
			}
		}
	}

	h := newHarness(t)
	ctx := context.Background()
	at := h.now.Add(time.Hour)
	for _, from := range Statuses {
		for _, to := range Statuses {
			c, err := h.svc.Create(ctx, h.acc, Changes{Name: ptr(from + " to " + to), EmailID: &h.email, Recipients: &Recipients{All: true}, SendAt: &at})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.Exec(ctx, `UPDATE campaigns SET status = $1 WHERE id = $2`, from, c.ID); err != nil {
				t.Fatal(err)
			}
			got, err := h.svc.SetStatus(ctx, h.acc, c.ID, to)
			var se *StatusError
			switch {
			case allowed[[2]string{from, to}]:
				if err != nil || got.Status != to || got.StatusChangedAt.Before(c.StatusChangedAt) {
					t.Errorf("%s to %s: %v", from, to, err)
				}
			case !errors.As(err, &se) || se.Status != from || se.To != to:
				t.Errorf("%s to %s should be refused: %v", from, to, err)
			}
		}

		c, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("edit " + from), EmailID: &h.email, Recipients: &Recipients{All: true}, SendAt: &at})
		_, _ = h.db.Exec(ctx, `UPDATE campaigns SET status = $1 WHERE id = $2`, from, c.ID)
		_, err := h.svc.Update(ctx, h.acc, c.ID, Changes{Name: ptr("edited")})
		var se *StatusError
		if Editable(from) != (err == nil) || (err != nil && (!errors.As(err, &se) || se.Status != from)) {
			t.Errorf("edit %s: %v", from, err)
		}
		err = h.svc.Delete(ctx, h.acc, c.ID)
		if (from == StatusDraft) != (err == nil) || (err != nil && !errors.As(err, &se)) {
			t.Errorf("delete %s: %v", from, err)
		}
	}
	if !Editable(StatusDraft) || !Editable(StatusScheduled) || Editable(StatusSending) {
		t.Error("Editable")
	}
	if _, err := h.svc.SetStatus(ctx, h.acc, noSuchID, StatusSending); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

const noSuchID = "00000000-0000-4000-8000-000000000000"

func TestScheduledKeepsASendTime(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	at := h.now.Add(time.Hour)
	c, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("A"), EmailID: &h.email, Recipients: &Recipients{All: true}, SendAt: &at})
	if _, err := h.svc.SetStatus(ctx, h.acc, c.ID, StatusScheduled); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Update(ctx, h.acc, c.ID, Changes{SendNow: true}); field(err) != "send_at" {
		t.Errorf("send now: %v", err)
	}
	later := at.Add(time.Hour)
	if u, err := h.svc.Update(ctx, h.acc, c.ID, Changes{SendAt: &later}); err != nil || !u.SendAt.Equal(later) || u.Status != StatusScheduled {
		t.Errorf("reschedule: %+v %v", u, err)
	}
}

func TestListAndDelete(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("A"), EmailID: &h.email, Recipients: &Recipients{All: true}})
	b, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("B"), EmailID: &h.email, Recipients: &Recipients{All: true}})
	if _, err := h.svc.SetStatus(ctx, h.acc, a.ID, StatusSending); err != nil {
		t.Fatal(err)
	}
	theirs := h.addEmail(t, h.acc2)
	if _, err := h.svc.Create(ctx, h.acc2, Changes{Name: ptr("Theirs"), EmailID: &theirs, Recipients: &Recipients{All: true}}); err != nil {
		t.Fatal(err)
	}
	page, err := h.svc.List(ctx, h.acc, "", nil, 1)
	if err != nil || len(page) != 1 || page[0].ID != b.ID {
		t.Fatalf("first page: %v %v", page, err)
	}
	page, err = h.svc.List(ctx, h.acc, "", &Cursor{CreatedAt: page[0].CreatedAt, ID: page[0].ID}, 10)
	if err != nil || len(page) != 1 || page[0].ID != a.ID || page[0].Tags == nil {
		t.Fatalf("second page: %v %v", page, err)
	}
	if page, _ = h.svc.List(ctx, h.acc, StatusSending, nil, 10); len(page) != 1 || page[0].ID != a.ID {
		t.Errorf("sending: %v", page)
	}
	if err := h.svc.Delete(ctx, h.acc, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Delete(ctx, h.acc, b.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
}

// TestRecipientsAndTagDelete checks who a campaign's audience picks, and
// that deleting a tag takes it off draft and scheduled campaigns and counts
// them.
func TestRecipientsAndTagDelete(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	people := &contacts.Service{DB: h.db}
	vip, news := h.tag(t, h.acc, "VIP"), h.tag(t, h.acc, "news")
	add := func(email, status string, tags ...string) {
		var id string
		if err := h.db.QueryRow(ctx, `INSERT INTO contacts (account_id, email, status, source) VALUES ($1, $2, $3, 'manual') RETURNING id`, h.acc, email, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for _, tag := range tags {
			if _, err := h.db.Exec(ctx, `INSERT INTO contact_tags (contact_id, tag_id) VALUES ($1, $2)`, id, tag); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("a@example.com", "subscribed", vip)
	add("b@example.com", "subscribed", vip, news)
	add("c@example.com", "subscribed")
	add("d@example.com", "unsubscribed", vip)
	add("e@example.com", "unconfirmed", news)

	tagged, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("Tagged"), EmailID: &h.email, Recipients: &Recipients{TagIDs: []string{vip, news}}})
	everyone, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("All"), EmailID: &h.email, Recipients: &Recipients{All: true}})
	for c, want := range map[*Campaign]int{tagged: 2, everyone: 3} {
		if n, err := people.CountRecipients(ctx, h.acc, c.Audience()); err != nil || n != want {
			t.Errorf("%s: %d %v", c.Name, n, err)
		}
		rs, err := people.Recipients(ctx, h.acc, c.Audience(), "", 10)
		if err != nil || len(rs) != want {
			t.Errorf("%s recipients: %d %v", c.Name, len(rs), err)
		}
	}
	if n, _ := people.CountRecipients(ctx, h.acc, contacts.Audience{}); n != 0 {
		t.Errorf("no tags picked %d", n)
	}

	sent, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("Sent"), EmailID: &h.email, Recipients: &Recipients{TagIDs: []string{vip}}})
	_, _ = h.db.Exec(ctx, `UPDATE campaigns SET status = 'sent' WHERE id = $1`, sent.ID)
	d, err := people.DeleteTag(ctx, h.acc, vip)
	if err != nil || d.CampaignsUpdated != 1 || d.ContactsUntagged != 3 {
		t.Fatalf("delete tag: %+v %v", d, err)
	}
	if g, _ := h.svc.Get(ctx, h.acc, tagged.ID); len(g.Tags) != 1 || g.Tags[0].ID != news || g.Version != 2 {
		t.Errorf("after tag delete: %+v", g)
	}
}
