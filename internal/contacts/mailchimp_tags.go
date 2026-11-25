package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// Mailchimp merge tags as contact field tags (MG-134). An imported email's
// merge tag becomes the tag of the contact field its merge field goes
// into; mailchimp.FieldTags.Match decides which. These say what the
// account's fields and an import's merge fields are, for the preview and
// for the job that makes the emails.

// maxFieldMailchimpTags is the most Mailchimp tags a field keeps, the
// latest; one import adds a few at most.
const maxFieldMailchimpTags = 20

// tagFields reads the account's fields, in the order they were made, with
// the Mailchimp tags earlier imports recorded on them, and the same by id.
func tagFields(ctx context.Context, q querier, accountID string) ([]*mailchimp.TagField, map[string]*mailchimp.TagField, error) {
	rows, err := q.Query(ctx, `SELECT id, key, name, mailchimp_tags FROM contact_fields WHERE account_id = $1
		ORDER BY created_at, id`, accountID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []*mailchimp.TagField
	byID := map[string]*mailchimp.TagField{}
	for rows.Next() {
		var id string
		f := &mailchimp.TagField{}
		if err := rows.Scan(&id, &f.Key, &f.Name, &f.Tags); err != nil {
			return nil, nil, err
		}
		out = append(out, f)
		byID[id] = f
	}
	return out, byID, rows.Err()
}

// MailchimpPreviewFieldTags is what a Mailchimp import's merge tags would
// become with the merge field choices fp (nil for an export with no
// contacts): each merge field's field, existing or new, and the account's
// fields, the new ones after them.
func (s *Service) MailchimpPreviewFieldTags(ctx context.Context, accountID string, fp *MailchimpFieldPlan) (*mailchimp.FieldTags, error) {
	fields, byID, err := tagFields(ctx, s.DB, accountID)
	if err != nil {
		return nil, err
	}
	ft := &mailchimp.FieldTags{Fields: fields}
	if fp == nil {
		return ft, nil
	}
	for _, c := range fp.Choices {
		mf := mailchimp.TagMergeField{Name: c.Name, Tags: c.Tags}
		switch {
		case c.Field != nil:
			mf.Field = byID[c.Field.ID]
		case c.NewField != nil:
			mf.Field = &mailchimp.TagField{Key: c.NewField.Key, Name: c.NewField.Name, New: true}
			ft.Fields = append(ft.Fields, mf.Field)
		}
		ft.MergeFields = append(ft.MergeFields, mf)
	}
	return ft, nil
}

// MailchimpImportFieldTags is what the merge tags of a confirmed bundle's
// emails become: the fields its contacts import (importID, nil when it had
// no contacts) put its merge fields into, as they are now, marked new for
// the ones it made, and the account's fields. A field deleted since is
// left out, so its merge field's tags are taken out.
func (s *Service) MailchimpImportFieldTags(ctx context.Context, accountID string, importID *string, b *mailchimp.Bundle) (*mailchimp.FieldTags, error) {
	fields, byID, err := tagFields(ctx, s.DB, accountID)
	if err != nil {
		return nil, err
	}
	ft := &mailchimp.FieldTags{Fields: fields}
	if importID == nil || b == nil {
		return ft, nil
	}
	var raw []byte
	err = s.DB.QueryRow(ctx, `SELECT field_columns FROM imports WHERE id = $1 AND account_id = $2`, *importID, accountID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ft, nil
	}
	if err != nil {
		return nil, err
	}
	var cols []storedFieldColumn
	if err := json.Unmarshal(raw, &cols); err != nil {
		return nil, fmt.Errorf("read import field columns: %w", err)
	}
	byColumn := map[string]*mailchimp.TagField{}
	for _, c := range cols {
		if f := byID[c.FieldID]; f != nil {
			f.New = f.New || c.Created
			byColumn[c.Column] = f
		}
	}
	for _, m := range mailchimpMergeFields(b).fields {
		ft.MergeFields = append(ft.MergeFields, mailchimp.TagMergeField{Name: m.Name, Tags: m.Tags, Field: byColumn[m.Name]})
	}
	return ft, nil
}

// recordMailchimpTags keeps, on each field a Mailchimp import puts a merge
// field into (matched, or made: made[i] from l.newCols[i]), that merge
// field's Mailchimp tags, so a later import's emails find the field by
// them. A tag names one field, so it comes off any other.
func recordMailchimpTags(ctx context.Context, tx pgx.Tx, accountID string, l *csvLayout, made []*Field, merge []MailchimpMergeField) error {
	fieldAt := map[int]string{}
	for id, col := range l.fieldCol {
		fieldAt[col] = id
	}
	for i, f := range made {
		fieldAt[l.newCols[i].col] = f.ID
	}
	for col, m := range merge {
		id, ok := fieldAt[col]
		if !ok || len(m.Tags) == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE contact_fields SET mailchimp_tags = ARRAY(SELECT t FROM unnest(mailchimp_tags) AS t WHERE t <> ALL($3))
			WHERE account_id = $1 AND id <> $2 AND mailchimp_tags && $3`, accountID, id, m.Tags); err != nil {
			return err
		}
		var have []string
		if err := tx.QueryRow(ctx, `SELECT mailchimp_tags FROM contact_fields WHERE id = $1 AND account_id = $2`, id, accountID).
			Scan(&have); err != nil {
			return err
		}
		have = slices.DeleteFunc(have, func(t string) bool { return slices.Contains(m.Tags, t) })
		have = append(have, m.Tags...)
		if len(have) > maxFieldMailchimpTags {
			have = have[len(have)-maxFieldMailchimpTags:]
		}
		if _, err := tx.Exec(ctx, `UPDATE contact_fields SET mailchimp_tags = $3 WHERE id = $1 AND account_id = $2`,
			id, accountID, have); err != nil {
			return err
		}
	}
	return nil
}
