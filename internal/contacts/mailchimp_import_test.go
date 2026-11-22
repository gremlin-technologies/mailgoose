package contacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/mailchimp"
)

// mcAudience is an audience of members given as "email status [tag ...]".
func mcAudience(name string, members ...string) *mailchimp.Audience {
	a := &mailchimp.Audience{Name: name}
	for i, m := range members {
		f := strings.Fields(m)
		a.Members = append(a.Members, &mailchimp.Member{Email: f[0], Status: mailchimp.Status(f[1]), Tags: f[2:],
			File: name + "/members.csv", Row: i + 2})
	}
	return a
}

func mcBundle(auds ...*mailchimp.Audience) *mailchimp.Bundle {
	return &mailchimp.Bundle{Source: mailchimp.SourceAccountExport, Audiences: auds}
}

func (h *harness) mailchimpImport(t *testing.T, b *mailchimp.Bundle) *Import {
	t.Helper()
	im, created, err := h.svc.CreateMailchimpImport(context.Background(), h.acc,
		MailchimpImportInput{Bundle: b, FileName: "export.zip", PermissionConfirmed: true})
	if err != nil {
		t.Fatalf("create Mailchimp import: %v", err)
	}
	if !created {
		t.Fatal("import not created")
	}
	h.runImports(t)
	im, err = h.svc.GetImport(context.Background(), h.acc, im.ID)
	if err != nil {
		t.Fatal(err)
	}
	return im
}

func (h *harness) statusOf(t *testing.T, email string) string {
	t.Helper()
	c := h.byEmail(t, email)
	if c == nil {
		return ""
	}
	return c.Status
}

