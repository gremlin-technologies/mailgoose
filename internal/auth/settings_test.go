package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

func set(s string) Field { return Field{Set: true, Value: &s} }

var clear = Field{Set: true}

func TestAccountSettings(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	s := &Service{DB: db}
	var id string
	if err := db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('Pat') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}

	st, err := s.AccountSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.TimeZone != "UTC" || st.PostalAddress != nil || st.Plan != "free" {
		t.Errorf("new account = %+v", st)
	}
	if got := st.MissingForSend(); len(got) != 1 || got[0] != "postal_address" {
		t.Errorf("MissingForSend = %v", got)
	}

	st, err = s.UpdateAccountSettings(ctx, id, SettingsUpdate{
		Name:          set("  Pat's Bakery "),
		FromName:      set("Pat"),
		FromEmail:     set("news@patsbakery.example"),
		PostalAddress: set(" Pat's Bakery\r\n\n 12 Main St \nSpringfield, IL 62701\n"),
		TimeZone:      set("America/Chicago"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Name != "Pat's Bakery" || *st.FromName != "Pat" || *st.FromEmail != "news@patsbakery.example" ||
		*st.PostalAddress != "Pat's Bakery\n12 Main St\nSpringfield, IL 62701" || st.TimeZone != "America/Chicago" {
		t.Errorf("after update = %+v", st)
	}
	if got := st.MissingForSend(); len(got) != 0 {
		t.Errorf("MissingForSend = %v", got)
	}

	// Fields not sent stay as they were; null clears.
	st, err = s.UpdateAccountSettings(ctx, id, SettingsUpdate{FromName: clear})
	if err != nil {
		t.Fatal(err)
	}
	if st.FromName != nil || st.FromEmail == nil || st.PostalAddress == nil || st.TimeZone != "America/Chicago" {
		t.Errorf("after clearing from_name = %+v", st)
	}
	again, err := s.AccountSettings(ctx, id)
	if err != nil || again.FromName != nil || *again.FromEmail != "news@patsbakery.example" {
		t.Errorf("read back = %+v, %v", again, err)
	}

	if _, err := s.AccountSettings(ctx, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrNoAccount) {
		t.Errorf("unknown account: %v", err)
	}
}

func TestAccountSettingsValidation(t *testing.T) {
	cases := []struct {
		name string
		u    SettingsUpdate
		code string
	}{
		{"clear name", SettingsUpdate{Name: clear}, "invalid_account_name"},
		{"blank name", SettingsUpdate{Name: set("  ")}, "invalid_account_name"},
		{"two-line from name", SettingsUpdate{FromName: set("Pat\nBakery")}, "invalid_from_name"},
		{"from name with brackets", SettingsUpdate{FromName: set("Pat <pat@x.example>")}, "invalid_from_name"},
		{"long from name", SettingsUpdate{FromName: set(strings.Repeat("a", 101))}, "invalid_from_name"},
		{"bad from email", SettingsUpdate{FromEmail: set("Pat <news@x.example>")}, "invalid_from_email"},
		{"blank postal address", SettingsUpdate{PostalAddress: set(" \n \n")}, "invalid_postal_address"},
		{"long postal address", SettingsUpdate{PostalAddress: set(strings.Repeat("a", 501))}, "invalid_postal_address"},
		{"too many lines", SettingsUpdate{PostalAddress: set(strings.Repeat("a\n", 9))}, "invalid_postal_address"},
		{"clear time zone", SettingsUpdate{TimeZone: clear}, "invalid_time_zone"},
		{"Local", SettingsUpdate{TimeZone: set("Local")}, "invalid_time_zone"},
		{"offset", SettingsUpdate{TimeZone: set("+05:00")}, "invalid_time_zone"},
		{"unknown zone", SettingsUpdate{TimeZone: set("Mars/Olympus")}, "invalid_time_zone"},
		{"padded zone", SettingsUpdate{TimeZone: set(" UTC")}, "invalid_time_zone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ve *ValidationError
			if err := normalizeUpdate(&tc.u); !errors.As(err, &ve) || ve.Code != tc.code {
				t.Errorf("err = %v, want %s", err, tc.code)
			}
		})
	}
	for _, ok := range []string{"UTC", "America/New_York", "Europe/London", "Asia/Kolkata", "Etc/GMT+5"} {
		u := SettingsUpdate{TimeZone: set(ok)}
		if err := normalizeUpdate(&u); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// Clearing the optional fields is fine.
	u := SettingsUpdate{FromName: clear, FromEmail: clear, PostalAddress: clear}
	if err := normalizeUpdate(&u); err != nil {
		t.Errorf("clearing optional fields: %v", err)
	}
}
