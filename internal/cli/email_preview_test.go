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
