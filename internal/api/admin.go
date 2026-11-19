package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/admin"
	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
)

// The admin endpoints (MG-34: OPS-6, CMP-8), for mailgoose staff only.

// requireAdmin returns the admin making the request, or writes the error
// and returns nil. Only a person signed in (a CLI token or a web session)
// can be an admin: an API key, even an admin's, is refused, so an agent
// holding a customer-style key never gets staff powers.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) *admin.Actor {
	c := s.requireFull(w, r)
	if c == nil {
		return nil
	}
	if s.Admin == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "The admin tools need a database, and none is configured."})
		return nil
	}
	refuse := Forbidden("admin_only", "Only a mailgoose admin can do this.")
	if c.Kind == auth.KindAPIKey {
		WriteError(w, refuse.WithHint("Admin tools take a person's sign-in, not an API key: run `mailgoose login` and try again."))
		return nil
	}
	a, err := s.Admin.Admin(r.Context(), c.UserID)
	if errors.Is(err, admin.ErrNotAdmin) {
		WriteError(w, refuse)
		return nil
	}
	if err != nil {
		s.adminError(w, r, err)
		return nil
	}
	return a
}

func (s *Server) adminError(w http.ResponseWriter, r *http.Request, err error) {
	var ie *admin.InputError
	var st *campaigns.StatusError
	switch {
	case errors.As(err, &ie):
		WriteError(w, Unprocessable("invalid_admin_request", "Nothing changed: "+ie.Message+".").
			WithDetails(map[string]any{"field": ie.Field}))
	case errors.Is(err, admin.ErrNotFound), errors.Is(err, campaigns.ErrNotFound):
		WriteError(w, NotFound("not_found", "Nothing with that id.").
			WithHint("`mailgoose admin find <address, name or domain>` finds an account."))
	case errors.Is(err, admin.ErrSuspended):
		WriteError(w, Conflict("account_suspended", "The account is suspended already, so nothing changed.").
			WithHint("`mailgoose admin account <account-id>` shows why."))
	case errors.Is(err, admin.ErrNotSuspended):
		WriteError(w, Conflict("account_not_suspended", "The account isn't suspended, so nothing changed."))
	case errors.Is(err, admin.ErrNotWaiting):
		WriteError(w, Conflict("signup_not_waiting", "The account isn't waiting for its sign-up review, so nothing changed.").
			WithHint("`mailgoose admin signups` lists the accounts waiting."))
	case errors.Is(err, admin.ErrNotHeld):
		WriteError(w, Conflict("import_not_held", "The import isn't waiting for approval, so nothing changed."))
	case errors.As(err, &st):
		WriteError(w, Conflict("campaign_locked", "The campaign is "+st.Status+", so it can't be stopped.").
			WithHint("Only a scheduled, sending or paused campaign can be stopped.").
			WithDetails(map[string]any{"status": st.Status}))
	default:
		s.Log.ErrorContext(r.Context(), "admin request failed", "err", err)
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "The database didn't answer.", Hint: "Try again in a minute."})
	}
}

func validUUID(w http.ResponseWriter, name, id string) bool {
	if !uuidPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", name+" must be an id."))
		return false
	}
	return true
}

// adminAccountJSON makes sure the lists are [] rather than null.
func adminAccountJSON(a *admin.Account) *admin.Account {
	if a.HeldImports == nil {
		a.HeldImports = []admin.HeldImport{}
	}
	if a.LiveCampaigns == nil {
		a.LiveCampaigns = []admin.LiveCampaign{}
	}
	if a.Throttles == nil {
		a.Throttles = []admin.Throttle{}
	}
	return a
}

// FindAdminAccounts is GET /v1/admin/accounts.
func (s *Server) FindAdminAccounts(w http.ResponseWriter, r *http.Request, params apigen.FindAdminAccountsParams) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	q := ""
	if params.Q != nil {
		q = strings.TrimSpace(*params.Q)
	}
	if q == "" {
		WriteError(w, BadRequest("invalid_parameter", "q is required: an account id, or part of an address, name or domain."))
		return
	}
	found, err := s.Admin.FindAccounts(r.Context(), q, pr.Limit)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	if found == nil {
		found = []admin.AccountSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": found, "next_cursor": nil})
}

// GetAdminAccount is GET /v1/admin/accounts/{account_id}.
func (s *Server) GetAdminAccount(w http.ResponseWriter, r *http.Request, id apigen.AccountId) {
	if s.requireAdmin(w, r) == nil || !validUUID(w, "account_id", id) {
		return
	}
	a, err := s.Admin.GetAccount(r.Context(), id)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminAccountJSON(a))
}

// adminReason reads {"reason": ...}.
func adminReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req apigen.AdminReasonRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return "", false
	}
	return req.Reason, true
}

