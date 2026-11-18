package domains

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

// fakeDNS answers from maps; anything else is NXDOMAIN.
type fakeDNS struct {
	mu    sync.Mutex
	cname map[string]string
	mx    map[string][]*net.MX
	txt   map[string][]string
	fail  error
}

func newFakeDNS() *fakeDNS {
	return &fakeDNS{cname: map[string]string{}, mx: map[string][]*net.MX{}, txt: map[string][]string{}}
}

func nx(name string) error { return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true} }

func (f *fakeDNS) LookupCNAME(_ context.Context, host string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", f.fail
	}
	if v, ok := f.cname[host]; ok {
		return v, nil
	}
	return "", nx(host)
}

func (f *fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.mx[name]; ok {
		return v, nil
	}
	return nil, nx(name)
}

func (f *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, nx(name)
}

// publish adds every record the domain asks for, as a careful person would.
func (f *fakeDNS) publish(s *Service, d *Domain) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range s.Records(d) {
		switch r.Type {
		case "CNAME":
			f.cname[r.Name] = r.Value + "."
		case "MX":
			f.mx[r.Name] = []*net.MX{{Host: r.Value + ".", Pref: 10}}
		case "TXT":
			f.txt[r.Name] = append(f.txt[r.Name], r.Value)
		}
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type harness struct {
	svc  *Service
	ses  *FakeSES
	dns  *fakeDNS
	clk  *clock
	acc  string
	acc2 string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	h := &harness{ses: &FakeSES{}, dns: newFakeDNS(), clk: &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}}
	h.svc = &Service{
		DB: db, SES: h.ses, DNS: h.dns, Region: "us-east-2",
		IdentityTags:     map[string]string{"Environment": "staging"},
		ConfigurationSet: "mailgoose-staging",
		Reserved:         []string{"mailgoose.app"},
		Now:              h.clk.now,
	}
	ctx := context.Background()
	for _, p := range []*string{&h.acc, &h.acc2} {
		if err := db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(p); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func TestNormalizeName(t *testing.T) {
	good := map[string]string{
		"Example.COM":         "example.com",
		" news.example.com. ": "news.example.com",
		"bücher.de":           "xn--bcher-kva.de",
		"pats.co.uk":          "pats.co.uk",
		"pat.github.io":       "pat.github.io",
	}
	for in, want := range good {
		if got, err := NormalizeName(in); err != nil || got != want {
			t.Errorf("NormalizeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "com", "co.uk", "localhost", "example", "pat@example.com", "http://example.com",
		"example.notarealtld", "exa mple.com", "192.0.2.1", strings.Repeat("a", 200) + ".com"} {
		if got, err := NormalizeName(bad); !errors.Is(err, ErrInvalidName) {
			t.Errorf("NormalizeName(%q) = %q, %v; want ErrInvalidName", bad, got, err)
		}
	}
}

func TestAddCreatesIdentityAndRecords(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d, err := h.svc.Add(ctx, h.acc, "Example.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "example.com" || d.MailFrom != "bounce.example.com" || d.Status != StatusPending || len(d.DKIMTokens) != 3 {
		t.Fatalf("domain = %+v", d)
	}
	if !d.CheckUntil.Equal(h.clk.t.Add(72*time.Hour)) || d.Checks != nil {
		t.Errorf("check_until %v, checks %v", d.CheckUntil, d.Checks)
	}
	got, err := h.ses.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String("example.com")})
	if err != nil {
		t.Fatal(err)
	}
	if tagValue(got.Tags, "Environment") != "staging" || tagValue(got.Tags, TagAccount) != h.acc {
		t.Errorf("identity tags = %v", got.Tags)
	}

	recs := h.svc.Records(d)
	if len(recs) != 6 {
		t.Fatalf("records = %+v", recs)
	}
	want := []struct{ purpose, typ, name, value string }{
		{PurposeDKIM, "CNAME", d.DKIMTokens[0] + "._domainkey.example.com", d.DKIMTokens[0] + ".dkim.amazonses.com"},
		{PurposeDKIM, "CNAME", d.DKIMTokens[1] + "._domainkey.example.com", d.DKIMTokens[1] + ".dkim.amazonses.com"},
		{PurposeDKIM, "CNAME", d.DKIMTokens[2] + "._domainkey.example.com", d.DKIMTokens[2] + ".dkim.amazonses.com"},
		{PurposeMailFrom, "MX", "bounce.example.com", "feedback-smtp.us-east-2.amazonses.com"},
		{PurposeSPF, "TXT", "bounce.example.com", "v=spf1 include:amazonses.com ~all"},
		{PurposeDMARC, "TXT", "_dmarc.example.com", "v=DMARC1; p=none;"},
	}
	for i, w := range want {
		r := recs[i]
		if r.Purpose != w.purpose || r.Type != w.typ || r.Name != w.name || r.Value != w.value || r.Required != (w.purpose != PurposeDMARC) {
			t.Errorf("record %d = %+v, want %+v", i, r, w)
		}
	}
	if *recs[3].Priority != 10 {
		t.Errorf("MX priority = %d", *recs[3].Priority)
	}
}

func TestAddSkipsDMARCWhenOneExists(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.dns.txt["_dmarc.example.com"] = []string{"v=DMARC1; p=reject"}
	d, err := h.svc.Add(ctx, h.acc, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	// A subdomain inherits the organizational domain's policy.
	sub, err := h.svc.Add(ctx, h.acc, "news.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []*Domain{d, sub} {
		if !x.HadDMARC || len(h.svc.Records(x)) != 5 {
			t.Errorf("%s: had_dmarc %v, %d records", x.Name, x.HadDMARC, len(h.svc.Records(x)))
		}
	}
}

func TestAddRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for name, want := range map[string]error{
		"com":                ErrInvalidName,
		"mailgoose.app":      ErrReserved,
		"mail.mailgoose.app": ErrReserved,
	} {
		if _, err := h.svc.Add(ctx, h.acc, name); !errors.Is(err, want) {
			t.Errorf("Add(%q) = %v, want %v", name, err, want)
		}
	}
	if _, err := h.svc.Add(ctx, h.acc, "example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Add(ctx, h.acc, "EXAMPLE.com"); !errors.Is(err, ErrExists) {
		t.Errorf("same account again: %v", err)
	}
	if _, err := h.svc.Add(ctx, h.acc2, "example.com"); !errors.Is(err, ErrTaken) {
		t.Errorf("other account while pending: %v", err)
	}
}

// TestAddRace adds the same new name from two accounts at once. Exactly one
// Add wins; every loser gets ErrTaken or ErrExists, never a database error.
func TestAddRace(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for round := range 5 {
		name := fmt.Sprintf("race%d.example.com", round)
		const n = 8
		errs := make([]error, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			acc := h.acc
			if i%2 == 1 {
				acc = h.acc2
			}
			wg.Go(func() {
				<-start
				_, errs[i] = h.svc.Add(ctx, acc, name)
			})
		}
		close(start)
		wg.Wait()
		wins := 0
		for _, err := range errs {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrTaken), errors.Is(err, ErrExists):
			default:
				t.Errorf("%s: unexpected error %v", name, err)
			}
		}
		if wins != 1 {
			t.Errorf("%s: %d adds won, want 1", name, wins)
		}
	}
}

func TestAddAfterSESFailureLeavesNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.ses.Err = errors.New("throttled")
	if _, err := h.svc.Add(ctx, h.acc, "example.com"); err == nil {
		t.Fatal("want an error")
	}
	h.ses.Err = nil
	if ds, _ := h.svc.List(ctx, h.acc, "", "", 10); len(ds) != 0 {
		t.Errorf("domains left: %v", ds)
	}
	if _, err := h.svc.Add(ctx, h.acc, "example.com"); err != nil {
		t.Errorf("retry: %v", err)
	}
}

func TestAddWithExistingSESIdentity(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// An identity left over from this account (say the save failed) is adopted.
	if _, err := h.ses.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{EmailIdentity: aws.String("example.com"),
		Tags: []types.Tag{{Key: aws.String(TagAccount), Value: aws.String(h.acc)}}}); err != nil {
		t.Fatal(err)
	}
	d, err := h.svc.Add(ctx, h.acc, "example.com")
	if err != nil || d.DKIMTokens[0] != h.ses.Tokens("example.com", 0)[0] {
		t.Fatalf("adopt: %+v, %v", d, err)
	}
	// One that isn't this account's is refused, and nothing is saved.
	if _, err := h.ses.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{EmailIdentity: aws.String("other.example"),
		Tags: []types.Tag{{Key: aws.String(TagAccount), Value: aws.String("someone-else")}}}); err != nil {
		t.Fatal(err)
	}
	h.ses.mu.Lock()
	h.ses.identities["other.com"] = h.ses.identities["other.example"]
	h.ses.mu.Unlock()
	if _, err := h.svc.Add(ctx, h.acc, "other.com"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("foreign identity: %v", err)
	}
	if ds, _ := h.svc.List(ctx, h.acc, "other.com", "", 10); len(ds) != 0 {
		t.Error("refused domain was saved")
	}
}

