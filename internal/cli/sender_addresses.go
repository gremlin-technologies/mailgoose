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

// Sender addresses (MG-85): the addresses an account may send as from
// mailgoose's shared sending domain, when it has no verified domain of its own.

// senderAddress is the API's SenderAddress.
type senderAddress struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	Status           string     `json:"status"`
	ConfirmSentAt    *time.Time `json:"confirm_sent_at"`
	ConfirmExpiresAt *time.Time `json:"confirm_expires_at"`
	ConfirmedAt      *time.Time `json:"confirmed_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

type senderAddressPage struct {
	Data       []senderAddress `json:"data"`
	NextCursor *string         `json:"next_cursor"`
}

// senderAddressID turns a sender address's email or id into its id, looking the
// email up.
func (a *App) senderAddressID(ctx context.Context, arg string) (string, error) {
	if domainIDPattern.MatchString(arg) {
		return arg, nil
	}
	var page senderAddressPage
	if err := a.call(ctx, http.MethodGet, "/v1/sender-addresses", nil, &page, true); err != nil {
		return "", err
	}
	for _, t := range page.Data {
		if strings.EqualFold(t.Email, strings.TrimSpace(arg)) {
			return t.ID, nil
		}
	}
	return "", &Error{Exit: ExitNotFound, Code: "not_found", Message: fmt.Sprintf("This account has no sender address %q.", arg),
		Hint: "`mailgoose sender-addresses list` shows them."}
}

func senderAddressState(t senderAddress) string {
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

func (a *App) senderAddressesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sender-addresses",
		Aliases: []string{"sender-address"},
		Short:   "Manage the addresses you send as when you have no domain of your own",
		Long: `A domain of your own is optional. Where the server has shared sending on
(` + "`mailgoose account get`" + ` shows a shared From), an email whose From
isn't on a verified domain of the account still sends if its From is a
confirmed sender address or your own sign-in address, which always counts.
It goes from the account's address on mailgoose's shared domain, with
replies to your address. Your own domain is better for deliverability and
trust: ` + "`mailgoose domain add <domain>`" + `.

An account can have up to 10 sender addresses, pending ones included. Each
is confirmed by a link emailed to it, valid for 7 days. A sender address's
email or id works wherever one is asked for.`,
	}

	list := calls("listSenderAddresses", &cobra.Command{
		Use:     "list",
		Short:   "List the sender addresses and whether each is confirmed",
		Example: `  mailgoose sender-addresses list`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var page senderAddressPage
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/sender-addresses", nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No sender addresses. Your sign-in address always counts; add another with `mailgoose sender-addresses add <email>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tEMAIL\tSTATUS")
			for _, t := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", t.ID, t.Email, senderAddressState(t))
			}
			return tw.Flush()
		},
	})

	add := calls("addSenderAddress", &cobra.Command{
		Use:   "add <email>",
		Short: "Add a sender address and email it a confirm link",
		Long: `Adds a sender address and emails it a link to confirm you own it. It's
pending until someone opens the link; only a confirmed address can send. Your
own sign-in address needs no slot: it always counts.`,
		Example: `  mailgoose sender-addresses add pat@example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var t senderAddress
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/sender-addresses", map[string]string{"email": args[0]}, &t, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(t)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Added %s (%s). A confirm link went to it; it can send once someone opens the link.\n", t.Email, t.ID)
			return nil
		},
	})

	resend := callsAll([]string{"resendSenderAddress", "listSenderAddresses"}, &cobra.Command{
		Use:     "resend <sender-address>",
		Short:   "Email a pending sender address a new confirm link",
		Long:    `Emails a pending sender address a new confirm link, valid for 7 days; the old link stops working. At most once a minute.`,
		Example: `  mailgoose sender-addresses resend pat@example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.senderAddressID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var t senderAddress
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/sender-addresses/"+url.PathEscape(id)+"/resend", nil, &t, true); err != nil {
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

	remove := callsAll([]string{"removeSenderAddress", "listSenderAddresses"}, &cobra.Command{
		Use:     "remove <sender-address>",
		Aliases: []string{"delete"},
		Short:   "Remove a sender address, freeing its slot",
		Long:    `Removes a sender address. Emails can no longer go from it on the shared domain, including copies of a campaign already sending.`,
		Example: `  mailgoose sender-addresses remove pat@example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.senderAddressID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.call(cmd.Context(), http.MethodDelete, "/v1/sender-addresses/"+url.PathEscape(id), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"id": id, "removed": true})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Removed sender address %s.\n", args[0])
			return nil
		},
	})

	confirm := calls("confirmSenderAddress", &cobra.Command{
		Use:   "confirm <token>",
		Short: "Confirm a sender address with the token from its emailed link",
		Long: `Confirms a sender address with the token from its link (the part after
"token="). Opening the link in a browser does the same. Needs no sign-in.`,
		Example: `  mailgoose sender-addresses confirm s1Xa...`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Email       string    `json:"email"`
				AccountName string    `json:"account_name"`
				ConfirmedAt time.Time `json:"confirmed_at"`
			}
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/sender-addresses/confirm", map[string]string{"token": args[0]}, &out, false); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Confirmed %s as a sender address of %s.\n", out.Email, out.AccountName)
			return nil
		},
	})

	cmd.AddCommand(list, add, resend, remove, confirm)
	return cmd
}
