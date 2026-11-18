package emailcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34":            true,
		"2606:2800:220:1::":        true,
		"127.0.0.1":                false,
		"10.1.2.3":                 false,
		"172.16.0.1":               false,
		"192.168.1.1":              false,
		"169.254.169.254":          false, // cloud metadata
		"100.64.0.1":               false,
		"0.0.0.0":                  false,
		"198.18.0.1":               false,
		"::1":                      false,
		"fd00::1":                  false,
		"fe80::1":                  false,
		"::ffff:10.0.0.1":          false,
		"64:ff9b::a00:1":           false,
		"224.0.0.1":                false,
		"255.255.255.255":          false,
		"::ffff:93.184.216.34":     true,
		"2001:4860:4860::8888":     true,
		"192.0.0.8":                false,
		"8.8.8.8":                  true,
		"fc00::1":                  false,
		"ff02::1":                  false,
		"0:0:0:0:0:0:0:0":          false,
		"64:ff9b:1::a00:1":         false,
		"100.127.255.255":          false,
		"100.128.0.1":              true,
		"198.20.0.1":               true,
		"172.32.0.1":               true,
		"169.254.0.1":              false,
		"127.255.255.254":          false,
		"192.168.255.255":          false,
		"10.255.255.255":           false,
		"::ffff:127.0.0.1":         false,
		"::ffff:169.254.169.254":   false,
		"2001:db8::1":              true, // documentation range: harmless, not internal
		"192.0.2.1":                true, // likewise
		"203.0.113.1":              true,
		"198.51.100.1":             true,
		"240.0.0.1":                true,
		"1.1.1.1":                  true,
		"9.9.9.9":                  true,
		"185.199.108.153":          true,
		"2a00:1450:4001:80b::200e": true,
	} {
		if got := PublicAddress(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: got %v", addr, got)
		}
	}
}

// local lets the checker reach a test server on 127.0.0.1.
func local(netip.AddrPort) bool { return true }

func TestCheckLinks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("/gone", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	mux.HandleFunc("/no-head", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/slow", func(http.ResponseWriter, *http.Request) { time.Sleep(300 * time.Millisecond) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	h := &HTTPLinks{Timeout: 100 * time.Millisecond, Allow: local}
	u := func(p string) string { return srv.URL + p }
	got := h.CheckLinks(context.Background(), []string{
		u("/ok"), u("/gone"), u("/forbidden"), u("/no-head"), u("/redirect"), u("/loop"), u("/slow"), u("/ok"),
	})
	want := map[string]string{
		"/ok": ResultOK, "/gone": ResultMissing, "/forbidden": ResultUnreachable, "/no-head": ResultOK,
		"/redirect": ResultOK, "/loop": ResultUnreachable, "/slow": ResultUnreachable,
	}
	if len(got) != len(want) {
		t.Errorf("got %d results, want %d", len(got), len(want))
	}
	for p, w := range want {
		if st := got[u(p)]; st.Result != w {
			t.Errorf("%s: got %+v, want %s", p, st, w)
		}
	}
	if d := got[u("/gone")].Detail; d != "the page answers 404 Not Found" {
		t.Errorf("gone detail %q", d)
	}
	if d := got[u("/slow")].Detail; d != "it timed out" {
		t.Errorf("slow detail %q", d)
	}
}

func TestCheckLinksRefusesPrivateAddresses(t *testing.T) {
	var hits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer private.Close()
	privateAddr := netip.MustParseAddrPort(strings.TrimPrefix(private.URL, "http://"))

	// With the default rule, a loopback address isn't dialed, by IP or by
	// a name that resolves to one.
	byName := "http://localhost:" + strconv.Itoa(int(privateAddr.Port())) + "/b"
	got := (&HTTPLinks{}).CheckLinks(context.Background(), []string{private.URL + "/a", byName})
	for u, st := range got {
		if st.Result != ResultNotChecked || !strings.Contains(st.Detail, "private address") {
			t.Errorf("%s: %+v", u, st)
		}
	}

	// A link to an allowed server that redirects into a private one is
	// stopped when the redirect is dialed.
	bounce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL+"/internal", http.StatusFound)
	}))
	defer bounce.Close()
	bounceAddr := netip.MustParseAddrPort(strings.TrimPrefix(bounce.URL, "http://"))
	onlyBounce := func(ap netip.AddrPort) bool { return ap == bounceAddr }
	got = (&HTTPLinks{Allow: onlyBounce}).CheckLinks(context.Background(), []string{bounce.URL + "/go"})
	if st := got[bounce.URL+"/go"]; st.Result != ResultNotChecked || !strings.Contains(st.Detail, "private address") {
		t.Errorf("redirect: %+v", st)
	}
	if hits.Load() != 0 {
		t.Errorf("the private server got %d requests", hits.Load())
	}
}

func TestCheckLinksTotalAndCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(200 * time.Millisecond) }))
	defer srv.Close()
	var urls []string
	for i := 0; i < MaxLinks+3; i++ {
		urls = append(urls, srv.URL+"/"+strings.Repeat("x", i+1))
	}
	start := time.Now()
	got := (&HTTPLinks{Timeout: time.Second, Total: 100 * time.Millisecond, Parallel: 2, Allow: local}).CheckLinks(context.Background(), urls)
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v; the total cap is 100ms", d)
	}
	capped, outOfTime := 0, 0
	for _, st := range got {
		switch {
		case st.Result == ResultNotChecked && strings.Contains(st.Detail, "only the first"):
			capped++
		case st.Result == ResultNotChecked && strings.Contains(st.Detail, "ran out of time"):
			outOfTime++
		}
	}
	if capped != 3 || outOfTime != MaxLinks {
		t.Errorf("capped %d, out of time %d", capped, outOfTime)
	}
}
