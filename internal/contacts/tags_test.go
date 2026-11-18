package contacts

import (
	"context"
	"errors"
	"testing"
)

func (h *harness) tag(t *testing.T, name string) *Tag {
	t.Helper()
	tg, err := h.svc.CreateTag(context.Background(), h.acc, name)
	if err != nil {
		t.Fatalf("create tag %s: %v", name, err)
	}
	return tg
}

func tagNames(c *Contact) []string {
	out := []string{}
	for _, t := range c.Tags {
		out = append(out, t.Name)
	}
	return out
}

func TestTagCreateRenameList(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	vip := h.tag(t, " VIP ")
	if vip.Name != "VIP" || vip.Contacts != 0 {
		t.Errorf("tag = %+v", vip)
	}
	if _, err := h.svc.CreateTag(ctx, h.acc, "vip"); !errors.Is(err, ErrTagExists) {
		t.Errorf("same name, other case: %v", err)
	}
	if _, err := h.svc.CreateTag(ctx, h.acc2, "vip"); err != nil {
		t.Errorf("other account: %v", err)
	}
	for _, bad := range []string{"", "  ", "a,b", "a\nb", string(make([]byte, MaxTagNameLen+1))} {
		if _, err := h.svc.CreateTag(ctx, h.acc, bad); code(err) != "invalid_tag_name" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	news := h.tag(t, "newsletter")
	h.tag(t, "Alpha")
	if _, err := h.svc.RenameTag(ctx, h.acc, news.ID, "VIP"); !errors.Is(err, ErrTagExists) {
		t.Errorf("rename onto existing: %v", err)
	}
	r, err := h.svc.RenameTag(ctx, h.acc, news.ID, "Weekly")
	if err != nil || r.Name != "Weekly" {
		t.Errorf("rename: %+v %v", r, err)
	}
	if _, err := h.svc.RenameTag(ctx, h.acc2, news.ID, "x"); !errors.Is(err, ErrTagNotFound) {
		t.Errorf("rename from other account: %v", err)
	}

	var names []string
	var after *TagCursor
	for {
		page, err := h.svc.ListTags(ctx, h.acc, "", after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, tg := range page {
			names = append(names, tg.Name)
		}
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &TagCursor{Name: last.Name, ID: last.ID}
	}
	if len(names) != 3 || names[0] != "Alpha" || names[1] != "VIP" || names[2] != "Weekly" {
		t.Errorf("list = %v", names)
	}
	if one, err := h.svc.ListTags(ctx, h.acc, "weekly", nil, 10); err != nil || len(one) != 1 || one[0].ID != news.ID {
		t.Errorf("by name: %v %v", one, err)
	}
}

func TestTagOneAndManyContacts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	vip := h.tag(t, "vip")
	pat := h.add(t, "pat@example.com", "")
	sam := h.add(t, "sam@example.org", StatusUnconfirmed)
	lee := h.add(t, "lee@example.org", "")
	other, err := h.svc.Create(ctx, h.acc2, CreateInput{Email: "pat@example.com"})
	if err != nil {
		t.Fatal(err)
	}

	// One contact.
	ch, err := h.svc.TagContacts(ctx, h.acc, vip.ID, Selection{IDs: []string{pat.ID}})
	if err != nil || ch.Matched != 1 || ch.Changed != 1 {
		t.Fatalf("tag one: %+v %v", ch, err)
	}
	if c, _ := h.svc.Get(ctx, h.acc, pat.ID); len(c.Tags) != 1 || c.Tags[0].ID != vip.ID || c.Tags[0].Name != "vip" {
		t.Errorf("pat's tags = %+v", c.Tags)
	}
	// Ids of other accounts are skipped; tagging twice changes nothing.
	ch, err = h.svc.TagContacts(ctx, h.acc, vip.ID, Selection{IDs: []string{pat.ID, sam.ID, other.ID}})
	if err != nil || ch.Matched != 2 || ch.Changed != 1 {
		t.Errorf("tag ids: %+v %v", ch, err)
	}
	// By filter.
	ch, err = h.svc.TagContacts(ctx, h.acc, vip.ID, Selection{Filter: &Filter{Email: "example.org"}})
	if err != nil || ch.Matched != 2 || ch.Changed != 1 {
		t.Errorf("tag by filter: %+v %v", ch, err)
	}
	if got, _ := h.svc.List(ctx, h.acc, Filter{TagID: vip.ID}, nil, 10); len(got) != 3 {
		t.Errorf("list by tag = %d", len(got))
	}
	if tg, _ := h.svc.GetTag(ctx, h.acc, vip.ID); tg.Contacts != 3 {
		t.Errorf("tag count = %d", tg.Contacts)
	}
	ch, err = h.svc.UntagContacts(ctx, h.acc, vip.ID, Selection{Filter: &Filter{Status: StatusUnconfirmed}})
	if err != nil || ch.Matched != 1 || ch.Changed != 1 {
		t.Errorf("untag by filter: %+v %v", ch, err)
	}
	ch, err = h.svc.UntagContacts(ctx, h.acc, vip.ID, Selection{IDs: []string{sam.ID, lee.ID}})
	if err != nil || ch.Matched != 2 || ch.Changed != 1 {
		t.Errorf("untag ids: %+v %v", ch, err)
	}
	if c, _ := h.svc.Get(ctx, h.acc, other.ID); c != nil {
		t.Error("other account's contact visible")
	}
	if c, _ := h.svc.Get(ctx, h.acc2, other.ID); len(c.Tags) != 0 {
		t.Errorf("other account's contact tagged: %v", tagNames(c))
	}

	for _, sel := range []Selection{{}, {IDs: []string{}}, {IDs: []string{"x"}}, {IDs: []string{pat.ID}, Filter: &Filter{}}, {IDs: make([]string, MaxBulkIDs+1)}} {
		if _, err := h.svc.TagContacts(ctx, h.acc, vip.ID, sel); code(err) != "invalid_selection" {
			t.Errorf("%+v: %v", sel, err)
		}
	}
	if _, err := h.svc.TagContacts(ctx, h.acc2, vip.ID, Selection{IDs: []string{other.ID}}); !errors.Is(err, ErrTagNotFound) {
		t.Errorf("tag of other account: %v", err)
	}
	// An empty filter is every contact.
	if ch, err := h.svc.TagContacts(ctx, h.acc, vip.ID, Selection{Filter: &Filter{}}); err != nil || ch.Matched != 3 {
		t.Errorf("empty filter: %+v %v", ch, err)
	}
}

func TestDeleteTagRemovesItFromContacts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	vip, keep := h.tag(t, "vip"), h.tag(t, "keep")
	pat := h.add(t, "pat@example.com", "")
	sam := h.add(t, "sam@example.com", "")
	for _, tg := range []*Tag{vip, keep} {
		if _, err := h.svc.TagContacts(ctx, h.acc, tg.ID, Selection{IDs: []string{pat.ID, sam.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.svc.DeleteTag(ctx, h.acc2, vip.ID); !errors.Is(err, ErrTagNotFound) {
		t.Errorf("delete from other account: %v", err)
	}
	d, err := h.svc.DeleteTag(ctx, h.acc, vip.ID)
	if err != nil || d.ContactsUntagged != 2 || d.CampaignsUpdated != 0 {
		t.Fatalf("delete = %+v %v", d, err)
	}
	if c, _ := h.svc.Get(ctx, h.acc, pat.ID); len(c.Tags) != 1 || c.Tags[0].Name != "keep" {
		t.Errorf("pat's tags = %v", tagNames(c))
	}
	if _, err := h.svc.GetTag(ctx, h.acc, vip.ID); !errors.Is(err, ErrTagNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	// Deleting a contact takes it off its tags.
	if err := h.svc.Delete(ctx, h.acc, sam.ID); err != nil {
		t.Fatal(err)
	}
	if tg, _ := h.svc.GetTag(ctx, h.acc, keep.ID); tg.Contacts != 1 {
		t.Errorf("keep count = %d", tg.Contacts)
	}
}
