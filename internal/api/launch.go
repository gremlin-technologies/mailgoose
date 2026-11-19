package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

// LaunchCampaign is POST /v1/campaigns/{campaign_id}/launch (CAM-3,
// CAM-4): without a code it answers with the summary and a code; with the
// code it launches.
func (s *Server) LaunchCampaign(w http.ResponseWriter, r *http.Request, id string) {
	c := s.requireFull(w, r)
	if c == nil {
		return
	}
	if s.Sending == nil || s.Campaigns == nil || s.Contacts == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Launching needs a database, and none is configured."})
		return
	}
	if !validCampaignID(w, id) {
		return
	}
	var req apigen.LaunchCampaignRequest
	if r.ContentLength != 0 {
		if err := DecodeJSON(w, r, &req); err != nil {
			WriteError(w, err)
			return
		}
	}
	code := ""
	if req.Confirm != nil {
		code = *req.Confirm
	}
	res, err := s.Sending.Launch(r.Context(), c.AccountID, id, code, s.Links)
	if s.launchError(w, r, err) {
		return
	}
	out, err := s.withCount(r, res.Campaign)
	if s.campaignError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.LaunchResult{Campaign: out, RecipientCount: res.RecipientCount, CreditsReserved: res.CreditsReserved})
}

func (s *Server) launchError(w http.ResponseWriter, r *http.Request, err error) bool {
	var confirm *sending.ConfirmationRequired
	var notReady *sending.NotReadyError
	var codeErr *sending.CodeError
	switch {
	case err == nil:
		return false
	case errors.As(err, &confirm):
		sum := confirm.Summary
		when := "as soon as it's launched"
		if sum.SendAt != nil {
			when = "at " + sum.SendAt.UTC().Format(time.RFC3339)
		}
		WriteError(w, Conflict("confirmation_required", fmt.Sprintf(
			"Nothing was launched. Launching %q sends %q from %s to %d %s %s, for %d %s.",
			sum.Name, sum.Subject, sum.From.Email, sum.RecipientCount, plural(sum.RecipientCount, "contact", "contacts"),
			when, sum.Credits, plural(sum.Credits, "credit", "credits"))).
			WithHint("Show this summary to the person. Only if they say yes, launch again with `confirm` set to the code. It works for 15 minutes.").
			WithDetails(map[string]any{
				"summary":    summaryJSON(sum),
				"code":       confirm.Code,
				"expires_at": confirm.ExpiresAt.UTC().Format(time.RFC3339),
			}))
	case errors.As(err, &notReady):
		WriteError(w, notReadyError("The campaign can't launch yet.", notReady))
	case errors.As(err, &codeErr):
		WriteError(w, Unprocessable("invalid_confirmation", "Nothing was launched: "+codeErr.Error()+".").
			WithHint("Launch again without `confirm` for a new summary and code, and show it to the person.").
			WithDetails(codeDetails(codeErr)))
	default:
		s.campaignError(w, r, err)
	}
	return true
}

// codeDetails are an invalid_confirmation error's details: the reason, and
// for audience_grew the summary's count and the count now.
func codeDetails(e *sending.CodeError) map[string]any {
	d := map[string]any{"reason": e.Reason}
	if e.Reason == sending.CodeGrew {
		d["summarized_count"] = e.Summarized
		d["recipient_count"] = e.Recipients
	}
	return d
}

func summaryJSON(s *sending.Summary) map[string]any {
	addr := func(a mail.Address) map[string]any {
		return map[string]any{"name": a.Name, "email": a.Email}
	}
	replyTo := make([]map[string]any, len(s.ReplyTo))
	for i, a := range s.ReplyTo {
		replyTo[i] = addr(a)
	}
	var sendAt any
	if s.SendAt != nil {
		sendAt = s.SendAt.UTC().Format(time.RFC3339)
	}
	warnings := s.Warnings
	if warnings == nil {
		warnings = []emailcheck.Issue{}
	}
	return map[string]any{
		"campaign_id":      s.CampaignID,
		"name":             s.Name,
		"subject":          s.Subject,
		"from":             addr(s.From),
		"reply_to":         replyTo,
		"recipient_count":  s.RecipientCount,
		"send_at":          sendAt,
		"credits":          s.Credits,
		"balance":          s.Balance,
		"campaign_version": s.CampaignVersion,
		"email_version":    s.EmailVersion,
		"warnings":         warnings,
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
