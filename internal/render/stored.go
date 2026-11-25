package render

import (
	"encoding/json"
	"fmt"
)

// storedSegment is a segment as JSON: literal text, or a merge tag and its
// fallback.
type storedSegment struct {
	Text     string `json:"text,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Fallback string `json:"fallback,omitempty"`
	// Slot is where the "Sent by mailgoose" footer goes (MG-119).
	Slot bool `json:"slot,omitempty"`
}

type storedTemplate struct {
	Subject []storedSegment `json:"subject"`
	HTML    []storedSegment `json:"html"`
	Text    []storedSegment `json:"text"`
	// BrandHTML and BrandText fill the slot in a branded copy.
	BrandHTML string `json:"brand_html,omitempty"`
	BrandText string `json:"brand_text,omitempty"`
}

func storeParts(p parts) []storedSegment {
	out := make([]storedSegment, len(p))
	for i, s := range p {
		out[i] = storedSegment{Text: s.text, Tag: s.tag, Fallback: s.fallback, Slot: s.slot}
	}
	return out
}

func loadParts(in []storedSegment) (parts, error) {
	out := make(parts, len(in))
	for i, s := range in {
		// A contact field's tag (MG-133) is kept by its key; a field
		// deleted since launch fills in as empty, taking the fallback.
		if s.Tag != "" && !Builtin(s.Tag) && !fieldKey.MatchString(s.Tag) {
			return nil, fmt.Errorf("render: stored template has unknown merge tag %q", s.Tag)
		}
		out[i] = segment{text: s.Text, tag: s.Tag, fallback: s.Fallback, slot: s.Slot}
	}
	return out, nil
}

// MarshalJSON stores a compiled template, so a launched campaign keeps the
// exact email it was launched with (CAM-8) and the send workers only fill
// in each recipient's values.
func (t *Template) MarshalJSON() ([]byte, error) {
	return json.Marshal(storedTemplate{Subject: storeParts(t.subject), HTML: storeParts(t.html), Text: storeParts(t.text),
		BrandHTML: t.brandHTML, BrandText: t.brandText})
}

// UnmarshalJSON loads a template MarshalJSON stored.
func (t *Template) UnmarshalJSON(b []byte) error {
	var st storedTemplate
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	var err error
	if t.subject, err = loadParts(st.Subject); err != nil {
		return err
	}
	if t.html, err = loadParts(st.HTML); err != nil {
		return err
	}
	if t.text, err = loadParts(st.Text); err != nil {
		return err
	}
	t.brandHTML, t.brandText = st.BrandHTML, st.BrandText
	return nil
}
