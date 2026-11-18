package domains

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// Resolver looks DNS records up. *net.Resolver satisfies it.
type Resolver interface {
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// lookupTimeout bounds each lookup, so one slow name server can't hold a check.
const lookupTimeout = 5 * time.Second

func canonical(h string) string { return strings.ToLower(strings.TrimSuffix(h, ".")) }

func notFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

func lookupFailed(err error) RecordCheck {
	return RecordCheck{Status: Missing, Detail: "The lookup failed: " + err.Error()}
}

// checkRecords looks up each of d's records, in the order Records lists them.
func (s *Service) checkRecords(ctx context.Context, d *Domain) []RecordCheck {
	recs := s.Records(d)
	out := make([]RecordCheck, len(recs))
	for i, r := range recs {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		var c RecordCheck
		switch r.Purpose {
		case PurposeDKIM:
			c = s.checkCNAME(lctx, r)
		case PurposeMailFrom:
			c = s.checkMX(lctx, r)
		case PurposeSPF:
			c = s.checkSPF(lctx, r)
		case PurposeDMARC:
			c = s.checkDMARC(lctx, r)
		}
		cancel()
		c.Purpose, c.Name = r.Purpose, r.Name
		out[i] = c
	}
	return out
}

func (s *Service) checkCNAME(ctx context.Context, r Record) RecordCheck {
	got, err := s.DNS.LookupCNAME(ctx, r.Name)
	switch {
	case notFound(err):
		return RecordCheck{Status: Missing}
	case err != nil:
		return lookupFailed(err)
	}
	got = canonical(got)
	switch got {
	case r.Value:
		return RecordCheck{Status: Found, Found: []string{got}}
	case canonical(r.Name):
		// The name resolves, but not through a CNAME.
		return RecordCheck{Status: Wrong, Detail: "The name has a record, but not a CNAME to " + r.Value + "."}
	}
	return RecordCheck{Status: Wrong, Found: []string{got}, Detail: "The CNAME points to " + got + " instead of " + r.Value + "."}
}

func (s *Service) checkMX(ctx context.Context, r Record) RecordCheck {
	mxs, err := s.DNS.LookupMX(ctx, r.Name)
	switch {
	case notFound(err) || (err == nil && len(mxs) == 0):
		return RecordCheck{Status: Missing}
	case err != nil:
		return lookupFailed(err)
	}
	var found []string
	ok := false
	for _, mx := range mxs {
		h := canonical(mx.Host)
		found = append(found, strconv.Itoa(int(mx.Pref))+" "+h)
		if h == r.Value {
			ok = true
		}
	}
	if ok {
		return RecordCheck{Status: Found, Found: found}
	}
	return RecordCheck{Status: Wrong, Found: found, Detail: "No MX record points to " + r.Value + "."}
}

func (s *Service) checkSPF(ctx context.Context, r Record) RecordCheck {
	txts, err := s.DNS.LookupTXT(ctx, r.Name)
	switch {
	case notFound(err):
		return RecordCheck{Status: Missing}
	case err != nil:
		return lookupFailed(err)
	}
	var spf []string
	for _, t := range txts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=spf1") {
			spf = append(spf, t)
		}
	}
	switch {
	case len(spf) == 0:
		return RecordCheck{Status: Missing}
	case len(spf) > 1:
		return RecordCheck{Status: Wrong, Found: spf, Detail: "There is more than one SPF record; keep one."}
	case !strings.Contains(strings.ToLower(spf[0]), "include:amazonses.com"):
		return RecordCheck{Status: Wrong, Found: spf, Detail: "The SPF record doesn't include amazonses.com."}
	}
	return RecordCheck{Status: Found, Found: spf}
}

func (s *Service) checkDMARC(ctx context.Context, r Record) RecordCheck {
	txts, err := s.DNS.LookupTXT(ctx, r.Name)
	switch {
	case notFound(err):
		return RecordCheck{Status: Missing}
	case err != nil:
		return lookupFailed(err)
	}
	for _, t := range txts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=dmarc1") {
			return RecordCheck{Status: Found, Found: []string{t}}
		}
	}
	return RecordCheck{Status: Missing}
}

// hasDMARC reports whether name already has a DMARC policy: its own, or for
// a subdomain, its organizational domain's, which receivers fall back to.
func (s *Service) hasDMARC(ctx context.Context, name string) bool {
	names := []string{name}
	if org, err := publicsuffix.EffectiveTLDPlusOne(name); err == nil && org != name {
		names = append(names, org)
	}
	for _, n := range names {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		found := s.checkDMARC(lctx, Record{Name: "_dmarc." + n}).Status == Found
		cancel()
		if found {
			return true
		}
	}
	return false
}
