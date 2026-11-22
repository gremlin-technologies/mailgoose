package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/segments"
)

// segmentsCaller is requireCaller for the segment endpoints, which also
// need the segments service.
func (s *Server) segmentsCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && (s.Segments == nil || s.Contacts == nil) {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Segments need a database, and none is configured."})
		return nil
	}
	return c
}

// segmentError writes the response for a segments error, and reports
// whether there was one.
func (s *Server) segmentError(w http.ResponseWriter, r *http.Request, err error) bool {
	var le *segments.LimitError
	var ce *segments.ConditionError
	var ie *segments.InvalidError
	switch {
	case err == nil:
		return false
	case errors.Is(err, segments.ErrNotFound):
		WriteError(w, NotFound("not_found", "No segment with that id in this account.").
			WithHint("`mailgoose segment list` shows the account's segments."))
	case errors.Is(err, segments.ErrExists):
		WriteError(w, Conflict("segment_exists", "This account already has a segment with that name.").
			WithHint("Segment names are unique ignoring case. `mailgoose segment list` shows them."))
	case errors.As(err, &le):
		WriteError(w, Conflict("segment_limit", fmt.Sprintf("The account has %d segments, the most it can keep.", segments.MaxSegments)).
			WithHint("Delete one you no longer use with `mailgoose segment delete`.").
			WithDetails(map[string]any{"limit": segments.MaxSegments}))
	case errors.As(err, &ce):
		WriteError(w, conditionError(ce))
	case errors.As(err, &ie):
		WriteError(w, Unprocessable("segment_invalid",
			"Segment "+ie.Segment.Name+" has a condition it can't use now: "+problemText(ie.Segment.Problems[0])+".").
			WithHint("Update its conditions with `mailgoose segment update`; `mailgoose segment get` lists every problem.").
			WithDetails(map[string]any{"problems": problemsJSON(ie.Segment.Problems)}))
	default:
		return s.contactError(w, r, err)
	}
	return true
}

func conditionError(ce *segments.ConditionError) *Error {
	details := map[string]any{"condition": ce.Condition, "id": ce.ID}
	switch ce.Code {
	case "unknown_tag":
		return Unprocessable(ce.Code, "No tag "+ce.ID+" in this account.").
			WithHint("`mailgoose tags list` shows the account's tags and their ids.").WithDetails(details)
	case "tracking_off":
		return Unprocessable(ce.Code, "Campaign "+ce.ID+" is set to send with open and click tracking off, so nobody opens or clicks it as far as mailgoose knows.").
			WithHint("Use a campaign sent with tracking on. `mailgoose campaign list` shows them.").WithDetails(details)
	case "report_archived":
		return Unprocessable(ce.Code, "Campaign "+ce.ID+" launched more than 12 months ago, and its raw events were deleted, so who opened or clicked it is gone.").
			WithHint("never_opened, never_clicked and the within-days conditions cover the events still kept.").WithDetails(details)
	default:
		return Unprocessable("unknown_campaign", "No campaign "+ce.ID+" in this account.").
			WithHint("`mailgoose campaign list` shows the account's campaigns and their ids.").WithDetails(details)
	}
}

func problemText(p segments.Problem) string {
	switch p.Code {
	case segments.ProblemTagDeleted:
		return "tag " + p.ID + " in " + p.Condition + " was deleted"
	case segments.ProblemCampaignDeleted:
		return "campaign " + p.ID + " in " + p.Condition + " was deleted"
	case segments.ProblemTrackingOff:
		return "campaign " + p.ID + " in " + p.Condition + " is set to send without tracking"
	default:
		return "campaign " + p.ID + " in " + p.Condition + " has its raw events archived"
	}
}

func problemsJSON(ps []segments.Problem) []apigen.SegmentProblem {
	out := make([]apigen.SegmentProblem, len(ps))
	for i, p := range ps {
		out[i] = apigen.SegmentProblem{Condition: apigen.SegmentProblemCondition(p.Condition), Id: p.ID, Code: apigen.SegmentProblemCode(p.Code)}
	}
	return out
}

func idList(ids []string) *[]string {
	if len(ids) == 0 {
		return nil
	}
	return &ids
}

func conditionsJSON(c segments.Conditions) apigen.SegmentConditions {
	out := apigen.SegmentConditions{TagIds: idList(c.TagIDs), Opened: idList(c.Opened), NotOpened: idList(c.NotOpened),
		Clicked: idList(c.Clicked), NotClicked: idList(c.NotClicked)}
	if len(c.Statuses) > 0 {
		st := make([]apigen.ContactStatus, len(c.Statuses))
		for i, v := range c.Statuses {
			st[i] = apigen.ContactStatus(v)
		}
		out.Statuses = &st
	}
	if c.NeverOpened {
		out.NeverOpened = &c.NeverOpened
	}
	if c.NeverClicked {
		out.NeverClicked = &c.NeverClicked
	}
	if c.OpenedWithinDays > 0 {
		out.OpenedWithinDays = &c.OpenedWithinDays
	}
	if c.ClickedWithinDays > 0 {
		out.ClickedWithinDays = &c.ClickedWithinDays
	}
	return out
}

