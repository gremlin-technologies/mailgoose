package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// fakeMarketingAPI stands in for Mailchimp's Marketing API with one
// audience, L1, of two members, one saved segment, 7, and two templates.
// The token says how it answers: tok-good reads, tok-revoked is refused as
// revoked, tok-refused gets a 400. Nothing in the tests reaches Mailchimp.
func fakeMarketingAPI(t *testing.T) *httptest.Server {
	t.Helper()
	type obj = map[string]any
	members := []obj{
		{"email_address": "ada@example.com", "status": "subscribed", "merge_fields": obj{"FNAME": "Ada"}, "tags": []obj{{"name": "VIP"}}},
		{"email_address": "bea@example.com", "status": "unsubscribed", "merge_fields": obj{}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer tok-good":
		case "Bearer tok-refused":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"title":"Invalid Resource","status":400}`))
			return
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"title":"API Key Invalid","status":401}`))
			return
		}
		var out any
		switch r.URL.Path {
		case "/3.0/lists":
			out = obj{"lists": []obj{{"id": "L1", "name": "Newsletter", "double_optin": true,
				"stats": obj{"member_count": 1, "unsubscribe_count": 1, "cleaned_count": 0, "total_contacts": 2}}}, "total_items": 1}
		case "/3.0/lists/L1":
			out = obj{"id": "L1", "name": "Newsletter"}
		case "/3.0/lists/L1/members":
			var ms []obj
			for _, m := range members {
				if s := r.URL.Query().Get("status"); s == "" || m["status"] == s {
					ms = append(ms, m)
				}
			}
			total := len(ms)
			if r.URL.Query().Get("offset") != "" && r.URL.Query().Get("offset") != "0" {
				ms = nil
			}
			out = obj{"members": ms, "total_items": total}
		case "/3.0/lists/L1/merge-fields":
			out = obj{"merge_fields": []obj{{"tag": "FNAME", "name": "First Name"}}, "total_items": 1}
		case "/3.0/lists/L1/interest-categories":
			out = obj{"categories": []obj{}, "total_items": 0}
		case "/3.0/lists/L1/segments/7":
			out = obj{"id": 7, "name": "Regulars"}
		case "/3.0/lists/L1/segments/7/members":
			out = obj{"members": []obj{{"email_address": "ada@example.com"}}, "total_items": 1}
		case "/3.0/campaigns":
			out = obj{"campaigns": []obj{{"id": "c1", "send_time": "2026-07-01T09:00:00+00:00",
				"settings": obj{"subject_line": "Summer menu", "title": "Summer"}}}, "total_items": 1}
		case "/3.0/campaigns/c1/content":
			out = obj{"html": "<p>Summer</p>", "plain_text": "Summer"}
		case "/3.0/templates":
			out = obj{"templates": []obj{}, "total_items": 2}
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"title":"Resource Not Found","status":404}`))
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fetchServers holds each test's fetchServer, for the before steps.
var fetchServers sync.Map // *testing.T -> *Server

// fetchServer is mailchimpOAuthServer reading from fakeMarketingAPI.
func fetchServer(t *testing.T) *Server {
	t.Helper()
	s := mailchimpOAuthServer(t)
	s.MailchimpConnections.APIURL = fakeMarketingAPI(t).URL
	fetchServers.Store(t, s)
	t.Cleanup(func() { fetchServers.Delete(t) })
	return s
}

// connectedWith connects the test key's account with token.
func connectedWith(token string) func(*testing.T, http.Handler) {
	return func(t *testing.T, _ http.Handler) {
		t.Helper()
		v, ok := fetchServers.Load(t)
		if !ok {
			t.Fatal("connecting needs fetchServer")
		}
		s := v.(*Server)
		var acct string
		if err := s.DB.(*pgxpool.Pool).QueryRow(t.Context(), `SELECT account_id FROM users WHERE email = 'key@example.com'`).Scan(&acct); err != nil {
			t.Fatal(err)
		}
		if _, err := s.MailchimpConnections.Put(t.Context(), acct, token, "us21", "Riverside Bakery"); err != nil {
			t.Fatal(err)
		}
	}
}

var connected = connectedWith("tok-good")

var addedFetches sync.Map // *testing.T -> fetch id

const fetchBody = `{"audience_ids":["L1"],"segment_ids":["7"],"campaigns":1}`

var createFetch = jsonRequest(http.MethodPost, "/v1/imports/mailchimp/fetch", fetchBody)

// queuedFetch connects and queues a fetch, keeping its id for fetchPath.
func queuedFetch(t *testing.T, h http.Handler) {
	t.Helper()
	connected(t, h)
	rec := do(t, h, bearer(createFetch)(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("fetch: %d %s", rec.Code, rec.Body)
	}
	var f struct {
		ID string `json:"id"`
	}
	decodeInto(t, rec, &f)
	addedFetches.Store(t, f.ID)
	t.Cleanup(func() { addedFetches.Delete(t) })
}

// fetchPath is GET of the test's fetch, or of one that doesn't exist.
func fetchPath(t *testing.T) *http.Request {
	id := noSuchID
	if v, ok := addedFetches.Load(t); ok {
		id = v.(string)
	}
	return httptest.NewRequest(http.MethodGet, "/v1/imports/mailchimp/fetch/"+id, nil)
}

func init() {
	audiences := request(http.MethodGet, "/v1/imports/mailchimp/audiences")
	limited := func(req func(*testing.T) *http.Request) contractCase {
		return contractCase{name: "rate limited", server: fullKey(rateLimitedAuthServer),
			before: send(bearer(request(http.MethodGet, "/v1/imports"))), req: bearer(req), status: http.StatusTooManyRequests}
	}
	contractCases["listMailchimpAudiences"] = []contractCase{
		{name: "listed", server: draftKey(fetchServer), before: connected, req: bearer(audiences), status: http.StatusOK},
		{name: "not connected", server: fullKey(fetchServer), req: bearer(audiences), status: http.StatusConflict},
		{name: "revoked", server: fullKey(fetchServer), before: connectedWith("tok-revoked"), req: bearer(audiences), status: http.StatusConflict},
		{name: "mailchimp refused", server: fullKey(fetchServer), before: connectedWith("tok-refused"), req: bearer(audiences), status: http.StatusBadGateway},
		{name: "no credentials", server: fetchServer, req: audiences, status: http.StatusUnauthorized},
		limited(audiences),
		{name: "not set up", server: fullKey(authServer), req: bearer(audiences), status: http.StatusServiceUnavailable},
		{name: "no database", server: serverWith(nil), req: bearer(audiences), status: http.StatusServiceUnavailable},
	}
	contractCases["createMailchimpFetch"] = []contractCase{
		{name: "queued", server: draftKey(fetchServer), before: connected, req: bearer(createFetch), status: http.StatusAccepted},
		{name: "malformed", server: fullKey(fetchServer), before: connected,
			req: bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/fetch", `{"audience_ids":"L1"}`)), status: http.StatusBadRequest},
		{name: "no audiences", server: fullKey(fetchServer), before: connected,
			req: bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/fetch", `{"audience_ids":[]}`)), status: http.StatusUnprocessableEntity},
		{name: "not connected", server: fullKey(fetchServer), req: bearer(createFetch), status: http.StatusConflict},
		{name: "already fetching", server: fullKey(fetchServer), before: queuedFetch, req: bearer(createFetch), status: http.StatusConflict},
		{name: "no credentials", server: fetchServer, req: createFetch, status: http.StatusUnauthorized},
		{name: "from another site", server: fetchServer, before: signedIn, req: fromAnotherSite(withSession(createFetch)), status: http.StatusForbidden},
		limited(createFetch),
		{name: "not set up", server: fullKey(authServer), req: bearer(createFetch), status: http.StatusServiceUnavailable},
		{name: "no database", server: serverWith(nil), req: bearer(createFetch), status: http.StatusServiceUnavailable},
	}
	contractCases["getMailchimpFetch"] = []contractCase{
		{name: "found", server: draftKey(fetchServer), before: queuedFetch, req: bearer(fetchPath), status: http.StatusOK},
		{name: "bad id", server: fullKey(authServer), req: bearer(request(http.MethodGet, "/v1/imports/mailchimp/fetch/nope")), status: http.StatusBadRequest},
		{name: "unknown", server: fullKey(authServer), req: bearer(fetchPath), status: http.StatusNotFound},
		{name: "no credentials", server: authServer, req: fetchPath, status: http.StatusUnauthorized},
		limited(fetchPath),
		{name: "no database", server: serverWith(nil), req: bearer(fetchPath), status: http.StatusServiceUnavailable},
	}
}

type fetchJSON struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	MembersRead int     `json:"members_read"`
	BundleID    *string `json:"bundle_id"`
	Error       *struct {
		Code string `json:"code"`
	} `json:"error"`
	Bundle *struct {
		ID      string `json:"id"`
		Preview *struct {
			Source      string `json:"source"`
			SegmentTags []struct {
				Audience string `json:"audience"`
				Tag      string `json:"tag"`
				Members  int    `json:"members"`
			} `json:"segment_tags"`
			TemplatesNotFetched int `json:"templates_not_fetched"`
			CampaignsAvailable  int `json:"campaigns_available"`
			Contacts            *struct {
				TagsToCreate []string `json:"tags_to_create"`
			} `json:"contacts"`
		} `json:"preview"`
	} `json:"bundle"`
}

func fetchJobOf(s *Server) *mailchimp.FetchJob {
	return &mailchimp.FetchJob{Store: s.Mailchimp, Connections: s.MailchimpConnections, Save: s.SaveFetched}
}

func TestMailchimpFetchThroughTheAPI(t *testing.T) {
	s := draftKey(fetchServer)(t)
	h := s.Handler()
	connected(t, h)

	rec := do(t, h, bearer(request(http.MethodGet, "/v1/imports/mailchimp/audiences"))(t))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"L1"`) {
		t.Fatalf("audiences = %d %s", rec.Code, rec.Body)
	}

	rec = do(t, h, bearer(createFetch)(t))
	var f fetchJSON
	decodeInto(t, rec, &f)
	if rec.Code != http.StatusAccepted || f.Status != "queued" {
		t.Fatalf("fetch = %d %s", rec.Code, rec.Body)
	}
	if ran, err := fetchJobOf(s).RunNext(t.Context()); !ran || err != nil {
		t.Fatalf("RunNext = %v, %v", ran, err)
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/mailchimp/fetch/"+f.ID))(t))
	decodeInto(t, rec, &f)
	if rec.Code != http.StatusOK || f.Status != "done" || f.MembersRead != 2 || f.Bundle == nil || f.Bundle.Preview == nil {
		t.Fatalf("fetch = %d %s", rec.Code, rec.Body)
	}
	p := f.Bundle.Preview
	if p.Source != "connected_account" || p.TemplatesNotFetched != 2 || p.CampaignsAvailable != 1 || len(p.SegmentTags) != 1 ||
		p.SegmentTags[0].Tag != "segment: Regulars" || p.SegmentTags[0].Members != 1 {
		t.Fatalf("preview = %s", rec.Body)
	}

	// Confirming a fetched bundle imports it and forgets the connection.
	rec = do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/bundles/"+f.Bundle.ID+"/confirm", confirmBody))(t))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/mailchimp/audiences"))(t))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "mailchimp_not_connected") {
		t.Fatalf("audiences after confirm = %d %s", rec.Code, rec.Body)
	}
}

