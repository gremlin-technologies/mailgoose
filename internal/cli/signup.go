package cli

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
)

// signupCmd signs up from the CLI with the same emailed code as the web
// (owner decision, Nov 18). It runs in two steps so it never waits for input:
// the first emails a code, the second exchanges it for a stored CLI token. A
// person still has to read the code from their email.
func (a *App) signupCmd() *cobra.Command {
	var email, accountName, code string
	var acceptTerms bool
	cmd := callsAll([]string{"createSignup", "createCliToken"}, &cobra.Command{
		Use:   "signup",
		Short: "Create an account with an emailed code, and sign this CLI in",
		Long: `Signs up in two steps, with the same six-digit code the web sign-up emails.

1. mailgoose signup --email you@example.com --account-name "Your shop" --accept-terms
   creates a pending account on the free plan and emails a code. --accept-terms
   says the person who owns the address accepts the terms of service and the
   anti-spam policy, at /terms and /anti-spam on the mailgoose web app.
2. mailgoose signup --email you@example.com --code 123456
   uses the code, activates the account and stores a full-scope token for the
   API URL in use, like mailgoose login.

The code goes to the address's inbox, so a person has to read it and pass it
on; an agent can't finish a sign-up alone. An address that already has an
account gets a sign-in code instead, and step 2 signs this CLI in to it.
Codes last 10 minutes and stop working after 5 wrong tries. Code emails are
limited to 5 an hour per address and 20 an hour per IP.`,
		Example: `  mailgoose signup --email pat@example.com --account-name "Pat's Bakery" --accept-terms
  mailgoose signup --email pat@example.com --code 042917`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			email = strings.TrimSpace(email)
			if email == "" {
				return usageError("--email is required.", "Give the address to sign up with.")
			}
			s, _, err := a.loadSettings()
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			if code != "" {
				if accountName != "" || acceptTerms {
					return usageError("--code finishes a sign-up; it doesn't take --account-name or --accept-terms.",
						"Run `mailgoose signup --email "+email+" --code <code>` on its own.")
				}
				var tok loginToken
				body := map[string]string{"email": email, "code": strings.TrimSpace(code), "client_name": a.clientName()}
				if _, err := a.send(ctx, s.APIURL, http.MethodPost, "/v1/cli-tokens", body, &tok, ""); err != nil {
					return err
				}
				return a.finishLogin(ctx, s.APIURL, tok)
			}

			if strings.TrimSpace(accountName) == "" {
				return usageError("--account-name is required to sign up.",
					"Name the account, such as your business: --account-name \"Pat's Bakery\". With a code from the email, use --code instead.")
			}
			if !acceptTerms {
				return usageError("Signing up needs --accept-terms.",
					"Pass --accept-terms only if the person who owns "+email+" accepts the terms of service and the anti-spam policy.")
			}
			body := map[string]any{"email": email, "account_name": accountName, "accepted_terms": true}
			if _, err := a.send(ctx, s.APIURL, http.MethodPost, "/v1/signup", body, nil, ""); err != nil {
				return err
			}
			next := fmt.Sprintf("mailgoose signup --email %s --code <code>", email)
			if a.jsonOut {
				a.printJSON(map[string]any{"status": "code_sent", "email": email, "next": next})
				return nil
			}
			fmt.Fprintf(a.Stdout, "If %s can get email, a six-digit code is on its way. It works for 10 minutes.\n", email)
			fmt.Fprintf(a.Stdout, "Finish with:\n\n  %s\n", next)
			return nil
		},
	})
	f := cmd.Flags()
	f.StringVar(&email, "email", "", "the address to sign up with")
	f.StringVar(&accountName, "account-name", "", "the account's name, such as your business (step 1)")
	f.BoolVar(&acceptTerms, "accept-terms", false, "the address's owner accepts the terms of service and the anti-spam policy (step 1)")
	f.StringVar(&code, "code", "", "the six-digit code from the email (step 2)")
	return cmd
}
