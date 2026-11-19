package sending

import (
	"fmt"

	"github.com/gremlin-technologies/mailgoose/internal/policy"
)

// termsProblem stops a launch while the account hasn't accepted the current
// version of the terms (MG-68); accepted is the version it last accepted,
// "" for none. Nil when it's up to date.
func termsProblem(accepted string) *Problem {
	cur := policy.Current()
	if accepted == cur.ID {
		return nil
	}
	msg := fmt.Sprintf("mailgoose's terms changed on %s (version %s), and this account hasn't accepted the new version yet.",
		cur.EffectiveDate().Format("January 2, 2006"), cur.ID)
	var was any
	if accepted == "" {
		msg = fmt.Sprintf("This account hasn't accepted mailgoose's terms (version %s) yet.", cur.ID)
	} else {
		was = accepted
	}
	return &Problem{Code: NewTermsNotAccepted, Message: msg,
		Hint: fmt.Sprintf("Read what changed on the web app's %s page and the terms at /terms, then accept with "+
			"`mailgoose account accept-terms --version %s`, or in the web app.", policy.ChangesPath, cur.ID),
		Details: map[string]any{"current_version": cur.ID, "accepted_version": was}}
}