func TestMailchimpFetchRevokedForgetsTheConnection(t *testing.T) {
	s := fullKey(fetchServer)(t)
	h := s.Handler()
	connectedWith("tok-revoked")(t, h)
	rec := do(t, h, bearer(request(http.MethodGet, "/v1/imports/mailchimp/audiences"))(t))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "mailchimp_access_revoked") {
		t.Fatalf("audiences = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "tok-revoked") {
		t.Error("the response holds the token")
	}
	rec = do(t, h, bearer(createFetch)(t))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "mailchimp_not_connected") {
		t.Fatalf("fetch after revoke = %d %s", rec.Code, rec.Body)
	}
}

func TestMailchimpFetchFailureShowsItsReason(t *testing.T) {
	s := fullKey(fetchServer)(t)
	h := s.Handler()
	connected(t, h)
	rec := do(t, h, bearer(jsonRequest(http.MethodPost, "/v1/imports/mailchimp/fetch", `{"audience_ids":["L404"]}`))(t))
	var f fetchJSON
	decodeInto(t, rec, &f)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("fetch = %d %s", rec.Code, rec.Body)
	}
	if _, err := fetchJobOf(s).RunNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, bearer(request(http.MethodGet, "/v1/imports/mailchimp/fetch/"+f.ID))(t))
	decodeInto(t, rec, &f)
	if f.Status != "failed" || f.Error == nil || f.Error.Code != mailchimp.ErrUnknownAudience || f.Bundle != nil {
		t.Fatalf("fetch = %s", rec.Body)
	}
}
