package linkrisk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	bad  = "https://bad.example/login"
	good = "https://good.example/"
)

func TestCheckListedAndCached(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	f := &Fake{Expires: now.Add(3 * time.Hour)}
	f.List(bad, SocialEngineering, Malware)
	c := &Checker{Lookup: f, Now: func() time.Time { return now }}

	res := c.Check(context.Background(), []string{good, bad, good})
	if !slices.Equal(res.Listed[bad], []string{SocialEngineering, Malware}) || len(res.Listed) != 1 || len(res.Unchecked) != 0 {
		t.Fatalf("result %+v", res)
	}
	if calls := f.Calls(); len(calls) != 2 {
		t.Fatalf("looked up %v; each distinct link once", calls)
	}

	// Within the cache times, nothing is looked up again.
	now = now.Add(29 * time.Minute)
	res = c.Check(context.Background(), []string{bad, good})
	if len(f.Calls()) != 2 || len(res.Listed[bad]) != 2 {
		t.Fatalf("calls %v, result %+v", f.Calls(), res)
	}
	// A clean link is looked up again after CleanTTL; a listed one keeps
	// the time Web Risk gave.
	now = now.Add(2 * time.Minute)
	c.Check(context.Background(), []string{bad, good})
	if calls := f.Calls(); len(calls) != 3 || calls[2] != good {
		t.Fatalf("calls %v", calls)
	}
	now = now.Add(3 * time.Hour)
	c.Check(context.Background(), []string{bad})
	if calls := f.Calls(); len(calls) != 4 || calls[3] != bad {
		t.Fatalf("calls %v", calls)
	}
}

func TestListedTTLCapped(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	f := &Fake{Expires: now.AddDate(1, 0, 0)}
	f.List(bad, Malware)
	c := &Checker{Lookup: f, Now: func() time.Time { return now }}
	c.Check(context.Background(), []string{bad})
	now = now.Add(MaxListedTTL)
	c.Check(context.Background(), []string{bad})
	if len(f.Calls()) != 2 {
		t.Errorf("calls %v: a listed verdict is kept at most %v", f.Calls(), MaxListedTTL)
	}
}

func TestCheckNotConfigured(t *testing.T) {
	c := &Checker{}
	res := c.Check(context.Background(), []string{bad, good})
	if len(res.Listed) != 0 || !slices.Equal(res.Unchecked, []string{bad, good}) || res.Why != ErrNotConfigured.Error() {
		t.Errorf("result %+v", res)
	}
}

func TestCheckFailsOpen(t *testing.T) {
	f := &Fake{Err: errors.New("503 Service Unavailable")}
	f.List(bad, Malware)
	c := &Checker{Lookup: f}
	res := c.Check(context.Background(), []string{bad, good})
	if len(res.Listed) != 0 || !slices.Equal(res.Unchecked, []string{bad, good}) || res.Why != "Web Risk didn't answer" {
		t.Errorf("result %+v", res)
	}
	// Failures aren't cached.
	f.mu.Lock()
	f.Err = nil
	f.mu.Unlock()
	if res := c.Check(context.Background(), []string{bad}); len(res.Listed) != 1 {
		t.Errorf("after recovering: %+v", res)
	}
}

func TestCheckTimesOut(t *testing.T) {
	f := &Fake{Delay: time.Second}
	f.List(bad, Malware)
	c := &Checker{Lookup: f, Timeout: 50 * time.Millisecond}
	start := time.Now()
	res := c.Check(context.Background(), []string{bad, good})
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("took %v", took)
	}
	if len(res.Listed) != 0 || len(res.Unchecked) != 2 || res.Why != "Web Risk didn't answer in time" {
		t.Errorf("result %+v", res)
	}
}

func TestCheckManyLinks(t *testing.T) {
	c := &Checker{Lookup: &Fake{}}
	var urls []string
	for i := range MaxLinks + 3 {
		urls = append(urls, "https://example.com/"+strings.Repeat("a", i+1))
	}
	res := c.Check(context.Background(), urls)
	if len(res.Unchecked) != 3 || !strings.Contains(res.Why, "first 200") {
		t.Errorf("unchecked %d, why %q", len(res.Unchecked), res.Why)
	}
}

func TestWebRisk(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got = append(got, q.Get("uri"))
		if q.Get("key") != "k123" || !slices.Equal(q["threatTypes"], ThreatTypes) || r.URL.Path != "/v1/uris:search" {
			t.Errorf("request %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		switch q.Get("uri") {
		case bad:
			_, _ = w.Write([]byte(`{"threat":{"threatTypes":["SOCIAL_ENGINEERING"],"expireTime":"2026-11-19T13:00:00.5Z"}}`))
		case good:
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, `{"error":{"code":403,"message":"API key not valid"}}`, http.StatusForbidden)
		}
	}))
	defer srv.Close()
	w := &WebRisk{Key: "k123", Endpoint: srv.URL + "/v1/uris:search"}
	ctx := context.Background()

	v, err := w.Search(ctx, bad)
	if err != nil || !slices.Equal(v.Threats, []string{SocialEngineering}) ||
		!v.Expires.Equal(time.Date(2026, 11, 19, 13, 0, 0, 5e8, time.UTC)) {
		t.Errorf("bad: %+v %v", v, err)
	}
	if v, err := w.Search(ctx, good); err != nil || len(v.Threats) != 0 {
		t.Errorf("good: %+v %v", v, err)
	}
	_, err = w.Search(ctx, "https://other.example/")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("error answer: %v", err)
	}
	if !slices.Equal(got, []string{bad, good, "https://other.example/"}) {
		t.Errorf("looked up %v", got)
	}

	// A failed connection's error never carries the key.
	srv.Close()
	_, err = w.Search(ctx, bad)
	if err == nil || strings.Contains(err.Error(), "k123") {
		t.Errorf("closed server: %v", err)
	}
	if _, err := (&WebRisk{}).Search(ctx, bad); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no key: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	for in, want := range map[string]string{Malware: "malware", SocialEngineering: "phishing",
		UnwantedSoftware: "unwanted software", "OTHER": "unsafe sites"} {
		if got := Describe(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}
