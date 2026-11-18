package api

import (
	"errors"
	"net/http"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

// CancelCampaign is POST /v1/campaigns/{campaign_id}/cancel (CAM-6).
func (s *Server) CancelCampaign(w http.ResponseWriter, r *http.Request, id string) {
	c := s.scheduleCaller(w, r, id)
	if c == nil {
		return
	}
	camp, err := s.Sending.Cancel(r.Context(), c.AccountID, id)
	s.writeScheduled(w, r, camp, err)
}

// RescheduleCampaign is POST /v1/campaigns/{campaign_id}/reschedule (CAM-6).
func (s *Server) RescheduleCampaign(w http.ResponseWriter, r *http.Request, id string) {
	c := s.scheduleCaller(w, r, id)
	if c == nil {
		return
	}
	var req apigen.RescheduleCampaignRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	camp, err := s.Sending.Reschedule(r.Context(), c.AccountID, id, req.SendAt)
	s.writeScheduled(w, r, camp, err)
}

func (s *Server) scheduleCaller(w http.ResponseWriter, r *http.Request, id string) *Caller {
	c := s.requireFull(w, r)
	if c == nil {
		return nil
	}
	if s.Sending == nil || s.Campaigns == nil || s.Contacts == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Scheduling needs a database, and none is configured."})
		return nil
	}
	if !validCampaignID(w, id) {
		return nil
	}
	return c
}

func (s *Server) writeScheduled(w http.ResponseWriter, r *http.Request, c *campaigns.Campaign, err error) {
	var re *sending.RescheduleError
	var st *campaigns.StatusError
	switch {
	case errors.As(err, &re):
		WriteError(w, Unprocessable("invalid_campaign", "The campaign wasn't moved: "+re.Message+".").
			WithDetails(map[string]any{"field": "send_at"}))
		return
	case errors.As(err, &st):
		hint := "Only a scheduled campaign can be moved or cancelled, before its send time."
		if st.Status == campaigns.StatusDraft {
			hint = "A draft isn't launched yet: change its send time with `mailgoose campaign update --send-at`, or remove it with `mailgoose campaign delete`."
		}
		WriteError(w, Conflict("campaign_locked", "The campaign is "+st.Status+", so it can't be moved or cancelled.").
			WithHint(hint).WithDetails(map[string]any{"status": st.Status}))
		return
	}
	if s.campaignError(w, r, err) {
		return
	}
	out, err := s.withCount(r, c)
	if s.campaignError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}
