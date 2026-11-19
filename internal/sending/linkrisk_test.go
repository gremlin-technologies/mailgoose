package sending

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/linkrisk"
)

const phishing = "https://login-bakery.example/verify"

// alerts records ops alerts.
type alerts struct {
	mu       sync.Mutex
	subjects []string
	texts    []string
}

func (a *alerts) notify(_ context.Context, subject, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.subjects = append(a.subjects, subject)
	a.texts = append(a.texts, text)
}

// withLink gives the harness's email a button to link, and a link safety
// check with nothing listed yet.
func withLink(t *testing.T, h *harness, link string) (*linkrisk.Fake, *alerts) {
	t.Helper()
	doc, err := emails.ParseDocument(emails.KindBlocks, []byte(`{"settings":{"subject":"Fresh bread"},
		"blocks":[{"id":"t","type":"text","text":"Hi"},{"id":"b","type":"button","text":"Order","href":"`+link+`"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if h.email, err = h.svc.Emails.Replace(context.Background(), h.acc, h.email.ID, h.email.Version, doc); err != nil {
		t.Fatal(err)
	}
	f := &linkrisk.Fake{}
	a := &alerts{}
	h.svc.LinkSafety = &linkrisk.Checker{Lookup: f}
	h.svc.Alert = a.notify
	return f, a
}

// relist starts a fresh cache with link listed, as if Web Risk had added
// it since the last lookup.
func relist(h *harness, f *linkrisk.Fake, link string) {
	f.List(link, linkrisk.SocialEngineering)
	h.svc.LinkSafety = &linkrisk.Checker{Lookup: f}
}

func (h *harness) pause(t *testing.T) (pausedAt bool, reason string) {
	t.Helper()
	var r *string
	if err := h.db.QueryRow(context.Background(), `SELECT sending_paused_at IS NOT NULL, sending_paused_reason FROM accounts WHERE id = $1`,
		h.acc).Scan(&pausedAt, &r); err != nil {
		t.Fatal(err)
	}
	if r != nil {
		reason = *r
	}
	return pausedAt, reason
}

func (h *harness) autoPauses(t *testing.T) []map[string]any {
	t.Helper()
	rows, err := h.db.Query(context.Background(), `
		SELECT actor, target_id, reason, details FROM admin_actions WHERE account_id = $1 AND action = 'auto_pause' ORDER BY id`, h.acc)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var actor, target, reason string
		var details []byte
		if err := rows.Scan(&actor, &target, &reason, &details); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		_ = json.Unmarshal(details, &d)
		out = append(out, map[string]any{"actor": actor, "target": target, "reason": reason, "details": d})
	}
	return out
}

func TestUnsafeLinkBlocksTestSend(t *testing.T) {
	h := newHarness(t)
	f, a := withLink(t, h, phishing)
	f.List(phishing, linkrisk.SocialEngineering)

	_, err := h.test()
	var nr *NotReadyError
	if !errors.As(err, &nr) || problemCodes(err) != UnsafeLink {
		t.Fatalf("want unsafe_link, got %v", err)
	}
	p := nr.Problems[0]
	if p.Details["url"] != phishing || !strings.Contains(p.Message, phishing) || !strings.Contains(p.Message, "phishing") {
		t.Errorf("problem %+v", p)
	}
	if len(h.fake.Sent()) != 0 || h.testsCounted(t) != 0 {
		t.Error("the test went out or counted")
	}
	// A test doesn't pause the account; only a launch does.
	if paused, _ := h.pause(t); paused || len(a.subjects) != 0 {
		t.Error("a test send paused the account")
	}
	// The test's own unsubscribe link isn't looked up.
	for _, c := range f.Calls() {
		if strings.Contains(c, "/u/test") {
			t.Errorf("looked up %s", c)
		}
	}
}

func TestLinkSafetyDownStillSends(t *testing.T) {
	h := newHarness(t)
	f, _ := withLink(t, h, phishing)
	f.List(phishing, linkrisk.SocialEngineering)
	f.Err = errors.New("web risk answered 503")
	if _, err := h.test(); err != nil {
		t.Fatalf("test with Web Risk down: %v", err)
	}
	h.svc.LinkSafety = &linkrisk.Checker{} // no API key
	h.buy(t, 10)
	cr := h.summary(t)
	if !strings.Contains(warningCodes(cr.Summary.Warnings), "link_safety_not_checked") {
		t.Errorf("summary warnings %+v", cr.Summary.Warnings)
	}
	if _, err := h.launch(cr.Code); err != nil {
		t.Fatalf("launch without a key: %v", err)
	}
}

func warningCodes(issues []emailcheck.Issue) string {
	var out []string
	for _, i := range issues {
		out = append(out, i.Code)
	}
	return strings.Join(out, ",")
}

func TestUnsafeLinkAtLaunchPausesAccount(t *testing.T) {
	h := newHarness(t)
	f, a := withLink(t, h, phishing)
	h.buy(t, 10)
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	relist(h, f, phishing)

	_, err := h.launch("")
	if problemCodes(err) != UnsafeLink {
		t.Fatalf("summary: %v", problemCodes(err))
	}
	paused, reason := h.pause(t)
	if !paused || !strings.Contains(reason, phishing) || !strings.Contains(reason, `"Bread week"`) {
		t.Errorf("paused %v, reason %q", paused, reason)
	}
	ap := h.autoPauses(t)
	if len(ap) != 1 || ap[0]["actor"] != "automatic" || ap[0]["target"] != h.campaign.ID || ap[0]["reason"] != reason {
		t.Fatalf("admin log %+v", ap)
	}
	if links, _ := ap[0]["details"].(map[string]any)["unsafe_links"].([]any); len(links) != 1 || links[0] != phishing {
		t.Errorf("details %+v", ap[0]["details"])
	}
	if len(a.subjects) != 1 || a.subjects[0] != "Pat's Bakery paused automatically for an unsafe link" ||
		!strings.Contains(a.texts[0], phishing) || !strings.Contains(a.texts[0], "mailgoose admin unpause "+h.acc) {
		t.Errorf("alerts %v %v", a.subjects, a.texts)
	}

	// Trying again finds the pause too, and logs and alerts nothing more.
	_, err = h.launch("")
	if got := problemCodes(err); got != UnsafeLink+","+AccountPaused {
		t.Errorf("again: %s", got)
	}
	if len(h.autoPauses(t)) != 1 || len(a.subjects) != 1 {
		t.Error("a second try logged or alerted again")
	}
}

func TestUnsafeLinkAtConfirmPausesAccount(t *testing.T) {
	h := newHarness(t)
	f, a := withLink(t, h, phishing)
	h.buy(t, 10)
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	cr := h.summary(t)
	relist(h, f, phishing)

	_, err := h.launch(cr.Code)
	if problemCodes(err) != UnsafeLink {
		t.Fatalf("confirm: %v", problemCodes(err))
	}
	if paused, _ := h.pause(t); !paused || len(a.subjects) != 1 || len(h.autoPauses(t)) != 1 {
		t.Errorf("paused %v, alerts %d", paused, len(a.subjects))
	}
	// Nothing was launched or reserved.
	c, err := h.svc.Campaigns.Get(context.Background(), h.acc, h.campaign.ID)
	if err != nil || c.LaunchedAt != nil || h.balance(t) != 10 {
		t.Errorf("campaign %+v, balance %d", c, h.balance(t))
	}
}

func TestUnsafeLinkKeepsAnEarlierPause(t *testing.T) {
	h := newHarness(t)
	f, a := withLink(t, h, phishing)
	h.buy(t, 10)
	if _, err := h.test(); err != nil {
		t.Fatal(err)
	}
	relist(h, f, phishing)
	if _, err := h.db.Exec(context.Background(), `UPDATE accounts SET sending_paused_at = now(), sending_paused_reason = 'Looking into it.' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	_, _ = h.launch("")
	if _, reason := h.pause(t); reason != "Looking into it." || len(a.subjects) != 0 || len(h.autoPauses(t)) != 0 {
		t.Errorf("reason %q, %d alerts", reason, len(a.subjects))
	}
}
