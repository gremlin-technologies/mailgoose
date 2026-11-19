package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/api"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// liveAPI runs the real API on a test database and points a harness at it
// with a full-scope key, for commands whose round trip matters.
func liveAPI(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	srv := &api.Server{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:       db,
		Contacts: &contacts.Service{DB: db},
		Emails:   &emails.Service{DB: db},
		Forms:    &forms.Service{DB: db},
		Auth:     &auth.Service{DB: db, Mail: &mail.Fake{}, From: mail.Address{Email: "signin@mail.test.example"}},
	}
	var accountID, userID string
	if err := db.QueryRow(ctx, `INSERT INTO accounts (name, status) VALUES ('Test', 'active') RETURNING id`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO users (account_id, email) VALUES ($1, 'pat@example.com') RETURNING id`, accountID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	key, err := srv.Auth.CreateKey(ctx, accountID, userID, "cli test", auth.ScopeFull)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = hs.URL
	h.env[EnvAPIKey] = key.Secret
	return h
}

func (h *harness) jsonOut(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, h.out)
	}
	return v
}

const springDoc = `{
  "settings": {"subject": "Spring menu", "from_email": "news@bakery.example"},
  "blocks": [
    {"type": "heading", "text": "Hello {{first_name|there}}", "level": 1},
    {"type": "text", "text": "Our **spring** bakes are in."},
    {"type": "columns", "columns": [
      {"blocks": [{"type": "image", "src": "https://cdn.example/rye.jpg", "alt": "Rye"}]},
      {"blocks": [{"type": "button", "text": "Order", "href": "https://bakery.example/order"}]}
    ]},
    {"type": "social", "links": [{"network": "instagram", "url": "https://instagram.com/bakery"}]}
  ]
}`

func TestEmailCommandsRoundTrip(t *testing.T) {
	h := liveAPI(t)
	dir := t.TempDir()
	docFile := filepath.Join(dir, "spring.json")
	if err := os.WriteFile(docFile, []byte(springDoc), 0o600); err != nil {
		t.Fatal(err)
	}

	if code := h.run("email", "create", "--name", "Spring", "--file", docFile, "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	created := h.jsonOut(t)
	id := created["id"].(string)

	// get --json, then replace with that file: the document is unchanged.
	if code := h.run("email", "get", id, "--json"); code != ExitOK {
		t.Fatalf("get: exit %d %s", code, h.errOut)
	}
	first := append([]byte(nil), h.out.Bytes()...)
	whole := filepath.Join(dir, "whole.json")
	if err := os.WriteFile(whole, first, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("email", "replace", id, "--file", whole); code != ExitOK || !strings.Contains(h.out.String(), "version 2") {
		t.Fatalf("replace: exit %d %s %s", code, h.out, h.errOut)
	}
	if code := h.run("email", "get", id, "--json"); code != ExitOK {
		t.Fatal(h.errOut)
	}
	var before, after struct {
		Version  int             `json:"version"`
		Document json.RawMessage `json:"document"`
	}
	_ = json.Unmarshal(first, &before)
	_ = json.Unmarshal(h.out.Bytes(), &after)
	if !bytes.Equal(compact(t, before.Document), compact(t, after.Document)) || after.Version != 2 {
		t.Errorf("round trip changed the document:\n%s\n%s", before.Document, after.Document)
	}

	// The same file again carries version 1, which is stale now.
	if code := h.run("email", "replace", id, "--file", whole, "--json"); code != ExitError || !strings.Contains(h.errOut.String(), "version_conflict") {
		t.Errorf("stale replace: exit %d %s", code, h.errOut)
	}
	// A bare document (no version) replaces the email as it is now, from
	// standard input; its settings take their defaults, so from_email goes.
	h.app.Stdin = strings.NewReader(`{"blocks":[{"type":"heading","text":"Just this"}]}`)
	if code := h.run("email", "replace", id, "--file", "-"); code != ExitOK {
		t.Errorf("replace from stdin: exit %d %s", code, h.errOut)
	}
	// A whole-email file for another email is refused before any request.
	if code := h.run("email", "replace", "00000000-0000-4000-8000-000000000000", "--file", whole); code != ExitUsage {
		t.Errorf("another email's file: exit %d", code)
	}
	// Unknown fields are refused with where they are.
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"blocks":[{"type":"spacer","colour":"#000000"}]}`), 0o600)
	if code := h.run("email", "replace", id, "--file", bad); code != ExitError || !strings.Contains(h.errOut.String(), "blocks[0].colour") {
		t.Errorf("unknown field: exit %d %s", code, h.errOut)
	}

	if code := h.run("email", "update", id, "--subject", "Spring is here", "--reply-to", "hi@bakery.example", "--json"); code != ExitOK {
		t.Fatalf("update: exit %d %s", code, h.errOut)
	}
	settings := h.jsonOut(t)["document"].(map[string]any)["settings"].(map[string]any)
	if settings["subject"] != "Spring is here" || settings["reply_to"] != "hi@bakery.example" || settings["from_email"] != nil {
		t.Errorf("settings = %v", settings)
	}
	if code := h.run("email", "update", id, "--reply-to", "", "--json"); code != ExitOK || h.jsonOut(t)["document"].(map[string]any)["settings"].(map[string]any)["reply_to"] != nil {
		t.Errorf("clearing reply-to: exit %d %s", code, h.out)
	}
	if code := h.run("email", "update", id, "--name", "x", "--version", "1"); code != ExitError {
		t.Errorf("stale update: exit %d", code)
	}
	if code := h.run("email", "update", id); code != ExitUsage {
		t.Errorf("update with nothing: exit %d", code)
	}

	if code := h.run("email", "get", id); code != ExitOK || !strings.Contains(h.out.String(), "Spring is here") ||
		!strings.Contains(h.out.String(), "heading") || !strings.Contains(h.out.String(), "footer") {
		t.Errorf("get: exit %d %s", code, h.out)
	}

	if code := h.run("email", "duplicate", id, "--name", "Summer", "--json"); code != ExitOK || h.jsonOut(t)["name"] != "Summer" {
		t.Errorf("duplicate: exit %d %s %s", code, h.out, h.errOut)
	}
	if code := h.run("email", "create", "--name", "Plain", "--kind", "html"); code != ExitOK {
		t.Errorf("create html: exit %d %s", code, h.errOut)
	}
	if code := h.run("email", "list"); code != ExitOK || strings.Count(h.out.String(), "\n") != 4 {
		t.Errorf("list: exit %d\n%s", code, h.out)
	}
	if code := h.run("email", "delete", id); code != ExitUsage {
		t.Errorf("delete without --yes: exit %d", code)
	}
	if code := h.run("email", "delete", id, "--yes"); code != ExitOK {
		t.Errorf("delete: exit %d %s", code, h.errOut)
	}
	if code := h.run("email", "get", id); code != ExitNotFound {
		t.Errorf("get after delete: exit %d", code)
	}
	if code := h.run("email", "get", "spring"); code != ExitUsage {
		t.Errorf("get by name: exit %d", code)
	}
}

func compact(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
