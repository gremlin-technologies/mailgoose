package emails

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func intp(i int) *int { return &i }

// ids lists the document's block ids in order, a column's as "parent/c:id".
func ids(d *Document) string {
	var out []string
	for _, info := range ListBlocks(d) {
		id := info.Block.BlockID()
		if info.Parent != "" {
			id = info.Parent + "/" + string(rune('0'+*info.Column)) + ":" + id
		}
		out = append(out, id)
	}
	return strings.Join(out, " ")
}

func mustDoc(t *testing.T, raw string) *Document {
	t.Helper()
	d, err := ParseDocument(KindBlocks, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

const sampleDoc = `{"blocks":[
	{"id":"h","type":"heading","text":"Hi"},
	{"id":"cols","type":"columns","columns":[{"blocks":[{"id":"l1","type":"text","text":"Left"}]},{"blocks":[]}]},
	{"id":"t","type":"text","text":"Body"},
	{"id":"f","type":"footer"}]}`

func TestAddBlockPositions(t *testing.T) {
	cases := []struct {
		name string
		pos  Position
		want string
	}{
		{"default goes before the footer", Position{}, "h cols cols/0:l1 t new f"},
		{"index 0", Position{Index: intp(0)}, "new h cols cols/0:l1 t f"},
		{"index just before footer", Position{Index: intp(3)}, "h cols cols/0:l1 t new f"},
		{"after a block", Position{After: "h"}, "h new cols cols/0:l1 t f"},
		{"after a block in a column", Position{After: "l1"}, "h cols cols/0:l1 cols/0:new t f"},
		{"empty right column", Position{Parent: "cols", Column: intp(1)}, "h cols cols/0:l1 cols/1:new t f"},
		{"left column, first", Position{Parent: "cols", Column: intp(0), Index: intp(0)}, "h cols cols/0:new cols/0:l1 t f"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, id, err := AddBlock(mustDoc(t, sampleDoc), json.RawMessage(`{"id":"new","type":"spacer","height":20}`), c.pos)
			if err != nil {
				t.Fatal(err)
			}
			if id != "new" || ids(d) != c.want {
				t.Errorf("got %q (id %s), want %q", ids(d), id, c.want)
			}
		})
	}
}

func TestAddBlockGivesIDAndDefaults(t *testing.T) {
	d, id, err := AddBlock(mustDoc(t, sampleDoc), json.RawMessage(`{"type":"heading","text":"New"}`), Position{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "b_") {
		t.Errorf("new id %q", id)
	}
	info, err := FindBlock(d, id)
	if err != nil {
		t.Fatal(err)
	}
	h := info.Block.(*Heading)
	if h.Text != "New" || h.Level != 1 || info.Path != "blocks[3]" {
		t.Errorf("block %+v at %s", h, info.Path)
	}
}

func TestAddBlockRefusals(t *testing.T) {
	cases := []struct {
		name  string
		block string
		pos   Position
		want  any
	}{
		{"after the footer", `{"type":"spacer"}`, Position{After: "f"}, ErrFooterLocked},
		{"index past the footer", `{"type":"spacer"}`, Position{Index: intp(4)}, ErrFooterLocked},
		{"second footer", `{"type":"footer"}`, Position{}, ErrFooterLocked},
		{"negative index", `{"type":"spacer"}`, Position{Index: intp(-1)}, &PositionError{}},
		{"unknown after", `{"type":"spacer"}`, Position{After: "nope"}, &PositionError{}},
		{"after with index", `{"type":"spacer"}`, Position{After: "h", Index: intp(0)}, &PositionError{}},
		{"parent not columns", `{"type":"spacer"}`, Position{Parent: "h", Column: intp(0)}, &PositionError{}},
		{"parent without column", `{"type":"spacer"}`, Position{Parent: "cols"}, &PositionError{}},
		{"column out of range", `{"type":"spacer"}`, Position{Parent: "cols", Column: intp(2)}, &PositionError{}},
		{"column without parent", `{"type":"spacer"}`, Position{Column: intp(0)}, &PositionError{}},
		{"index past a column's end", `{"type":"spacer"}`, Position{Parent: "cols", Column: intp(1), Index: intp(1)}, &PositionError{}},
		{"columns in a column", `{"type":"columns","columns":[{"blocks":[]},{"blocks":[]}]}`, Position{Parent: "cols", Column: intp(1)}, &DocError{}},
		{"bad field", `{"type":"button","text":"Go","href":"ftp://x"}`, Position{}, &DocError{}},
		{"unknown type", `{"type":"video"}`, Position{}, &DocError{}},
		{"duplicate id", `{"id":"t","type":"spacer"}`, Position{}, &DocError{}},
		{"not an object", `[1]`, Position{}, &DocError{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := AddBlock(mustDoc(t, sampleDoc), json.RawMessage(c.block), c.pos)
			checkErr(t, err, c.want)
		})
	}
}

func checkErr(t *testing.T, err error, want any) {
	t.Helper()
	switch w := want.(type) {
	case *PositionError:
		var pe *PositionError
		if !errors.As(err, &pe) {
			t.Errorf("got %v, want a PositionError", err)
		}
	case *DocError:
		var de *DocError
		if !errors.As(err, &de) {
			t.Errorf("got %v, want a DocError", err)
		}
	case error:
		if !errors.Is(err, w) {
			t.Errorf("got %v, want %v", err, w)
		}
	}
}

func TestUpdateBlock(t *testing.T) {
	d, err := UpdateBlock(mustDoc(t, sampleDoc), "l1", json.RawMessage(`{"text":"Changed","align":"center"}`))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := FindBlock(d, "l1")
	if b := info.Block.(*Text); b.Text != "Changed" || b.Align != "center" {
		t.Errorf("block %+v", b)
	}
	if _, err := UpdateBlock(d, "f", json.RawMessage(`{"text":"Sent with care"}`)); err != nil {
		t.Errorf("the footer's text can change: %v", err)
	}

	for name, c := range map[string]struct {
		id, fields string
		want       any
	}{
		"unknown block":  {"nope", `{"text":"x"}`, ErrBlockNotFound},
		"change type":    {"h", `{"type":"text"}`, &DocError{}},
		"change id":      {"h", `{"id":"h2"}`, &DocError{}},
		"empty":          {"h", `{}`, &DocError{}},
		"unknown field":  {"h", `{"colour":"red"}`, &DocError{}},
		"invalid value":  {"h", `{"level":9}`, &DocError{}},
		"null not taken": {"h", `{"text":null}`, &DocError{}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := UpdateBlock(mustDoc(t, sampleDoc), c.id, json.RawMessage(c.fields))
			checkErr(t, err, c.want)
		})
	}
}

func TestMoveBlock(t *testing.T) {
	cases := []struct {
		name, id string
		pos      Position
		want     string
	}{
		{"to the top", "t", Position{Index: intp(0)}, "t h cols cols/0:l1 f"},
		{"to the end", "h", Position{}, "cols cols/0:l1 t h f"},
		{"index is the final place", "h", Position{Index: intp(1)}, "cols cols/0:l1 h t f"},
		{"into a column", "t", Position{Parent: "cols", Column: intp(1)}, "h cols cols/0:l1 cols/1:t f"},
		{"out of a column", "l1", Position{After: "t"}, "h cols t l1 f"},
		{"between columns", "l1", Position{Parent: "cols", Column: intp(1), Index: intp(0)}, "h cols cols/1:l1 t f"},
		{"columns with contents", "cols", Position{After: "t"}, "h t cols cols/0:l1 f"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := MoveBlock(mustDoc(t, sampleDoc), c.id, c.pos)
			if err != nil {
				t.Fatal(err)
			}
			if ids(d) != c.want {
				t.Errorf("got %q, want %q", ids(d), c.want)
			}
		})
	}

	for name, c := range map[string]struct {
		id   string
		pos  Position
		want any
	}{
		"the footer":       {"f", Position{Index: intp(0)}, ErrFooterLocked},
		"after the footer": {"h", Position{After: "f"}, ErrFooterLocked},
		"past the footer":  {"h", Position{Index: intp(4)}, ErrFooterLocked},
		"unknown":          {"nope", Position{}, ErrBlockNotFound},
		"after itself":     {"h", Position{After: "h"}, &PositionError{}},
		"into itself":      {"cols", Position{Parent: "cols", Column: intp(0)}, &PositionError{}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := MoveBlock(mustDoc(t, sampleDoc), c.id, c.pos)
			checkErr(t, err, c.want)
		})
	}
}

