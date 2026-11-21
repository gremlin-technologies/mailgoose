package emails

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

// everyBlock is a document with one of each block type, with ids.
const everyBlock = `{
  "schema_version": 1,
  "settings": {"subject": "Spring menu", "preview_text": "New bakes", "from_name": "Pat's Bakery",
    "from_email": "news@bakery.example", "reply_to": null, "background_color": "#ffffff", "content_width": 640},
  "blocks": [
    {"id": "h1", "type": "heading", "text": "Hello", "level": 2, "align": "center"},
    {"id": "t1", "type": "text", "text": "**Fresh** bread, *daily*.\n\n- rye\n- spelt", "align": "left"},
    {"id": "i1", "type": "image", "src": "https://cdn.example/bread.jpg", "alt": "Bread", "href": "https://bakery.example", "width": 300, "align": "center"},
    {"id": "bt", "type": "button", "text": "Order", "href": "https://bakery.example/order", "align": "center", "background_color": "#000000", "text_color": "#ffffff"},
    {"id": "d1", "type": "divider", "color": "#cccccc", "thickness": 2},
    {"id": "s1", "type": "spacer", "height": 32},
    {"id": "c1", "type": "columns", "layout": "1:1", "columns": [
      {"blocks": [{"id": "c1a", "type": "text", "text": "Left", "align": "left"}]},
      {"blocks": [{"id": "c1b", "type": "image", "src": "https://cdn.example/x.png", "alt": "", "href": null, "width": null, "align": "center"}]}
    ]},
    {"id": "so", "type": "social", "links": [{"network": "instagram", "url": "https://instagram.com/bakery"}, {"network": "website", "url": "https://bakery.example", "label": "Our site"}],
     "display": "icons_and_text", "color": "dark", "size": 40, "spacing": 24, "layout": "vertical", "align": "center"},
    {"id": "ft", "type": "footer", "text": "You signed up at the counter."}
  ]
}`

func TestEveryBlockRoundTrips(t *testing.T) {
	d, err := ParseDocument(KindBlocks, []byte(everyBlock))
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, out, []byte(everyBlock)) {
		t.Errorf("round trip changed the document:\n%s", out)
	}
	// And again from its own output.
	d2, err := ParseDocument(KindBlocks, out)
	if err != nil {
		t.Fatal(err)
	}
	out2, _ := json.Marshal(d2)
	if string(out2) != string(out) {
		t.Errorf("second round trip differs:\n%s\n%s", out, out2)
	}
}

