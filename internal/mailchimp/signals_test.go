package mailchimp

import (
	"slices"
	"testing"
	"time"
)

// A preview as the API stores it, cut to what matters here.
const keptPreview = `{
	"source": "account_export",
	"audiences": [
		{"name": "Newsletter", "members": 1000,
		 "by_status": {"subscribed": 800, "unsubscribed": 120, "cleaned": 60, "pending": 20, "transactional": 0, "archived": 0}},
		{"name": "Wholesale", "members": 200,
		 "by_status": {"subscribed": 100, "unsubscribed": 40, "cleaned": 40, "pending": 0, "transactional": 0, "archived": 20}}
	],
	"contacts": {"with_optin_time": 720, "newest_optin": "2026-11-01T10:00:00Z"},
	"emails": []
}`

func TestSignalsKeptOnConfirm(t *testing.T) {
	s, db, acct := storeWithAccount(t)
	ctx := t.Context()
	u, err := s.Save(ctx, acct, "export.zip", zipDir(t, "testdata/account_export", ""), []byte(keptPreview), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ctx, acct, u.ID, nil, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	var file *string
	var raw []byte
	if err := db.QueryRow(ctx, `SELECT file_name, signals FROM mailchimp_bundles WHERE id = $1`, u.ID).Scan(&file, &raw); err != nil {
		t.Fatal(err)
	}
	sig, err := ParseSignals(file, raw)
	if err != nil || sig == nil {
		t.Fatalf("signals = %+v, %v (%s)", sig, err, raw)
	}
	newest := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	if sig.Source != "file" || sig.FileName == nil || *sig.FileName != "export.zip" || sig.Members != 1200 ||
		!slices.Equal(sig.Audiences, []SignalAudience{{"Newsletter", 1000}, {"Wholesale", 200}}) ||
		sig.ByStatus != (StatusCounts{Subscribed: 900, Unsubscribed: 160, Cleaned: 100, Pending: 20, Archived: 20}) ||
		sig.CleanedShare != 100.0/1200 || sig.UnsubscribedShare != 160.0/1200 || sig.WithOptinTime != 720 ||
		sig.OptinShare != 0.8 || sig.NewestOptin == nil || !sig.NewestOptin.Equal(newest) {
		t.Errorf("signals = %+v", sig)
	}
	want := []string{
		"From file export.zip: Newsletter (1,000), Wholesale (200).",
		"Members: 900 subscribed, 160 unsubscribed, 100 cleaned, 20 pending, 20 archived.",
		"Cleaned 8.3%, unsubscribed 13.3%; 80.0% of subscribed have a Mailchimp opt-in time, the newest 2026-11-01.",
	}
	if got := sig.Lines(); !slices.Equal(got, want) {
		t.Errorf("lines =\n%q\nwant\n%q", got, want)
	}
}

func TestSignalsFromAConnectedAccount(t *testing.T) {
	file := FetchedFileName
	sig, err := ParseSignals(&file, []byte(`{"source": "connected_account", "audiences": [
		{"name": "News", "members": 0, "by_status": {"subscribed": 0}}], "with_optin_time": 0, "newest_optin": null}`))
	if err != nil {
		t.Fatal(err)
	}
	// Nothing subscribed divides by nothing: the shares are 0.
	if sig.Source != "oauth" || sig.FileName != nil || sig.Members != 0 || sig.OptinShare != 0 || sig.CleanedShare != 0 {
		t.Errorf("signals = %+v", sig)
	}
	want := []string{
		"From a connected Mailchimp account: News (0).",
		"Members: 0 subscribed.",
		"Cleaned 0.0%, unsubscribed 0.0%; 0.0% of subscribed have a Mailchimp opt-in time.",
	}
	if got := sig.Lines(); !slices.Equal(got, want) {
		t.Errorf("lines = %q", got)
	}
	// An upload confirmed before signals were kept has none.
	for _, raw := range [][]byte{nil, []byte("null")} {
		if sig, err := ParseSignals(nil, raw); sig != nil || err != nil {
			t.Errorf("%q: %+v, %v", raw, sig, err)
		}
	}
}
