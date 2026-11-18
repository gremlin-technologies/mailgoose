package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testDomainID = "7c1d2e3f-4a5b-4c6d-8e7f-901a2b3c4d5e"

func testDomain(status string) string {
	return `{"id":"` + testDomainID + `","name":"example.com","status":"` + status + `","mail_from_domain":"bounce.example.com","ses_status":"pending",
"records":[{"purpose":"dkim","type":"CNAME","name":"a1._domainkey.example.com","value":"a1.dkim.amazonses.com","priority":null,"required":true,"status":"found","found":["a1.dkim.amazonses.com"],"detail":null},
{"purpose":"mail_from_mx","type":"MX","name":"bounce.example.com","value":"feedback-smtp.us-east-2.amazonses.com","priority":10,"required":true,"status":"missing","found":[],"detail":null},
{"purpose":"dmarc","type":"TXT","name":"_dmarc.example.com","value":"v=DMARC1; p=none;","priority":null,"required":false,"status":"missing","found":[],"detail":null}],
"created_at":"2026-11-18T12:00:00Z","last_checked_at":null,"check_until":"2026-11-21T12:00:00Z","verified_at":null,"failed_at":null}`
}

// domainsStub answers the domain operations, with example.com pending
// unless verified is set, and records each request.
func domainsStub(t *testing.T, status string) (*harness, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := seenRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &req.body)
		}
		seen = append(seen, req)
		w.Header().Set("Content-Type", "application/json")
		one := "/v1/domains/" + testDomainID
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/domains":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(testDomain("pending")))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/domains" && r.URL.Query().Get("name") == "nope.com":
			_, _ = w.Write([]byte(`{"data":[],"next_cursor":null}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/domains":
			_, _ = w.Write([]byte(`{"data":[` + testDomain(status) + `],"next_cursor":null}`))
		case r.Method == http.MethodGet && r.URL.Path == one:
			_, _ = w.Write([]byte(testDomain(status)))
		case r.Method == http.MethodPost && r.URL.Path == one+"/verify":
			_, _ = w.Write([]byte(strings.Replace(testDomain(status), `"created_at"`, `"records_changed":false,"created_at"`, 1)))
		case r.Method == http.MethodDelete && r.URL.Path == one:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"No domain with that id in this account.","hint":""}}`))
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h, &seen
}

func TestDomainAdd(t *testing.T) {
	h, seen := domainsStub(t, "pending")
	if code := h.run("domain", "add", "example.com"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{"Added example.com", "a1._domainkey.example.com", "10 feedback-smtp.us-east-2.amazonses.com", "missing (suggested)", "domain verify example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if got := (*seen)[0]; got.method != http.MethodPost || got.path != "/v1/domains" || got.body["name"] != "example.com" {
		t.Errorf("request = %+v", got)
	}
	if code := h.run("domain", "add"); code != ExitUsage {
		t.Errorf("no domain: exit %d", code)
	}
}

func TestDomainByName(t *testing.T) {
	h, seen := domainsStub(t, "verified")
	if code := h.run("domain", "get", "Example.com"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if len(*seen) != 2 || (*seen)[0].query != "name=example.com" || (*seen)[1].path != "/v1/domains/"+testDomainID {
		t.Errorf("requests = %+v", *seen)
	}
	*seen = nil
	if code := h.run("domain", "get", testDomainID); code != ExitOK || len(*seen) != 1 {
		t.Errorf("by id: exit %d, requests %+v", code, *seen)
	}
	if code := h.run("domain", "get", "nope.com"); code != ExitNotFound || !strings.Contains(h.errOut.String(), "no domain nope.com") {
		t.Errorf("unknown: exit %d, %s", code, h.errOut)
	}
}

func TestDomainVerify(t *testing.T) {
	h, _ := domainsStub(t, "pending")
	if code := h.run("domain", "verify", "example.com"); code != ExitError || !strings.Contains(h.out.String(), "missing") ||
		!strings.Contains(h.errOut.String(), "isn't verified yet") {
		t.Errorf("pending: exit %d\nout %s\nerr %s", code, h.out, h.errOut)
	}
	h, _ = domainsStub(t, "verified")
	if code := h.run("domain", "verify", "example.com", "--json"); code != ExitOK {
		t.Fatalf("verified: exit %d: %s", code, h.errOut)
	}
	var d domain
	if err := json.Unmarshal(h.out.Bytes(), &d); err != nil || d.Status != "verified" || d.RecordsChanged == nil {
		t.Errorf("--json = %s", h.out)
	}
}

func TestDomainListAndDelete(t *testing.T) {
	h, seen := domainsStub(t, "verified")
	if code := h.run("domain", "list"); code != ExitOK || !strings.Contains(h.out.String(), "example.com") {
		t.Errorf("list: exit %d %s", code, h.out)
	}
	if code := h.run("domain", "delete", "example.com"); code != ExitUsage || !strings.Contains(h.errOut.String(), "--yes") {
		t.Errorf("delete without --yes: exit %d %s", code, h.errOut)
	}
	*seen = nil
	if code := h.run("domain", "delete", "example.com", "--yes"); code != ExitOK {
		t.Fatalf("delete: exit %d %s", code, h.errOut)
	}
	if last := (*seen)[len(*seen)-1]; last.method != http.MethodDelete || last.path != "/v1/domains/"+testDomainID {
		t.Errorf("delete request = %+v", last)
	}
}
