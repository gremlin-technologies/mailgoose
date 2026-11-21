// Package emails stores the account's emails: a block document (DES-1,
// DES-2) or hand-written HTML (DES-9), with the settings both kinds share.
//
// A document is checked whole whenever it's saved. ParseDocument is the one
// way in, so a stored document always has known block types and fields,
// every block has an id that's unique in the document, and a blocks email
// ends with exactly one footer.
package emails

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SchemaVersion is the block document schema this code reads and writes.
const SchemaVersion = 1

// Kinds of email.
const (
	KindBlocks = "blocks"
	KindHTML   = "html"
)

// Block types (DES-2).
const (
	TypeHeading = "heading"
	TypeText    = "text"
	TypeImage   = "image"
	TypeButton  = "button"
	TypeDivider = "divider"
	TypeSpacer  = "spacer"
	TypeColumns = "columns"
	TypeSocial  = "social"
	TypeFooter  = "footer"
)

// BlockTypes lists every block type, in the order the docs give them.
var BlockTypes = []string{TypeHeading, TypeText, TypeImage, TypeButton, TypeDivider, TypeSpacer, TypeColumns, TypeSocial, TypeFooter}

// Limits on a document.
const (
	MaxBlocks      = 200     // in the whole document, counting blocks inside columns
	MaxHTMLBytes   = 512_000 // the API refuses over MaxSendableHTMLBytes
	MaxTextChars   = 20_000
	MaxSocialLinks = 10
)

// SocialNetworks are the networks a social block links to.
var SocialNetworks = []string{"facebook", "instagram", "x", "linkedin", "youtube", "tiktok", "threads", "bluesky", "website"}

// Settings are the parts of an email outside its content.
type Settings struct {
	Subject         string  `json:"subject"`
	PreviewText     string  `json:"preview_text"`
	FromName        string  `json:"from_name"`
	FromEmail       *string `json:"from_email"`
	ReplyTo         *string `json:"reply_to"`
	BackgroundColor string  `json:"background_color"`
	ContentWidth    int     `json:"content_width"`
}

// DefaultSettings are what a new email starts with, and what a document
// leaves out takes.
func DefaultSettings() Settings {
	return Settings{BackgroundColor: "#f4f4f5", ContentWidth: 600}
}

// Block is one block of a document. Each type is its own struct below.
type Block interface {
	BlockID() string
	BlockType() string
	setID(string)
}

type base struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

func (b *base) BlockID() string   { return b.ID }
func (b *base) BlockType() string { return b.Type }
func (b *base) setID(id string)   { b.ID = id }

// Heading is a title line.
type Heading struct {
	base
	Text  string `json:"text"`
	Level int    `json:"level"`
	Align string `json:"align"`
}

// Text is a paragraph or more. Text holds a small Markdown subset: **bold**,
// *italic*, [links](https://...), "- " and "1. " lists, and blank lines
// between paragraphs. The renderer (MG-21) turns it into HTML.
type Text struct {
	base
	Text  string `json:"text"`
	Align string `json:"align"`
}

// Image shows a picture, optionally linked.
type Image struct {
	base
	Src   string  `json:"src"`
	Alt   string  `json:"alt"`
	Href  *string `json:"href"`
	Width *int    `json:"width"`
	Align string  `json:"align"`
}

// Button is a link drawn as a button.
type Button struct {
	base
	Text            string `json:"text"`
	Href            string `json:"href"`
	Align           string `json:"align"`
	BackgroundColor string `json:"background_color"`
	TextColor       string `json:"text_color"`
}

// Divider is a horizontal rule.
type Divider struct {
	base
	Color     string `json:"color"`
	Thickness int    `json:"thickness"`
}

// Spacer is empty vertical space.
type Spacer struct {
	base
	Height int `json:"height"`
}

// Column is one column of a Columns block.
type Column struct {
	Blocks []Block `json:"blocks"`
}

// Columns is a row of one to four columns with blocks inside each. Layout
// says how many columns there are and how wide each is.
type Columns struct {
	base
	Layout  string   `json:"layout"`
	Columns []Column `json:"columns"`
}

