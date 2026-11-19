package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

const secret = "whsec_test_secret"

func TestParsePacks(t *testing.T) {
	got, err := ParsePacks("")
	if err != nil || len(got) != len(DefaultPacks) {
		t.Fatalf("empty: %v, %v", got, err)
	}
	got, err = ParsePacks(" Small=100:50 , big=1000:400")
	if err != nil {
		t.Fatal(err)
	}
	want := []Pack{{"small", 100, 50}, {"big", 1000, 400}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"x", "a=1", "a=1:", "a=0:5", "a=5:0", "a=-1:5", "=1:2", "a=1:2,a=3:4", "a=x:2"} {
		if _, err := ParsePacks(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestVerifySignature(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"id":"evt_1"}`)
	good := Sign(body, secret, now)
	cases := []struct {
		name   string
		body   []byte
		header string
		secret string
		ok     bool
	}{
		{"good", body, good, secret, true},
		{"one of several signatures", body, good + ",v1=00ff", secret, true},
		{"changed body", []byte(`{"id":"evt_2"}`), good, secret, false},
		{"other secret", body, good, "whsec_other", false},
		{"no secret", body, good, "", false},
		{"no header", body, "", secret, false},
		{"no signature", body, "t=" + fmt.Sprint(now.Unix()), secret, false},
		{"too old", body, Sign(body, secret, now.Add(-6*time.Minute)), secret, false},
		{"from the future", body, Sign(body, secret, now.Add(6*time.Minute)), secret, false},
		{"a little old", body, Sign(body, secret, now.Add(-4*time.Minute)), secret, true},
	}
	for _, c := range cases {
		err := VerifySignature(c.body, c.header, c.secret, now)
		if (err == nil) != c.ok {
			t.Errorf("%s: err %v", c.name, err)
		}
	}
}

type harness struct {
	db  *pgxpool.Pool
	svc *Service
	now time.Time
	acc string
	mu  sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{db: dbtest.New(t), now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	h.svc = &Service{DB: h.db, WebhookSecret: secret, Packs: DefaultPacks, BaseURL: "https://app.example/", Now: h.clock}
	if err := h.db.QueryRow(context.Background(),
		`INSERT INTO accounts (name, status, plan, activated_at) VALUES ('Bakery', 'active', 'free', $1) RETURNING id`,
		h.now.AddDate(0, -1, 0)).Scan(&h.acc); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

// event is a Stripe Checkout event for the harness's account.
func (h *harness) event(id, typ, session, status, pack string, amount int64) []byte {
	credits := map[string]string{"10k": "10000", "50k": "50000"}[pack]
	b, _ := json.Marshal(map[string]any{
		"id": id, "type": typ,
		"data": map[string]any{"object": map[string]any{
			"id": session, "object": "checkout.session", "payment_status": status,
			"client_reference_id": h.acc, "amount_subtotal": amount, "currency": "usd",
			"metadata": map[string]string{"account_id": h.acc, "pack": pack, "credits": credits},
		}},
	})
	return b
}

func (h *harness) send(t *testing.T, body []byte) Outcome {
	t.Helper()
	out, err := h.svc.HandleWebhook(context.Background(), body, Sign(body, secret, h.clock()))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (h *harness) balance(t *testing.T) int64 {
	t.Helper()
	s, err := (&credits.Ledger{DB: h.db, Now: h.clock}).Summary(context.Background(), h.acc)
	if err != nil {
		t.Fatal(err)
	}
	return s.Balance
}

func TestWebhookBuysCredits(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	free := h.balance(t)
	if free == 0 {
		t.Fatal("the free account has no grant to start with")
	}

	if got := h.send(t, h.event("evt_1", "checkout.session.completed", "cs_1", "paid", "10k", 2000)); got != OutcomeCredited {
		t.Fatalf("outcome %s", got)
	}
	if got := h.balance(t); got != free+10_000 {
		t.Errorf("balance %d, want %d", got, free+10_000)
	}
	var plan string
	var paidSince *time.Time
	var expires time.Time
	if err := h.db.QueryRow(ctx, `SELECT plan, paid_since FROM accounts WHERE id = $1`, h.acc).Scan(&plan, &paidSince); err != nil {
		t.Fatal(err)
	}
	if plan != "paid" || paidSince == nil || !paidSince.Equal(h.now) {
		t.Errorf("plan %s since %v", plan, paidSince)
	}
	if err := h.db.QueryRow(ctx, `SELECT expires_at FROM credit_transactions WHERE account_id = $1 AND ref = 'stripe:cs_1'`, h.acc).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if want := h.now.AddDate(0, 24, 0); !expires.Equal(want) {
		t.Errorf("expires %v, want %v (24 months)", expires, want)
	}

	// The same event again, and another event for the same session.
	if got := h.send(t, h.event("evt_1", "checkout.session.completed", "cs_1", "paid", "10k", 2000)); got != OutcomeDuplicate {
		t.Errorf("redelivered event: %s", got)
	}
	if got := h.send(t, h.event("evt_2", "checkout.session.async_payment_succeeded", "cs_1", "paid", "10k", 2000)); got != OutcomeDuplicate {
		t.Errorf("second event for the session: %s", got)
	}
	if got := h.balance(t); got != free+10_000 {
		t.Errorf("balance %d after duplicates", got)
	}

	// A month on, a paid account gets no free grant, and a second purchase
	// keeps the first paid_since.
	h.mu.Lock()
	h.now = h.now.AddDate(0, 1, 1)
	h.mu.Unlock()
	if got := h.send(t, h.event("evt_3", "checkout.session.completed", "cs_2", "paid", "50k", 7500)); got != OutcomeCredited {
		t.Fatalf("second purchase: %s", got)
	}
	var grants int
	if err := h.db.QueryRow(ctx, `SELECT count(*) FROM credit_transactions WHERE account_id = $1 AND type = 'grant' AND created_at > $2`,
		h.acc, *paidSince).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Errorf("%d free grants after paying", grants)
	}
	var since time.Time
	if err := h.db.QueryRow(ctx, `SELECT paid_since FROM accounts WHERE id = $1`, h.acc).Scan(&since); err != nil {
		t.Fatal(err)
	}
	if !since.Equal(*paidSince) {
		t.Errorf("paid_since moved to %v", since)
	}
}

func TestWebhookAddsNothing(t *testing.T) {
	cases := []struct {
		name string
		body func(h *harness) []byte
		want Outcome
	}{
		{"unpaid", func(h *harness) []byte {
			return h.event("evt_u", "checkout.session.completed", "cs_u", "unpaid", "10k", 2000)
		}, OutcomeUnpaid},
		{"other event", func(h *harness) []byte {
			return h.event("evt_o", "charge.refunded", "cs_o", "paid", "10k", 2000)
		}, OutcomeIgnored},
		{"wrong amount", func(h *harness) []byte {
			return h.event("evt_a", "checkout.session.completed", "cs_a", "paid", "10k", 1)
		}, OutcomeMismatch},
		{"unknown pack", func(h *harness) []byte {
			return h.event("evt_p", "checkout.session.completed", "cs_p", "paid", "free-money", 2000)
		}, OutcomeMismatch},
		{"credits don't match the pack", func(h *harness) []byte {
			return []byte(strings.Replace(string(h.event("evt_c", "checkout.session.completed", "cs_c", "paid", "10k", 2000)),
				`"credits":"10000"`, `"credits":"99999"`, 1))
		}, OutcomeMismatch},
		{"another currency", func(h *harness) []byte {
			return []byte(strings.Replace(string(h.event("evt_e", "checkout.session.completed", "cs_e", "paid", "10k", 2000)),
				`"currency":"usd"`, `"currency":"eur"`, 1))
		}, OutcomeMismatch},
		{"client reference isn't the account", func(h *harness) []byte {
			return []byte(strings.Replace(string(h.event("evt_r", "checkout.session.completed", "cs_r", "paid", "10k", 2000)),
				`"client_reference_id":"`+h.acc+`"`, `"client_reference_id":"someone-else"`, 1))
		}, OutcomeMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			before := h.balance(t)
			if got := h.send(t, c.body(h)); got != c.want {
				t.Errorf("outcome %s, want %s", got, c.want)
			}
			if got := h.balance(t); got != before {
				t.Errorf("balance %d, was %d", got, before)
			}
			var plan string
			if err := h.db.QueryRow(context.Background(), `SELECT plan FROM accounts WHERE id = $1`, h.acc).Scan(&plan); err != nil {
				t.Fatal(err)
			}
			if plan != "free" {
				t.Errorf("plan %s", plan)
			}
		})
	}
}

// A session carries the price and credits it was opened at, so changing
// CREDIT_PACKS while a checkout is open still credits what was paid for.
func TestWebhookPriceChangedWhileOpen(t *testing.T) {
	h := newHarness(t)
	stamped := func(id, session string, amount int64) []byte {
		return []byte(strings.Replace(string(h.event(id, "checkout.session.completed", session, "paid", "10k", amount)),
			`"credits":"10000"`, `"credits":"10000","price_cents":"2000"`, 1))
	}
	// The 10k pack now costs $25 and is renamed; the open session was $20.
	h.svc.Packs = []Pack{{Name: "10k-new", Credits: 10_000, PriceCents: 2_500}}
	before := h.balance(t)
	if got := h.send(t, stamped("evt_1", "cs_1", 2000)); got != OutcomeCredited {
		t.Fatalf("outcome %s", got)
	}
	if got := h.balance(t) - before; got != 10_000 {
		t.Errorf("added %d credits", got)
	}
	// What was charged must still match the stamped price.
	if got := h.send(t, stamped("evt_2", "cs_2", 1)); got != OutcomeMismatch {
		t.Errorf("underpaid: outcome %s", got)
	}
	if got := h.balance(t) - before; got != 10_000 {
		t.Errorf("added %d credits in all", got)
	}
}

// A paid session for an account that's gone, closed or not an id adds
// nothing and isn't retried.
func TestWebhookNoAccount(t *testing.T) {
	cases := map[string]func(h *harness) string{
		"missing":   func(*harness) string { return "0b7b0b9e-5d4e-4a0e-9a53-5c1f4c1e2f00" },
		"not an id": func(*harness) string { return "acc_nope" },
		"closed": func(h *harness) string {
			if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET status = 'closed', closed_at = $2 WHERE id = $1`, h.acc, h.now); err != nil {
				t.Fatal(err)
			}
			return h.acc
		},
	}
	for name, account := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			acc := account(h)
			body := []byte(strings.ReplaceAll(string(h.event("evt_1", "checkout.session.completed", "cs_1", "paid", "10k", 2000)),
				`"`+h.acc+`"`, `"`+acc+`"`))
			out, err := h.svc.HandleWebhook(context.Background(), body, Sign(body, secret, h.now))
			if err != nil || out != OutcomeNoAccount {
				t.Fatalf("outcome %s, %v", out, err)
			}
			var n int
			if err := h.db.QueryRow(context.Background(), `SELECT count(*) FROM credit_transactions WHERE type = 'purchase'`).Scan(&n); err != nil || n != 0 {
				t.Errorf("%d purchases, %v", n, err)
			}
			// Recorded, so a redelivery is a duplicate rather than a retry loop.
			if out, err := h.svc.HandleWebhook(context.Background(), body, Sign(body, secret, h.now)); err != nil || out != OutcomeDuplicate {
				t.Errorf("redelivered: %s, %v", out, err)
			}
		})
	}
}

