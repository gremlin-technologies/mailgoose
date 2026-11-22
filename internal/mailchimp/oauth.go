package mailchimp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Connecting a Mailchimp account with OAuth (MG-73), from Mailchimp's public
// OAuth 2 documentation: the person authorizes the app at
// login.mailchimp.com/oauth2/authorize, which sends them back with a code;
// the code is exchanged at /oauth2/token for a token that doesn't expire and
// has no scopes; /oauth2/metadata names the account's data center, which is
// part of the API's address.

// DefaultLoginURL is where Mailchimp's OAuth endpoints are.
const DefaultLoginURL = "https://login.mailchimp.com"

// oauthTimeout bounds each request to Mailchimp's OAuth endpoints.
const oauthTimeout = 15 * time.Second

// maxOAuthBody is the most of an OAuth response that's read.
const maxOAuthBody = 64 << 10

// dataCenter is the shape of a Mailchimp data center, such as us21. The API's
// address is built from it, never taken from a response, so a response can't
// point the client somewhere else.
var dataCenter = regexp.MustCompile(`^[a-z]{2,4}[0-9]{1,3}$`)

// OAuth is the app's Mailchimp OAuth client.
type OAuth struct {
	ClientID     string
	ClientSecret string
	// RedirectURL is the callback registered with the Mailchimp app.
	RedirectURL string
	// LoginURL is DefaultLoginURL, or a fake server in tests.
	LoginURL string
	// HTTP makes the requests; nil means a client with oauthTimeout.
	HTTP *http.Client
}

// Metadata is what Mailchimp says about the connected account.
type Metadata struct {
	DataCenter  string
	AccountName string
}

// ErrOAuth is a failed exchange or metadata read. Its message never holds
// the token or the code.
type ErrOAuth struct {
	Step   string
	Status int
	// Reason is Mailchimp's error code, if it gave one.
	Reason string
}

func (e *ErrOAuth) Error() string {
	msg := "mailchimp oauth " + e.Step + " failed"
	if e.Status != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.Status)
	}
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

func (o *OAuth) loginURL() string {
	if o.LoginURL != "" {
		return strings.TrimRight(o.LoginURL, "/")
	}
	return DefaultLoginURL
}

func (o *OAuth) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: oauthTimeout}
}

// AuthorizeURL is where the person goes to let mailgoose read their account.
func (o *OAuth) AuthorizeURL(state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", o.ClientID)
	q.Set("redirect_uri", o.RedirectURL)
	q.Set("state", state)
	return o.loginURL() + "/oauth2/authorize?" + q.Encode()
}

// Exchange trades the code the callback got for an access token.
func (o *OAuth) Exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", o.ClientID)
	form.Set("client_secret", o.ClientSecret)
	form.Set("redirect_uri", o.RedirectURL)
	form.Set("code", code)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.loginURL()+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", &ErrOAuth{Step: "token exchange"}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := o.do(req, "token exchange", &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", &ErrOAuth{Step: "token exchange", Reason: "no access token in the response"}
	}
	return out.AccessToken, nil
}

// Metadata reads the connected account's data center and name.
func (o *OAuth) Metadata(ctx context.Context, token string) (Metadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.loginURL()+"/oauth2/metadata", nil)
	if err != nil {
		return Metadata{}, &ErrOAuth{Step: "metadata"}
	}
	req.Header.Set("Authorization", "OAuth "+token)
	req.Header.Set("Accept", "application/json")
	var out struct {
		DC          string `json:"dc"`
		AccountName string `json:"accountname"`
	}
	if err := o.do(req, "metadata", &out); err != nil {
		return Metadata{}, err
	}
	if !dataCenter.MatchString(out.DC) {
		return Metadata{}, &ErrOAuth{Step: "metadata", Reason: "no data center in the response"}
	}
	return Metadata{DataCenter: out.DC, AccountName: truncate(out.AccountName, 200)}, nil
}

// do sends req and decodes a 2xx JSON answer into out. Errors keep only the
// status and Mailchimp's error code, never the body: a body can echo the
// code or hold the token.
func (o *OAuth) do(req *http.Request, step string, out any) error {
	resp, err := o.client().Do(req)
	if err != nil {
		reason := "request failed"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timed out"
		}
		return &ErrOAuth{Step: step, Reason: reason}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody))
	if err != nil {
		return &ErrOAuth{Step: step, Status: resp.StatusCode, Reason: "the response couldn't be read"}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return &ErrOAuth{Step: step, Status: resp.StatusCode, Reason: shortReason(e.Error)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &ErrOAuth{Step: step, Status: resp.StatusCode, Reason: "the response isn't JSON"}
	}
	return nil
}

// errorCode is the shape of an OAuth error code, such as invalid_grant.
var errorCode = regexp.MustCompile(`^[a-z_]{1,64}$`)

// shortReason keeps an error code that looks like one, so a response can't
// put anything else in the logs.
func shortReason(s string) string {
	if errorCode.MatchString(s) {
		return s
	}
	if s == "" {
		return ""
	}
	return "unexpected error"
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}
