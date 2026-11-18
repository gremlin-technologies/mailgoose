package api

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/signups"
)

// Signup forms post from people's own sites, so their limits are on the
// poster's IP and on the form, not on any credentials (FRM-5).
const (
	// signupsPerIPPerMinute is how many posts one IP may make to forms. A
	// person signs up once; a household or office behind one address a
	// few times.
	signupsPerIPPerMinute = 10
	// signupsPerFormPerMinute is how many posts one form takes from
	// everyone, so a form can't be used to flood the account's list.
	signupsPerFormPerMinute = 600
	// maxSignupBytes caps a form post's body.
	maxSignupBytes = 8 << 10
)

// signupPathPrefix is where forms post (forms.ActionPath).
const signupPathPrefix = "/f/"

// signupLimits holds the form endpoint's two limiters.
type signupLimits struct {
	ip, form *RateLimiter
}

func newSignupLimits(now func() time.Time) *signupLimits {
	return &signupLimits{
		ip:   &RateLimiter{PerMinute: signupsPerIPPerMinute, Now: now},
		form: &RateLimiter{PerMinute: signupsPerFormPerMinute, Now: now},
	}
}

// signupRoutes adds the form endpoint and the confirm pages to mux.
func (s *Server) signupRoutes(mux *http.ServeMux, now func() time.Time) {
	limits := newSignupLimits(now)
	mux.HandleFunc("OPTIONS "+signupPathPrefix+"{form_id}", signupPreflight)
	mux.HandleFunc("POST "+signupPathPrefix+"{form_id}", func(w http.ResponseWriter, r *http.Request) {
		s.signupPost(w, r, limits)
	})
	mux.HandleFunc(signupPathPrefix, func(w http.ResponseWriter, r *http.Request) {
		allowAnyOrigin(w)
		w.Header().Set("Allow", "POST, OPTIONS")
		writeSignupError(w, r, &Error{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed",
			Message: "Signup forms take posts only.", Hint: "Sign up through the form on the website you came from."})
	})

	mux.HandleFunc("GET "+signups.ConfirmPrefix+"{token}", s.confirmPage)
	mux.HandleFunc("POST "+signups.ConfirmPrefix+"{token}", s.confirmPost)
	mux.HandleFunc(signups.ConfirmPrefix, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, HEAD, POST")
			writePage(w, http.StatusMethodNotAllowed, page{Title: "This page can't do that", Body: []string{"Open the confirm link from your email instead."}})
			return
		}
		writePage(w, http.StatusBadRequest, badConfirmLinkPage)
	})
}

// allowAnyOrigin lets any website's page read the endpoint's answers. No
// credentials are involved, so this exposes nothing.
func allowAnyOrigin(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

func signupPreflight(w http.ResponseWriter, _ *http.Request) {
	allowAnyOrigin(w)
	h := w.Header()
	h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Accept, Content-Type")
	h.Set("Access-Control-Max-Age", "86400")
	w.WriteHeader(http.StatusNoContent)
}

// wantsJSON reports whether a form post wants a JSON answer (the embed
// script, or an agent) rather than a page or a redirect (a plain form).
func wantsJSON(r *http.Request) bool {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		return true
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return ct == "application/json"
}

// formPost is a form post's fields, from a form or from JSON.
type formPost struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Honeypot  string `json:"website"`
	Stamp     string `json:"_mg_ts"`
	ElapsedMS any    `json:"_mg_ms"`
}

func readFormPost(r *http.Request) (*formPost, error) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var b formPost
	if ct == "application/json" {
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			return nil, err
		}
		return &b, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	v := r.PostForm
	b = formPost{
		Email:     v.Get(forms.FieldEmail),
		FirstName: v.Get(forms.FieldFirstName),
		LastName:  v.Get(forms.FieldLastName),
		Honeypot:  v.Get(forms.HoneypotField),
		Stamp:     v.Get(forms.StampField),
	}
	if ms := v.Get(forms.ElapsedField); ms != "" {
		b.ElapsedMS = ms
	}
	return &b, nil
}

// elapsed is how long the page was open, when the post says.
func (b *formPost) elapsed() *time.Duration {
	var ms float64
	switch v := b.ElapsedMS.(type) {
	case float64:
		ms = v
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil
		}
		ms = n
	default:
		return nil
	}
	d := time.Duration(ms * float64(time.Millisecond))
	return &d
}

