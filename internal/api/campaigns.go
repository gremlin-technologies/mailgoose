package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/reports"
)

// campaignsCaller is requireCaller for the campaign endpoints, which also
// need the campaigns and contacts services.
func (s *Server) campaignsCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && (s.Campaigns == nil || s.Contacts == nil) {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Campaigns need a database, and none is configured."})
		return nil
	}
	return c
}

// campaignError writes the response for a campaigns error, and reports
// whether there was one.
func (s *Server) campaignError(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv *campaigns.InvalidError
	var st *campaigns.StatusError
	var ver *campaigns.VersionError
	switch {
	case err == nil:
		return false
	case errors.As(err, &inv):
		WriteError(w, Unprocessable("invalid_campaign", "The campaign can't be saved: "+inv.Message+".").
			WithDetails(map[string]any{"field": inv.Field}))
	case errors.Is(err, campaigns.ErrUnknownEmail):
		WriteError(w, Unprocessable("unknown_email", "The email_id isn't one of this account's emails.").
			WithHint("`mailgoose email list` shows the account's emails."))
	case errors.Is(err, campaigns.ErrUnknownTag):
		WriteError(w, Unprocessable("unknown_tag", "A tag id isn't one of this account's tags.").
			WithHint("`mailgoose tags list` shows the account's tags."))
	case errors.As(err, &st):
		hint := "Only drafts can be changed. A launched campaign was confirmed as it is."
		if r.Method == http.MethodDelete {
			hint = "Only drafts can be deleted; a scheduled campaign can be cancelled instead."
		}
		WriteError(w, Conflict("campaign_locked", "The campaign is "+st.Status+", so it can't be changed that way.").
			WithHint(hint).WithDetails(map[string]any{"status": st.Status}))
	case errors.As(err, &ver):
		WriteError(w, Conflict("version_conflict", "The campaign was changed since that version was read, so nothing changed.").
			WithHint("Get the campaign again and redo the change on the current version.").
			WithDetails(map[string]any{"current_version": ver.Current}))
	case errors.Is(err, campaigns.ErrNotFound):
		WriteError(w, NotFound("not_found", "No campaign with that id in this account.").
			WithHint("`mailgoose campaign list` shows the account's campaigns."))
	default:
		s.Log.ErrorContext(r.Context(), "campaign request failed", "err", err)
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "The database didn't answer.", Hint: "Try again in a minute."})
	}
	return true
}

func validCampaignID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "campaign_id must be a campaign's id."))
		return false
	}
	return true
}

func campaignJSON(c *campaigns.Campaign) apigen.Campaign {
	out := apigen.Campaign{Id: c.ID, Name: c.Name, EmailId: c.EmailID, Status: apigen.CampaignStatus(c.Status),
		Tracking: c.Tracking, Version: c.Version, CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
		Recipients: apigen.CampaignRecipients{All: c.AllContacts, Tags: make([]apigen.TagRef, len(c.Tags))}}
	if c.SendAt != nil {
		at := c.SendAt.UTC()
		out.SendAt = &at
	}
	for i, t := range c.Tags {
		out.Recipients.Tags[i] = apigen.TagRef{Id: t.ID, Name: t.Name}
	}
	if t := c.LastTest; t != nil {
		out.LastTest = &apigen.CampaignTest{At: t.At.UTC(), Version: t.Version, EmailVersion: t.EmailVersion}
	}
	if c.LaunchedAt != nil {
		l := &apigen.CampaignLaunch{At: c.LaunchedAt.UTC()}
		if c.RecipientTotal != nil {
			l.RecipientCount = *c.RecipientTotal
		}
		if c.CreditsReserved != nil {
			l.CreditsReserved = *c.CreditsReserved
		}
		out.Launch = l
	}
	if sl := c.Slice; sl != nil {
		out.Slice = &apigen.CampaignSlice{State: apigen.CampaignSliceState(sl.State), Size: sl.Size}
		if sl.ReleaseAt != nil {
			at := sl.ReleaseAt.UTC()
			out.Slice.ReleaseAt = &at
		}
	}
	return out
}

