package mailchimp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	fakeToken = "tok-0123456789abcdef"
	fakeCode  = "code-good"
)

// fakeMailchimp is a stand-in for login.mailchimp.com and the Marketing API.
// Nothing in the tests reaches Mailchimp. exchanges counts token requests.
type fakeMailchimp struct {
	*httptest.Server
	exchanges atomic.Int32
	dc        string
}

func newFakeMailchimp(t *testing.T) *fakeMailchimp {
	t.Helper()
	f := &fakeMailchimp{dc: "us21"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/oauth2/token":
			f.exchanges.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("token exchange with %s", r.Method)
			}
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("client_id") != "client-1" ||
				r.PostForm.Get("client_secret") != "secret-1" || r.PostForm.Get("redirect_uri") != "https://mailgoose.test/oauth/mailchimp/callback" {
				t.Errorf("token form = %v", r.PostForm)
			}
			if r.PostForm.Get("code") != fakeCode {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad code ` + r.PostForm.Get("code") + `"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"` + fakeToken + `","expires_in":0,"scope":null}`))
		case r.URL.Path == "/oauth2/metadata":
			if r.Header.Get("Authorization") != "OAuth "+fakeToken {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"dc":"` + f.dc + `","role":"owner","accountname":"Riverside Bakery","user_id":1,
				"login_url":"https://login.mailchimp.com","api_endpoint":"https://evil.example"}`))
		case strings.HasPrefix(r.URL.Path, "/3.0"):
			if r.Method != http.MethodGet {
				t.Errorf("the Marketing API got a %s", r.Method)
			}
			if r.Header.Get("Authorization") != "Bearer "+fakeToken {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"title":"API Key Invalid","status":401}`))
				return
			}
			_, _ = w.Write([]byte(`{"account_name":"Riverside Bakery"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMailchimp) oauth() *OAuth {
	return &OAuth{ClientID: "client-1", ClientSecret: "secret-1", RedirectURL: "https://mailgoose.test/oauth/mailchimp/callback", LoginURL: f.URL}
}

// connections is a Connections against a fake Mailchimp, for a new account.
func connections(t *testing.T) (*Connections, *fakeMailchimp, string) {
	t.Helper()
	_, db, acct := storeWithAccount(t)
	f := newFakeMailchimp(t)
	return &Connections{DB: db, OAuth: f.oauth(), APIURL: f.URL}, f, acct
}

// stateOf is the state in an authorize URL.
func stateOf(t *testing.T, u string) string {
	t.Helper()
	p, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	return p.Query().Get("state")
}

func TestConnectFinishesOnceForItsOwnAccount(t *testing.T) {
	c, f, acct := connections(t)
	ctx := t.Context()
	u, expires, err := c.Start(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := url.Parse(u)
	if p.Path != "/oauth2/authorize" || p.Query().Get("client_id") != "client-1" || p.Query().Get("response_type") != "code" ||
		p.Query().Get("redirect_uri") != "https://mailgoose.test/oauth/mailchimp/callback" || len(stateOf(t, u)) < 40 {
		t.Fatalf("authorize URL = %s", u)
	}
	if d := time.Until(expires); d < StateTTL-time.Minute || d > StateTTL {
		t.Errorf("link expires in %v", d)
	}
	state := stateOf(t, u)
	conn, err := c.Finish(ctx, state, acct, fakeCode, "")
	if err != nil {
		t.Fatal(err)
	}
	if conn.AccountName != "Riverside Bakery" || conn.DataCenter != "us21" || conn.ExpiresAt.Sub(conn.ConnectedAt) != ConnectionTTL {
		t.Errorf("connection = %+v", conn)
	}
	// The state works once.
	if _, err := c.Finish(ctx, state, acct, fakeCode, ""); !errors.Is(err, ErrStateNotFound) {
		t.Errorf("second finish: %v", err)
	}
	if got, err := c.Get(ctx, acct); err != nil || got == nil || got.AccountName != "Riverside Bakery" {
		t.Errorf("get = %+v, %v", got, err)
	}
	// The token is stored sealed, and opens into a read-only client.
	var sealed []byte
	if err := c.DB.QueryRow(ctx, `SELECT token FROM mailchimp_connections WHERE account_id = $1`, acct).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(fakeToken)) || bytes.Contains(sealed, []byte("tok-")) {
		t.Error("the token is stored in the clear")
	}
	client, err := c.Client(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	if name, err := client.Account(ctx); err != nil || name != "Riverside Bakery" {
		t.Errorf("account = %q, %v", name, err)
	}
	if f.exchanges.Load() != 1 {
		t.Errorf("%d token exchanges", f.exchanges.Load())
	}
}

func TestConnectNeedsTheAccountsOwnBrowser(t *testing.T) {
	c, f, acct := connections(t)
	ctx := t.Context()
	for _, signedIn := range []string{"", "00000000-0000-4000-8000-000000000000"} {
		u, _, err := c.Start(ctx, acct)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Finish(ctx, stateOf(t, u), signedIn, fakeCode, ""); !errors.Is(err, ErrWrongAccount) {
			t.Errorf("signed in as %q: %v", signedIn, err)
		}
		// The link is used up, and the code was never exchanged.
		if _, err := c.Finish(ctx, stateOf(t, u), acct, fakeCode, ""); !errors.Is(err, ErrStateNotFound) {
			t.Errorf("finish after a refusal: %v", err)
		}
	}
	if f.exchanges.Load() != 0 {
		t.Errorf("%d token exchanges", f.exchanges.Load())
	}
	if conn, _ := c.Get(ctx, acct); conn != nil {
		t.Errorf("connected: %+v", conn)
	}
}

func TestConnectLinksExpireAndCanBeDenied(t *testing.T) {
	c, _, acct := connections(t)
	ctx := t.Context()
	u, _, err := c.Start(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return time.Now().Add(StateTTL + time.Minute) }
	if _, err := c.Finish(ctx, stateOf(t, u), acct, fakeCode, ""); !errors.Is(err, ErrStateNotFound) {
		t.Errorf("expired link: %v", err)
	}
	c.Now = nil
	// A new link replaces the last one.
	first, _, _ := c.Start(ctx, acct)
	second, _, _ := c.Start(ctx, acct)
	if _, err := c.Finish(ctx, stateOf(t, first), acct, fakeCode, ""); !errors.Is(err, ErrStateNotFound) {
		t.Errorf("replaced link: %v", err)
	}
	if _, err := c.Finish(ctx, stateOf(t, second), acct, "", "access_denied"); !errors.Is(err, ErrDenied) {
		t.Errorf("denied: %v", err)
	}
	if _, err := c.Finish(ctx, "", acct, fakeCode, ""); !errors.Is(err, ErrStateNotFound) {
		t.Errorf("no state: %v", err)
	}
}

func TestConnectionsAreForgotten(t *testing.T) {
	c, _, acct := connections(t)
	ctx := t.Context()
	connect := func() {
		t.Helper()
		u, _, err := c.Start(ctx, acct)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Finish(ctx, stateOf(t, u), acct, fakeCode, ""); err != nil {
			t.Fatal(err)
		}
	}
	connect()
	if err := c.Forget(ctx, acct); err != nil {
		t.Fatal(err)
	}
	if conn, err := c.Get(ctx, acct); conn != nil || err != nil {
		t.Errorf("after forget: %+v, %v", conn, err)
	}
	if _, err := c.Client(ctx, acct); !errors.Is(err, ErrNotConnected) {
		t.Errorf("client after forget: %v", err)
	}

	// After a day it's gone, and the hourly prune deletes it.
	connect()
	later := func() time.Time { return time.Now().Add(ConnectionTTL + time.Minute) }
	c.Now = later
	if conn, err := c.Get(ctx, acct); conn != nil || err != nil {
		t.Errorf("expired: %+v, %v", conn, err)
	}
	if _, err := c.Client(ctx, acct); !errors.Is(err, ErrNotConnected) {
		t.Errorf("client when expired: %v", err)
	}
	s := &Store{DB: c.DB, Now: later}
	if n, err := s.Prune(ctx); err != nil || n != 1 {
		t.Errorf("prune = %d, %v", n, err)
	}
}

func TestSealedTokensOpenOnlyForTheirAccountAndSecret(t *testing.T) {
	c := &Connections{OAuth: &OAuth{ClientID: "c", ClientSecret: "secret-1"}}
	sealed, err := c.seal("acct-a", fakeToken)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.open("acct-a", sealed); err != nil || got != fakeToken {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := c.open("acct-b", sealed); err == nil {
		t.Error("opened for another account")
	}
	other := &Connections{OAuth: &OAuth{ClientID: "c", ClientSecret: "secret-2"}}
	if _, err := other.open("acct-a", sealed); err == nil {
		t.Error("opened with another secret")
	}
	again, _ := c.seal("acct-a", fakeToken)
	if bytes.Equal(sealed, again) {
		t.Error("two seals of one token are the same")
	}
}

func TestConnectNeedsTheApp(t *testing.T) {
	_, db, acct := storeWithAccount(t)
	for _, c := range []*Connections{{DB: db}, {DB: db, OAuth: &OAuth{ClientID: "c"}}} {
		if c.Configured() {
			t.Error("configured without an id and secret")
		}
		if _, _, err := c.Start(t.Context(), acct); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("start: %v", err)
		}
	}
}

func TestOAuthErrorsKeepCodesAndTokensOut(t *testing.T) {
	f := newFakeMailchimp(t)
	o := f.oauth()
	_, err := o.Exchange(t.Context(), "code-leaked-secret")
	var oe *ErrOAuth
	if !errors.As(err, &oe) || oe.Status != http.StatusBadRequest || oe.Reason != "invalid_grant" {
		t.Fatalf("exchange: %#v", err)
	}
	if strings.Contains(err.Error(), "leaked") {
		t.Errorf("the error holds the code: %v", err)
	}
	if _, err := o.Metadata(t.Context(), "tok-wrong"); err == nil || strings.Contains(err.Error(), "tok-wrong") {
		t.Errorf("metadata: %v", err)
	}
	// A data center that isn't one is refused; the API's address is never
	// taken from the response.
	f.dc = "evil.example/x"
	if _, err := o.Metadata(t.Context(), fakeToken); err == nil {
		t.Error("a bad data center was taken")
	}
	if _, err := NewClient(fakeToken, "evil.example", "", nil); err == nil {
		t.Error("a client for a bad data center was made")
	}
	if c, err := NewClient(fakeToken, "us21", "", nil); err != nil || c.baseURL != "https://us21.api.mailchimp.com" {
		t.Errorf("client = %+v, %v", c, err)
	}
}

func TestTheClientOnlyReads(t *testing.T) {
	f := newFakeMailchimp(t)
	c, err := NewClient(fakeToken, "us21", f.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, _ := http.NewRequestWithContext(ctx, method, f.URL+"/3.0/lists", strings.NewReader(`{}`))
		if resp, err := c.http.Do(req); !errors.Is(err, ErrNotRead) {
			if resp != nil {
				_ = resp.Body.Close()
			}
			t.Errorf("%s went out: %v", method, err)
		}
	}
	var out map[string]any
	if err := c.Get(ctx, "/3.0/", nil, &out); err != nil || out["account_name"] != "Riverside Bakery" {
		t.Errorf("get = %v, %v", out, err)
	}
	if err := c.Get(ctx, "/oauth2/token", nil, nil); err == nil {
		t.Error("read outside the Marketing API")
	}
	bad, _ := NewClient("tok-wrong", "us21", f.URL, nil)
	err = bad.Get(ctx, "/3.0/", nil, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized || strings.Contains(err.Error(), "tok-wrong") {
		t.Errorf("bad token: %v", err)
	}
}
