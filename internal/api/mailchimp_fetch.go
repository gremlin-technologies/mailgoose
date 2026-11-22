package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// Reading a connected Mailchimp account (MG-74): its audiences, and fetches
// of what the customer picks into a bundle like an uploaded export's.

var (
	errMailchimpNotConnected = Conflict("mailchimp_not_connected", "No Mailchimp account is connected, or the connection has expired.").
					WithHint("Connect one: `mailgoose import mailchimp connect`.")
	errMailchimpRevoked = Conflict("mailchimp_access_revoked", "Mailchimp no longer allows mailgoose to read that account, so the connection was forgotten.").
				WithHint("Connect again: `mailgoose import mailchimp connect`.")
	errMailchimpUnavailable = (&Error{Status: http.StatusBadGateway, Code: "mailchimp_unavailable",
		Message: "Mailchimp's API didn't answer just now."}).WithHint("Try again in a few minutes.")
)

var (
	mailchimpAudienceID = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)
	mailchimpSegmentID  = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// fetchCaller is the caller of a fetch endpoint, or nil after writing the
// error.
func (s *Server) fetchCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.mailchimpCaller(w, r)
	if c != nil && s.MailchimpConnections == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Connecting Mailchimp needs a database, and none is configured."})
		return nil
	}
	return c
}

// ListMailchimpAudiences is GET /v1/imports/mailchimp/audiences.
func (s *Server) ListMailchimpAudiences(w http.ResponseWriter, r *http.Request) {
	c := s.fetchCaller(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	client, err := s.MailchimpConnections.Client(ctx, c.AccountID)
	if s.mailchimpClientError(w, r, c.AccountID, err) {
		return
	}
	auds, err := client.Audiences(ctx)
	if s.mailchimpClientError(w, r, c.AccountID, err) {
		return
	}
	out := apigen.MailchimpAudienceList{Audiences: make([]apigen.MailchimpAccountAudience, len(auds))}
	for i, a := range auds {
		out.Audiences[i] = apigen.MailchimpAccountAudience{Id: a.ID, Name: a.Name, DoubleOptin: a.DoubleOptin}
		out.Audiences[i].Members.Subscribed, out.Audiences[i].Members.Unsubscribed = a.Subscribed, a.Unsubscribed
		out.Audiences[i].Members.Cleaned, out.Audiences[i].Members.Total = a.Cleaned, a.Total
	}
	writeJSON(w, http.StatusOK, out)
}

// mailchimpClientError writes the response for an error reading the
// connected account, and reports whether there was one. A revoked token is
// forgotten.
func (s *Server) mailchimpClientError(w http.ResponseWriter, r *http.Request, accountID string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, mailchimp.ErrNotConfigured):
		WriteError(w, errMailchimpNotConfigured)
	case errors.Is(err, mailchimp.ErrNotConnected):
		WriteError(w, errMailchimpNotConnected)
	case errors.Is(err, mailchimp.ErrAccessRevoked):
		if ferr := s.MailchimpConnections.Forget(context.WithoutCancel(r.Context()), accountID); ferr != nil {
			s.Log.ErrorContext(r.Context(), "forgetting a revoked Mailchimp connection failed", "err", ferr)
		}
		WriteError(w, errMailchimpRevoked)
	case r.Context().Err() != nil:
	default:
		s.Log.WarnContext(r.Context(), "reading a connected Mailchimp account failed", "err", err)
		WriteError(w, errMailchimpUnavailable)
	}
	return true
}

