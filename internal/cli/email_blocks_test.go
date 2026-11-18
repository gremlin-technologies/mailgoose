package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEmailBlocksBuildFromEmpty builds a four-block email from a new one
// using only block commands, then checks the document the API holds.
func TestEmailBlocksBuildFromEmpty(t *testing.T) {
	h := liveAPI(t)
	run := func(args ...string) map[string]any {
		t.Helper()
		if code := h.run(append(args, "--json")...); code != ExitOK {
			t.Fatalf("%v: exit %d %s", args, code, h.errOut)
		}
		return h.jsonOut(t)
	}
	blockID := func(out map[string]any) string {
		return out["block"].(map[string]any)["block"].(map[string]any)["id"].(string)
	}

	id := run("email", "create", "--name", "Built")["id"].(string)
	heading := blockID(run("email", "blocks", "add", id, "--type", "heading", "--text", "Hi"))
	cols := blockID(run("email", "blocks", "add", id, "--type", "columns"))
	run("email", "blocks", "add", id, "--type", "image", "--src", "https://cdn.example/bun.jpg", "--alt", "A bun", "--in", cols, "--column", "0")
	run("email", "blocks", "add", id, "--type", "text", "--data", `{"text": "Right side", "align": "right"}`, "--in", cols, "--column", "1")
	run("email", "blocks", "add", id, "--type", "button", "--text", "Order", "--href", "https://bakery.example/order", "--after", heading)

	got := run("email", "get", id)
	raw, _ := json.Marshal(got["document"].(map[string]any)["blocks"])
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	want := "heading:Hi button:Order columns[image:https://cdn.example/bun.jpg][text:Right side] footer"
	if o := blockOutline(blocks); o != want {
		t.Errorf("document:\n got %s\nwant %s", o, want)
	}
	if got["version"].(float64) != 6 || blocks[1]["href"] != "https://bakery.example/order" || blocks[0]["level"].(float64) != 1 {
		t.Errorf("version %v, blocks %v", got["version"], blocks)
	}

	// Update, move and remove, and the footer stays put.
	run("email", "blocks", "update", id, heading, "--text", "Hello", "--level", "2")
	moved := run("email", "blocks", "move", id, heading, "--in", cols, "--column", "1", "--index", "0")
	if b := moved["block"].(map[string]any); b["parent_id"] != cols || b["path"] != "blocks[1].columns[1].blocks[0]" {
		t.Errorf("moved = %v", b)
	}
	list := run("email", "blocks", "list", id)
	data := list["data"].([]any)
	footer := data[len(data)-1].(map[string]any)["block"].(map[string]any)["id"].(string)
	if code := h.run("email", "blocks", "remove", id, footer); code != ExitError || !strings.Contains(h.errOut.String(), "footer stays the last block") {
		t.Errorf("remove the footer: exit %d %s", code, h.errOut)
	}
	if code := h.run("email", "blocks", "move", id, footer, "--index", "0"); code != ExitError || !strings.Contains(h.errOut.String(), "footer stays the last block") {
		t.Errorf("move the footer: exit %d %s", code, h.errOut)
	}
	if code := h.run("email", "blocks", "remove", id, cols); code != ExitOK || !strings.Contains(h.out.String(), "version 9") {
		t.Errorf("remove: exit %d %s %s", code, h.out, h.errOut)
	}

	// People see where each block is.
	if code := h.run("email", "blocks", "list", id); code != ExitOK || !strings.Contains(h.out.String(), `button  "Order"`) {
		t.Errorf("list: exit %d\n%s", code, h.out)
	}
	if code := h.run("email", "blocks", "update", id, heading); code != ExitUsage {
		t.Errorf("update with nothing: exit %d", code)
	}
	if code := h.run("email", "blocks", "add", id, "--data", "[1]"); code != ExitUsage {
		t.Errorf("bad --data: exit %d", code)
	}
}

// blockOutline sums up blocks as type:text, a columns block's columns in
// brackets.
func blockOutline(bs []map[string]any) string {
	var parts []string
	for _, b := range bs {
		p := b["type"].(string)
		if s, ok := b["text"].(string); ok && s != "" && b["type"] != "footer" {
			p += ":" + s
		}
		if s, ok := b["src"].(string); ok {
			p += ":" + s
		}
		if cs, ok := b["columns"].([]any); ok {
			for _, c := range cs {
				var inner []map[string]any
				r, _ := json.Marshal(c.(map[string]any)["blocks"])
				_ = json.Unmarshal(r, &inner)
				p += "[" + blockOutline(inner) + "]"
			}
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}
