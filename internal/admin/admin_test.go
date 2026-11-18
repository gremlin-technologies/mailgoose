package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
)

type harness struct {
	db    *pgxpool.Pool
	svc   *Service
	now   time.Time
	acc   string // Pat's Bakery, a customer
	staff Actor
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	h := &harness{db: db, now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	clock := func() time.Time { return h.now }
	h.svc = &Service{DB: db, Now: clock,
		Sending: &sending.Service{DB: db, Campaigns: &campaigns.Service{DB: db}, Now: clock}}
	h.acc = h.account(t, "Pat's Bakery", "pat@patsbakery.com")
	staffAcc := h.account(t, "Gremlin Technologies", "ops@mailgoose.example")
	var staffID string
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE account_id = $1`, staffAcc).Scan(&staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Admin(ctx, staffID); !errors.Is(err, ErrNotAdmin) {
		t.Fatalf("before grant: %v", err)
	}
	if err := h.svc.SetAdmin(ctx, Server, "OPS@mailgoose.example", true, "on call"); err != nil {
		t.Fatal(err)
	}
	a, err := h.svc.Admin(ctx, staffID)
	if err != nil {
		t.Fatal(err)
	}
	h.staff = *a
	return h
}

func (h *harness) account(t *testing.T, name, email string) string {
	t.Helper()
	var id string
	err := h.db.QueryRow(context.Background(), `
		INSERT INTO accounts (name, from_name, from_email, postal_address, status, activated_at)
		VALUES ($1, $1, $2, '12 Main St', 'active', $3) RETURNING id`, name, email, h.now.AddDate(0, -2, 0)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(context.Background(), `INSERT INTO users (account_id, email) VALUES ($1, $2)`, id, email); err != nil {
		t.Fatal(err)
	}
	return id
}

type logged struct {
	actor, action, reason string
	userID, target        *string
}

func (h *harness) log(t *testing.T) []logged {
	t.Helper()
	rows, err := h.db.Query(context.Background(),
		`SELECT actor, action, reason, actor_user_id::text, target_id FROM admin_actions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (logged, error) {
		var l logged
		err := row.Scan(&l.actor, &l.action, &l.reason, &l.userID, &l.target)
		return l, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) last(t *testing.T) logged {
	t.Helper()
	l := h.log(t)
	return l[len(l)-1]
}

func TestAdminRole(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	l := h.log(t)
	if len(l) != 1 || l[0].actor != "mailgoosed" || l[0].action != ActionGrantAdmin || l[0].reason != "on call" || l[0].userID != nil {
		t.Errorf("log = %+v", l)
	}
	var patID string
	_ = h.db.QueryRow(ctx, `SELECT id FROM users WHERE account_id = $1`, h.acc).Scan(&patID)
	if _, err := h.svc.Admin(ctx, patID); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("customer: %v", err)
	}
	if _, err := h.svc.Admin(ctx, ""); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("no user: %v", err)
	}
	if err := h.svc.SetAdmin(ctx, Server, "nobody@example.com", true, "x"); !errors.Is(err, ErrNoUser) {
		t.Errorf("unknown address: %v", err)
	}
	if err := h.svc.SetAdmin(ctx, Server, "ops@mailgoose.example", false, "left the rota"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Admin(ctx, h.staff.UserID); !errors.Is(err, ErrNotAdmin) {
		t.Errorf("after revoke: %v", err)
	}
	if h.last(t).action != ActionRevokeAdmin {
		t.Errorf("revoke not logged")
	}
}

func TestReasonRequired(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var ie *InputError
	if _, err := h.svc.Pause(ctx, h.staff, h.acc, "   "); !errors.As(err, &ie) || ie.Field != "reason" {
		t.Errorf("blank reason: %v", err)
	}
	long := make([]byte, MaxReason+1)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := h.svc.Pause(ctx, h.staff, h.acc, string(long)); !errors.As(err, &ie) {
		t.Errorf("long reason: %v", err)
	}
	if len(h.log(t)) != 1 {
		t.Error("a refused action was logged")
	}
}

func TestFindAccounts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.db.Exec(ctx, `
		INSERT INTO domains (account_id, name, mail_from, created_at, check_until) VALUES ($1, 'patsbakery.com', 'bounce', now(), now())`,
		h.acc); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{h.acc, "PAT@", "bakery", "patsbakery.com", "pats"} {
		got, err := h.svc.FindAccounts(ctx, q, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != h.acc || got[0].OwnerEmail == nil || *got[0].OwnerEmail != "pat@patsbakery.com" {
			t.Errorf("%q: %+v", q, got)
		}
	}
	if got, _ := h.svc.FindAccounts(ctx, "100%_", 10); len(got) != 0 {
		t.Errorf("wildcards matched: %+v", got)
	}
	if _, err := h.svc.FindAccounts(ctx, " ", 10); err == nil {
		t.Error("empty query")
	}
}

func TestPauseAndUnpause(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a, err := h.svc.Pause(ctx, h.staff, h.acc, "Spam reports from a list provider")
	if err != nil {
		t.Fatal(err)
	}
	if a.PausedAt == nil || !a.PausedAt.Equal(h.now) || a.PauseReason == nil || *a.PauseReason != "Spam reports from a list provider" {
		t.Errorf("paused = %+v", a.AccountSummary)
	}
	l := h.last(t)
	if l.action != ActionPause || l.actor != "ops@mailgoose.example" || l.userID == nil || *l.userID != h.staff.UserID {
		t.Errorf("log = %+v", l)
	}
	// Pausing again keeps the time and takes the new reason.
	h.now = h.now.Add(time.Hour)
	a, _ = h.svc.Pause(ctx, h.staff, h.acc, "Still checking")
	if !a.PausedAt.Equal(h.now.Add(-time.Hour)) || *a.PauseReason != "Still checking" {
		t.Errorf("paused again = %+v", a.AccountSummary)
	}
	a, err = h.svc.Unpause(ctx, h.staff, h.acc, "Customer cleaned the list")
	if err != nil {
		t.Fatal(err)
	}
	if a.PausedAt != nil || a.PauseReason != nil || h.last(t).action != ActionUnpause {
		t.Errorf("unpaused = %+v", a.AccountSummary)
	}
	if _, err := h.svc.Pause(ctx, h.staff, "00000000-0000-0000-0000-000000000000", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown account: %v", err)
	}
}

func TestGrantCredits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ledger := &credits.Ledger{DB: h.db, Now: func() time.Time { return h.now }}
	before, err := ledger.Summary(ctx, h.acc)
	if err != nil {
		t.Fatal(err)
	}
	g, err := h.svc.GrantCredits(ctx, h.staff, h.acc, 5000, "Sorry for the outage")
	if err != nil {
		t.Fatal(err)
	}
	if g.Balance != before.Balance+5000 || !g.ExpiresAt.Equal(h.now.AddDate(2, 0, 0)) {
		t.Errorf("granted = %+v (balance before %d)", g, before.Balance)
	}
	if l := h.last(t); l.action != ActionGrantCredits {
		t.Errorf("log = %+v", l)
	}
	// The free monthly grant still comes next month.
	h.now = h.now.AddDate(0, 1, 1)
	after, err := ledger.Summary(ctx, h.acc)
	if err != nil {
		t.Fatal(err)
	}
	// Last month's free credits expired; this month's came.
	if after.Balance != 5000+credits.FreeMonthlyGrant {
		t.Errorf("next month: balance %d", after.Balance)
	}
	var ie *InputError
	for _, n := range []int64{0, -5, MaxGrant + 1} {
		if _, err := h.svc.GrantCredits(ctx, h.staff, h.acc, n, "x"); !errors.As(err, &ie) || ie.Field != "amount" {
			t.Errorf("%d: %v", n, err)
		}
	}
}

