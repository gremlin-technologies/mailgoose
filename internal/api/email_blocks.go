package api

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/gremlin-technologies/mailgoose/internal/api/apigen"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

// Block operations (DES-4, MG-20) change one block of a blocks email at a
// time. Each names the email's version, like every other email change.

var blockIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// emailBlockJSON is the API's EmailBlockEntry. The block marshals itself.
type emailBlockJSON struct {
	Block    emails.Block `json:"block"`
	Path     string       `json:"path"`
	ParentID *string      `json:"parent_id"`
	Column   *int         `json:"column"`
	Index    int          `json:"index"`
}

func toEmailBlockJSON(info emails.BlockInfo) emailBlockJSON {
	out := emailBlockJSON{Block: info.Block, Path: info.Path, Column: info.Column, Index: info.Index}
	if info.Parent != "" {
		out.ParentID = &info.Parent
	}
	return out
}

type emailBlockChangeJSON struct {
	Block emailBlockJSON `json:"block"`
	Email emailJSON      `json:"email"`
}

// blockPlace is the place fields that addEmailBlock and moveEmailBlock share.
type blockPlace struct {
	After    *string `json:"after"`
	Index    *int    `json:"index"`
	ParentID *string `json:"parent_id"`
	Column   *int    `json:"column"`
}

func (p blockPlace) position() emails.Position {
	pos := emails.Position{Index: p.Index, Column: p.Column}
	if p.After != nil {
		pos.After = *p.After
	}
	if p.ParentID != nil {
		pos.Parent = *p.ParentID
	}
	return pos
}

func validBlockID(w http.ResponseWriter, id string) bool {
	if !blockIDPattern.MatchString(id) {
		WriteError(w, BadRequest("invalid_parameter", "block_id must be a block's id.").
			WithHint("`mailgoose email blocks list <email>` shows each block's id."))
		return false
	}
	return true
}

// blockChanged writes a block change's answer: the block where it is now,
// and the email as saved.
func (s *Server) blockChanged(w http.ResponseWriter, r *http.Request, status int, e *emails.Email, blockID string) {
	info, err := emails.FindBlock(e.Document, blockID)
	if s.emailError(w, r, err) {
		return
	}
	writeJSON(w, status, emailBlockChangeJSON{Block: toEmailBlockJSON(info), Email: toEmailJSON(e)})
}

// ListEmailBlocks is GET /v1/emails/{email_id}/blocks.
func (s *Server) ListEmailBlocks(w http.ResponseWriter, r *http.Request, id string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) {
		return
	}
	e, err := s.Emails.Get(r.Context(), c.AccountID, id)
	if s.emailError(w, r, err) {
		return
	}
	if e.Kind != emails.KindBlocks {
		s.emailError(w, r, emails.ErrNotBlocks)
		return
	}
	infos := emails.ListBlocks(e.Document)
	out := struct {
		EmailID string           `json:"email_id"`
		Version int              `json:"version"`
		Data    []emailBlockJSON `json:"data"`
	}{EmailID: e.ID, Version: e.Version, Data: make([]emailBlockJSON, len(infos))}
	for i, info := range infos {
		out.Data[i] = toEmailBlockJSON(info)
	}
	writeJSON(w, http.StatusOK, out)
}

// AddEmailBlock is POST /v1/emails/{email_id}/blocks.
func (s *Server) AddEmailBlock(w http.ResponseWriter, r *http.Request, id string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) {
		return
	}
	var req struct {
		Version *int            `json:"version"`
		Block   json.RawMessage `json:"block"`
		blockPlace
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if !needVersion(w, req.Version) {
		return
	}
	if len(req.Block) == 0 || string(req.Block) == "null" {
		WriteError(w, BadRequest("invalid_parameter", "block is required: the new block, with at least its type.").
			WithHint(`For example {"type": "heading", "text": "Hello"}.`))
		return
	}
	var blockID string
	e, err := s.Emails.ChangeBlocks(r.Context(), c.AccountID, id, *req.Version, func(d *emails.Document) (*emails.Document, error) {
		nd, nid, err := emails.AddBlock(d, req.Block, req.position())
		blockID = nid
		return nd, err
	})
	if s.emailError(w, r, err) {
		return
	}
	s.blockChanged(w, r, http.StatusCreated, e, blockID)
}

// UpdateEmailBlock is PATCH /v1/emails/{email_id}/blocks/{block_id}.
func (s *Server) UpdateEmailBlock(w http.ResponseWriter, r *http.Request, id, blockID string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) || !validBlockID(w, blockID) {
		return
	}
	var req struct {
		Version *int            `json:"version"`
		Fields  json.RawMessage `json:"fields"`
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if !needVersion(w, req.Version) {
		return
	}
	if len(req.Fields) == 0 || string(req.Fields) == "null" {
		WriteError(w, BadRequest("invalid_parameter", "fields is required: the block's fields to set.").
			WithHint(`For example {"text": "Hello again"}.`))
		return
	}
	e, err := s.Emails.ChangeBlocks(r.Context(), c.AccountID, id, *req.Version, func(d *emails.Document) (*emails.Document, error) {
		return emails.UpdateBlock(d, blockID, req.Fields)
	})
	if s.emailError(w, r, err) {
		return
	}
	s.blockChanged(w, r, http.StatusOK, e, blockID)
}

// MoveEmailBlock is POST /v1/emails/{email_id}/blocks/{block_id}/move.
func (s *Server) MoveEmailBlock(w http.ResponseWriter, r *http.Request, id, blockID string) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) || !validBlockID(w, blockID) {
		return
	}
	var req struct {
		Version *int `json:"version"`
		blockPlace
	}
	if err := DecodeJSON(w, r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if !needVersion(w, req.Version) {
		return
	}
	e, err := s.Emails.ChangeBlocks(r.Context(), c.AccountID, id, *req.Version, func(d *emails.Document) (*emails.Document, error) {
		return emails.MoveBlock(d, blockID, req.position())
	})
	if s.emailError(w, r, err) {
		return
	}
	s.blockChanged(w, r, http.StatusOK, e, blockID)
}

// RemoveEmailBlock is DELETE /v1/emails/{email_id}/blocks/{block_id}.
func (s *Server) RemoveEmailBlock(w http.ResponseWriter, r *http.Request, id, blockID string, params apigen.RemoveEmailBlockParams) {
	c := s.emailsCaller(w, r)
	if c == nil || !validEmailID(w, id) || !validBlockID(w, blockID) || !needVersion(w, &params.Version) {
		return
	}
	e, err := s.Emails.ChangeBlocks(r.Context(), c.AccountID, id, params.Version, func(d *emails.Document) (*emails.Document, error) {
		return emails.RemoveBlock(d, blockID)
	})
	if s.emailError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, toEmailJSON(e))
}
