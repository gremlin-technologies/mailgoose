package mailchimp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/images"
)

// Emails from an export (MG-71): every template, and the newest campaigns.

// How many past campaigns come over as emails.
const (
	// DefaultCampaigns is how many of the newest campaigns come over when
	// the customer doesn't say.
	DefaultCampaigns = 10
	// MaxCampaigns is the most one import brings.
	MaxCampaigns = 100
)

// Where an imported email came from.
const (
	EmailFromTemplate = "template"
	EmailFromCampaign = "campaign"
)

// Note codes for what the email checks find in a converted email: an error
// that stops it being sent until it's fixed, or a warning.
const (
	NoteInvalidHTML = "invalid_html"
	NoteHTMLWarning = "html_warning"
	NoteTooLarge    = "too_large"
)

// EmailPlan is one email an import would make.
type EmailPlan struct {
	// From is EmailFromTemplate or EmailFromCampaign, and File the export
	// file it came from.
	From string `json:"from"`
	File string `json:"file"`
	// Name, Subject and PreviewText are the new email's.
	Name        string `json:"name"`
	Subject     string `json:"subject"`
	PreviewText string `json:"preview_text"`
	// Errors keep the email from being sent until they're fixed; it's made
	// anyway. Warnings are things to look at.
	Errors   []Note `json:"errors"`
	Warnings []Note `json:"warnings"`
	// Images counts the Mailchimp-hosted images to copy.
	Images int `json:"images"`

	conv *Conversion
}

// ImportedEmail is an email an import made.
type ImportedEmail struct {
	EmailPlan
	EmailID string `json:"email_id"`
}

// PlanEmails converts a bundle's templates and its newest campaigns
// (campaigns of them, 0 for DefaultCampaigns, at most MaxCampaigns), without
// fetching or writing anything. Copying images can still add a warning.
func PlanEmails(b *Bundle, campaigns int) []EmailPlan {
	var out []EmailPlan
	for _, t := range b.Templates {
		out = append(out, plan(EmailFromTemplate, t.Path, emailName(t.Name, "", "", "Mailchimp template"), t.HTML, ""))
	}
	for _, c := range NewestCampaigns(b, campaigns) {
		out = append(out, plan(EmailFromCampaign, c.Path, emailName(c.Title, c.Subject, c.Name, "Mailchimp campaign"), c.HTML, c.Subject))
	}
	return out
}

func plan(from, file, name, src, subject string) EmailPlan {
	// A stored email must be valid UTF-8; fix it first, so the size and
	// checks are of what's stored.
	conv := ConvertHTML(strings.ToValidUTF8(src, "\uFFFD"), strings.ToValidUTF8(subject, "\uFFFD"), "")
	p := EmailPlan{From: from, File: file, Name: name, Subject: clipRunes(conv.Subject, 200),
		PreviewText: clipRunes(conv.PreviewText, 200), Images: len(conv.Images), conv: conv}
	p.Errors = slices.Clone(conv.Errors)
	p.Warnings = slices.Clone(conv.Warnings)
	p.check(conv.HTML)
	return p
}

// check adds what the email checks find in h. A size over what an email can
// store is an error of its own.
func (p *EmailPlan) check(h string) {
	if len(h) > emails.MaxHTMLBytes {
		p.Errors = append(p.Errors, Note{Code: NoteTooLarge, Message: fmt.Sprintf(
			"the HTML is over %d KB, more than an email holds, so the email was made empty; paste a smaller version into it", emails.MaxHTMLBytes/1000)})
		return
	}
	errs, warns := emails.CheckHTML(h)
	conditional := slices.ContainsFunc(p.Errors, func(n Note) bool { return n.Code == NoteConditional })
	for _, e := range errs {
		// The conversion's own note already explains conditionals.
		if conditional && e.Code == emails.HTMLMailchimpTag {
			continue
		}
		p.Errors = append(p.Errors, Note{Code: NoteInvalidHTML, Line: e.Line, Message: e.Message})
	}
	for _, w := range warns {
		p.Warnings = append(p.Warnings, Note{Code: NoteHTMLWarning, Line: w.Line, Message: w.Message})
	}
}

// NewestCampaigns returns up to n of the bundle's campaigns, newest first:
// those with a send time by it, then the rest in reverse file order. n is
// DefaultCampaigns when 0 or less, and at most MaxCampaigns.
func NewestCampaigns(b *Bundle, n int) []*Campaign {
	if n <= 0 {
		n = DefaultCampaigns
	}
	n = min(n, MaxCampaigns)
	cs := slices.Clone(b.Campaigns)
	slices.Reverse(cs)
	slices.SortStableFunc(cs, func(x, y *Campaign) int {
		switch {
		case x.SentAt != nil && y.SentAt != nil:
			return y.SentAt.Compare(*x.SentAt)
		case x.SentAt != nil:
			return -1
		case y.SentAt != nil:
			return 1
		}
		return 0
	})
	return cs[:min(n, len(cs))]
}

