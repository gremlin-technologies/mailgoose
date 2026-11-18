package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
)

// domainsCaller is requireCaller for the domain endpoints, which also need
// the domains service.
func (s *Server) domainsCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && s.Domains == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Sending domains need a database, and none is configured."})
		return nil
	}
	return c
}

// domainError writes the response for a domains error, and reports whether
// there was one.
func (s *Server) domainError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, domains.ErrInvalidName):
		WriteError(w, Unprocessable("invalid_domain", "That isn't a domain name mailgoose can send from.").
			WithHint("Give a domain you own, such as example.com or news.example.com, without @ or https://."))
	case errors.Is(err, domains.ErrReserved):
		WriteError(w, Unprocessable("reserved_domain", "That domain belongs to mailgoose."))
	case errors.Is(err, domains.ErrExists):
		WriteError(w, Conflict("domain_exists", "This account already has that domain.").
			WithHint("`mailgoose domain verify <domain>` checks its records."))
	case errors.Is(err, domains.ErrTaken):
		WriteError(w, Conflict("domain_taken", "Another mailgoose account has that domain.").
			WithHint("If it's yours, contact support."))
	case errors.Is(err, domains.ErrUnavailable):
		WriteError(w, Conflict("domain_unavailable", "That domain is already set up in Amazon SES outside this account.").
			WithHint("If it's yours, contact support."))
	case errors.Is(err, domains.ErrNotFound):
		WriteError(w, NotFound("not_found", "No domain with that id in this account.").
			WithHint("`mailgoose domain list` shows the account's domains."))
	default:
		s.Log.ErrorContext(r.Context(), "domain request failed", "err", err)
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Amazon SES or the database didn't answer.", Hint: "Try again in a minute."})
	}
	return true
}

func (s *Server) domainJSON(d *domains.Domain) apigen.Domain {
	out := apigen.Domain{
		Id:             d.ID,
		Name:           d.Name,
		Status:         apigen.DomainStatus(d.Status),
		MailFromDomain: d.MailFrom,
		CreatedAt:      d.CreatedAt.UTC(),
		CheckUntil:     d.CheckUntil.UTC(),
		LastCheckedAt:  utcPtr(d.LastCheckedAt),
		VerifiedAt:     utcPtr(d.VerifiedAt),
		FailedAt:       utcPtr(d.FailedAt),
		Records:        []apigen.DnsRecord{},
	}
	if d.SESStatus != "" {
		out.SesStatus = &d.SESStatus
	}
	checks := map[string]domains.RecordCheck{}
	for _, c := range d.Checks {
		checks[c.Purpose+"|"+c.Name] = c
	}
	for _, rec := range s.Domains.Records(d) {
		jr := apigen.DnsRecord{
			Purpose:  apigen.DnsRecordPurpose(rec.Purpose),
			Type:     apigen.DnsRecordType(rec.Type),
			Name:     rec.Name,
			Value:    rec.Value,
			Priority: rec.Priority,
			Required: rec.Required,
			Status:   "unchecked",
			Found:    []string{},
		}
		if c, ok := checks[rec.Purpose+"|"+rec.Name]; ok {
			jr.Status = apigen.DnsRecordStatus(c.Status)
			if len(c.Found) > 0 {
				jr.Found = c.Found
			}
			if c.Detail != "" {
				detail := c.Detail
				jr.Detail = &detail
			}
		}
		out.Records = append(out.Records, jr)
	}
	return out
}

// ListDomains is GET /v1/domains.
func (s *Server) ListDomains(w http.ResponseWriter, r *http.Request, params apigen.ListDomainsParams) {
	c := s.domainsCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	if pr.After != "" && !uuidPattern.MatchString(pr.After) {
		WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
		return
	}
	name := ""
	if params.Name != nil {
		name = *params.Name
	}
	ds, err := s.Domains.List(r.Context(), c.AccountID, name, pr.After, pr.Limit+1)
	if s.domainError(w, r, err) {
		return
	}
	page := NewPage(ds, pr.Limit, func(d *domains.Domain) string { return d.ID })
	out := apigen.DomainList{Data: make([]apigen.Domain, len(page.Data)), NextCursor: page.NextCursor}
	for i, d := range page.Data {
		out.Data[i] = s.domainJSON(d)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateDomain is POST /v1/domains.
func (s *Server) CreateDomain(w http.ResponseWriter, r *http.Request) {
	c := s.domainsCaller(w, r)
	if c == nil {
		return
	}
	var req apigen.CreateDomainRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	d, err := s.Domains.Add(r.Context(), c.AccountID, req.Name)
	if s.domainError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, s.domainJSON(d))
}

func validDomainID(w http.ResponseWriter, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "domain_id must be a domain's id."))
		return false
	}
	return true
}

// GetDomain is GET /v1/domains/{domain_id}.
func (s *Server) GetDomain(w http.ResponseWriter, r *http.Request, id string) {
	c := s.domainsCaller(w, r)
	if c == nil || !validDomainID(w, id) {
		return
	}
	d, err := s.Domains.Get(r.Context(), c.AccountID, id)
	if s.domainError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, s.domainJSON(d))
}

// DeleteDomain is DELETE /v1/domains/{domain_id}.
func (s *Server) DeleteDomain(w http.ResponseWriter, r *http.Request, id string) {
	c := s.domainsCaller(w, r)
	if c == nil || !validDomainID(w, id) {
		return
	}
	if s.domainError(w, r, s.Domains.Delete(r.Context(), c.AccountID, id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// VerifyDomain is POST /v1/domains/{domain_id}/verify.
func (s *Server) VerifyDomain(w http.ResponseWriter, r *http.Request, id string) {
	c := s.domainsCaller(w, r)
	if c == nil || !validDomainID(w, id) {
		return
	}
	d, recreated, err := s.Domains.Verify(r.Context(), c.AccountID, id)
	if s.domainError(w, r, err) {
		return
	}
	out := s.domainJSON(d)
	out.RecordsChanged = &recreated
	writeJSON(w, http.StatusOK, out)
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
