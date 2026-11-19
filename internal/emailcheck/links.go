package emailcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

// What a link check found.
const (
	ResultOK          = "ok"
	ResultMissing     = "missing"     // the page or the site doesn't exist
	ResultUnreachable = "unreachable" // no good answer: an error status, a timeout, a refused connection
	ResultNotChecked  = "not_checked" // private address, or out of time
)

// LinkStatus is what a link check found for one address.
type LinkStatus struct {
	Result string
	// final means asking again with GET can't help, as when the site
	// doesn't exist.
	final bool
	// Detail says why, for anything but ResultOK: "404 Not Found", "timed
	// out".
	Detail string
}

// LinkChecker looks at each of the links and says what it found, with a
// status for every one.
type LinkChecker interface {
	CheckLinks(ctx context.Context, urls []string) map[string]LinkStatus
}

// HTTPLinks checks links with a HEAD request each, falling back to GET
// when a site refuses HEAD. It never connects to a private, loopback,
// link-local or otherwise internal address, whether the link names one or
// its host or a redirect leads to one: the check is on the address each
// connection actually dials.
type HTTPLinks struct {
	// Timeout bounds each link; zero means 3 seconds.
	Timeout time.Duration
	// Total bounds the whole check; links not finished by then are
	// ResultNotChecked. Zero means 10 seconds.
	Total time.Duration
	// Parallel is how many links are checked at once; zero means 8.
	Parallel int
	// Allow reports whether an address and port may be dialed. Nil allows
	// public addresses only (PublicAddress). Tests point it at a local
	// server.
	Allow func(netip.AddrPort) bool
}

// MaxLinks is the most distinct links one check fetches; the rest are
// ResultNotChecked.
const MaxLinks = 50

// MaxRedirects is how many redirects a link may take.
const MaxRedirects = 5

var errPrivate = errors.New("the address is private")

// Ranges that are internal or special even though netip doesn't call them
// private: carrier-grade NAT, "this network", IETF protocol assignments,
// benchmarking, and NAT64, which can reach an IPv4 private address.
var internalRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// PublicAddress reports whether a is a public unicast address, the only
// kind HTTPLinks dials by default.
func PublicAddress(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range internalRanges {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func (h *HTTPLinks) client() *http.Client {
	allow := h.Allow
	if allow == nil {
		allow = func(ap netip.AddrPort) bool { return PublicAddress(ap.Addr()) }
	}
	dialer := &net.Dialer{
		Timeout: h.timeout(),
		// Control runs after the name is resolved, on the address about to
		// be dialed, so no lookup or redirect can get around it.
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !allow(ap) {
				return errPrivate
			}
			return nil
		},
	}
	return &http.Client{
		Transport: &http.Transport{
			// No proxy: a proxy would dial for us, past the check above.
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   h.timeout(),
			ResponseHeaderTimeout: h.timeout(),
			MaxIdleConnsPerHost:   2,
			DisableCompression:    true,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return fmt.Errorf("more than %d redirects", MaxRedirects)
			}
			return nil
		},
	}
}

func (h *HTTPLinks) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return 3 * time.Second
}

// CheckLinks checks each distinct link, a few at a time, within Total.
func (h *HTTPLinks) CheckLinks(ctx context.Context, urls []string) map[string]LinkStatus {
	total := h.Total
	if total <= 0 {
		total = 10 * time.Second
	}
	parallel := h.Parallel
	if parallel <= 0 {
		parallel = 8
	}
	ctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	client := h.client()
	defer client.CloseIdleConnections()

	// Every link starts as out of time, so one the check never reaches says
	// so; the goroutines only overwrite their own entries.
	out := make(map[string]LinkStatus, len(urls))
	var todo []string
	for _, u := range urls {
		if _, seen := out[u]; seen {
			continue
		}
		if len(todo) == MaxLinks {
			out[u] = LinkStatus{Result: ResultNotChecked, Detail: fmt.Sprintf("only the first %d links are checked", MaxLinks)}
			continue
		}
		out[u] = LinkStatus{Result: ResultNotChecked, Detail: "the check ran out of time"}
		todo = append(todo, u)
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	for _, u := range todo {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			st := h.check(ctx, client, u)
			mu.Lock()
			out[u] = st
			mu.Unlock()
		}(u)
	}
	wg.Wait()
	return out
}

func (h *HTTPLinks) check(total context.Context, client *http.Client, u string) LinkStatus {
	st := h.fetch(total, client, http.MethodHead, u)
	// Some sites refuse HEAD, or answer it wrongly; ask again with GET.
	if st.Result != ResultOK && st.Result != ResultNotChecked && !st.final && total.Err() == nil {
		if get := h.fetch(total, client, http.MethodGet, u); get.Result == ResultOK || st.Result == ResultUnreachable {
			st = get
		}
	}
	return st
}

func (h *HTTPLinks) fetch(total context.Context, client *http.Client, method, u string) LinkStatus {
	ctx, cancel := context.WithTimeout(total, h.timeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return LinkStatus{Result: ResultMissing, Detail: "it isn't a valid address"}
	}
	req.Header.Set("User-Agent", "mailgoose-link-check/1 (+https://mailgoose.app)")
	resp, err := client.Do(req)
	if err != nil {
		var dnsErr *net.DNSError
		switch {
		case errors.Is(err, errPrivate):
			return LinkStatus{Result: ResultNotChecked, Detail: "it points to a private address, which isn't checked"}
		case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
			return LinkStatus{Result: ResultMissing, final: true, Detail: "the site " + req.URL.Hostname() + " doesn't exist"}
		case total.Err() != nil:
			return LinkStatus{Result: ResultNotChecked, Detail: "the check ran out of time"}
		case ctx.Err() != nil:
			return LinkStatus{Result: ResultUnreachable, Detail: "it timed out"}
		}
		return LinkStatus{Result: ResultUnreachable, Detail: "it couldn't be reached"}
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode < 400:
		return LinkStatus{Result: ResultOK}
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return LinkStatus{Result: ResultMissing, Detail: "the page answers " + resp.Status}
	}
	return LinkStatus{Result: ResultUnreachable, Detail: "the page answers " + resp.Status}
}
