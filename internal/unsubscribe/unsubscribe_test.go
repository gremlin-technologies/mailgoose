package unsubscribe

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
	db  *pgxpool.Pool
	svc *Service
	acc string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := dbtest.New(t)
	h := &harness{db: db, svc: &Service{DB: db}}
	if err := db.QueryRow(context.Background(), `INSERT INTO accounts (name) VALUES ('Pat''s Bakery') RETURNING id`).Scan(&h.acc); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) contact(t *testing.T, account, email, status string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(context.Background(),
		`INSERT INTO contacts (account_id, email, status, source) VALUES ($1, $2, $3, 'api') RETURNING id`, account, email, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (h *harness) campaign(t *testing.T, account string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(context.Background(),
		`INSERT INTO campaigns (account_id, name, all_contacts, status) VALUES ($1, 'Fresh bread', true, 'sent') RETURNING id`, account).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (h *harness) token(t *testing.T, account, contact, campaign string) string {
	t.Helper()
	g, err := h.svc.Signer(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := g.Token(contact, campaign)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (h *harness) status(t *testing.T, contact string) string {
	t.Helper()
	var s string
	if err := h.db.QueryRow(context.Background(), `SELECT status FROM contacts WHERE id = $1`, contact).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (h *harness) records(t *testing.T) []string {
	t.Helper()
	rows, err := h.db.Query(context.Background(),
		`SELECT coalesce(contact_id::text, '-') || ' ' || coalesce(campaign_id::text, '-') || ' ' || source FROM unsubscribes ORDER BY created_at, campaign_id NULLS LAST`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestUnsubscribe(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	h.svc.Now = func() time.Time { return at }
	pat := h.contact(t, h.acc, "pat@example.com", "subscribed")
	cmp := h.campaign(t, h.acc)
	tok := h.token(t, h.acc, pat, cmp)

	g, _ := h.svc.Signer(ctx, h.acc)
	url, _ := g.URL("https://app.mailgoose.example", pat, cmp)
	if url != "https://app.mailgoose.example/u/"+tok || !strings.HasPrefix(tok, "u1") || len(tok) != 88 {
		t.Errorf("url %s (token %d chars)", url, len(tok))
	}

	// Looking changes nothing.
	tg, err := h.svc.Lookup(ctx, tok)
	if err != nil || tg.Email != "pat@example.com" || tg.AccountName != "Pat's Bakery" || tg.CampaignID != cmp || !tg.Subscribed() {
		t.Fatalf("lookup %+v %v", tg, err)
	}
	if h.status(t, pat) != "subscribed" || len(h.records(t)) != 0 {
		t.Fatal("lookup changed something")
	}

	// Unsubscribing takes effect at once and is recorded with the time,
	// campaign and source.
	tg, changed, err := h.svc.Unsubscribe(ctx, tok, SourceOneClick)
	if err != nil || !changed || tg.Status != "unsubscribed" || tg.Subscribed() {
		t.Fatalf("unsubscribe %+v %v %v", tg, changed, err)
	}
	var changedAt time.Time
	_ = h.db.QueryRow(ctx, `SELECT status_changed_at FROM contacts WHERE id = $1`, pat).Scan(&changedAt)
	if h.status(t, pat) != "unsubscribed" || !changedAt.Equal(at) {
		t.Errorf("contact %s at %v", h.status(t, pat), changedAt)
	}
	if got := h.records(t); len(got) != 1 || got[0] != pat+" "+cmp+" one_click" {
		t.Errorf("records %v", got)
	}
	var recordedAt time.Time
	_ = h.db.QueryRow(ctx, `SELECT created_at FROM unsubscribes`).Scan(&recordedAt)
	if !recordedAt.Equal(at) {
		t.Errorf("recorded at %v", recordedAt)
	}

	// Again: the same answer and no second record.
	if tg, changed, err = h.svc.Unsubscribe(ctx, tok, SourceLink); err != nil || changed || tg.Status != "unsubscribed" {
		t.Errorf("again %+v %v %v", tg, changed, err)
	}
	if len(h.records(t)) != 1 {
		t.Error("a second unsubscribe was recorded")
	}

	// A link with no campaign (a test send), and a bounced contact, which
	// stays bounced.
	jo := h.contact(t, h.acc, "jo@example.com", "unconfirmed")
	if _, changed, err := h.svc.Unsubscribe(ctx, h.token(t, h.acc, jo, ""), SourceLink); err != nil || !changed {
		t.Errorf("unconfirmed: %v %v", changed, err)
	}
	bo := h.contact(t, h.acc, "bo@example.com", "bounced")
	if tg, changed, err := h.svc.Unsubscribe(ctx, h.token(t, h.acc, bo, cmp), SourceLink); err != nil || changed || tg.Status != "bounced" {
		t.Errorf("bounced: %+v %v %v", tg, changed, err)
	}
	if got := h.records(t); len(got) != 2 || got[1] != jo+" - link" {
		t.Errorf("records %v", got)
	}

	// Records outlive the contact and the campaign.
	if _, err := h.db.Exec(ctx, `DELETE FROM contacts WHERE id = $1`, pat); err != nil {
		t.Fatal(err)
	}
	if got := h.records(t); got[0] != "- "+cmp+" one_click" {
		t.Errorf("after contact delete: %v", got)
	}
	if _, err := h.svc.Lookup(ctx, tok); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted contact: %v", err)
	}
}

func TestBadTokens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pat := h.contact(t, h.acc, "pat@example.com", "subscribed")
	tok := h.token(t, h.acc, pat, "")

	var other string
	_ = h.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('B') RETURNING id`).Scan(&other)
	forged := h.token(t, other, pat, "")

	// Changing any part (account, contact, campaign or signature) breaks
	// the signature.
	changed := func(i int) string {
		b := []byte(tok)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return string(b)
	}
	bad := map[string]string{
		"empty":      "",
		"old format": "u0" + tok[2:],
		"no prefix":  tok[2:],
		"cut short":  tok[:len(tok)-1],
		"too long":   tok + "A",
		"not base64": tok[:len(tok)-1] + "!",
		"padded":     tok[:len(tok)-2] + "==",
		"account":    changed(5),
		"contact":    changed(30),
		"campaign":   changed(50),
		"signature":  changed(len(tok) - 1),
		"keyless":    "u1" + strings.Repeat("A", 86),
	}
	for name, b := range bad {
		if _, err := h.svc.Lookup(ctx, b); !errors.Is(err, ErrBadToken) {
			t.Errorf("%s: %v", name, err)
		}
		if _, _, err := h.svc.Unsubscribe(ctx, b, SourceLink); !errors.Is(err, ErrBadToken) {
			t.Errorf("%s unsubscribe: %v", name, err)
		}
	}
	// Another account's own token can't reach this account's contact.
	if _, _, err := h.svc.Unsubscribe(ctx, forged, SourceLink); !errors.Is(err, ErrNotFound) {
		t.Errorf("other account: %v", err)
	}
	if h.status(t, pat) != "subscribed" || len(h.records(t)) != 0 {
		t.Error("a bad token changed something")
	}

	// A well-formed token for a contact that doesn't exist.
	g, _ := h.svc.Signer(ctx, h.acc)
	ghost, _ := g.Token("00000000-0000-4000-8000-000000000001", "")
	if _, err := h.svc.Lookup(ctx, ghost); !errors.Is(err, ErrNotFound) {
		t.Errorf("ghost: %v", err)
	}
	if _, err := g.Token("not-a-uuid", ""); err == nil {
		t.Error("bad contact id made a token")
	}
}

func TestSignerKeyIsStable(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	pat := h.contact(t, h.acc, "pat@example.com", "subscribed")
	var wg sync.WaitGroup
	toks := make([]string, 8)
	for i := range toks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g, err := h.svc.Signer(ctx, h.acc)
			if err != nil {
				t.Error(err)
				return
			}
			toks[i], _ = g.Token(pat, "")
		}()
	}
	wg.Wait()
	for _, tok := range toks[1:] {
		if tok != toks[0] {
			t.Fatal("racing signers made different keys")
		}
	}
	var n int
	_ = h.db.QueryRow(ctx, `SELECT count(*) FROM unsubscribe_keys`).Scan(&n)
	if n != 1 {
		t.Errorf("%d keys", n)
	}
}

func TestCampaignOfAnotherAccountIsNotRecorded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var other string
	_ = h.db.QueryRow(ctx, `INSERT INTO accounts (name) VALUES ('B') RETURNING id`).Scan(&other)
	theirs := h.campaign(t, other)
	pat := h.contact(t, h.acc, "pat@example.com", "subscribed")
	if _, changed, err := h.svc.Unsubscribe(ctx, h.token(t, h.acc, pat, theirs), SourceLink); err != nil || !changed {
		t.Fatalf("%v %v", changed, err)
	}
	if got := h.records(t); len(got) != 1 || got[0] != pat+" - link" {
		t.Errorf("records %v", got)
	}
}
