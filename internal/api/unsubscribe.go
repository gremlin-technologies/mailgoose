package api

import (
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
)

// recipientPagesPerMinute limits the pages recipients reach from an email
// (unsubscribing) per IP. It's far above the API's limit because one-click
// unsubscribes arrive from a mail provider's few servers, for every
// recipient of a campaign at once.
const recipientPagesPerMinute = 1200

// maxRecipientFormBytes caps the body of a post to a recipient page.
const maxRecipientFormBytes = 4 << 10

// recipientPages serves the pages a campaign's recipients reach from an
// email, with no sign-in: the unsubscribe page and the RFC 8058 one-click
// endpoint, both at /u/{token}. They're HTML for people, not API
// operations, so they're outside the spec and skip the API's sign-in,
// cross-origin and idempotency layers; the token is the only authority.
func (s *Server) recipientPages() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+unsubscribe.PathPrefix+"{token}", s.unsubscribePage)
	mux.HandleFunc("POST "+unsubscribe.PathPrefix+"{token}", s.unsubscribePost)
	mux.HandleFunc(unsubscribe.PathPrefix, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			writePage(w, http.StatusMethodNotAllowed, page{Title: "This page can't do that", Body: []string{"Open the unsubscribe link from your email instead."}})
			return
		}
		writePage(w, http.StatusBadRequest, badLinkPage)
	})
	return mux
}

// isRecipientPage reports whether a path is one of recipientPages'.
func isRecipientPage(path string) bool {
	return strings.HasPrefix(path, unsubscribe.PathPrefix)
}

// logPath is a request's path for the logs, with any unsubscribe token
// left out: a token is enough to unsubscribe someone.
func logPath(path string) string {
	if isRecipientPage(path) && len(path) > len(unsubscribe.PathPrefix) {
		return unsubscribe.PathPrefix + "[token]"
	}
	return path
}

// testToken is the token in a test send's unsubscribe link
// (sending.TestUnsubscribePath). Real tokens start "u1".
const testToken = "test"

var testSendPage = page{
	Title: "This was a test email",
	Body: []string{
		"In the campaign itself, this link unsubscribes the person it's sent to, with one click.",
		"Nothing has been changed.",
	},
}

func (s *Server) unsubscribePage(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("token") == testToken {
		writePage(w, http.StatusOK, testSendPage)
		return
	}
	if s.Unsubscribe == nil {
		writePage(w, http.StatusServiceUnavailable, unavailablePage)
		return
	}
	t, err := s.Unsubscribe.Lookup(r.Context(), r.PathValue("token"))
	if err != nil {
		s.writeUnsubscribeError(w, r, err)
		return
	}
	if !t.Subscribed() {
		writePage(w, http.StatusOK, donePage(t))
		return
	}
	writePage(w, http.StatusOK, page{
		Title:  "Unsubscribe from " + t.AccountName + "?",
		Body:   []string{t.Email + " will stop getting emails from " + t.AccountName + "."},
		Button: "Unsubscribe",
		Small:  "Changed your mind? Just close this page.",
	})
}

// unsubscribePost unsubscribes: from the page's button, or from a mail
// app's one-click button (RFC 8058), which posts
// "List-Unsubscribe=One-Click" and reads only the status.
func (s *Server) unsubscribePost(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRecipientFormBytes)
	oneClick := false
	if err := r.ParseForm(); err == nil {
		oneClick = r.PostForm.Get("List-Unsubscribe") == "One-Click"
	}
	source := unsubscribe.SourceLink
	if oneClick {
		source = unsubscribe.SourceOneClick
	}
	if r.PathValue("token") == testToken {
		writePage(w, http.StatusOK, testSendPage)
		return
	}
	if s.Unsubscribe == nil {
		writePage(w, http.StatusServiceUnavailable, unavailablePage)
		return
	}
	t, _, err := s.Unsubscribe.Unsubscribe(r.Context(), r.PathValue("token"), source)
	if err != nil {
		s.writeUnsubscribeError(w, r, err)
		return
	}
	writePage(w, http.StatusOK, donePage(t))
}

