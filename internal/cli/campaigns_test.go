package cli

import (
	"strings"
	"testing"
	"time"
)

func TestCampaignsAgainstAPI(t *testing.T) {
	h := liveAPI(t)
	h.app.Stdin = strings.NewReader(springDoc)
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("email: exit %d %s", code, h.errOut)
	}
	emailID := h.jsonOut(t)["id"].(string)
	for _, args := range [][]string{
		{"tags", "create", "VIP"},
		{"contacts", "add", "a@example.com", "--status", "subscribed"},
		{"contacts", "add", "b@example.com", "--status", "subscribed"},
		{"contacts", "add", "c@example.com", "--status", "unconfirmed"},
		{"tags", "add", "VIP", "a@example.com", "c@example.com"},
	} {
		if code := h.run(args...); code != ExitOK {
			t.Fatalf("%v: exit %d %s", args, code, h.errOut)
		}
	}

	if code := h.run("campaign", "count", "--all"); code != ExitOK || h.out.String() != "2 subscribed contacts.\n" {
		t.Errorf("count all: exit %d %q %s", code, h.out, h.errOut)
	}
	if code := h.run("campaign", "count", "--tag", "VIP", "--json"); code != ExitOK || h.jsonOut(t)["recipient_count"] != 1.0 {
		t.Errorf("count VIP: exit %d %s %s", code, h.out, h.errOut)
	}

	at := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Minute)
	if code := h.run("campaign", "create", "--name", "VIP preview", "--email", emailID, "--tag", "VIP", "--send-at", at.Format(time.RFC3339), "--json"); code != ExitOK {
		t.Fatalf("create: exit %d %s", code, h.errOut)
	}
	c := h.jsonOut(t)
	id := c["id"].(string)
	if c["status"] != "draft" || c["recipient_count"] != 1.0 || c["send_at"] != at.Format(time.RFC3339) {
		t.Errorf("created %v", c)
	}

	if code := h.run("campaign", "get", id); code != ExitOK {
		t.Fatalf("get: exit %d %s", code, h.errOut)
	}
	for _, want := range []string{"VIP preview", "Status:", "draft", "subscribed contacts tagged VIP", "Recipients now:  1", emailID} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("get lacks %q:\n%s", want, h.out)
		}
	}

	if code := h.run("campaign", "update", id, "--all", "--send-at", "now", "--version", "1"); code != ExitOK {
		t.Fatalf("update: exit %d %s", code, h.errOut)
	}
	for _, want := range []string{"Updated campaign \"VIP preview\"", "version 2", "every subscribed contact", "Recipients now:  2", "when launched"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("update lacks %q:\n%s", want, h.out)
		}
	}
	if code := h.run("campaign", "update", id, "--name", "x", "--version", "1"); code != ExitError || !strings.Contains(h.errOut.String(), "changed since") {
		t.Errorf("stale: exit %d %s", code, h.errOut)
	}

	for _, args := range [][]string{
		{"campaign", "create", "--email", emailID, "--all"},
		{"campaign", "create", "--name", "x", "--all"},
		{"campaign", "create", "--name", "x", "--email", emailID},
		{"campaign", "create", "--name", "x", "--email", emailID, "--all", "--tag", "VIP"},
		{"campaign", "create", "--name", "x", "--email", emailID, "--all", "--send-at", "soon"},
		{"campaign", "update", id},
		{"campaign", "count"},
		{"campaign", "get", "spring"},
		{"campaign", "delete", id},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d %s", args, code, h.errOut)
		}
	}
	if code := h.run("campaign", "create", "--name", "x", "--email", emailID, "--tag", "nope"); code != ExitNotFound {
		t.Errorf("unknown tag: exit %d %s", code, h.errOut)
	}
	if code := h.run("campaign", "create", "--name", "x", "--email", emailID, "--all", "--send-at", "2001-01-01 09:00"); code == ExitOK || !strings.Contains(h.errOut.String(), "future") {
		t.Errorf("past: exit %d %s", code, h.errOut)
	}

	if code := h.run("campaign", "list", "--status", "draft"); code != ExitOK || !strings.Contains(h.out.String(), "VIP preview") || !strings.Contains(h.out.String(), "when launched") {
		t.Errorf("list: exit %d %s %s", code, h.out, h.errOut)
	}
	if code := h.run("campaign", "list", "--status", "sent"); code != ExitOK || !strings.Contains(h.out.String(), "No campaigns") {
		t.Errorf("list sent: exit %d %s", code, h.out)
	}
	if code := h.run("campaign", "delete", id, "--yes"); code != ExitOK || !strings.Contains(h.out.String(), "Deleted campaign "+id) {
		t.Errorf("delete: exit %d %s", code, h.errOut)
	}
	if code := h.run("campaign", "get", id); code != ExitNotFound {
		t.Errorf("get deleted: exit %d", code)
	}
}

func TestParseSendAt(t *testing.T) {
	if at, err := parseSendAt("NOW"); at != nil || err != nil {
		t.Errorf("now: %v %v", at, err)
	}
	if at, err := parseSendAt("2026-12-01T09:00:00Z"); err != nil || !at.Equal(time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("rfc3339: %v %v", at, err)
	}
	if at, err := parseSendAt("2026-12-01 09:00"); err != nil || !at.Equal(time.Date(2026, 12, 1, 9, 0, 0, 0, time.Local)) {
		t.Errorf("local: %v %v", at, err)
	}
	if _, err := parseSendAt("tomorrow"); err == nil {
		t.Error("tomorrow parsed")
	}
}
