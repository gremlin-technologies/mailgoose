package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/policy"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

func TestTermsVia(t *testing.T) {
	cli := httptest.NewRequest(http.MethodPost, "/", nil)
	cli.Header.Set("User-Agent", "mailgoose-cli/1.2.3")
	browser := httptest.NewRequest(http.MethodPost, "/", nil)
	browser.Header.Set("User-Agent", "Mozilla/5.0")
	for _, tt := range []struct {
		name string
		r    *http.Request
		c    *Caller
		want string
	}{
		{"CLI sign-up", cli, nil, auth.TermsViaCLI},
		{"web sign-up", browser, nil, auth.TermsViaWeb},
		{"web session", browser, &Caller{Kind: KindSession}, auth.TermsViaWeb},
		{"CLI login", browser, &Caller{Kind: auth.KindCLIToken}, auth.TermsViaCLI},
		{"CLI with an API key", cli, &Caller{Kind: auth.KindAPIKey}, auth.TermsViaCLI},
		{"API key", browser, &Caller{Kind: auth.KindAPIKey}, auth.TermsViaAPI},
	} {
		if got := termsVia(tt.r, tt.c); got != tt.want {
			t.Errorf("%s: %s, want %s", tt.name, got, tt.want)
		}
	}
}

// TestSignUpRecordsHowTermsWereAccepted checks POST /v1/signup records the
// current version, from the CLI by its User-Agent (MG-68).
func TestSignUpRecordsHowTermsWereAccepted(t *testing.T) {
	s := authServer(t)
	h := s.Handler()
	r := jsonRequest(http.MethodPost, "/v1/signup", signupBody)(t)
	r.Header.Set("User-Agent", "mailgoose-cli/0.1.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("sign-up: %d %s", rec.Code, rec.Body)
	}
	var version, via string
	if err := s.DB.(*pgxpool.Pool).QueryRow(context.Background(), `
		SELECT version, via FROM terms_acceptances t JOIN users u ON u.account_id = t.account_id
		WHERE u.email = 'pat@example.com'`).Scan(&version, &via); err != nil {
		t.Fatal(err)
	}
	if version != policy.Current().ID || via != "cli" {
		t.Errorf("recorded %s via %s", version, via)
	}
}

// TestNewTermsGateLaunch checks an account on an older version of the terms
// sees it on GET /v1/account, can't launch, and can once it accepts.
func TestNewTermsGateLaunch(t *testing.T) {
	s := fullKey(authServer)(t)
	h := s.Handler()
	launchable(t, h)
	pool := s.DB.(*pgxpool.Pool)
	if _, err := pool.Exec(context.Background(), `UPDATE accounts SET terms_version = '2000-01-01'`); err != nil {
		t.Fatal(err)
	}
	cur := policy.Current().ID

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(request(http.MethodGet, "/v1/account"))(t))
	var acct struct {
		Terms struct {
			AcceptedVersion  *string `json:"accepted_version"`
			CurrentVersion   string  `json:"current_version"`
			CurrentEffective string  `json:"current_effective"`
			AcceptanceNeeded bool    `json:"acceptance_needed"`
		} `json:"terms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &acct); err != nil {
		t.Fatal(err)
	}
	if acct.Terms.AcceptedVersion == nil || *acct.Terms.AcceptedVersion != "2000-01-01" || acct.Terms.CurrentVersion != cur ||
		acct.Terms.CurrentEffective != policy.Current().Effective || !acct.Terms.AcceptanceNeeded {
		t.Fatalf("terms %+v", acct.Terms)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignLaunch(""))(t))
	var refused struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Problems []struct {
					Code           string `json:"code"`
					CurrentVersion string `json:"current_version"`
				} `json:"problems"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refused); err != nil {
		t.Fatal(err)
	}
	p := refused.Error.Details.Problems
	if rec.Code != http.StatusUnprocessableEntity || refused.Error.Code != "not_ready" || len(p) != 1 ||
		p[0].Code != sending.NewTermsNotAccepted || p[0].CurrentVersion != cur {
		t.Fatalf("launch: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(jsonRequest(http.MethodPost, "/v1/account/terms", acceptTermsBody))(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	var via string
	if err := pool.QueryRow(context.Background(), `SELECT via FROM terms_acceptances WHERE version = $1`, cur).Scan(&via); err != nil || via != "api" {
		t.Errorf("acceptance via %q (%v)", via, err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bearer(campaignLaunch(""))(t))
	if rec.Code != http.StatusConflict {
		t.Fatalf("summary after accepting: %d %s", rec.Code, rec.Body)
	}
}
