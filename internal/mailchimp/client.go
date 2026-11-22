package mailchimp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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
}

// apiTimeout bounds each request to Mailchimp's API.
const apiTimeout = 30 * time.Second

// maxAPIBody is the most of one API response that's read.
const maxAPIBody = 32 << 20

// ErrNotRead means a request other than GET was attempted. It never reaches
// Mailchimp.
var ErrNotRead = errors.New("the Mailchimp client only reads (GET)")

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
	return &Client{token: token, baseURL: strings.TrimRight(baseURL, "/"), http: c}, nil
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

// Get reads path (such as "/3.0/lists") with query into out.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	if !strings.HasPrefix(path, "/3.0/") && path != "/3.0" {
		return fmt.Errorf("mailchimp: %q isn't a Marketing API path", path)
	}
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("mailchimp api: %s failed", path)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIBody+1))
	if err != nil {
		return fmt.Errorf("mailchimp api: reading %s failed", path)
	}
	if len(body) > maxAPIBody {
		return fmt.Errorf("mailchimp api: %s answered more than %d MB", path, maxAPIBody>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var p struct {
			Title string `json:"title"`
		}
		_ = json.Unmarshal(body, &p)
		return &APIError{Status: resp.StatusCode, Title: truncate(p.Title, 100)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("mailchimp api: %s didn't answer JSON", path)
	}
	return nil
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