// signupPost takes a post to a form's public endpoint (FRM-3 to FRM-5).
// Whatever happened to the address, the person gets the form's success
// message or redirect, so a form can't be used to see who's on a list.
func (s *Server) signupPost(w http.ResponseWriter, r *http.Request, limits *signupLimits) {
	allowAnyOrigin(w)
	formID := r.PathValue("form_id")
	if !uuidPattern.MatchString(formID) {
		writeSignupError(w, r, formNotFound)
		return
	}
	if ok, _, wait := limits.ip.allow(ClientIP(r)); !ok {
		writeSignupThrottled(w, r, wait)
		return
	}
	if ok, _, wait := limits.form.allow(formID); !ok {
		writeSignupThrottled(w, r, wait)
		return
	}
	if s.Signups == nil {
		writeSignupError(w, r, &Error{Status: http.StatusServiceUnavailable, Code: "unavailable",
			Message: "Signing up isn't working right now. Please try again in a few minutes."})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSignupBytes)
	b, err := readFormPost(r)
	if err != nil {
		writeSignupError(w, r, BadRequest("malformed_signup", "The signup couldn't be read.").
			WithHint("Post the form's fields as a form or as JSON."))
		return
	}
	res, err := s.Signups.Submit(r.Context(), formID, signups.Submission{
		Email: b.Email, FirstName: b.FirstName, LastName: b.LastName,
		Honeypot: b.Honeypot, Stamp: b.Stamp, Elapsed: b.elapsed(), IP: ClientIP(r),
	}, s.webURL(r))
	if err != nil {
		s.writeSubmitError(w, r, err)
		return
	}
	f := res.Form
	if f.RedirectURL != nil && *f.RedirectURL != "" {
		if wantsJSON(r) {
			writeJSON(w, http.StatusOK, map[string]string{"redirect_url": *f.RedirectURL})
			return
		}
		http.Redirect(w, r, *f.RedirectURL, http.StatusSeeOther) //nolint:gosec // the form's own https redirect, set by its owner, never from the post
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]string{"message": f.SuccessMessage})
		return
	}
	writePage(w, http.StatusOK, page{Title: "Thanks!", Body: []string{f.SuccessMessage}, Back: backURL(r)})
}

var formNotFound = NotFound("form_not_found", "This signup form doesn't exist any more.")

func (s *Server) writeSubmitError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *signups.InvalidError
	var limit *contacts.LimitError
	var send *signups.SendError
	switch {
	case errors.Is(err, signups.ErrFormNotFound):
		writeSignupError(w, r, formNotFound)
	case errors.Is(err, signups.ErrBadStamp):
		writeSignupError(w, r, BadRequest("form_code_incomplete", "This form's code is incomplete, so it can't take signups.").
			WithHint("The site's owner should paste the form's embed code again, from `mailgoose form embed <form-id>`."))
	case errors.As(err, &invalid):
		writeSignupError(w, r, Unprocessable("invalid_signup", invalid.Message).
			WithDetails(map[string]any{"field": invalid.Field}))
	case errors.Is(err, signups.ErrNotReady):
		writeSignupError(w, r, &Error{Status: http.StatusServiceUnavailable, Code: "form_not_ready",
			Message: "This form isn't taking signups yet.",
			Hint:    "The form's owner needs a verified sending domain to send confirmation emails from; `mailgoose form get <form-id>` says what's missing."})
	case errors.As(err, &limit):
		writeSignupError(w, r, Conflict("list_full", "This list isn't taking new signups right now.").
			WithHint("The account is at its contact limit."))
	case errors.As(err, &send):
		s.Log.ErrorContext(r.Context(), "send a confirmation email", "err", err)
		writeSignupError(w, r, &Error{Status: http.StatusBadGateway, Code: "confirmation_failed",
			Message: "We couldn't send the confirmation email. Please try again in a few minutes."})
	default:
		s.Log.ErrorContext(r.Context(), "form signup", "err", err)
		writeSignupError(w, r, Internal())
	}
}