// withCount adds the campaign's recipient count, counted now.
func (s *Server) withCount(r *http.Request, c *campaigns.Campaign) (apigen.Campaign, error) {
	out := campaignJSON(c)
	n, err := s.Contacts.CountRecipients(r.Context(), c.AccountID, c.Audience())
	out.RecipientCount = &n
	return out, err
}

// recipientsInput is RecipientsInput, read so that `all: false` with no tags
// is caught by the service.
func recipientsInput(in *apigen.RecipientsInput) *campaigns.Recipients {
	if in == nil {
		return nil
	}
	out := &campaigns.Recipients{All: in.All != nil && *in.All}
	if in.TagIds != nil {
		out.TagIDs = *in.TagIds
	}
	return out
}

// campaignBody is a create or update request. send_at is kept raw to tell
// null (send when launched) from left out.
type campaignBody struct {
	Name       *string                 `json:"name"`
	EmailID    *string                 `json:"email_id"`
	Recipients *apigen.RecipientsInput `json:"recipients"`
	SendAt     json.RawMessage         `json:"send_at"`
	Tracking   *bool                   `json:"tracking"`
	Version    *int                    `json:"version"`
}

func (b *campaignBody) changes() (campaigns.Changes, *Error) {
	ch := campaigns.Changes{Name: b.Name, EmailID: b.EmailID, Recipients: recipientsInput(b.Recipients),
		Tracking: b.Tracking, Version: b.Version}
	switch {
	case len(b.SendAt) == 0:
	case string(b.SendAt) == "null":
		ch.SendNow = true
	default:
		var at time.Time
		if err := json.Unmarshal(b.SendAt, &at); err != nil {
			return ch, BadRequest("invalid_json", "send_at must be a date-time like 2026-12-01T09:00:00Z, or null.")
		}
		ch.SendAt = &at
	}
	return ch, nil
}

func campaignCursor(c *campaigns.Campaign) string {
	return c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
}

