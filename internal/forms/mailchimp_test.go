package forms

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func tagNames(f *Form) []string {
	var out []string
	for _, t := range f.Tags {
		out = append(out, t.Name)
	}
	slices.Sort(out)
	return out
}

func TestMailchimpDraftsForOneAudience(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	made, err := h.svc.MailchimpDrafts(ctx, h.acc, []MailchimpDraft{{Audience: "Bakery news"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 1 || made[0].Name != "Bakery news" || made[0].Audience != "Bakery news" || !made[0].New || made[0].ID == "" {
		t.Fatalf("made = %+v", made)
	}
	f, err := h.svc.Get(ctx, h.acc, made[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []Field{{Name: FieldEmail, Label: "Email", Required: true}, {Name: FieldFirstName, Label: "First name"}, {Name: FieldLastName, Label: "Last name"}}
	if !slices.Equal(f.Fields, want) || !f.DoubleOptIn || !f.Draft || f.MailchimpAudience == nil || *f.MailchimpAudience != "Bakery news" ||
		len(f.Tags) != 0 || f.SuccessMessage != DefaultSuccessConfirm || len(f.SigningKey) != 32 {
		t.Fatalf("form = %+v", f)
	}

	// Saving it, even with nothing changed, makes it live.
	saved, err := h.svc.Update(ctx, h.acc, f.ID, Changes{})
	if err != nil || saved.Draft {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	// A form made by hand is never a draft.
	own, err := h.svc.Create(ctx, h.acc, Changes{Name: ptr("Mine")})
	if err != nil || own.Draft || own.MailchimpAudience != nil {
		t.Fatalf("own = %+v, %v", own, err)
	}
}

func TestMailchimpDraftsForSeveralAudiencesWithTags(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// One tag exists already, named with other capitals; the form takes it
	// rather than making a second.
	existing := h.tag(t, h.acc, "interests: baking")
	made, err := h.svc.MailchimpDrafts(ctx, h.acc, []MailchimpDraft{
		{Audience: "Bakery news", Tags: []string{"audience: Bakery news", "Interests: Baking"}},
		{Audience: "Wholesale", Tags: []string{"audience: Wholesale"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 2 || made[0].Name != "Bakery news" || made[1].Name != "Wholesale" || !made[0].New || !made[1].New {
		t.Fatalf("made = %+v", made)
	}
	news, err := h.svc.Get(ctx, h.acc, made[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tagNames(news); !slices.Equal(got, []string{"audience: Bakery news", "interests: baking"}) {
		t.Errorf("news tags = %v", got)
	}
	if !slices.ContainsFunc(news.Tags, func(tg Tag) bool { return tg.ID == existing }) {
		t.Errorf("news doesn't use the existing tag: %+v", news.Tags)
	}
	wholesale, err := h.svc.Get(ctx, h.acc, made[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := tagNames(wholesale); !slices.Equal(got, []string{"audience: Wholesale"}) {
		t.Errorf("wholesale tags = %v", got)
	}

	// Too many tags for one form is refused before anything is made.
	many := make([]string, MaxTags+1)
	for i := range many {
		many[i] = strings.Repeat("t", i+1)
	}
	if _, err := h.svc.MailchimpDrafts(ctx, h.acc2, []MailchimpDraft{{Audience: "Big", Tags: many}}); field(err) != "form_tags" {
		t.Errorf("too many tags: %v", err)
	}
	var n int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM forms WHERE account_id = $1`, h.acc2).Scan(&n); err != nil || n != 0 {
		t.Errorf("forms after the refusal = %d, %v", n, err)
	}
}

func TestMailchimpDraftsAgainMakesNoSecondForm(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.svc.MailchimpDrafts(ctx, h.acc, []MailchimpDraft{{Audience: "Bakery news"}})
	if err != nil {
		t.Fatal(err)
	}
	// The customer renames and saves it, then imports again: the audience
	// keeps that form as it is, found ignoring case, and a new audience
	// gets one.
	if _, err := h.svc.Update(ctx, h.acc, first[0].ID, Changes{Name: ptr("Newsletter signup")}); err != nil {
		t.Fatal(err)
	}
	again, err := h.svc.MailchimpDrafts(ctx, h.acc, []MailchimpDraft{
		{Audience: "BAKERY NEWS", Tags: []string{"audience: BAKERY NEWS"}},
		{Audience: "Wholesale"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 || again[0].ID != first[0].ID || again[0].New || again[0].Name != "Newsletter signup" || !again[1].New {
		t.Fatalf("again = %+v", again)
	}
	f, err := h.svc.Get(ctx, h.acc, first[0].ID)
	if err != nil || f.Draft || len(f.Tags) != 0 {
		t.Fatalf("kept form = %+v, %v", f, err)
	}
	var n int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM forms WHERE account_id = $1`, h.acc).Scan(&n); err != nil || n != 2 {
		t.Errorf("forms = %d, %v", n, err)
	}
	// Another account's audience of the same name is its own.
	other, err := h.svc.MailchimpDrafts(ctx, h.acc2, []MailchimpDraft{{Audience: "Bakery news"}})
	if err != nil || !other[0].New || other[0].ID == first[0].ID {
		t.Fatalf("other account = %+v, %v", other, err)
	}
}
