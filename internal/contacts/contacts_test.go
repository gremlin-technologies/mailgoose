package contacts

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

type harness struct {
	svc       *Service
	now       time.Time
	acc, acc2 string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	h := &harness{now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	// Each call moves the clock a second, so contacts sort predictably.
	h.svc = &Service{DB: db, Now: func() time.Time { h.now = h.now.Add(time.Second); return h.now }}
	for _, p := range []*string{&h.acc, &h.acc2} {
		if err := db.QueryRow(context.Background(), `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func str(s string) *string { return &s }

func set(s string) auth.Field { return auth.Field{Set: true, Value: &s} }
func null() auth.Field        { return auth.Field{Set: true} }
func code(err error) string {
	var ve *auth.ValidationError
	if errors.As(err, &ve) {
		return ve.Code
	}
	return ""
}

func (h *harness) add(t *testing.T, email, status string) *Contact {
	t.Helper()
	c, err := h.svc.Create(context.Background(), h.acc, CreateInput{Email: email, Status: status})
	if err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	return c
}

func TestCreateStoresLowercaseUniqueEmail(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, err := h.svc.Create(ctx, h.acc, CreateInput{Email: " Pat@Example.COM ", FirstName: str(" Pat "), LastName: str("")})
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "pat@example.com" || *c.FirstName != "Pat" || c.LastName != nil || c.Status != StatusSubscribed || c.Source != SourceAPI {
		t.Errorf("contact = %+v", c)
	}
	if _, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "PAT@example.com"}); !errors.Is(err, ErrExists) {
		t.Errorf("duplicate: %v", err)
	}
	// Another account can have the same address.
	if _, err := h.svc.Create(ctx, h.acc2, CreateInput{Email: "pat@example.com"}); err != nil {
		t.Errorf("other account: %v", err)
	}
	// Another account can't see it.
	if _, err := h.svc.Get(ctx, h.acc2, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get from other account: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	h := newHarness(t)
	long := ""
	for range MaxNameLen + 1 {
		long += "a"
	}
	for _, tc := range []struct {
		in   CreateInput
		want string
	}{
		{CreateInput{Email: "not an address"}, "invalid_email"},
		{CreateInput{Email: "Pat <pat@example.com>"}, "invalid_email"},
		{CreateInput{Email: "pat@example.com", FirstName: &long}, "invalid_name"},
		{CreateInput{Email: "pat@example.com", LastName: str("a\nb")}, "invalid_name"},
		{CreateInput{Email: "pat@example.com", Status: StatusBounced}, "invalid_status"},
		{CreateInput{Email: "pat@example.com", Status: "deleted"}, "invalid_status"},
		{CreateInput{Email: "pat@example.com", Source: "magic"}, "invalid_source"},
	} {
		if _, err := h.svc.Create(context.Background(), h.acc, tc.in); code(err) != tc.want {
			t.Errorf("%+v: %v, want %s", tc.in, err, tc.want)
		}
	}
}

func TestUpdate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "pat@example.com", FirstName: str("Pat"), LastName: str("Lee"), Status: StatusUnconfirmed})
	if err != nil {
		t.Fatal(err)
	}
	created := c.StatusChangedAt

	u, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{FirstName: set("Patricia"), LastName: null()})
	if err != nil || *u.FirstName != "Patricia" || u.LastName != nil || u.Status != StatusUnconfirmed || !u.StatusChangedAt.Equal(created) {
		t.Fatalf("names: %+v %v", u, err)
	}
	u, err = h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Status: set(StatusSubscribed), Email: set("PATRICIA@example.com")})
	if err != nil || u.Status != StatusSubscribed || u.Email != "patricia@example.com" || !u.StatusChangedAt.After(created) {
		t.Fatalf("status and email: %+v %v", u, err)
	}

	h.add(t, "sam@example.com", "")
	if _, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Email: set("sam@example.com")}); !errors.Is(err, ErrExists) {
		t.Errorf("email taken: %v", err)
	}
	if _, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Email: null()}); code(err) != "invalid_email" {
		t.Errorf("null email: %v", err)
	}
	if _, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Status: set(StatusComplained)}); code(err) != "invalid_status" {
		t.Errorf("set complained: %v", err)
	}
	if _, err := h.svc.Update(ctx, h.acc2, c.ID, UpdateInput{FirstName: set("X")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("other account: %v", err)
	}
}

// TestMovingASuppressedContactKeepsTheOldAddressSuppressed: an
// unsubscribed, bounced or complained contact that changes its address
// leaves the old address on the suppression list, so it can't be added
// back as subscribed.
func TestMovingASuppressedContactKeepsTheOldAddressSuppressed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for i, status := range []string{StatusUnsubscribed, StatusBounced, StatusComplained} {
		old := fmt.Sprintf("gone%d@example.com", i)
		c := h.add(t, old, "")
		if _, err := h.svc.DB.Exec(ctx, `UPDATE contacts SET status = $2 WHERE id = $1`, c.ID, status); err != nil {
			t.Fatal(err)
		}
		u, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Email: set(fmt.Sprintf("other%d@example.com", i))})
		if err != nil || u.Status != status {
			t.Fatalf("%s: move: %+v %v", status, u, err)
		}
		again, err := h.svc.Create(ctx, h.acc, CreateInput{Email: old})
		if err != nil || again.Status != status {
			t.Errorf("%s: the old address came back as %+v %v", status, again, err)
		}
	}

	// A subscribed contact's old address is free to use again.
	c := h.add(t, "free@example.com", "")
	if _, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Email: set("moved@example.com")}); err != nil {
		t.Fatal(err)
	}
	if again, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "free@example.com"}); err != nil || again.Status != StatusSubscribed {
		t.Errorf("subscribed: %+v %v", again, err)
	}
}

func TestSuppressedStatusesCantBeUndone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	c := h.add(t, "pat@example.com", StatusUnsubscribed)
	for _, s := range []string{StatusSubscribed, StatusUnconfirmed} {
		if _, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Status: set(s)}); !errors.Is(err, ErrSuppressed) {
			t.Errorf("unsubscribed to %s: %v", s, err)
		}
	}
	// Setting the same status, or only a name, is fine.
	if _, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Status: set(StatusUnsubscribed), FirstName: set("Pat")}); err != nil {
		t.Errorf("no-op status: %v", err)
	}

	b := h.add(t, "sam@example.com", "")
	if _, err := h.svc.DB.Exec(ctx, `UPDATE contacts SET status = 'bounced' WHERE id = $1`, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Update(ctx, h.acc, b.ID, UpdateInput{Status: set(StatusUnsubscribed)}); !errors.Is(err, ErrSuppressed) {
		t.Errorf("bounced to unsubscribed: %v", err)
	}
	// Anyone may unsubscribe a subscribed contact.
	s := h.add(t, "lee@example.com", "")
	if u, err := h.svc.Update(ctx, h.acc, s.ID, UpdateInput{Status: set(StatusUnsubscribed)}); err != nil || u.Status != StatusUnsubscribed {
		t.Errorf("unsubscribe: %v", err)
	}
}

func TestDeleteLeavesSuppressionForSuppressedContacts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, status := range []string{StatusUnsubscribed, StatusBounced, StatusComplained} {
		c := h.add(t, status+"@example.com", "")
		if _, err := h.svc.DB.Exec(ctx, `UPDATE contacts SET status = $2 WHERE id = $1`, c.ID, status); err != nil {
			t.Fatal(err)
		}
		if err := h.svc.Delete(ctx, h.acc, c.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.Get(ctx, h.acc, c.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: still there after delete: %v", status, err)
		}
		if ok, _ := h.svc.IsSuppressed(ctx, h.acc, c.Email); !ok {
			t.Errorf("%s: not suppressed after delete", status)
		}
		// Re-adding the address keeps the suppressed status, whatever is asked.
		again, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "  " + c.Email, Status: StatusSubscribed})
		if err != nil || again.Status != status {
			t.Errorf("%s: re-added as %+v, %v", status, again, err)
		}
		// The contact now carries the status, so the entry is gone until the
		// next delete.
		if ok, _ := h.svc.IsSuppressed(ctx, h.acc, c.Email); ok {
			t.Errorf("%s: suppression kept after re-add", status)
		}
	}

	// A subscribed or unconfirmed contact leaves nothing behind.
	c := h.add(t, "pat@example.com", StatusUnconfirmed)
	if err := h.svc.Delete(ctx, h.acc, c.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.svc.IsSuppressed(ctx, h.acc, "pat@example.com"); ok {
		t.Error("unconfirmed contact left a suppression")
	}
	var stored int
	if err := h.svc.DB.QueryRow(ctx, `SELECT count(*) FROM suppressions WHERE email_hash LIKE '%@%'`).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("suppression list holds addresses: %d %v", stored, err)
	}
	// Suppressions are per account.
	if ok, _ := h.svc.IsSuppressed(ctx, h.acc2, "bounced@example.com"); ok {
		t.Error("suppression leaked to another account")
	}
	if err := h.svc.Delete(ctx, h.acc, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestChangingEmailToSuppressedAddressKeepsSuppression(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	gone := h.add(t, "old@example.com", StatusUnsubscribed)
	if err := h.svc.Delete(ctx, h.acc, gone.ID); err != nil {
		t.Fatal(err)
	}
	c := h.add(t, "new@example.com", "")
	u, err := h.svc.Update(ctx, h.acc, c.ID, UpdateInput{Email: set("old@example.com")})
	if err != nil || u.Status != StatusUnsubscribed {
		t.Errorf("update = %+v, %v", u, err)
	}
}

func TestListFiltersAndPages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, in := range []CreateInput{
		{Email: "ann@example.com", FirstName: str("Ann"), LastName: str("Smith")},
		{Email: "bob@example.org", FirstName: str("Bob"), Status: StatusUnconfirmed},
		{Email: "cat@example.com", LastName: str("Smithers")},
		{Email: "dan_x@example.com"},
		{Email: "danax@example.com"},
	} {
		if _, err := h.svc.Create(ctx, h.acc, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.Create(ctx, h.acc2, CreateInput{Email: "ann@example.com"}); err != nil {
		t.Fatal(err)
	}
	emails := func(cs []*Contact) []string {
		out := []string{}
		for _, c := range cs {
			out = append(out, c.Email)
		}
		return out
	}
	for _, tc := range []struct {
		f    Filter
		want []string
	}{
		{Filter{}, []string{"danax@example.com", "dan_x@example.com", "cat@example.com", "bob@example.org", "ann@example.com"}},
		{Filter{Email: "EXAMPLE.ORG"}, []string{"bob@example.org"}},
		{Filter{Email: "dan_"}, []string{"dan_x@example.com"}}, // _ is literal
		{Filter{Name: "smith"}, []string{"cat@example.com", "ann@example.com"}},
		{Filter{Name: "ann smith"}, []string{"ann@example.com"}},
		{Filter{Status: StatusUnconfirmed}, []string{"bob@example.org"}},
		{Filter{Name: "smith", Email: "cat"}, []string{"cat@example.com"}},
	} {
		got, err := h.svc.List(ctx, h.acc, tc.f, nil, 50)
		if err != nil || len(got) != len(tc.want) {
			t.Errorf("%+v: %v %v, want %v", tc.f, emails(got), err, tc.want)
			continue
		}
		for i := range got {
			if got[i].Email != tc.want[i] {
				t.Errorf("%+v: %v, want %v", tc.f, emails(got), tc.want)
				break
			}
		}
	}

	var all []string
	var after *Cursor
	for {
		page, err := h.svc.List(ctx, h.acc, Filter{}, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, emails(page)...)
		if len(page) < 2 {
			break
		}
		cur := CursorOf(page[len(page)-1])
		after = &cur
	}
	if len(all) != 5 || all[0] != "danax@example.com" || all[4] != "ann@example.com" {
		t.Errorf("paged = %v", all)
	}
}

func TestOnlySubscribedContactsAreRecipients(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	want := map[string]bool{}
	for _, status := range []string{StatusSubscribed, StatusUnconfirmed, StatusUnsubscribed, StatusSubscribed, StatusBounced, StatusComplained, StatusSubscribed} {
		c := h.add(t, status+time.Now().Format("150405.000000000")+"@example.com", "")
		if _, err := h.svc.DB.Exec(ctx, `UPDATE contacts SET status = $2 WHERE id = $1`, c.ID, status); err != nil {
			t.Fatal(err)
		}
		if status == StatusSubscribed {
			want[c.ID] = true
		}
	}
	if _, err := h.svc.Create(ctx, h.acc2, CreateInput{Email: "other@example.com"}); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	after := ""
	for {
		batch, err := h.svc.Recipients(ctx, h.acc, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range batch {
			if !CanReceive(c.Status) || got[c.ID] {
				t.Errorf("recipient %+v", c)
			}
			got[c.ID] = true
		}
		if len(batch) < 2 {
			break
		}
		after = batch[len(batch)-1].ID
	}
	if len(got) != len(want) {
		t.Errorf("recipients = %d, want %d", len(got), len(want))
	}
	for id := range want {
		if !got[id] {
			t.Errorf("subscribed contact %s missing", id)
		}
	}
	for _, s := range []string{StatusUnconfirmed, StatusUnsubscribed, StatusBounced, StatusComplained} {
		if CanReceive(s) {
			t.Errorf("CanReceive(%s)", s)
		}
	}
}