func TestDefaultsIDsAndFooter(t *testing.T) {
	d, err := ParseDocument(KindBlocks, []byte(`{"blocks":[{"type":"heading","text":"Hi"},{"type":"spacer"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if d.Settings != DefaultSettings() || d.SchemaVersion != 1 {
		t.Errorf("settings = %+v", d.Settings)
	}
	if len(d.Blocks) != 3 || d.Blocks[2].BlockType() != TypeFooter {
		t.Fatalf("blocks = %+v; want a footer added at the end", d.Blocks)
	}
	h := d.Blocks[0].(*Heading)
	if h.Level != 1 || h.Align != "left" || d.Blocks[1].(*Spacer).Height != 24 {
		t.Errorf("defaults not applied: %+v %+v", h, d.Blocks[1])
	}
	seen := map[string]bool{}
	for _, b := range d.Blocks {
		if !strings.HasPrefix(b.BlockID(), "b_") || seen[b.BlockID()] {
			t.Errorf("block id %q", b.BlockID())
		}
		seen[b.BlockID()] = true
	}
	// New ids never take one the document already uses.
	d, err = ParseDocument(KindBlocks, []byte(`{"blocks":[{"type":"spacer"},{"id":"mine","type":"spacer"}]}`))
	if err != nil || d.Blocks[1].BlockID() != "mine" || d.Blocks[0].BlockID() == "mine" {
		t.Errorf("ids = %v, %v", d.Blocks, err)
	}
}

// A social block saved before MG-109 had only links and align; it reads
// with the new fields' defaults.
func TestSocialDefaults(t *testing.T) {
	d, err := ParseDocument(KindBlocks, []byte(`{"blocks":[{"type":"social","links":[{"network":"x","url":"https://x.example"}],"align":"left"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	s := d.Blocks[0].(*Social)
	want := Social{base: s.base, Links: s.Links, Display: "icons", Color: "brand", Size: 32, Spacing: 12, Layout: "horizontal", Align: "left"}
	if !reflect.DeepEqual(*s, want) {
		t.Errorf("social = %+v, want %+v", *s, want)
	}
	raw, _ := json.Marshal(s.Links)
	if string(raw) != `[{"network":"x","url":"https://x.example"}]` {
		t.Errorf("links = %s; an empty label stays out", raw)
	}
}

func TestDocumentErrors(t *testing.T) {
	for _, tc := range []struct{ doc, path string }{
		{`{"blocks":[{"type":"video","src":"x"}]}`, "blocks[0].type"},
		{`{"blocks":[{"text":"x"}]}`, "blocks[0].type"},
		{`{"blocks":[{"type":"heading","text":"x","colour":"red"}]}`, "blocks[0].colour"},
		{`{"blocks":[],"theme":"dark"}`, "theme"},
		{`{"blocks":[],"settings":{"font":"Arial"}}`, "settings.font"},
		{`{"blocks":[],"settings":{"content_width":"wide"}}`, "settings.content_width"},
		{`{"blocks":[],"settings":{"content_width":1200}}`, "settings.content_width"},
		{`{"blocks":[],"settings":{"subject":null}}`, "settings.subject"},
		{`{"blocks":[],"settings":{"from_email":"Pat <pat@example.com>"}}`, "settings.from_email"},
		{`{"blocks":[],"schema_version":2}`, "schema_version"},
		{`{"settings":{}}`, "blocks"},
		{`{"blocks":[],"html":"<p>x</p>"}`, "html"},
		{`{"blocks":[{"type":"heading","text":""}]}`, "blocks[0].text"},
		{`{"blocks":[{"type":"heading","text":"x","level":4}]}`, "blocks[0].level"},
		{`{"blocks":[{"type":"heading","text":"x","level":"big"}]}`, "blocks[0].level"},
		{`{"blocks":[{"type":"heading","text":"x","align":null}]}`, "blocks[0].align"},
		{`{"blocks":[{"type":"image","src":"javascript:alert(1)"}]}`, "blocks[0].src"},
		{`{"blocks":[{"type":"button","text":"Go","href":"/relative"}]}`, "blocks[0].href"},
		{`{"blocks":[{"type":"button","text":"Go","href":"https://x.example","text_color":"white"}]}`, "blocks[0].text_color"},
		{`{"blocks":[{"type":"social","links":[{"network":"myspace","url":"https://x.example"}]}]}`, "blocks[0].links[0].network"},
		{`{"blocks":[{"type":"social","links":[{"network":"x","url":"https://x.example","handle":"X"}]}]}`, "blocks[0].links[0].handle"},
		{`{"blocks":[{"type":"social","links":[{"network":"x","url":"https://x.example","label":"X\nY"}]}]}`, "blocks[0].links[0].label"},
		{`{"blocks":[{"type":"social","display":"logos","links":[{"network":"x","url":"https://x.example"}]}]}`, "blocks[0].display"},
		{`{"blocks":[{"type":"social","color":"gray","links":[{"network":"x","url":"https://x.example"}]}]}`, "blocks[0].color"},
		{`{"blocks":[{"type":"social","size":24,"links":[{"network":"x","url":"https://x.example"}]}]}`, "blocks[0].size"},
		{`{"blocks":[{"type":"social","spacing":60,"links":[{"network":"x","url":"https://x.example"}]}]}`, "blocks[0].spacing"},
		{`{"blocks":[{"type":"social","layout":"grid","links":[{"network":"x","url":"https://x.example"}]}]}`, "blocks[0].layout"},
		{`{"blocks":[{"type":"columns","columns":[{"blocks":[]}]}]}`, "blocks[0].columns"},
		{`{"blocks":[{"type":"columns","columns":[{"blocks":[]},{"blocks":[{"type":"columns","columns":[]}]}]}]}`, "blocks[0].columns[1].blocks[0].type"},
		{`{"blocks":[{"type":"columns","columns":[{"blocks":[{"type":"footer"}]},{"blocks":[]}]}]}`, "blocks[0].columns[0].blocks[0].type"},
		{`{"blocks":[{"type":"columns","columns":[{"blocks":[{"type":"text","text":"x","bold":true}]},{"blocks":[]}]}]}`, "blocks[0].columns[0].blocks[0].bold"},
		{`{"blocks":[{"type":"columns","columns":[{"blocks":[],"width":50},{"blocks":[]}]}]}`, "blocks[0].columns[0].width"},
		{`{"blocks":[{"type":"columns","layout":"2:2","columns":[{"blocks":[]},{"blocks":[]}]}]}`, "blocks[0].layout"},
		{`{"blocks":[{"type":"columns","layout":"1:1:1","columns":[{"blocks":[]},{"blocks":[]}]}]}`, "blocks[0].columns"},
		{`{"blocks":[{"type":"columns","layout":"1","columns":[]}]}`, "blocks[0].columns"},
		{`{"blocks":[{"type":"footer"},{"type":"spacer"}]}`, "blocks[0]"},
		{`{"blocks":[{"id":"a","type":"spacer"},{"id":"a","type":"spacer"}]}`, "blocks[1].id"},
		{`{"blocks":[{"id":"a b","type":"spacer"}]}`, "blocks[0].id"},
	} {
		_, err := ParseDocument(KindBlocks, []byte(tc.doc))
		var de *DocError
		if !errors.As(err, &de) {
			t.Errorf("%s: err = %v, want a DocError", tc.doc, err)
			continue
		}
		if de.Path != tc.path {
			t.Errorf("%s: path = %q (%v), want %q", tc.doc, de.Path, de, tc.path)
		}
	}

	var many []string
	for range MaxBlocks + 1 {
		many = append(many, `{"type":"spacer"}`)
	}
	if _, err := ParseDocument(KindBlocks, []byte(`{"blocks":[`+strings.Join(many, ",")+`]}`)); err == nil {
		t.Error("a document over the block limit was accepted")
	}
}

func TestHTMLDocuments(t *testing.T) {
	d, err := ParseDocument(KindHTML, []byte(`{"settings":{"subject":"Hi"},"html":"<p>Hi</p>"}`))
	if err != nil || d.Kind() != KindHTML || *d.HTML != "<p>Hi</p>" || d.Settings.Subject != "Hi" {
		t.Fatalf("doc = %+v, %v", d, err)
	}
	out, _ := json.Marshal(d)
	var back map[string]any
	_ = json.Unmarshal(out, &back)
	if back["html"] != "<p>Hi</p>" || back["blocks"] != nil {
		t.Errorf("json = %s", out)
	}
	for _, doc := range []string{`{"settings":{}}`, `{"html":"x","blocks":[]}`, `{"html":"` + strings.Repeat("x", MaxHTMLBytes+1) + `"}`} {
		if _, err := ParseDocument(KindHTML, []byte(doc)); err == nil {
			t.Errorf("html %.40s accepted", doc)
		}
	}
}

func TestEmailsStore(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	s := &Service{DB: db}
	var acct, other string
	for _, id := range []*string{&acct, &other} {
		if err := db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(id); err != nil {
			t.Fatal(err)
		}
	}

	e, err := s.Create(ctx, acct, "  Spring  ", KindBlocks, nil)
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "Spring" || e.Version != 1 || len(e.Document.Blocks) != 1 || e.Document.Blocks[0].BlockType() != TypeFooter {
		t.Fatalf("new email = %+v", e)
	}
	if _, err := s.Get(ctx, other, e.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another account read it: %v", err)
	}
	if _, err := s.Create(ctx, acct, " ", KindBlocks, nil); !errors.Is(err, ErrInvalidName) {
		t.Errorf("blank name: %v", err)
	}
	if _, err := s.Create(ctx, acct, "x", "mjml", nil); !errors.Is(err, ErrInvalidKind) {
		t.Errorf("bad kind: %v", err)
	}

	// Replace with every block, then move blocks around: ids stay put.
	doc, err := ParseDocument(KindBlocks, []byte(everyBlock))
	if err != nil {
		t.Fatal(err)
	}
	e, err = s.Replace(ctx, acct, e.ID, 1, doc)
	if err != nil || e.Version != 2 {
		t.Fatalf("replace = %+v, %v", e, err)
	}
	got, _ := json.Marshal(e.Document)
	if !sameJSON(t, got, []byte(everyBlock)) {
		t.Errorf("stored document differs:\n%s", got)
	}
	moved := doc.Blocks
	moved[0], moved[1] = moved[1], moved[0]
	e, err = s.Replace(ctx, acct, e.ID, 2, &Document{SchemaVersion: 1, Settings: doc.Settings, Blocks: moved})
	if err != nil {
		t.Fatal(err)
	}
	if e.Document.Blocks[0].BlockID() != "t1" || e.Document.Blocks[1].BlockID() != "h1" {
		t.Errorf("after the move: %s, %s", e.Document.Blocks[0].BlockID(), e.Document.Blocks[1].BlockID())
	}

	// A stale version is refused and changes nothing.
	_, err = s.Replace(ctx, acct, e.ID, 2, NewDocument(KindBlocks))
	var ve *VersionError
	if !errors.As(err, &ve) || ve.Current != 3 {
		t.Errorf("stale replace: %v", err)
	}
	if again, _ := s.Get(ctx, acct, e.ID); again.Version != 3 || len(again.Document.Blocks) != 9 {
		t.Errorf("stale replace changed it: %+v", again)
	}
	if _, err := s.Replace(ctx, acct, e.ID, 3, NewDocument(KindHTML)); !errors.Is(err, ErrInvalidKind) {
		t.Errorf("html into a blocks email: %v", err)
	}

	// Update settings in part; blocks don't change.
	name := "Spring menu"
	e, err = s.Update(ctx, acct, e.ID, 3, Change{Name: &name, Settings: json.RawMessage(`{"subject":"New","reply_to":"hi@bakery.example"}`)})
	if err != nil {
		t.Fatal(err)
	}
	st := e.Document.Settings
	if e.Name != name || st.Subject != "New" || *st.ReplyTo != "hi@bakery.example" || st.PreviewText != "New bakes" || e.Version != 4 {
		t.Errorf("updated = %+v %+v", e, st)
	}
	e, err = s.Update(ctx, acct, e.ID, 4, Change{Settings: json.RawMessage(`{"from_email":null}`)})
	if err != nil || e.Document.Settings.FromEmail != nil {
		t.Errorf("clearing from_email: %+v, %v", e, err)
	}
	var de *DocError
	if _, err := s.Update(ctx, acct, e.ID, 5, Change{Settings: json.RawMessage(`{"color":"red"}`)}); !errors.As(err, &de) {
		t.Errorf("unknown setting: %v", err)
	}
	if _, err := s.Update(ctx, other, e.ID, 5, Change{Name: &name}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update from another account: %v", err)
	}

	dup, err := s.Duplicate(ctx, acct, e.ID, "")
	if err != nil || dup.Name != "Copy of Spring menu" || dup.Version != 1 || dup.ID == e.ID {
		t.Fatalf("duplicate = %+v, %v", dup, err)
	}
	a, _ := json.Marshal(e.Document)
	b, _ := json.Marshal(dup.Document)
	if string(a) != string(b) {
		t.Error("the copy's document differs")
	}

	list, err := s.List(ctx, acct, nil, 10)
	if err != nil || len(list) != 2 || list[0].ID != dup.ID {
		t.Fatalf("list = %v, %v", list, err)
	}
	rest, err := s.List(ctx, acct, &Cursor{CreatedAt: list[0].CreatedAt, ID: list[0].ID}, 10)
	if err != nil || len(rest) != 1 || rest[0].ID != e.ID {
		t.Errorf("second page = %v, %v", rest, err)
	}

	if err := s.Delete(ctx, acct, dup.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, acct, dup.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestConcurrentUpdatesOneWins(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	s := &Service{DB: db}
	var acct string
	if err := db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	e, err := s.Create(ctx, acct, "x", KindBlocks, nil)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 8)
	for i := range 8 {
		go func() {
			subject := `{"subject":"v` + string(rune('a'+i)) + `"}`
			_, err := s.Update(ctx, acct, e.ID, 1, Change{Settings: json.RawMessage(subject)})
			errs <- err
		}()
	}
	won := 0
	for range 8 {
		var ve *VersionError
		switch err := <-errs; {
		case err == nil:
			won++
		case errors.As(err, &ve):
		default:
			t.Error(err)
		}
	}
	if won != 1 {
		t.Errorf("%d updates of version 1 went through, want 1", won)
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}
