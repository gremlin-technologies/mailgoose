package sending

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gremlin-technologies/mailgoose/internal/auth"
)

// SignupWaiting tells the operators that a new account is waiting for its
// sign-up review (MG-66). It never fails: it logs, and a note that can't be
// sent is logged too.
func (a *OpsAlert) SignupWaiting(ctx context.Context, w auth.Waiting) {
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	log.InfoContext(ctx, "account waiting for sign-up review", "account_id", w.AccountID, "flags", w.Flags)
	flags := "none (every new account is reviewed)"
	if len(w.Flags) > 0 {
		flags = strings.Join(w.Flags, ", ")
	}
	a.Notify(ctx, w.AccountName+" is waiting for review", fmt.Sprintf(`A new account is waiting for its sign-up review.

Account: %s (%s)
Owner: %s
Flags: %s

Until it's approved it can build emails, import contacts and send tests,
but can't launch campaigns or take form signups.

See every waiting account with:
  mailgoose admin signups
Approve it with:
  mailgoose admin approve-signup %s
Or reject it, which suspends it, with:
  mailgoose admin reject-signup %s --reason "..."
`, w.AccountName, w.AccountID, w.Email, flags, w.AccountID, w.AccountID))
}