// DefaultColumnLayout is the layout of a columns block that names none
// (MG-110): two equal columns. A layout gives the columns' relative widths.
const DefaultColumnLayout = "1:1"

// ColumnLayouts are the layouts a columns block may have, in the order the
// designer offers them.
var ColumnLayouts = []string{"1", "1:1", "1:1:1", "1:1:1:1", "1:2", "2:1", "1:3", "3:1"}

// LayoutWidths returns a layout's relative column widths, or nil when it
// isn't one of ColumnLayouts.
func LayoutWidths(layout string) []int {
	known := false
	for _, l := range ColumnLayouts {
		known = known || l == layout
	}
	if !known {
		return nil
	}
	parts := strings.Split(layout, ":")
	out := make([]int, len(parts))
	for i, p := range parts {
		out[i] = int(p[0] - '0')
	}
	return out
}

// SocialLink is one network's link in a Social block.
type SocialLink struct {
	Network string `json:"network"`
	URL     string `json:"url"`
}

// Social is a row of social network icons.
type Social struct {
	base
	Links []SocialLink `json:"links"`
	Align string       `json:"align"`
}

// Footer is the locked last block. The renderer adds the account's postal
// address and the unsubscribe link to it; Text is an optional line above
// them.
type Footer struct {
	base
	Text string `json:"text"`
}

// Document is an email's settings and content. Blocks is set for a blocks
// email and HTML for an html email; the other is nil.
type Document struct {
	SchemaVersion int
	Settings      Settings
	Blocks        []Block
	HTML          *string
}

// Kind is KindHTML when the document holds HTML and KindBlocks otherwise.
func (d *Document) Kind() string {
	if d.HTML != nil {
		return KindHTML
	}
	return KindBlocks
}

// MarshalJSON writes the document as the API's BlockDocument or
// HtmlDocument.
func (d *Document) MarshalJSON() ([]byte, error) {
	if d.HTML != nil {
		return json.Marshal(struct {
			SchemaVersion int      `json:"schema_version"`
			Settings      Settings `json:"settings"`
			HTML          string   `json:"html"`
		}{d.SchemaVersion, d.Settings, *d.HTML})
	}
	blocks := d.Blocks
	if blocks == nil {
		blocks = []Block{}
	}
	return json.Marshal(struct {
		SchemaVersion int      `json:"schema_version"`
		Settings      Settings `json:"settings"`
		Blocks        []Block  `json:"blocks"`
	}{d.SchemaVersion, d.Settings, blocks})
}

// MarshalJSON writes an empty column's blocks as [] rather than null.
func (c Column) MarshalJSON() ([]byte, error) {
	blocks := c.Blocks
	if blocks == nil {
		blocks = []Block{}
	}
	return json.Marshal(struct {
		Blocks []Block `json:"blocks"`
	}{blocks})
}

// NewDocument is the document a new email of kind starts with: default
// settings and, for blocks, just the footer.
func NewDocument(kind string) *Document {
	d := &Document{SchemaVersion: SchemaVersion, Settings: DefaultSettings()}
	if kind == KindHTML {
		empty := ""
		d.HTML = &empty
		return d
	}
	d.Blocks = []Block{newFooter()}
	return d
}

func newFooter() *Footer {
	return &Footer{base: base{ID: NewBlockID(), Type: TypeFooter}}
}

// DocError says what's wrong with a document and where, as a path such as
// blocks[2].columns[0].blocks[1].href.
type DocError struct {
	Path    string
	Message string
}

