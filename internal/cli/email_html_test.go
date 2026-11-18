package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmailImportHTML(t *testing.T) {
	h := liveAPI(t)
	if code := h.run("email", "create", "--name", "Spring", "--kind", "html", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)
	dir := t.TempDir()

	bad := filepath.Join(dir, "bad.html")
	_ = os.WriteFile(bad, []byte("<p>Hi</p>\n<form><input></form>\n<script>go()</script>\n"), 0o600)
	if code := h.run("email", "import-html", id, bad); code != ExitError {
		t.Fatalf("bad: exit %d", code)
	}
	for _, want := range []string{"The HTML has 3 problems, so nothing was imported:", "\n  line 2: <form>", "\n  line 3: <script>", "\n  line 3: there's no {{unsubscribe_url}}", "Hint: Fix each line"} {
		if !strings.Contains(h.errOut.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, h.errOut)
		}
	}
	if code := h.run("email", "import-html", id, bad, "--json"); code != ExitError || !strings.Contains(h.errOut.String(), `"code": "invalid_html"`) ||
		!strings.Contains(h.errOut.String(), `"line": 2`) {
		t.Errorf("bad --json: exit %d %s", code, h.errOut)
	}

	good := filepath.Join(dir, "good.html")
	_ = os.WriteFile(good, []byte("<style>\n.r { position: absolute }\n</style>\n<p>Hi {{first_name}}</p>\n<a href=\"{{unsubscribe_url}}\">Unsubscribe</a>\n"), 0o600)
	if code := h.run("email", "import-html", id, good); code != ExitOK {
		t.Fatalf("good: exit %d %s", code, h.errOut)
	}
	for _, want := range []string{`Imported 114 bytes of HTML into "Spring"`, "version 2.", "1 warning: some email clients", "  line 2: position: Gmail and Outlook drop it"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, h.out)
		}
	}

	// From standard input, with --json.
	h.app.Stdin = strings.NewReader(`<p>Bye</p><a href="{{unsubscribe_url}}">Unsubscribe</a>`)
	if code := h.run("email", "import-html", id, "-", "--json"); code != ExitOK {
		t.Fatalf("stdin: exit %d %s", code, h.errOut)
	}
	out := h.jsonOut(t)
	doc := out["email"].(map[string]any)["document"].(map[string]any)
	if doc["html"] != `<p>Bye</p><a href="{{unsubscribe_url}}">Unsubscribe</a>` || len(out["warnings"].([]any)) != 0 {
		t.Errorf("stdin import: %v", out)
	}

	// A stale --version is refused.
	if code := h.run("email", "import-html", id, good, "--version", "1"); code != ExitError || !strings.Contains(h.errOut.String(), "saved since") {
		t.Errorf("stale: exit %d %s", code, h.errOut)
	}
	if code := h.run("email", "import-html", id, filepath.Join(dir, "missing.html")); code != ExitUsage {
		t.Errorf("missing file: exit %d", code)
	}
}
