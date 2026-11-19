package api

import (
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/policy"
)

// cliUserAgent starts the User-Agent the CLI sends.
const cliUserAgent = "mailgoose-cli/"

// termsVia says how a request accepts the terms (MG-68): the CLI by its
// User-Agent or its login, the web app by its session, and anything else
// with an API key is the API. A sign-up (no caller yet) that isn't the CLI
// is the web app's form.
func termsVia(r *http.Request, c *Caller) string {
	switch {
	case strings.HasPrefix(r.UserAgent(), cliUserAgent), c != nil && c.Kind == auth.KindCLIToken:
		return auth.TermsViaCLI
	case c == nil, c.Kind == KindSession:
		return auth.TermsViaWeb
	}
	return auth.TermsViaAPI
}

func termsJSON(st *auth.Settings) apigen.TermsStatus {
	cur := policy.Current()
	t := apigen.TermsStatus{
		AcceptedVersion:  st.TermsVersion,
		CurrentVersion:   cur.ID,
		CurrentEffective: openapi_types.Date{Time: cur.EffectiveDate()},
		AcceptanceNeeded: st.TermsNeeded(),
	}
	if st.TermsAcceptedAt != nil {
		at := st.TermsAcceptedAt.UTC()
		t.AcceptedAt = &at
	}
	return t
}

// AcceptTerms is POST /v1/account/terms.
func (s *Server) AcceptTerms(w http.ResponseWriter, r *http.Request) {
	c := s.requireFull(w, r)
	if c == nil {
		return
	}
	var req apigen.AcceptTermsRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	st, err := s.Auth.AcceptTerms(r.Context(), c.AccountID, strings.TrimSpace(req.Version), termsVia(r, c))
	if s.accountError(w, r, err) {
		return
	}
	s.writeAccount(w, r, st)
}