func TestMailchimpImportMapsStatusesAndPreviewWritesNothing(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.add(t, "old@example.com", StatusSubscribed)
	h.add(t, "kept@example.com", StatusUnsubscribed)
	gone := h.add(t, "gone@example.com", StatusUnsubscribed)
	if err := h.svc.Delete(ctx, h.acc, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateTag(ctx, h.acc, "VIP"); err != nil {
		t.Fatal(err)
	}
	b := mcBundle(mcAudience("Newsletter",
		"Sub@Example.com subscribed vip Fall,Promo",
		"pend@example.com pending",
		"unsub@example.com unsubscribed",
		"clean@example.com cleaned",
		"tx@example.com transactional",
		"arch@example.com archived",
		"not-an-address subscribed",
		"old@example.com unsubscribed newtag",
		"kept@example.com subscribed",
		"gone@example.com subscribed",
	))
	b.Audiences[0].Members[0].FirstName = "Sam"
	b.Audiences[0].Members[0].OptinTime = "2024-03-01 10:00:00"

	p, err := h.svc.PreviewMailchimpImport(ctx, h.acc, b)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{StatusSubscribed: 1, StatusUnconfirmed: 1, StatusUnsubscribed: 1, StatusBounced: 1}; fmt.Sprint(p.New) != fmt.Sprint(want) {
		t.Errorf("new = %v, want %v", p.New, want)
	}
	if p.ExistingTagged != 2 || p.ExistingSuppressed != 1 {
		t.Errorf("existing tagged %d, suppressed %d; want 2, 1", p.ExistingTagged, p.ExistingSuppressed)
	}
	wantSkipped := map[string]int{SkipInvalidEmail: 1, SkipInvalidRow: 0, SkipSuppressed: 1, SkipNotSubscribed: 1, SkipArchived: 1}
	if fmt.Sprint(p.Skipped) != fmt.Sprint(wantSkipped) {
		t.Errorf("skipped = %v, want %v", p.Skipped, wantSkipped)
	}
	// "Fall,Promo" is one Mailchimp tag; mailgoose names can't hold commas.
	if want := []string{"Fall Promo", "newtag"}; fmt.Sprint(p.TagsToCreate) != fmt.Sprint(want) {
		t.Errorf("tags to create = %v, want %v", p.TagsToCreate, want)
	}
	if p.Summary.Subscribed != 3 || p.Summary.WithOptinTime != 1 || len(p.Summary.RenamedTags) != 1 {
		t.Errorf("summary = %+v", p.Summary)
	}
	if p.Plan.Plan != "free" || p.Plan.Upgrade || p.Plan.Contacts != 2 || p.Waits || !slices.Equal(p.Holds, []string{HoldInvalidAddresses}) {
		t.Errorf("plan %+v, waits %v, holds %v", p.Plan, p.Waits, p.Holds)
	}
	var imports, tags int
	if err := h.svc.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM imports), (SELECT count(*) FROM tags)`).Scan(&imports, &tags); err != nil {
		t.Fatal(err)
	}
	if imports != 0 || tags != 1 || h.statusOf(t, "old@example.com") != StatusSubscribed {
		t.Fatalf("preview wrote: %d imports, %d tags, old is %s", imports, tags, h.statusOf(t, "old@example.com"))
	}

	if _, _, err := h.svc.CreateMailchimpImport(ctx, h.acc, MailchimpImportInput{Bundle: b}); code(err) != "permission_required" {
		t.Errorf("without permission: %v", err)
	}
	im := h.mailchimpImport(t, b)
	if im.Status != ImportDone || im.Kind != ImportKindMailchimp || im.Imported != 4 || im.ExistingTagged != 2 || im.ExistingSuppressed != 1 {
		t.Errorf("import = %+v", im)
	}
	if im.Mailchimp == nil || im.Mailchimp.Audiences[0].Name != "Newsletter" || im.Mailchimp.ByMailchimpStatus["cleaned"] != 1 {
		t.Errorf("summary = %+v", im.Mailchimp)
	}
	for email, want := range map[string]string{
		"sub@example.com": StatusSubscribed, "pend@example.com": StatusUnconfirmed, "unsub@example.com": StatusUnsubscribed,
		"clean@example.com": StatusBounced, "tx@example.com": "", "arch@example.com": "", "gone@example.com": "",
		// An existing contact is taken off the list, never put back on it.
		"old@example.com": StatusUnsubscribed, "kept@example.com": StatusUnsubscribed,
	} {
		if got := h.statusOf(t, email); got != want {
			t.Errorf("%s is %q, want %q", email, got, want)
		}
	}
	sub := h.byEmail(t, "sub@example.com")
	if sub.FirstName == nil || *sub.FirstName != "Sam" || fmt.Sprint(tagNames(sub)) != "[Fall Promo VIP]" || sub.Source != "import" {
		t.Errorf("sub = %+v, tags %v", sub, tagNames(sub))
	}
	if got := tagNames(h.byEmail(t, "old@example.com")); fmt.Sprint(got) != "[newtag]" {
		t.Errorf("old's tags = %v", got)
	}
	var wording []byte
	if err := h.svc.DB.QueryRow(ctx, `SELECT wording FROM consents WHERE contact_id = $1`, sub.ID).Scan(&wording); err != nil {
		t.Fatal(err)
	}
	var w struct {
		ListSource string `json:"list_source"`
		Detail     string `json:"list_source_detail"`
		FileName   string `json:"file_name"`
		Mailchimp  struct {
			Audiences []struct {
				Name, Status string
				OptinTime    string `json:"optin_time"`
			}
		}
	}
	if err := json.Unmarshal(wording, &w); err != nil {
		t.Fatal(err)
	}
	if w.ListSource != "export" || w.Detail != "Mailchimp" || w.FileName != "export.zip" || len(w.Mailchimp.Audiences) != 1 ||
		w.Mailchimp.Audiences[0].OptinTime != "2024-03-01 10:00:00" {
		t.Errorf("consent = %s", wording)
	}
	skips := h.skipped(t, im.ID)
	var got []string
	for _, s := range skips {
		got = append(got, s.Reason+" "+s.Email+" "+s.Source)
	}
	want := []string{
		"invalid_email not-an-address Newsletter/members.csv:8",
		"not_subscribed tx@example.com Newsletter/members.csv:6",
		"archived arch@example.com Newsletter/members.csv:7",
		"suppressed gone@example.com Newsletter/members.csv:11",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("skips\n got %q\nwant %q", got, want)
	}
	if im.SkippedByReason[SkipArchived] != 1 || im.Skipped != 4 {
		t.Errorf("skipped %d by reason %v", im.Skipped, im.SkippedByReason)
	}
}

func TestMailchimpImportMergesAudiences(t *testing.T) {
	h := newHarness(t)
	a := mcAudience("Shop", "pat@example.com subscribed buyer", "lee@example.com pending")
	a.Members[0].Groups = []mailchimp.Group{{Category: "Interests", Name: "Shoes"}}
	b := mcBundle(a, mcAudience("Blog", "pat@example.com unsubscribed", "lee@example.com subscribed reader"))
	p, err := h.svc.PreviewMailchimpImport(context.Background(), h.acc, b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Summary.MergeSuppressed != 1 || fmt.Sprint(p.MergeSuppressedExamples) != "[pat@example.com]" {
		t.Errorf("merge suppressed %d %v", p.Summary.MergeSuppressed, p.MergeSuppressedExamples)
	}
	h.mailchimpImport(t, b)
	pat, lee := h.byEmail(t, "pat@example.com"), h.byEmail(t, "lee@example.com")
	if pat.Status != StatusUnsubscribed || lee.Status != StatusSubscribed {
		t.Errorf("pat %s, lee %s", pat.Status, lee.Status)
	}
	if got := tagNames(pat); fmt.Sprint(got) != "[audience: Blog audience: Shop buyer Interests: Shoes]" {
		t.Errorf("pat's tags = %q", got)
	}
	if got := tagNames(lee); fmt.Sprint(got) != "[audience: Blog audience: Shop reader]" {
		t.Errorf("lee's tags = %q", got)
	}
}

func TestMailchimpImportWaitsForAPlanThatCoversIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.DB.Exec(ctx, `
		INSERT INTO contacts (account_id, email, status, source)
		SELECT $1, 'c' || n || '@example.com', 'subscribed', 'api' FROM generate_series(1, $2) AS n`, h.acc, FreeContactLimit-3); err != nil {
		t.Fatal(err)
	}
	b := mcBundle(mcAudience("List", "a@example.com subscribed", "b@example.com subscribed", "c@example.com pending",
		"d@example.com subscribed", "out@example.com unsubscribed", "c1@example.com cleaned"))
	p, err := h.svc.PreviewMailchimpImport(ctx, h.acc, b)
	if err != nil {
		t.Fatal(err)
	}
	// 997 - 1 cleaned + 4 new = 1,000 is the free plan's limit, not over it.
	if p.Waits || p.Plan.Contacts != FreeContactLimit {
		t.Fatalf("waits %v, plan %+v", p.Waits, p.Plan)
	}
	b.Audiences[0].Members = append(b.Audiences[0].Members, &mailchimp.Member{Email: "e@example.com", Status: mailchimp.Subscribed, File: "List/members.csv", Row: 8})
	p, err = h.svc.PreviewMailchimpImport(ctx, h.acc, b)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Waits || !slices.Equal(p.Holds, []string{HoldOverContactLimit}) || !p.Plan.Upgrade || p.Plan.Contacts != FreeContactLimit+1 ||
		p.Plan.Units != 1 || p.Plan.MonthCents != 500 || p.Plan.YearCents != 5000 {
		t.Fatalf("waits %v, holds %v, plan %+v", p.Waits, p.Holds, p.Plan)
	}

	im := h.mailchimpImport(t, b)
	if im.Status != ImportWaiting || im.Review != ReviewHeld || im.HoldReason == nil || *im.HoldReason != HoldOverContactLimit {
		t.Fatalf("import = %+v", im)
	}
	// The suppressions are in; the subscribed part isn't.
	if h.statusOf(t, "out@example.com") != StatusUnsubscribed || h.statusOf(t, "c1@example.com") != StatusBounced ||
		h.statusOf(t, "a@example.com") != "" {
		t.Errorf("out %q, c1 %q, a %q", h.statusOf(t, "out@example.com"), h.statusOf(t, "c1@example.com"), h.statusOf(t, "a@example.com"))
	}
	if ran, err := h.svc.RunNextImport(ctx); ran || err != nil {
		t.Fatalf("a waiting import ran on the free plan: %v %v", ran, err)
	}

	if _, err := h.svc.DB.Exec(ctx, `UPDATE accounts SET plan = 'paid' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	h.runImports(t)
	im, err = h.svc.GetImport(ctx, h.acc, im.ID)
	if err != nil {
		t.Fatal(err)
	}
	if im.Status != ImportDone || im.Imported != 6 || im.Review != ReviewHeld {
		t.Errorf("after the upgrade: %+v", im)
	}
	var held int
	if err := h.svc.DB.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE held_import_id = $1`, im.ID).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 5 || h.statusOf(t, "e@example.com") != StatusSubscribed {
		t.Errorf("%d held, e is %q", held, h.statusOf(t, "e@example.com"))
	}
}

func TestMailchimpImportIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	b := mcBundle(mcAudience("List", "a@example.com subscribed"))
	in := MailchimpImportInput{Bundle: b, PermissionConfirmed: true}
	first, created, err := h.svc.CreateMailchimpImport(ctx, h.acc, in)
	if err != nil || !created {
		t.Fatal(created, err)
	}
	again, created, err := h.svc.CreateMailchimpImport(ctx, h.acc, in)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("again: %v %v %v", again, created, err)
	}
	in.IdempotencyKey = "k1"
	if _, created, err = h.svc.CreateMailchimpImport(ctx, h.acc, in); err != nil || !created {
		t.Fatal(created, err)
	}
	in.Bundle = mcBundle(mcAudience("List", "b@example.com subscribed"))
	if _, _, err = h.svc.CreateMailchimpImport(ctx, h.acc, in); code(err) != "idempotency_key_reused" {
		t.Errorf("reused key: %v", err)
	}
	// A failed import frees its key.
	if _, err := h.svc.DB.Exec(ctx, `UPDATE imports SET status = 'failed' WHERE idempotency_key = 'k1'`); err != nil {
		t.Fatal(err)
	}
	if _, created, err = h.svc.CreateMailchimpImport(ctx, h.acc, in); err != nil || !created {
		t.Errorf("after failing: %v %v", created, err)
	}
}

func TestMailchimpImportHoldsALargeFirstImport(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.DB.Exec(ctx, `UPDATE accounts SET plan = 'paid' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	var members []string
	for i := range LargeFirstImport + 1 {
		members = append(members, fmt.Sprintf("p%d@example.com subscribed", i))
	}
	im := h.mailchimpImport(t, mcBundle(mcAudience("List", members...)))
	if im.Status != ImportDone || im.Review != ReviewHeld || im.HoldReason == nil || *im.HoldReason != HoldLargeFirstImport ||
		im.Imported != LargeFirstImport+1 {
		t.Errorf("import = %+v", im)
	}
}

