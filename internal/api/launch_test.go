package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

var launchCodes sync.Map // *testing.T -> confirmation code

// launchable is readyCampaign tested, with a subscribed contact and credits
// to send to it.
func launchable(t *testing.T, h http.Handler) {
	t.Helper()
	readyCampaign(t, h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignTest(""))(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("test send: %d %s", rec.Code, rec.Body)
	}
	addedContact(t, h)
	id, _ := addedCampaigns.Load(t)
	v, _ := testDBs.Load(t)
	if _, err := v.(*pgxpool.Pool).Exec(t.Context(), `
		INSERT INTO credit_transactions (account_id, type, amount, expires_at)
		SELECT account_id, 'purchase', 100, now() + interval '1 year' FROM campaigns WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// summarized is launchable with a launch summary asked for, and its code
// kept for campaignLaunch's confirmed request.
func summarized(t *testing.T, h http.Handler) {
	t.Helper()
	launchable(t, h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignLaunch(""))(t))
	if rec.Code != http.StatusConflict {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body)
	}
	launchCodes.Store(t, launchCode(t, rec))
	t.Cleanup(func() { launchCodes.Delete(t) })
}

func launchCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Code string `json:"code"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error.Code != "confirmation_required" || e.Error.Details.Code == "" {
		t.Fatalf("summary: %v %s", err, rec.Body)
	}
	return e.Error.Details.Code
}

// campaignLaunch is a launch request for the test's campaign.
func campaignLaunch(body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		r := campaignPath(http.MethodPost, body)(t)
		r.URL.Path += "/launch"
		return r
	}
}

// confirmedLaunch is a launch request with the code summarized kept.
func confirmedLaunch(t *testing.T) *http.Request {
	code, _ := launchCodes.Load(t)
	s, _ := code.(string)
	return campaignLaunch(`{"confirm":"` + s + `"}`)(t)
}

func TestLaunchCampaign(t *testing.T) {
	srv := authServer(t)
	h := srv.Handler()
	fullKey(func(*testing.T) *Server { return srv })(t)
	launchable(t, h)

	// Without a code: the summary, and nothing launched.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignLaunch(""))(t))
	if rec.Code != http.StatusConflict {
		t.Fatalf("summary: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{`"recipient_count":1`, `"credits":1`, `"from":{"email":"hello@patsbakery.com"`, `"expires_at":`, "Show this summary to the person"} {
		if !strings.Contains(body, want) {
			t.Errorf("summary lacks %s: %s", want, body)
		}
	}
	code := launchCode(t, rec)

	// A wrong code is refused with its reason.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignLaunch(`{"confirm":"2222-2222"}`))(t))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"reason":"wrong"`) {
		t.Errorf("wrong code: %d %s", rec.Code, rec.Body)
	}

	// The code, typed in lower case without its dash, launches.
	confirmed := func() *httptest.ResponseRecorder {
		r := bearer(campaignLaunch(`{"confirm":"` + strings.ToLower(strings.ReplaceAll(code, "-", "")) + `"}`))(t)
		r.Header.Set(IdempotencyKeyHeader, "launch-spring")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	rec = confirmed()
	if rec.Code != http.StatusOK {
		t.Fatalf("launch: %d %s", rec.Code, rec.Body)
	}
	// A retry with the same Idempotency-Key gets the same answer.
	if again := confirmed(); again.Code != http.StatusOK || again.Body.String() != rec.Body.String() {
		t.Errorf("retry: %d %s", again.Code, again.Body)
	}
	var out struct {
		Campaign struct {
			Status string `json:"status"`
			Launch *struct {
				RecipientCount  int `json:"recipient_count"`
				CreditsReserved int `json:"credits_reserved"`
			} `json:"launch"`
		} `json:"campaign"`
		RecipientCount  int `json:"recipient_count"`
		CreditsReserved int `json:"credits_reserved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Campaign.Status != "sending" || out.RecipientCount != 1 || out.CreditsReserved != 1 ||
		out.Campaign.Launch == nil || out.Campaign.Launch.RecipientCount != 1 {
		t.Errorf("launched: %s", rec.Body)
	}

	// Launching again answers the same, and edits are refused.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignLaunch(""))(t))
	if rec.Code != http.StatusOK {
		t.Errorf("again: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignPath(http.MethodPatch, `{"name":"Later"}`))(t))
	if rec.Code != http.StatusConflict {
		t.Errorf("edit after launch: %d %s", rec.Code, rec.Body)
	}
}

func TestLaunchCampaignNotReady(t *testing.T) {
	srv := authServer(t)
	h := srv.Handler()
	fullKey(func(*testing.T) *Server { return srv })(t)
	readyCampaign(t, h)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignLaunch(""))(t))
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, `"code":"not_ready"`) || !strings.Contains(body, `"untested"`) {
		t.Errorf("not ready: %d %s", rec.Code, body)
	}
}

// A code refused because the audience grew says by how much, so an agent
// can tell the person before it asks for a new summary.
func TestLaunchAudienceGrewError(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/campaigns/x/launch", nil)
	if !(&Server{}).launchError(rec, r, &sending.CodeError{Reason: sending.CodeGrew, Summarized: 100, Recipients: 140}) {
		t.Fatal("not handled")
	}
	var e struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	d := e.Error.Details
	if rec.Code != http.StatusUnprocessableEntity || e.Error.Code != "invalid_confirmation" || d["reason"] != "audience_grew" ||
		d["summarized_count"] != 100.0 || d["recipient_count"] != 140.0 ||
		e.Error.Message != "Nothing was launched: the campaign now reaches 140 contacts, more than the 100 in the summary." {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}
