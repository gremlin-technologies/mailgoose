package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
)

// contactFilterParams are the filters listing, counting and exporting
// contacts share; each operation's params give the same fields.
type contactFilterParams struct {
	Email, Name, TagID                     *string
	Status                                 *apigen.ContactStatus
	Opened, NotOpened, Clicked, NotClicked *[]string
	NeverOpened, NeverClicked              *bool
	OpenedWithinDays, ClickedWithinDays    *int
}

// contactFilter checks list, count and export filters and turns them into
// a contacts.Filter, writing the error when one is bad.
func contactFilter(w http.ResponseWriter, p contactFilterParams) (contacts.Filter, bool) {
	var f contacts.Filter
	if p.Email != nil {
		f.Email = *p.Email
	}
	if p.Name != nil {
		f.Name = *p.Name
	}
	if p.Status != nil {
		if !p.Status.Valid() {
			WriteError(w, BadRequest("invalid_parameter", "status must be unconfirmed, subscribed, unsubscribed, bounced or complained."))
			return f, false
		}
		f.Status = string(*p.Status)
	}
	if p.TagID != nil {
		if !uuidPattern.MatchString(*p.TagID) {
			WriteError(w, BadRequest("invalid_parameter", "tag_id must be a tag's id."))
			return f, false
		}
		f.TagID = *p.TagID
	}
	e := &f.Engagement
	for _, l := range []struct {
		name string
		in   *[]string
		out  *[]string
	}{
		{"opened", p.Opened, &e.Opened},
		{"not_opened", p.NotOpened, &e.NotOpened},
		{"clicked", p.Clicked, &e.Clicked},
		{"not_clicked", p.NotClicked, &e.NotClicked},
	} {
		if l.in == nil {
			continue
		}
		// Ids are stored lower case, and the filters compare them as text.
		ids := make([]string, len(*l.in))
		for i, id := range *l.in {
			if !uuidPattern.MatchString(id) {
				WriteError(w, BadRequest("invalid_parameter", l.name+" must be a campaign's id."))
				return f, false
			}
			ids[i] = strings.ToLower(id)
		}
		*l.out = ids
	}
	if n := len(e.Campaigns()); n > contacts.MaxFilterCampaigns {
		WriteError(w, BadRequest("invalid_parameter",
			fmt.Sprintf("The engagement filters can name at most %d campaigns, and these name %d.", contacts.MaxFilterCampaigns, n)))
		return f, false
	}
	e.NeverOpened = p.NeverOpened != nil && *p.NeverOpened
	e.NeverClicked = p.NeverClicked != nil && *p.NeverClicked
	for _, d := range []struct {
		name string
		in   *int
		out  *int
	}{
		{"opened_within_days", p.OpenedWithinDays, &e.OpenedWithinDays},
		{"clicked_within_days", p.ClickedWithinDays, &e.ClickedWithinDays},
	} {
		if d.in == nil {
			continue
		}
		if *d.in < 1 || *d.in > contacts.MaxFilterDays {
			WriteError(w, BadRequest("invalid_parameter", fmt.Sprintf("%s is a number of days from 1 to %d.", d.name, contacts.MaxFilterDays)))
			return f, false
		}
		*d.out = *d.in
	}
	return f, true
}

// checkContactFilter checks the campaigns f's engagement filters name,
// writing the error when one can't be used.
func (s *Server) checkContactFilter(w http.ResponseWriter, r *http.Request, accountID string, f contacts.Filter) bool {
	err := s.Contacts.CheckFilter(r.Context(), accountID, f)
	var fe *contacts.FilterCampaignError
	switch {
	case err == nil:
		return true
	case !errors.As(err, &fe):
		s.contactError(w, r, err)
	case errors.Is(err, contacts.ErrFilterTrackingOff):
		WriteError(w, Unprocessable("tracking_off", "Campaign "+fe.CampaignID+" was sent with open and click tracking off, so nobody opened or clicked it as far as mailgoose knows.").
			WithHint("Filter on a campaign sent with tracking on. `mailgoose campaign list` shows them."))
	case errors.Is(err, contacts.ErrFilterArchived):
		WriteError(w, (&Error{Status: http.StatusGone, Code: "report_archived",
			Message: "Campaign " + fe.CampaignID + " launched more than 12 months ago, and its raw events were deleted, so who opened or clicked it is gone."}).
			WithHint("never_opened, never_clicked and the within-days filters cover the events still kept."))
	default:
		WriteError(w, Unprocessable("unknown_campaign", "No campaign "+fe.CampaignID+" in this account.").
			WithHint("`mailgoose campaign list` shows the account's campaigns and their ids."))
	}
	return false
}

// CountContacts is GET /v1/contacts/count.
func (s *Server) CountContacts(w http.ResponseWriter, r *http.Request, params apigen.CountContactsParams) {
	c := s.contactsCaller(w, r)
	if c == nil {
		return
	}
	f, ok := contactFilter(w, contactFilterParams{
		Email: params.Email, Name: params.Name, Status: params.Status, TagID: params.TagId,
		Opened: params.Opened, NotOpened: params.NotOpened, Clicked: params.Clicked, NotClicked: params.NotClicked,
		NeverOpened: params.NeverOpened, NeverClicked: params.NeverClicked,
		OpenedWithinDays: params.OpenedWithinDays, ClickedWithinDays: params.ClickedWithinDays,
	})
	if !ok || !s.checkContactFilter(w, r, c.AccountID, f) {
		return
	}
	n, err := s.Contacts.Count(r.Context(), c.AccountID, f)
	if s.contactError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, apigen.ContactCount{Count: n})
}
