package emails

import (
	"net/url"
	"strings"
)

// TagLinkMessage says why a link that a merge tag could point anywhere is
// refused, and what to write instead.
const TagLinkMessage = "a merge tag can't decide where a link goes, because each contact's field would choose it and the link check never sees that; " +
	"only {{unsubscribe_url}} may be a whole link. Put other tags after a full address, such as https://shop.example/?name={{first_name}}"

// FixedLinkHost reports whether a link's destination is fixed before any
// merge tag is filled in. A link with no tag is fixed. A link that is
// exactly {{unsubscribe_url}} is ours. Otherwise a tag may only come in
// the path, query or fragment of a full http or https address, or in a
// mailto: address. A tag anywhere else, such as a whole link of
// {{first_name|https://shop.example}}, lets a contact's field choose where
// the link goes, so a phishing address imported as a field would go out
// without the link check (MG-64) ever seeing it.
func FixedLinkHost(href string) bool {
	i := strings.Index(href, "{{")
	if i < 0 || unsubscribeHref.MatchString(href) {
		return true
	}
	prefix := strings.TrimLeft(href[:i], " \t\r\n\f")
	if strings.HasPrefix(strings.ToLower(prefix), "mailto:") {
		return true
	}
	u, err := url.Parse(prefix)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return false
	}
	// The host has ended only once a /, ? or # follows it; before that, a
	// tag would carry on the host name (https://shop.example{{...}}).
	rest := prefix[strings.Index(prefix, "//")+2:]
	return strings.ContainsAny(rest, "/?#")
}