func TestApproveImport(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var imp string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO imports (account_id, permission_confirmed, email_column, csv, total_rows, review, hold_reason, status)
		VALUES ($1, true, 'email', 'email', 3000, 'held', 'large_first_import', 'done') RETURNING id`, h.acc).Scan(&imp); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO contacts (account_id, email, status, source, held_import_id, import_id)
		SELECT $1, 'c' || i || '@example.com', 'subscribed', 'import', $2, $2 FROM generate_series(1, 3) i`, h.acc, imp); err != nil {
		t.Fatal(err)
	}
	a, err := h.svc.GetAccount(ctx, h.acc)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.HeldImports) != 1 || a.HeldImports[0].ID != imp || a.Contacts != 3 {
		t.Errorf("account = %+v", a)
	}
	got, err := h.svc.ApproveImport(ctx, h.staff, imp, "Checked the source: a signup sheet")
	if err != nil {
		t.Fatal(err)
	}
	if got.Released != 3 || got.AccountID != h.acc {
		t.Errorf("approved = %+v", got)
	}
	var held int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE held_import_id IS NOT NULL`).Scan(&held)
	if held != 0 {
		t.Errorf("%d still held", held)
	}
	if l := h.last(t); l.action != ActionApproveImport || l.target == nil || *l.target != imp {
		t.Errorf("log = %+v", l)
	}
	if _, err := h.svc.ApproveImport(ctx, h.staff, imp, "again"); !errors.Is(err, ErrNotHeld) {
		t.Errorf("again: %v", err)
	}
	if _, err := h.svc.ApproveImport(ctx, h.staff, "00000000-0000-0000-0000-000000000000", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func TestStopCampaign(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var camp string
	if err := h.db.QueryRow(ctx, `
		INSERT INTO campaigns (account_id, name, all_contacts, status) VALUES ($1, 'Bread week', true, 'sending') RETURNING id`,
		h.acc).Scan(&camp); err != nil {
		t.Fatal(err)
	}
	// Ten recipients, credits reserved for them; three already sent.
	err := pgx.BeginFunc(ctx, h.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO campaign_recipients (account_id, campaign_id, status)
			SELECT $1, $2, CASE WHEN i <= 3 THEN 'sent' ELSE 'queued' END FROM generate_series(1, 10) i`, h.acc, camp); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO send_queue (account_id, campaign_id, run_at) VALUES ($1, $2, $3)`, h.acc, camp, h.now); err != nil {
			return err
		}
		_, err := credits.Debit(ctx, tx, h.acc, credits.TypeReserve, 10, "campaign:"+camp, h.now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger := &credits.Ledger{DB: h.db, Now: func() time.Time { return h.now }}
	before, _ := ledger.Summary(ctx, h.acc)

	c, err := h.svc.StopCampaign(ctx, h.staff, camp, "Phishing content")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != campaigns.StatusCancelled {
		t.Errorf("status %s", c.Status)
	}
	after, _ := ledger.Summary(ctx, h.acc)
	if after.Balance != before.Balance+7 {
		t.Errorf("balance %d, want %d", after.Balance, before.Balance+7)
	}
	if l := h.last(t); l.action != ActionStopCampaign || l.target == nil || *l.target != camp {
		t.Errorf("log = %+v", l)
	}
	if _, err := h.svc.StopCampaign(ctx, h.staff, "00000000-0000-0000-0000-000000000000", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	// A draft can't be stopped, and isn't logged.
	var draft string
	_ = h.db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts) VALUES ($1, 'Draft', true) RETURNING id`, h.acc).Scan(&draft)
	n := len(h.log(t))
	var st *campaigns.StatusError
	if _, err := h.svc.StopCampaign(ctx, h.staff, draft, "x"); !errors.As(err, &st) {
		t.Errorf("draft: %v", err)
	}
	if len(h.log(t)) != n {
		t.Error("a refused stop was logged")
	}
}

func TestListActions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, r := range []string{"one", "two", "three"} {
		if _, err := h.svc.Pause(ctx, h.staff, h.acc, r); err != nil {
			t.Fatal(err)
		}
	}
	page, err := h.svc.ListActions(ctx, h.acc, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].Reason != "three" || page[1].Reason != "two" {
		t.Fatalf("first page = %+v", page)
	}
	next, _ := h.svc.ListActions(ctx, h.acc, page[1].ID, 2)
	if len(next) != 1 || next[0].Reason != "one" {
		t.Errorf("second page = %+v", next)
	}
	all, _ := h.svc.ListActions(ctx, "", 0, 10)
	if len(all) != 4 { // and the grant of admin, on the staff account
		t.Errorf("all = %d", len(all))
	}
}