func TestWebhookBadSignature(t *testing.T) {
	h := newHarness(t)
	body := h.event("evt_1", "checkout.session.completed", "cs_1", "paid", "10k", 2000)
	for name, sig := range map[string]string{
		"none":         "",
		"other secret": Sign(body, "whsec_other", h.now),
		"old":          Sign(body, secret, h.now.Add(-time.Hour)),
	} {
		if _, err := h.svc.HandleWebhook(context.Background(), body, sig); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A signed body that isn't an event.
	junk := []byte(`not json`)
	if _, err := h.svc.HandleWebhook(context.Background(), junk, Sign(junk, secret, h.now)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("junk: %v", err)
	}
	var n int
	if err := h.db.QueryRow(context.Background(), `SELECT count(*) FROM stripe_events`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d events recorded (%v)", n, err)
	}
}

// TestConcurrentEventsCreditOnce sends two events for one session at once.
func TestConcurrentEventsCreditOnce(t *testing.T) {
	h := newHarness(t)
	before := h.balance(t)
	var wg sync.WaitGroup
	outs := make([]Outcome, 2)
	errs := make([]error, 2)
	for i, typ := range []string{"checkout.session.completed", "checkout.session.async_payment_succeeded"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := h.event(fmt.Sprintf("evt_%d", i), typ, "cs_same", "paid", "10k", 2000)
			outs[i], errs[i] = h.svc.HandleWebhook(context.Background(), body, Sign(body, secret, h.clock()))
		}()
	}
	wg.Wait()
	credited := 0
	for i := range outs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if outs[i] == OutcomeCredited {
			credited++
		}
	}
	if credited != 1 {
		t.Errorf("credited %d times: %v", credited, outs)
	}
	if got := h.balance(t); got != before+10_000 {
		t.Errorf("balance %d, want %d", got, before+10_000)
	}
}

