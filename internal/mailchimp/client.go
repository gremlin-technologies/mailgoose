package mailchimp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client reads a connected Mailchimp account through Mailchimp's Marketing
// API (MG-73; MG-74 adds what it reads). A token carries full access, so the
// client can only make GET requests: its transport refuses any other method
// before it leaves the process.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
	slots   chan struct{}
	// Backoff is how long to wait before retry attempt n (1, 2, ...) when
	// Mailchimp's answer doesn't say; tests make it short.
	Backoff func(attempt int) time.Duration
}

// MaxConnections is the most requests in flight to one Mailchimp account
// at a time: Mailchimp allows 10 simultaneous connections per user and
// refuses more with 429.
const MaxConnections = 10

// maxAttempts is how many times one request is tried when Mailchimp
// answers 429 or a 5xx, or the request fails on the way.
const maxAttempts = 5

// maxRetryWait is the longest wait before a retry, whatever Retry-After
// says.
const maxRetryWait = time.Minute

// accountSlots holds MaxConnections slots for each token, shared by every
// client made for it in this process, so a fetch and a listing of the same
// account together stay under Mailchimp's limit.
var accountSlots sync.Map

func slotsFor(token string) chan struct{} {
	h := sha256.Sum256([]byte(token))
	ch, _ := accountSlots.LoadOrStore(string(h[:]), make(chan struct{}, MaxConnections))
	return ch.(chan struct{})
}

func defaultBackoff(attempt int) time.Duration {
	d := time.Second << (attempt - 1)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// apiTimeout bounds each request to Mailchimp's API.
const apiTimeout = 30 * time.Second

// maxAPIBody is the most of one API response that's read.
const maxAPIBody = 32 << 20

// ErrNotRead means a request other than GET was attempted. It never reaches
// Mailchimp.
var ErrNotRead = errors.New("the Mailchimp client only reads (GET)")

// ErrAccessRevoked means Mailchimp no longer takes the token: the customer
// removed mailgoose's access, or the connection was replaced.
var ErrAccessRevoked = errors.New("mailchimp no longer allows mailgoose to read this account")

// NewClient is a client for the account in dataCenter. baseURL is empty for
// Mailchimp itself (https://<dc>.api.mailchimp.com), or a fake server in
// tests. httpClient nil means one with apiTimeout.
func NewClient(token, dataCenterName, baseURL string, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		if !dataCenter.MatchString(dataCenterName) {
			return nil, fmt.Errorf("mailchimp: %q isn't a data center", dataCenterName)
		}
		baseURL = "https://" + dataCenterName + ".api.mailchimp.com"
	}
	c := &http.Client{Timeout: apiTimeout}
	if httpClient != nil {
		cp := *httpClient
		c = &cp
	}
	next := c.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	c.Transport = readOnly{next: next}
	return &Client{token: token, baseURL: strings.TrimRight(baseURL, "/"), http: c, slots: slotsFor(token), Backoff: defaultBackoff}, nil
}

// readOnly refuses every request that isn't a GET.
type readOnly struct{ next http.RoundTripper }

func (t readOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, ErrNotRead
	}
	return t.next.RoundTrip(r)
}

// APIError is an error answer from Mailchimp's API, with its problem
// details' title and status. The token is never in it.
type APIError struct {
	Status int
	Title  string
}

func (e *APIError) Error() string {
	if e.Title != "" {
		return fmt.Sprintf("mailchimp api: HTTP %d: %s", e.Status, e.Title)
	}
	return fmt.Sprintf("mailchimp api: HTTP %d", e.Status)
}

// Get reads path (such as "/3.0/lists") with query into out. It waits for
// one of the account's MaxConnections slots, and retries 429 and 5xx
// answers and failed requests, up to maxAttempts tries in all. A 401 is
// ErrAccessRevoked.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	if !strings.HasPrefix(path, "/3.0/") && path != "/3.0" {
		return fmt.Errorf("mailchimp: %q isn't a Marketing API path", path)
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	for attempt := 1; ; attempt++ {
		body, wait, err := c.try(ctx, path, u)
		if wait < 0 || attempt == maxAttempts {
			if err != nil {
				return err
			}
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("mailchimp api: %s didn't answer JSON", path)
			}
			return nil
		}
		if wait == 0 {
			wait = c.Backoff(attempt)
		}
		if wait > maxRetryWait {
			wait = maxRetryWait
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// try makes one request. wait is -1 when the result is final (body or
// err), and otherwise how long Mailchimp asked to wait before trying again
// (0 when it didn't say).
func (c *Client) try(ctx context.Context, path, u string) ([]byte, time.Duration, error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, -1, ctx.Err()
	}
	defer func() { <-c.slots }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, -1, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, -1, ctx.Err()
		}
		return nil, 0, fmt.Errorf("mailchimp api: %s failed", path)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody+1))
	if err != nil {
		return nil, 0, fmt.Errorf("mailchimp api: reading %s failed", path)
	}
	if len(body) > maxAPIBody {
		return nil, -1, fmt.Errorf("mailchimp api: %s answered more than %d MB", path, maxAPIBody>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var p struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(body, &p)
		apiErr := &APIError{Status: resp.StatusCode, Title: truncate(p.Title, 100)}
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, -1, fmt.Errorf("%w (%w)", ErrAccessRevoked, apiErr)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			return nil, retryAfter(resp.Header.Get("Retry-After")), apiErr
		}
		return nil, -1, apiErr
	}
	return body, -1, nil
}

// retryAfter reads a Retry-After header given in seconds; 0 when absent or
// in another form.
func retryAfter(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// Account is the connected account's name, from the API's root.
func (c *Client) Account(ctx context.Context) (string, error) {
	var out struct {
		AccountName string `json:"account_name"`
	}
	if err := c.Get(ctx, "/3.0/", nil, &out); err != nil {
		return "", err
	}
	return out.AccountName, nil
}
