package sending

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/policy"
)

// TestLaunchNeedsCurrentTerms checks the re-acceptance gate (MG-68): an
// account on an older version of the terms, or none, can't launch, at the
// summary or the confirm step, until it accepts the current one.
func TestLaunchNeedsCurrentTerms(t *testing.T) {
	h := readyHarness(t)
	ctx := context.Background()
	cur := policy.Current()
	cr := h.summary(t)

	if _, err := h.db.Exec(ctx, `UPDATE accounts SET terms_version = '2000-01-01' WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	_, err := h.launch("")
	var nr *NotReadyError
	if !errors.As(err, &nr) || problemCodes(err) != NewTermsNotAccepted {
		t.Fatalf("summary: %s", problemCodes(err))
	}
	p := nr.Problems[0]
	if !strings.Contains(p.Message, "version "+cur.ID) || !strings.Contains(p.Hint, "mailgoose account accept-terms --version "+cur.ID) ||
		!strings.Contains(p.Hint, policy.ChangesPath) {
		t.Errorf("problem %+v", p)
	}
	if p.Details["current_version"] != cur.ID || p.Details["accepted_version"] != "2000-01-01" {
		t.Errorf("details %v", p.Details)
	}
	// A code issued before the terms changed doesn't get round it.
	if _, err := h.launch(cr.Code); problemCodes(err) != NewTermsNotAccepted {
		t.Fatalf("confirm: %s", problemCodes(err))
	}

	// Never accepted at all.
	if _, err := h.db.Exec(ctx, `UPDATE accounts SET terms_version = NULL WHERE id = $1`, h.acc); err != nil {
		t.Fatal(err)
	}
	_, err = h.launch("")
	if !errors.As(err, &nr) || problemCodes(err) != NewTermsNotAccepted || nr.Problems[0].Details["accepted_version"] != nil {
		t.Fatalf("never accepted: %s %v", problemCodes(err), err)
	}

	// Accepting lets it launch.
	a := &auth.Service{DB: h.db}
	if _, err := a.AcceptTerms(ctx, h.acc, cur.ID, auth.TermsViaCLI); err != nil {
		t.Fatal(err)
	}
	if _, err := h.launch(h.summary(t).Code); err != nil {
		t.Fatalf("launch after accepting: %v", err)
	}
}
