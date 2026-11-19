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
}

type storedTemplate struct {
	Subject []storedSegment `json:"subject"`
	HTML    []storedSegment `json:"html"`
	Text    []storedSegment `json:"text"`
}

func storeParts(p parts) []storedSegment {
	out := make([]storedSegment, len(p))
	for i, s := range p {
		out[i] = storedSegment{Text: s.text, Tag: s.tag, Fallback: s.fallback}
	}
	return out
}

func loadParts(in []storedSegment) (parts, error) {
	out := make(parts, len(in))
	for i, s := range in {
		if s.Tag != "" && !knownTag(s.Tag) {
			return nil, fmt.Errorf("render: stored template has unknown merge tag %q", s.Tag)
		}
		out[i] = segment{text: s.Text, tag: s.Tag, fallback: s.Fallback}
	}
	return out, nil
}

// MarshalJSON stores a compiled template, so a launched campaign keeps the
// exact email it was launched with (CAM-8) and the send workers only fill
// in each recipient's values.
func (t *Template) MarshalJSON() ([]byte, error) {
	return json.Marshal(storedTemplate{Subject: storeParts(t.subject), HTML: storeParts(t.html), Text: storeParts(t.text)})
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
	t.text, err = loadParts(st.Text)
	return err
}

func knownTag(tag string) bool {
	for _, t := range Tags {
		if t == tag {
			return true
		}
	}
	return false
}