// emailName is the first of names that isn't blank, as an email name: one
// line of at most emails.MaxNameChars characters.
func emailName(names ...string) string {
	for _, n := range names {
		if n = strings.Join(strings.Fields(n), " "); n != "" {
			return clipRunes(n, emails.MaxNameChars)
		}
	}
	return ""
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// Fetcher gets an image's bytes.
type Fetcher func(ctx context.Context, rawURL string) ([]byte, error)

// EmailImporter makes the emails of a bundle.
type EmailImporter struct {
	Emails *emails.Service
	Images *images.Service
	// Fetch gets a Mailchimp-hosted image; nil means FetchImage.
	Fetch Fetcher
}

// Import makes the emails PlanEmails lists, copying their Mailchimp-hosted
// images into the account's image storage. Each email is made even when its
// checks fail, with the errors listed; only a failure to store one stops the
// import.
func (im *EmailImporter) Import(ctx context.Context, accountID string, b *Bundle, campaigns int) ([]ImportedEmail, error) {
	fetch := im.Fetch
	if fetch == nil {
		fetch = FetchImage
	}
	copied := map[string]string{} // image address as written -> new address
	failed := map[string]error{}
	var out []ImportedEmail
	for _, p := range PlanEmails(b, campaigns) {
		h := p.conv.HTML
		// The plan's checks were on the HTML before relinking; check again.
		p.Errors, p.Warnings = slices.Clone(p.conv.Errors), slices.Clone(p.conv.Warnings)
		for _, src := range p.conv.Images {
			if _, done := copied[src]; !done && failed[src] == nil {
				if u, err := im.copyImage(ctx, accountID, src, fetch); err != nil {
					failed[src] = err
				} else {
					copied[src] = u
				}
			}
			if _, ok := copied[src]; !ok {
				p.Warnings = append(p.Warnings, Note{Code: NoteImageNotCopied, Message: fmt.Sprintf(
					"the image %s couldn't be copied (%s), so it still loads from Mailchimp; upload it and change its link before you leave Mailchimp",
					shortURL(src), failed[src])})
			}
		}
		h = relink(h, copied)
		p.check(h)
		doc := emails.NewDocument(emails.KindHTML)
		if len(h) <= emails.MaxHTMLBytes {
			doc.HTML = &h
		}
		doc.Settings.Subject, doc.Settings.PreviewText = p.Subject, p.PreviewText
		// The stored document has to read back; make sure it does.
		raw, err := json.Marshal(doc)
		if err != nil {
			return out, err
		}
		if doc, err = emails.ParseDocument(emails.KindHTML, raw); err != nil {
			return out, fmt.Errorf("email from %s: %w", p.File, err)
		}
		e, err := im.Emails.Create(ctx, accountID, p.Name, emails.KindHTML, doc)
		if err != nil {
			return out, fmt.Errorf("make email from %s: %w", p.File, err)
		}
		p.conv = nil
		out = append(out, ImportedEmail{EmailPlan: p, EmailID: e.ID})
	}
	return out, nil
}

// relink points each image address in h that copied has at its copy. Only
// whole addresses are replaced, so one that starts another is left alone.
func relink(h string, copied map[string]string) string {
	var b strings.Builder
	last := 0
	for _, m := range imageRef.FindAllStringSubmatchIndex(h, -1) {
		u, ok := copied[h[m[2]:m[3]]]
		if !ok {
			continue
		}
		b.WriteString(h[last:m[2]])
		b.WriteString(html.EscapeString(u))
		last = m[3]
	}
	b.WriteString(h[last:])
	return b.String()
}

func (im *EmailImporter) copyImage(ctx context.Context, accountID, src string, fetch Fetcher) (string, error) {
	raw := html.UnescapeString(src)
	data, err := fetch(ctx, raw)
	if err != nil {
		return "", err
	}
	name := ""
	if u, err := url.Parse(raw); err == nil {
		name = path.Base(u.Path)
	}
	img, err := im.Images.Upload(ctx, accountID, name, data)
	var unsupported *images.UnsupportedError
	switch {
	case errors.Is(err, images.ErrTooMany):
		return "", errors.New("the account has as many images as it can keep")
	case errors.Is(err, images.ErrTooLarge), errors.Is(err, images.ErrEmpty), errors.As(err, &unsupported):
		return "", err
	case err != nil:
		return "", fmt.Errorf("store image: %w", err)
	}
	return img.URL, nil
}

// shortURL is an image address short enough for a note.
func shortURL(s string) string {
	s = html.UnescapeString(s)
	if u, err := url.Parse(s); err == nil {
		u.RawQuery, u.Fragment = "", ""
		s = u.String()
	}
	return clipRunes(s, 120)
}

// Fetching images. Only https addresses on Mailchimp's image hosts are
// fetched, only from public addresses, with no redirect off those hosts.
const (
	fetchTimeout   = 15 * time.Second
	fetchRedirects = 3
)

var imageClient = &http.Client{
	Timeout: fetchTimeout,
	Transport: &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout: fetchTimeout,
			// Control runs on the resolved address, so no lookup or
			// redirect gets around it.
			Control: func(_, address string, _ syscall.RawConn) error {
				ap, err := netip.ParseAddrPort(address)
				if err != nil || !emailcheck.PublicAddress(ap.Addr()) || ap.Port() != 443 {
					return errors.New("not a public https address")
				}
				return nil
			},
		}).DialContext,
		TLSHandshakeTimeout:   fetchTimeout,
		ResponseHeaderTimeout: fetchTimeout,
		MaxIdleConnsPerHost:   4,
	},
	CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > fetchRedirects {
			return errors.New("too many redirects")
		}
		return allowedImageURL(r.URL)
	},
}

func allowedImageURL(u *url.URL) error {
	if u.Scheme != "https" || !ImageHost(u.Hostname()) || (u.Port() != "" && u.Port() != "443") {
		return errors.New("not an https address on a Mailchimp image host")
	}
	return nil
}

// FetchImage gets a Mailchimp-hosted image, at most images.MaxBytes.
func FetchImage(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("not a valid address")
	}
	if err := allowedImageURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := imageClient.Do(req)
	if err != nil {
		return nil, errors.New("it couldn't be fetched")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the image host answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, images.MaxBytes+1))
	if err != nil {
		return nil, errors.New("it couldn't be fetched")
	}
	if len(data) > images.MaxBytes {
		return nil, images.ErrTooLarge
	}
	return data, nil
}