func (e *DocError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

func docErr(path, format string, args ...any) error {
	return &DocError{Path: path, Message: fmt.Sprintf(format, args...)}
}

// ParseDocument reads and checks a document for an email of kind. Settings
// and block fields it leaves out take their defaults, a block without an id
// gets a new one, and a blocks document without a footer gets one at the
// end. Anything else wrong is a *DocError.
func ParseDocument(kind string, raw []byte) (*Document, error) {
	var in struct {
		SchemaVersion *int              `json:"schema_version"`
		Settings      json.RawMessage   `json:"settings"`
		Blocks        []json.RawMessage `json:"blocks"`
		HTML          *string           `json:"html"`
	}
	if err := strictDecode(raw, &in); err != nil {
		return nil, decodeErr("", "documents", err)
	}
	if in.SchemaVersion != nil && *in.SchemaVersion != SchemaVersion {
		return nil, docErr("schema_version", "must be %d, the only schema version there is", SchemaVersion)
	}
	d := &Document{SchemaVersion: SchemaVersion, Settings: DefaultSettings()}
	if len(in.Settings) > 0 && string(in.Settings) != "null" {
		s, err := ParseSettings(d.Settings, in.Settings)
		if err != nil {
			return nil, err
		}
		d.Settings = s
	}

	switch kind {
	case KindHTML:
		if in.Blocks != nil {
			return nil, docErr("blocks", "an html email has no blocks; put its markup in html")
		}
		if in.HTML == nil {
			return nil, docErr("html", "an html email needs html")
		}
		if len(*in.HTML) > MaxHTMLBytes {
			return nil, docErr("html", "is over %d bytes", MaxHTMLBytes)
		}
		if !utf8.ValidString(*in.HTML) {
			return nil, docErr("html", "isn't valid UTF-8")
		}
		d.HTML = in.HTML
		return d, nil
	case KindBlocks:
	default:
		return nil, fmt.Errorf("emails: unknown kind %q", kind)
	}

	if in.HTML != nil {
		return nil, docErr("html", "a blocks email has no html; its content is blocks")
	}
	p := &parser{ids: map[string]string{}}
	blocks, err := p.blocks(in.Blocks, "blocks", false)
	if err != nil {
		return nil, err
	}
	for i, b := range blocks {
		if b.BlockType() == TypeFooter && i != len(blocks)-1 {
			return nil, docErr(fmt.Sprintf("blocks[%d]", i), "the footer must be the last block")
		}
	}
	if len(blocks) == 0 || blocks[len(blocks)-1].BlockType() != TypeFooter {
		if p.count >= MaxBlocks {
			return nil, docErr("blocks", "has more than %d blocks", MaxBlocks-1)
		}
		blocks = append(blocks, newFooter())
	}
	if err := p.assignIDs(blocks); err != nil {
		return nil, err
	}
	d.Blocks = blocks
	return d, nil
}

// ParseSettings reads settings, as a whole or in part, over base: a field
// raw leaves out keeps base's value. from_email and reply_to may be null.
func ParseSettings(base Settings, raw []byte) (Settings, error) {
	s := base
	if err := strictDecode(raw, &s); err != nil {
		return base, decodeErr("settings", "settings", err)
	}
	// A null for a field that can't be null leaves it as it was in
	// json.Unmarshal; refuse it instead, so null never means "no change".
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	for k, v := range keys {
		if string(v) == "null" && k != "from_email" && k != "reply_to" {
			return base, docErr("settings."+k, "can't be null")
		}
	}
	switch {
	case utf8.RuneCountInString(s.Subject) > 200:
		return base, docErr("settings.subject", "is over 200 characters")
	case strings.ContainsAny(s.Subject, "\r\n"):
		return base, docErr("settings.subject", "can't contain line breaks")
	case utf8.RuneCountInString(s.PreviewText) > 200:
		return base, docErr("settings.preview_text", "is over 200 characters")
	case utf8.RuneCountInString(s.FromName) > 100:
		return base, docErr("settings.from_name", "is over 100 characters")
	case strings.ContainsAny(s.FromName, "\r\n\"<>"):
		return base, docErr("settings.from_name", `can't contain line breaks, quotes or < >`)
	case !hexColor.MatchString(s.BackgroundColor):
		return base, docErr("settings.background_color", "must be a color like #f4f4f5")
	case s.ContentWidth < 480 || s.ContentWidth > 800:
		return base, docErr("settings.content_width", "must be 480 to 800 pixels")
	}
	for path, addr := range map[string]**string{"settings.from_email": &s.FromEmail, "settings.reply_to": &s.ReplyTo} {
		if *addr == nil {
			continue
		}
		v := strings.TrimSpace(**addr)
		if v == "" {
			*addr = nil
			continue
		}
		if !plainAddress(v) {
			return base, docErr(path, "must be an email address like news@example.com")
		}
		*addr = &v
	}
	return s, nil
}

func plainAddress(v string) bool {
	a, err := mail.ParseAddress(v)
	return err == nil && a.Address == v && a.Name == "" && len(v) <= 254
}

var (
	hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	blockID  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
)

// parser checks blocks and keeps the ids it has seen.
type parser struct {
	ids   map[string]string // id -> path of the block that has it
	count int
}

// blocks parses a list of blocks at path; nested is true inside a column,
// where columns and footers aren't allowed.
func (p *parser) blocks(raws []json.RawMessage, path string, nested bool) ([]Block, error) {
	if raws == nil {
		return nil, docErr(path, "is missing; give a list of blocks")
	}
	out := make([]Block, 0, len(raws))
	for i, raw := range raws {
		b, err := p.block(raw, fmt.Sprintf("%s[%d]", path, i), nested)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (p *parser) block(raw json.RawMessage, path string, nested bool) (Block, error) {
	p.count++
	if p.count > MaxBlocks {
		return nil, docErr(path, "is one block too many; a document holds at most %d", MaxBlocks)
	}
	var head struct {
		Type *string `json:"type"`
		ID   *string `json:"id"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, docErr(path, "must be a block object with a type")
	}
	if head.Type == nil {
		return nil, docErr(path+".type", "is missing; it's one of %s", strings.Join(BlockTypes, ", "))
	}
	t := *head.Type
	var b Block
	bs := base{Type: t}
	switch t {
	case TypeHeading:
		b = &Heading{base: bs, Level: 1, Align: "left"}
	case TypeText:
		b = &Text{base: bs, Align: "left"}
	case TypeImage:
		b = &Image{base: bs, Align: "center"}
	case TypeButton:
		b = &Button{base: bs, Align: "center", BackgroundColor: "#18181b", TextColor: "#ffffff"}
	case TypeDivider:
		b = &Divider{base: bs, Color: "#e4e4e7", Thickness: 1}
	case TypeSpacer:
		b = &Spacer{base: bs, Height: 24}
	case TypeColumns:
		if nested {
			return nil, docErr(path+".type", "a column can't hold another columns block")
		}
		return p.columns(raw, path)
	case TypeSocial:
		if err := checkLinks(raw, path); err != nil {
			return nil, err
		}
		b = &Social{base: bs, Align: "center"}
	case TypeFooter:
		if nested {
			return nil, docErr(path+".type", "the footer can't go inside a column")
		}
		b = &Footer{base: bs}
	default:
		return nil, docErr(path+".type", "%q isn't a block type; use one of %s", t, strings.Join(BlockTypes, ", "))
	}
	if err := strictDecode(raw, b); err != nil {
		return nil, decodeErr(path, t+" blocks", err)
	}
	if err := checkNulls(raw, path, "href", "width"); err != nil {
		return nil, err
	}
	if err := checkBlock(b, path); err != nil {
		return nil, err
	}
	return b, p.noteID(b, path)
}

func (p *parser) columns(raw json.RawMessage, path string) (Block, error) {
	var in struct {
		base
		Layout  *string           `json:"layout"`
		Columns []json.RawMessage `json:"columns"`
	}
	if err := strictDecode(raw, &in); err != nil {
		return nil, decodeErr(path, "columns blocks", err)
	}
	layout := DefaultColumnLayout
	if in.Layout != nil {
		layout = *in.Layout
	}
	widths := LayoutWidths(layout)
	if widths == nil {
		return nil, docErr(path+".layout", "must be one of %s", strings.Join(ColumnLayouts, ", "))
	}
	if in.Columns == nil {
		// A new columns block with no columns starts with empty ones.
		for range widths {
			in.Columns = append(in.Columns, json.RawMessage(`{"blocks":[]}`))
		}
	}
	if len(in.Columns) != len(widths) {
		return nil, docErr(path+".columns", "must hold %s for the %s layout, not %d", plural(len(widths), "column"), layout, len(in.Columns))
	}
	b := &Columns{base: in.base, Layout: layout, Columns: make([]Column, len(widths))}
	for i, rawCol := range in.Columns {
		var col struct {
			Blocks []json.RawMessage `json:"blocks"`
		}
		colPath := fmt.Sprintf("%s.columns[%d]", path, i)
		if err := strictDecode(rawCol, &col); err != nil {
			return nil, decodeErr(colPath, "columns", err)
		}
		if col.Blocks == nil {
			col.Blocks = []json.RawMessage{}
		}
		blocks, err := p.blocks(col.Blocks, colPath+".blocks", true)
		if err != nil {
			return nil, err
		}
		b.Columns[i].Blocks = blocks
	}
	return b, p.noteID(b, path)
}

// noteID checks a block's own id, if it has one, is well formed and not
// already used in the document.
func (p *parser) noteID(b Block, path string) error {
	id := b.BlockID()
	if id == "" {
		return nil
	}
	if !blockID.MatchString(id) {
		return docErr(path+".id", "must be 1 to 40 letters, digits, - or _; leave it out to get a new one")
	}
	if prev, ok := p.ids[id]; ok {
		return docErr(path+".id", "%q is already the id of %s", id, prev)
	}
	p.ids[id] = path
	return nil
}

// assignIDs gives every block without an id a new one, after all the ids
// the document brought have been seen.
func (p *parser) assignIDs(blocks []Block) error {
	for _, b := range blocks {
		if b.BlockID() == "" {
			id := NewBlockID()
			for p.ids[id] != "" {
				id = NewBlockID()
			}
			p.ids[id] = "new"
			b.setID(id)
		}
		if c, ok := b.(*Columns); ok {
			for _, col := range c.Columns {
				if err := p.assignIDs(col.Blocks); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// NewBlockID makes a block id: "b_" and ten random lowercase letters and
// digits.
func NewBlockID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	for i, c := range buf {
		buf[i] = alphabet[int(c)%len(alphabet)]
	}
	return "b_" + string(buf)
}

func checkBlock(b Block, path string) error {
	align := func(a string) error {
		if a != "left" && a != "center" && a != "right" {
			return docErr(path+".align", "must be left, center or right")
		}
		return nil
	}
	color := func(field, v string) error {
		if !hexColor.MatchString(v) {
			return docErr(path+"."+field, "must be a color like #18181b")
		}
		return nil
	}
	switch b := b.(type) {
	case *Heading:
		if err := needText(path+".text", b.Text, 500); err != nil {
			return err
		}
		if strings.ContainsAny(b.Text, "\r\n") {
			return docErr(path+".text", "can't contain line breaks; use a text block for more lines")
		}
		if b.Level < 1 || b.Level > 3 {
			return docErr(path+".level", "must be 1, 2 or 3")
		}
		return align(b.Align)
	case *Text:
		if err := needText(path+".text", b.Text, MaxTextChars); err != nil {
			return err
		}
		return align(b.Align)
	case *Image:
		if err := linkURL(path+".src", b.Src, false); err != nil {
			return err
		}
		if utf8.RuneCountInString(b.Alt) > 500 {
			return docErr(path+".alt", "is over 500 characters")
		}
		if b.Href != nil {
			if err := linkURL(path+".href", *b.Href, true); err != nil {
				return err
			}
		}
		if b.Width != nil && (*b.Width < 1 || *b.Width > 800) {
			return docErr(path+".width", "must be 1 to 800 pixels, or null for the full width")
		}
		return align(b.Align)
	case *Button:
		if err := needText(path+".text", b.Text, 100); err != nil {
			return err
		}
		if err := linkURL(path+".href", b.Href, true); err != nil {
			return err
		}
		if err := color("background_color", b.BackgroundColor); err != nil {
			return err
		}
		if err := color("text_color", b.TextColor); err != nil {
			return err
		}
		return align(b.Align)
	case *Divider:
		if b.Thickness < 1 || b.Thickness > 10 {
			return docErr(path+".thickness", "must be 1 to 10 pixels")
		}
		return color("color", b.Color)
	case *Spacer:
		if b.Height < 4 || b.Height > 200 {
			return docErr(path+".height", "must be 4 to 200 pixels")
		}
	case *Social:
		if len(b.Links) == 0 || len(b.Links) > MaxSocialLinks {
			return docErr(path+".links", "must hold 1 to %d links", MaxSocialLinks)
		}
		for i, l := range b.Links {
			lp := fmt.Sprintf("%s.links[%d]", path, i)
			known := false
			for _, n := range SocialNetworks {
				known = known || l.Network == n
			}
			if !known {
				return docErr(lp+".network", "must be one of %s", strings.Join(SocialNetworks, ", "))
			}
			if err := linkURL(lp+".url", l.URL, false); err != nil {
				return err
			}
		}
		return align(b.Align)
	case *Footer:
		if utf8.RuneCountInString(b.Text) > 1000 {
			return docErr(path+".text", "is over 1000 characters")
		}
	}
	return nil
}

func needText(path, v string, max int) error {
	if strings.TrimSpace(v) == "" {
		return docErr(path, "is empty")
	}
	if utf8.RuneCountInString(v) > max {
		return docErr(path, "is over %d characters", max)
	}
	return nil
}

// linkURL checks an absolute http or https URL; mailto also counts for a
// link someone clicks. A link may be exactly {{unsubscribe_url}}; any
// other merge tag may only come after a fixed address (FixedLinkHost),
// and the renderer (MG-21) checks the tags themselves.
func linkURL(path, v string, link bool) error {
	if link && !FixedLinkHost(v) {
		return docErr(path, "%s", TagLinkMessage)
	}
	if link && unsubscribeHref.MatchString(v) {
		return nil
	}
	u, err := url.Parse(v)
	ok := err == nil && len(v) <= 2000
	if ok {
		switch u.Scheme {
		case "http", "https":
			ok = u.Host != ""
		case "mailto":
			ok = link && u.Opaque != ""
		default:
			ok = false
		}
	}
	if !ok {
		return docErr(path, "must be a full https:// address")
	}
	return nil
}

// checkLinks checks each of a social block's links on its own, so an
// unknown field's path names the link.
func checkLinks(raw json.RawMessage, path string) error {
	var in struct {
		Links []json.RawMessage `json:"links"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil // the whole decode reports it
	}
	for i, l := range in.Links {
		var link SocialLink
		if err := strictDecode(l, &link); err != nil {
			return decodeErr(fmt.Sprintf("%s.links[%d]", path, i), "social links", err)
		}
	}
	return nil
}

// checkNulls refuses null for fields that may be null only where the
// struct says so: json.Unmarshal would quietly keep the default otherwise.
func checkNulls(raw json.RawMessage, path string, nullable ...string) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil
	}
	for k, v := range keys {
		if string(v) != "null" {
			continue
		}
		allowed := false
		for _, n := range nullable {
			allowed = allowed || k == n
		}
		if !allowed {
			return docErr(path+"."+k, "can't be null")
		}
	}
	return nil
}

func strictDecode(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// decodeErr turns a JSON decoding error into a DocError at path. what names
// the thing for "X have no field" messages.
func decodeErr(path, what string, err error) error {
	join := func(field string) string {
		if path == "" {
			return field
		}
		if field == "" {
			return path
		}
		return path + "." + field
	}
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &typeErr):
		field := typeErr.Field
		if i := strings.LastIndex(field, "."); i >= 0 && path != "" {
			field = field[i+1:]
		}
		return docErr(join(field), "must be %s, not a JSON %s", jsonKind(typeErr.Type.Kind().String()), typeErr.Value)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return docErr(join(field), "isn't a field %s have", what)
	default:
		return docErr(path, "isn't valid JSON for this: %v", err)
	}
}

func jsonKind(goKind string) string {
	switch goKind {
	case "string":
		return "a string"
	case "int", "int64":
		return "a whole number"
	case "bool":
		return "true or false"
	case "slice":
		return "a list"
	case "struct", "map":
		return "an object"
	case "ptr":
		return "a value"
	default:
		return "a " + goKind
	}
}

// plural is "1 column" or "3 columns".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
