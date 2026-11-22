package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/reports"
)

// recipientQuery checks a recipient list's activity and link (MG-112).
func recipientQuery(w http.ResponseWriter, activity apigen.ReportActivity, link *apigen.ReportLinkPosition) (reports.RecipientQuery, bool) {
	q := reports.RecipientQuery{Activity: reports.Activity(activity)}
	if !q.Activity.Valid() {
		WriteError(w, BadRequest("invalid_parameter", "activity must be one of opened, not_opened, clicked, not_clicked, bounced, complained or unsubscribed."))
		return q, false
	}
	if link != nil {
		if q.Activity != reports.Clicked {
			WriteError(w, BadRequest("invalid_parameter", "link only goes with activity=clicked."))
			return q, false
		}
		if *link < 1 {
			WriteError(w, BadRequest("invalid_parameter", "link is a link's position in the report, from 1."))
			return q, false
		}
		q.Link = *link
	}
	return q, true
}

// recipientsError writes a recipient list's own errors, and the campaign's.
func (s *Server) recipientsError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case errors.Is(err, reports.ErrArchived):
		WriteError(w, (&Error{Status: http.StatusGone, Code: "report_archived",
			Message: "This campaign launched more than 12 months ago, and its raw events were deleted, so who did what is gone."}).
			WithHint("The report still has its saved numbers: `mailgoose campaign report <id>`."))
	case errors.Is(err, reports.ErrTrackingOff):
		WriteError(w, Unprocessable("tracking_off", "This campaign was sent with open and click tracking off, so there are no opens or clicks to list.").
			WithHint("Bounces, complaints and unsubscribes are still listed. Leave tracking on for the next campaign to see opens and clicks."))
	default:
		return s.campaignError(w, r, err)
	}
	return true
}

func (s *Server) reportsReady(w http.ResponseWriter) bool {
	if s.Reports == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Reports need a database, and none is configured."})
		return false
	}
	return true
}

// ListCampaignReportRecipients is GET /v1/campaigns/{id}/report/recipients.
func (s *Server) ListCampaignReportRecipients(w http.ResponseWriter, r *http.Request, id string, params apigen.ListCampaignReportRecipientsParams) {
	c := s.campaignsCaller(w, r)
	if c == nil || !validCampaignID(w, id) || !s.reportsReady(w) {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	q, ok := recipientQuery(w, params.Activity, params.Link)
	if !ok {
		return
	}
	if pr.After != "" {
		if q.After, err = strconv.ParseInt(pr.After, 10, 64); err != nil || q.After < 1 {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
	}
	q.Limit = pr.Limit + 1
	rows, err := s.Reports.Recipients(r.Context(), c.AccountID, id, q)
	if s.recipientsError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, NewPage(rows, pr.Limit, func(rc reports.Recipient) string { return strconv.FormatInt(rc.ID, 10) }))
}

// ExportCampaignReportRecipients is GET
// /v1/campaigns/{id}/report/recipients/export. Like the contact export, the
// CSV streams as it's read; an error part way cuts it short and is logged.
func (s *Server) ExportCampaignReportRecipients(w http.ResponseWriter, r *http.Request, id string, params apigen.ExportCampaignReportRecipientsParams) {
	c := s.campaignsCaller(w, r)
	if c == nil || !validCampaignID(w, id) || !s.reportsReady(w) {
		return
	}
	q, ok := recipientQuery(w, params.Activity, params.Link)
	if !ok {
		return
	}
	// Check the campaign first, so its errors are JSON, not a cut CSV.
	if _, err := s.Reports.Recipients(r.Context(), c.AccountID, id, reports.RecipientQuery{Activity: q.Activity, Link: q.Link, Limit: 1}); s.recipientsError(w, r, err) {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+string(q.Activity)+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := s.Reports.RecipientsCSV(r.Context(), c.AccountID, id, q, w); err != nil {
		s.Log.ErrorContext(r.Context(), "recipient export cut short", "err", err)
	}
}
