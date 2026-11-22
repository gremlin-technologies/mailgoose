package forms

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// MailchimpDraft is the form a Mailchimp audience gets when its import is
// confirmed (MG-76): named after the audience, putting Tags (by name) on
// everyone who signs up.
type MailchimpDraft struct {
	Audience string
	Tags     []string
}

// MailchimpForm is the form an audience has after MailchimpDrafts: New is
// false for one an earlier import had already made.
type MailchimpForm struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Audience string `json:"audience"`
	New      bool   `json:"new"`
}

// MailchimpDrafts gives each audience a form. Mailchimp's embedded form code
// posts to Mailchimp, so these are the customer's replacements: email, first
// name and last name, double opt-in on, and draft, so they take no signups
// until the customer has looked at and saved them. An audience that already
// has a form from an earlier import keeps it as it is. Tags that don't
// exist yet are made, as the import makes them.
func (s *Service) MailchimpDrafts(ctx context.Context, accountID string, drafts []MailchimpDraft) ([]MailchimpForm, error) {
	out := make([]MailchimpForm, 0, len(drafts))
	for _, d := range drafts {
		if len(d.Tags) > MaxTags {
			return nil, invalid("form_tags", "a form can put up to %d tags on people, and the form for %s would have %d", MaxTags, d.Audience, len(d.Tags))
		}
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		for _, d := range drafts {
			f, err := mailchimpDraft(ctx, tx, accountID, d)
			if err != nil {
				return err
			}
			out = append(out, f)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func mailchimpDraft(ctx context.Context, tx pgx.Tx, accountID string, d MailchimpDraft) (MailchimpForm, error) {
	out := MailchimpForm{Audience: d.Audience}
	err := tx.QueryRow(ctx, `SELECT id, name FROM forms WHERE account_id = $1 AND lower(mailchimp_audience) = lower($2)`,
		accountID, d.Audience).Scan(&out.ID, &out.Name)
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	f := newForm(accountID)
	f.Name = d.Audience
	if utf8.RuneCountInString(f.Name) > MaxNameChars {
		f.Name = strings.TrimSpace(string([]rune(f.Name)[:MaxNameChars]))
	}
	f.Fields = []Field{
		{Name: FieldEmail, Label: DefaultLabels[FieldEmail], Required: true},
		{Name: FieldFirstName, Label: DefaultLabels[FieldFirstName]},
		{Name: FieldLastName, Label: DefaultLabels[FieldLastName]},
	}
	f.SuccessMessage = DefaultSuccessConfirm
	if err := apply(f, Changes{}); err != nil {
		return out, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return out, err
	}
	fields, _ := json.Marshal(f.Fields)
	// Another confirm making the same audience's form at once waits on the
	// index here and then makes none; its form is read below.
	err = tx.QueryRow(ctx, `
		INSERT INTO forms (account_id, name, fields, button_text, success_message, double_opt_in,
			confirm_subject, confirm_text, signing_key, draft, mailchimp_audience)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, true, $10)
		ON CONFLICT (account_id, lower(mailchimp_audience)) WHERE mailchimp_audience IS NOT NULL DO NOTHING
		RETURNING id`,
		accountID, f.Name, fields, f.ButtonText, f.SuccessMessage, f.DoubleOptIn, f.ConfirmSubject, f.ConfirmText, key,
		d.Audience).Scan(&out.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id, name FROM forms WHERE account_id = $1 AND lower(mailchimp_audience) = lower($2)`,
			accountID, d.Audience).Scan(&out.ID, &out.Name)
		return out, err
	}
	if err != nil {
		return out, err
	}
	out.Name, out.New = f.Name, true
	if len(d.Tags) == 0 {
		return out, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tags (account_id, name) SELECT $1, n FROM unnest($2::text[]) AS n
		ON CONFLICT (account_id, lower(name)) DO NOTHING`, accountID, d.Tags); err != nil {
		return out, err
	}
	var ids []string
	lower := make([]string, len(d.Tags))
	for i, t := range d.Tags {
		lower[i] = strings.ToLower(t)
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM tags WHERE account_id = $1 AND lower(name) = ANY($2)`, accountID, lower)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, setTags(ctx, tx, accountID, out.ID, ids)
}
