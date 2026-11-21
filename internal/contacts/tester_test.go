package contacts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/tester"
)

func (h *harness) makeTester(t *testing.T, account string) {
	t.Helper()
	if _, err := h.svc.DB.Exec(context.Background(), `
		UPDATE accounts SET tester_started_at = $2, tester_until = $3, tester_credits = 500 WHERE id = $1`,
		account, h.now, h.now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

// TestTesterContactLimit: a tester account holds at most tester.Contacts
// counted contacts on any plan (MG-86), and says it's a tester's limit.
func TestTesterContactLimit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.setPlan(t, h.acc, "paid")
	h.makeTester(t, h.acc)
	h.fill(t, h.acc, tester.Contacts-1)
	h.add(t, "last@example.com", StatusSubscribed)
	_, err := h.svc.Create(ctx, h.acc, CreateInput{Email: "over@example.com"})
	var le *LimitError
	if !errors.As(err, &le) || !le.Tester || le.Limit != tester.Contacts || le.Count != tester.Contacts {
		t.Fatalf("over the tester limit: %v (%+v)", err, le)
	}
	// An unsubscribed contact still fits.
	h.add(t, "gone@example.com", StatusUnsubscribed)
}

// TestTesterImportStopsAtTheCap: an import into a tester account stops at
// its contact cap, whatever its plan.
func TestTesterImportStopsAtTheCap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.setPlan(t, h.acc, "paid")
	h.makeTester(t, h.acc)
	h.fill(t, h.acc, tester.Contacts-5)
	im := h.importFile(t, ImportInput{ListSource: ListSourceForm, CSV: rows(12), PermissionConfirmed: true})
	if im.Imported != 5 || im.SkippedByReason[SkipOverLimit] != 7 {
		t.Fatalf("import = %+v", im)
	}
	if n, _ := h.svc.CountedContacts(ctx, h.acc); n != tester.Contacts {
		t.Errorf("counted = %d", n)
	}
}
