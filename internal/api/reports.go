package api

import (
	"net/http"
)

// GetCampaignReport is GET /v1/campaigns/{campaign_id}/report (REP-1,
// REP-2). It reads reports.Service.Campaign, the one query the CLI and web
// app read too.
func (s *Server) GetCampaignReport(w http.ResponseWriter, r *http.Request, id string) {
	c := s.campaignsCaller(w, r)
	if c == nil || !validCampaignID(w, id) {
		return
	}
	if !s.reportsReady(w) {
		return
	}
	rep, err := s.Reports.Campaign(r.Context(), c.AccountID, id)
	if s.campaignError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