// conditionsFrom reads the request's conditions. A within-days condition
// of 0 or less is refused here, since the service takes 0 as none.
func conditionsFrom(in apigen.SegmentConditions) (segments.Conditions, *Error) {
	var c segments.Conditions
	deref := func(p *[]string) []string {
		if p == nil {
			return nil
		}
		return *p
	}
	c.TagIDs, c.Opened, c.NotOpened = deref(in.TagIds), deref(in.Opened), deref(in.NotOpened)
	c.Clicked, c.NotClicked = deref(in.Clicked), deref(in.NotClicked)
	if in.Statuses != nil {
		for _, st := range *in.Statuses {
			c.Statuses = append(c.Statuses, string(st))
		}
	}
	c.NeverOpened = in.NeverOpened != nil && *in.NeverOpened
	c.NeverClicked = in.NeverClicked != nil && *in.NeverClicked
	for _, d := range []struct {
		name string
		in   *int
		out  *int
	}{
		{"opened_within_days", in.OpenedWithinDays, &c.OpenedWithinDays},
		{"clicked_within_days", in.ClickedWithinDays, &c.ClickedWithinDays},
	} {
		if d.in == nil {
			continue
		}
		if *d.in < 1 || *d.in > contacts.MaxFilterDays {
			return c, Unprocessable("invalid_conditions", fmt.Sprintf("conditions.%s is a number of days from 1 to %d.", d.name, contacts.MaxFilterDays))
		}
		*d.out = *d.in
	}
	return c, nil
}

func segmentJSON(sg *segments.Segment) apigen.Segment {
	return apigen.Segment{Id: sg.ID, Name: sg.Name, Conditions: conditionsJSON(sg.Conditions),
		Valid: sg.Valid(), Problems: problemsJSON(sg.Problems),
		CreatedAt: sg.CreatedAt.UTC(), UpdatedAt: sg.UpdatedAt.UTC()}
}

func validSegmentID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, NotFound("not_found", "No segment with that id in this account.").
			WithHint("`mailgoose segment list` shows the account's segments."))
		return false
	}
	return true
}

// ListSegments is GET /v1/segments.
func (s *Server) ListSegments(w http.ResponseWriter, r *http.Request, params apigen.ListSegmentsParams) {
	c := s.segmentsCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	var after *segments.Cursor
	if pr.After != "" {
		name, id, ok := strings.Cut(pr.After, "\x00")
		if !ok || !uuidPattern.MatchString(id) {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
		after = &segments.Cursor{Name: name, ID: id}
	}
	name := ""
	if params.Name != nil {
		name = *params.Name
	}
	sgs, err := s.Segments.List(r.Context(), c.AccountID, name, after, pr.Limit+1)
	if s.segmentError(w, r, err) {
		return
	}
	page := NewPage(sgs, pr.Limit, func(sg *segments.Segment) string { return sg.Name + "\x00" + sg.ID })
	out := apigen.SegmentList{Data: make([]apigen.Segment, len(page.Data)), NextCursor: page.NextCursor}
	for i, sg := range page.Data {
		out.Data[i] = segmentJSON(sg)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateSegment is POST /v1/segments.
func (s *Server) CreateSegment(w http.ResponseWriter, r *http.Request) {
	c := s.segmentsCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.CreateSegmentRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	conds, e := conditionsFrom(req.Conditions)
	if e != nil {
		WriteError(w, e)
		return
	}
	sg, err := s.Segments.Create(r.Context(), c.AccountID, req.Name, conds)
	if s.segmentError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, segmentJSON(sg))
}

// GetSegment is GET /v1/segments/{segment_id}.
func (s *Server) GetSegment(w http.ResponseWriter, r *http.Request, id string) {
	c := s.segmentsCaller(w, r)
	if c == nil || !validSegmentID(w, id) {
		return
	}
	sg, err := s.Segments.Get(r.Context(), c.AccountID, id)
	if s.segmentError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, segmentJSON(sg))
}

// UpdateSegment is PATCH /v1/segments/{segment_id}.
func (s *Server) UpdateSegment(w http.ResponseWriter, r *http.Request, id string) {
	c := s.segmentsCaller(w, r)
	if c == nil || !validSegmentID(w, id) {
		return
	}
	var req apigen.UpdateSegmentRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	var conds *segments.Conditions
	if req.Conditions != nil {
		cs, e := conditionsFrom(*req.Conditions)
		if e != nil {
			WriteError(w, e)
			return
		}
		conds = &cs
	}
	sg, err := s.Segments.Update(r.Context(), c.AccountID, id, req.Name, conds)
	if s.segmentError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, segmentJSON(sg))
}

// DeleteSegment is DELETE /v1/segments/{segment_id}.
func (s *Server) DeleteSegment(w http.ResponseWriter, r *http.Request, id string) {
	c := s.segmentsCaller(w, r)
	if c == nil || !validSegmentID(w, id) {
		return
	}
	if s.segmentError(w, r, s.Segments.Delete(r.Context(), c.AccountID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListSegmentContacts is GET /v1/segments/{segment_id}/contacts.
func (s *Server) ListSegmentContacts(w http.ResponseWriter, r *http.Request, id string, _ apigen.ListSegmentContactsParams) {
	c := s.segmentsCaller(w, r)
	if c == nil || !validSegmentID(w, id) {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	var after *contacts.Cursor
	if pr.After != "" {
		var ok bool
		if after, ok = parseContactCursor(pr.After); !ok {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
	}
	m, err := s.Segments.Members(r.Context(), c.AccountID, id, after, pr.Limit+1)
	if s.segmentError(w, r, err) {
		return
	}
	page := NewPage(m.Contacts, pr.Limit, contactCursor)
	out := apigen.SegmentContactList{Data: make([]apigen.Contact, len(page.Data)), NextCursor: page.NextCursor, Count: m.Count}
	for i, ct := range page.Data {
		out.Data[i] = contactJSON(ct)
	}
	writeJSON(w, http.StatusOK, out)
}