func TestMailchimpImportResumesAfterAStop(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.DB.Exec(ctx, `UPDATE accounts SET plan = 'paid' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	var members []string
	for i := range 2500 {
		members = append(members, fmt.Sprintf("r%d@example.com subscribed t%d", i, i%3))
	}
	members = append(members, "gone@example.com unsubscribed")
	im, _, err := h.svc.CreateMailchimpImport(ctx, h.acc, MailchimpImportInput{Bundle: mcBundle(mcAudience("List", members...)),
		PermissionConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	stops := 0
	afterImportBatch = func(processed int) error {
		if processed > 1 && stops == 0 {
			stops++
			return fmt.Errorf("stopped at %d", processed)
		}
		return nil
	}
	t.Cleanup(func() { afterImportBatch = nil })
	if _, err := h.svc.RunNextImport(ctx); err == nil {
		t.Fatal("no stop")
	}
	// The lease runs out, and the next run carries on.
	h.now = h.now.Add(ImportLease + time.Minute)
	h.runImports(t)
	im, err = h.svc.GetImport(ctx, h.acc, im.ID)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := h.svc.DB.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE account_id = $1`, h.acc).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if im.Status != ImportDone || im.Imported != 2501 || n != 2501 {
		t.Errorf("import = %+v, %d contacts", im, n)
	}
}

