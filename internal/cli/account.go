package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// account is the API's AccountSettings.
type account struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Plan           string    `json:"plan"`
	FromName       *string   `json:"from_name"`
	FromEmail      *string   `json:"from_email"`
	PostalAddress  *string   `json:"postal_address"`
	TimeZone       string    `json:"time_zone"`
	MissingForSend []string  `json:"missing_for_send"`
	SendLimit      sendLimit `json:"send_limit"`
	Terms          terms     `json:"terms"`
	CreatedAt      time.Time `json:"created_at"`
}

// terms is the API's TermsStatus.
type terms struct {
	AcceptedVersion  *string    `json:"accepted_version"`
	AcceptedAt       *time.Time `json:"accepted_at"`
	CurrentVersion   string     `json:"current_version"`
	CurrentEffective string     `json:"current_effective"`
	AcceptanceNeeded bool       `json:"acceptance_needed"`
}

// termsLine describes an accepted version of the terms, for people.
func termsLine(version *string, at *time.Time) string {
	if version == nil {
		return "never accepted"
	}
	if at == nil {
		return "version " + *version
	}
	return fmt.Sprintf("version %s, accepted %s", *version, at.Local().Format("2006-01-02 15:04"))
}

// sendLimit is the API's SendLimit.
type sendLimit struct {
	Daily        int        `json:"daily"`
	Usual        int        `json:"usual"`
	Reason       *string    `json:"reason"`
	LoweredUntil *time.Time `json:"lowered_until"`
}

// String is the limit, with why it's lower than usual.
func (l sendLimit) String() string {
	s := thousands(l.Daily) + " a day"
	if l.Reason != nil {
		s += "\n             " + *l.Reason
	}
	return s
}

