// Package emaillists holds the address lists the abuse checks use (MG-63,
// MG-66): disposable email domains and role addresses.
//
// disposable_domains.txt is the community list from
// https://github.com/disposable-email-domains/disposable-email-domains,
// released under CC0 1.0 (public domain), as packaged on npm by
// disposable-email-domains-js 2.2.0 (published 2026-11-17), vendored on
// 2026-11-19 with 10,299 domains. Refresh it from the same source; keep one
// lower-case domain per line.
//
// role_locals.txt is mailgoose's own list of local parts that name a role or
// a mailbox rather than a person (info@, sales@, admin@ ...), from the
// mailbox names RFC 2142 reserves plus the usual business ones. Lists of
// these are a sign of a scraped list: nobody signs up as info@.
package emaillists

import (
	_ "embed"
	"strings"
)

//go:embed disposable_domains.txt
var disposableText string

//go:embed role_locals.txt
var roleText string

var (
	disposable = lines(disposableText)
	role       = lines(roleText)
)

func lines(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" && !strings.HasPrefix(l, "#") {
			out[l] = struct{}{}
		}
	}
	return out
}

// Disposable reports whether domain, or a domain it's under, is a known
// disposable (throwaway) mail domain: x.mailinator.com counts as
// mailinator.com does.
func Disposable(domain string) bool {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	for d != "" && strings.Contains(d, ".") {
		if _, ok := disposable[d]; ok {
			return true
		}
		_, d, _ = strings.Cut(d, ".")
	}
	return false
}

// Role reports whether local, the part of an address before the @, names a
// role rather than a person. A +tag is ignored, and so are dots, dashes and
// underscores: "no.reply" and "no_reply" count as "noreply".
func Role(local string) bool {
	l := strings.ToLower(strings.TrimSpace(local))
	if i := strings.IndexByte(l, '+'); i >= 0 {
		l = l[:i]
	}
	if _, ok := role[l]; ok {
		return true
	}
	_, ok := role[strings.NewReplacer(".", "", "_", "", "-", "").Replace(l)]
	return ok
}

// Address splits a normalized address into its local part and domain; ok is
// false when there's no @.
func Address(email string) (local, domain string, ok bool) {
	i := strings.LastIndexByte(email, '@')
	if i < 1 || i == len(email)-1 {
		return "", "", false
	}
	return email[:i], email[i+1:], true
}

// Sizes are how many entries each list holds, for logs and docs.
func Sizes() (disposableDomains, roleLocals int) { return len(disposable), len(role) }
