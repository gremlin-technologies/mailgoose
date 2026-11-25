package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmailPreview(t *testing.T) {
	h := liveAPI(t)
	h.app.Stdin = strings.NewReader(springDoc)
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)

	// To standard output by default, with the sample contact filled in.
	if code := h.run("email", "preview", id); code != ExitOK {
		t.Fatalf("stdout: exit %d %s", code, h.errOut)
	}
	page := h.out.String()
	if !strings.HasPrefix(page, "<!DOCTYPE html>") || !strings.Contains(page, "Your postal address goes here.") {
		t.Errorf("stdout preview:\n%.300s", page)
	}

	// --out writes a file and says where.
	file := filepath.Join(t.TempDir(), "spring.html")
	if code := h.run("email", "preview", id, "--width", "mobile", "--out", file); code != ExitOK {
		t.Fatalf("--out: exit %d %s", code, h.errOut)
	}
	b, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(b), "375") || !strings.Contains(string(b), "srcdoc=") {
		t.Errorf("mobile file: %v\n%.300s", err, b)
	}
	if want := "Wrote the mobile preview: " + file + "\n"; h.out.String() != want {
		t.Errorf("stdout %q, want %q", h.out, want)
	}

	// --open at a terminal opens the file; elsewhere it only says why not.
	var opened []string
	h.app.OpenFile = func(_ context.Context, p string) error { opened = append(opened, p); return nil }
	h.app.IsTerminal = func() bool { return true }
	if code := h.run("email", "preview", id, "--open", "--json"); code != ExitOK {
		t.Fatalf("--open: exit %d %s", code, h.errOut)
	}
	out := h.jsonOut(t)
	if len(opened) != 1 || out["file"] != opened[0] || out["opened"] != true || out["width"] != "desktop" {
		t.Errorf("--open: opened %v, output %v", opened, out)
	}
	_ = os.Remove(opened[0])

	h.app.IsTerminal = func() bool { return false }
	if code := h.run("email", "preview", id, "--open", "--out", file); code != ExitOK {
		t.Fatalf("--open off a terminal: exit %d %s", code, h.errOut)
	}
	if len(opened) != 1 || !strings.Contains(h.errOut.String(), "Not opening a browser") || !strings.HasPrefix(h.out.String(), "Wrote") {
		t.Errorf("off a terminal: opened %v, stderr %q", opened, h.errOut)
	}

	// A browser that won't start is a note, not a failure.
	h.app.IsTerminal = func() bool { return true }
	h.app.OpenFile = func(context.Context, string) error { return errors.New("no browser") }
	if code := h.run("email", "preview", id, "--open", "--out", file); code != ExitOK ||
		!strings.Contains(h.errOut.String(), "Couldn't open a browser: no browser") {
		t.Errorf("no browser: exit %d %s", code, h.errOut)
	}

	if code := h.run("email", "preview", id, "--width", "tablet"); code != ExitUsage {
		t.Errorf("bad width: exit %d", code)
	}
}

func TestEmailPreviewProblems(t *testing.T) {
	h := liveAPI(t)
	if code := h.run("email", "create", "--name", "Blank", "--kind", "html", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)
	if code := h.run("email", "preview", id); code != ExitError || !strings.Contains(h.errOut.String(), "nothing to preview") {
		t.Errorf("empty email: exit %d %s", code, h.errOut)
	}
}

// TestFieldMergeTags: the CLI's preview and check know the account's
// contact fields (MG-133), and a field an active campaign's email uses
// isn't deleted.
func TestFieldMergeTags(t *testing.T) {
	h := liveAPI(t)
	for _, args := range [][]string{
		{"fields", "create", "Company"},
		{"fields", "create", "Plan"},
		{"contacts", "add", "pat@example.com", "--first-name", "Pat", "--field", "company=Acme", "--field", "plan=Pro"},
	} {
		if code := h.run(args...); code != ExitOK {
			t.Fatalf("%v: exit %d %s", args, code, h.errOut)
		}
	}
	h.app.Stdin = strings.NewReader(`{"settings":{"subject":"News"},"blocks":[
		{"type":"heading","text":"For {{company|your team}}"},
		{"type":"text","text":"Your plan: {{plan}}."}]}`)
	if code := h.run("email", "create", "--name", "Fields", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)

	if code := h.run("email", "preview", id); code != ExitOK || !strings.Contains(h.out.String(), ">For your team</h1>") {
		t.Errorf("sample preview: exit %d %.200s", code, h.errOut)
	}
	if code := h.run("email", "preview", id, "--contact", "pat@example.com"); code != ExitOK ||
		!strings.Contains(h.out.String(), ">For Acme</h1>") || !strings.Contains(h.out.String(), "Your plan: Pro.") {
		t.Errorf("--contact: exit %d %s", code, h.errOut)
	}
	if code := h.run("email", "preview", id, "--contact", "nobody@example.com"); code != ExitNotFound {
		t.Errorf("unknown contact: exit %d", code)
	}

	if code := h.run("email", "check", id, "--json"); code != ExitError && code != ExitOK {
		t.Fatalf("check: exit %d %s", code, h.errOut)
	}
	if out := h.out.String(); !strings.Contains(out, `"merge_tag_no_fallback"`) || strings.Contains(out, `"merge_tag"`) {
		t.Errorf("check: %s", out)
	}

	if code := h.run("campaign", "create", "--name", "Fields", "--email", id, "--all", "--json"); code != ExitOK {
		t.Fatalf("campaign: exit %d %s", code, h.errOut)
	}
	if code := h.run("fields", "delete", "plan", "--yes", "--json"); code == ExitOK || !strings.Contains(h.errOut.String(), "field_in_use") ||
		!strings.Contains(h.errOut.String(), `The email \"Fields\"`) {
		t.Errorf("delete a field in use: exit %d %s", code, h.errOut)
	}
}