// TestMailchimpImportOf100kIsQuick checks the ticket's bound: 100,000
// members in under five minutes.
func TestMailchimpImportOf100kIsQuick(t *testing.T) {
	if os.Getenv("MAILGOOSE_SLOW_TESTS") == "" {
		t.Skip("set MAILGOOSE_SLOW_TESTS=1 to run; about a minute without -race")
	}
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.DB.Exec(ctx, `UPDATE accounts SET plan = 'paid' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	a := &mailchimp.Audience{Name: "Big"}
	for i := range 100_000 {
		st := mailchimp.Subscribed
		if i%10 == 0 {
			st = mailchimp.Unsubscribed
		}
		a.Members = append(a.Members, &mailchimp.Member{Email: fmt.Sprintf("m%d@example.com", i), Status: st,
			Tags: []string{fmt.Sprintf("t%d", i%50)}, File: "Big/members.csv", Row: i + 2})
	}
	start := time.Now()
	im := h.mailchimpImport(t, mcBundle(a))
	took := time.Since(start)
	t.Logf("100,000 members in %s", took)
	if im.Status != ImportDone || im.Imported != 100_000 {
		t.Errorf("import = %+v", im)
	}
	if took > 5*time.Minute {
		t.Errorf("took %s", took)
	}
}

func TestMailchimpImportCanBeCancelledWhileItWaits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.DB.Exec(ctx, `
		INSERT INTO contacts (account_id, email, status, source)
		SELECT $1, 'c' || n || '@example.com', 'subscribed', 'api' FROM generate_series(1, $2) AS n`, h.acc, FreeContactLimit-1); err != nil {
		t.Fatal(err)
	}
	im := h.mailchimpImport(t, mcBundle(mcAudience("List", "a@example.com subscribed", "b@example.com subscribed",
		"out@example.com unsubscribed")))
	if im.Status != ImportWaiting {
		t.Fatalf("import = %+v", im)
	}
	note, err := h.svc.WaitNote(ctx, h.acc)
	if err != nil || !strings.Contains(note, "paid plan") || !strings.Contains(note, "cancel it") {
		t.Errorf("free note %q, %v", note, err)
	}
	if _, err := h.svc.DB.Exec(ctx, `UPDATE accounts SET tester_started_at = now(), tester_until = now() + interval '30 days',
		tester_credits = 500 WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if note, err = h.svc.WaitNote(ctx, h.acc); err != nil || !strings.Contains(note, "tester account") || !strings.Contains(note, "won't carry on") {
		t.Errorf("tester note %q, %v", note, err)
	}

	for range 2 { // a second cancel changes nothing
		c, err := h.svc.CancelImport(ctx, h.acc, im.ID)
		if err != nil || c.Status != ImportCancelled || c.FinishedAt == nil {
			t.Fatalf("cancel = %+v, %v", c, err)
		}
	}
	// It stays cancelled when the account could take it, and what it
	// already did stays.
	if _, err := h.svc.DB.Exec(ctx, `UPDATE accounts SET plan = 'paid', tester_started_at = NULL, tester_until = NULL,
		tester_credits = NULL WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	if ran, err := h.svc.RunNextImport(ctx); ran || err != nil {
		t.Fatalf("a cancelled import ran: %v %v", ran, err)
	}
	if h.statusOf(t, "out@example.com") != StatusUnsubscribed || h.statusOf(t, "a@example.com") != "" {
		t.Errorf("out %q, a %q", h.statusOf(t, "out@example.com"), h.statusOf(t, "a@example.com"))
	}

	done := h.mailchimpImport(t, mcBundle(mcAudience("Other", "z@example.com subscribed")))
	if _, err := h.svc.CancelImport(ctx, h.acc, done.ID); !errors.Is(err, ErrImportNotWaiting) {
		t.Errorf("cancel a done import: %v", err)
	}
	if _, err := h.svc.CancelImport(ctx, h.acc, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrImportNotFound) {
		t.Errorf("cancel an unknown import: %v", err)
	}
}

func TestMailchimpImportRefusesRowsTooLargeToStore(t *testing.T) {
	h := newHarness(t)
	old := maxMailchimpImportBytes
	maxMailchimpImportBytes = 10
	t.Cleanup(func() { maxMailchimpImportBytes = old })
	_, _, err := h.svc.CreateMailchimpImport(context.Background(), h.acc,
		MailchimpImportInput{Bundle: mcBundle(mcAudience("List", "a@example.com subscribed")), PermissionConfirmed: true})
	var ve *auth.ValidationError
	if !errors.As(err, &ve) || ve.Code != "export_too_large" {
		t.Errorf("err = %v", err)
	}
}
