package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testFetchID = "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"

// testFetchedBundle is testBundlePreview as a fetch makes it.
var testFetchedBundle = strings.Replace(testBundlePreview, `"source":"account_export",`,
	`"source":"connected_account","segment_tags":[{"audience":"Newsletter","tag":"segment: Regulars","members":2}],"templates_not_fetched":4,`, 1)

func testFetch(status, step string, read int) string {
	bundle, bundleID := "null", "null"
	if status == "done" {
		bundle, bundleID = testFetchedBundle, `"`+testBundleID+`"`
	}
	return `{"id":"` + testFetchID + `","status":"` + status + `","step":"` + step + `","audience":"Newsletter","members_read":` + itoa(read) +
		`,"members_total":5,"audience_ids":["a1b2c3"],"segment_ids":["40213"],"campaigns":10,"error":null,"bundle_id":` + bundleID +
		`,"bundle":` + bundle + `,"created_at":"2026-11-22T12:00:00Z","finished_at":null}`
}

type fetchRequest struct {
	method, path string
	body         map[string]any
}

// fetchStub answers the fetch endpoints: the fetch runs for two checks,
// then is done.
func fetchStub(t *testing.T, final string) (*harness, *[]fetchRequest) {
	t.Helper()
	var seen []fetchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := fetchRequest{method: r.Method, path: r.URL.Path}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &req.body)
		}
		seen = append(seen, req)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/imports/mailchimp/audiences":
			_, _ = w.Write([]byte(`{"audiences":[{"id":"a1b2c3","name":"News\tletter","double_optin":true,
				"members":{"subscribed":3,"unsubscribed":1,"cleaned":1,"total":5}}]}`))
		case "/v1/imports/mailchimp/fetch":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(testFetch("queued", "", 0)))
		case "/v1/imports/mailchimp/fetch/" + testFetchID:
			gets := 0
			for _, s := range seen {
				if s.method == http.MethodGet {
					gets++
				}
			}
			switch gets {
			case 1:
				_, _ = w.Write([]byte(testFetch("running", "members", 2)))
			case 2:
				_, _ = w.Write([]byte(testFetch("running", "members", 4)))
			default:
				_, _ = w.Write([]byte(final))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No Mailchimp fetch with that id."}}`))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	h.app.Sleep = func(context.Context, time.Duration) error { return nil }
	return h, &seen
}

func TestImportMailchimpAudiences(t *testing.T) {
	h, _ := fetchStub(t, "")
	if code := h.run("import", "mailchimp", "audiences"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{"a1b2c3", "News letter", "yes", "fetch --audience <id>"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestImportMailchimpFetchWaitsAndShowsThePreview(t *testing.T) {
	h, seen := fetchStub(t, testFetch("done", "", 5))
	if code := h.run("import", "mailchimp", "fetch", "--audience", "a1b2c3", "--segment", "40213", "--campaigns", "0"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(*seen) != 4 {
		t.Fatalf("requests = %+v", *seen)
	}
	b := (*seen)[0].body
	if (*seen)[0].method != http.MethodPost || b["campaigns"] != float64(0) || len(b["audience_ids"].([]any)) != 1 ||
		b["segment_ids"].([]any)[0] != "40213" {
		t.Errorf("fetch request = %+v", (*seen)[0])
	}
	if e := h.errOut.String(); !strings.Contains(e, "reading members of Newsletter, 2 of 5") || !strings.Contains(e, "4 of 5") {
		t.Errorf("progress:\n%s", e)
	}
	out := h.out.String()
	for _, want := range []string{"Fetched 5 members", "Newsletter", `Tag "segment: Regulars" holds the 2 members`, "your 4 templates aren't included"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestImportMailchimpFetchNeedsAnAudience(t *testing.T) {
	h, seen := fetchStub(t, "")
	if code := h.run("import", "mailchimp", "fetch"); code != ExitUsage {
		t.Fatalf("exit %d", code)
	}
	if len(*seen) != 0 {
		t.Errorf("requests = %+v", *seen)
	}
}

func TestImportMailchimpFetchNoWait(t *testing.T) {
	h, seen := fetchStub(t, "")
	if code := h.run("import", "mailchimp", "fetch", "--audience", "a1b2c3", "--no-wait"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(*seen) != 1 || (*seen)[0].body["campaigns"] != nil {
		t.Errorf("requests = %+v", *seen)
	}
	if out := h.out.String(); !strings.Contains(out, "waiting to start") || !strings.Contains(out, "fetch-status "+testFetchID) {
		t.Errorf("output:\n%s", out)
	}
}

func TestImportMailchimpFetchFailureExits(t *testing.T) {
	failed := strings.Replace(testFetch("failed", "", 0), `"error":null`,
		`"error":{"code":"mailchimp_access_revoked","message":"Mailchimp no longer allows mailgoose to read this account."}`, 1)
	h, _ := fetchStub(t, failed)
	if code := h.run("import", "mailchimp", "fetch", "--audience", "a1b2c3", "--json"); code != ExitError {
		t.Fatalf("exit %d", code)
	}
	if e := h.errOut.String(); !strings.Contains(e, "no longer allows mailgoose") {
		t.Errorf("stderr:\n%s", e)
	}
	var got map[string]any
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || got["status"] != "failed" {
		t.Errorf("json = %s (%v)", h.out, err)
	}
}

func TestImportMailchimpFetchStatus(t *testing.T) {
	h, seen := fetchStub(t, "")
	if code := h.run("import", "mailchimp", "fetch-status", testFetchID); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if out := h.out.String(); !strings.Contains(out, "reading members of Newsletter, 2 of 5") {
		t.Errorf("output:\n%s", out)
	}
	if code := h.run("import", "mailchimp", "fetch-status", "nope"); code == ExitOK {
		t.Error("an unknown fetch succeeded")
	}
	if len(*seen) != 2 {
		t.Errorf("requests = %+v", *seen)
	}
}