// thousands writes n with thousands separators: 10,000.
func thousands(n int) string {
	if n < 0 {
		return "-" + thousands(-n)
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func (a *App) accountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Show and change account settings",
		Long: `Account settings: the account name, the default From name and address for
new campaigns, the postal address that goes in every email's footer (campaigns
can't launch without it) and the time zone. "account get" also shows which
version of mailgoose's terms the account accepted; when the terms change,
"account accept-terms" accepts the new version.`,
	}

	get := calls("getAccount", &cobra.Command{
		Use: "get",
		Example: `  mailgoose account get
  mailgoose account get --json`,
		Short: "Show account settings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var acct account
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/account", nil, &acct, true); err != nil {
				return err
			}
			a.printAccount(acct)
			return nil
		},
	})

	var name, fromName, fromEmail, postal, postalFile, tz string
	update := calls("updateAccount", &cobra.Command{
		Use:   "update",
		Short: "Change account settings; only the flags given change",
		Long: `Changes only the settings whose flags are given. An empty value removes the
default From name, From address or postal address. Give a multi-line postal
address with --postal-address-file, or with \n between lines.`,
		Example: `  mailgoose account update --from-name "Pat's Bakery" --from-email news@patsbakery.example
  mailgoose account update --postal-address "Pat's Bakery\n12 Main St\nSpringfield, IL 62701"
  mailgoose account update --postal-address-file address.txt --time-zone America/Chicago
  mailgoose account update --from-email ""`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			body := map[string]any{}
			optional := func(flag, field, v string) {
				if !f.Changed(flag) {
					return
				}
				if v == "" {
					body[field] = nil
				} else {
					body[field] = v
				}
			}
			if f.Changed("name") {
				body["name"] = name
			}
			optional("from-name", "from_name", fromName)
			optional("from-email", "from_email", fromEmail)
			if f.Changed("postal-address") && f.Changed("postal-address-file") {
				return usageError("Give --postal-address or --postal-address-file, not both.", "")
			}
			if f.Changed("postal-address-file") {
				b, err := os.ReadFile(postalFile) //nolint:gosec // G304: the person names the file to read.
				if err != nil {
					return usageError("Can't read the postal address file: "+err.Error(), "")
				}
				postal = string(b)
				optional("postal-address-file", "postal_address", strings.TrimSpace(postal))
			} else {
				optional("postal-address", "postal_address", strings.ReplaceAll(postal, `\n`, "\n"))
			}
			if f.Changed("time-zone") {
				body["time_zone"] = tz
			}
			if len(body) == 0 {
				return usageError("Nothing to change.", "Give at least one of --name, --from-name, --from-email, --postal-address, --postal-address-file or --time-zone.")
			}
			var acct account
			if err := a.call(cmd.Context(), http.MethodPatch, "/v1/account", body, &acct, true); err != nil {
				return err
			}
			a.printAccount(acct)
			return nil
		},
	})
	uf := update.Flags()
	uf.StringVar(&name, "name", "", "account name")
	uf.StringVar(&fromName, "from-name", "", `default From name ("" removes it)`)
	uf.StringVar(&fromEmail, "from-email", "", `default From address ("" removes it)`)
	uf.StringVar(&postal, "postal-address", "", `postal address, lines separated by \n ("" removes it)`)
	uf.StringVar(&postalFile, "postal-address-file", "", "read the postal address from this file")
	uf.StringVar(&tz, "time-zone", "", "IANA time zone, such as America/New_York")

	var confirm string
	del := calls("deleteAccount", &cobra.Command{
		Use:   "delete",
		Short: "Delete the account and everything in it",
		Long: `Deletes the account at once: emails, contacts, tags, imports, the suppression
list, sending domains (and their SES identities), API keys and every sign-in.
It can't be undone. The credit ledger is kept, with the account's name and
settings cleared, because payments and refunds need it.

Pass --confirm with the account's exact name, as "mailgoose account get" shows
it. Only a signed-in person or a full-scope API key can delete an account.`,
		Example: `  mailgoose account delete --confirm "Pat's Bakery"`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if confirm == "" {
				return needFlag("confirm", "The account's exact name (deleting can't be undone)")
			}
			path := "/v1/account?" + url.Values{"confirm": {confirm}}.Encode()
			if err := a.call(cmd.Context(), http.MethodDelete, path, nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"name": confirm, "deleted": true})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted the account %s.\n", confirm)
			fmt.Fprintln(a.Stdout, "Its API keys and logins no longer work. Run `mailgoose logout` to remove a stored login from this computer.")
			return nil
		},
	})
	del.Flags().StringVar(&confirm, "confirm", "", "the account's exact name; deleting can't be undone")

	var termsVersion string
	accept := calls("acceptTerms", &cobra.Command{
		Use:   "accept-terms --version <version>",
		Short: "Accept the current terms of service and policies",
		Long: `Accepts the current version of mailgoose's terms of service, anti-spam
policy, privacy policy and data processing agreement. When they change,
campaigns can't launch until the new version is accepted.

Read them first, and what changed, in the web app at /terms and
/policies/changes. "mailgoose account get" shows the version the account
accepted and the current one. --version must be the current version, so
you accept exactly what you read. Only a signed-in person or a full-scope
API key can accept.`,
		Example: `  mailgoose account accept-terms --version 2026-11-19
  mailgoose account accept-terms --version 2026-11-19 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if termsVersion == "" {
				return needFlag("version", "The version you accept, as `mailgoose account get` shows it")
			}
			var acct account
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/account/terms", map[string]any{"version": termsVersion}, &acct, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(acct)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Accepted the terms, version %s.\n", acct.Terms.CurrentVersion)
			return nil
		},
	})
	accept.Flags().StringVar(&termsVersion, "version", "", "the version you accept; it must be the current one")

	cmd.AddCommand(get, update, accept, del)
	return cmd
}

func (a *App) printAccount(acct account) {
	if a.jsonOut {
		a.printJSON(acct)
		return
	}
	orNone := func(p *string) string {
		if p == nil {
			return "(not set)"
		}
		return *p
	}
	fmt.Fprintf(a.Stdout, "Account:     %s (%s plan)\n", acct.Name, acct.Plan)
	fmt.Fprintf(a.Stdout, "ID:          %s\n", acct.ID)
	fmt.Fprintf(a.Stdout, "From name:   %s\n", orNone(acct.FromName))
	fmt.Fprintf(a.Stdout, "From email:  %s\n", orNone(acct.FromEmail))
	addr := orNone(acct.PostalAddress)
	fmt.Fprintf(a.Stdout, "Postal:      %s\n", strings.ReplaceAll(addr, "\n", "\n             "))
	fmt.Fprintf(a.Stdout, "Time zone:   %s\n", acct.TimeZone)
	if acct.SendLimit.Daily > 0 {
		fmt.Fprintf(a.Stdout, "Send limit:  %s\n", acct.SendLimit)
	}
	fmt.Fprintf(a.Stdout, "Terms:       %s\n", termsLine(acct.Terms.AcceptedVersion, acct.Terms.AcceptedAt))
	if acct.Terms.AcceptanceNeeded {
		fmt.Fprintf(a.Stdout, "\nThe terms changed: version %s took effect on %s. Read what changed in the web app at /policies/changes,\n", acct.Terms.CurrentVersion, acct.Terms.CurrentEffective)
		fmt.Fprintf(a.Stdout, "then accept with `mailgoose account accept-terms --version %s`. Campaigns can't launch until you do.\n", acct.Terms.CurrentVersion)
	}
	if len(acct.MissingForSend) > 0 {
		fmt.Fprintf(a.Stdout, "\nCampaigns can't launch until you set: %s\n", strings.Join(acct.MissingForSend, ", "))
	}
}
