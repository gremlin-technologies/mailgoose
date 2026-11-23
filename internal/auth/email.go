package auth

import (
	"errors"
	"fmt"
	"html"

	"github.com/jackc/pgx/v5/pgconn"

	mgmail "github.com/gremlin-technologies/mailgoose/internal/mail"
)

// What a code email says first, by why the code was sent (MG-123).
const (
	// introSignup finishes a new or still pending sign-up.
	introSignup = "Here's your code to finish signing up for mailgoose."
	// introSignin is a sign-in the person asked for.
	introSignin = "Here's your code to sign in to mailgoose."
	// introSignupExisting is a sign-up made with an address that already
	// has an active account.
	introSignupExisting = "Someone tried to sign up for mailgoose with this address, which already has an account. Here's a code to sign in instead."
)

// codeMessage is the email that carries a code. The code is in the subject
// too, so it shows in a notification without opening the email.
func codeMessage(from mgmail.Address, to, code, intro, accountID string) mgmail.Message {
	const outro = "It works once, for 10 minutes. If you didn't ask for it, you can ignore this email."
	text := fmt.Sprintf("%s\n\n%s\n\n%s\n", intro, code, outro)
	body := fmt.Sprintf(`<!doctype html>
<html><body style="font-family:-apple-system,Segoe UI,Helvetica,Arial,sans-serif;color:#1a1a1a">
<p>%s</p>
<p style="font-size:28px;font-weight:600;letter-spacing:4px;font-family:Menlo,Consolas,monospace">%s</p>
<p style="color:#555">%s</p>
</body></html>
`, html.EscapeString(intro), code, html.EscapeString(outro))
	return mgmail.Message{
		From:      from,
		To:        mgmail.Address{Email: to},
		Subject:   "Your mailgoose code: " + code,
		HTML:      body,
		Text:      text,
		Kind:      mgmail.KindSystem,
		AccountID: accountID,
	}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
