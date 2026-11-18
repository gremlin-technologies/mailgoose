package api

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ListApiKeys is GET /v1/api-keys.
func (s *Server) ListApiKeys(w http.ResponseWriter, r *http.Request, _ apigen.ListApiKeysParams) {
	c := s.requireFull(w, r)
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
	keys, err := s.Auth.ListKeys(r.Context(), c.AccountID, pr.After, pr.Limit+1)
	if s.authError(w, r, err) {
		return
	}
	page := NewPage(keys, pr.Limit, func(k auth.Key) string { return k.ID })
	out := apigen.ApiKeyList{Data: make([]apigen.ApiKey, len(page.Data)), NextCursor: page.NextCursor}
	for i, k := range page.Data {
		out.Data[i] = keyJSON(k)
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateApiKey is POST /v1/api-keys.
func (s *Server) CreateApiKey(w http.ResponseWriter, r *http.Request) {
	c := s.requireFull(w, r)
	if c == nil {
		return
	}
	var req apigen.CreateApiKeyRequest
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	k, err := s.Auth.CreateKey(r.Context(), c.AccountID, c.UserID, req.Name, string(req.Scope))
	if s.authError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusCreated, apigen.CreatedApiKey{Key: keyJSON(k.Key), Secret: k.Secret})
}

// RevokeApiKey is DELETE /v1/api-keys/{key_id}.
func (s *Server) RevokeApiKey(w http.ResponseWriter, r *http.Request, keyID string) {
	c := s.requireFull(w, r)
	if c == nil {
		return
	}
	if !uuidPattern.MatchString(keyID) {
		WriteError(w, BadRequest("invalid_parameter", "key_id must be a key's id."))
		return
	}
	err := s.Auth.RevokeKey(r.Context(), c.AccountID, keyID)
	if errors.Is(err, auth.ErrKeyNotFound) {
		WriteError(w, NotFound("not_found", "No active API key with that id in this account.").
			WithHint("`mailgoose keys list` shows the account's keys."))
		return
	}
	if s.authError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func keyJSON(k auth.Key) apigen.ApiKey {
	out := apigen.ApiKey{
		Id:        k.ID,
		Name:      k.Name,
		Scope:     apigen.ApiKeyScope(k.Scope),
		Hint:      k.Hint,
		CreatedAt: k.CreatedAt.UTC(),
	}
	if k.LastUsedAt != nil {
		t := k.LastUsedAt.UTC()
		out.LastUsedAt = &t
	}
	return out
}