func TestRemoveBlock(t *testing.T) {
	d, err := RemoveBlock(mustDoc(t, sampleDoc), "cols")
	if err != nil {
		t.Fatal(err)
	}
	if ids(d) != "h t f" {
		t.Errorf("got %q", ids(d))
	}
	d, err = RemoveBlock(mustDoc(t, sampleDoc), "l1")
	if err != nil || ids(d) != "h cols t f" {
		t.Errorf("remove from a column: %q %v", ids(d), err)
	}
	if _, err := RemoveBlock(d, "f"); !errors.Is(err, ErrFooterLocked) {
		t.Errorf("footer: %v", err)
	}
	if _, err := RemoveBlock(d, "l1"); !errors.Is(err, ErrBlockNotFound) {
		t.Errorf("gone: %v", err)
	}
}

func TestBlockOpsOnHTML(t *testing.T) {
	d := NewDocument(KindHTML)
	if _, _, err := AddBlock(d, json.RawMessage(`{"type":"spacer"}`), Position{}); !errors.Is(err, ErrNotBlocks) {
		t.Errorf("add: %v", err)
	}
	if _, err := RemoveBlock(d, "x"); !errors.Is(err, ErrNotBlocks) {
		t.Errorf("remove: %v", err)
	}
}

// TestBlockOpsKeepNumbers checks values survive the trip through JSON
// exactly, so an edit to one block never changes another.
func TestBlockOpsKeepNumbers(t *testing.T) {
	d := mustDoc(t, `{"settings":{"content_width":640},"blocks":[{"id":"i","type":"image","src":"https://x.example/a.png","width":321},{"id":"s","type":"spacer","height":17}]}`)
	d, err := UpdateBlock(d, "s", json.RawMessage(`{"height":18}`))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := FindBlock(d, "i")
	if w := info.Block.(*Image).Width; w == nil || *w != 321 || d.Settings.ContentWidth != 640 {
		t.Errorf("image width %v, content width %d", w, d.Settings.ContentWidth)
	}
}