type fakeStripe struct {
	got []SessionRequest
	err error
}

func (f *fakeStripe) CreateCheckoutSession(_ context.Context, r SessionRequest) (*Session, error) {
	f.got = append(f.got, r)
	if f.err != nil {
		return nil, f.err
	}
	return &Session{ID: "cs_test", URL: "https://checkout.stripe.test/c/cs_test", ExpiresAt: time.Unix(1, 0)}, nil
}

func TestCheckout(t *testing.T) {
	ctx := context.Background()
	svc := &Service{Packs: DefaultPacks, BaseURL: "https://app.example/"}
	if _, _, err := svc.Checkout(ctx, "acc", "10k", ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no Stripe: %v", err)
	}
	f := &fakeStripe{}
	svc.Stripe = f
	var unknown *UnknownPackError
	if _, _, err := svc.Checkout(ctx, "acc", "5k", ""); !errors.As(err, &unknown) || len(unknown.Packs) != 4 {
		t.Errorf("unknown pack: %v", err)
	}
	sess, pack, err := svc.Checkout(ctx, "acc", " 50K ", "key")
	if err != nil {
		t.Fatal(err)
	}
	if sess.URL == "" || pack.Name != "50k" || pack.PriceCents != 7500 {
		t.Errorf("session %+v pack %+v", sess, pack)
	}
	want := SessionRequest{AccountID: "acc", Pack: pack, SuccessURL: "https://app.example/credits?checkout=done",
		CancelURL: "https://app.example/credits?checkout=cancelled", IdempotencyKey: "key"}
	if len(f.got) != 1 || f.got[0] != want {
		t.Errorf("Stripe got %+v", f.got)
	}
}
