package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

// TestCampaign is POST /v1/campaigns/{campaign_id}/test.
func (s *Server) TestCampaign(w http.ResponseWriter, r *http.Request, id string) {
	c := s.campaignsCaller(w, r)
	if c == nil {
		return
	}
	if s.Sending == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Test sends need a database and a mail sender, and this server has none configured."})
		return
	}
	if !validCampaignID(w, id) {
		return
	}
	var req apigen.TestCampaignRequest
	if r.ContentLength != 0 {
		if err := DecodeJSON(w, r, &req); err != nil {
			WriteError(w, err)
			return
		}
	}
	var to []string
	if req.To != nil {
		to = *req.To
	}
	res, err := s.Sending.Test(r.Context(), c.AccountID, id, to, s.webURL(r))
	if s.testSendError(w, r, err) {
		return
	}
	out, err := s.withCount(r, res.Campaign)
	if s.campaignError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.TestSendResult{Campaign: out, To: res.To, SentToday: res.SentToday, DailyLimit: sending.TestsPerDay})
}

func (s *Server) testSendError(w http.ResponseWriter, r *http.Request, err error) bool {
	var notReady *sending.NotReadyError
	var invalid *sending.InvalidError
	var recipient *sending.RecipientError
	var suppressed *sending.SuppressedError
	var limit *sending.LimitError
	var failed *sending.SendError
	switch {
	case err == nil:
		return false
	case errors.As(err, &notReady):
		problems := make([]map[string]string, len(notReady.Problems))
		msgs := make([]string, len(notReady.Problems))
		for i, p := range notReady.Problems {
			problems[i] = map[string]string{"code": p.Code, "message": p.Message, "hint": p.Hint}
			msgs[i] = p.Message
		}
		WriteError(w, Unprocessable("not_ready", "The campaign can't be sent yet. "+strings.Join(msgs, " ")).
			WithHint(notReady.Problems[0].Hint).WithDetails(map[string]any{"problems": problems}))
	case errors.As(err, &invalid):
		WriteError(w, Unprocessable("invalid_test", "The test can't be sent: "+invalid.Message+".").
			WithDetails(map[string]any{"field": invalid.Field}))
	case errors.As(err, &suppressed):
		why := "is on the account's suppression list"
		if suppressed.Status != "" {
			why = "has " + suppressed.Status
		}
		WriteError(w, Unprocessable("test_recipient_suppressed", suppressed.Address+" "+why+", so tests don't go to it.").
			WithHint("Send the test to another address, or leave `to` out to send it to yourself.").
			WithDetails(map[string]any{"address": suppressed.Address}))
	case errors.As(err, &recipient):
		WriteError(w, Unprocessable("test_recipient_not_allowed", "Tests go to the account owner or to addresses on a verified sending domain, and "+recipient.Address+" is neither.").
			WithHint("Leave `to` out to send to yourself, or verify the address's domain with `mailgoose domain add`.").
			WithDetails(map[string]any{"address": recipient.Address}))
	case errors.As(err, &limit):
		secs := int(math.Ceil(limit.RetryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		WriteError(w, TooManyRequests("test_limit", fmt.Sprintf("This account has sent %d test emails in the last 24 hours, and the limit is %d.", limit.Sent, sending.TestsPerDay)).
			WithHint(fmt.Sprintf("Wait %s (see Retry-After), or send to fewer addresses.", wait(secs))))
	case errors.As(err, &failed):
		s.Log.ErrorContext(r.Context(), "test send failed", "err", failed.Err)
		WriteError(w, &Error{Status: http.StatusBadGateway, Code: "test_send_failed",
			Message: "The test to " + failed.Address + " couldn't be sent; the mail provider refused it or didn't answer.",
			Hint:    "Try again in a minute. It still counts toward the day's tests."})
	default:
		// Not found, a campaign past scheduled, or the database.
		s.campaignError(w, r, err)
	}
	return true
}

// wait says a number of seconds the way a person would.
func wait(secs int) string {
	switch {
	case secs < 120:
		return strconv.Itoa(secs) + " seconds"
	case secs < 2*3600:
		return strconv.Itoa((secs+59)/60) + " minutes"
	default:
		return strconv.Itoa((secs+3599)/3600) + " hours"
	}
}
