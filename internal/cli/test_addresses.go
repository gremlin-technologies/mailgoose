package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// Test addresses (MG-80): who an account in test mode may send to.

// testAddress is the API's TestAddress.
type testAddress struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	Status           string     `json:"status"`
	ConfirmSentAt    *time.Time `json:"confirm_sent_at"`
	ConfirmExpiresAt *time.Time `json:"confirm_expires_at"`
	ConfirmedAt      *time.Time `json:"confirmed_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

type testAddressPage struct {
	Data       []testAddress `json:"data"`
	NextCursor *string       `json:"next_cursor"`
}

// testAddressID turns a test address's email or id into its id, looking the
// email up.
func (a *App) testAddressID(ctx context.Context, arg string) (string, error) {
	if domainIDPattern.MatchString(arg) {
		return arg, nil
	}
	var page testAddressPage
	if err := a.call(ctx, http.MethodGet, "/v1/test-addresses", nil, &page, true); err != nil {
		return "", err
	}
	for _, t := range page.Data {
		if strings.EqualFold(t.Email, strings.TrimSpace(arg)) {
			return t.ID, nil
		}
	}
	return "", &Error{Exit: ExitNotFound, Code: "not_found", Message: fmt.Sprintf("This account has no test address %q.", arg),
		Hint: "`mailgoose test-addresses list` shows them."}
}

func testAddressState(t testAddress) string {
	switch {
	case t.Status == "confirmed" && t.ConfirmedAt != nil:
		return "confirmed " + t.ConfirmedAt.Local().Format("2006-01-02")
	case t.ConfirmExpiresAt != nil && !t.ConfirmExpiresAt.After(time.Now()):
		return "pending (link expired; resend it)"
	case t.ConfirmSentAt == nil:
		return "pending (link not sent; resend it)"
	default:
		return "pending (link sent " + t.ConfirmSentAt.Local().Format("2006-01-02 15:04") + ")"
	}
}

func (a *App) testAddressesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "test-addresses",
		Aliases: []string{"test-address"},
		Short:   "Manage the addresses an account in test mode may send to",
		Long: `An account in test mode sends campaigns and tests only to its confirmed test
addresses and the owner's own sign-in address, and its forms take signups
only from them. It can have up to 5 test addresses, pending ones included.
Each is confirmed by a link emailed to it, valid for 7 days.

` + "`mailgoose account`" + ` shows whether the account is in test mode. A test
address's email or id works wherever one is asked for.`,
	}

	list := calls("listTestAddresses", &cobra.Command{
		Use:     "list",
		Short:   "List the test addresses and whether each is confirmed",
		Example: `  mailgoose test-addresses list`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var page testAddressPage
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/test-addresses", nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No test addresses. Add one with `mailgoose test-addresses add <email>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tEMAIL\tSTATUS")
			for _, t := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", t.ID, t.Email, testAddressState(t))
			}
			return tw.Flush()
		},
	})

	add := calls("addTestAddress", &cobra.Command{
		Use:   "add <email>",
		Short: "Add a test address and email it a confirm link",
		Long: `Adds a test address and emails it a link to confirm it. It's pending until
someone opens the link; only confirmed addresses get sends. The owner's own
sign-in address needs no slot: test mode always sends to it.`,
		Example: `  mailgoose test-addresses add qa@example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var t testAddress
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/test-addresses", map[string]string{"email": args[0]}, &t, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(t)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Added %s (%s). A confirm link went to it; it gets sends once someone opens the link.\n", t.Email, t.ID)
			return nil
		},
	})

	resend := callsAll([]string{"resendTestAddress", "listTestAddresses"}, &cobra.Command{
		Use:     "resend <test-address>",
		Short:   "Email a pending test address a new confirm link",
		Long:    `Emails a pending test address a new confirm link, valid for 7 days; the old link stops working. At most once a minute.`,
		Example: `  mailgoose test-addresses resend qa@example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.testAddressID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var t testAddress
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/test-addresses/"+url.PathEscape(id)+"/resend", nil, &t, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(t)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Sent %s a new confirm link.\n", t.Email)
			return nil
		},
	})

	remove := callsAll([]string{"removeTestAddress", "listTestAddresses"}, &cobra.Command{
		Use:     "remove <test-address>",
		Aliases: []string{"delete"},
		Short:   "Remove a test address, freeing its slot",
		Long:    `Removes a test address. While the account is in test mode, sends to it stop, including copies of a campaign already sending.`,
		Example: `  mailgoose test-addresses remove qa@example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.testAddressID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.call(cmd.Context(), http.MethodDelete, "/v1/test-addresses/"+url.PathEscape(id), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"id": id, "removed": true})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Removed test address %s.\n", args[0])
			return nil
		},
	})

	confirm := calls("confirmTestAddress", &cobra.Command{
		Use:   "confirm <token>",
		Short: "Confirm a test address with the token from its emailed link",
		Long: `Confirms a test address with the token from its link (the part after
"token="). Opening the link in a browser does the same. Needs no sign-in.`,
		Example: `  mailgoose test-addresses confirm t1Xa...`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Email       string    `json:"email"`
				AccountName string    `json:"account_name"`
				ConfirmedAt time.Time `json:"confirmed_at"`
			}
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/test-addresses/confirm", map[string]string{"token": args[0]}, &out, false); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Confirmed %s as a test address of %s.\n", out.Email, out.AccountName)
			return nil
		},
	})

	cmd.AddCommand(list, add, resend, remove, confirm)
	return cmd
}