func TestVerifyReportsEachRecord(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d, err := h.svc.Add(ctx, h.acc, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	h.ses.Unverified = map[string]bool{"example.com": true}

	// Nothing published yet.
	d, _, err = h.svc.Verify(ctx, h.acc, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusPending || len(d.Checks) != 6 || d.SESStatus != "pending" || d.LastCheckedAt == nil {
		t.Fatalf("after first verify: %+v", d)
	}
	for _, c := range d.Checks {
		if c.Status != Missing {
			t.Errorf("%s %s = %s, want missing", c.Purpose, c.Name, c.Status)
		}
	}

	// Published, but one DKIM CNAME points elsewhere, the SPF record lacks
	// amazonses.com, and the MX points at another host.
	h.dns.publish(h.svc, d)
	h.dns.cname[d.DKIMTokens[1]+"._domainkey.example.com"] = "wrong.example.net."
	h.dns.txt["bounce.example.com"] = []string{"v=spf1 -all"}
	h.dns.mx["bounce.example.com"] = []*net.MX{{Host: "mx.example.net.", Pref: 5}}
	d, _, _ = h.svc.Verify(ctx, h.acc, d.ID)
	statuses := map[string]string{}
	for _, c := range d.Checks {
		statuses[c.Name] = c.Status
	}
	if statuses[d.DKIMTokens[0]+"._domainkey.example.com"] != Found || statuses[d.DKIMTokens[1]+"._domainkey.example.com"] != Wrong ||
		statuses["bounce.example.com"] != Wrong || statuses["_dmarc.example.com"] != Found || d.Status != StatusPending {
		t.Errorf("checks = %+v", d.Checks)
	}
	if d.Checks[3].Found[0] != "5 mx.example.net" || !strings.Contains(d.Checks[1].Detail, "wrong.example.net") {
		t.Errorf("found/detail = %+v", d.Checks)
	}

	// Everything right, but SES hasn't confirmed: still pending.
	h.dns = newFakeDNS()
	h.svc.DNS = h.dns
	h.dns.publish(h.svc, d)
	if d, _, _ = h.svc.Verify(ctx, h.acc, d.ID); d.Status != StatusPending {
		t.Errorf("before SES confirms: %s", d.Status)
	}
	// SES confirms: verified.
	h.ses.Unverified = nil
	d, _, _ = h.svc.Verify(ctx, h.acc, d.ID)
	if d.Status != StatusVerified || d.VerifiedAt == nil || d.SESStatus != "success" {
		t.Errorf("after SES confirms: %+v", d)
	}
}

func TestVerifyDNSLookupFailureIsMissing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d, _ := h.svc.Add(ctx, h.acc, "example.com")
	h.dns.fail = errors.New("i/o timeout")
	d, _, err := h.svc.Verify(ctx, h.acc, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Checks[0].Status != Missing || !strings.Contains(d.Checks[0].Detail, "timeout") {
		t.Errorf("check = %+v", d.Checks[0])
	}
}

func TestBackgroundRechecks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d, _ := h.svc.Add(ctx, h.acc, "example.com")
	h.ses.Unverified = map[string]bool{"example.com": true}

	if n, err := h.svc.RecheckDue(ctx, 10); err != nil || n != 0 {
		t.Fatalf("nothing due yet: %d, %v", n, err)
	}
	h.clk.t = h.clk.t.Add(CheckEvery)
	if n, _ := h.svc.RecheckDue(ctx, 10); n != 1 {
		t.Fatalf("due after 5 minutes: %d", n)
	}
	if n, _ := h.svc.RecheckDue(ctx, 10); n != 0 {
		t.Fatalf("checked again at once: %d", n)
	}

	// Records go up and SES confirms: the next re-check verifies it.
	h.dns.publish(h.svc, d)
	h.ses.Unverified = nil
	h.clk.t = h.clk.t.Add(CheckEvery)
	if _, err := h.svc.RecheckDue(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.svc.Get(ctx, h.acc, d.ID); got.Status != StatusVerified {
		t.Errorf("status = %s, want verified", got.Status)
	}
	// Verified domains aren't re-checked.
	h.clk.t = h.clk.t.Add(CheckEvery)
	if n, _ := h.svc.RecheckDue(ctx, 10); n != 0 {
		t.Errorf("re-checked a verified domain")
	}
}

func TestFailsAfter72HoursAndVerifyRestarts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d, _ := h.svc.Add(ctx, h.acc, "example.com")
	first := d.DKIMTokens
	h.ses.Failed = map[string]bool{"example.com": true}

	h.clk.t = h.clk.t.Add(CheckFor)
	if _, err := h.svc.RecheckDue(ctx, 10); err != nil {
		t.Fatal(err)
	}
	d, _ = h.svc.Get(ctx, h.acc, d.ID)
	if d.Status != StatusFailed || d.FailedAt == nil || d.SESStatus != "failed" {
		t.Fatalf("after 72 hours: %+v", d)
	}
	h.clk.t = h.clk.t.Add(time.Hour)
	if n, _ := h.svc.RecheckDue(ctx, 10); n != 0 {
		t.Error("failed domains aren't re-checked")
	}

	// Another account can take a failed name over...
	// ...but first the owner tries again: SES gave up, so the identity is
	// made again with new DKIM records, and 72 more hours start.
	h.ses.Failed = nil
	h.ses.Unverified = map[string]bool{"example.com": true}
	d, recreated, err := h.svc.Verify(ctx, h.acc, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !recreated || d.DKIMTokens[0] == first[0] || d.Status != StatusPending || d.FailedAt != nil ||
		!d.CheckUntil.Equal(h.clk.t.Add(CheckFor)) {
		t.Errorf("after verify: recreated %v, %+v", recreated, d)
	}
}

func TestTakeOverFailedDomain(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	old, _ := h.svc.Add(ctx, h.acc, "example.com")
	h.ses.Failed = map[string]bool{"example.com": true}
	h.clk.t = h.clk.t.Add(CheckFor)
	if _, err := h.svc.RecheckDue(ctx, 10); err != nil {
		t.Fatal(err)
	}

	h.ses.Failed = nil
	d, err := h.svc.Add(ctx, h.acc2, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if d.AccountID != h.acc2 || len(h.ses.Deleted) != 1 {
		t.Errorf("takeover: %+v, deleted %v", d, h.ses.Deleted)
	}
	if _, err := h.svc.Get(ctx, h.acc, old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("old domain: %v", err)
	}
	got, _ := h.ses.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String("example.com")})
	if tagValue(got.Tags, TagAccount) != h.acc2 {
		t.Error("identity should now be tagged with the new account")
	}
}

func TestDeleteRemovesIdentity(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d, _ := h.svc.Add(ctx, h.acc, "example.com")
	if err := h.svc.Delete(ctx, h.acc2, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another account's delete: %v", err)
	}
	if err := h.svc.Delete(ctx, h.acc, d.ID); err != nil {
		t.Fatal(err)
	}
	if h.ses.Has("example.com") {
		t.Error("SES identity still there")
	}
	if _, err := h.svc.Get(ctx, h.acc, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}

	// An identity already gone from SES doesn't block the delete.
	d, _ = h.svc.Add(ctx, h.acc, "example.com")
	_, _ = h.ses.DeleteEmailIdentity(ctx, &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String("example.com")})
	if err := h.svc.Delete(ctx, h.acc, d.ID); err != nil {
		t.Errorf("delete with identity gone: %v", err)
	}

	// An SES error keeps the domain, so the delete can be retried.
	d, _ = h.svc.Add(ctx, h.acc, "example.com")
	h.ses.Err = errors.New("throttled")
	if err := h.svc.Delete(ctx, h.acc, d.ID); err == nil {
		t.Error("want an error")
	}
	h.ses.Err = nil
	if _, err := h.svc.Get(ctx, h.acc, d.ID); err != nil {
		t.Errorf("domain should remain: %v", err)
	}
}

