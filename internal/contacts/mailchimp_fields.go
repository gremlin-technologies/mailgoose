package contacts

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// Mailchimp merge fields as contact fields (MG-132). An import's merge
// fields are chosen the way a CSV import's columns are (MG-131): each fills
// one of the account's contact fields, matched by name or named, becomes a
// new field, or is left out. A merge field stands where a column would,
// named by its name in the preview. With no new fields asked for, the
// import makes one for each merge field not matched, in Mailchimp's order,
// while the plan's field limit has room, and leaves out the rest.

// mergeFieldSamples is how many values the preview shows of a merge field.
const mergeFieldSamples = 3

// MailchimpMergeField is one merge field of a bundle, beyond first and last
// name. Merge fields of several audiences whose names match loosely (case,
// spaces, underscores, dashes and dots ignored) are one, named as the first
// audience names it.
type MailchimpMergeField struct {
	Name string `json:"name"`
	// Audiences are the audiences that have it, in the bundle's order.
	Audiences []string `json:"audiences"`
	// Members counts member rows with a value for it.
	Members int `json:"members"`
	// Samples are up to 3 different values, in the order first seen.
	Samples []string `json:"samples"`
}

// mcMerge is a bundle's merge fields, and where each audience's are among
// them.
type mcMerge struct {
	fields []MailchimpMergeField
	// at maps an audience's merge field, by its place in the audience's
	// MergeFields, to its place in fields; -1 for one whose name has
	// nothing to match by.
	at map[*mailchimp.Audience][]int
}

// mailchimpMergeFields lists b's merge fields in Mailchimp's order: each
// audience's in its own order, audiences in the bundle's order, a name
// shared with an earlier audience taking the earlier one's place.
func mailchimpMergeFields(b *mailchimp.Bundle) *mcMerge {
	mg := &mcMerge{fields: []MailchimpMergeField{}, at: map[*mailchimp.Audience][]int{}}
	byKey := map[string]int{}
	// lastRow is, for each merge field, the last member row counted, so
	// a row with two columns for one merge field counts once.
	var lastRow []*mailchimp.Member
	for _, a := range b.Audiences {
		idx := make([]int, len(a.MergeFields))
		for k, name := range a.MergeFields {
			hk := headerKey(name)
			if hk == "" {
				idx[k] = -1
				continue
			}
			i, ok := byKey[hk]
			if !ok {
				i = len(mg.fields)
				byKey[hk] = i
				mg.fields = append(mg.fields, MailchimpMergeField{Name: strings.TrimSpace(name), Audiences: []string{}, Samples: []string{}})
				lastRow = append(lastRow, nil)
			}
			if !slices.Contains(mg.fields[i].Audiences, a.Name) {
				mg.fields[i].Audiences = append(mg.fields[i].Audiences, a.Name)
			}
			idx[k] = i
		}
		mg.at[a] = idx
		for _, m := range a.Members {
			for k, v := range m.Merge {
				if k >= len(idx) || idx[k] < 0 || v == "" {
					continue
				}
				f := &mg.fields[idx[k]]
				if lastRow[idx[k]] != m {
					lastRow[idx[k]] = m
					f.Members++
				}
				if v = importFieldValue(v); len(f.Samples) < mergeFieldSamples && v != "" && !slices.Contains(f.Samples, v) {
					f.Samples = append(f.Samples, v)
				}
			}
		}
	}
	return mg
}

// MailchimpFieldInput says which merge fields fill which contact fields.
type MailchimpFieldInput struct {
	// FieldColumns names the merge field each of the account's contact
	// fields comes from, keyed by the field's key or id; "" leaves the
	// field out. A field left out of the map takes a merge field whose
	// name matches its name or key loosely, if one isn't taken.
	FieldColumns map[string]string
	// NewFields are the contact fields to make, each from the merge field
	// its Column names. Nil makes the default ones: a field for each merge
	// field not matched, in Mailchimp's order, while the plan's limit has
	// room. An empty list makes none.
	NewFields *[]NewImportField
}

// MergeSkipFieldLimit is why the default leaves a merge field out when the
// plan's field limit has no room for another field.
const MergeSkipFieldLimit = "field_limit"

