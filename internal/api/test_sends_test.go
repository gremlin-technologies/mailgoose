package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// readyCampaign is addedCampaign on an account that can send: a From on a
// verified domain and a postal address. The domain is set in the database,
// since verifying one takes DNS.
func readyCampaign(t *testing.T, h http.Handler) {
	t.Helper()
	addedCampaign(t, h)
	id, _ := addedCampaigns.Load(t)
	v, _ := testDBs.Load(t)
	db := v.(*pgxpool.Pool)
	var acc string
	if err := db.QueryRow(t.Context(), `SELECT account_id FROM campaigns WHERE id = $1`, id).Scan(&acc); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE accounts SET from_name = 'Pat''s Bakery', from_email = 'hello@patsbakery.com', postal_address = '12 Main St' WHERE id = $1`,
		`INSERT INTO domains (account_id, name, mail_from, created_at, check_until, status) VALUES ($1, 'patsbakery.com', 'bounce', now(), now(), 'verified')`,
		// The email's own From (on bakery.example, which can't be verified)
		// gives way to the account's.
		`UPDATE emails SET document = document #- '{settings,from_email}' WHERE account_id = $1`,
	} {
		if _, err := db.Exec(t.Context(), q, acc); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// failingMail is readyCampaign with a mail provider that refuses
// everything.
func failingMail(t *testing.T, h http.Handler) {
	t.Helper()
	readyCampaign(t, h)
	v, _ := outboxes.Load(t)
	v.(*mail.Fake).Err = errors.New("the provider is down")
}

// testsUsedUp is readyCampaign with the day's 50 test sends used.
func testsUsedUp(t *testing.T, h http.Handler) {
	t.Helper()
	readyCampaign(t, h)
	id, _ := addedCampaigns.Load(t)
	v, _ := testDBs.Load(t)
	if _, err := v.(*pgxpool.Pool).Exec(t.Context(), `
		INSERT INTO test_sends (account_id, sent_at)
		SELECT account_id, now() - interval '1 hour' FROM campaigns, generate_series(1, 50) WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// campaignTest is a test-send request for the test's campaign.
func campaignTest(body string) func(*testing.T) *http.Request {
	return func(t *testing.T) *http.Request {
		r := campaignPath(http.MethodPost, body)(t)
		r.URL.Path += "/test"
		return r
	}
}

func TestCampaignTestSend(t *testing.T) {
	srv := authServer(t)
	h := srv.Handler()
	fullKey(func(*testing.T) *Server { return srv })(t)
	readyCampaign(t, h)
	v, _ := outboxes.Load(t)
	outbox := v.(*mail.Fake)
	outbox.Reset()

	// No body: to the owner.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignTest(""))(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Campaign struct {
			Version  int `json:"version"`
			LastTest *struct {
				Version      int `json:"version"`
				EmailVersion int `json:"email_version"`
			} `json:"last_test"`
		} `json:"campaign"`
		To         []string `json:"to"`
		SentToday  int      `json:"sent_today"`
		DailyLimit int      `json:"daily_limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.To, ",") != "key@example.com" || out.SentToday != 1 || out.DailyLimit != 50 ||
		out.Campaign.LastTest == nil || out.Campaign.LastTest.Version != out.Campaign.Version || out.Campaign.LastTest.EmailVersion < 1 {
		t.Errorf("result %s", rec.Body)
	}
	sent := outbox.Sent()
	if len(sent) != 1 || !strings.HasPrefix(sent[0].Message.Subject, "[Test] ") || sent[0].Message.Kind != mail.KindTest ||
		sent[0].Message.From.Email != "hello@patsbakery.com" {
		t.Fatalf("sent %+v", sent)
	}
	// The unsubscribe link and header point at the test page on this
	// server.
	if !strings.Contains(sent[0].Message.HTML, "/u/test") || sent[0].Message.Headers[0].Value != "<https://example.com/u/test>" {
		t.Errorf("unsubscribe: %+v", sent[0].Message.Headers)
	}

	// Get shows the last test too; a list does as well.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignPath(http.MethodGet, ""))(t))
	if !strings.Contains(rec.Body.String(), `"last_test":{"at":`) {
		t.Errorf("get: %s", rec.Body)
	}

	// To a colleague on the verified domain, and refusals.
	for body, want := range map[string]string{
		`{"to":["sam@patsbakery.com","Key@Example.com"]}`: "200",
		`{"to":["x@gmail.com"]}`:                          "test_recipient_not_allowed",
		`{"to":["nope"]}`:                                 "invalid_test",
		`{"to":["a@patsbakery.com","b@patsbakery.com","c@patsbakery.com","d@patsbakery.com","e@patsbakery.com","f@patsbakery.com"]}`: "invalid_test",
		`{"recipients":[]}`: "invalid_json",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearer(campaignTest(body))(t))
		got := rec.Body.String()
		if want == "200" {
			if rec.Code != http.StatusOK || !strings.Contains(got, `"to":["sam@patsbakery.com","key@example.com"]`) {
				t.Errorf("%s: %d %s", body, rec.Code, got)
			}
			continue
		}
		if !strings.Contains(got, `"code":"`+want+`"`) {
			t.Errorf("%s: %d %s", body, rec.Code, got)
		}
	}

	// An unverified From says how to fix it.
	db, _ := testDBs.Load(t)
	if _, err := db.(*pgxpool.Pool).Exec(t.Context(), `UPDATE accounts SET from_email = 'hi@elsewhere.com'`); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignTest(""))(t))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"unverified_from"`) ||
		!strings.Contains(rec.Body.String(), "mailgoose domain add elsewhere.com") {
		t.Errorf("unverified: %d %s", rec.Code, rec.Body)
	}

	// html markup saved before a check was tightened stops the send as
	// invalid_html, not as a server error.
	for _, q := range []string{
		`UPDATE accounts SET from_email = 'hello@patsbakery.com'`,
		`UPDATE emails SET kind = 'html', document = jsonb_build_object('settings', document->'settings', 'html', '<p>Unsubscribe: {{unsubscribe_url}}</p>')`,
	} {
		if _, err := db.(*pgxpool.Pool).Exec(t.Context(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignTest(""))(t))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"invalid_html"`) {
		t.Errorf("legacy html: %d %s", rec.Code, rec.Body)
	}
}
