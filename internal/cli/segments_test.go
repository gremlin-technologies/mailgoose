package cli

import (
	"strings"
	"testing"
)

// MG-114: segments round trip through the real API, by name.
func TestSegmentCommands(t *testing.T) {
	h := liveAPI(t)
	must := func(args ...string) string {
		t.Helper()
		if code := h.run(args...); code != ExitOK {
			t.Fatalf("%v: exit %d %s", args, code, h.errOut)
		}
		return h.out.String()
	}
	must("tags", "create", "vip")
	must("contacts", "add", "ann@example.com", "--status", "subscribed")
	must("contacts", "add", "bo@example.com", "--status", "subscribed")
	must("contacts", "add", "cy@example.com", "--status", "unsubscribed")
	must("tags", "add", "vip", "ann@example.com", "cy@example.com")

	if out := must("segment", "create", "VIPs", "--tag", "vip", "--status", "subscribed", "--status", "unconfirmed"); !strings.Contains(out, `Created segment "VIPs"`) {
		t.Errorf("create: %q", out)
	}
	if out := must("segment", "contacts", "vips"); !strings.Contains(out, "1 contact in the segment.") || !strings.Contains(out, "ann@example.com") {
		t.Errorf("contacts: %q", out)
	}
	must("segment", "contacts", "VIPs", "--json")
	if v := h.jsonOut(t); v["count"] != float64(1) || len(v["data"].([]any)) != 1 {
		t.Errorf("contacts --json: %v", v)
	}
	if out := must("segment", "get", "VIPs"); !strings.Contains(out, "status is subscribed or unconfirmed") || !strings.Contains(out, "has tag ") {
		t.Errorf("get: %q", out)
	}

	// Condition flags replace the conditions; --name alone keeps them.
	must("segment", "update", "VIPs", "--status", "subscribed")
	if out := must("segment", "contacts", "VIPs"); !strings.Contains(out, "2 contacts in the segment.") {
		t.Errorf("after update: %q", out)
	}
	must("segment", "update", "VIPs", "--name", "Subscribed")
	must("segment", "get", "Subscribed", "--json")
	if v := h.jsonOut(t); v["name"] != "Subscribed" || v["conditions"].(map[string]any)["statuses"] == nil {
		t.Errorf("after rename: %v", v)
	}
	if code := h.run("segment", "update", "Subscribed"); code != ExitUsage {
		t.Errorf("update with nothing to change: exit %d", code)
	}

	// A deleted tag makes a segment invalid, and its contacts are refused.
	must("segment", "create", "Tagged", "--tag", "vip")
	must("tags", "delete", "vip", "--yes")
	if out := must("segment", "get", "Tagged"); !strings.Contains(out, "Invalid until its conditions are updated") || !strings.Contains(out, "the tag was deleted") {
		t.Errorf("get invalid: %q", out)
	}
	if code := h.run("segment", "contacts", "Tagged"); code == ExitOK || !strings.Contains(h.errOut.String(), "can't use now") {
		t.Errorf("contacts of an invalid segment: exit %d %s", code, h.errOut)
	}
	if out := must("segment", "list"); !strings.Contains(out, "Subscribed") || !strings.Contains(out, "no") {
		t.Errorf("list: %q", out)
	}

	if code := h.run("segment", "create", "Empty"); code == ExitOK || !strings.Contains(h.errOut.String(), "at least one condition") {
		t.Errorf("no conditions: exit %d %s", code, h.errOut)
	}
	if code := h.run("segment", "delete", "Tagged"); code != ExitUsage {
		t.Errorf("delete without --yes: exit %d", code)
	}
	must("segment", "delete", "Tagged", "--yes")
	if code := h.run("segment", "get", "Tagged"); code != ExitNotFound {
		t.Errorf("get after delete: exit %d", code)
	}
}

// MG-115: a campaign goes to a segment, named on the command line.
func TestCampaignToSegmentCommands(t *testing.T) {
	h := liveAPI(t)
	h.app.Stdin = strings.NewReader(springDoc)
	if code := h.run("email", "create", "--name", "Spring", "--file", "-", "--json"); code != ExitOK {
		t.Fatalf("email: exit %d %s", code, h.errOut)
	}
	emailID := h.jsonOut(t)["id"].(string)
	must := func(args ...string) string {
		t.Helper()
		if code := h.run(args...); code != ExitOK {
			t.Fatalf("%v: exit %d %s", args, code, h.errOut)
		}
		return h.out.String()
	}
	must("tags", "create", "vip")
	must("contacts", "add", "ann@example.com", "--status", "subscribed")
	must("contacts", "add", "bo@example.com", "--status", "subscribed")
	must("tags", "add", "vip", "ann@example.com")
	must("segment", "create", "VIPs", "--tag", "vip")

	if out := must("campaign", "count", "--segment", "VIPs"); out != "1 subscribed contacts.\n" {
		t.Errorf("count: %q", out)
	}
	must("campaign", "create", "--name", "Win back", "--email", emailID, "--segment", "vips", "--json")
	c := h.jsonOut(t)
	id := c["id"].(string)
	seg, _ := c["recipients"].(map[string]any)["segment"].(map[string]any)
	if seg["name"] != "VIPs" || c["recipient_count"] != 1.0 {
		t.Errorf("created %v", c)
	}
	if out := must("campaign", "get", id); !strings.Contains(out, `subscribed contacts in segment "VIPs"`) {
		t.Errorf("get: %q", out)
	}
	if code := h.run("campaign", "update", id, "--all", "--segment", "VIPs"); code != ExitUsage {
		t.Errorf("two kinds of recipients: exit %d", code)
	}
	if code := h.run("segment", "delete", "VIPs", "--yes"); code == ExitOK || !strings.Contains(h.errOut.String(), "Win back") {
		t.Errorf("delete a segment a draft uses: exit %d %s", code, h.errOut)
	}
	must("campaign", "update", id, "--all")
	must("segment", "delete", "VIPs", "--yes")
}