// MergeFieldChoice is what one merge field becomes.
type MergeFieldChoice struct {
	MailchimpMergeField
	// Field is the account's contact field it fills; nil when none.
	Field *Field
	// NewField is the contact field the import would make from it; nil
	// when none.
	NewField *NewFieldPreview
	// SkipReason is why the default leaves it out: MergeSkipFieldLimit,
	// or the code a new field of its name would be refused with
	// (invalid_field_name, reserved_field_name, field_exists). Empty when
	// it's matched or made, or left out as asked.
	SkipReason string
}

// MailchimpFieldPlan is what a Mailchimp import's merge fields would
// become.
type MailchimpFieldPlan struct {
	// Choices are the merge fields in Mailchimp's order.
	Choices []MergeFieldChoice
	// Limit is the plan's field limit, Existing how many fields the
	// account has, New how many the import would make, and LeftOut how
	// many merge fields the default leaves out for the limit.
	Limit, Existing, New, LeftOut int
	// Problem is why the import would be refused with these choices; nil
	// when it would be taken.
	Problem *auth.ValidationError

	layout *csvLayout
}

// kept are the names of the merge fields that fill a field.
func (p *MailchimpFieldPlan) kept() map[string]bool {
	out := map[string]bool{}
	for _, c := range p.Choices {
		if c.Field != nil || c.NewField != nil {
			out[c.Name] = true
		}
	}
	return out
}

// PlanMailchimpFields says what merge fields would become in the account
// as it is now, without writing anything.
func (s *Service) PlanMailchimpFields(ctx context.Context, accountID string, merge []MailchimpMergeField, in MailchimpFieldInput) (*MailchimpFieldPlan, error) {
	af, err := s.accountFields(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return planMailchimpFields(merge, af, in)
}

// planMailchimpFields works out what merge fields become for an account
// with the contact fields af, with MG-131's column choices.
func planMailchimpFields(merge []MailchimpMergeField, af AccountFields, in MailchimpFieldInput) (*MailchimpFieldPlan, error) {
	names := make([]string, len(merge))
	for i, m := range merge {
		names[i] = m.Name
	}
	l := &csvLayout{hasHeader: true, header: names, col: map[string]int{}, fieldCol: map[string]int{}, merge: true}
	var asked []NewImportField
	if in.NewFields != nil {
		asked = *in.NewFields
	}
	c := fieldChoice(af, in.FieldColumns, asked)
	var first error
	fail := func(err error) {
		if first == nil {
			first = err
		}
	}
	used := map[int]bool{}
	l.chooseFields(c, used, fail)
	l.guessFields(c, used)

	p := &MailchimpFieldPlan{Limit: af.Limit, Existing: len(af.Fields), Choices: make([]MergeFieldChoice, len(merge)), layout: l}
	for i, m := range merge {
		p.Choices[i].MailchimpMergeField = m
	}
	if in.NewFields == nil {
		taken := newFieldNames(af.Fields, l.colWord())
		room := af.Limit - len(af.Fields)
		for i, m := range merge {
			if used[i] {
				continue
			}
			// A name that can't be made says why first; the limit only
			// leaves out what could otherwise be made.
			name, key, err := taken.check(m.Name)
			var ve *auth.ValidationError
			if errors.As(err, &ve) {
				p.Choices[i].SkipReason = ve.Code
				continue
			}
			if err != nil {
				return nil, err
			}
			if room <= 0 {
				p.Choices[i].SkipReason = MergeSkipFieldLimit
				p.LeftOut++
				continue
			}
			taken.keep(name, key)
			used[i] = true
			l.newCols = append(l.newCols, newFieldCol{name: name, key: key, col: i})
			room--
		}
	}
	for _, f := range af.Fields {
		if i, ok := l.fieldCol[f.ID]; ok {
			p.Choices[i].Field = f
		}
	}
	for _, nc := range l.newCols {
		p.Choices[nc.col].NewField = &NewFieldPreview{Name: nc.name, Key: nc.key}
	}
	p.New = len(l.newCols)
	if first != nil {
		var ve *auth.ValidationError
		if !errors.As(first, &ve) {
			return nil, first
		}
		p.Problem = ve
	}
	return p, nil
}
