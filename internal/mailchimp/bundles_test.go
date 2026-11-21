package mailchimp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
)

func storeWithAccount(t *testing.T) (*Store, *pgxpool.Pool, string) {
	t.Helper()
	db := dbtest.New(t)
	var acct string
	if err := db.QueryRow(context.Background(), `INSERT INTO accounts (name) VALUES ('A') RETURNING id`).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	return &Store{DB: db}, db, acct
}

func savedExport(t *testing.T, s *Store, acct string) *Upload {
	t.Helper()
	data := zipDir(t, "testdata/account_export", "")
	u, err := s.Save(t.Context(), acct, "export.zip", data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestStoreKeepsAnUploadForSevenDays(t *testing.T) {
	s, _, acct := storeWithAccount(t)
	ctx := t.Context()
	u := savedExport(t, s, acct)
	if u.FileName != "export.zip" || !u.HasData || u.ConfirmedAt != nil || u.EmailsStatus != EmailsNone ||
		u.ExpiresAt.Sub(u.CreatedAt) != UploadTTL {
		t.Fatalf("saved = %+v", u)
	}
	if _, err := s.Get(ctx, acct, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "00000000-0000-4000-8000-000000000000", u.ID); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("another account's get: %v", err)
	}
	if data, err := s.Data(ctx, acct, u.ID); err != nil || len(data) != u.Bytes {
		t.Errorf("data = %d bytes, %v", len(data), err)
	}

	// Past its expiry it's gone, and the hourly prune deletes it.
	s.Now = func() time.Time { return time.Now().Add(UploadTTL + time.Minute) }
	if _, err := s.Get(ctx, acct, u.ID); !errors.Is(err, ErrUploadNotFound) {
		t.Errorf("expired get: %v", err)
	}
	if n, err := s.Prune(ctx); err != nil || n != 1 {
		t.Errorf("prune = %d, %v", n, err)
	}
}

func TestStoreConfirmsOnce(t *testing.T) {
	s, _, acct := storeWithAccount(t)
	ctx := t.Context()
	u := savedExport(t, s, acct)
	c, err := s.Confirm(ctx, acct, u.ID, nil, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if c.ConfirmedAt == nil || c.Campaigns != 1 || c.EmailsTotal != 2 || c.EmailsStatus != EmailsQueued || !c.HasData {
		t.Fatalf("confirmed = %+v", c)
	}
	// A second confirm changes nothing, and a confirmed upload doesn't expire.
	again, err := s.Confirm(ctx, acct, u.ID, nil, 5, 6)
	if err != nil || again.Campaigns != 1 || again.EmailsTotal != 2 {
		t.Fatalf("again = %+v, %v", again, err)
	}
	s.Now = func() time.Time { return time.Now().Add(UploadTTL + time.Minute) }
	if _, err := s.Get(ctx, acct, u.ID); err != nil {
		t.Errorf("confirmed upload after its expiry: %v", err)
	}

	// With no emails to make, the file goes at once.
	u2 := savedExport(t, &Store{DB: s.DB}, acct)
	c2, err := (&Store{DB: s.DB}).Confirm(ctx, acct, u2.ID, nil, 1, 0)
	if err != nil || c2.EmailsStatus != EmailsDone || c2.HasData {
		t.Fatalf("no emails = %+v, %v", c2, err)
	}
	if _, err := (&Store{DB: s.DB}).Data(ctx, acct, u2.ID); !errors.Is(err, ErrUploadUsed) {
		t.Errorf("data after use: %v", err)
	}
}

func emailCount(t *testing.T, db *pgxpool.Pool, acct string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM emails WHERE account_id = $1`, acct).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEmailJobMakesEmailsAndCarriesOn(t *testing.T) {
	s, db, acct := storeWithAccount(t)
	ctx := t.Context()
	job := &EmailJob{Store: s, Importer: &EmailImporter{Emails: &emails.Service{DB: db}}}

	// A job that stopped after its first email carries on from the second.
	u := savedExport(t, s, acct)
	if _, err := s.Confirm(ctx, acct, u.ID, nil, 2, 3); err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(ImportedEmail{EmailPlan: EmailPlan{From: EmailFromTemplate, Name: "made before the stop"},
		EmailID: "00000000-0000-4000-8000-000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE mailchimp_bundles SET emails = jsonb_build_array($2::jsonb), emails_status = 'running',
		lease_until = now() - interval '1 minute', attempts = 1 WHERE id = $1`, u.ID, first); err != nil {
		t.Fatal(err)
	}
	if ran, err := job.RunNext(ctx); !ran || err != nil {
		t.Fatalf("run = %v, %v", ran, err)
	}
	if ran, err := job.RunNext(ctx); ran || err != nil {
		t.Fatalf("second run = %v, %v", ran, err)
	}
	got, err := s.Get(ctx, acct, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EmailsStatus != EmailsDone || len(got.Emails) != 3 || got.HasData || got.Emails[0].Name != "made before the stop" {
		t.Fatalf("done = %+v", got)
	}
	if n := emailCount(t, db, acct); n != 2 {
		t.Errorf("emails made = %d, want 2", n)
	}
	for _, e := range got.Emails[1:] {
		if e.EmailID == "" || e.From != EmailFromCampaign {
			t.Errorf("made = %+v", e)
		}
	}
}

func TestEmailJobGivesUpAfterThreeTries(t *testing.T) {
	s, db, acct := storeWithAccount(t)
	ctx := t.Context()
	u := savedExport(t, s, acct)
	if _, err := s.Confirm(ctx, acct, u.ID, nil, 1, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE mailchimp_bundles SET emails_status = 'running',
		lease_until = now() - interval '1 minute', attempts = 3 WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	job := &EmailJob{Store: s, Importer: &EmailImporter{Emails: &emails.Service{DB: db}}}
	if ran, err := job.RunNext(ctx); ran || err != nil {
		t.Fatalf("run = %v, %v", ran, err)
	}
	got, err := s.Get(ctx, acct, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EmailsStatus != EmailsFailed || got.EmailsError == nil || got.HasData || emailCount(t, db, acct) != 0 {
		t.Fatalf("failed = %+v", got)
	}
}