// ListCampaigns is GET /v1/campaigns.
func (s *Server) ListCampaigns(w http.ResponseWriter, r *http.Request, params apigen.ListCampaignsParams) {
	c := s.campaignsCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	status := ""
	if params.Status != nil {
		if !params.Status.Valid() {
			WriteError(w, BadRequest("invalid_parameter", "status must be one of draft, scheduled, sending, sent, cancelled, paused or failed."))
			return
		}
		status = string(*params.Status)
	}
	var after *campaigns.Cursor
	if pr.After != "" {
		cur, ok := parseContactCursor(pr.After)
		if !ok {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
		after = &campaigns.Cursor{CreatedAt: cur.CreatedAt, ID: cur.ID}
	}
	cs, err := s.Campaigns.List(r.Context(), c.AccountID, status, after, pr.Limit+1)
	if s.campaignError(w, r, err) {
		return
	}
	page := NewPage(cs, pr.Limit, campaignCursor)
	// Each launched campaign's summary, for the whole page in one query
	// (MG-111).
	summaries := map[string]*reports.Summary{}
	if s.Reports != nil {
		ids := make([]string, 0, len(page.Data))
		for _, cp := range page.Data {
			if cp.LaunchedAt != nil {
				ids = append(ids, cp.ID)
			}
		}
		if summaries, err = s.Reports.Summaries(r.Context(), c.AccountID, ids); s.campaignError(w, r, err) {
			return
		}
	}
	out := campaignListJSON{Data: make([]campaignListItem, len(page.Data)), NextCursor: page.NextCursor}
	for i, cp := range page.Data {
		out.Data[i] = campaignListItem{Campaign: campaignJSON(cp), Summary: summaries[cp.ID]}
	}
	writeJSON(w, http.StatusOK, out)
}

// campaignListItem is apigen.CampaignListItem: a campaign with its
// report's summary, null before launch.
type campaignListItem struct {
	apigen.Campaign
	Summary *reports.Summary `json:"summary"`
}

type campaignListJSON struct {
	Data       []campaignListItem `json:"data"`
	NextCursor *string            `json:"next_cursor"`
}

// CreateCampaign is POST /v1/campaigns.
func (s *Server) CreateCampaign(w http.ResponseWriter, r *http.Request) {
	c := s.campaignsCaller(w, r)
	if c == nil {
		return
	}
	var req campaignBody
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Version != nil {
		WriteError(w, BadRequest("invalid_json", "A new campaign takes no version."))
		return
	}
	ch, bad := req.changes()
	if bad != nil {
		WriteError(w, bad)
		return
	}
	if ch.Name == nil {
		ch.Name = new(string)
	}
	cp, err := s.Campaigns.Create(r.Context(), c.AccountID, ch)
	if s.campaignError(w, r, err) {
		return
	}
	out, err := s.withCount(r, cp)
	if s.campaignError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// CountCampaignRecipients is POST /v1/campaigns/recipient-count.
func (s *Server) CountCampaignRecipients(w http.ResponseWriter, r *http.Request) {
	c := s.campaignsCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.RecipientCountRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	rc := recipientsInput(&req.Recipients)
	switch {
	case rc.All && len(rc.TagIDs) > 0:
		WriteError(w, Unprocessable("invalid_campaign", "The campaign can't be saved: pick all contacts or tags, not both.").
			WithDetails(map[string]any{"field": "recipients"}))
		return
	case !rc.All && len(rc.TagIDs) == 0:
		WriteError(w, Unprocessable("invalid_campaign", "The campaign can't be saved: pick all contacts, or at least one tag.").
			WithDetails(map[string]any{"field": "recipients"}))
		return
	}
	for _, id := range rc.TagIDs {
		if !uuidPattern.MatchString(id) {
			s.campaignError(w, r, campaigns.ErrUnknownTag)
			return
		}
	}
	if !rc.All {
		known, err := s.Contacts.TagsExist(r.Context(), c.AccountID, rc.TagIDs)
		if s.campaignError(w, r, err) {
			return
		}
		if !known {
			s.campaignError(w, r, campaigns.ErrUnknownTag)
			return
		}
	}
	n, err := s.Contacts.CountRecipients(r.Context(), c.AccountID, contacts.Audience{All: rc.All, TagIDs: rc.TagIDs})
	if s.campaignError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.RecipientCount{RecipientCount: n})
}

// GetCampaign is GET /v1/campaigns/{campaign_id}.
func (s *Server) GetCampaign(w http.ResponseWriter, r *http.Request, id string) {
	c := s.campaignsCaller(w, r)
	if c == nil || !validCampaignID(w, id) {
		return
	}
	cp, err := s.Campaigns.Get(r.Context(), c.AccountID, id)
	if s.campaignError(w, r, err) {
		return
	}
	out, err := s.withCount(r, cp)
	if s.campaignError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// UpdateCampaign is PATCH /v1/campaigns/{campaign_id}.
func (s *Server) UpdateCampaign(w http.ResponseWriter, r *http.Request, id string) {
	c := s.campaignsCaller(w, r)
	if c == nil || !validCampaignID(w, id) {
		return
	}
	var req campaignBody
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	ch, bad := req.changes()
	if bad != nil {
		WriteError(w, bad)
		return
	}
	cp, err := s.Campaigns.Update(r.Context(), c.AccountID, id, ch)
	if s.campaignError(w, r, err) {
		return
	}
	out, err := s.withCount(r, cp)
	if s.campaignError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteCampaign is DELETE /v1/campaigns/{campaign_id}.
func (s *Server) DeleteCampaign(w http.ResponseWriter, r *http.Request, id string) {
	c := s.campaignsCaller(w, r)
	if c == nil || !validCampaignID(w, id) {
		return
	}
	if s.campaignError(w, r, s.Campaigns.Delete(r.Context(), c.AccountID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
