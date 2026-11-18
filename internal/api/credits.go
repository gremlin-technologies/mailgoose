package api

import (
	"net/http"
	"strconv"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
)

// creditsCaller is requireCaller for the credits endpoints, which also need
// the ledger.
func (s *Server) creditsCaller(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.requireCaller(w, r)
	if c != nil && s.Credits == nil {
		WriteError(w, &Error{Status: http.StatusServiceUnavailable, Code: "service_unavailable",
			Message: "Credits need a database, and none is configured."})
		return nil
	}
	return c
}

// GetCredits is GET /v1/credits.
func (s *Server) GetCredits(w http.ResponseWriter, r *http.Request) {
	c := s.creditsCaller(w, r)
	if c == nil {
		return
	}
	sum, err := s.Credits.Summary(r.Context(), c.AccountID)
	if err != nil {
		s.Log.ErrorContext(r.Context(), "reading credits failed", "err", err)
		WriteError(w, Internal())
		return
	}
	out := apigen.CreditSummary{Balance: sum.Balance, Plan: apigen.CreditSummaryPlan(sum.Plan)}
	if sum.NextGrantAt != nil {
		t := sum.NextGrantAt.UTC()
		out.NextGrantAt = &t
	}
	writeJSON(w, http.StatusOK, out)
}

// ListCreditTransactions is GET /v1/credits/transactions.
func (s *Server) ListCreditTransactions(w http.ResponseWriter, r *http.Request, _ apigen.ListCreditTransactionsParams) {
	c := s.creditsCaller(w, r)
	if c == nil {
		return
	}
	pr, err := ParsePageRequest(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	var before int64
	if pr.After != "" {
		before, err = strconv.ParseInt(pr.After, 10, 64)
		if err != nil || before <= 0 {
			WriteError(w, BadRequest("invalid_cursor", "That cursor isn't one this list gave out."))
			return
		}
	}
	txs, err := s.Credits.History(r.Context(), c.AccountID, before, pr.Limit+1)
	if err != nil {
		s.Log.ErrorContext(r.Context(), "reading credit history failed", "err", err)
		WriteError(w, Internal())
		return
	}
	page := NewPage(txs, pr.Limit, func(t credits.Transaction) string { return strconv.FormatInt(t.ID, 10) })
	out := apigen.CreditTransactionList{Data: make([]apigen.CreditTransaction, len(page.Data)), NextCursor: page.NextCursor}
	for i, t := range page.Data {
		out.Data[i] = apigen.CreditTransaction{
			Id:        strconv.FormatInt(t.ID, 10),
			Type:      apigen.CreditTransactionType(t.Type),
			Amount:    t.Amount,
			Ref:       t.Ref,
			CreatedAt: t.CreatedAt.UTC(),
		}
		if t.ExpiresAt != nil {
			e := t.ExpiresAt.UTC()
			out.Data[i].ExpiresAt = &e
		}
	}
	writeJSON(w, http.StatusOK, out)
}
