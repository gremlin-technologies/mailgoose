package emails

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Block operations (DES-4) change one block of a blocks document at a time:
// add, update, move and remove, at the top level or inside a columns
// block's columns. Each works on the document's JSON and then reads the
// whole result back with ParseDocument, so every rule a whole document has
// (types, fields, ids, limits, where columns and the footer may go) holds
// for a single change too.

var (
	// ErrBlockNotFound means the document has no block with that id.
	ErrBlockNotFound = errors.New("block not found")
	// ErrNotBlocks means the email is html, which has no blocks.
	ErrNotBlocks = errors.New("an html email has no blocks")
	// ErrFooterLocked means the change would remove the footer, move it, add
	// a second one, or put a block after it.
	ErrFooterLocked = errors.New("the footer stays the last block")
)

// PositionError says why a position can't be used.
type PositionError struct {
	Message string
}

func (e *PositionError) Error() string { return e.Message }

func posErr(format string, args ...any) error {
	return &PositionError{Message: fmt.Sprintf(format, args...)}
}

// Position says where a block goes. With After, it goes right after that
// block, in the same list. Otherwise the list is the top level, or column
// Column of the columns block Parent, and Index is the place in that list
// (0 is first; the block ends up at that index). With no Index the block
// goes last, which at the top level means just before the footer.
type Position struct {
	Index  *int
	After  string
	Parent string
	Column *int
}

// BlockInfo is a block and where it is.
type BlockInfo struct {
	Block Block
	// Path is the block's place, as in a DocError: blocks[2] or
	// blocks[2].columns[0].blocks[1].
	Path string
	// Parent is the columns block holding it, and Column which of its
	// columns; both are empty at the top level.
	Parent string
	Column *int
	Index  int
}

// ListBlocks returns every block in document order, a columns block's
// contents right after it.
func ListBlocks(d *Document) []BlockInfo {
	var out []BlockInfo
	for i, b := range d.Blocks {
		out = append(out, BlockInfo{Block: b, Path: fmt.Sprintf("blocks[%d]", i), Index: i})
		cols, ok := b.(*Columns)
		if !ok {
			continue
		}
		for c, col := range cols.Columns {
			for j, inner := range col.Blocks {
				out = append(out, BlockInfo{Block: inner, Path: fmt.Sprintf("blocks[%d].columns[%d].blocks[%d]", i, c, j),
					Parent: b.BlockID(), Column: &c, Index: j})
			}
		}
	}
	return out
}

// FindBlock returns the block with id, or ErrBlockNotFound.
func FindBlock(d *Document, id string) (BlockInfo, error) {
	for _, info := range ListBlocks(d) {
		if info.Block.BlockID() == id {
			return info, nil
		}
	}
	return BlockInfo{}, ErrBlockNotFound
}

// AddBlock puts the block in raw (a JSON object with at least a type) at
// pos, and returns the new document and the block's id. A block without an
// id gets a new one.
func AddBlock(d *Document, raw json.RawMessage, pos Position) (*Document, string, error) {
	t, err := toTree(d)
	if err != nil {
		return nil, "", err
	}
	block, err := objectOf(raw, "block")
	if err != nil {
		return nil, "", err
	}
	if typ, _ := block["type"].(string); typ == TypeFooter {
		return nil, "", ErrFooterLocked
	}
	id, _ := block["id"].(string)
	if id == "" {
		id = NewBlockID()
		block["id"] = id
	} else if _, _, found := t.find(id); found {
		return nil, "", docErr("block.id", "%q is already a block in this email", id)
	}
	if err := t.insert(block, pos); err != nil {
		return nil, "", err
	}
	nd, err := t.document()
	return nd, id, err
}

// UpdateBlock sets the fields in raw (a JSON object) on the block with id.
// Fields left out keep their values; a field set to null clears it where
// the schema allows null. The id and type can't change.
func UpdateBlock(d *Document, id string, raw json.RawMessage) (*Document, error) {
	t, err := toTree(d)
	if err != nil {
		return nil, err
	}
	fields, err := objectOf(raw, "fields")
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, docErr("fields", "names no field to change")
	}
	for _, k := range []string{"id", "type"} {
		if _, ok := fields[k]; ok {
			return nil, docErr("fields."+k, "can't change; add a new block and remove this one instead")
		}
	}
	s, i, found := t.find(id)
	if !found {
		return nil, ErrBlockNotFound
	}
	block := s.list()[i].(map[string]any)
	for k, v := range fields {
		block[k] = v
	}
	return t.document()
}

// MoveBlock moves the block with id to pos. The footer can't move.
func MoveBlock(d *Document, id string, pos Position) (*Document, error) {
	t, err := toTree(d)
	if err != nil {
		return nil, err
	}
	s, i, found := t.find(id)
	if !found {
		return nil, ErrBlockNotFound
	}
	block := s.list()[i].(map[string]any)
	if block["type"] == TypeFooter {
		return nil, ErrFooterLocked
	}
	if pos.After == id {
		return nil, posErr("a block can't go after itself")
	}
	if pos.Parent == id {
		return nil, posErr("a columns block can't go inside itself")
	}
	s.remove(i)
	if err := t.insert(block, pos); err != nil {
		return nil, err
	}
	return t.document()
}