func (s *Server) writeUnsubscribeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, unsubscribe.ErrBadToken):
		writePage(w, http.StatusBadRequest, badLinkPage)
	case errors.Is(err, unsubscribe.ErrNotFound):
		writePage(w, http.StatusNotFound, page{
			Title: "You're not on this list",
			Body: []string{
				"This address has already been removed, so there's nothing to unsubscribe from.",
				"Nothing has been changed.",
			},
		})
	default:
		s.Log.ErrorContext(r.Context(), "unsubscribe", "err", err)
		writePage(w, http.StatusInternalServerError, unavailablePage)
	}
}

func donePage(t *unsubscribe.Target) page {
	return page{
		Title: "You're unsubscribed",
		Body:  []string{t.Email + " won't get any more emails from " + t.AccountName + "."},
		Small: "Unsubscribed by mistake? Sign up again through " + t.AccountName + "'s signup form.",
	}
}

var badLinkPage = page{
	Title: "This unsubscribe link doesn't work",
	Body: []string{
		"It may have been cut short or changed on the way. Open the link straight from the email, or reply to the email and ask to be taken off the list.",
		"Nothing has been changed.",
	},
}

// recipientPageLimited answers a recipient who hit the limit with a page,
// not the API's JSON. A limited one-click POST is logged, because a mail
// provider's unsubscribe that never lands is a compliance problem.
func (s *Server) recipientPageLimited(w http.ResponseWriter, r *http.Request, retryAfter int) {
	if r.Method == http.MethodPost {
		s.Log.WarnContext(r.Context(), "unsubscribe post rate limited",
			"ip", ClientIP(r), "retry_after", retryAfter, "one_click", r.PostFormValue("List-Unsubscribe") == "One-Click")
	}
	writePage(w, http.StatusTooManyRequests, busyPage)
}

var busyPage = page{
	Title: "Too many tries just now",
	Body:  []string{"Please wait a minute and open the link again. Nothing has been changed."},
}

var unavailablePage = page{
	Title: "Unsubscribing isn't working right now",
	Body:  []string{"Please try the link again in a few minutes. Nothing has been changed."},
}

// page is what a recipient page shows. Button, when set, posts back to
// the page's own address.
type page struct {
	Title  string
	Body   []string
	Button string
	Small  string
}

// writePage writes a recipient page. The headers keep it out of caches,
// search results and frames, and keep the token in its address out of
// Referer headers.
func writePage(w http.ResponseWriter, status int, p page) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	_ = pageTemplate.Execute(w, p)
}

// The page uses the web app's tokens (web/src/styles.css), light and dark.
var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}}</title>
<style>
:root{--bg:#faf5ef;--surface:#fff;--text:#473e3b;--muted:#6b5f5a;--border:#ebddd2;--accent:#5562a3;--accent-hover:#47528a;--accent-text:#fff;--focus:#5562a3;--mark:#f68a3f}
@media (prefers-color-scheme:dark){:root{--bg:#1f1b1a;--surface:#2a2523;--text:#f4e3d2;--muted:#c2b2a6;--border:#3d3532;--accent:#9aa7e0;--accent-hover:#b3bdea;--accent-text:#1f1b1a;--focus:#9aa7e0}}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;display:grid;place-items:center;padding:24px 16px;background:var(--bg);color:var(--text);font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
main{width:100%;max-width:440px;background:var(--surface);border:1px solid var(--border);border-radius:16px;padding:32px 28px}
main::before{content:"";display:block;width:36px;height:4px;border-radius:2px;background:var(--mark);margin-bottom:20px}
h1{font-size:22px;line-height:1.3;margin:0 0 12px;overflow-wrap:anywhere}
p{margin:0 0 12px;overflow-wrap:anywhere}
.small{color:var(--muted);font-size:14px;margin-top:20px}
button{font:inherit;font-weight:600;width:100%;margin-top:12px;padding:12px 16px;border:0;border-radius:10px;background:var(--accent);color:var(--accent-text);cursor:pointer}
button:hover{background:var(--accent-hover)}
button:focus-visible{outline:3px solid var(--focus);outline-offset:2px}
footer{margin-top:20px;color:var(--muted);font-size:13px;text-align:center}
</style>
</head>
<body>
<div>
<main>
<h1>{{.Title}}</h1>
{{range .Body}}<p>{{.}}</p>
{{end}}{{if .Button}}<form method="post"><button type="submit">{{.Button}}</button></form>
{{end}}{{if .Small}}<p class="small">{{.Small}}</p>
{{end}}</main>
<footer>Sent with mailgoose</footer>
</div>
</body>
</html>
`))
