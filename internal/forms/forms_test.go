package forms

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

type harness struct {
	db        *pgxpool.Pool
	svc       *Service
	acc, acc2 string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	h := &harness{db: db, svc: &Service{DB: db}}
	for _, p := range []*string{&h.acc, &h.acc2} {
		if err := db.QueryRow(context.Background(), `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	return h
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

func TestCreateWithDefaults(t *testing.T) {
	h := newHarness(t)
	f, err := h.svc.Create(context.Background(), h.acc, Changes{Name: ptr("  Newsletter ")})
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "Newsletter" || len(f.Fields) != 1 || f.Fields[0] != (Field{Name: "email", Label: "Email", Required: true}) ||
		f.ButtonText != "Subscribe" || !f.DoubleOptIn || f.SuccessMessage != DefaultSuccessConfirm || f.RedirectURL != nil ||
		f.ConfirmSubject != DefaultConfirmSubject || f.ConfirmText != DefaultConfirmText || len(f.SigningKey) != 32 || len(f.Tags) != 0 {
		t.Errorf("form = %+v", f)
	}
	// Without double opt-in the default message doesn't mention a
	// confirmation email.
	f, err = h.svc.Create(context.Background(), h.acc, Changes{Name: ptr("Quick"), DoubleOptIn: ptr(false)})
	if err != nil || f.DoubleOptIn || f.SuccessMessage != DefaultSuccess {
		t.Errorf("no double opt-in: %+v %v", f, err)
	}
}

func TestCreateEverything(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	vip, news := h.tag(t, h.acc, "VIP"), h.tag(t, h.acc, "news")
	f, err := h.svc.Create(ctx, h.acc, Changes{
		Name:           ptr("Footer"),
		Fields:         &[]Field{{Name: "first_name"}, {Name: "email", Label: "Your email", Required: true}, {Name: "last_name", Required: true}},
		TagIDs:         &[]string{vip, news, vip},
		ButtonText:     ptr("Join"),
		SuccessMessage: ptr("Welcome!"),
		RedirectURL:    ptr("https://bakery.example/thanks"),
		ConfirmSubject: ptr("Confirm, please"),
		ConfirmText:    ptr("One click and you're in."),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{{"first_name", "First name", false}, {"email", "Your email", true}, {"last_name", "Last name", true}}
	for i, fl := range f.Fields {
		if fl != want[i] {
			t.Errorf("field %d = %+v", i, fl)
		}
	}
	if len(f.Tags) != 2 || f.Tags[0].Name != "news" || f.Tags[1].Name != "VIP" {
		t.Errorf("tags = %+v", f.Tags)
	}
	if f.ButtonText != "Join" || *f.RedirectURL != "https://bakery.example/thanks" || f.ConfirmSubject != "Confirm, please" {
		t.Errorf("form = %+v", f)
	}
	if !f.Asks(FieldLastName) || (&Form{}).Asks(FieldEmail) {
		t.Error("Asks")
	}
}

func TestValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	long := strings.Repeat("x", 3000)
	cases := map[string]Changes{
		"name":                {Name: ptr(" ")},
		"fields":              {Name: ptr("x"), Fields: &[]Field{{Name: "first_name"}}},
		"fields[0].name":      {Name: ptr("x"), Fields: &[]Field{{Name: "phone"}}},
		"fields[1].name":      {Name: ptr("x"), Fields: &[]Field{{Name: "email", Required: true}, {Name: "email", Required: true}}},
		"fields[0].required":  {Name: ptr("x"), Fields: &[]Field{{Name: "email"}}},
		"fields[0].label":     {Name: ptr("x"), Fields: &[]Field{{Name: "email", Required: true, Label: "a\nb"}}},
		"button_text":         {Name: ptr("x"), ButtonText: ptr(long)},
		"success_message":     {Name: ptr("x"), SuccessMessage: ptr(" ")},
		"redirect_url":        {Name: ptr("x"), RedirectURL: ptr("http://bakery.example")},
		"confirm_subject":     {Name: ptr("x"), ConfirmSubject: ptr("a\r\nBcc: x")},
		"confirm_text":        {Name: ptr("x"), ConfirmText: ptr(long)},
		"redirect_url (path)": {Name: ptr("x"), RedirectURL: ptr("/thanks")},
	}
	for want, c := range cases {
		_, err := h.svc.Create(ctx, h.acc, c)
		if got := field(err); got != strings.TrimSuffix(want, " (path)") {
			t.Errorf("%s: got %q (%v)", want, got, err)
		}
	}

	var many []string
	for i := 0; i < MaxTags+1; i++ {
		many = append(many, h.tag(t, h.acc, "t"+strings.Repeat("x", i)))
	}
	if _, err := h.svc.Create(ctx, h.acc, Changes{Name: ptr("x"), TagIDs: &many}); field(err) != "tag_ids" {
		t.Errorf("too many tags: %v", err)
	}
	other := h.tag(t, h.acc2, "theirs")
	for _, ids := range [][]string{{other}, {"not-a-uuid"}} {
		if _, err := h.svc.Create(ctx, h.acc, Changes{Name: ptr("x"), TagIDs: &ids}); !errors.Is(err, ErrUnknownTag) {
			t.Errorf("tag %v: %v", ids, err)
		}
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM forms`).Scan(&n)
	if n != 0 {
		t.Errorf("%d forms saved by failed creates", n)
	}
}

func TestUpdateListDelete(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	vip := h.tag(t, h.acc, "VIP")
	a, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("A"), TagIDs: &[]string{vip}, RedirectURL: ptr("https://x.example/ok")})
	b, _ := h.svc.Create(ctx, h.acc, Changes{Name: ptr("B")})
	if _, err := h.svc.Create(ctx, h.acc2, Changes{Name: ptr("Theirs")}); err != nil {
		t.Fatal(err)
	}

	// Only what's named changes; an empty redirect removes it.
	u, err := h.svc.Update(ctx, h.acc, a.ID, Changes{ButtonText: ptr("Go"), RedirectURL: ptr(""), DoubleOptIn: ptr(false)})
	if err != nil || u.Name != "A" || u.ButtonText != "Go" || u.RedirectURL != nil || u.DoubleOptIn || len(u.Tags) != 1 ||
		!u.UpdatedAt.After(a.UpdatedAt) && !u.UpdatedAt.Equal(a.UpdatedAt) {
		t.Errorf("update: %+v %v", u, err)
	}
	if u, err = h.svc.Update(ctx, h.acc, a.ID, Changes{TagIDs: &[]string{}}); err != nil || len(u.Tags) != 0 {
		t.Errorf("clear tags: %+v %v", u, err)
	}
	if _, err := h.svc.Update(ctx, h.acc, a.ID, Changes{Name: ptr("")}); field(err) != "name" {
		t.Errorf("bad update: %v", err)
	}
	if _, err := h.svc.Update(ctx, h.acc2, a.ID, Changes{Name: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("other account: %v", err)
	}

	page, err := h.svc.List(ctx, h.acc, nil, 1)
	if err != nil || len(page) != 1 || page[0].ID != b.ID {
		t.Fatalf("first page: %v %v", page, err)
	}
	page, err = h.svc.List(ctx, h.acc, &Cursor{CreatedAt: page[0].CreatedAt, ID: page[0].ID}, 10)
	if err != nil || len(page) != 1 || page[0].ID != a.ID || page[0].Tags == nil {
		t.Fatalf("second page: %v %v", page, err)
	}

	// Deleting the tag takes it off the form; deleting the form goes.
	if _, err := h.svc.Update(ctx, h.acc, a.ID, Changes{TagIDs: &[]string{vip}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM tags WHERE id = $1`, vip); err != nil {
		t.Fatal(err)
	}
	if g, _ := h.svc.Get(ctx, h.acc, a.ID); len(g.Tags) != 0 {
		t.Errorf("tags after tag delete: %+v", g.Tags)
	}
	if err := h.svc.Delete(ctx, h.acc, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Get(ctx, h.acc, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
	if err := h.svc.Delete(ctx, h.acc, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func TestStamp(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	at := time.Unix(1_800_000_000, 0)
	s := Stamp(key, "form-1", at)
	got, err := CheckStamp(key, "form-1", s)
	if err != nil || !got.Equal(at) {
		t.Fatalf("check: %v %v", got, err)
	}
	unix, sig, _ := strings.Cut(s, ".")
	for name, bad := range map[string]string{
		"other form":   "",
		"other time":   "1800000001." + sig,
		"no signature": unix,
		"negative":     "-1." + sig,
		"other key":    Stamp([]byte("another key"), "form-1", at),
		"tampered":     s[:len(s)-1] + "A",
	} {
		id := "form-1"
		if name == "other form" {
			id, bad = "form-2", s
		}
		if _, err := CheckStamp(key, id, bad); !errors.Is(err, ErrBadStamp) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
