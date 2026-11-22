package render

import (
	"html"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/brandassets"
)

// The "Sent by mailgoose" footer (MG-119): a small line under the compliance
// footer of every copy a free account sends, the wordmark linked to the
// sign-up page. A template always has a place for it, a slot, and each copy
// says whether to fill it (Recipient.Branded), so a free account that
// upgrades loses it from the next copy it renders, and one whose plan ends
// gets it back. It's added here, not in the document, so no editor, API
// call or CLI command can edit or remove it.

// BrandText is what the line says: the wordmark's alt text and the plain-text
// line's label.
const BrandText = "Sent by mailgoose"

// brandRef marks sign-ups that come from the footer, so they can be counted.
const brandRef = "ref=footer"

// brandSlot stands for the footer's place in rendered text until findTags
// has split the text into segments. A NUL can't be in an email's markup, so
// it can't clash with the customer's text; cleanSlot strips any that is.
const brandSlot = "\x00mg-brand\x00"

// SignupURL is the web app's sign-up page the footer links to, from the
// base URL the API is reached at (PUBLIC_BASE_URL). The link isn't click
// tracked (Track keeps it, see IsBrandLink).
func SignupURL(base string) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/signup?" + brandRef
}

// IsBrandLink reports whether href is the footer's link, which click
// tracking leaves alone so it doesn't count toward a campaign's clicks.
func IsBrandLink(base, href string) bool {
	url := SignupURL(base)
	return url != "" && href == url
}

// brandHTML is the line as a table row under a blocks email's footer; ""
// when there's nowhere to link to or load the wordmark from.
func brandHTML(base string) string {
	url, img := SignupURL(base), brandassets.WordmarkURL(base)
	if url == "" {
		return ""
	}
	return `<tr><td class="mg-pad" align="center" style="padding:0 ` + px(padX) + ` 24px;">` + brandLink(url, img) + "</td></tr>\n"
}

// brandHTMLFragment is the line for an html email: a centered block just
// before </body>.
func brandHTMLFragment(base string) string {
	url, img := SignupURL(base), brandassets.WordmarkURL(base)
	if url == "" {
		return ""
	}
	return `<div style="padding:0 16px 24px;text-align:center;">` + brandLink(url, img) + "</div>\n"
}

func brandLink(url, img string) string {
	return `<a href="` + html.EscapeString(url) + `" style="text-decoration:none;"><img src="` + html.EscapeString(img) +
		`" alt="` + BrandText + `" width="96" style="display:inline-block;width:96px;max-width:100%;height:auto;border:0;"></a>`
}

// brandText is the plain-text part's line.
func brandText(base string) string {
	if url := SignupURL(base); url != "" {
		return "\n" + BrandText + ": " + url + "\n"
	}
	return ""
}

// cleanSlot removes any NUL from the customer's markup, so none can look
// like the slot.
func cleanSlot(s string) string { return strings.ReplaceAll(s, "\x00", "") }

// splitSlot turns the slot marker in each literal segment into a slot
// segment of its own.
func splitSlot(p parts) parts {
	var out parts
	for _, s := range p {
		if s.tag != "" || !strings.Contains(s.text, brandSlot) {
			out = append(out, s)
			continue
		}
		pieces := strings.Split(s.text, brandSlot)
		for i, piece := range pieces {
			if i > 0 {
				out = append(out, segment{slot: true})
			}
			if piece != "" {
				out = append(out, segment{text: piece})
			}
		}
	}
	return out
}
