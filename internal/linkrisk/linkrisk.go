// Package linkrisk looks up an email's links on lists of phishing,
// malware and unwanted-software sites (MG-64), so they're stopped before
// they go out.
//
// The lists are Google Web Risk's: licensed for commercial use and free up
// to 100,000 lookups a month (Google's free Safe Browsing API and
// Spamhaus's free domain list are for non-commercial use only). The vendor
// sits behind Lookup, with a Fake for tests and local runs.
//
// The check fails open: with no API key, or when Web Risk errors or takes
// longer than Timeout, links pass, the result says they weren't looked up,
// and the miss is logged. Sending never depends on Web Risk being up.
package linkrisk

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// Threat types, as Web Risk names them.
const (
	Malware           = "MALWARE"
	SocialEngineering = "SOCIAL_ENGINEERING"
	UnwantedSoftware  = "UNWANTED_SOFTWARE"
)

// ThreatTypes are the lists every link is looked up on.
var ThreatTypes = []string{Malware, SocialEngineering, UnwantedSoftware}

// Describe says what a threat type means, for a person.
func Describe(threat string) string {
	switch threat {
	case Malware:
		return "malware"
	case SocialEngineering:
		return "phishing"
	case UnwantedSoftware:
		return "unwanted software"
	}
	return "unsafe sites"
}

// Verdict is what a lookup found for one link.
type Verdict struct {
	// Threats lists the lists the link is on; empty when it's on none.
	Threats []string
	// Expires is how long the verdict may be cached: the time Web Risk
	// gives for a listed link. Zero for a link on no list, which is cached
	// for CleanTTL.
	Expires time.Time
}

// Lookup is the vendor: it looks up one link.
type Lookup interface {
	Search(ctx context.Context, url string) (Verdict, error)
}

// Defaults.
const (
	// Timeout bounds one check, every lookup together (the ticket's 2
	// seconds). Links not looked up by then pass, unchecked.
	Timeout = 2 * time.Second
	// CleanTTL is how long a link on no list is cached. Web Risk gives no
	// time for those; this keeps one campaign's links (check, test, launch
	// summary, launch) to one lookup each while still catching a site that
	// turns bad later.
	CleanTTL = 30 * time.Minute
	// MaxListedTTL caps how long a listed link is cached, whatever Web Risk
	// says.
	MaxListedTTL = 24 * time.Hour
	// MaxLinks is the most distinct links one check looks up; the rest are
	// unchecked.
	MaxLinks = 200
	// parallel is how many lookups run at once.
	parallel = 8
	// maxCache is how many verdicts are kept in memory.
	maxCache = 10_000
)

// Result is what a check found.
type Result struct {
	// Listed maps each link on a list to the lists it's on.
	Listed map[string][]string
	// Unchecked are the links that weren't looked up, and Why says why
	// ("the check isn't set up", "Web Risk didn't answer in time").
	Unchecked []string
	Why       string
}

// Checker looks links up, with a cache. A nil Lookup means the check isn't
// set up (no API key): every link passes, unchecked.
type Checker struct {
	Lookup Lookup
	Log    *slog.Logger
	// Timeout overrides the default 2 seconds; tests shorten it.
	Timeout time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu    sync.Mutex
	cache map[[32]byte]cached
}

type cached struct {
	threats []string
	expires time.Time
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log
}

// ErrNotConfigured is the reason links go unchecked without an API key.
var ErrNotConfigured = errors.New("the link safety check isn't set up")

func key(url string) [32]byte { return sha256.Sum256([]byte(url)) }

func (c *Checker) get(url string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.cache[key(url)]
	if !ok || !c.now().Before(v.expires) {
		return nil, false
	}
	return v.threats, true
}

func (c *Checker) put(url string, v Verdict) {
	now := c.now()
	expires := now.Add(CleanTTL)
	if len(v.Threats) > 0 {
		expires = v.Expires
		if expires.IsZero() || expires.Before(now) {
			expires = now.Add(CleanTTL)
		}
		if limit := now.Add(MaxListedTTL); expires.After(limit) {
			expires = limit
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = map[[32]byte]cached{}
	}
	if len(c.cache) >= maxCache {
		for k, e := range c.cache {
			if !now.Before(e.expires) {
				delete(c.cache, k)
			}
		}
		// Still full: drop any (map order is random).
		for k := range c.cache {
			if len(c.cache) < maxCache {
				break
			}
			delete(c.cache, k)
		}
	}
	c.cache[key(url)] = cached{threats: slices.Clone(v.Threats), expires: expires}
}

// Check looks up each distinct link, within Timeout, and never fails: a
// link that couldn't be looked up is in Unchecked, and logged.
func (c *Checker) Check(ctx context.Context, urls []string) Result {
	res := Result{Listed: map[string][]string{}}
	var todo []string
	seen := map[string]bool{}
	for _, u := range urls {
		if seen[u] {
			continue
		}
		seen[u] = true
		if threats, ok := c.get(u); ok {
			if len(threats) > 0 {
				res.Listed[u] = threats
			}
			continue
		}
		todo = append(todo, u)
	}
	if len(todo) == 0 {
		return res
	}
	if c.Lookup == nil {
		res.Unchecked, res.Why = todo, ErrNotConfigured.Error()
		c.log().WarnContext(ctx, "links not checked for phishing or malware", "reason", res.Why, "links", len(todo))
		return res
	}
	if len(todo) > MaxLinks {
		res.Unchecked = append(res.Unchecked, todo[MaxLinks:]...)
		res.Why = fmt.Sprintf("only the first %d links are looked up", MaxLinks)
		todo = todo[:MaxLinks]
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type answer struct {
		url string
		v   Verdict
		err error
	}
	answers := make(chan answer, len(todo))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, u := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				answers <- answer{url: u, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()
			v, err := c.Lookup.Search(ctx, u)
			answers <- answer{url: u, v: v, err: err}
		}()
	}
	wg.Wait()
	close(answers)
	var failed []string
	var firstErr error
	for a := range answers {
		if a.err != nil {
			failed = append(failed, a.url)
			if firstErr == nil || errors.Is(a.err, context.DeadlineExceeded) {
				firstErr = a.err
			}
			continue
		}
		c.put(a.url, a.v)
		if len(a.v.Threats) > 0 {
			res.Listed[a.url] = slices.Clone(a.v.Threats)
		}
	}
	if len(failed) > 0 {
		// Keep the links in the order they came.
		slices.SortFunc(failed, func(a, b string) int { return slices.Index(todo, a) - slices.Index(todo, b) })
		res.Unchecked = append(failed, res.Unchecked...)
		res.Why = "Web Risk didn't answer"
		if errors.Is(firstErr, context.DeadlineExceeded) {
			res.Why = "Web Risk didn't answer in time"
		}
		c.log().WarnContext(ctx, "links not checked for phishing or malware", "reason", res.Why, "err", firstErr, "links", len(failed))
	}
	return res
}
