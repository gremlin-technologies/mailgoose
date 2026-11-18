package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testTagID = "5d4c3b2a-1f0e-4d9c-8b7a-6f5e4d3c2b1a"

// tagsStub knows the tag "vip" and the contact pat@example.com.
func tagsStub(t *testing.T) (*harness, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	vip := `{"id":"` + testTagID + `","name":"vip","contact_count":2,"created_at":"2026-11-18T12:00:00Z"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &req.body)
		}
		seen = append(seen, req)
		w.Header().Set("Content-Type", "application/json")
		one := "/v1/tags/" + testTagID
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tags" && r.URL.Query().Get("name") == "nope":
			_, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tags":
			_, _ = w.Write([]byte(`{"data":[` + vip + `],"next_cursor":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tags":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(vip))
		case r.Method == http.MethodPatch && r.URL.Path == one:
			_, _ = w.Write([]byte(strings.Replace(vip, `"vip"`, `"gold"`, 1)))
		case r.Method == http.MethodDelete && r.URL.Path == one:
			_, _ = w.Write([]byte(`{"contacts_untagged":2,"campaigns_updated":0}`))
		case r.Method == http.MethodPost && (r.URL.Path == one+"/contacts" || r.URL.Path == one+"/contacts/remove"):
			_, _ = w.Write([]byte(`{"matched":2,"changed":1}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/contacts":
			_, _ = w.Write([]byte(`{"data":[` + testContact("pat@example.com", "subscribed") + `],"next_cursor":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No tag with that id in this account.","hint":""}}`))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h, &seen
}

func TestTagsCreateRenameDelete(t *testing.T) {
	h, seen := tagsStub(t)
	if code := h.run("tags", "create", "vip"); code != ExitOK || !strings.Contains(h.out.String(), testTagID) {
		t.Errorf("create: exit %d %s %s", code, h.out, h.errOut)
	}
	*seen = nil
	if code := h.run("tags", "rename", "vip", "gold"); code != ExitOK {
		t.Fatalf("rename: exit %d %s", code, h.errOut)
	}
	if len(*seen) != 2 || (*seen)[0].query != "name=vip" || (*seen)[1].method != http.MethodPatch || (*seen)[1].body["name"] != "gold" {
		t.Errorf("rename requests = %+v", *seen)
	}
	if code := h.run("tags", "delete", "vip"); code != ExitUsage {
		t.Errorf("delete without --yes: exit %d", code)
	}
	if code := h.run("tags", "delete", "vip", "--yes"); code != ExitOK || !strings.Contains(h.out.String(), "2 contacts") {
		t.Errorf("delete: exit %d %s", code, h.out)
	}
	if code := h.run("tags", "rename", "nope", "x"); code != ExitNotFound {
		t.Errorf("unknown tag: exit %d", code)
	}
	if code := h.run("tags", "list"); code != ExitOK || !strings.Contains(h.out.String(), "vip") {
		t.Errorf("list: exit %d %s", code, h.out)
	}
}

func TestTagsAddAndRemove(t *testing.T) {
	h, seen := tagsStub(t)
	if code := h.run("tags", "add", "vip", "pat@example.com", testContactID); code != ExitOK {
		t.Fatalf("add: exit %d %s", code, h.errOut)
	}
	last := (*seen)[len(*seen)-1]
	ids, _ := last.body["contact_ids"].([]any)
	if last.path != "/v1/tags/"+testTagID+"/contacts" || len(ids) != 2 || ids[0] != testContactID || ids[1] != testContactID {
		t.Errorf("add request = %+v", last)
	}
	if !strings.Contains(h.out.String(), "1 contacts changed of 2") {
		t.Errorf("output %q", h.out)
	}

	*seen = nil
	if code := h.run("tags", "remove", "vip", "--status", "unsubscribed", "--tag", "vip", "--json"); code != ExitOK {
		t.Fatalf("remove: exit %d %s", code, h.errOut)
	}
	last = (*seen)[len(*seen)-1]
	f, _ := last.body["filter"].(map[string]any)
	if last.path != "/v1/tags/"+testTagID+"/contacts/remove" || f["status"] != "unsubscribed" || f["tag_id"] != testTagID {
		t.Errorf("remove request = %+v", last)
	}

	*seen = nil
	if code := h.run("tags", "add", "vip", "--all"); code != ExitOK {
		t.Fatalf("--all: exit %d %s", code, h.errOut)
	}
	if f, ok := (*seen)[len(*seen)-1].body["filter"].(map[string]any); !ok || len(f) != 0 {
		t.Errorf("--all body = %+v", (*seen)[len(*seen)-1].body)
	}
	for _, args := range [][]string{{"tags", "add", "vip"}, {"tags", "add", "vip", "pat@example.com", "--all"}} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

func TestContactsListByTag(t *testing.T) {
	h, seen := tagsStub(t)
	if code := h.run("contacts", "list", "--tag", "vip"); code != ExitOK {
		t.Fatalf("exit %d %s", code, h.errOut)
	}
	if q := (*seen)[len(*seen)-1].query; !strings.Contains(q, "tag_id="+testTagID) {
		t.Errorf("query %q", q)
	}
}
