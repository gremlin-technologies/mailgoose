package credits

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

type harness struct {
	ledger *Ledger
	db     *pgxpool.Pool
	now    time.Time
	mu     sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{db: dbtest.New(t), now: time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)}
	h.ledger = &Ledger{DB: h.db, Now: h.clock}
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) set(t time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = t
}

// account makes an account activated at the harness's current time.
func (h *harness) account(t *testing.T, plan string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(context.Background(),
		`INSERT INTO accounts (name, status, plan, activated_at) VALUES ('A', 'active', $1, $2) RETURNING id`,
		plan, h.clock()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (h *harness) balance(t *testing.T, id string) int64 {
	t.Helper()
	s, err := h.ledger.Summary(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return s.Balance
}

func (h *harness) types(t *testing.T, id string) []string {
	t.Helper()
	hist, err := h.ledger.History(context.Background(), id, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(hist) - 1; i >= 0; i-- {
		out = append(out, hist[i].Type)
	}
	return out
}

func TestFreeGrantAndExpiry(t *testing.T) {
	h := newHarness(t)
	id := h.account(t, "free")

	s, err := h.ledger.Summary(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if s.Balance != FreeMonthlyGrant || s.Plan != "free" || s.NextGrantAt == nil ||
		!s.NextGrantAt.Equal(time.Date(2026, 2, 28, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("summary = %+v (next %v)", s, s.NextGrantAt)
	}
	// Reading again in the same month grants nothing more.
	if b := h.balance(t, id); b != FreeMonthlyGrant {
		t.Errorf("balance = %d", b)
	}

	if _, err := h.ledger.Debit(context.Background(), id, TypeSpend, 300, ""); err != nil {
		t.Fatal(err)
	}
	// The anniversary of Jan 31 in February is the 28th. The 700 left expire
	// and a new 1,000 arrive.
	h.set(time.Date(2026, 2, 28, 9, 0, 0, 0, time.UTC))
	if b := h.balance(t, id); b != FreeMonthlyGrant {
		t.Errorf("after the anniversary: balance = %d, want %d", b, FreeMonthlyGrant)
	}
	want := []string{"grant", "spend", "expiry", "grant"}
	if got := h.types(t, id); !equal(got, want) {
		t.Errorf("history = %v, want %v", got, want)
	}

	// Months with no activity get no backfill: only the current grant.
	h.set(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	if b := h.balance(t, id); b != FreeMonthlyGrant {
		t.Errorf("months later: balance = %d", b)
	}
}

func TestPaidAccountsGetTheGrant(t *testing.T) {
	h := newHarness(t)
	id := h.account(t, "paid")
	s, err := h.ledger.Summary(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if s.Balance != FreeMonthlyGrant || s.Plan != "paid" || s.NextGrantAt == nil ||
		!s.NextGrantAt.Equal(time.Date(2026, 2, 28, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("summary = %+v (next %v)", s, s.NextGrantAt)
	}
}

// An account that upgrades mid-period keeps the grant it has and gets no
// second one; the next period's grant comes on either plan, and a
// cancelled subscription goes on getting it.
func TestGrantAcrossPlanChanges(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := h.account(t, "free")
	setPlan := func(plan string) {
		t.Helper()
		if _, err := h.db.Exec(ctx, `UPDATE accounts SET plan = $2 WHERE id = $1`, id, plan); err != nil {
			t.Fatal(err)
		}
	}
	if b := h.balance(t, id); b != FreeMonthlyGrant {
		t.Fatalf("free: balance = %d", b)
	}
	if _, err := h.ledger.Debit(ctx, id, TypeSpend, 300, ""); err != nil {
		t.Fatal(err)
	}

	// Upgrade mid-period: nothing is added, nothing is taken away.
	h.set(time.Date(2026, 2, 10, 9, 0, 0, 0, time.UTC))
	setPlan("paid")
	if b := h.balance(t, id); b != FreeMonthlyGrant-300 {
		t.Errorf("upgraded mid-period: balance = %d, want %d", b, FreeMonthlyGrant-300)
	}

	// The next period on the paid plan: the leftover expires, a new grant arrives.
	h.set(time.Date(2026, 2, 28, 9, 0, 0, 0, time.UTC))
	if b := h.balance(t, id); b != FreeMonthlyGrant {
		t.Errorf("paid, next period: balance = %d, want %d", b, FreeMonthlyGrant)
	}

	// Cancelled mid-period: the grant stays, and the period after brings another.
	h.set(time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC))
	setPlan("free")
	if b := h.balance(t, id); b != FreeMonthlyGrant {
		t.Errorf("cancelled mid-period: balance = %d", b)
	}
	h.set(time.Date(2026, 3, 31, 9, 0, 0, 0, time.UTC))
	if b := h.balance(t, id); b != FreeMonthlyGrant {
		t.Errorf("free again, next period: balance = %d", b)
	}
	var grants int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM credit_transactions WHERE account_id = $1 AND type = 'grant'`, id).Scan(&grants); err != nil || grants != 3 {
		t.Errorf("%d grants (%v), want 3: one a period", grants, err)
	}
}

func TestClosedAccountsGetNoGrant(t *testing.T) {
	h := newHarness(t)
	id := h.account(t, "free")
	if b := h.balance(t, id); b != FreeMonthlyGrant {
		t.Fatalf("balance = %d", b)
	}
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET status = 'closed', closed_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	h.set(h.clock().AddDate(0, 2, 0))
	s, err := h.ledger.Summary(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if s.NextGrantAt != nil || strings.Count(strings.Join(h.types(t, id), " "), "grant") != 1 {
		t.Errorf("closed account: summary %+v, history %v", s, h.types(t, id))
	}
}

func TestPendingAccountsGetNoGrant(t *testing.T) {
	h := newHarness(t)
	var id string
	if err := h.db.QueryRow(context.Background(), `INSERT INTO accounts (name) VALUES ('P') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if b := h.balance(t, id); b != 0 {
		t.Errorf("pending account balance = %d", b)
	}
}

func TestDebitsUseTheLotExpiringFirst(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	id := h.account(t, "free")
	h.balance(t, id) // the free grant, expiring Feb 28
	// A purchase lasting 24 months (MG-39 makes these).
	var purchase int64
	if err := h.db.QueryRow(ctx, `INSERT INTO credit_transactions (account_id, type, amount, expires_at) VALUES ($1, 'purchase', 10000, $2) RETURNING id`,
		id, h.clock().AddDate(2, 0, 0)).Scan(&purchase); err != nil {
		t.Fatal(err)
	}

	if _, err := h.ledger.Debit(ctx, id, TypeReserve, FreeMonthlyGrant+500, "campaign-1"); err != nil {
		t.Fatal(err)
	}
	// The whole grant, then 500 from the purchase.
	var fromPurchase int64
	if err := h.db.QueryRow(ctx, `SELECT -sum(amount) FROM credit_allocations WHERE lot_id = $1`, purchase).Scan(&fromPurchase); err != nil {
		t.Fatal(err)
	}
	if fromPurchase != 500 {
		t.Errorf("taken from the purchase = %d, want 500", fromPurchase)
	}

	// Releasing 600 gives the purchase its 500 back first, then 100 to the grant.
	got, err := h.ledger.Release(ctx, id, "campaign-1", 600)
	if err != nil || got != 600 {
		t.Fatalf("release = %d, %v", got, err)
	}
	if b := h.balance(t, id); b != FreeMonthlyGrant+10000-(FreeMonthlyGrant+500)+600 {
		t.Errorf("balance = %d", b)
	}
	// Releasing more than is held gives back only what's left.
	if got, err := h.ledger.Release(ctx, id, "campaign-1", 5000); err != nil || got != FreeMonthlyGrant+500-600 {
		t.Errorf("release the rest = %d, %v; want %d", got, err, FreeMonthlyGrant+500-600)
	}
	if got, err := h.ledger.Release(ctx, id, "campaign-1", 1); err != nil || got != 0 {
		t.Errorf("release with nothing held = %d, %v", got, err)
	}

	// At the next anniversary the grant's unused credits expire; the
	// purchase's don't.
	h.set(time.Date(2026, 2, 28, 9, 0, 0, 0, time.UTC))
	if b := h.balance(t, id); b != 10000+FreeMonthlyGrant {
		t.Errorf("after the anniversary: balance = %d", b)
	}
}

func TestReleaseAfterExpiryExpiresAgain(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	id := h.account(t, "free")
	if _, err := h.ledger.Debit(ctx, id, TypeReserve, 400, "c"); err != nil {
		t.Fatal(err)
	}
	h.set(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	// The old grant expired with 400 still reserved; giving them back now
	// mustn't revive expired credits.
	if _, err := h.ledger.Release(ctx, id, "c", 400); err != nil {
		t.Fatal(err)
	}
	if b := h.balance(t, id); b != FreeMonthlyGrant {
		t.Errorf("balance = %d, want %d", b, FreeMonthlyGrant)
	}
}

func TestBalanceNeverGoesNegative(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	id := h.account(t, "free")
	h.balance(t, id)

	var wg sync.WaitGroup
	debits := int(FreeMonthlyGrant / 100)
	errs := make(chan error, 2*debits)
	for i := range 2 * debits {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.ledger.Debit(ctx, id, TypeReserve, 100, string(rune('a'+i)))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	ok, short := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrInsufficientCredits):
			short++
		default:
			t.Errorf("debit: %v", err)
		}
	}
	if ok != debits || short != debits {
		t.Errorf("%d debits went through and %d were refused, want %d and %[3]d", ok, short, debits)
	}
	if b := h.balance(t, id); b != 0 {
		t.Errorf("balance = %d, want 0", b)
	}
	var lowest int64
	if err := h.db.QueryRow(ctx, `
		SELECT min(running) FROM (
		    SELECT sum(amount) OVER (ORDER BY id) AS running FROM credit_transactions WHERE account_id = $1) r`,
		id).Scan(&lowest); err != nil {
		t.Fatal(err)
	}
	if lowest < 0 {
		t.Errorf("the balance went to %d along the way", lowest)
	}
}

func TestHistoryPages(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	id := h.account(t, "free")
	for range 3 {
		if _, err := h.ledger.Debit(ctx, id, TypeSpend, 10, ""); err != nil {
			t.Fatal(err)
		}
	}
	first, err := h.ledger.History(ctx, id, 0, 2)
	if err != nil || len(first) != 2 || first[0].ID < first[1].ID {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	rest, err := h.ledger.History(ctx, id, first[1].ID, 10)
	if err != nil || len(rest) != 2 || rest[1].Type != TypeGrant {
		t.Errorf("rest = %+v, %v", rest, err)
	}
}

func TestBadDebits(t *testing.T) {
	h := newHarness(t)
	id := h.account(t, "free")
	for _, tc := range []struct {
		typ string
		n   int64
	}{{TypeGrant, 1}, {TypeSpend, 0}, {TypeSpend, -5}} {
		if _, err := h.ledger.Debit(context.Background(), id, tc.typ, tc.n, ""); err == nil {
			t.Errorf("Debit(%s, %d) succeeded", tc.typ, tc.n)
		}
	}
	if _, err := h.ledger.Summary(context.Background(), "00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrNoAccount) {
		t.Errorf("unknown account: err = %v", err)
	}
}

func TestPeriod(t *testing.T) {
	anchor := time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ now, start, next string }{
		{"2026-01-31T09:00:00Z", "2026-01-31T09:00:00Z", "2026-02-28T09:00:00Z"},
		{"2026-02-28T08:59:59Z", "2026-01-31T09:00:00Z", "2026-02-28T09:00:00Z"},
		{"2026-02-28T09:00:00Z", "2026-02-28T09:00:00Z", "2026-03-31T09:00:00Z"},
		{"2026-04-30T10:00:00Z", "2026-04-30T09:00:00Z", "2026-05-31T09:00:00Z"},
		{"2027-02-01T00:00:00Z", "2027-01-31T09:00:00Z", "2027-02-28T09:00:00Z"},
		{"2028-02-29T09:00:00Z", "2028-02-29T09:00:00Z", "2028-03-31T09:00:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, tc.now)
		start, next := period(anchor, now)
		if start.Format(time.RFC3339) != tc.start || next.Format(time.RFC3339) != tc.next {
			t.Errorf("period(%s) = %s, %s; want %s, %s", tc.now, start.Format(time.RFC3339), next.Format(time.RFC3339), tc.start, tc.next)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
