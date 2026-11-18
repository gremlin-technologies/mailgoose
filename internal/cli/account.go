package cli

import (
	"fmt"
	"net/http"
	"os"
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
	CreatedAt      time.Time `json:"created_at"`
}

func (a *App) accountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Show and change account settings",
		Long: `Account settings: the account name, the default From name and address for
new campaigns, the postal address that goes in every email's footer (campaigns
can't launch without it) and the time zone.`,
	}

	get := calls("getAccount", &cobra.Command{
		Use:   "get",
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

	cmd.AddCommand(get, update)
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
	if len(acct.MissingForSend) > 0 {
		fmt.Fprintf(a.Stdout, "\nCampaigns can't launch until you set: %s\n", strings.Join(acct.MissingForSend, ", "))
	}
}
