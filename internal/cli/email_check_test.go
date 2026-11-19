package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmailCheckAgainstAPI(t *testing.T) {
	h := liveAPI(t)
	h.app.Stdin = strings.NewReader(springDoc)
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	id := h.jsonOut(t)["id"].(string)

	// A new account has no postal address or verified domain: exit 1.
	if code := h.run("email", "check", id); code != ExitError {
		t.Fatalf("exit %d %s", code, h.errOut)
	}
	for _, want := range []string{"Errors (2):", "settings.from_email: news@bakery.example isn't on a verified sending domain", "[unverified_from]", "[missing_postal_address]"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, h.out)
		}
	}
	if !strings.Contains(h.errOut.String(), "The email has 2 errors, so it can't be sent yet.") {
		t.Errorf("stderr %q", h.errOut)
	}

	if code := h.run("email", "check", id, "--json"); code != ExitError {
		t.Fatalf("--json: exit %d", code)
	}
	out := h.jsonOut(t)
	if out["ok"] != false || len(out["errors"].([]any)) != 2 || !strings.Contains(h.errOut.String(), `"code": "email_has_errors"`) {
		t.Errorf("--json: %v / %s", out, h.errOut)
	}
}

// checkStub answers checkEmail with body.
func checkStub(t *testing.T, body string) *harness {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/check") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h
}

func TestEmailCheckWarningsOnlyExitsZero(t *testing.T) {
	h := checkStub(t, `{"ok":true,"errors":[],"warnings":[
		{"code":"missing_alt","message":"The image has no alt text.","path":"blocks[2].alt"},
		{"code":"unsupported_css","message":"Position: Gmail drops it.","line":4},
		{"code":"link_unreachable","message":"The link https://x.example may be broken: it timed out.","url":"https://x.example"}]}`)
	if code := h.run("email", "check", "3f1c9a2e-6b7d-4c1e-9f0a-1b2c3d4e5f60"); code != ExitOK {
		t.Fatalf("exit %d %s", code, h.errOut)
	}
	want := "Warnings (3):\n" +
		"  blocks[2].alt: The image has no alt text. [missing_alt]\n" +
		"  line 4: Position: Gmail drops it. [unsupported_css]\n" +
		"  The link https://x.example may be broken: it timed out. [link_unreachable]\n"
	if h.out.String() != want {
		t.Errorf("stdout\n%s\nwant\n%s", h.out, want)
	}
}

func TestEmailCheckClean(t *testing.T) {
	h := checkStub(t, `{"ok":true,"errors":[],"warnings":[]}`)
	if code := h.run("email", "check", "3f1c9a2e-6b7d-4c1e-9f0a-1b2c3d4e5f60"); code != ExitOK || h.out.String() != "No problems found.\n" {
		t.Errorf("exit %d %q %s", code, h.out, h.errOut)
	}
}