func writeSignupThrottled(w http.ResponseWriter, r *http.Request, wait time.Duration) {
	secs := int(wait.Seconds() + 0.999)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeSignupError(w, r, TooManyRequests("rate_limited", "Too many signups from here. Please wait a minute and try again."))
}

// writeSignupError answers a form post: the API's error body for JSON, a
// page otherwise.
func writeSignupError(w http.ResponseWriter, r *http.Request, e *Error) {
	if wantsJSON(r) {
		WriteError(w, e)
		return
	}
	title := "That didn't work"
	switch e.Status {
	case http.StatusUnprocessableEntity:
		title = "Please check what you entered"
	case http.StatusTooManyRequests:
		title = "Please wait a minute"
	}
	writePage(w, e.Status, page{Title: title, Body: []string{e.Message}, Back: backURL(r)})
}

// backURL is the page the form was on, for a link back to it. It's the
// Referer, when the poster's browser sent one and it's a web address.
func backURL(r *http.Request) string {
	u, err := url.Parse(r.Header.Get("Referer"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// confirmPage shows a confirm link's page. Opening it changes nothing,
// because mail scanners open links: the button confirms.
func (s *Server) confirmPage(w http.ResponseWriter, r *http.Request) {
	if s.Signups == nil {
		writePage(w, http.StatusServiceUnavailable, confirmUnavailablePage)
		return
	}
	p, err := s.Signups.Lookup(r.Context(), r.PathValue("token"))
	if err != nil {
		s.writeConfirmError(w, r, err)
		return
	}
	if p.Confirmed {
		writePage(w, http.StatusOK, confirmedPage(p))
		return
	}
	writePage(w, http.StatusOK, page{
		Title:  "Confirm your subscription to " + p.AccountName,
		Body:   []string{p.Email + " will get emails from " + p.AccountName + "."},
		Button: "Confirm my subscription",
		Small:  "Didn't sign up? Just close this page and nothing will happen.",
	})
}

func (s *Server) confirmPost(w http.ResponseWriter, r *http.Request) {
	if s.Signups == nil {
		writePage(w, http.StatusServiceUnavailable, confirmUnavailablePage)
		return
	}
	p, err := s.Signups.Confirm(r.Context(), r.PathValue("token"))
	if err != nil {
		s.writeConfirmError(w, r, err)
		return
	}
	writePage(w, http.StatusOK, confirmedPage(p))
}

func (s *Server) writeConfirmError(w http.ResponseWriter, r *http.Request, err error) {
	var limit *contacts.LimitError
	switch {
	case errors.Is(err, signups.ErrBadToken):
		writePage(w, http.StatusBadRequest, badConfirmLinkPage)
	case errors.Is(err, signups.ErrNotFound):
		writePage(w, http.StatusNotFound, page{
			Title: "This signup isn't waiting any more",
			Body: []string{
				"The address may have been removed from the list since, or a newer confirmation email replaced this one.",
				"Nothing has been changed.",
			},
		})
	case errors.Is(err, signups.ErrExpired):
		writePage(w, http.StatusGone, page{
			Title: "This confirm link has expired",
			Body: []string{
				"Confirm links work for 7 days. Sign up again through the form and we'll send a new one.",
				"Nothing has been changed.",
			},
		})
	case errors.As(err, &limit):
		writePage(w, http.StatusConflict, page{
			Title: "This list isn't taking new subscribers right now",
			Body:  []string{"Please try the link again later. Nothing has been changed."},
		})
	default:
		s.Log.ErrorContext(r.Context(), "confirm a signup", "err", err)
		writePage(w, http.StatusInternalServerError, confirmUnavailablePage)
	}
}

func confirmedPage(p *signups.Pending) page {
	return page{
		Title: "You're subscribed",
		Body:  []string{p.Email + " will get emails from " + p.AccountName + "."},
		Small: "Every email has a link to unsubscribe.",
	}
}

var badConfirmLinkPage = page{
	Title: "This confirm link doesn't work",
	Body: []string{
		"It may have been cut short or changed on the way. Open the link straight from the email.",
		"Nothing has been changed.",
	},
}

var confirmUnavailablePage = page{
	Title: "Confirming isn't working right now",
	Body:  []string{"Please try the link again in a few minutes. Nothing has been changed."},
}