// CreateMailchimpFetch is POST /v1/imports/mailchimp/fetch.
func (s *Server) CreateMailchimpFetch(w http.ResponseWriter, r *http.Request) {
	c := s.fetchCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.CreateMailchimpFetchRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	in := mailchimp.FetchRequest{Campaigns: mailchimp.DefaultCampaigns}
	if req.Campaigns != nil {
		in.Campaigns = *req.Campaigns
	}
	if in.Campaigns < 0 || in.Campaigns > mailchimp.MaxCampaigns {
		WriteError(w, Unprocessable("invalid_campaigns", fmt.Sprintf("campaigns is 0 to %d.", mailchimp.MaxCampaigns)))
		return
	}
	var ok bool
	if in.AudienceIDs, ok = cleanIDs(req.AudienceIds, mailchimpAudienceID, mailchimp.MaxFetchAudiences); !ok || len(in.AudienceIDs) == 0 {
		WriteError(w, Unprocessable("invalid_audience_ids",
			fmt.Sprintf("audience_ids is 1 to %d audience ids from `listMailchimpAudiences`.", mailchimp.MaxFetchAudiences)).
			WithHint("`mailgoose import mailchimp audiences` lists them."))
		return
	}
	if req.SegmentIds != nil {
		if in.SegmentIDs, ok = cleanIDs(*req.SegmentIds, mailchimpSegmentID, mailchimp.MaxFetchSegments); !ok {
			WriteError(w, Unprocessable("invalid_segment_ids",
				fmt.Sprintf("segment_ids is up to %d Mailchimp saved segment ids, each a number.", mailchimp.MaxFetchSegments)))
			return
		}
	}
	ctx := r.Context()
	if !s.MailchimpConnections.Configured() {
		WriteError(w, errMailchimpNotConfigured)
		return
	}
	conn, err := s.MailchimpConnections.Get(ctx, c.AccountID)
	if s.mailchimpError(w, r, err) {
		return
	}
	if conn == nil {
		WriteError(w, errMailchimpNotConnected)
		return
	}
	if n, err := s.Mailchimp.Unconfirmed(ctx, c.AccountID); s.mailchimpError(w, r, err) {
		return
	} else if n >= mailchimp.MaxUnconfirmedUploads {
		s.mailchimpError(w, r, mailchimp.ErrTooManyUploads)
		return
	}
	f, err := s.Mailchimp.QueueFetch(ctx, c.AccountID, in)
	if errors.Is(err, mailchimp.ErrFetchRunning) {
		WriteError(w, Conflict("fetch_in_progress", "This account's earlier Mailchimp fetch hasn't finished.").
			WithHint("Check it with `mailgoose import mailchimp fetch-status <id>`, then fetch again."))
		return
	}
	if s.mailchimpError(w, r, err) {
		return
	}
	out, err := s.mailchimpFetchJSON(ctx, f)
	if s.mailchimpError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

// cleanIDs drops repeats and checks each id against pattern; ok is false
// for a bad id or more than limit.
func cleanIDs(ids []string, pattern *regexp.Regexp, limit int) (out []string, ok bool) {
	seen := map[string]bool{}
	out = []string{}
	for _, id := range ids {
		if !pattern.MatchString(id) {
			return nil, false
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, len(out) <= limit
}

// GetMailchimpFetch is GET /v1/imports/mailchimp/fetch/{fetch_id}.
func (s *Server) GetMailchimpFetch(w http.ResponseWriter, r *http.Request, id apigen.FetchId) {
	c := s.fetchCaller(w, r)
	if c == nil {
		return
	}
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "fetch_id must be a fetch's id."))
		return
	}
	f, err := s.Mailchimp.GetFetch(r.Context(), c.AccountID, id)
	if errors.Is(err, mailchimp.ErrFetchNotFound) {
		WriteError(w, NotFound("not_found", "No Mailchimp fetch with that id in this account; finished fetches are kept for 7 days."))
		return
	}
	if s.mailchimpError(w, r, err) {
		return
	}
	out, err := s.mailchimpFetchJSON(r.Context(), f)
	if s.mailchimpError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) mailchimpFetchJSON(ctx context.Context, f *mailchimp.Fetch) (apigen.MailchimpFetch, error) {
	out := apigen.MailchimpFetch{Id: f.ID, Status: apigen.MailchimpFetchStatus(f.Status), Step: f.Step, Audience: f.Audience,
		MembersRead: f.MembersRead, MembersTotal: f.MembersTotal, AudienceIds: f.AudienceIDs, SegmentIds: f.SegmentIDs,
		Campaigns: f.Campaigns, BundleId: f.BundleID, CreatedAt: f.CreatedAt.UTC(), FinishedAt: utcPtr(f.FinishedAt)}
	if f.ErrorCode != nil {
		out.Error = &struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: *f.ErrorCode, Message: deref(f.ErrorMessage)}
	}
	if f.BundleID != nil {
		u, err := s.Mailchimp.Get(ctx, f.AccountID, *f.BundleID)
		if errors.Is(err, mailchimp.ErrUploadNotFound) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		b, err := s.mailchimpBundleJSON(u)
		if err != nil {
			return out, err
		}
		out.Bundle = &b
	}
	return out, nil
}

// SaveFetched is the fetch job's mailchimp.SaveFetchedFunc: it reads the
// fetched bundle as an upload is read, works out its preview and stores it.
func (s *Server) SaveFetched(ctx context.Context, accountID string, f *mailchimp.Fetched) (string, error) {
	opts := mailchimp.Options{GroupColumns: f.GroupColumns, MaxMembers: s.MailchimpMaxMembers}
	release, err := mailchimp.HoldRead(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	b, err := mailchimp.Read(bytes.NewReader(f.Data), int64(len(f.Data)), mailchimp.FetchedFileName, opts)
	if err != nil {
		return "", err
	}
	p, err := s.mailchimpPreviewJSON(ctx, accountID, b, mailchimp.DefaultCampaigns)
	if err != nil {
		return "", err
	}
	p.Source = apigen.MailchimpPreviewSourceConnectedAccount
	for i, a := range p.Audiences {
		if v, ok := f.DoubleOptin[a.Name]; ok {
			p.Audiences[i].DoubleOptin = &v
		}
	}
	tags := make([]struct {
		Audience string `json:"audience"`
		Members  int    `json:"members"`
		Tag      string `json:"tag"`
	}, len(f.Segments))
	for i, t := range f.Segments {
		tags[i].Audience, tags[i].Tag, tags[i].Members = t.Audience, t.Tag, t.Members
	}
	p.SegmentTags = &tags
	p.TemplatesNotFetched = &f.Templates
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	u, err := s.Mailchimp.SaveFetched(ctx, accountID, f.Data, raw, opts)
	if err != nil {
		return "", err
	}
	return u.ID, nil
}