func TestListPagesAndFilters(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var ids []string
	for _, n := range []string{"a.com", "b.com", "c.com"} {
		d, err := h.svc.Add(ctx, h.acc, n)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
		h.clk.t = h.clk.t.Add(time.Second)
	}
	page, _ := h.svc.List(ctx, h.acc, "", "", 2)
	if len(page) != 2 || page[0].Name != "a.com" {
		t.Fatalf("page 1 = %v", page)
	}
	page, _ = h.svc.List(ctx, h.acc, "", page[1].ID, 2)
	if len(page) != 1 || page[0].Name != "c.com" {
		t.Errorf("page 2 = %v", page)
	}
	if one, _ := h.svc.List(ctx, h.acc, "B.com", "", 10); len(one) != 1 || one[0].ID != ids[1] {
		t.Errorf("by name = %v", one)
	}
	if none, _ := h.svc.List(ctx, h.acc2, "", "", 10); len(none) != 0 {
		t.Errorf("other account sees %v", none)
	}
}

func TestRequireVerifiedFrom(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	d, _ := h.svc.Add(ctx, h.acc, "example.com")
	pending, _ := h.svc.Add(ctx, h.acc, "pending.org")
	h.ses.Unverified = map[string]bool{"pending.org": true}
	h.dns.publish(h.svc, d)
	h.dns.publish(h.svc, pending)
	if d, _, _ = h.svc.Verify(ctx, h.acc, d.ID); d.Status != StatusVerified {
		t.Fatalf("setup: %s", d.Status)
	}
	if _, _, err := h.svc.Verify(ctx, h.acc, pending.ID); err != nil {
		t.Fatal(err)
	}

	for addr, ok := range map[string]bool{
		"news@example.com":         true,
		"News@EXAMPLE.com":         true,
		"news@mail.example.com":    true,
		"news@notexample.com":      false,
		"news@pending.org":         false,
		"news@gmail.com":           false,
		"not-an-address":           false,
		"news@example.com.evil.io": false,
	} {
		err := h.svc.RequireVerifiedFrom(ctx, h.acc, addr)
		if ok && err != nil || !ok && !errors.Is(err, ErrFromNotVerified) {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if err := h.svc.RequireVerifiedFrom(ctx, h.acc2, "news@example.com"); !errors.Is(err, ErrFromNotVerified) {
		t.Errorf("another account's verified domain: %v", err)
	}
}

func TestParseTags(t *testing.T) {
	got, err := ParseTags("Environment=staging, Team = mail")
	if err != nil || got["Environment"] != "staging" || got["Team"] != "mail" {
		t.Errorf("ParseTags = %v, %v", got, err)
	}
	if got, err := ParseTags(""); err != nil || len(got) != 0 {
		t.Errorf("empty = %v, %v", got, err)
	}
	if _, err := ParseTags("Environment"); err == nil {
		t.Error("want an error for a tag without a value")
	}
}

func TestLocalDNSVerifiesEverything(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.svc.DNS = LocalDNS{Region: "us-east-2"}
	d, _ := h.svc.Add(ctx, h.acc, "example.com")
	d, _, err := h.svc.Verify(ctx, h.acc, d.ID)
	if err != nil || d.Status != StatusVerified {
		t.Errorf("local verify: %+v, %v", d, err)
	}
}