// PauseAccount is POST /v1/admin/accounts/{account_id}/pause.
func (s *Server) PauseAccount(w http.ResponseWriter, r *http.Request, id apigen.AccountId) {
	s.pauseOrUnpause(w, r, id, s.Admin.Pause)
}

// UnpauseAccount is POST /v1/admin/accounts/{account_id}/unpause.
func (s *Server) UnpauseAccount(w http.ResponseWriter, r *http.Request, id apigen.AccountId) {
	s.pauseOrUnpause(w, r, id, s.Admin.Unpause)
}

// SuspendAccount is POST /v1/admin/accounts/{account_id}/suspend (MG-65).
func (s *Server) SuspendAccount(w http.ResponseWriter, r *http.Request, id apigen.AccountId) {
	s.pauseOrUnpause(w, r, id, s.Admin.Suspend)
}

// UnsuspendAccount is POST /v1/admin/accounts/{account_id}/unsuspend.
func (s *Server) UnsuspendAccount(w http.ResponseWriter, r *http.Request, id apigen.AccountId) {
	s.pauseOrUnpause(w, r, id, s.Admin.Unsuspend)
}

// ListSignupReviews is GET /v1/admin/signups (MG-66).
func (s *Server) ListSignupReviews(w http.ResponseWriter, r *http.Request, _ apigen.ListSignupReviewsParams) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	found, err := s.Admin.ListSignups(r.Context(), pr.Limit)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	if found == nil {
		found = []admin.AccountSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": found, "next_cursor": nil})
}

// ApproveSignup is POST /v1/admin/accounts/{account_id}/approve-signup.
func (s *Server) ApproveSignup(w http.ResponseWriter, r *http.Request, id apigen.AccountId) {
	s.pauseOrUnpause(w, r, id, s.Admin.ApproveSignup)
}

// RejectSignup is POST /v1/admin/accounts/{account_id}/reject-signup.
func (s *Server) RejectSignup(w http.ResponseWriter, r *http.Request, id apigen.AccountId) {
	s.pauseOrUnpause(w, r, id, s.Admin.RejectSignup)
}

func (s *Server) pauseOrUnpause(w http.ResponseWriter, r *http.Request, id string,
	f func(ctx context.Context, a admin.Actor, accountID, reason string) (*admin.Account, error)) {
	a := s.requireAdmin(w, r)
	if a == nil || !validUUID(w, "account_id", id) {
		return
	}
	reason, ok := adminReason(w, r)
	if !ok {
		return
	}
	out, err := f(r.Context(), *a, id, reason)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, adminAccountJSON(out))
}

// GrantCredits is POST /v1/admin/accounts/{account_id}/credits.
func (s *Server) GrantCredits(w http.ResponseWriter, r *http.Request, id apigen.AccountId) {
	a := s.requireAdmin(w, r)
	if a == nil || !validUUID(w, "account_id", id) {
		return
	}
	var req apigen.GrantCreditsRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	g, err := s.Admin.GrantCredits(r.Context(), *a, id, req.Amount, req.Reason)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// StopCampaign is POST /v1/admin/campaigns/{campaign_id}/stop.
func (s *Server) StopCampaign(w http.ResponseWriter, r *http.Request, id apigen.CampaignId) {
	a := s.requireAdmin(w, r)
	if a == nil || !validCampaignID(w, id) {
		return
	}
	reason, ok := adminReason(w, r)
	if !ok {
		return
	}
	c, err := s.Admin.StopCampaign(r.Context(), *a, id, reason)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, campaignJSON(c))
}

// ApproveImport is POST /v1/admin/imports/{import_id}/approve.
func (s *Server) ApproveImport(w http.ResponseWriter, r *http.Request, id apigen.ImportId) {
	a := s.requireAdmin(w, r)
	if a == nil || !validUUID(w, "import_id", id) {
		return
	}
	reason, ok := adminReason(w, r)
	if !ok {
		return
	}
	out, err := s.Admin.ApproveImport(r.Context(), *a, id, reason)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ListAdminActions is GET /v1/admin/actions.
func (s *Server) ListAdminActions(w http.ResponseWriter, r *http.Request, params apigen.ListAdminActionsParams) {
	if s.requireAdmin(w, r) == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	accountID := ""
	if params.AccountId != nil {
		if accountID = *params.AccountId; !validUUID(w, "account_id", accountID) {
			return
		}
	}
	var before int64
	if pr.After != "" {
		if before, err = strconv.ParseInt(pr.After, 10, 64); err != nil || before < 1 {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
	}
	actions, err := s.Admin.ListActions(r.Context(), accountID, before, pr.Limit+1)
	if err != nil {
		s.adminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, NewPage(actions, pr.Limit, func(a admin.Action) string { return strconv.FormatInt(a.ID, 10) }))
}
