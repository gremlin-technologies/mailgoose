package linkrisk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WebRiskEndpoint is Web Risk's uris.search method.
const WebRiskEndpoint = "https://webrisk.googleapis.com/v1/uris:search"

// WebRisk looks links up with Google Web Risk's uris.search, authenticated
// with an API key restricted to the Web Risk API.
type WebRisk struct {
	Key string
	// Endpoint overrides WebRiskEndpoint; tests point it at a local server.
	Endpoint string
	// Client is the HTTP client; nil means one with a 2-second timeout.
	Client *http.Client
}

var defaultClient = &http.Client{Timeout: Timeout}

// webRiskAnswer is uris.search's response: empty for a link on no list.
type webRiskAnswer struct {
	Threat *struct {
		ThreatTypes []string  `json:"threatTypes"`
		ExpireTime  time.Time `json:"expireTime"`
	} `json:"threat"`
}

// Search looks one link up on the malware, social engineering and unwanted
// software lists.
func (w *WebRisk) Search(ctx context.Context, link string) (Verdict, error) {
	if w.Key == "" {
		return Verdict{}, ErrNotConfigured
	}
	endpoint := w.Endpoint
	if endpoint == "" {
		endpoint = WebRiskEndpoint
	}
	q := url.Values{"uri": {link}, "key": {w.Key}}
	for _, t := range ThreatTypes {
		q.Add("threatTypes", t)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return Verdict{}, err
	}
	client := w.Client
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// The key is in the address; never let it reach a log.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Verdict{}, fmt.Errorf("web risk: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Verdict{}, fmt.Errorf("web risk: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Verdict{}, fmt.Errorf("web risk answered %s: %s", resp.Status, strings.TrimSpace(string(body[:min(len(body), 300)])))
	}
	var a webRiskAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return Verdict{}, fmt.Errorf("web risk: read the answer: %w", err)
	}
	if a.Threat == nil {
		return Verdict{}, nil
	}
	return Verdict{Threats: a.Threat.ThreatTypes, Expires: a.Threat.ExpireTime}, nil
}

// Fake is a Lookup for tests and local runs: links in Listed are on those
// lists, everything else is on none. It never calls Google.
type Fake struct {
	mu sync.Mutex
	// Listed maps a link to its threat types.
	Listed map[string][]string
	// Expires is the time listed verdicts carry; zero means an hour on.
	Expires time.Time
	// Err, when set, is returned for every lookup.
	Err error
	// Delay holds each lookup this long, or until the context ends.
	Delay time.Duration
	calls []string
}

// Search answers from Listed.
func (f *Fake) Search(ctx context.Context, link string) (Verdict, error) {
	f.mu.Lock()
	f.calls = append(f.calls, link)
	threats, delay, ferr, exp := f.Listed[link], f.Delay, f.Err, f.Expires
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return Verdict{}, ctx.Err()
		}
	}
	if ferr != nil {
		return Verdict{}, ferr
	}
	if len(threats) == 0 {
		return Verdict{}, nil
	}
	if exp.IsZero() {
		exp = time.Now().Add(time.Hour)
	}
	return Verdict{Threats: append([]string(nil), threats...), Expires: exp}, nil
}

// Calls lists the links looked up, in order.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// List puts a link on a list.
func (f *Fake) List(link string, threats ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Listed == nil {
		f.Listed = map[string][]string{}
	}
	f.Listed[link] = threats
}