// RemoveBlock removes the block with id, and a columns block's contents
// with it. The footer can't be removed.
func RemoveBlock(d *Document, id string) (*Document, error) {
	t, err := toTree(d)
	if err != nil {
		return nil, err
	}
	s, i, found := t.find(id)
	if !found {
		return nil, ErrBlockNotFound
	}
	if s.list()[i].(map[string]any)["type"] == TypeFooter {
		return nil, ErrFooterLocked
	}
	s.remove(i)
	return t.document()
}

// tree is a blocks document as decoded JSON, numbers kept exact.
type tree struct {
	root map[string]any
}

func toTree(d *Document) (*tree, error) {
	if d.Kind() != KindBlocks {
		return nil, ErrNotBlocks
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := decodeNumbers(raw, &root); err != nil {
		return nil, err
	}
	return &tree{root: root}, nil
}

func (t *tree) document() (*Document, error) {
	raw, err := json.Marshal(t.root)
	if err != nil {
		return nil, err
	}
	return ParseDocument(KindBlocks, raw)
}

// slot is one list of blocks: the top level, or one column's.
type slot struct {
	holder map[string]any
	top    bool
}

func (s slot) list() []any {
	l, _ := s.holder["blocks"].([]any)
	return l
}

func (s slot) remove(i int) {
	l := s.list()
	s.holder["blocks"] = append(l[:i:i], l[i+1:]...)
}

func (s slot) insertAt(i int, block map[string]any) {
	l := s.list()
	out := make([]any, 0, len(l)+1)
	out = append(out, l[:i]...)
	out = append(out, block)
	s.holder["blocks"] = append(out, l[i:]...)
}

func (t *tree) top() slot { return slot{holder: t.root, top: true} }

// columnsOf returns a columns block's column holders.
func columnsOf(block map[string]any) []map[string]any {
	cols, _ := block["columns"].([]any)
	out := make([]map[string]any, 0, len(cols))
	for _, c := range cols {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// find returns the list holding the block with id and its index there.
func (t *tree) find(id string) (slot, int, bool) {
	top := t.top()
	for i, b := range top.list() {
		block, _ := b.(map[string]any)
		if block["id"] == id {
			return top, i, true
		}
		for _, col := range columnsOf(block) {
			s := slot{holder: col}
			for j, inner := range s.list() {
				if m, _ := inner.(map[string]any); m["id"] == id {
					return s, j, true
				}
			}
		}
	}
	return slot{}, 0, false
}

// insert puts block at pos.
func (t *tree) insert(block map[string]any, pos Position) error {
	if pos.After != "" {
		if pos.Index != nil || pos.Parent != "" || pos.Column != nil {
			return posErr("give after on its own, or index with parent_id and column")
		}
		s, i, found := t.find(pos.After)
		if !found {
			return posErr("after: no block %q in this email", pos.After)
		}
		if s.top && i == len(s.list())-1 {
			return ErrFooterLocked
		}
		s.insertAt(i+1, block)
		return nil
	}

	s := t.top()
	switch {
	case pos.Parent != "":
		ps, pi, found := t.find(pos.Parent)
		if !found || !ps.top {
			return posErr("parent_id: no columns block %q at the top level of this email", pos.Parent)
		}
		parent := ps.list()[pi].(map[string]any)
		if parent["type"] != TypeColumns {
			return posErr("parent_id: %q is a %v block; only a columns block holds blocks", pos.Parent, parent["type"])
		}
		cols := columnsOf(parent)
		if pos.Column == nil {
			return posErr("column is required with parent_id: 0 for the left column, 1 for the right")
		}
		if *pos.Column < 0 || *pos.Column >= len(cols) {
			return posErr("column must be 0 or 1")
		}
		s = slot{holder: cols[*pos.Column]}
	case pos.Column != nil:
		return posErr("column needs parent_id, the columns block's id")
	}

	n := len(s.list())
	limit := n // a column takes a block at its end
	if s.top {
		limit = n - 1 // the top level ends with the footer
	}
	i := limit
	if pos.Index != nil {
		i = *pos.Index
	}
	switch {
	case i < 0:
		return posErr("index can't be negative")
	case s.top && i > limit:
		return ErrFooterLocked
	case i > limit:
		return posErr("index %d is past the end of the column, which has %d blocks", i, n)
	}
	s.insertAt(i, block)
	return nil
}

// objectOf decodes raw as a JSON object, numbers kept exact.
func objectOf(raw json.RawMessage, what string) (map[string]any, error) {
	var m map[string]any
	if len(bytes.TrimSpace(raw)) == 0 || decodeNumbers(raw, &m) != nil || m == nil {
		return nil, docErr(what, "must be a JSON object")
	}
	return m, nil
}

func decodeNumbers(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data")
	}
	return nil
}
